package mirror

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// fakeMeta answers the two lookups a pass makes through the metadata port (the
// artist entity and a release-group's editions). Discographies and release payloads
// go through modules directly and are stubbed over HTTP instead; see stubMB.
type fakeMeta struct {
	artist   func(string) (models.MusicBrainzArtistLookup, error)
	editions func(string) ([]models.MusicBrainzReleaseSearchResult, error)
}

func (f fakeMeta) GetRelease(string) (models.MusicBrainzReleaseResponse, error) {
	return models.MusicBrainzReleaseResponse{}, nil
}

func (f fakeMeta) GetArtist(id string) (models.MusicBrainzArtistLookup, error) {
	if f.artist != nil {
		return f.artist(id)
	}
	return models.MusicBrainzArtistLookup{ID: id}, nil
}

func (f fakeMeta) GetArtistReleaseGroups(string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
	return nil, false, nil
}

func (f fakeMeta) GetReleaseGroupReleases(id string) ([]models.MusicBrainzReleaseSearchResult, error) {
	if f.editions != nil {
		return f.editions(id)
	}
	return nil, nil
}

func (f fakeMeta) SearchReleases(metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
	return metadata.ReleaseSearchPage{}, nil
}

func (f fakeMeta) SearchArtists(string) ([]models.MusicBrainzArtistSearchResult, error) {
	return nil, nil
}

// stubMB stands MusicBrainz up for the lookups that bypass the metadata port. Release
// "rel-gone" is a 404, "rel-bad" a non-transient 400, and every other release answers
// with release-group "rg-new" and a title that changes per request — so a second read
// of the same ID is always a content change. Artist "a-bad"'s discography is a 400.
// The rate limit is lifted for the test so a pass does not sleep a second per entity.
func stubMB(t *testing.T) *int32 {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(req.URL.Path, "/release-group"):
			if req.URL.Query().Get("artist") == "a-bad" {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"release-group-count":0,"release-groups":[]}`)
		case req.URL.Path == "/release/rel-gone":
			http.Error(w, "not found", http.StatusNotFound)
		case req.URL.Path == "/release/rel-bad":
			http.Error(w, "bad request", http.StatusBadRequest)
		case strings.HasPrefix(req.URL.Path, "/release/"):
			id := strings.TrimPrefix(req.URL.Path, "/release/")
			fmt.Fprintf(w, `{"id":%q,"title":"Take %d","release-group":{"id":"rg-new"}}`, id, n)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	modules.SetMusicBrainzBaseURLForTest(t, srv.URL)
	modules.SetMusicBrainzRateLimit(1000)
	t.Cleanup(func() { modules.SetMusicBrainzRateLimit(1) })
	return &hits
}

func phaseStat(t *testing.T, res Result, phase string) PhaseStat {
	t.Helper()
	stat, ok := res.Phases[phase]
	if !ok {
		t.Fatalf("no %s phase in %+v", phase, res.Phases)
	}
	return stat
}

func itemStatus(res Result, mbid string) string {
	for _, item := range res.Items {
		if item.Path == mbid {
			return item.Status
		}
	}
	return ""
}

// One tracked pass over every kind of entity and every outcome a fetch can have. The
// point is the bookkeeping: a gone entity is an answer (fetched, not an error), a
// failed one is counted against its own phase, and the shared Summary agrees with the
// Result the caller is handed.
func TestPassRecordsEveryOutcome(t *testing.T) {
	stubMB(t)
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})
	r.meta = fakeMeta{
		artist: func(id string) (models.MusicBrainzArtistLookup, error) {
			switch id {
			case "a-gone":
				return models.MusicBrainzArtistLookup{}, &modules.GoneError{EntityType: models.MigrationEntityArtist, MBID: id, Status: 404}
			case "a-bad":
				return models.MusicBrainzArtistLookup{}, errors.New("artist lookup exploded")
			}
			return models.MusicBrainzArtistLookup{ID: id}, nil
		},
		editions: func(id string) ([]models.MusicBrainzReleaseSearchResult, error) {
			if id == "rg-bad" {
				return nil, errors.New("editions exploded")
			}
			return nil, nil
		},
	}

	scope := Scope{
		Title:    "Metadata refresh",
		Artists:  []string{"a-ok", "a-gone", "a-bad"},
		Groups:   []string{"rg-ok", "rg-bad"},
		Releases: []string{"rel-ok", "rel-gone", "rel-bad"},
		Cold:     map[string]bool{"rel-ok": true},
	}
	res, err := r.Run(context.Background(), scope)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Checked != scope.size() {
		t.Errorf("checked = %d, want %d", res.Checked, scope.size())
	}
	// a-bad fails as an entity, its discography fails at the stub, rg-bad and rel-bad.
	if res.Errors != 4 {
		t.Errorf("errors = %d, want 4: %+v", res.Errors, res.Items)
	}
	if res.GoneReleases != 1 {
		t.Errorf("gone releases = %d, want 1", res.GoneReleases)
	}
	if s := phaseStat(t, res, PhaseArtists); s.Checked != 3 || s.Errors != 1 || s.Fetched != 2 {
		t.Errorf("artist phase = %+v, want 3 checked / 2 fetched (gone counts) / 1 error", s)
	}
	if s := phaseStat(t, res, PhaseDiscographies); s.Errors != 1 {
		t.Errorf("discography phase = %+v, want 1 error", s)
	}
	if s := phaseStat(t, res, PhaseReleases); s.Errors != 1 || s.Fetched != 2 {
		t.Errorf("release phase = %+v, want 2 fetched / 1 error", s)
	}

	for mbid, want := range map[string]string{
		"a-gone":   models.EventItemStatusGone,
		"a-bad":    models.EventItemStatusError,
		"rg-bad":   models.EventItemStatusError,
		"rel-gone": models.EventItemStatusGone,
		"rel-bad":  models.EventItemStatusError,
	} {
		if got := itemStatus(res, mbid); got != want {
			t.Errorf("%s detail status = %q, want %q", mbid, got, want)
		}
	}
	if !strings.Contains(res.LastError, "rel-bad") {
		t.Errorf("last error = %q, want the last failure (rel-bad)", res.LastError)
	}

	status := r.Status()
	if status.Errors != res.Errors || status.GoneReleases != 1 || status.Done != scope.size() {
		t.Errorf("summary %+v disagrees with result %+v", status, res)
	}
	if status.LastError != res.LastError {
		t.Errorf("summary last error = %q, want %q", status.LastError, res.LastError)
	}
}

// A release read twice with different content is reported as changed upstream, and
// one whose payload names a different release-group is re-linked in the collection.
// Both are reported, not acted on: nothing here writes files.
func TestForcedPassReportsChangedAndRelinkedReleases(t *testing.T) {
	stubMB(t)
	db := testDB(t)
	if err := db.Create(&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-old"}).Error; err != nil {
		t.Fatalf("seed release: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{})
	r.meta = fakeMeta{}

	// The first read has nothing to compare against, so it cannot be a change — but it
	// does see the release sitting in a different group.
	first, err := r.Run(context.Background(), Scope{Releases: []string{"rel-1"}})
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if len(first.ChangedReleases) != 0 {
		t.Errorf("a first read reported a change: %+v", first.ChangedReleases)
	}
	if first.Relinked != 1 || itemStatus(first, "rel-1") != models.EventItemStatusRelinked {
		t.Errorf("relinked = %d / %q, want the release re-linked", first.Relinked, itemStatus(first, "rel-1"))
	}
	var row models.CollectionRelease
	if err := db.First(&row, "mb_id = ?", "rel-1").Error; err != nil {
		t.Fatalf("reload release: %v", err)
	}
	if row.ReleaseGroupMBID != "rg-new" {
		t.Errorf("release-group = %q, want rg-new", row.ReleaseGroupMBID)
	}

	// Forced, so the fresh copy is re-read; the stub's title differs on every request.
	second, err := r.Run(context.Background(), Scope{Releases: []string{"rel-1"}, Artists: []string{"a1"}, Groups: []string{"rg-new"}, Force: true})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(second.ChangedReleases) != 1 || second.ChangedReleases[0] != "rel-1" {
		t.Errorf("changed = %+v, want rel-1", second.ChangedReleases)
	}
	if itemStatus(second, "rel-1") != models.EventItemStatusRefreshed {
		t.Errorf("detail status = %q, want refreshed", itemStatus(second, "rel-1"))
	}
	if got := r.Status().ChangedReleases; got != 1 {
		t.Errorf("summary changed releases = %d, want 1", got)
	}
}

// A re-link that cannot be written is logged, and the fetch still counts as a success
// — the release was read; only the bookkeeping about its group failed.
func TestRelinkFailureDoesNotFailTheFetch(t *testing.T) {
	stubMB(t)
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})
	r.meta = fakeMeta{}
	if err := db.Migrator().DropTable(&models.CollectionRelease{}); err != nil {
		t.Fatalf("drop: %v", err)
	}

	res := r.RunInline(context.Background(), Scope{Releases: []string{"rel-1"}})
	if res.Fetched != 1 || res.Errors != 0 || res.Relinked != 0 {
		t.Errorf("result = %+v, want one clean fetch and no re-link", res)
	}
}

// RunStage is the scan's refresh stage: its own event, parented under the run, and
// it leaves the shared Summary alone because the run is publishing its own progress.
func TestRunStageRecordsAChildEvent(t *testing.T) {
	stubMB(t)
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})
	r.meta = fakeMeta{}

	parent := events.Begin(db, models.EventTypeProcess, "Processing")
	res := r.RunStage(context.Background(), Scope{Title: "Metadata refresh", Releases: []string{"rel-1"}}, parent)
	if res.Fetched != 1 {
		t.Fatalf("result = %+v, want one fetch", res)
	}

	var stage models.Event
	if err := db.Where("type = ? AND parent_id = ?", models.EventTypeMirror, parent.ID).First(&stage).Error; err != nil {
		t.Fatalf("load stage event: %v", err)
	}
	if stage.FinishedAt == nil || stage.Status != models.EventStatusOK {
		t.Errorf("stage = %+v, want a finished ok event", stage)
	}
	if got := r.Status(); got.Done != 0 || got.Running {
		t.Errorf("a stage touched the shared summary: %+v", got)
	}
}

// Start reports nothing back once the pass is under way; a pass that hit errors still
// finishes and releases the guard.
func TestStartRunsAPassToCompletion(t *testing.T) {
	stubMB(t)
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})
	r.meta = fakeMeta{}

	if err := r.Start(context.Background(), Scope{Title: "Metadata refresh", Releases: []string{"rel-bad"}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitUntil(t, func() bool { return !r.Running() })
	if got := r.Status(); got.Errors != 1 {
		t.Errorf("errors = %d, want 1", got.Errors)
	}
}

// A pass drains the migration queue at its boundary, and a queue it cannot read is
// logged rather than failing the pass that found the redirects.
func TestPassSurvivesAnUnreadableMigrationQueue(t *testing.T) {
	db := testDB(t)
	original := files.ConfigFile
	files.ConfigFile = models.ConfigStruct{}
	t.Cleanup(func() { files.ConfigFile = original })
	if err := db.Migrator().DropTable(&models.MusicbrainzMigration{}); err != nil {
		t.Fatalf("drop: %v", err)
	}

	NewRunner(db, nil, models.ConfigStruct{}).applyMigrations()
	(&Runner{}).applyMigrations() // no database is a no-op
}

// Every scope constructor hands a read failure back to its caller rather than
// returning a partial scope that would refresh some arbitrary subset.
func TestScopesReportReadFailures(t *testing.T) {
	seed := func(t *testing.T) (*gorm.DB, models.Library) {
		db := testDB(t)
		lib := models.Library{Name: "L", Path: "/m"}
		if err := db.Create(&lib).Error; err != nil {
			t.Fatalf("library: %v", err)
		}
		if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: "/m/a.flac", MBReleaseID: "rel-1"}).Error; err != nil {
			t.Fatalf("item: %v", err)
		}
		if err := db.Create(&models.CollectionArtist{MBID: "a1", Name: "Artist"}).Error; err != nil {
			t.Fatalf("artist: %v", err)
		}
		return db, lib
	}
	drop := func(t *testing.T, db *gorm.DB, model any) {
		if err := db.Migrator().DropTable(model); err != nil {
			t.Fatalf("drop: %v", err)
		}
	}

	// Which scopes read which table: a scope that never touches a table must not be
	// made to fail by its absence, so the expectations are per constructor.
	cases := []struct {
		name                        string
		model                       any
		collection, library, artist bool
	}{
		{"migrations", &models.MusicbrainzMigration{}, true, true, true},
		{"release groups", &models.CollectionReleaseGroup{}, true, false, true},
		{"releases", &models.CollectionRelease{}, true, true, true},
		{"library items", &models.LibraryItem{}, true, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, lib := seed(t)
			drop(t, db, c.model)

			if _, err := CollectionScope(db, false); (err != nil) != c.collection {
				t.Errorf("CollectionScope err = %v, want failure %v", err, c.collection)
			}
			if _, err := LibraryScope(db, lib.ID); (err != nil) != c.library {
				t.Errorf("LibraryScope err = %v, want failure %v", err, c.library)
			}
			if _, err := ArtistScope(db, "a1", false); (err != nil) != c.artist {
				t.Errorf("ArtistScope err = %v, want failure %v", err, c.artist)
			}
		})
	}

	t.Run("collection refresh", func(t *testing.T) {
		db, _ := seed(t)
		drop(t, db, &models.MusicbrainzMigration{})
		if err := NewRunner(db, nil, models.ConfigStruct{}).RunCollection(context.Background(), false); err == nil {
			t.Error("RunCollection started a pass over a scope it could not build")
		}
	})
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
