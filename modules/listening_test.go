package modules

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

func lbListen(ts int64, msid, recording string, artists ...string) string {
	var as []string
	for _, a := range artists {
		as = append(as, fmt.Sprintf(`{"artist_mbid":%q,"artist_credit_name":"Name %s"}`, a, a))
	}
	mapping := "null"
	if recording != "" {
		mapping = fmt.Sprintf(`{"recording_mbid":%q,"release_mbid":"rel","artists":[%s]}`, recording, strings.Join(as, ","))
	}
	return fmt.Sprintf(`{"listened_at":%d,"recording_msid":%q,"track_metadata":{"artist_name":"A","track_name":"T","release_name":"R","additional_info":{"recording_mbid":"fallback"},"mbid_mapping":%s}}`,
		ts, msid, mapping)
}

// TestListenBrainzListensSince: pages backwards by max_ts until the cursor, maps
// MBIDs from the mapping (falling back to additional_info), sends the token, and
// waits out a 429 once.
func TestListenBrainzListensSince(t *testing.T) {
	var slept atomic.Int64
	listenBrainzSleep = func(d time.Duration) { slept.Add(int64(d)) }
	t.Cleanup(func() { listenBrainzSleep = time.Sleep })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Header.Get("Authorization") != "Token tok" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/1/user/someone/listens" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if n == 1 {
			w.Header().Set("X-RateLimit-Reset-In", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.URL.Query().Get("min_ts") != "100" {
			t.Errorf("min_ts = %q", r.URL.Query().Get("min_ts"))
		}
		// One short page: the newest three, one at the cursor (excluded).
		fmt.Fprintf(w, `{"payload":{"count":3,"listens":[%s,%s,%s]}}`,
			lbListen(300, "m3", "rec3", "a", "b"), lbListen(200, "m2", ""), lbListen(100, "m1", "rec1"))
	}))
	defer srv.Close()

	c := NewListenBrainzClient("someone", "tok")
	c.BaseURL = srv.URL
	var got []ListenBrainzListen
	newest, err := c.ListensSince(time.Unix(100, 0), func(page []ListenBrainzListen) error {
		got = append(got, page...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if slept.Load() != int64(3*time.Second) {
		t.Errorf("slept %v on a 429", time.Duration(slept.Load()))
	}
	if !newest.Equal(time.Unix(300, 0)) || len(got) != 2 {
		t.Fatalf("newest %v, got %+v", newest, got)
	}
	if got[0].RecordingMBID != "rec3" || len(got[0].Artists) != 2 || got[0].Artists[1].Name != "Name b" || got[0].ExternalKey() != "300:m3" {
		t.Errorf("mapped listen = %+v", got[0])
	}
	if got[1].RecordingMBID != "fallback" {
		t.Errorf("unmapped listen = %+v, want additional_info fallback", got[1])
	}
}

// TestListenBrainzPaging: a full page asks for the next one below its oldest listen.
func TestListenBrainzPaging(t *testing.T) {
	var maxes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		maxes = append(maxes, r.URL.Query().Get("max_ts"))
		if len(maxes) == 1 {
			var ls []string
			for i := 0; i < listenBrainzPage; i++ {
				ls = append(ls, lbListen(int64(5000-i), "m"+strconv.Itoa(i), "r"))
			}
			fmt.Fprintf(w, `{"payload":{"listens":[%s]}}`, strings.Join(ls, ","))
			return
		}
		fmt.Fprint(w, `{"payload":{"listens":[]}}`)
	}))
	defer srv.Close()
	c := NewListenBrainzClient("u", "")
	c.BaseURL = srv.URL
	total := 0
	if _, err := c.ListensSince(time.Time{}, func(p []ListenBrainzListen) error { total += len(p); return nil }); err != nil {
		t.Fatal(err)
	}
	if total != listenBrainzPage || len(maxes) != 2 || maxes[1] != strconv.Itoa(5000-listenBrainzPage+1) {
		t.Errorf("total %d, max_ts asked %v", total, maxes)
	}
}

func TestListenBrainzValidate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1/validate-token":
			fmt.Fprint(w, `{"valid":true,"user_name":"Owner"}`)
		case "/1/user/owner/listens":
			fmt.Fprint(w, `{"payload":{"listens":[]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := func(user, token string) *ListenBrainzClient {
		c := NewListenBrainzClient(user, token)
		c.BaseURL = srv.URL
		return c
	}
	if _, err := client("owner", "t").Validate(); err != nil {
		t.Errorf("valid account: %v", err)
	}
	if _, err := client("other", "t").Validate(); err == nil {
		t.Error("a token for another user passed")
	}
	if _, err := client("nobody", "").Validate(); !errors.Is(err, ErrListenBrainzNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	if _, err := client("", "").Validate(); err == nil {
		t.Error("no user name passed")
	}
}

// TestPlexHistory: plays are read oldest first from the cursor, tracks only, keyed by
// history key; the track file is cached; accounts skip the server's own.
func TestPlexHistory(t *testing.T) {
	var fileCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/status/sessions/history/all":
			if r.URL.Query().Get("accountID") != "7" || !strings.Contains(r.URL.RawQuery, "viewedAt>=100") {
				t.Errorf("history query = %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `<MediaContainer size="3">
				<Track historyKey="/h/1" ratingKey="11" type="track" title="T" parentTitle="Al" grandparentTitle="Ar" viewedAt="100"/>
				<Track historyKey="/h/2" ratingKey="12" type="track" title="T2" parentTitle="Al" grandparentTitle="Ar" originalTitle="Guest" viewedAt="250"/>
				<Video historyKey="/h/3" ratingKey="13" type="movie" title="M" viewedAt="300"/>
			</MediaContainer>`)
		case r.URL.Path == "/library/metadata/12":
			fileCalls.Add(1)
			fmt.Fprint(w, `<MediaContainer><Track><Media><Part file="D:\Music\Ar\Al (2020)\02 T2.flac"/></Media></Track></MediaContainer>`)
		case r.URL.Path == "/library/metadata/99":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/accounts":
			fmt.Fprint(w, `<MediaContainer><Account id="0" name=""/><Account id="1" name="owner"/><Account id="7" name="friend"/></MediaContainer>`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	plexTrackFilesMu.Lock()
	plexTrackFiles = map[string]plexTrackFile{}
	plexTrackFilesMu.Unlock()

	p := NewPlexClient(srv.URL, "tok")
	p.ServerID = "s1"
	var plays []PlexPlay
	newest, err := p.History("7", time.Unix(100, 0), func(page []PlexPlay) error { plays = append(plays, page...); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(plays) != 1 || plays[0].HistoryKey != "/h/2" || plays[0].Artist != "Guest" || !newest.Equal(time.Unix(250, 0)) {
		t.Fatalf("plays %+v, newest %v", plays, newest)
	}

	for i := 0; i < 2; i++ {
		file, err := p.TrackFile("12")
		if err != nil || !strings.HasSuffix(file, "02 T2.flac") {
			t.Fatalf("file = %q, %v", file, err)
		}
	}
	if fileCalls.Load() != 1 {
		t.Errorf("track file asked %d times, want 1 (cached)", fileCalls.Load())
	}
	if file, err := p.TrackFile("99"); err != nil || file != "" {
		t.Errorf("gone track = %q, %v", file, err)
	}

	accounts, err := p.Accounts()
	if err != nil || len(accounts) != 2 || accounts[1].Name != "friend" {
		t.Errorf("accounts = %+v, %v", accounts, err)
	}
}

// TestTrailingPathIndex: a Plex path on another mount and OS finds the file by its
// last segments, and a longer suffix tells apart two files sharing three.
func TestTrailingPathIndex(t *testing.T) {
	idx := NewTrailingPathIndex([]models.LibraryItem{
		{Path: "/music/Artist/Album (2020)/01 Song.flac", MBRecordingID: "r1"},
		{Path: "/lossless/A/Artist/Album (2020)/02 Song.flac", MBRecordingID: "r2"},
		{Path: "/lossy/B/Artist/Album (2020)/02 Song.flac", MBRecordingID: "r3"},
	})
	if it, ok := idx.Lookup(`D:\Media\Music\artist\Album (2020)\01 Song.flac`); !ok || it.MBRecordingID != "r1" {
		t.Errorf("windows path = %+v, %v", it, ok)
	}
	if it, ok := idx.Lookup("/data/B/Artist/Album (2020)/02 Song.flac"); !ok || it.MBRecordingID != "r3" {
		t.Errorf("disambiguated = %+v, %v", it, ok)
	}
	if _, ok := idx.Lookup("/x/Artist/Album (2020)/02 Song.flac"); ok {
		t.Error("an ambiguous path matched")
	}
	if _, ok := idx.Lookup("/nowhere/else.flac"); ok {
		t.Error("an unknown path matched")
	}
	if _, ok := idx.Lookup(""); ok {
		t.Error("an empty path matched")
	}
}

// TestPlexSignIn: the pin, the user and the resources come from plex.tv; the owner
// is account 1 on the server, anyone else matches by plex.tv id.
func TestPlexSignIn(t *testing.T) {
	tv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Client-Identifier") == "" {
			t.Error("no client identifier")
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v2/pins":
			fmt.Fprint(w, `{"id":42,"code":"abcd"}`)
		case r.URL.Path == "/api/v2/pins/42":
			fmt.Fprint(w, `{"id":42,"code":"abcd","authToken":"usertok"}`)
		case r.URL.Path == "/api/v2/user":
			if r.Header.Get("X-Plex-Token") != "usertok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"id":7,"username":"friend"}`)
		case r.URL.Path == "/api/v2/resources":
			fmt.Fprint(w, `[{"clientIdentifier":"mine","owned":true},{"clientIdentifier":"shared","owned":false}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer tv.Close()
	old := PlexTVBaseURL
	PlexTVBaseURL = tv.URL
	t.Cleanup(func() { PlexTVBaseURL = old })

	pin, err := CreatePlexPin()
	if err != nil || pin.ID != 42 || !strings.Contains(PlexAuthURL(pin.Code), "code=abcd") {
		t.Fatalf("pin = %+v, %v", pin, err)
	}
	pin, _ = CheckPlexPin(42)
	user, err := PlexTVUser(pin.AuthToken)
	if err != nil || user.ID != 7 {
		t.Fatalf("user = %+v, %v", user, err)
	}
	resources, _ := PlexTVResources(pin.AuthToken)

	server := func(machine string) *PlexClient {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/identity":
				fmt.Fprintf(w, `<MediaContainer machineIdentifier=%q/>`, machine)
			case "/accounts":
				fmt.Fprint(w, `<MediaContainer><Account id="1" name="owner"/><Account id="7" name="friend"/></MediaContainer>`)
			}
		}))
		t.Cleanup(srv.Close)
		return NewPlexClient(srv.URL, "servertok")
	}
	if a, err := server("mine").PlexAccountFor(user, resources); err != nil || a.ID != "1" {
		t.Errorf("owned server = %+v, %v", a, err)
	}
	if a, err := server("shared").PlexAccountFor(user, resources); err != nil || a.ID != "7" {
		t.Errorf("shared server = %+v, %v", a, err)
	}
	if _, err := server("shared").PlexAccountFor(PlexUser{ID: 99}, resources); !errors.Is(err, ErrPlexAccountNotFound) {
		t.Errorf("stranger: %v", err)
	}
}
