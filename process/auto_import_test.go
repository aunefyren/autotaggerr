package process

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// tagFlac writes Vorbis comments to a fixture.
func tagFlac(t *testing.T, path string, tags map[string]string) {
	t.Helper()
	args := []string{}
	for k, v := range tags {
		args = append(args, "--set-tag="+k+"="+v)
	}
	if out, err := exec.Command("metaflac", append(args, path)...).CombinedOutput(); err != nil {
		t.Fatalf("metaflac: %v\n%s", err, out)
	}
}

// autoWorld is the download world with its category set to import automatically, and
// its two files tagged — or not — by tag.
func autoWorld(t *testing.T, tag func(i int, file string)) (importWorld, *fakeDownloadClient) {
	t.Helper()
	w := newImportWorld(t)
	for i, file := range w.sources() {
		tag(i, file)
	}
	fake := &fakeDownloadClient{
		downloads: []modules.Download{{ID: "hash1", Name: "Band - Album", Category: "music", Path: "/downloads/Band - Album", Seeding: true, CompletedAt: time.Now().Add(-time.Hour)}},
		files: map[string][]modules.DownloadFile{"hash1": {
			{Path: "/downloads/Band - Album/01 song one.flac", Complete: true},
			{Path: "/downloads/Band - Album/02 song two.flac", Complete: true},
		}},
	}
	useFakeDownloadClient(t, fake)
	newDownloadClientRow(t, w.db, w.library, w.root)
	w.db.Model(&models.DownloadClientCategory{}).Where("category = ?", "music").Update("auto_import", true)
	return w, fake
}

func taggedWith(t *testing.T, ids ...string) func(int, string) {
	return func(i int, file string) {
		tags := map[string]string{"MUSICBRAINZ_ALBUMID": "rel-1"}
		if i < len(ids) && ids[i] != "" {
			tags["MUSICBRAINZ_RELEASETRACKID"] = ids[i]
		}
		tagFlac(t, file, tags)
	}
}

// TestAutoImportsAnAlbumItsTagsIdentify: every file names the release and its track, so
// the album is imported with nobody reviewing it — copied, tagged, and the download
// marked imported.
func TestAutoImportsAnAlbumItsTagsIdentify(t *testing.T) {
	w, fake := autoWorld(t, taggedWith(t, "trk-1", "trk-2"))
	w.runner.PollDownloads()
	w.runner.waitIdle(t)

	item := downloadItems(t, w.db)["hash1"]
	if item.State != models.DownloadStateImported || item.AutoCheckedAt == nil || len(item.Notes) != 0 {
		t.Fatalf("download = %s groups %v notes %v", item.State, item.Groups, item.Notes)
	}
	if strings.Join(fake.marked, ",") != "hash1" {
		t.Errorf("client told = %v", fake.marked)
	}
	var event models.Event
	w.db.Where("type = ?", models.EventTypeFileImport).First(&event)
	if !strings.HasPrefix(event.Title, "Auto-import Album") {
		t.Errorf("event title = %q", event.Title)
	}
	var pinned int64
	w.db.Model(&models.LibraryItem{}).Where("mb_release_track_id IN ? AND pinned = ?", []string{"trk-1", "trk-2"}, true).Count(&pinned)
	if pinned != 2 {
		t.Errorf("pinned items = %d, want 2", pinned)
	}
}

// The filenames' numbers pair the files with tracks when the tags name only the
// release.
func TestAutoImportPairsByTrackNumber(t *testing.T) {
	w, _ := autoWorld(t, taggedWith(t))
	w.runner.PollDownloads()
	w.runner.waitIdle(t)
	if item := downloadItems(t, w.db)["hash1"]; item.State != models.DownloadStateImported {
		t.Fatalf("download = %s notes %v", item.State, item.Notes)
	}
}

// Anything short of certain is left for review, with the reason, and looked at once.
func TestAutoImportLeavesUncertainAlbumsForReview(t *testing.T) {
	for _, tc := range []struct {
		name string
		tag  func(*testing.T) func(int, string)
		note string
	}{
		{"untagged file", func(t *testing.T) func(int, string) {
			return func(i int, file string) {
				if i == 0 {
					tagFlac(t, file, map[string]string{"MUSICBRAINZ_ALBUMID": "rel-1"})
				}
			}
		}, "1 of 2 files name their MusicBrainz release"},
		{"two releases", func(t *testing.T) func(int, string) {
			return func(i int, file string) {
				tagFlac(t, file, map[string]string{"MUSICBRAINZ_ALBUMID": []string{"rel-1", "rel-2"}[i]})
			}
		}, "more than one release"},
		{"one track twice", func(t *testing.T) func(int, string) { return taggedWith(t, "trk-1", "trk-1") }, "do not each name a different track"},
		{"some track IDs", func(t *testing.T) func(int, string) { return taggedWith(t, "trk-1") }, "do not each name a different track"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, fake := autoWorld(t, tc.tag(t))
			w.runner.PollDownloads()
			w.runner.waitIdle(t)
			item := downloadItems(t, w.db)["hash1"]
			note := item.Notes[item.Path]
			if item.State != models.DownloadStateReady || note == "" || !strings.Contains(note, tc.note) || item.AutoCheckedAt == nil {
				t.Fatalf("download = %s notes %v checked %v", item.State, item.Notes, item.AutoCheckedAt)
			}
			if len(fake.marked) != 0 {
				t.Errorf("client told %v about an album nobody imported", fake.marked)
			}
		})
	}
}

// A file already in the library is a Replace decision, and only a person makes it.
func TestAutoImportNeverReplaces(t *testing.T) {
	w, _ := autoWorld(t, taggedWith(t, "trk-1", "trk-2"))
	existing := filepath.Join(w.library.Path, "Band", "Album (2001)", "01 Song One.flac")
	synthFlacAt(t, existing)
	w.runner.PollDownloads()
	w.runner.waitIdle(t)
	item := downloadItems(t, w.db)["hash1"]
	if item.State != models.DownloadStateReady || !strings.Contains(item.Notes[item.Path], "Replace") {
		t.Fatalf("download = %s notes %v", item.State, item.Notes)
	}
}

// Off by default: a category nobody switched on is never imported without review, and
// its downloads are not marked as looked at — so switching it on later still reaches them.
func TestAutoImportIsOffUnlessSwitchedOn(t *testing.T) {
	w, _ := autoWorld(t, taggedWith(t, "trk-1", "trk-2"))
	w.db.Model(&models.DownloadClientCategory{}).Where("category = ?", "music").Update("auto_import", false)
	w.runner.PollDownloads()
	w.runner.waitIdle(t)
	item := downloadItems(t, w.db)["hash1"]
	if item.State != models.DownloadStateReady || item.AutoCheckedAt != nil {
		t.Fatalf("with auto-import off = %s checked %v", item.State, item.AutoCheckedAt)
	}

	w.db.Model(&models.DownloadClientCategory{}).Where("category = ?", "music").Update("auto_import", true)
	w.runner.PollDownloads()
	w.runner.waitIdle(t)
	if item := downloadItems(t, w.db)["hash1"]; item.State != models.DownloadStateImported {
		t.Errorf("after switching it on = %s notes %v", item.State, item.Notes)
	}
}
