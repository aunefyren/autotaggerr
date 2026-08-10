package modules

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// releaseFixture is a minimal but complete release: one medium, one track, an artist
// credit (BuildFileTags refuses a release without one), and a primary type.
func releaseFixture() models.MusicBrainzReleaseResponse {
	track := models.Track{ID: "trk-1", Position: 1, Title: "Airbag",
		ArtistCredit: []models.ArtistCredit{{Name: "Radiohead", Artist: models.Artist{ID: "art-1", Name: "Radiohead"}}}}
	resp := models.MusicBrainzReleaseResponse{
		ID: "rel-1", Title: "OK Computer", Status: "Official", Date: "1997-06-16",
		ArtistCredit: []models.ArtistCredit{{Name: "Radiohead", Artist: models.Artist{ID: "art-1", Name: "Radiohead"}}},
		Media:        []models.MusicBrainzMedia{{Position: 1, Tracks: []models.Track{track}}},
	}
	resp.ReleaseGroup.PrimaryType = "Album"
	return resp
}

// TagResolvedFile fetches the release for a correlation, finds the matching track, and
// writes the file's tags. This drives the back half of the per-file pipeline end to end
// against a mock MusicBrainz and a real synthesized FLAC.
func TestTagResolvedFileWritesTags(t *testing.T) {
	requireTool(t, "metaflac")
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(releaseFixture())
	})

	path := synthAudio(t, ".flac")
	correlation := models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", Source: models.CorrelationSourceTags}
	refreshSet := NewAlbumRefreshSet(nil)

	unchanged, written, changed, err := TagResolvedFile(path, correlation, nil, refreshSet, t.TempDir(), models.TaggerSettings{})
	if err != nil {
		t.Fatalf("TagResolvedFile: %v", err)
	}
	if unchanged || written == 0 {
		t.Fatalf("first tag write should change the file: unchanged=%v written=%d", unchanged, written)
	}
	if len(changed) == 0 {
		t.Error("expected a field-level diff for the Activity feed")
	}

	// The album title should now be on the file.
	tags, err := getFlacTagsMap(path)
	if err != nil {
		t.Fatalf("getFlacTagsMap: %v", err)
	}
	if got := firstTag(tags, "ALBUM"); got != "OK Computer" {
		t.Errorf("ALBUM = %q, want OK Computer", got)
	}

	// Writing again with the same release is idempotent — nothing to change.
	unchanged2, written2, _, err := TagResolvedFile(path, correlation, nil, refreshSet, t.TempDir(), models.TaggerSettings{})
	if err != nil {
		t.Fatalf("second TagResolvedFile: %v", err)
	}
	if !unchanged2 || written2 != 0 {
		t.Errorf("second identical write should be a no-op: unchanged=%v written=%d", unchanged2, written2)
	}
}

// When the correlation's track ID is not in the fetched release, tagging fails with
// ErrTrackNotInRelease and leaves the file untouched — the "manager's release and track
// mapping disagree" case.
func TestTagResolvedFileTrackNotInRelease(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(releaseFixture())
	})

	correlation := models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "not-a-real-track"}
	_, _, _, err := TagResolvedFile("/music/x.flac", correlation, nil, NewAlbumRefreshSet(nil), "/music", models.TaggerSettings{})
	if err == nil {
		t.Fatal("expected an error when the track is not in the release")
	}
	if !errors.Is(err, ErrTrackNotInRelease) {
		t.Errorf("error = %v, want ErrTrackNotInRelease", err)
	}
}

// A MusicBrainz fetch failure surfaces as a wrapped error rather than a match attempt.
func TestTagResolvedFileFetchError(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	correlation := models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1"}
	_, _, _, err := TagResolvedFile("/music/x.flac", correlation, nil, NewAlbumRefreshSet(nil), "/music", models.TaggerSettings{})
	if err == nil {
		t.Fatal("expected an error when MusicBrainz cannot be fetched")
	}
}
