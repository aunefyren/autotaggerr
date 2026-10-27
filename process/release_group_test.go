package process

import (
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// releaseGroupNaming sets the import world's track file to name the codec, depth and
// release group.
func releaseGroupNaming(t *testing.T, w importWorld) {
	t.Helper()
	if err := w.db.Model(&models.TaggerProfile{}).Where("id = ?", *w.library.TaggerProfileID).
		Update("naming_track_file", "{track:00} {Track Title} [{MediaInfo AudioCodec} {MediaInfo AudioBitsPerSample}]{-Release Group}").Error; err != nil {
		t.Fatal(err)
	}
}

// TestImportNamesAndRecordsTheReleaseGroup: the group the review confirmed is in the
// target the plan shows, in the file's name once imported, and on the index row — and
// the codec and depth are read from the source before it moves.
func TestImportNamesAndRecordsTheReleaseGroup(t *testing.T) {
	w := newImportWorld(t)
	releaseGroupNaming(t, w)

	rows := w.runner.ProposeImport(w.library, w.release, " GRP ", w.sources())
	if want := "Band/Album (2001)/01 Song One [FLAC 16]-GRP.flac"; rows[0].TargetRel != want {
		t.Fatalf("target = %q, want %q", rows[0].TargetRel, want)
	}
	plan := ImportPlan{Library: w.library, Release: w.release, Root: w.root, Mode: modules.TransferCopy, Rows: rows, ReleaseGroup: "GRP"}
	if err := w.runner.Import(plan); err != nil {
		t.Fatal(err)
	}
	w.runner.waitIdle(t)

	var item models.LibraryItem
	if err := w.db.Where("path = ?", rows[0].Target).First(&item).Error; err != nil {
		t.Fatalf("imported file not indexed at its target: %v", err)
	}
	if item.ReleaseGroup == nil || *item.ReleaseGroup != "GRP" {
		t.Errorf("indexed release group = %v, want GRP", item.ReleaseGroup)
	}

	// No group is a group recorded as none: known, and empty.
	w2 := newImportWorld(t)
	releaseGroupNaming(t, w2)
	if rows := w2.runner.ProposeImport(w2.library, w2.release, "", w2.sources()); !strings.HasSuffix(rows[0].TargetRel, "01 Song One [FLAC 16].flac") {
		t.Errorf("no group = %q, want the name without it", rows[0].TargetRel)
	}
}

// TestOrganiseNamesAnUnrecordedReleaseGroupAsNone: a file indexed before release groups
// were recorded is renamed like one recorded as none — the token renders empty and its
// prefix goes with it, Lidarr's rule — rather than left where it is.
func TestOrganiseNamesAnUnrecordedReleaseGroupAsNone(t *testing.T) {
	w := organisedWorld(t)
	releaseGroupNaming(t, w)
	var items []models.LibraryItem
	w.db.Where("library_id = ?", w.library.ID).Order("path").Find(&items)
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	// The import recorded "" for both; forget the first, as an older row would have,
	// and give the second a group.
	w.db.Model(&items[0]).Update("release_group", nil)
	w.db.Model(&items[1]).Update("release_group", "GRP")

	plan, err := w.runner.PlanOrganise(OrganiseScope{LibraryID: w.library.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Skipped) != 0 || len(plan.Moves) != 2 {
		t.Fatalf("plan = %d moves, skipped %+v; want both moved", len(plan.Moves), plan.Skipped)
	}
	if got := plan.Moves[0].ToRel; !strings.HasSuffix(got, "01 Song One [FLAC 16].flac") {
		t.Errorf("unrecorded group = %q, want the name without it", got)
	}
	if got := plan.Moves[1].ToRel; !strings.HasSuffix(got, "02 Song Two [FLAC 16]-GRP.flac") {
		t.Errorf("recorded group = %q", got)
	}
}
