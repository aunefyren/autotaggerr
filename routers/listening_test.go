package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
)

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return v
}

// TestListeningAccounts: a user links ListenBrainz (token write-only), sees only their
// own accounts, cannot touch another user's, cannot link Plex by hand unless admin,
// and unlinking forgets the plays.
func TestListeningAccounts(t *testing.T) {
	r, api := setupAPI(t)
	admin := loginToken(t, r)
	viewer := userToken(t, r, api.DB)

	w := do(r, "POST", "/api/v1/listening/accounts", viewer, map[string]any{"source": "listenbrainz", "username": " someone ", "token": "secret-tok"})
	if w.Code != http.StatusCreated {
		t.Fatalf("link: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-tok") {
		t.Errorf("token returned: %s", w.Body.String())
	}
	created := decode[listeningAccountView](t, w)
	if created.Username != "someone" || !created.TokenSet || !created.Enabled {
		t.Errorf("created = %+v", created)
	}
	if w := do(r, "POST", "/api/v1/listening/accounts", viewer, map[string]any{"source": "listenbrainz", "username": "SOMEONE"}); w.Code != http.StatusConflict {
		t.Errorf("duplicate = %d", w.Code)
	}
	for _, body := range []map[string]any{{"source": "listenbrainz"}, {"source": "spotify"}} {
		if w := do(r, "POST", "/api/v1/listening/accounts", viewer, body); w.Code != http.StatusBadRequest {
			t.Errorf("%v = %d, want 400", body, w.Code)
		}
	}

	// Plays for the account, to see them counted and then forgotten.
	viewerID := created.UserID
	l := models.Listen{UserID: viewerID, AccountID: created.ID, Source: "listenbrainz", ListenedAt: time.Now(), ExternalKey: "k", RecordingMBID: "r"}
	api.DB.Create(&l)
	api.DB.Create(&models.ListenArtist{ListenID: l.ID, ArtistMBID: "a"})
	api.DB.Create(&models.Listen{UserID: viewerID, AccountID: created.ID, Source: "listenbrainz", ListenedAt: time.Now(), ExternalKey: "k2"})

	list := decode[[]listeningAccountView](t, do(r, "GET", "/api/v1/listening/accounts", viewer, nil))
	if len(list) != 1 || list[0].Plays != 2 || list[0].Matched != 1 {
		t.Errorf("viewer's list = %+v", list)
	}
	if adminList := decode[[]listeningAccountView](t, do(r, "GET", "/api/v1/listening/accounts", admin, nil)); len(adminList) != 0 {
		t.Errorf("admin sees another user's accounts: %+v", adminList)
	}
	path := "/api/v1/listening/accounts/" + created.ID.String()
	for _, call := range []struct{ method, path string }{{"PUT", path}, {"DELETE", path}, {"POST", path + "/test"}} {
		if w := do(r, call.method, call.path, admin, map[string]any{"enabled": false}); w.Code != http.StatusNotFound {
			t.Errorf("%s by another user = %d, want 404", call.method, w.Code)
		}
	}
	if w := do(r, "PUT", "/api/v1/listening/accounts/nope", viewer, nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d", w.Code)
	}

	// Update: switch off, keep the token when absent, clear it when empty.
	w = do(r, "PUT", path, viewer, map[string]any{"enabled": false})
	if v := decode[listeningAccountView](t, w); v.Enabled || !v.TokenSet {
		t.Errorf("after switching off = %+v", v)
	}
	w = do(r, "PUT", path, viewer, map[string]any{"token": ""})
	if v := decode[listeningAccountView](t, w); v.TokenSet {
		t.Errorf("token not cleared: %+v", v)
	}
	if w := do(r, "PUT", path, viewer, map[string]any{"username": " "}); w.Code != http.StatusBadRequest {
		t.Errorf("blank user name = %d", w.Code)
	}

	// Plex by hand: refused for a non-admin, validated for an admin.
	server := models.MediaServer{Name: "Plex", Type: models.MediaServerTypePlex, Enabled: true, BaseURL: "http://127.0.0.1:1", Token: "t"}
	api.DB.Create(&server)
	plexBody := map[string]any{"source": "plex", "media_server_id": server.ID.String(), "plex_account_id": "1", "plex_account_name": "owner"}
	if w := do(r, "POST", "/api/v1/listening/accounts", viewer, plexBody); w.Code != http.StatusForbidden {
		t.Errorf("non-admin plex link = %d, want 403", w.Code)
	}
	if w := do(r, "POST", "/api/v1/listening/accounts", admin, map[string]any{"source": "plex", "media_server_id": uuid.NewString(), "plex_account_id": "1"}); w.Code != http.StatusBadRequest {
		t.Errorf("plex link to no server = %d", w.Code)
	}
	if w := do(r, "POST", "/api/v1/listening/accounts", admin, plexBody); w.Code != http.StatusCreated {
		t.Errorf("admin plex link = %d %s", w.Code, w.Body.String())
	}
	if w := do(r, "POST", "/api/v1/listening/accounts", admin, plexBody); w.Code != http.StatusConflict {
		t.Errorf("plex linked twice = %d", w.Code)
	}
	adminList := decode[[]listeningAccountView](t, do(r, "GET", "/api/v1/listening/accounts", admin, nil))
	if len(adminList) != 1 || adminList[0].ServerName != "Plex" {
		t.Errorf("admin list = %+v", adminList)
	}
	// Test: the server is unreachable, said in the body.
	w = do(r, "POST", "/api/v1/listening/accounts/"+adminList[0].ID.String()+"/test", admin, nil)
	if v := decode[map[string]any](t, w); v["healthy"] != false {
		t.Errorf("test of an unreachable server = %v", v)
	}

	if w := do(r, "POST", "/api/v1/listening/sync", viewer, nil); w.Code != http.StatusAccepted {
		t.Errorf("sync = %d", w.Code)
	}

	// Renaming the ListenBrainz user forgets the old user's plays.
	do(r, "PUT", path, viewer, map[string]any{"username": "someone-else"})
	var n int64
	api.DB.Model(&models.Listen{}).Where("account_id = ?", created.ID).Count(&n)
	if n != 0 {
		t.Errorf("plays kept after a rename: %d", n)
	}
	api.DB.Create(&models.Listen{UserID: viewerID, AccountID: created.ID, Source: "listenbrainz", ListenedAt: time.Now(), ExternalKey: "k3"})

	if w := do(r, "DELETE", path, viewer, nil); w.Code != http.StatusNoContent {
		t.Fatalf("unlink = %d", w.Code)
	}
	api.DB.Model(&models.Listen{}).Where("account_id = ?", created.ID).Count(&n)
	var artists int64
	api.DB.Model(&models.ListenArtist{}).Count(&artists)
	if n != 0 || artists != 0 {
		t.Errorf("after unlink: %d plays, %d artists", n, artists)
	}
}

// TestListenBrainzAccountTest: the test route asks ListenBrainz.
func TestListenBrainzAccountTest(t *testing.T) {
	r, api := setupAPI(t)
	admin := loginToken(t, r)
	lb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"payload":{"listens":[]}}`)
	}))
	defer lb.Close()
	old := modules.ListenBrainzBaseURL
	modules.ListenBrainzBaseURL = lb.URL
	t.Cleanup(func() { modules.ListenBrainzBaseURL = old })
	_ = api
	created := decode[listeningAccountView](t, do(r, "POST", "/api/v1/listening/accounts", admin, map[string]any{"source": "listenbrainz", "username": "u"}))
	w := do(r, "POST", "/api/v1/listening/accounts/"+created.ID.String()+"/test", admin, nil)
	if v := decode[map[string]any](t, w); v["healthy"] != true {
		t.Errorf("test = %v", v)
	}
}

// TestPlexPinFlow: a pin is bound to whoever started it; once approved it links the
// signed-in Plex account on each server it can reach.
func TestPlexPinFlow(t *testing.T) {
	r, api := setupAPI(t)
	admin := loginToken(t, r)
	viewer := userToken(t, r, api.DB)

	approved := false
	tv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v2/pins":
			fmt.Fprint(w, `{"id":77,"code":"c0de"}`)
		case r.URL.Path == "/api/v2/pins/77":
			if approved {
				fmt.Fprint(w, `{"id":77,"authToken":"usertok"}`)
			} else {
				fmt.Fprint(w, `{"id":77}`)
			}
		case r.URL.Path == "/api/v2/user":
			fmt.Fprint(w, `{"id":7,"username":"friend"}`)
		case r.URL.Path == "/api/v2/resources":
			fmt.Fprint(w, `[]`)
		}
	}))
	defer tv.Close()
	old := modules.PlexTVBaseURL
	modules.PlexTVBaseURL = tv.URL
	t.Cleanup(func() { modules.PlexTVBaseURL = old })

	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity":
			fmt.Fprint(w, `<MediaContainer machineIdentifier="m1"/>`)
		case "/accounts":
			fmt.Fprint(w, `<MediaContainer><Account id="1" name="owner"/><Account id="7" name="friend"/></MediaContainer>`)
		}
	}))
	defer plex.Close()
	server := models.MediaServer{Name: "Home", Type: models.MediaServerTypePlex, Enabled: true, BaseURL: plex.URL, Token: "t"}
	api.DB.Create(&server)

	// Admin lists the server's accounts.
	accounts := decode[map[string]any](t, do(r, "GET", "/api/v1/media-servers/"+server.ID.String()+"/accounts", admin, nil))
	if list, _ := accounts["accounts"].([]any); len(list) != 2 {
		t.Errorf("accounts = %v", accounts)
	}
	if w := do(r, "GET", "/api/v1/media-servers/"+server.ID.String()+"/accounts", viewer, nil); w.Code != http.StatusForbidden {
		t.Errorf("non-admin account list = %d", w.Code)
	}

	w := do(r, "POST", "/api/v1/listening/plex/pin", viewer, nil)
	pin := decode[map[string]any](t, w)
	if pin["id"] != float64(77) || !strings.Contains(pin["auth_url"].(string), "c0de") {
		t.Fatalf("pin = %v", pin)
	}
	if w := do(r, "POST", "/api/v1/listening/plex/pin/77/claim", admin, nil); w.Code != http.StatusNotFound {
		t.Errorf("claim by another user = %d, want 404", w.Code)
	}
	if w := do(r, "POST", "/api/v1/listening/plex/pin/x/claim", viewer, nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad pin id = %d", w.Code)
	}
	if w := do(r, "POST", "/api/v1/listening/plex/pin/77/claim", viewer, nil); w.Code != http.StatusAccepted {
		t.Errorf("waiting pin = %d", w.Code)
	}
	approved = true
	w = do(r, "POST", "/api/v1/listening/plex/pin/77/claim", viewer, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"linked":true`) {
		t.Fatalf("claim = %d %s", w.Code, w.Body.String())
	}
	list := decode[[]listeningAccountView](t, do(r, "GET", "/api/v1/listening/accounts", viewer, nil))
	if len(list) != 1 || list[0].PlexAccountID != "7" || list[0].PlexAccountName != "friend" {
		t.Errorf("linked = %+v", list)
	}
	// The pin is spent.
	if w := do(r, "POST", "/api/v1/listening/plex/pin/77/claim", viewer, nil); w.Code != http.StatusNotFound {
		t.Errorf("reused pin = %d", w.Code)
	}
}

// TestGraphListeningParams: the overlay's parameters are validated, and the overlay
// is the signed-in user's.
func TestGraphListeningParams(t *testing.T) {
	r, api := setupAPI(t)
	admin := loginToken(t, r)
	for _, q := range []string{"edges=sideways", "since=yesterday", "since=0d", "min_plays=-1"} {
		if w := do(r, "GET", "/api/v1/insights/graph?"+q, admin, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, w.Code)
		}
	}
	w := do(r, "GET", "/api/v1/insights/graph?since=30d&edges=both&listened=1", admin, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"listening"`) {
		t.Errorf("no account linked = %d %s", w.Code, w.Body.String())
	}
	do(r, "POST", "/api/v1/listening/accounts", admin, map[string]any{"source": "listenbrainz", "username": "u"})
	_ = api
	w = do(r, "GET", "/api/v1/insights/graph?since=all", admin, nil)
	if !strings.Contains(w.Body.String(), `"listening":{`) {
		t.Errorf("linked account, no summary: %s", w.Body.String())
	}
}

func TestParsePeriod(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	if got, err := parsePeriod("30d", now); err != nil || !got.Equal(now.AddDate(0, 0, -30)) {
		t.Errorf("30d = %v, %v", got, err)
	}
	for _, all := range []string{"", "all"} {
		if got, err := parsePeriod(all, now); err != nil || !got.IsZero() {
			t.Errorf("%q = %v, %v", all, got, err)
		}
	}
	if _, err := parsePeriod("30", now); err == nil {
		t.Error("days without a unit passed")
	}
}
