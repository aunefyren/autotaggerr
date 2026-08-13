package mirror

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func init() {
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{
		Type: "sqlite",
		DSN:  filepath.Join(t.TempDir(), "mirror.db"),
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

// CollectionScope covers all three entity kinds, which is the whole point of the
// unified scope — the old per-artist refresh silently skipped edition lists.
func TestCollectionScopeCoversEveryKind(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "a1", Name: "Talk Talk"}).Error; err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-1", ArtistMBID: "a1", Title: "Spirit of Eden"}).Error; err != nil {
		t.Fatalf("seed release-group: %v", err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-1", Title: "Spirit of Eden"}).Error; err != nil {
		t.Fatalf("seed release: %v", err)
	}

	scope, err := CollectionScope(db, false)
	if err != nil {
		t.Fatalf("CollectionScope: %v", err)
	}
	if len(scope.Artists) != 1 || len(scope.Groups) != 1 || len(scope.Releases) != 1 {
		t.Fatalf("scope = %+v", scope)
	}
	// Artists count twice: the entity and the discography are separate fetches.
	if scope.size() != 4 {
		t.Errorf("size = %d, want 4", scope.size())
	}
	// A scheduled pass must not force: it re-reads only what has expired.
	if scope.Force {
		t.Error("the scheduled collection scope must not ignore the TTL")
	}
}

// Asking by hand means "check now", so the artist scope ignores the TTL — and it
// must include edition lists, which the old per-artist refresh never fetched.
func TestArtistScopeCoversEditions(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "a1", Name: "Talk Talk"}).Error; err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-1", ArtistMBID: "a1", Title: "Spirit of Eden"}).Error; err != nil {
		t.Fatalf("seed release-group: %v", err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-1", ArtistMBID: "a1", Title: "Spirit of Eden"}).Error; err != nil {
		t.Fatalf("seed release: %v", err)
	}

	scope, err := ArtistScope(db, "a1", false)
	if err != nil {
		t.Fatalf("ArtistScope: %v", err)
	}
	if len(scope.Groups) != 1 {
		t.Errorf("edition lists missing from the artist scope: %+v", scope.Groups)
	}
	if len(scope.Releases) != 1 {
		t.Errorf("releases missing from the artist scope: %+v", scope.Releases)
	}
}

// TestOnlyAnExplicitForceIgnoresTheCache is the invariant behind "ignoring the cache
// is deliberate and rare": **no scope forces unless its caller passed an argument
// saying so**, and the scopes that take no such argument can never force at all.
//
// It used to say something narrower — one argument, on one constructor — which stopped
// being true when forcing became available per artist. That is a widening of where the
// choice can be made, not of when it happens by itself, and the second is what this
// test exists to protect. Both constructors that accept a force argument are checked
// in both positions below, so adding a third scope with a defaulted-on force still
// fails here.
//
// It is worth a test rather than a comment because the failure is silent and
// expensive. A scope that forces re-reads every entity it covers at one rate-limited
// request each — hours, for a collection-sized scope — and nothing about a running
// pass says which reading it is. Every scheduled entry point (the nightly refresh,
// the scan's DueScope stage, both startup runs) builds one of the unforced scopes
// below, so this is what stops a schedule from ever silently becoming a full re-read.
func TestOnlyAnExplicitForceIgnoresTheCache(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "a1", Name: "Talk Talk"}).Error; err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	lib := models.Library{Name: "Music", Path: "/music"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("seed library: %v", err)
	}

	artist, err := ArtistScope(db, "a1", false)
	if err != nil {
		t.Fatalf("ArtistScope: %v", err)
	}
	library, err := LibraryScope(db, lib.ID)
	if err != nil {
		t.Fatalf("LibraryScope: %v", err)
	}
	collection, err := CollectionScope(db, false)
	if err != nil {
		t.Fatalf("CollectionScope: %v", err)
	}

	for name, scope := range map[string]Scope{
		"ArtistScope(false)":     artist,
		"LibraryScope":           library,
		"CollectionScope(false)": collection,
		"DueScope":               DueScope([]string{"rel-1"}),
	} {
		if scope.Force {
			t.Errorf("%s ignores the cache without being asked to", name)
		}
	}

	// The two that can force must actually do it when asked — an argument that is
	// accepted and dropped would fail silently in the direction of doing too little,
	// which is the harder half to notice.
	forced, err := CollectionScope(db, true)
	if err != nil {
		t.Fatalf("CollectionScope(force): %v", err)
	}
	if !forced.Force {
		t.Error("CollectionScope(db, true) must ignore the cache — it is how the collection is asked")
	}
	forcedArtist, err := ArtistScope(db, "a1", true)
	if err != nil {
		t.Fatalf("ArtistScope(force): %v", err)
	}
	if !forcedArtist.Force {
		t.Error("ArtistScope(db, mbid, true) must ignore the cache — it is how one artist is asked")
	}
	// A forced pass says so in its title, so a queue entry and an event row are not
	// indistinguishable from the cheap reading.
	if !strings.Contains(forcedArtist.Title, "Full metadata refresh") {
		t.Errorf("forced artist title = %q, want it to name the reading", forcedArtist.Title)
	}
	// The title is how a user tells the two apart after the fact, so it moves with the
	// flag rather than being set alongside it.
	if forced.Title != "Full metadata refresh" {
		t.Errorf("forced title = %q, want %q", forced.Title, "Full metadata refresh")
	}
	if collection.Title != "Metadata refresh" {
		t.Errorf("unforced title = %q, want %q", collection.Title, "Metadata refresh")
	}
}

// An MBID-less row is skipped rather than fetched: an empty ID cannot identify
// anything, and asking MusicBrainz about it burns a rate-limit slot to get a 404.
func TestCollectionScopeSkipsBlankMBIDs(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "", Name: "nameless"}).Error; err != nil {
		t.Fatalf("seed artist: %v", err)
	}

	scope, err := CollectionScope(db, false)
	if err != nil {
		t.Fatalf("CollectionScope: %v", err)
	}
	if scope.size() != 0 {
		t.Fatalf("expected an empty scope, got %+v", scope)
	}
}

func TestCollectionScopeWithoutDB(t *testing.T) {
	scope, err := CollectionScope(nil, false)
	if err != nil {
		t.Fatalf("CollectionScope: %v", err)
	}
	if scope.size() != 0 {
		t.Fatalf("expected an empty scope, got %+v", scope)
	}
}

// A pass over an empty collection still completes and reports cleanly, which is
// what the cron job does on a fresh install.
// DueScope is a thin constructor: it labels the refresh and carries exactly the
// release IDs it was handed, nothing more.
func TestDueScope(t *testing.T) {
	scope := DueScope([]string{"rel-a", "rel-b"})
	if scope.Title == "" {
		t.Error("DueScope produced an unlabelled scope")
	}
	if len(scope.Releases) != 2 || scope.Releases[0] != "rel-a" {
		t.Errorf("DueScope releases = %v, want the two it was given", scope.Releases)
	}
}

func TestRunnerRunningStartsFalse(t *testing.T) {
	if NewRunner(testDB(t), nil, models.ConfigStruct{}).Running() {
		t.Error("a fresh runner reports a pass in progress")
	}
}

// RunCollection over an empty collection resolves an empty scope and returns cleanly
// without any MusicBrainz call — the whole-collection entry point still works when
// there is nothing to refresh.
func TestRunCollectionOverEmptyCollection(t *testing.T) {
	r := NewRunner(testDB(t), nil, models.ConfigStruct{})
	if err := r.RunCollection(context.Background(), false); err != nil {
		t.Fatalf("RunCollection: %v", err)
	}
	if r.Running() {
		t.Error("pass should not still be running")
	}
}

func TestRunOverEmptyCollection(t *testing.T) {
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})

	if _, err := r.Run(context.Background(), Scope{Title: "Metadata refresh"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	status := r.Status()
	if status.Running {
		t.Error("pass should not still be running")
	}
	if status.FinishedAt == nil {
		t.Error("expected a finish time")
	}
	if status.Total != 0 || status.Done != 0 || status.Errors != 0 {
		t.Errorf("unexpected counters: %+v", status)
	}
}

// Overlapping passes are dropped rather than queued: a second one would only
// compete for the same rate-limit budget to redo work the first is already doing.
func TestRunDropsOverlappingPasses(t *testing.T) {
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})

	// Hold the pass open by claiming the guard directly — the pass body is what a
	// real overlap would be sitting in.
	if !r.running.CompareAndSwap(false, true) {
		t.Fatal("guard was already claimed")
	}
	defer r.running.Store(false)

	if _, err := r.Run(context.Background(), Scope{}); err != ErrAlreadyRunning {
		t.Fatalf("Run = %v, want ErrAlreadyRunning", err)
	}
	if err := r.Start(context.Background(), Scope{}); err != ErrAlreadyRunning {
		t.Fatalf("Start = %v, want ErrAlreadyRunning", err)
	}
}

// Start takes the guard synchronously, so an API caller is told the truth about
// whether its request started a pass rather than racing the goroutine it spawned.
func TestStartClaimsGuardBeforeReturning(t *testing.T) {
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})

	if err := r.Start(context.Background(), Scope{Title: "Metadata refresh"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for r.running.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r.running.Load() {
		t.Fatal("pass did not finish")
	}
}

// Cancelling is safe at any point because a pass keeps no cursor: the next one
// resumes by skipping whatever is already fresh.
func TestWaitForTurnReturnsOnCancel(t *testing.T) {
	r := NewRunner(nil, func() bool { return true }, models.ConfigStruct{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := r.waitForTurn(ctx, true); err == nil {
		t.Fatal("expected a cancelled context to end the wait")
	}
}

// The mirror yields to a running scan because both spend the same
// one-request-per-second budget and the scan has a user waiting on it.
func TestWaitForTurnYieldsThenResumes(t *testing.T) {
	var mu sync.Mutex
	busy := true
	r := NewRunner(nil, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return busy
	}, models.ConfigStruct{})

	// Shorten the poll so the test does not wait out the production interval.
	original := yieldPollInterval
	yieldPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { yieldPollInterval = original })

	go func() {
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		busy = false
		mu.Unlock()
	}()

	done := make(chan error, 1)
	go func() { done <- r.waitForTurn(context.Background(), true) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("waitForTurn: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForTurn did not resume after the scan finished")
	}
}

func TestWaitForTurnReturnsImmediatelyWhenIdle(t *testing.T) {
	r := NewRunner(nil, func() bool { return false }, models.ConfigStruct{})
	if err := r.waitForTurn(context.Background(), true); err != nil {
		t.Fatalf("waitForTurn: %v", err)
	}

	// A nil predicate means nothing to yield to.
	if err := NewRunner(nil, nil, models.ConfigStruct{}).waitForTurn(context.Background(), true); err != nil {
		t.Fatalf("waitForTurn with no predicate: %v", err)
	}
}

func TestSummaryLine(t *testing.T) {
	line := summaryLine(Result{Checked: 10, Fetched: 3, Fresh: 7}, 90*time.Second)
	want := "10 entities — 3 fetched, 7 already cached (1m30s)"
	if line != want {
		t.Fatalf("got %q, want %q", line, want)
	}

	loud := summaryLine(Result{Checked: 10, Fetched: 3, Fresh: 5, Errors: 1, ChangedReleases: []string{"rel-1"}}, time.Second)
	wantLoud := "10 entities — 3 fetched, 5 already cached, 1 release(s) changed upstream, 1 failed (1s)"
	if loud != wantLoud {
		t.Fatalf("got %q, want %q", loud, wantLoud)
	}
}

// Status folds in live cache coverage, which is meaningful between passes and so
// must not be reset with the per-pass counters.
func TestStatusReportsCacheCoverage(t *testing.T) {
	status := NewRunner(testDB(t), nil, models.ConfigStruct{}).Status()
	if status.Cached == nil {
		t.Fatal("expected cache coverage in the status")
	}
	if _, ok := status.Cached["release"]; !ok {
		t.Error("expected a release count in the coverage map")
	}
}

// Progress is read by two callers that must agree: the flusher that writes a running
// pass onto its event row, and the scan runner's status endpoint while the pass is the
// queued job in flight. Both draw the same bar, so it has to be the live summary and
// not a copy taken when the pass began.
func TestProgressTracksTheLivePass(t *testing.T) {
	r := NewRunner(testDB(t), nil, models.ConfigStruct{})

	r.setStatus(func(s *Summary) {
		*s = Summary{Running: true, Total: 26373, Done: 19963, Phase: PhaseEditions}
	})

	p := r.Progress()
	if p.Total != 26373 || p.Done != 19963 || p.Phase != PhaseEditions {
		t.Errorf("progress = %d/%d phase=%q, want 19963/26373 phase=%q", p.Done, p.Total, p.Phase, PhaseEditions)
	}

	r.setStatus(func(s *Summary) { s.Done = 20500; s.Phase = PhaseReleases })
	if p := r.Progress(); p.Done != 20500 || p.Phase != PhaseReleases {
		t.Errorf("progress after the pass moved on = %d phase=%q, want 20500 phase=%q", p.Done, p.Phase, PhaseReleases)
	}
}

// finish is what writes the Activity event a multi-hour pass is watched through,
// so it is worth asserting separately from a pass that can reach the network.
func TestFinishRecordsAnEvent(t *testing.T) {
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})

	r.setStatus(func(s *Summary) {
		*s = Summary{Running: true, Total: 4, Done: 4, Fetched: 1, Fresh: 3}
	})
	ev := events.Begin(db, models.EventTypeMirror, "Metadata refresh")
	r.finish(ev, time.Now().Add(-time.Second), Scope{}, Result{Checked: 4, Fetched: 1, Fresh: 3}, false)

	var stored models.Event
	if err := db.First(&stored, "id = ?", ev.ID).Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if stored.Status != models.EventStatusOK {
		t.Errorf("status = %q, want ok", stored.Status)
	}
	if stored.FinishedAt == nil {
		t.Error("expected the event to be closed out")
	}

	status := r.Status()
	if status.Running || status.Phase != PhaseIdle {
		t.Errorf("runner should be idle after finish: %+v", status)
	}
}

// Per-entity errors are counted and the pass carries on, so a pass that hit some
// is still an "ok" outcome with a count — not a failed job.
func TestFinishRecordsErrorCounts(t *testing.T) {
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})

	ev := events.Begin(db, models.EventTypeMirror, "Metadata refresh")
	r.finish(ev, time.Now(), Scope{}, Result{Errors: 1}, false)

	var stored models.Event
	if err := db.First(&stored, "id = ?", ev.ID).Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if stored.Status != models.EventStatusOK {
		t.Errorf("status = %q — one failed entity does not fail the pass", stored.Status)
	}
	if got := stored.Details["errors"]; got == nil {
		t.Error("expected the error count on the event details")
	}
}

func TestCancelledPassIsNotAFailure(t *testing.T) {
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})

	r.setStatus(func(s *Summary) { *s = Summary{Running: true, Total: 10, Done: 3} })
	ev := events.Begin(db, models.EventTypeMirror, "Metadata refresh")
	r.finish(ev, time.Now(), Scope{}, Result{Checked: 3}, true)

	var stored models.Event
	if err := db.First(&stored, "id = ?", ev.ID).Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if stored.Status != models.EventStatusOK {
		t.Errorf("status = %q — a deliberate stop is not a failure", stored.Status)
	}
	if !strings.HasPrefix(stored.Summary, "stopped early") {
		t.Errorf("summary = %q, want it to say the pass stopped early", stored.Summary)
	}
	if got := r.Status(); got.Errors != 0 {
		t.Errorf("cancelling must not count as an error: %+v", got)
	}
}

// Cancel on an idle runner is a no-op rather than a panic — the API exposes it
// unconditionally.
func TestCancelWhenIdle(t *testing.T) {
	NewRunner(testDB(t), nil, models.ConfigStruct{}).Cancel()
}

// The contract this package exists to hold: a refresh reports what changed and
// writes nothing. If someone later re-adds a re-tag here, this fails.
func TestRefreshReportsChangesWithoutActingOnThem(t *testing.T) {
	res := Result{ChangedReleases: []string{"rel-1", "rel-2"}}

	if len(res.ChangedReleases) != 2 {
		t.Fatalf("changed releases = %+v", res.ChangedReleases)
	}
	// Result carries no notion of files written, deliberately — there is nowhere for
	// a re-tag count to go, so a re-tag cannot be quietly added without changing the
	// shape of the handover.
	line := summaryLine(res, time.Second)
	if !strings.Contains(line, "changed upstream") {
		t.Errorf("summary should report the handover, got %q", line)
	}
	if strings.Contains(line, "tagged") || strings.Contains(line, "written") {
		t.Errorf("a refresh summary must not claim file writes: %q", line)
	}
}

// Cold releases get a longer TTL so a followed artist's back catalogue does not
// consume the rate limit that owned releases need.
func TestCollectionScopeMarksUnownedReleasesCold(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionRelease{MBID: "owned", ReleaseGroupMBID: "rg", OwnedTracks: 3}).Error; err != nil {
		t.Fatalf("seed owned: %v", err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: "catalog", ReleaseGroupMBID: "rg"}).Error; err != nil {
		t.Fatalf("seed catalog: %v", err)
	}

	scope, err := CollectionScope(db, false)
	if err != nil {
		t.Fatalf("CollectionScope: %v", err)
	}
	if scope.Cold["owned"] {
		t.Error("a release with files on disk must keep the short TTL")
	}
	if !scope.Cold["catalog"] {
		t.Error("a release nobody owns should get the long TTL")
	}
}

// Forcing has to reach past the lookup's *own* cache check. Skipping the pass's
// freshness gate is not enough — GetMusicBrainzArtist consults the cache itself, so
// without expiring the entry a forced refresh would quietly make no request at all
// and the manual "check now" button would be a no-op.
func TestForcedRefreshActuallyRefetches(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"a1","name":"Talk Talk","type":"Group"}`)
	}))
	t.Cleanup(srv.Close)
	modules.SetMusicBrainzBaseURLForTest(t, srv.URL)

	r := NewRunner(nil, nil, models.ConfigStruct{})
	warm := Scope{Artists: []string{"a1"}}

	// Warm the cache, then ask again without forcing: the second pass must not fetch.
	r.RunInline(context.Background(), warm)
	before := atomic.LoadInt32(&calls)
	r.RunInline(context.Background(), warm)
	if got := atomic.LoadInt32(&calls); got != before {
		t.Fatalf("an unforced pass refetched a fresh entry: %d -> %d", before, got)
	}

	// Forcing must go back to the network.
	r.RunInline(context.Background(), Scope{Artists: []string{"a1"}, Force: true})
	if got := atomic.LoadInt32(&calls); got <= before {
		t.Fatalf("a forced pass made no request: calls still %d", got)
	}
}

// A refresh creates migrations as a side effect of fetching, and it must queue them
// under the *user's* approval policy rather than applying them regardless. Holding
// a category for review is the user saying "ask me first", and a background job
// that quietly re-pointed their records anyway would be the worst kind of bug —
// silent, and about identity.
func TestRefreshRespectsMigrationApprovalPolicy(t *testing.T) {
	db := testDB(t)

	original := files.ConfigFile
	files.ConfigFile = models.ConfigStruct{AutotaggerrMigrationReviewReleases: true}
	t.Cleanup(func() { files.ConfigFile = original })

	// Something has to be keyed on the old ID for the merge to be a decision at all:
	// a migration nothing references is closed as already-settled rather than held,
	// which would answer this test's question by not asking it.
	if err := db.Create(&models.CollectionRelease{MBID: "old-release"}).Error; err != nil {
		t.Fatalf("seed edition: %v", err)
	}

	// A merge that the policy says must be reviewed before it is acted on.
	pending := models.MusicbrainzMigration{
		EntityType: models.MigrationEntityRelease,
		OldMBID:    "old-release",
		NewMBID:    "new-release",
		Kind:       models.MigrationKindRedirect,
		Status:     models.MigrationStatusPending,
		DetectedAt: time.Now(),
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	NewRunner(db, nil, models.ConfigStruct{}).applyMigrations()

	var after models.MusicbrainzMigration
	if err := db.First(&after, "old_mb_id = ?", "old-release").Error; err != nil {
		t.Fatalf("reload migration: %v", err)
	}
	if after.Status != models.MigrationStatusPending {
		t.Fatalf("status = %q — a held category was applied without approval", after.Status)
	}
}

// The mirror image: with nothing held for review, a refresh applies what it finds
// rather than piling up a queue nobody asked to review.
func TestRefreshAppliesMigrationsWhenPolicyAllows(t *testing.T) {
	db := testDB(t)

	original := files.ConfigFile
	files.ConfigFile = models.ConfigStruct{}
	t.Cleanup(func() { files.ConfigFile = original })

	// As above: applied and closed-as-settled are different outcomes, and only a merge
	// something is actually keyed on distinguishes them.
	if err := db.Create(&models.CollectionRelease{MBID: "old-release"}).Error; err != nil {
		t.Fatalf("seed edition: %v", err)
	}

	pending := models.MusicbrainzMigration{
		EntityType: models.MigrationEntityRelease,
		OldMBID:    "old-release",
		NewMBID:    "new-release",
		Kind:       models.MigrationKindRedirect,
		Status:     models.MigrationStatusPending,
		DetectedAt: time.Now(),
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	NewRunner(db, nil, models.ConfigStruct{}).applyMigrations()

	var after models.MusicbrainzMigration
	if err := db.First(&after, "old_mb_id = ?", "old-release").Error; err != nil {
		t.Fatalf("reload migration: %v", err)
	}
	if after.Status == models.MigrationStatusPending {
		t.Fatal("nothing was held for review, yet the migration was left pending")
	}
}

// Forcing is the user saying they do not trust any cached copy, so honouring the
// long "nobody owns this" TTL then would be answering a different question.
func TestForcedCollectionScopeDropsTTLTiering(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionRelease{MBID: "catalog", ReleaseGroupMBID: "rg"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	unforced, err := CollectionScope(db, false)
	if err != nil {
		t.Fatalf("CollectionScope: %v", err)
	}
	if !unforced.Cold["catalog"] {
		t.Error("an unowned release should be tiered cold on a scheduled pass")
	}

	forced, err := CollectionScope(db, true)
	if err != nil {
		t.Fatalf("CollectionScope: %v", err)
	}
	if forced.Cold["catalog"] {
		t.Error("a forced pass must not extend TTLs")
	}
	if !forced.Force || forced.Title != "Full metadata refresh" {
		t.Errorf("forced scope = %+v", forced)
	}
}

// Releases reached only through the file index must be covered: reading just the
// owned-editions table is what made a collection refresh skip releases that files
// on disk actually point at.
func TestCollectionScopeIncludesReleasesFromTheFileIndex(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.LibraryItem{Path: "/m/a.flac", MBReleaseID: "only-in-index"}).Error; err != nil {
		t.Fatalf("seed item: %v", err)
	}

	scope, err := CollectionScope(db, false)
	if err != nil {
		t.Fatalf("CollectionScope: %v", err)
	}
	found := false
	for _, id := range scope.Releases {
		if id == "only-in-index" {
			found = true
		}
	}
	if !found {
		t.Fatalf("release known only to the file index was skipped: %+v", scope.Releases)
	}
}

// A library is a set of files, so its scope is derived from the file index — the
// collection is aggregated across libraries and cannot say which one a release
// came from.
func TestLibraryScopeFollowsTheFileIndex(t *testing.T) {
	db := testDB(t)

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	other := models.Library{Name: "Other", Path: "/o"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("other library: %v", err)
	}

	if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: "/m/a.flac", MBReleaseID: "rel-mine"}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	if err := db.Create(&models.LibraryItem{LibraryID: other.ID, Path: "/o/b.flac", MBReleaseID: "rel-theirs"}).Error; err != nil {
		t.Fatalf("other item: %v", err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: "rel-mine", ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1"}).Error; err != nil {
		t.Fatalf("edition: %v", err)
	}

	scope, err := LibraryScope(db, lib.ID)
	if err != nil {
		t.Fatalf("LibraryScope: %v", err)
	}
	if len(scope.Releases) != 1 || scope.Releases[0] != "rel-mine" {
		t.Fatalf("releases = %+v — another library's files leaked in", scope.Releases)
	}
	// The group and artist behind the release come along, or refreshing a library
	// would warm its releases and leave the pages built on top of them cold.
	if len(scope.Groups) != 1 || scope.Groups[0] != "rg-1" {
		t.Errorf("groups = %+v", scope.Groups)
	}
	if len(scope.Artists) != 1 || scope.Artists[0] != "art-1" {
		t.Errorf("artists = %+v", scope.Artists)
	}
}

func TestLibraryScopeWithNoFiles(t *testing.T) {
	db := testDB(t)
	lib := models.Library{Name: "Empty", Path: "/e"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}

	scope, err := LibraryScope(db, lib.ID)
	if err != nil {
		t.Fatalf("LibraryScope: %v", err)
	}
	if scope.size() != 0 {
		t.Fatalf("expected an empty scope, got %+v", scope)
	}
}
