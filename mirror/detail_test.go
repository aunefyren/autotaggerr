package mirror

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// A pass reads four kinds of entity at very different costs, so one "1204 fetched"
// cannot say whether the hours went on discographies or on release payloads. Every
// entity a pass considers must land in its own phase's tally.
func TestPassCountsEachPhaseSeparately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	modules.SetMusicBrainzBaseURLForTest(t, srv.URL)

	r := NewRunner(nil, nil, models.ConfigStruct{})
	res := r.RunInline(context.Background(), Scope{Groups: []string{"g1"}, Releases: []string{"r1"}})

	for _, phase := range []string{PhaseEditions, PhaseReleases} {
		stat, ok := res.Phases[phase]
		if !ok {
			t.Fatalf("phase %q missing from the breakdown: %+v", phase, res.Phases)
		}
		if stat.Checked != 1 {
			t.Errorf("phase %q checked = %d, want 1", phase, stat.Checked)
		}
	}
	// A phase with nothing in scope must not appear at all — an "artists: 0" row would
	// read as "we looked and found none".
	if _, ok := res.Phases[PhaseArtists]; ok {
		t.Errorf("a phase with no entities in scope should not be tallied: %+v", res.Phases)
	}
}

// The counters say twelve entities failed; they never say which twelve. A pass that
// cannot name what it could not read leaves the user with a number and no next step.
func TestPassRecordsWhatItCouldNotRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	modules.SetMusicBrainzBaseURLForTest(t, srv.URL)

	r := NewRunner(nil, nil, models.ConfigStruct{})
	res := r.RunInline(context.Background(), Scope{Groups: []string{"g1"}})

	if res.ItemsTotal != 1 || len(res.Items) != 1 {
		t.Fatalf("items = %d (total %d), want 1", len(res.Items), res.ItemsTotal)
	}
	item := res.Items[0]
	if item.Path != "g1" {
		t.Errorf("path = %q, want the MBID", item.Path)
	}
	if item.Status != models.EventItemStatusError {
		t.Errorf("status = %q, want error", item.Status)
	}
	if item.Phase != PhaseEditions {
		t.Errorf("phase = %q, want %q — a row has to say which kind of entity it was", item.Phase, PhaseEditions)
	}
	if item.Error == "" {
		t.Error("an error row with no error text says nothing the count did not")
	}
}

// The cap bounds what is stored, not what is reported. A pass that recorded 500 of
// 3120 must be able to say so, or the UI implies 500 was all of it. The cap is the
// configured one, and an unset limit falls back to the default rather than to zero —
// which would store nothing while still counting everything.
func TestDetailRowsAreCappedButStillCounted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		want  int
	}{
		{"configured", 20, 20},
		{"unset falls back to the default", 0, models.DefaultEventDetailRetention},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := Result{detailLimit: tc.limit}
			total := tc.want + 25
			for i := 0; i < total; i++ {
				res.note(models.EventItem{Path: "rel", Status: models.EventItemStatusRefreshed})
			}

			if len(res.Items) != tc.want {
				t.Errorf("stored %d rows, want the cap of %d", len(res.Items), tc.want)
			}
			if res.ItemsTotal != total {
				t.Errorf("ItemsTotal = %d, want every row counted (%d)", res.ItemsTotal, total)
			}
		})
	}
}

// The runner is what carries the configured cap onto each pass's Result, and a
// non-positive setting must not mean "keep nothing".
func TestRunnerResolvesRetentionFromConfig(t *testing.T) {
	r := NewRunner(nil, nil, models.ConfigStruct{
		AutotaggerrEventRetention:       10,
		AutotaggerrEventDetailRetention: 25,
	})
	if r.eventRetention != 10 || r.detailRetention != 25 {
		t.Errorf("configured retention not carried: events=%d detail=%d", r.eventRetention, r.detailRetention)
	}

	zero := NewRunner(nil, nil, models.ConfigStruct{})
	if zero.eventRetention != models.DefaultEventRetention ||
		zero.detailRetention != models.DefaultEventDetailRetention {
		t.Errorf("unset retention should fall back to the defaults: events=%d detail=%d",
			zero.eventRetention, zero.detailRetention)
	}
}

// The order a pass walks its phases is itself information — identity before catalogs
// before the heavy payloads — so the payload carries them in that order rather than
// leaving a reader to know it.
func TestPhaseDetailsKeepWalkOrderAndSkipEmptyPhases(t *testing.T) {
	res := Result{Phases: map[string]PhaseStat{
		PhaseReleases: {Checked: 9, Fetched: 2, Fresh: 7},
		PhaseArtists:  {Checked: 3, Fetched: 3},
		PhaseEditions: {Checked: 0},
	}}

	got := phaseDetails(res)
	if len(got) != 2 {
		t.Fatalf("phases = %+v, want the two with work in them", got)
	}
	if got[0]["phase"] != PhaseArtists || got[1]["phase"] != PhaseReleases {
		t.Errorf("phases out of walk order: %+v", got)
	}
	if got[1]["fresh"] != 7 {
		t.Errorf("release phase fresh = %v, want 7", got[1]["fresh"])
	}
}

// The rows have to reach the database with the event, or the modal that reads them
// shows an empty list beside a non-zero count.
func TestFinishWritesTheDetailRows(t *testing.T) {
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})

	ev := events.Begin(db, models.EventTypeMirror, "Metadata refresh")
	res := Result{
		Checked: 2,
		Fetched: 2,
		Phases:  map[string]PhaseStat{PhaseReleases: {Checked: 2, Fetched: 2}},
		Items: []models.EventItem{
			{Path: "rel-1", Status: models.EventItemStatusRefreshed, Phase: PhaseReleases},
			{Path: "rel-2", Status: models.EventItemStatusGone, Phase: PhaseReleases},
		},
		ItemsTotal: 2,
	}
	r.finish(ev, time.Now().Add(-time.Second), Scope{}, res, false)

	var stored []models.EventItem
	if err := db.Where("event_id = ?", ev.ID).Order("path").Find(&stored).Error; err != nil {
		t.Fatalf("load detail rows: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored %d detail rows, want 2", len(stored))
	}
	if stored[0].Status != models.EventItemStatusRefreshed || stored[1].Status != models.EventItemStatusGone {
		t.Errorf("outcomes did not survive the write: %+v", stored)
	}

	var event models.Event
	if err := db.First(&event, "id = ?", ev.ID).Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if event.Details["phases"] == nil {
		t.Error("expected the per-phase breakdown on the event")
	}
	detail, ok := event.Details["detail"].(map[string]any)
	if !ok {
		t.Fatalf("detail summary = %#v, want a map", event.Details["detail"])
	}
	// Without the pair, "2 rows" cannot be told apart from "2 of 3120".
	if detail["recorded"] == nil || detail["total"] == nil || detail["limit"] == nil {
		t.Errorf("detail summary must carry recorded/total/limit, got %+v", detail)
	}
}

// A counter that names a status becomes a chip over the rows. If the two ever drift
// apart the chip is dead — it filters to nothing while showing a non-zero count — so
// every filter a pass declares has to be a status it actually writes.
func TestDeclaredFiltersMatchTheRowsWritten(t *testing.T) {
	db := testDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{})

	ev := events.Begin(db, models.EventTypeMirror, "Metadata refresh")
	res := Result{
		Checked:         4,
		ChangedReleases: []string{"rel-1"},
		GoneReleases:    1,
		Relinked:        1,
		Errors:          1,
		Items: []models.EventItem{
			{Path: "rel-1", Kind: models.EventItemKindEntity, Status: models.EventItemStatusRefreshed},
			{Path: "rel-2", Kind: models.EventItemKindEntity, Status: models.EventItemStatusGone},
			{Path: "rel-3", Kind: models.EventItemKindEntity, Status: models.EventItemStatusRelinked},
			{Path: "rel-4", Kind: models.EventItemKindEntity, Status: models.EventItemStatusError, Error: "boom"},
		},
		ItemsTotal: 4,
	}
	r.finish(ev, time.Now(), Scope{}, res, false)

	var stored models.Event
	if err := db.First(&stored, "id = ?", ev.ID).Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if len(stored.Stats) == 0 {
		t.Fatal("the pass declared no counters")
	}

	written := map[string]bool{}
	for _, item := range res.Items {
		written[item.Status] = true
	}
	for _, stat := range stored.Stats {
		if stat.Filter == "" || stat.Value == 0 {
			continue
		}
		if !written[stat.Filter] {
			t.Errorf("counter %q filters on %q, which no row carries", stat.Label, stat.Filter)
		}
	}
}

// Every row a metadata pass writes is an entity, not a file. The distinction is what
// stops the detail view reporting "0 tags written" beside a release MBID — a claim
// about the user's audio from the one verb that promises not to touch it.
func TestPassRowsAreMarkedAsEntities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	modules.SetMusicBrainzBaseURLForTest(t, srv.URL)

	r := NewRunner(nil, nil, models.ConfigStruct{})
	res := r.RunInline(context.Background(), Scope{Groups: []string{"g1"}})

	if len(res.Items) == 0 {
		t.Fatal("expected a row for the entity that failed")
	}
	for _, item := range res.Items {
		if item.Kind != models.EventItemKindEntity {
			t.Errorf("row %q has kind %q, want %q", item.Path, item.Kind, models.EventItemKindEntity)
		}
	}
}
