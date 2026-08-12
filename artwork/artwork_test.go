package artwork

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/database"
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

// Distinct MBIDs per test: the artwork index is process-global in modules, so tests
// sharing an id would see each other's cache entries.
const (
	artistA = "11111111-1111-4111-8111-111111111111"
	groupA  = "22222222-2222-4222-8222-222222222222"
	groupB  = "33333333-3333-4333-8333-333333333333"
	groupC  = "44444444-4444-4444-8444-444444444444"
	groupD  = "55555555-5555-4555-8555-555555555555"
	groupE  = "66666666-6666-4666-8666-666666666666"
	groupF  = "77777777-7777-4777-8777-777777777777"
	groupG  = "88888888-8888-4888-8888-888888888888"
)

// jpeg is the smallest byte sequence the artwork path sniffs as an image.
var jpeg = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{
		Type: "sqlite",
		DSN:  filepath.Join(t.TempDir(), "artwork.db"),
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

// coverServer stands in for the Cover Art Archive and counts requests, which is how
// the "already warm costs nothing" claims are checked rather than assumed.
func coverServer(t *testing.T, handler http.HandlerFunc) (modules.ArtworkProviders, *int64) {
	t.Helper()
	var calls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return modules.ArtworkProviders{CoverArtEnabled: true, CoverArtBaseURL: server.URL}, &calls
}

func servesJPEG(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jpeg) }

func coverUnit(mbid string) unit {
	return unit{modules.ArtworkEntityReleaseGroup, mbid, modules.ArtworkKindFront, rowCoverSize}
}

// --- targets ----------------------------------------------------------------

func TestCollectionTargetsCoverArtistsAndGroups(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: artistA, Name: "Talk Talk"}).Error; err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroup{MBID: groupA, ArtistMBID: artistA, Title: "Spirit of Eden"}).Error; err != nil {
		t.Fatalf("seed group: %v", err)
	}

	targets, err := CollectionTargets(db)
	if err != nil {
		t.Fatalf("CollectionTargets: %v", err)
	}
	if len(targets.Artists) != 1 || len(targets.Groups) != 1 {
		t.Errorf("targets = %+v, want one of each", targets)
	}
}

// A release-group whose deletion is on record resolves nowhere, so asking the Cover
// Art Archive about it buys a guaranteed 404 every single night.
func TestCollectionTargetsSkipRetiredGroups(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionReleaseGroup{MBID: groupA, Title: "Gone"}).Error; err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if err := db.Create(&models.MusicbrainzMigration{
		EntityType: models.MigrationEntityReleaseGroup,
		Kind:       models.MigrationKindDeleted,
		OldMBID:    groupA,
	}).Error; err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	targets, err := CollectionTargets(db)
	if err != nil {
		t.Fatalf("CollectionTargets: %v", err)
	}
	if len(targets.Groups) != 0 {
		t.Errorf("groups = %v, want the retired one skipped", targets.Groups)
	}
}

// A rebuild creating two hundred release-groups notifies two hundred times, so the
// accumulated set must not grow without bound or repeat work.
func TestTargetsMergeDropsDuplicates(t *testing.T) {
	targets := Targets{Artists: []string{artistA}}
	targets.merge(Targets{Artists: []string{artistA, ""}, Groups: []string{groupA}})
	targets.merge(Targets{Groups: []string{groupA}})

	if len(targets.Artists) != 1 || len(targets.Groups) != 1 {
		t.Errorf("merged = %+v, want one of each", targets)
	}
}

// --- planning ---------------------------------------------------------------

// The guard this package most needs. modules.GetArtwork turns a switched-off provider
// into ErrNoArtwork, and ErrNoArtwork is *remembered* — so a pass that asked anyway
// would write "there is no cover for this album" for every album in the collection and
// keep saying so for a week after the source was enabled.
func TestPlanIsEmptyWithoutProviders(t *testing.T) {
	targets := Targets{Artists: []string{artistA}, Groups: []string{groupA}}

	if units := plan(modules.ArtworkProviders{}, targets); len(units) != 0 {
		t.Fatalf("planned %d image(s) with no provider configured: %+v", len(units), units)
	}
	// fanart.tv enabled but keyless cannot resolve an image, so it plans none either —
	// the same truth /artwork-capabilities reports to the UI.
	if units := plan(modules.ArtworkProviders{FanartEnabled: true}, targets); len(units) != 0 {
		t.Errorf("keyless fanart planned %d artist image(s)", len(units))
	}
}

func TestPlanCoversEveryEntity(t *testing.T) {
	providers := modules.ArtworkProviders{
		CoverArtEnabled: true, FanartEnabled: true, FanartAPIKey: "k",
	}
	units := plan(providers, Targets{Artists: []string{artistA}, Groups: []string{groupA}})
	if len(units) != 3 {
		t.Fatalf("planned %d unit(s), want 3 (portrait, backdrop, cover): %+v", len(units), units)
	}

	kinds := map[string]bool{}
	for _, u := range units {
		kinds[u.entity+"/"+u.kind] = true
		if u.entity == modules.ArtworkEntityReleaseGroup && u.size != rowCoverSize {
			// The list size, not the hero: a page asks for a hundred rows and one hero,
			// and only the first is worth warming.
			t.Errorf("cover size = %d, want %d", u.size, rowCoverSize)
		}
	}
	for _, want := range []string{"artist/thumb", "artist/background", "release-group/front"} {
		if !kinds[want] {
			t.Errorf("no unit planned for %s", want)
		}
	}
}

// The two providers are independent: one being absent must not silence the other.
func TestPlanTreatsProvidersIndependently(t *testing.T) {
	targets := Targets{Artists: []string{artistA}, Groups: []string{groupA}}
	units := plan(modules.ArtworkProviders{CoverArtEnabled: true}, targets)
	if len(units) != 1 || units[0].entity != modules.ArtworkEntityReleaseGroup {
		t.Errorf("cover-only providers planned %+v", units)
	}
}

// --- warming ----------------------------------------------------------------

// The whole point: an image the cache already holds costs nothing on the next pass,
// so a nightly run does not re-download the collection.
func TestWarmOneSkipsWhatIsAlreadyCached(t *testing.T) {
	t.Chdir(t.TempDir())
	providers, calls := coverServer(t, servesJPEG)
	r := newRunner(nil, models.ConfigStruct{})

	var res Result
	r.warmOne(providers, coverUnit(groupA), false, &res, false)
	if res.Fetched != 1 || res.Fresh != 0 {
		t.Fatalf("first pass = %+v, want one fetch", res)
	}

	r.warmOne(providers, coverUnit(groupA), false, &res, false)
	if res.Fresh != 1 {
		t.Errorf("second pass = %+v, want the cached copy counted as fresh", res)
	}
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Errorf("upstream calls = %d, want 1 — a warm image must not be re-fetched", n)
	}
}

// Force is what "disregard the cache" means, and it has to reach past GetArtwork's own
// cache check or nothing happens.
func TestForceRefetchesACachedImage(t *testing.T) {
	t.Chdir(t.TempDir())
	providers, calls := coverServer(t, servesJPEG)
	r := newRunner(nil, models.ConfigStruct{})

	var res Result
	r.warmOne(providers, coverUnit(groupB), false, &res, false)

	forced := Result{}
	r.warmOne(providers, coverUnit(groupB), true, &forced, false)
	if forced.Fetched != 1 || forced.Fresh != 0 {
		t.Errorf("forced pass = %+v, want the image fetched again", forced)
	}
	if n := atomic.LoadInt64(calls); n != 2 {
		t.Errorf("upstream calls = %d, want 2", n)
	}
}

// The cheaper and more useful half of forcing: re-asking about images a provider
// previously said do not exist. "fanart.tv had nothing last week" is exactly what a
// user wants re-checked after someone uploads art.
func TestForceReAsksARememberedAbsence(t *testing.T) {
	t.Chdir(t.TempDir())
	modules.ResetArtworkNegativeCache()

	var serve atomic.Bool
	providers, calls := coverServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if serve.Load() {
			_, _ = w.Write(jpeg)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
	r := newRunner(nil, models.ConfigStruct{})

	var res Result
	r.warmOne(providers, coverUnit(groupC), false, &res, false)
	if res.Missing != 1 {
		t.Fatalf("first pass = %+v, want one missing", res)
	}
	// Unforced, the remembered absence is trusted and costs nothing.
	r.warmOne(providers, coverUnit(groupC), false, &res, false)
	if res.Fresh != 1 || atomic.LoadInt64(calls) != 1 {
		t.Errorf("unforced re-check = %+v, calls = %d — the negative must be trusted", res, atomic.LoadInt64(calls))
	}

	serve.Store(true)
	forced := Result{}
	r.warmOne(providers, coverUnit(groupC), true, &forced, false)
	if forced.Fetched != 1 {
		t.Errorf("forced re-check = %+v, want the newly uploaded art picked up", forced)
	}
}

// "No cover for this album" is the ordinary answer for a followed artist's back
// catalogue, not a failure — and it gets no detail row, because thousands of rows
// saying nothing happened is not detail.
func TestNoImageIsAnAnswerNotAFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	modules.ResetArtworkNegativeCache()
	providers, _ := coverServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	r := newRunner(nil, models.ConfigStruct{})

	var res Result
	r.warmOne(providers, coverUnit(groupD), false, &res, false)

	if res.Missing != 1 || res.Errors != 0 || res.Fetched != 0 {
		t.Errorf("result = %+v, want one missing and no error", res)
	}
	if len(res.Items) != 0 {
		t.Errorf("recorded %d detail row(s) for a coverless album", len(res.Items))
	}
}

// A provider that is actually broken is worth a row and worth the error count.
func TestProviderFailureIsRecorded(t *testing.T) {
	t.Chdir(t.TempDir())
	providers, _ := coverServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	r := newRunner(nil, models.ConfigStruct{})

	var res Result
	r.warmOne(providers, coverUnit(groupE), false, &res, true)

	if res.Errors != 1 || res.Missing != 0 {
		t.Errorf("result = %+v, want one error and no missing", res)
	}
	if len(res.Items) != 1 || res.Items[0].Path != groupE ||
		res.Items[0].Status != models.EventItemStatusError {
		t.Fatalf("detail rows = %+v, want one error row for the group", res.Items)
	}
	if got := r.Status(); got.Errors != 1 || got.LastError == "" {
		t.Errorf("summary = %+v, want the failure visible", got)
	}
}

// --- the queue --------------------------------------------------------------

// Targeted work jumps ahead of a running collection pass. Without this, adding an
// artist while a first pass grinds through three thousand cold covers puts their
// images twenty minutes into the queue — the exact wait this package removes.
func TestPendingTargetsAreDrainedDuringAPass(t *testing.T) {
	t.Chdir(t.TempDir())
	providers, _ := coverServer(t, servesJPEG)
	r := newRunner(nil, models.ConfigStruct{})
	r.providers = func(*gorm.DB) modules.ArtworkProviders { return providers }

	// Queued before the pass starts, so the first boundary check picks it up.
	r.Warm(nil, []string{groupF})

	res, cancelled := r.execute(context.Background(), providers, []unit{coverUnit(groupG)}, false)
	if cancelled {
		t.Fatal("pass reported itself cancelled")
	}
	if res.Checked != 2 {
		t.Errorf("checked = %d, want 2 — the pending target should have been folded in", res.Checked)
	}
	if r.takeHooks().Empty() == false {
		t.Error("pending targets were left in the queue")
	}
}

// A pass that is cancelled stops at the next image and reports itself as such.
func TestCancelledPassStopsEarly(t *testing.T) {
	t.Chdir(t.TempDir())
	providers, _ := coverServer(t, servesJPEG)
	r := newRunner(nil, models.ConfigStruct{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, cancelled := r.execute(ctx, providers, []unit{coverUnit(groupA)}, false)
	if !cancelled {
		t.Error("a cancelled pass did not report itself cancelled")
	}
	if res.Checked != 0 {
		t.Errorf("checked = %d, want 0 — cancellation should stop before the first image", res.Checked)
	}
}

// Forcing survives a second, unforced request for the same queued pass: two presses
// where one ticked the box must not resolve to the cheap reading of the expensive one.
func TestQueuedForceIsSticky(t *testing.T) {
	r := newRunner(nil, models.ConfigStruct{})
	r.RefreshCollection(true)
	r.RefreshCollection(false)

	queued, force := r.takeFull()
	if !queued || !force {
		t.Errorf("queued = %v force = %v, want both true", queued, force)
	}
}

// The disabled switch governs unattended work, and the create hooks are the half that
// is easy to forget: the cron job is gated where it is installed, so without this a
// user who turned artwork off would still see it fetch every time an album arrived.
func TestDisabledStopsTheCreateHooks(t *testing.T) {
	previous := files.ConfigFile.AutotaggerrArtworkDisabled
	t.Cleanup(func() { files.ConfigFile.AutotaggerrArtworkDisabled = previous })

	r := newRunner(nil, models.ConfigStruct{})

	files.ConfigFile.AutotaggerrArtworkDisabled = true
	r.Warm([]string{artistA}, nil)
	if !r.takeHooks().Empty() {
		t.Error("a create hook queued work while artwork fetching is disabled")
	}

	// Pressing the button is someone asking for the pass now, so it is deliberately
	// not gated: a control that silently did nothing because of a setting on another
	// page is the worse failure.
	r.RefreshCollection(false)
	if queued, _ := r.takeFull(); !queued {
		t.Error("the manual refresh was swallowed by the disabled switch")
	}

	files.ConfigFile.AutotaggerrArtworkDisabled = false
	r.Warm([]string{artistA}, nil)
	if r.takeHooks().Empty() {
		t.Error("a create hook queued nothing while artwork fetching is enabled")
	}
}

// --- what a pass records ----------------------------------------------------

func artworkEvents(t *testing.T, db *gorm.DB) []models.Event {
	t.Helper()
	var rows []models.Event
	if err := db.Where("type = ?", models.EventTypeArtwork).Find(&rows).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}
	return rows
}

// runnerFor builds a runner whose providers point at a stub, without starting the
// worker — run() is called directly so the assertions are not racing a goroutine.
func runnerFor(t *testing.T, db *gorm.DB, providers modules.ArtworkProviders) *Runner {
	t.Helper()
	r := newRunner(db, models.ConfigStruct{})
	r.providers = func(*gorm.DB) modules.ArtworkProviders { return providers }
	return r
}

// The scheduled pass is the one someone configured a cron for, so it reports even
// when it found everything already cached — "it ran and there was nothing to do" is
// the answer a schedule owes you.
func TestScheduledPassAlwaysRecordsAnEvent(t *testing.T) {
	t.Chdir(t.TempDir())
	db := testDB(t)
	providers, _ := coverServer(t, servesJPEG)
	r := runnerFor(t, db, providers)

	r.run(Targets{Groups: []string{groupA}}, false, true)
	first := artworkEvents(t, db)
	if len(first) != 1 {
		t.Fatalf("recorded %d event(s), want 1", len(first))
	}
	if first[0].Status != models.EventStatusOK {
		t.Errorf("status = %q, want ok", first[0].Status)
	}

	// Second run finds it cached and still records: nothing fetched, but the pass
	// happened.
	r.run(Targets{Groups: []string{groupA}}, false, true)
	if got := artworkEvents(t, db); len(got) != 2 {
		t.Errorf("recorded %d event(s) across two scheduled passes, want 2", len(got))
	}
}

// A targeted warm is triggered by row creation, and adding twenty artists must not put
// twenty rows in the feed. It reports only when it did something.
func TestTargetedWarmOnlyRecordsWhenItFetched(t *testing.T) {
	t.Chdir(t.TempDir())
	db := testDB(t)
	providers, _ := coverServer(t, servesJPEG)
	r := runnerFor(t, db, providers)

	r.run(Targets{Groups: []string{groupB}}, false, false)
	if got := artworkEvents(t, db); len(got) != 1 {
		t.Fatalf("recorded %d event(s) for a warm that fetched, want 1", len(got))
	}

	// Everything already cached: nothing happened, so nothing is reported.
	r.run(Targets{Groups: []string{groupB}}, false, false)
	if got := artworkEvents(t, db); len(got) != 1 {
		t.Errorf("recorded %d event(s), want the second warm to stay silent", len(got))
	}
}

// Nothing to do at all — no provider configured — must not open an event either, or an
// install with no artwork source would accrue a nightly row saying nothing.
func TestPassWithNothingPlannedRecordsNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	db := testDB(t)
	r := runnerFor(t, db, modules.ArtworkProviders{})

	r.run(Targets{Groups: []string{groupA}}, false, true)
	if got := artworkEvents(t, db); len(got) != 0 {
		t.Errorf("recorded %d event(s) with no provider configured, want 0", len(got))
	}
}

// The stats are what the feed row shows, and "no image available" has to be its own
// figure rather than folded into fetched — on a page about artwork providers it is
// what explains a screen full of monogram tiles.
func TestEventStatsSeparateMissingFromFetched(t *testing.T) {
	t.Chdir(t.TempDir())
	modules.ResetArtworkNegativeCache()
	db := testDB(t)
	providers, _ := coverServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	r := runnerFor(t, db, providers)

	r.run(Targets{Groups: []string{groupD}}, false, true)

	rows := artworkEvents(t, db)
	if len(rows) != 1 {
		t.Fatalf("recorded %d event(s), want 1", len(rows))
	}
	stats := map[string]int{}
	for _, s := range rows[0].Stats {
		stats[s.Label] = s.Value
	}
	if stats["No image available"] != 1 {
		t.Errorf("stats = %+v, want one 'No image available'", rows[0].Stats)
	}
	if stats["Fetched"] != 0 {
		t.Errorf("stats = %+v — a coverless album must not count as fetched", rows[0].Stats)
	}
	if !strings.Contains(rows[0].Summary, "no image available") {
		t.Errorf("summary = %q, want it to name the missing images", rows[0].Summary)
	}
}

// Cancel on an idle runner is a no-op rather than a panic — the API exposes it
// unconditionally, and the UI offers Stop whenever a pass looks like it is running.
func TestCancelWhenIdle(t *testing.T) {
	newRunner(nil, models.ConfigStruct{}).Cancel()
}

// The worker is what actually drains the queue in production, so at least one test has
// to go through it rather than calling run directly.
func TestWorkerDrainsQueuedWork(t *testing.T) {
	t.Chdir(t.TempDir())
	db := testDB(t)
	providers, _ := coverServer(t, servesJPEG)
	r := runnerFor(t, db, providers)
	go r.worker()

	r.Warm(nil, []string{groupG})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(artworkEvents(t, db)) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the worker never drained the queued warm")
}
