package modules

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// mergedReleaseFixture is the survivor of a release merge: two tracks whose IDs are
// the survivor's own, so a correlation carrying the merged-away release's track IDs
// matches neither by ID.
func mergedReleaseFixture() models.MusicBrainzReleaseResponse {
	resp := releaseFixture()
	first := resp.Media[0].Tracks[0]
	first.Recording.ID = "rec-1"
	second := models.Track{ID: "trk-2", Position: 2, Title: "Paranoid Android", ArtistCredit: first.ArtistCredit}
	second.Recording.ID = "rec-2"
	resp.Media[0].Tracks = []models.Track{first, second}
	return resp
}

func TestMatchCorrelatedTrack(t *testing.T) {
	survivor := mergedReleaseFixture()

	repeated := mergedReleaseFixture()
	repeated.Media[0].Tracks[1].Recording.ID = "rec-1"
	repeated.Media[0].Tracks[1].Title = "Airbag"

	cases := []struct {
		name        string
		release     models.MusicBrainzReleaseResponse
		correlation models.Correlation
		wantTrack   string // "" = no match
	}{
		{"exact track ID", survivor,
			models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "trk-2"}, "trk-2"},
		// Fetched under its own ID, a miss is drift, not a merge: nothing to fall back on.
		{"same release, unknown track, no fallback", survivor,
			models.Correlation{MBReleaseID: "rel-1", MBReleaseTrackID: "old-trk", MBRecordingID: "rec-2"}, ""},
		{"merged release, matched by recording", survivor,
			models.Correlation{MBReleaseID: "rel-old", MBReleaseTrackID: "old-trk", MBRecordingID: "rec-2", TrackTitle: "Airbag"}, "trk-2"},
		{"merged release, recording gone, matched by title", survivor,
			models.Correlation{MBReleaseID: "rel-old", MBReleaseTrackID: "old-trk", MBRecordingID: "rec-old", TrackTitle: "paranoid android"}, "trk-2"},
		{"merged release, nothing matches", survivor,
			models.Correlation{MBReleaseID: "rel-old", MBReleaseTrackID: "old-trk", MBRecordingID: "rec-old", TrackTitle: "Lucky"}, ""},
		// A repeated recording/title is the multi-disc ambiguity; refuse rather than pick.
		{"merged release, ambiguous", repeated,
			models.Correlation{MBReleaseID: "rel-old", MBReleaseTrackID: "old-trk", MBRecordingID: "rec-1", TrackTitle: "Airbag"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			track, _, ok := matchCorrelatedTrack(tc.release, tc.correlation)
			if tc.wantTrack == "" {
				if ok {
					t.Fatalf("matched %s, want no match", track.ID)
				}
				return
			}
			if !ok || track.ID != tc.wantTrack {
				t.Fatalf("got (%s, %v), want %s", track.ID, ok, tc.wantTrack)
			}
		})
	}
}

// The case seen in production: the manager still reports the merged-away release and
// its track IDs, MusicBrainz answers with the survivor, and the file is tagged with
// the survivor's IDs rather than failing on every pass.
func TestTagResolvedFileMergedRelease(t *testing.T) {
	requireTool(t, "metaflac")
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(mergedReleaseFixture())
	})

	path := synthAudio(t, ".flac")
	correlation := models.Correlation{MBReleaseID: "rel-old", MBReleaseTrackID: "old-trk", MBRecordingID: "rec-2", Source: models.CorrelationSourceLidarr}
	if _, _, _, err := TagResolvedFile(path, correlation, nil, NewAlbumRefreshSet(nil), t.TempDir(), models.TaggerSettings{}); err != nil {
		t.Fatalf("TagResolvedFile: %v", err)
	}

	tags, err := getFlacTagsMap(path)
	if err != nil {
		t.Fatalf("getFlacTagsMap: %v", err)
	}
	if got := firstTag(tags, "MUSICBRAINZ_RELEASETRACKID"); got != "trk-2" {
		t.Errorf("MUSICBRAINZ_RELEASETRACKID = %q, want the survivor's trk-2", got)
	}
	if got := firstTag(tags, "MUSICBRAINZ_ALBUMID"); got != "rel-1" {
		t.Errorf("MUSICBRAINZ_ALBUMID = %q, want the survivor's rel-1", got)
	}
}

// A merged release with no recognisable track still fails, but says why — and does not
// send the user to re-correlate, which would only fetch the same stale pair again.
func TestTagResolvedFileMergedReleaseNoMatch(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(mergedReleaseFixture())
	})

	correlation := models.Correlation{MBReleaseID: "rel-old", MBReleaseTrackID: "old-trk", MBRecordingID: "rec-old", TrackTitle: "Lucky"}
	_, _, _, err := TagResolvedFile("/music/x.flac", correlation, nil, NewAlbumRefreshSet(nil), "/music", models.TaggerSettings{})
	if !errors.Is(err, ErrTrackNotInRelease) {
		t.Fatalf("error = %v, want ErrTrackNotInRelease", err)
	}
	if !strings.Contains(err.Error(), "merged into rel-1") || strings.Contains(err.Error(), "re-correlate") {
		t.Errorf("error should name the merge and not suggest re-correlating: %v", err)
	}
}
