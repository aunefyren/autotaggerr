package process

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/migration"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// dropTable removes one table from under a runner, so the query that reads it fails
// while everything before it still works. It is the narrowest way to reach the
// "log and carry on" branches every background verb has around its reads.
func dropTable(t *testing.T, db *gorm.DB, model any) {
	t.Helper()
	if err := db.Migrator().DropTable(model); err != nil {
		t.Fatalf("drop table: %v", err)
	}
}

// closeDB makes every query fail, for the branches that guard the very first read.
func closeDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// eventsOfType loads every event of one type, newest last.
func eventsOfType(t *testing.T, db *gorm.DB, typ string) []models.Event {
	t.Helper()
	var evs []models.Event
	if err := db.Where("type = ?", typ).Order("started_at").Find(&evs).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}
	return evs
}

// Every kind is classified exactly once. aheadOfMetadata decides queue priority and
// metadataRefresh decides whose progress Status reads, so a kind in both — or a
// file-writing kind read as a refresh — would show the wrong bar or jump the queue.
func TestJobKindClassification(t *testing.T) {
	cases := []struct {
		kind    jobKind
		writes  bool
		ahead   bool
		refresh bool
	}{
		{jobProcessAll, true, true, false},
		{jobProcessLibrary, true, true, false},
		{jobProcessArtist, true, true, false},
		{jobRetagAll, true, true, false},
		{jobRetagLibrary, true, true, false},
		{jobRetagArtist, true, true, false},
		{jobForceRecorrelate, true, true, false},
		{jobRefreshAll, false, false, true},
		{jobRefreshVerify, false, false, true},
		{jobRefreshArtist, false, false, true},
		{jobRefreshLibrary, false, false, true},
		{jobRepairArtist, false, false, false},
		// Discovery writes the index, not audio, but is ordered like file work.
		{jobDiscoverAll, false, true, false},
		{jobDiscoverArtist, false, true, false},
	}
	for _, c := range cases {
		if got := c.kind.fileWriting(); got != c.writes {
			t.Errorf("%s.fileWriting() = %v, want %v", c.kind, got, c.writes)
		}
		if got := c.kind.aheadOfMetadata(); got != c.ahead {
			t.Errorf("%s.aheadOfMetadata() = %v, want %v", c.kind, got, c.ahead)
		}
		if got := c.kind.metadataRefresh(); got != c.refresh {
			t.Errorf("%s.metadataRefresh() = %v, want %v", c.kind, got, c.refresh)
		}
	}
}

// A panicking job is contained so the worker survives it and drains what is queued
// behind. Without the recover one bad run would freeze the whole queue.
func TestQueueSurvivesAPanickingJob(t *testing.T) {
	r := newQueueRunner()
	ran := make(chan struct{})
	r.enqueue(job{jobRetagAll, "boom", "boom", func() { panic("boom") }})
	r.enqueue(job{jobRetagAll, "after", "after", func() { close(ran) }})
	waitFor(t, func() bool {
		select {
		case <-ran:
			return true
		default:
			return false
		}
	}, "the job queued behind a panic never ran")
}

// While a metadata job is the one in flight, Status reports the mirror runner's
// entity counters rather than the scan's (stale) file counters.
func TestStatusReadsRefreshProgressDuringAMetadataJob(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})

	r.progTotal.Store(99)
	r.running.Store(true)
	t.Cleanup(func() { r.running.Store(false) })
	r.setStatus(func(s *Summary) { s.CurrentJob = &JobView{Kind: string(jobRefreshAll), Title: "Metadata refresh"} })

	s := r.Status()
	if !s.Running {
		t.Fatal("status should report the running job")
	}
	if s.Total != 0 {
		t.Errorf("total = %d — a refresh job must not show the scan's file counters", s.Total)
	}
}

// Status must always answer. A failed count leaves Indexed at zero rather than
// erroring the endpoint the UI polls.
func TestStatusSurvivesAFailedCount(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})
	dropTable(t, db, &models.LibraryItem{})

	if got := r.Status().Indexed; got != 0 {
		t.Errorf("indexed = %d, want 0 when the count fails", got)
	}
}

// The library list failing to load is logged and nothing runs — not a panic, and not
// a tagging event claiming zero files were processed.
func TestRunAndRetagAllWithUnreadableLibraries(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})
	dropTable(t, db, &models.Library{})

	r.runAllNow()
	r.retagAllNow()

	if n := len(eventsOfType(t, db, models.EventTypeTagFiles)); n != 0 {
		t.Errorf("recorded %d tag-files event(s) for a run that could not load its libraries", n)
	}
}

// With libraries present but no items table, the re-tag verbs stop before opening an
// event: an event over a set of files nobody could read would claim a zero it never
// measured.
func TestRetagVerbsStopWhenItemsCannotBeLoaded(t *testing.T) {
	db := newTestDB(t)
	_, _ = seedArtistWithFile(t, db, t.TempDir())
	var library models.Library
	if err := db.First(&library).Error; err != nil {
		t.Fatalf("load library: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{})
	dropTable(t, db, &models.LibraryItem{})

	r.retagAllNow()
	r.retagLibraryNow(library.ID)
	r.retagArtistNow("artist-1")

	if n := len(eventsOfType(t, db, models.EventTypeTagFiles)); n != 0 {
		t.Errorf("recorded %d tag-files event(s) though no items could be read", n)
	}
}

// The scope resolvers pass a broken index straight back to the caller, so the API
// can answer the request with the error instead of queueing a scan of nothing.
func TestScopesReportAnUnreadableIndex(t *testing.T) {
	db := newTestDB(t)
	seedArtistWithFile(t, db, t.TempDir())
	r := NewRunner(db, nil, models.ConfigStruct{})
	dropTable(t, db, &models.LibraryItem{})

	if _, err := r.ArtistScope("artist-1"); err == nil || errors.Is(err, ErrNothingToProcess) {
		t.Errorf("ArtistScope err = %v, want the index error", err)
	}
	if err := r.RunArtist("artist-1"); err == nil {
		t.Error("RunArtist should refuse when the scope cannot be resolved")
	}
	if _, err := r.ReleaseGroupScope("rg-1"); err == nil || errors.Is(err, ErrNothingToProcess) {
		t.Errorf("ReleaseGroupScope err = %v, want the index error", err)
	}
}

// A release-group with no title falls back to its MBID, so the queue entry and the
// event still name something.
func TestReleaseGroupScopeTitleFallsBackToMBID(t *testing.T) {
	db := newTestDB(t)
	seedArtistWithFile(t, db, t.TempDir())
	if err := db.Model(&models.CollectionReleaseGroup{}).Where("mb_id = ?", "rg-1").Update("title", "").Error; err != nil {
		t.Fatalf("clear title: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{})

	scope, err := r.ReleaseGroupScope("rg-1")
	if err != nil {
		t.Fatalf("ReleaseGroupScope: %v", err)
	}
	if scope.Title != "Processing rg-1" {
		t.Errorf("title = %q, want the MBID fallback", scope.Title)
	}
}

// narrowDue falls back to the full due list when the scope cannot be resolved:
// refreshing too much wastes rate limit, refreshing nothing silently stops drift
// detection.
func TestNarrowDueFallsBackOnAnUnreadableIndex(t *testing.T) {
	db := newTestDB(t)
	lib := models.Library{Name: "L", Path: "/m", Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{})
	dropTable(t, db, &models.LibraryItem{})

	filter := newScopeFilter(Scope{Targets: []Target{{Library: lib, Roots: []string{"/m/Artist"}}}})
	due := []string{"rel-a", "rel-b"}
	if got := r.narrowDue(due, filter); len(got) != 2 {
		t.Errorf("narrowDue = %v, want the whole due list back", got)
	}
}

// One release whose items cannot be loaded is skipped, and the rest of the drift pass
// still counts it as checked — the metadata did change, whatever happened to the files.
func TestRetagReleasesCarriesOnPastAnUnreadableRelease(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})
	dropTable(t, db, &models.LibraryItem{})

	res := r.retagReleases([]string{"rel-1", "rel-2"}, modules.NewAlbumRefreshSet(nil), components.NewDetailCollector(0), scopeFilter{})
	if res.checked != 2 || res.changedReleases != 2 || res.retagged != 0 {
		t.Errorf("result = %+v, want 2 checked / 2 changed / 0 retagged", res)
	}
}

// A file whose library row is gone cannot be re-tagged — there is no tagger to write
// it with — and says so per item rather than abandoning the batch.
func TestRetagItemsReportsAMissingLibrary(t *testing.T) {
	db := newTestDB(t)
	item := models.LibraryItem{LibraryID: uuid.New(), Path: "/m/a.flac", MBReleaseID: "rel-1"}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{})

	results, err := r.RetagItems([]uuid.UUID{item.ID})
	if err != nil {
		t.Fatalf("RetagItems: %v", err)
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("results = %+v, want one per-item error", results)
	}
	evs := eventsOfType(t, db, models.EventTypeTagFiles)
	if len(evs) != 1 || evs[0].Status != models.EventStatusError {
		t.Errorf("events = %+v, want one errored tag-files event", evs)
	}
}

// The refresh verbs log and return when their scope cannot be built; neither may
// start a pass over a scope that does not exist.
func TestRefreshVerbsSkipAnUnresolvableScope(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})

	r.refreshArtistNow("no-such-artist", false)
	r.refreshLibraryNow(uuid.New())

	if n := len(eventsOfType(t, db, models.EventTypeMirror)); n != 0 {
		t.Errorf("recorded %d refresh event(s) for scopes that never resolved", n)
	}
}

// The collection-wide refresh verbs surface a failed scope build as a log line, not a
// panic, and record no pass.
func TestCollectionRefreshVerbsSurviveABrokenDatabase(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})
	closeDB(t, db)

	r.syncDriftNow()
	r.verifyIdentitiesNow()
}

// A force re-correlate over a library whose items cannot be read leaves pins alone
// and moves on to the next target, rather than failing the whole run.
func TestPrepareForceRecorrelateSkipsUnreadableTargets(t *testing.T) {
	db := newTestDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	manager := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: srv.URL, LidarrAPIKey: "k"}
	if err := db.Create(&manager).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	lib := models.Library{Name: "L", Path: t.TempDir(), Enabled: true, ManagerID: &manager.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{})
	dropTable(t, db, &models.LibraryItem{})

	// Must return rather than hang or panic; nothing else is observable with the
	// table gone.
	r.prepareForceRecorrelate(Scope{Targets: []Target{{Library: lib}}})
}

// finishTagging names removals and drift only when they happened, and both carry
// their own counters on the event.
func TestFinishTaggingReportsRemovalsAndDrift(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})
	ev := events.Begin(db, models.EventTypeTagFiles, taggingActivityTitle)

	errorFiles := make([]string, maxErrorFilesRecorded+5)
	for i := range errorFiles {
		errorFiles[i] = filepath.Join("/m", "f", string(rune('a'+i%26)))
	}
	r.finishTagging(ev, taggingResult{
		processed:   10,
		unchanged:   4,
		tagsWritten: 12,
		removed:     2,
		errorFiles:  errorFiles,
		drift:       releaseRefresh{changedReleases: 3, retagged: 5},
	}, components.NewDetailCollector(0))

	var stored models.Event
	if err := db.First(&stored, "id = ?", ev.ID).Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if stored.Status != models.EventStatusError {
		t.Errorf("status = %q, want error", stored.Status)
	}
	for _, want := range []string{"2 removed", "5 re-tagged from 3 release(s) changed upstream"} {
		if !strings.Contains(stored.Summary, want) {
			t.Errorf("summary %q is missing %q", stored.Summary, want)
		}
	}
	if s, ok := statByLabel(stored.Stats, "Changed upstream"); !ok || s.Value != 3 {
		t.Errorf("changed-upstream stat = %+v (present %v), want 3", s, ok)
	}
}

// recordMigrations is the identity stage's own event. Each clause of its summary
// appears only when its count is non-zero, and an error from the drain overrides the
// summary so a reader sees why rather than a row of zeroes.
func TestRecordMigrationsSummaries(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})

	r.recordMigrations(nil, migration.Result{Applied: 1, Resolved: 2, Failed: 1, Errors: []string{"x"}}, nil)
	r.recordMigrations(nil, migration.Result{}, errors.New("database is locked"))

	evs := eventsOfType(t, db, models.EventTypeMigration)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2", len(evs))
	}
	for _, ev := range evs {
		if ev.Status != models.EventStatusError {
			t.Errorf("status = %q for %q, want error", ev.Status, ev.Summary)
		}
	}
	var counted, failed bool
	for _, ev := range evs {
		if strings.Contains(ev.Summary, "2 resolved elsewhere") && strings.Contains(ev.Summary, "1 failed") {
			counted = true
		}
		if ev.Summary == "failed — database is locked" {
			failed = true
		}
	}
	if !counted || !failed {
		t.Errorf("summaries = %q / %q", evs[0].Summary, evs[1].Summary)
	}
}

// A drain that cannot read its queue still records the stage, as an error — the one
// case where silence would hide a real problem.
func TestApplyMigrationsRecordsADrainFailure(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})
	dropTable(t, db, &models.MusicbrainzMigration{})

	original := files.ConfigFile
	files.ConfigFile = models.ConfigStruct{}
	t.Cleanup(func() { files.ConfigFile = original })

	r.applyMigrations(nil)

	evs := eventsOfType(t, db, models.EventTypeMigration)
	if len(evs) != 1 || evs[0].Status != models.EventStatusError || !strings.HasPrefix(evs[0].Summary, "failed") {
		t.Errorf("events = %+v, want one failed identity-change event", evs)
	}
}

// seedGhost records a release-group the collection still lists in its catalog
// although MusicBrainz has confirmed its deletion — what the repair stage acts on.
func seedGhost(t *testing.T, db *gorm.DB, rgMBID, artistMBID string) {
	t.Helper()
	if err := db.Create(&models.CollectionArtist{MBID: artistMBID, Name: "Artist"}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroup{MBID: rgMBID, ArtistMBID: artistMBID, Title: "Ghost", InCatalog: true}).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: rgMBID, ArtistMBID: artistMBID}).Error; err != nil {
		t.Fatalf("credit: %v", err)
	}
	if err := db.Create(&models.MusicbrainzMigration{
		EntityType: models.MigrationEntityReleaseGroup,
		OldMBID:    rgMBID,
		Kind:       models.MigrationKindDeleted,
		Status:     models.MigrationStatusPending,
	}).Error; err != nil {
		t.Fatalf("migration: %v", err)
	}
}

// A repair with candidates records its stage, and a manager that cannot be reached is
// reported on that event as a failure rather than stopping the run.
func TestRepairGhostAlbumsRecordsAnUnreachableManager(t *testing.T) {
	db := newTestDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	if err := db.Create(&models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: srv.URL, LidarrAPIKey: "k"}).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	seedGhost(t, db, "rg-ghost", "artist-1")
	r := NewRunner(db, nil, models.ConfigStruct{})

	stats := r.repairGhostAlbums(nil)
	if stats.Candidates != 1 || len(stats.Failures) != 1 {
		t.Fatalf("stats = %+v, want 1 candidate and 1 failure", stats)
	}

	evs := eventsOfType(t, db, models.EventTypeLidarrSync)
	if len(evs) != 1 || evs[0].Status != models.EventStatusError {
		t.Fatalf("events = %+v, want one errored repair event", evs)
	}
	if !strings.Contains(evs[0].Summary, "1 album(s) with unresolvable IDs") {
		t.Errorf("summary = %q", evs[0].Summary)
	}
}

// A repair whose ghost lookup fails is logged and returns what it had; no event,
// because nothing was attempted.
func TestRepairGhostAlbumsSurvivesAnUnreadableCatalog(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})
	dropTable(t, db, &models.CollectionReleaseGroup{})

	if stats := r.repairGhostAlbums(nil); stats.Candidates != 0 {
		t.Errorf("stats = %+v, want empty", stats)
	}
	if n := len(eventsOfType(t, db, models.EventTypeLidarrSync)); n != 0 {
		t.Errorf("recorded %d repair event(s) for a pass that could not start", n)
	}
}

// The approve button's repair is one parent event, with the summary saying plainly
// when there was nothing left to repair — and a failed manager marks it as an error.
func TestRepairArtistAlbumsNow(t *testing.T) {
	original := files.ConfigFile
	files.ConfigFile = models.ConfigStruct{}
	t.Cleanup(func() { files.ConfigFile = original })

	t.Run("nothing to repair", func(t *testing.T) {
		db := newTestDB(t)
		r := NewRunner(db, nil, models.ConfigStruct{})
		r.repairArtistAlbumsNow("artist-1")

		evs := eventsOfType(t, db, models.EventTypeMigration)
		var parent *models.Event
		for i := range evs {
			if evs[i].Title == "Repair albums via the manager" {
				parent = &evs[i]
			}
		}
		if parent == nil {
			t.Fatalf("no parent repair event among %+v", evs)
		}
		if parent.Summary != "nothing left to repair for this artist" || parent.Status != models.EventStatusOK {
			t.Errorf("parent = %q / %q", parent.Status, parent.Summary)
		}
	})

	t.Run("manager failure", func(t *testing.T) {
		db := newTestDB(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusUnauthorized)
		}))
		t.Cleanup(srv.Close)
		if err := db.Create(&models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: srv.URL, LidarrAPIKey: "k"}).Error; err != nil {
			t.Fatalf("manager: %v", err)
		}
		seedGhost(t, db, "rg-ghost", "artist-1")
		r := NewRunner(db, nil, models.ConfigStruct{})
		r.RepairArtistAlbums("artist-1")
		r.waitIdle(t)

		var parent models.Event
		if err := db.Where("type = ? AND title = ?", models.EventTypeMigration, "Repair albums via the manager").First(&parent).Error; err != nil {
			t.Fatalf("load parent: %v", err)
		}
		if parent.Status != models.EventStatusError {
			t.Errorf("status = %q, want error for a manager that failed", parent.Status)
		}
		if !strings.Contains(parent.Summary, "refreshed") {
			t.Errorf("summary = %q", parent.Summary)
		}
	})

	t.Run("repair marks cannot be cleared", func(t *testing.T) {
		db := newTestDB(t)
		r := NewRunner(db, nil, models.ConfigStruct{})
		dropTable(t, db, &models.MusicbrainzMigration{})
		// The deferred clear fails with the table gone; the verb must still return.
		r.repairArtistAlbumsNow("artist-1")
	})

	t.Run("no database", func(t *testing.T) {
		r := &Runner{}
		r.repairArtistAlbumsNow("artist-1")
		if stats := r.repairGhostAlbumsWith(nil, collection.RepairOptions{}); stats.Candidates != 0 {
			t.Errorf("stats = %+v", stats)
		}
	})
}

// A forced re-correlate clears the manual pins inside its scope on a Lidarr library,
// so the walk asks the manager instead of reusing a hand-chosen match. Pins outside
// the scope are someone else's decision and stay.
func TestPrepareForceRecorrelateClearsPinsInScope(t *testing.T) {
	db := newTestDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	manager := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: srv.URL, LidarrAPIKey: "k"}
	if err := db.Create(&manager).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	root := t.TempDir()
	lib := models.Library{Name: "L", Path: root, Enabled: true, ManagerID: &manager.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	inScope := seedItem(t, db, lib, filepath.Join(root, "Artist", "Album", "01.flac"), true)
	outOfScope := seedItem(t, db, lib, filepath.Join(root, "Other", "Album", "01.flac"), true)

	r := NewRunner(db, nil, models.ConfigStruct{})
	r.prepareForceRecorrelate(Scope{Targets: []Target{{Library: lib, Roots: []string{filepath.Join(root, "Artist")}}}})

	pinned := func(id uuid.UUID) bool {
		var item models.LibraryItem
		if err := db.First(&item, "id = ?", id).Error; err != nil {
			t.Fatalf("reload item: %v", err)
		}
		return item.Pinned
	}
	if pinned(inScope.ID) {
		t.Error("the pin inside the scope survived a forced re-correlate")
	}
	if !pinned(outOfScope.ID) {
		t.Error("a pin outside the scope was cleared")
	}

	// A second pass finds nothing pinned in scope and leaves the rest alone.
	r.prepareForceRecorrelate(Scope{Targets: []Target{{Library: lib, Roots: []string{filepath.Join(root, "Artist")}}}})
	if !pinned(outOfScope.ID) {
		t.Error("a pin outside the scope was cleared on the second pass")
	}
}
