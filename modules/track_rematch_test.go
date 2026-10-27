package modules

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// rebuiltReleaseFixture is rel-1 after MusicBrainz rebuilt its tracklist under the
// same release ID: every track has a new ID, and a bonus track was inserted at the
// front, so positions moved too. trk-1 / trk-2 exist nowhere in it.
func rebuiltReleaseFixture() models.MusicBrainzReleaseResponse {
	resp := releaseFixture()
	credit := resp.Media[0].Tracks[0].ArtistCredit
	bonus := models.Track{ID: "new-0", Position: 1, Title: "Intro", ArtistCredit: credit}
	bonus.Recording.ID = "rec-intro"
	airbag := models.Track{ID: "new-1", Position: 2, Title: "Airbag", ArtistCredit: credit}
	airbag.Recording.ID = "rec-1"
	android := models.Track{ID: "new-2", Position: 3, Title: "Paranoid Android", ArtistCredit: credit}
	android.Recording.ID = "rec-2-rebuilt"
	resp.Media[0].Tracks = []models.Track{bonus, airbag, android}
	return resp
}

// taggedFile writes an MP3 carrying its own disc, track number and title. ID3 is read
// and written in-process, so unlike the FLAC fixtures this needs no ffmpeg — the
// re-match reads tags, not audio.
func taggedFile(t *testing.T, disc, track, title string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := SetMP3Tags(path, models.FileTags{DiscNumber: disc, Track: track, Title: title}, models.TaggerSettings{}); err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}
	return path
}

func TestRematchRebuiltTrack(t *testing.T) {
	release := rebuiltReleaseFixture()

	repeated := rebuiltReleaseFixture()
	repeated.Media[0].Tracks[2].Recording.ID = "rec-1"

	cases := []struct {
		name        string
		release     models.MusicBrainzReleaseResponse
		disc, track string
		title       string
		correlation models.Correlation
		want        string // "" = refused
	}{
		{"by recording, though the position moved", release, "1", "1", "Airbag",
			models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1"}, "new-1"},
		{"by position, confirmed by title, recording re-issued", release, "1", "3", "Paranoid Android",
			models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-2", MBRecordingID: "rec-2"}, "new-2"},
		{"position names a track with another title", release, "1", "2", "Paranoid Android",
			models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-2", MBRecordingID: "rec-2"}, ""},
		{"repeated recording, position and title agree", repeated, "1", "2", "Airbag",
			models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1"}, "new-1"},
		{"repeated recording, nothing else to go on", repeated, "", "", "Airbag",
			models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1"}, ""},
		{"no such disc", release, "2", "3", "Paranoid Android",
			models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-2"}, ""},
		{"track number with a total", release, "1/1", "3/3", "paranoid android",
			models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-2"}, "new-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := taggedFile(t, tc.disc, tc.track, tc.title)
			track, _, ok := rematchRebuiltTrack(path, tc.release, tc.correlation)
			if tc.want == "" {
				if ok {
					t.Fatalf("re-matched to %s, want a refusal", track.ID)
				}
				return
			}
			if !ok || track.ID != tc.want {
				t.Fatalf("got (%s, %v), want %s", track.ID, ok, tc.want)
			}
		})
	}
}

// End to end for a file identified by its own tags: the write goes ahead under the
// rebuilt track, and the correlation comes back carrying the new IDs and the source
// that says how they were found.
func TestTagCorrelatedFileRematchesARebuiltRelease(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rebuiltReleaseFixture())
	})
	path := taggedFile(t, "1", "3", "Paranoid Android")
	correlation := models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-2", MBRecordingID: "rec-2", Source: models.CorrelationSourceTags}

	if _, _, _, err := TagCorrelatedFile(path, &correlation, nil, NewAlbumRefreshSet(), t.TempDir(), models.TaggerSettings{}); err != nil {
		t.Fatalf("TagCorrelatedFile: %v", err)
	}
	if correlation.MBReleaseTrackID != "new-2" || correlation.MBRecordingID != "rec-2-rebuilt" || correlation.Source != models.CorrelationSourceRematched {
		t.Errorf("correlation = %+v, want new-2 / rec-2-rebuilt / rematched", correlation)
	}
	got, err := ExtractMusicBrainzTrackID(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "new-2" {
		t.Errorf("file's release track ID = %q, want new-2", got)
	}
}

// Who may be re-matched: never a manual correlation (a person chose it), and never a
// manager's (it hands out a fresh pair on every pass, so guessing is never needed).
func TestTagCorrelatedFileDoesNotRematchManagerOrManualCorrelations(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rebuiltReleaseFixture())
	})
	for _, source := range []string{models.CorrelationSourceLidarr, models.CorrelationSourceManual} {
		path := taggedFile(t, "1", "3", "Paranoid Android")
		correlation := models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-2", MBRecordingID: "rec-1", Source: source}
		_, _, _, err := TagCorrelatedFile(path, &correlation, nil, NewAlbumRefreshSet(), t.TempDir(), models.TaggerSettings{})
		if !errors.Is(err, ErrTrackNotInRelease) {
			t.Errorf("%s: err = %v, want ErrTrackNotInRelease", source, err)
		}
		if correlation.MBReleaseTrackID != "trk-2" {
			t.Errorf("%s: correlation changed to %+v", source, correlation)
		}
	}
}
