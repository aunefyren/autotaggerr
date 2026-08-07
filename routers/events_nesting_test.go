package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/models"
)

// The feed lists runs. A run's stages are reached by opening it, so six stages must not
// push five other runs off the first page.
func TestFeedListsRunsNotStages(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	run := events.Begin(api.DB, models.EventTypeProcess, "Process all libraries")
	stage := events.BeginChild(api.DB, run, models.EventTypeMirror, "Metadata refresh")
	events.Finish(api.DB, stage, models.EventStatusOK, "4 entities", nil)
	events.Finish(api.DB, run, models.EventStatusOK, "done", nil)

	var page struct {
		Events []models.Event `json:"events"`
	}
	w := do(r, "GET", "/api/v1/events", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, ev := range page.Events {
		if ev.ParentID != nil {
			t.Fatalf("feed returned a stage event (%s) — it belongs under its run", ev.Type)
		}
	}

	// A type filter still has to be able to find one, or "show me every metadata
	// refresh" would silently skip the ones that ran inside a run.
	w = do(r, "GET", "/api/v1/events?nested=1&type="+models.EventTypeMirror, token, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode nested: %v", err)
	}
	if len(page.Events) != 1 || page.Events[0].ID != stage.ID {
		t.Fatalf("nested=1 did not return the stage: %+v", page.Events)
	}
}

// Browsing groups; filtering flattens. Asking for every metadata refresh means every
// one — a filter that silently skipped the ones inside runs would be answering a
// narrower question than it was asked.
func TestAFilterIncludesStagesWithoutAskingTwice(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	run := events.Begin(api.DB, models.EventTypeProcess, "Process all libraries")
	stage := events.BeginChild(api.DB, run, models.EventTypeMirror, "Metadata refresh")
	events.Finish(api.DB, stage, models.EventStatusOK, "4 entities", nil)
	events.Finish(api.DB, run, models.EventStatusOK, "done", nil)

	var page struct {
		Events []models.Event `json:"events"`
	}
	w := do(r, "GET", "/api/v1/events?type="+models.EventTypeMirror, token, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Events) != 1 || page.Events[0].ID != stage.ID {
		t.Fatalf("a type filter missed the stage that ran inside a run: %+v", page.Events)
	}
	// A stage in a flat list is unmoored without it — "Metadata refresh" says nothing
	// about which run found it.
	if page.Events[0].ParentTitle != "Process all libraries" {
		t.Errorf("stage row parent title = %q, want the run's", page.Events[0].ParentTitle)
	}

	// The override still works in both directions, so an API caller is never stuck
	// with the inference.
	w = do(r, "GET", "/api/v1/events?nested=0&type="+models.EventTypeMirror, token, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode nested=0: %v", err)
	}
	if len(page.Events) != 0 {
		t.Errorf("nested=0 should have excluded the stage, got %+v", page.Events)
	}
}

// The feed pages, and the page after the first has to be the *next* rows rather than
// the same ones again. `total` counts what the filter matched, not what fitted on the
// page, or the control cannot know there is a second one.
func TestFeedPagesWithoutRepeatingOrSkipping(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	const runs = 7
	for i := 0; i < runs; i++ {
		ev := events.Begin(api.DB, models.EventTypeProcess, "run")
		events.Finish(api.DB, ev, models.EventStatusOK, "done", nil)
		// The feed sorts on started_at, so rows written inside one clock tick would
		// have no defined order to page through.
		time.Sleep(2 * time.Millisecond)
	}

	var page struct {
		Total  int            `json:"total"`
		Events []models.Event `json:"events"`
	}
	seen := map[string]bool{}
	for offset := 0; offset < runs; offset += 3 {
		w := do(r, "GET", fmt.Sprintf("/api/v1/events?limit=3&offset=%d", offset), token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("offset %d: status %d", offset, w.Code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if page.Total != runs {
			t.Fatalf("total = %d, want %d — the control cannot size itself without it", page.Total, runs)
		}
		for _, ev := range page.Events {
			if seen[ev.ID.String()] {
				t.Errorf("event %s appeared on two pages", ev.ID)
			}
			seen[ev.ID.String()] = true
		}
	}
	if len(seen) != runs {
		t.Errorf("paging reached %d of %d runs", len(seen), runs)
	}
}

// A chip states its own result before it is pressed, so the count has to be what
// pressing it returns — and each facet is computed without its own filter, or the list
// of types collapses to the one already chosen the moment you pick it.
func TestFacetsCountWhatTheFilterWouldReturn(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	ok := events.Begin(api.DB, models.EventTypeProcess, "Process all libraries")
	events.Finish(api.DB, ok, models.EventStatusOK, "done", nil)
	bad := events.Begin(api.DB, models.EventTypeProcess, "Process all libraries")
	events.Finish(api.DB, bad, models.EventStatusError, "boom", nil)
	refresh := events.Begin(api.DB, models.EventTypeMirror, "Metadata refresh")
	events.Finish(api.DB, refresh, models.EventStatusOK, "done", nil)

	var page struct {
		Total  int                         `json:"total"`
		Facets map[string]map[string]int64 `json:"facets"`
	}
	decode := func(url string) {
		t.Helper()
		w := do(r, "GET", url, token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", url, w.Code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatalf("%s: decode: %v", url, err)
		}
	}

	decode("/api/v1/events")
	if page.Facets["status"]["error"] != 1 || page.Facets["type"][models.EventTypeProcess] != 2 {
		t.Fatalf("unfiltered facets = %+v", page.Facets)
	}

	// Pressing the Failed chip must return exactly what it promised.
	decode("/api/v1/events?status=" + models.EventStatusError)
	if page.Total != 1 {
		t.Errorf("Failed chip said 1 and returned %d", page.Total)
	}
	// With a status filter on, the type counts narrow to it — that is the facet doing
	// its job — while the status counts keep every option, so the filter can be changed
	// rather than only cleared.
	if page.Facets["type"][models.EventTypeMirror] != 0 {
		t.Errorf("type facet ignored the active status filter: %+v", page.Facets["type"])
	}
	if page.Facets["status"][models.EventStatusOK] != 2 {
		t.Errorf("status facet collapsed to its own filter: %+v", page.Facets["status"])
	}
}

// The feed's search is a query, so it has to fold case the same way whichever database
// is behind it — SQLite's LIKE folds ASCII and Postgres's does not.
func TestFeedSearchIsCaseInsensitiveOnTitle(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	ev := events.Begin(api.DB, models.EventTypeMirror, "Metadata refresh for Talk Talk")
	events.Finish(api.DB, ev, models.EventStatusOK, "done", nil)
	other := events.Begin(api.DB, models.EventTypeProcess, "Process all libraries")
	events.Finish(api.DB, other, models.EventStatusOK, "done", nil)

	var page struct {
		Total  int            `json:"total"`
		Events []models.Event `json:"events"`
	}
	w := do(r, "GET", "/api/v1/events?q=talk+talk", token, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Total != 1 || len(page.Events) != 1 || page.Events[0].ID != ev.ID {
		t.Fatalf("search for a lowercased title found %+v", page.Events)
	}
}

// A feed row offers to expand only when there is something to expand into, so the count
// has to arrive with the row rather than after a fetch it exists to avoid.
func TestFeedRowsCarryTheirStageCount(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	run := events.Begin(api.DB, models.EventTypeProcess, "Process all libraries")
	for i := 0; i < 3; i++ {
		events.Finish(api.DB, events.BeginChild(api.DB, run, models.EventTypeCollectionScan, "Collection scan"),
			models.EventStatusOK, "done", nil)
	}
	events.Finish(api.DB, run, models.EventStatusOK, "done", nil)

	lone := events.Begin(api.DB, models.EventTypeHealth, "Health check")
	events.Finish(api.DB, lone, models.EventStatusOK, "ok", nil)

	var page struct {
		Events []models.Event `json:"events"`
	}
	w := do(r, "GET", "/api/v1/events", token, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byID := map[string]models.Event{}
	for _, ev := range page.Events {
		byID[ev.ID.String()] = ev
	}
	if got := byID[run.ID.String()].ChildCount; got != 3 {
		t.Errorf("run child_count = %d, want 3", got)
	}
	if got := byID[lone.ID.String()].ChildCount; got != 0 {
		t.Errorf("an event with no stages reports %d; it must not offer to expand", got)
	}
}

// Opening a run returns its stages in the order they happened — the order is the thing
// a reader opens a run to see.
func TestGetEventAttachesItsStages(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	run := events.Begin(api.DB, models.EventTypeProcess, "Process all libraries")
	for _, kind := range []string{models.EventTypeMirror, models.EventTypeProcessFiles, models.EventTypeCollectionScan} {
		events.Finish(api.DB, events.BeginChild(api.DB, run, kind, kind), models.EventStatusOK, "done", nil)
	}
	events.Finish(api.DB, run, models.EventStatusOK, "done", nil)

	w := do(r, "GET", "/api/v1/events/"+run.ID.String(), token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got models.Event
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Children) != 3 {
		t.Fatalf("children = %d, want 3", len(got.Children))
	}
	if got.Children[0].Type != models.EventTypeMirror {
		t.Errorf("stages out of order, first is %q", got.Children[0].Type)
	}
}

// The Scan verb answers its caller inline, which is why it recorded nothing for a long
// time. Being fast is not the same as being uninteresting: it can move albums between
// artists, and pressing it left no trace.
func TestScanVerbRecordsAnEvent(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	if w := do(r, "POST", "/api/v1/scan", token, nil); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var count int64
	api.DB.Model(&models.Event{}).Where("type = ?", models.EventTypeCollectionScan).Count(&count)
	if count != 1 {
		t.Errorf("recorded %d collection scan event(s), want 1", count)
	}
}
