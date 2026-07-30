package modules

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// testRelease builds a MusicBrainz release whose media/tracks contain the given
// track IDs — enough for ProcessTrackFile to match and build tags.
func testRelease(trackIDs ...string) models.MusicBrainzReleaseResponse {
	credit := []models.ArtistCredit{{Name: "Test Artist", Artist: models.Artist{ID: "art-1", Name: "Test Artist"}}}
	tracks := make([]models.Track, 0, len(trackIDs))
	for i, id := range trackIDs {
		tracks = append(tracks, models.Track{
			ID:           id,
			Title:        "Track " + id,
			Position:     i + 1,
			ArtistCredit: credit,
		})
	}
	return models.MusicBrainzReleaseResponse{
		ID:           "rel-123",
		Title:        "Test Album",
		ArtistCredit: credit,
		ReleaseGroup: models.ReleaseGroup{PrimaryType: "Album"},
		Media:        []models.MusicBrainzMedia{{Tracks: tracks}},
	}
}

// seedFlac synthesizes a FLAC and embeds the MB release/track IDs that the
// file-tag fallback path of ProcessTrackFile reads.
func seedFlac(t *testing.T, path, releaseID, trackID string) {
	t.Helper()
	requireTool(t, "metaflac")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// synthesize into place
	synthInto(t, path)
	seed := models.FileTags{
		MBAlbumID:        releaseID,
		MBReleaseTrackID: trackID,
		MBRecordingID:    "rec-" + trackID,
		Title:            "Seed Title",
		Album:            "Seed Album",
		Artist:           "Seed Artist",
	}
	if _, _, _, err := SetFlacTags(path, seed, models.ConfigStruct{}); err != nil {
		t.Fatalf("seed SetFlacTags: %v", err)
	}
}

// synthInto writes a tiny silent FLAC to an explicit path.
func synthInto(t *testing.T, path string) {
	t.Helper()
	requireTool(t, "ffmpeg")
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "0.1", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg synth failed: %v\n%s", err, out)
	}
}

func TestProcessTrackFileFromFileTags(t *testing.T) {
	requireTool(t, "metaflac")
	root := t.TempDir()
	path := filepath.Join(root, "Test Artist", "Test Album (2020)", "01 track.flac")
	seedFlac(t, path, "rel-123", "trk-1")

	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(testRelease("trk-1"))
	})

	set := NewAlbumRefreshSet(nil)
	unchanged, written, err := ProcessTrackFile(path, nil, nil, set, root, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("ProcessTrackFile: %v", err)
	}
	if unchanged || written == 0 {
		t.Fatalf("first pass should write tags, got unchanged=%v written=%d", unchanged, written)
	}

	// The album/title from MusicBrainz should now be on the file.
	tags, err := getFlacTagsMap(path)
	if err != nil {
		t.Fatal(err)
	}
	if firstTag(tags, "ALBUM") != "Test Album" {
		t.Errorf("ALBUM = %q, want Test Album", firstTag(tags, "ALBUM"))
	}
	if firstTag(tags, "TITLE") != "Track trk-1" {
		t.Errorf("TITLE = %q, want 'Track trk-1'", firstTag(tags, "TITLE"))
	}

	// Second pass over an already-tagged file must be a no-op.
	unchanged2, written2, err := ProcessTrackFile(path, nil, nil, set, root, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("second ProcessTrackFile: %v", err)
	}
	if !unchanged2 || written2 != 0 {
		t.Errorf("second pass should be unchanged, got unchanged=%v written=%d", unchanged2, written2)
	}
}

func TestScanFolderRecursive(t *testing.T) {
	requireTool(t, "metaflac")
	root := t.TempDir()
	albumDir := filepath.Join(root, "Test Artist", "Test Album (2020)")
	seedFlac(t, filepath.Join(albumDir, "01 one.flac"), "rel-123", "trk-1")
	seedFlac(t, filepath.Join(albumDir, "02 two.flac"), "rel-123", "trk-2")
	// a non-audio file must be ignored entirely
	if err := os.WriteFile(filepath.Join(albumDir, "cover.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(testRelease("trk-1", "trk-2"))
	})

	cfg := models.ConfigStruct{AutotaggerrProcessConcurrency: 2}

	counter, unchanged, tagsWritten, errorFiles, _, err := ScanFolderRecursive(root, nil, nil, nil, cfg)
	if err != nil {
		t.Fatalf("ScanFolderRecursive: %v", err)
	}
	if counter != 2 {
		t.Errorf("processed count = %d, want 2", counter)
	}
	if len(errorFiles) != 0 {
		t.Errorf("unexpected error files: %v", errorFiles)
	}
	if unchanged != 0 {
		t.Errorf("first scan unchanged = %d, want 0", unchanged)
	}
	if tagsWritten == 0 {
		t.Error("first scan should have written tags")
	}

	// A second scan should find everything already tagged.
	counter2, unchanged2, tagsWritten2, _, _, err := ScanFolderRecursive(root, nil, nil, nil, cfg)
	if err != nil {
		t.Fatalf("second ScanFolderRecursive: %v", err)
	}
	if counter2 != 2 || unchanged2 != 2 || tagsWritten2 != 0 {
		t.Errorf("second scan = (count %d, unchanged %d, written %d), want (2, 2, 0)", counter2, unchanged2, tagsWritten2)
	}
}
