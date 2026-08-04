package modules

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// multiDiscReleaseFixture is the shape that causes the endless-retag loop: a
// two-medium release whose disc 1 and disc 2 both open with the same recording at
// position 1. Only the file's folder can say which of the two a given file is.
//
// Modelled on the real report: an expanded soundtrack whose disc 1 is the long score
// (2 tracks here) and whose disc 2 is the original album (1 track), both starting with
// "Opening Titles".
func multiDiscReleaseFixture() models.MusicBrainzReleaseResponse {
	credit := []models.ArtistCredit{{Name: "John Williams", Artist: models.Artist{ID: "art-1", Name: "John Williams"}}}

	discOneTrackOne := models.Track{ID: "trk-d1-1", Position: 1, Title: "Opening Titles", ArtistCredit: credit}
	discOneTrackOne.Recording.ID = "rec-opening"
	discOneTrackTwo := models.Track{ID: "trk-d1-2", Position: 2, Title: "Journey to the Island", ArtistCredit: credit}
	discOneTrackTwo.Recording.ID = "rec-journey"
	discTwoTrackOne := models.Track{ID: "trk-d2-1", Position: 1, Title: "Opening Titles", ArtistCredit: credit}
	discTwoTrackOne.Recording.ID = "rec-opening"

	resp := models.MusicBrainzReleaseResponse{
		ID: "rel-jp", Title: "Jurassic Park", Status: "Official", Date: "1993-01-01",
		ArtistCredit: credit,
		Media: []models.MusicBrainzMedia{
			{Position: 1, Tracks: []models.Track{discOneTrackOne, discOneTrackTwo}},
			{Position: 2, Tracks: []models.Track{discTwoTrackOne}},
		},
	}
	resp.ReleaseGroup.PrimaryType = "Album"
	return resp
}

// TestTagResolvedFileRefusesWrongDisc is the production bug, end to end: a file living
// in "CD 02" is handed disc 1's track. Writing it would set DISCNUMBER=1 and
// TRACKTOTAL to disc 1's count on a file the next resolution puts back on disc 2 — the
// loop. The tagging path must refuse instead of writing.
func TestTagResolvedFileRefusesWrongDisc(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(multiDiscReleaseFixture())
	})

	root := t.TempDir()
	path := filepath.Join(root, "John Williams", "Jurassic Park (1993)", "CD 02",
		"John Williams - Jurassic Park - 01 Opening Titles.flac")
	correlation := models.Correlation{
		MBReleaseID:      "rel-jp",
		MBReleaseTrackID: "trk-d1-1", // disc 1's copy, for a file that sits on disc 2
		Source:           models.CorrelationSourceLidarr,
	}

	_, written, changed, err := TagResolvedFile(path, correlation, nil, NewAlbumRefreshSet(nil), root, models.ConfigStruct{})
	if !errors.Is(err, ErrDiscMismatch) {
		t.Fatalf("error = %v, want ErrDiscMismatch", err)
	}
	if written != 0 || len(changed) != 0 {
		t.Errorf("nothing may be written when the disc disagrees: written=%d changed=%d", written, len(changed))
	}
	// The message has to name both discs, or the user cannot tell which side is wrong.
	for _, want := range []string{"disc 2", "disc 1", "rel-jp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err.Error(), want)
		}
	}
}

// TestTagResolvedFileAcceptsMatchingDisc is the same release resolved correctly: the
// guard must be silent, or every multi-disc file in the library stops tagging.
func TestTagResolvedFileAcceptsMatchingDisc(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(multiDiscReleaseFixture())
	})

	root := t.TempDir()
	path := filepath.Join(root, "John Williams", "Jurassic Park (1993)", "CD 02", "01 Opening Titles.flac")
	correlation := models.Correlation{MBReleaseID: "rel-jp", MBReleaseTrackID: "trk-d2-1", Source: models.CorrelationSourceLidarr}

	// The file does not exist, so tagging fails further down — the point is only that
	// it got past the disc check.
	_, _, _, err := TagResolvedFile(path, correlation, nil, NewAlbumRefreshSet(nil), root, models.ConfigStruct{})
	if errors.Is(err, ErrDiscMismatch) {
		t.Fatalf("disc 2 file resolved to the disc 2 track must not be refused: %v", err)
	}
}

func TestVerifyDiscFolder(t *testing.T) {
	release := multiDiscReleaseFixture()
	discOne, discTwo := release.Media[0], release.Media[1]

	// A release where the folder numbering legitimately disagrees with MusicBrainz:
	// medium 1 is a bonus disc, so the "CD 2" folder really is medium 2's audio and
	// medium 1 holds nothing that looks like it.
	offsetRelease := models.MusicBrainzReleaseResponse{
		ID: "rel-offset",
		Media: []models.MusicBrainzMedia{
			{Position: 1, Tracks: []models.Track{{ID: "trk-dvd-1", Position: 1, Title: "Behind the Scenes"}}},
			{Position: 2, Tracks: []models.Track{{ID: "trk-cd-1", Position: 1, Title: "Opening Titles"}}},
		},
	}

	singleDisc := releaseFixture()

	cases := []struct {
		name        string
		path        string
		correlation models.Correlation
		track       models.Track
		media       models.MusicBrainzMedia
		release     models.MusicBrainzReleaseResponse
		wantRefusal bool
	}{
		{
			name:        "look-alike on the folder's disc is refused",
			path:        "/music/A/Album (1993)/CD 02/01 Opening Titles.flac",
			correlation: models.Correlation{Source: models.CorrelationSourceLidarr},
			track:       discOne.Tracks[0], media: discOne, release: release,
			wantRefusal: true,
		},
		{
			name:        "the disc the folder names is accepted",
			path:        "/music/A/Album (1993)/CD 02/01 Opening Titles.flac",
			correlation: models.Correlation{Source: models.CorrelationSourceLidarr},
			track:       discTwo.Tracks[0], media: discTwo, release: release,
		},
		{
			name:        "a manual correlation outranks the folder",
			path:        "/music/A/Album (1993)/CD 02/01 Opening Titles.flac",
			correlation: models.Correlation{Source: models.CorrelationSourceManual},
			track:       discOne.Tracks[0], media: discOne, release: release,
		},
		{
			name:        "no disc folder, no opinion",
			path:        "/music/A/Album (1993)/01 Opening Titles.flac",
			correlation: models.Correlation{Source: models.CorrelationSourceLidarr},
			track:       discOne.Tracks[0], media: discOne, release: release,
		},
		{
			name:        "different position on the folder's disc is not a look-alike",
			path:        "/music/A/Album (1993)/CD 02/02 Journey to the Island.flac",
			correlation: models.Correlation{Source: models.CorrelationSourceLidarr},
			track:       discOne.Tracks[1], media: discOne, release: release,
		},
		{
			name:        "folder numbering may legitimately differ from medium numbering",
			path:        "/music/A/Album (1993)/CD 1/01 Opening Titles.flac",
			correlation: models.Correlation{Source: models.CorrelationSourceLidarr},
			track:       offsetRelease.Media[1].Tracks[0], media: offsetRelease.Media[1], release: offsetRelease,
		},
		{
			name:        "single-medium release ignores the folder entirely",
			path:        "/music/A/Album (1997)/CD 2/01 Airbag.flac",
			correlation: models.Correlation{Source: models.CorrelationSourceLidarr},
			track:       singleDisc.Media[0].Tracks[0], media: singleDisc.Media[0], release: singleDisc,
		},
		{
			name:        "a disc the release does not have is not evidence",
			path:        "/music/A/Album (1993)/CD 3/01 Opening Titles.flac",
			correlation: models.Correlation{Source: models.CorrelationSourceLidarr},
			track:       discOne.Tracks[0], media: discOne, release: release,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := verifyDiscFolder(c.path, c.correlation, c.track, c.media, c.release)
			if c.wantRefusal && !errors.Is(err, ErrDiscMismatch) {
				t.Fatalf("err = %v, want ErrDiscMismatch", err)
			}
			if !c.wantRefusal && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}
