package scan

import (
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/mirror"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// TestScanSummaryLine: the base counts are always present, and the removed-files,
// credit-change and metadata-refresh clauses appear only when they actually happened —
// an ordinary scan with nothing gone, nothing moved and nothing due upstream must read
// exactly as it did before.
func TestScanSummaryLine(t *testing.T) {
	base := "10 processed · 3 changed · 7 tags written · 0 errors"

	cases := []struct {
		name    string
		removed int
		credits int
		refresh mirror.Result
		want    string
	}{
		{
			name:    "nothing due — no clause",
			refresh: mirror.Result{Checked: 5, Fetched: 0},
			want:    base,
		},
		{
			name:    "fetched but nothing changed",
			refresh: mirror.Result{Checked: 5, Fetched: 4},
			want:    base + " · 4 releases refreshed",
		},
		{
			name:    "fetched and some changed upstream",
			refresh: mirror.Result{Checked: 5, Fetched: 4, ChangedReleases: []string{"rel-1", "rel-2"}},
			want:    base + " · 4 releases refreshed, 2 changed upstream",
		},
		{
			name:    "files pruned from the index",
			removed: 12,
			refresh: mirror.Result{Checked: 5},
			want:    base + " · 12 removed",
		},
		{
			name:    "pruned and refreshed, in that order",
			removed: 2,
			refresh: mirror.Result{Checked: 5, Fetched: 4},
			want:    base + " · 2 removed · 4 releases refreshed",
		},
		{
			name:    "an album changed artists upstream",
			credits: 3,
			refresh: mirror.Result{Checked: 5},
			want:    base + " · 3 credit change(s)",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scanSummaryLine(10, 3, 7, 0, c.removed, c.credits, c.refresh)
			if got != c.want {
				t.Errorf("scanSummaryLine = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRefreshDetailItem: a changed-release row is a release outcome, not a file one —
// it carries the release MBID, the refreshed status/phase, and the count of files the
// drift stage re-tagged as a result (its TagsWritten), with no tag diff.
func TestRefreshDetailItem(t *testing.T) {
	item := refreshDetailItem("rel-abc", 5)

	if item.Path != "rel-abc" {
		t.Errorf("Path = %q, want rel-abc", item.Path)
	}
	if item.Status != models.EventItemStatusRefreshed {
		t.Errorf("Status = %q, want %q", item.Status, models.EventItemStatusRefreshed)
	}
	if item.Phase != models.EventItemPhaseRefresh {
		t.Errorf("Phase = %q, want %q", item.Phase, models.EventItemPhaseRefresh)
	}
	if item.TagsWritten != 5 {
		t.Errorf("TagsWritten = %d, want 5", item.TagsWritten)
	}
	if len(item.Changes) != 0 {
		t.Errorf("a refreshed release row must carry no tag diff, got %d changes", len(item.Changes))
	}
}

// TestRetagReleasesRecordsRefreshRows: every release that changed upstream gets one
// detail row, even when it owns no indexed files to re-tag (0 files). The fact that
// the release changed is what the feed must not lose. Using releases with no library
// items keeps the test off disk and the network while still exercising the wiring.
func TestRetagReleasesRecordsRefreshRows(t *testing.T) {
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})

	refreshSet := modules.NewAlbumRefreshSet(nil)
	detail := components.NewDetailCollector(maxDetailItemsRecorded)

	changed := []string{"rel-1", "rel-2"}
	res := r.retagReleases(changed, refreshSet, detail)

	if res.checked != 2 || res.changedReleases != 2 {
		t.Errorf("checked/changedReleases = %d/%d, want 2/2", res.checked, res.changedReleases)
	}
	if len(res.refreshItems) != 2 {
		t.Fatalf("refreshItems = %d, want 2 (one per changed release)", len(res.refreshItems))
	}

	seen := map[string]models.EventItem{}
	for _, item := range res.refreshItems {
		seen[item.Path] = item
	}
	for _, mbID := range changed {
		item, ok := seen[mbID]
		if !ok {
			t.Errorf("no refresh row for release %q", mbID)
			continue
		}
		if item.Status != models.EventItemStatusRefreshed || item.Phase != models.EventItemPhaseRefresh {
			t.Errorf("row for %q = status %q phase %q, want refreshed/refresh", mbID, item.Status, item.Phase)
		}
		if item.TagsWritten != 0 {
			t.Errorf("row for %q re-tagged %d files, want 0 (no indexed files)", mbID, item.TagsWritten)
		}
	}

	// No files existed to re-tag, so no file rows and no re-tag count.
	if res.retagged != 0 {
		t.Errorf("retagged = %d, want 0", res.retagged)
	}
}
