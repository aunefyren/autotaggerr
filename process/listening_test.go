package process

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func reloadAccount(t *testing.T, db *gorm.DB, id uuid.UUID) models.ListeningAccount {
	t.Helper()
	var a models.ListeningAccount
	if err := db.First(&a, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return a
}

// TestSyncListening: ListenBrainz and Plex plays are stored, Plex plays matched to
// the library by path; a second pass reads from the cursor and stores nothing twice;
// a broken account records its error, keeps its cursor and does not stop the others.
func TestSyncListening(t *testing.T) {
	db := newTestDB(t)
	r := newFollowRunner(db)
	lib := models.Library{Name: "music", Path: "/music", Enabled: true}
	db.Create(&lib)
	db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: "/music/Artist/Album (2020)/01 Song.flac",
		MBReleaseID: "rel", MBRecordingID: "rec-plex", Status: models.LibraryItemStatusOK})

	var lbBroken atomic.Bool
	var lbMinTS atomic.Value
	lbMinTS.Store("")
	lb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lbBroken.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		lbMinTS.Store(r.URL.Query().Get("min_ts"))
		fmt.Fprint(w, `{"payload":{"listens":[
			{"listened_at":2000,"recording_msid":"m2","track_metadata":{"track_name":"B","mbid_mapping":{"recording_mbid":"rec-b","artists":[{"artist_mbid":"art-b","artist_credit_name":"B"}]}}},
			{"listened_at":1000,"recording_msid":"m1","track_metadata":{"track_name":"A"}}]}}`)
	}))
	defer lb.Close()
	oldLB := modules.ListenBrainzBaseURL
	modules.ListenBrainzBaseURL = lb.URL
	t.Cleanup(func() { modules.ListenBrainzBaseURL = oldLB })

	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status/sessions/history/all":
			fmt.Fprint(w, `<MediaContainer><Track historyKey="/h/1" ratingKey="5" type="track" title="Song" viewedAt="1500"/></MediaContainer>`)
		case "/library/metadata/5":
			fmt.Fprint(w, `<MediaContainer><Track><Media><Part file="/data/Artist/Album (2020)/01 Song.flac"/></Media></Track></MediaContainer>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer plex.Close()
	server := models.MediaServer{Name: "Plex", Type: models.MediaServerTypePlex, Enabled: true, BaseURL: plex.URL, Token: "t"}
	db.Create(&server)

	user := uuid.New()
	lbAcc := models.ListeningAccount{UserID: user, Source: models.ListeningSourceListenBrainz, Enabled: true, Username: "u"}
	plexAcc := models.ListeningAccount{UserID: user, Source: models.ListeningSourcePlex, Enabled: true, MediaServerID: &server.ID, PlexAccountID: "1"}
	db.Create(&lbAcc)
	db.Create(&plexAcc)

	if err := r.SyncListeningFor(nil); err != nil {
		t.Fatal(err)
	}
	var listens []models.Listen
	db.Order("listened_at").Find(&listens)
	if len(listens) != 3 {
		t.Fatalf("stored %d plays, want 3: %+v", len(listens), listens)
	}
	if listens[1].Source != models.ListeningSourcePlex || listens[1].RecordingMBID != "rec-plex" || !strings.HasSuffix(listens[1].Path, "01 Song.flac") {
		t.Errorf("plex play = %+v", listens[1])
	}
	var artists []models.ListenArtist
	db.Find(&artists)
	if len(artists) != 1 || artists[0].ArtistMBID != "art-b" || artists[0].ListenID != listens[2].ID {
		t.Errorf("artists = %+v", artists)
	}
	got := reloadAccount(t, db, lbAcc.ID)
	if got.Cursor == nil || got.Cursor.Unix() != 2000 || got.LastSyncedAt == nil || got.LastError != "" {
		t.Errorf("lb account after sync = %+v", got)
	}

	// Again: from the cursor, nothing stored twice (the stub answers the same plays).
	if err := r.SyncListeningFor(&user); err != nil {
		t.Fatal(err)
	}
	if lbMinTS.Load() != "2000" {
		t.Errorf("second pass min_ts = %v", lbMinTS.Load())
	}
	var n int64
	db.Model(&models.Listen{}).Count(&n)
	if n != 3 {
		t.Errorf("plays after a second pass = %d, want 3", n)
	}

	// Broken: error recorded, cursor kept, Plex still read.
	lbBroken.Store(true)
	db.Model(&models.ListeningAccount{}).Where("id = ?", plexAcc.ID).Update("cursor", nil)
	if err := r.SyncListeningFor(nil); err != nil {
		t.Fatal(err)
	}
	got = reloadAccount(t, db, lbAcc.ID)
	if got.LastError == "" || got.Cursor == nil || got.Cursor.Unix() != 2000 {
		t.Errorf("broken account = %+v", got)
	}
	if p := reloadAccount(t, db, plexAcc.ID); p.Cursor == nil || p.LastError != "" {
		t.Errorf("plex account after another's failure = %+v", p)
	}

	// Another user's sync touches nobody else.
	other := uuid.New()
	before := reloadAccount(t, db, plexAcc.ID).LastSyncedAt
	time.Sleep(10 * time.Millisecond)
	if err := r.SyncListeningFor(&other); err != nil {
		t.Fatal(err)
	}
	if after := reloadAccount(t, db, plexAcc.ID).LastSyncedAt; !after.Equal(*before) {
		t.Error("a sync for another user read this account")
	}
}

// TestSyncListeningBusy: an overlapping pass is refused, not waited for.
func TestSyncListeningBusy(t *testing.T) {
	r := newFollowRunner(newTestDB(t))
	r.listenMu.Lock()
	defer r.listenMu.Unlock()
	if err := r.SyncListeningFor(nil); err != ErrListeningBusy {
		t.Errorf("overlap = %v", err)
	}
	r.SyncListening() // logs nothing, returns
}

// TestSyncListeningMissingServer: a Plex account whose server is gone records why.
func TestSyncListeningMissingServer(t *testing.T) {
	db := newTestDB(t)
	r := newFollowRunner(db)
	gone := uuid.New()
	acc := models.ListeningAccount{UserID: uuid.New(), Source: models.ListeningSourcePlex, Enabled: true, MediaServerID: &gone, PlexAccountID: "1"}
	odd := models.ListeningAccount{UserID: uuid.New(), Source: "spotify", Enabled: true}
	db.Create(&acc)
	db.Create(&odd)
	r.SyncListening()
	if a := reloadAccount(t, db, acc.ID); !strings.Contains(a.LastError, "removed or disabled") {
		t.Errorf("missing server = %q", a.LastError)
	}
	if a := reloadAccount(t, db, odd.ID); !strings.Contains(a.LastError, "unknown") {
		t.Errorf("unknown source = %q", a.LastError)
	}
}
