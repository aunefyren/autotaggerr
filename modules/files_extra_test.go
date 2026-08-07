package modules

import (
	"slices"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// The extract/set/diff dispatchers all reject an unsupported extension before touching
// disk, so the refusal is deterministic and needs no fixture file.
func TestTagDispatchersRejectUnsupportedExtension(t *testing.T) {
	const path = "/music/Artist/Album (2020)/notes.txt"

	for name, fn := range map[string]func(string) (string, error){
		"release":   ExtractMusicBrainzReleaseID,
		"track":     ExtractMusicBrainzTrackID,
		"recording": ExtractMusicBrainzRecordingID,
		"title":     ExtractTrackTitle,
	} {
		if _, err := fn(path); err == nil {
			t.Errorf("%s extractor accepted an unsupported extension", name)
		}
	}

	if _, _, _, err := SetFileTags(path, models.FileTags{}, models.ConfigStruct{}); err == nil {
		t.Error("SetFileTags accepted an unsupported extension")
	}
	if _, err := DiffFileTags(path, models.FileTags{}, models.ConfigStruct{}); err == nil {
		t.Error("DiffFileTags accepted an unsupported extension")
	}
}

// With tag fallback allowed and no manager, resolution reads the file's own tags —
// which fails for an unsupported extension. This exercises the fallback branch that the
// unmatched test deliberately skips.
func TestResolveCorrelationTagFallbackUnsupported(t *testing.T) {
	if _, err := ResolveCorrelation("/music/Artist/Album/track.txt", nil, "/music", true); err == nil {
		t.Error("expected an extraction error for an unsupported file with tag fallback")
	}
}

// BuildFileTags turns a MusicBrainz release + track into the tag set written to disk.
// It is pure, so the interesting cases are the credit/config permutations and the
// missing-album-artist refusal — no file or network needed.
func TestBuildFileTags(t *testing.T) {
	track := models.Track{
		Position: 3,
		Title:    "Paranoid Android",
		ArtistCredit: []models.ArtistCredit{
			{Name: "Radiohead", Joinphrase: " feat. ", Artist: models.Artist{ID: "art-1", Name: "Radiohead (current)"}},
			{Name: "Guest", Artist: models.Artist{ID: "art-2", Name: "Guest"}},
		},
	}
	track.Recording.ISRCs = []string{"GBAYE9700340", "GBAYE9700341"}

	resp := models.MusicBrainzReleaseResponse{
		Title:        "OK Computer",
		Status:       "Official",
		Date:         "1997-06-16",
		ArtistCredit: []models.ArtistCredit{{Name: "Radiohead", Artist: models.Artist{ID: "art-1", Name: "Radiohead (current)"}}},
		Media: []models.MusicBrainzMedia{
			{Position: 1, Tracks: []models.Track{track, {Title: "b"}}},
			{Position: 2, Tracks: []models.Track{{Title: "c"}}},
		},
	}
	resp.ReleaseGroup.PrimaryType = "Album"
	resp.ReleaseGroup.FirstReleaseDate = "1997-05-21"

	// Original names, keeping every contributing artist.
	tags, err := BuildFileTags(track, resp.Media[0], resp, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("BuildFileTags: %v", err)
	}
	if tags.Album != "OK Computer" || tags.Title != "Paranoid Android" {
		t.Errorf("album/title = %q/%q", tags.Album, tags.Title)
	}
	if tags.AlbumArtist != "Radiohead" { // original name, not the "(current)" one
		t.Errorf("album artist = %q, want the original credit name", tags.AlbumArtist)
	}
	if tags.Track != "3" || tags.TrackTotal != "2" || tags.DiscNumber != "1" || tags.DiscTotal != "2" {
		t.Errorf("positions = track %q/%q disc %q/%q", tags.Track, tags.TrackTotal, tags.DiscNumber, tags.DiscTotal)
	}
	if tags.ReleaseYear != "1997" || tags.OriginalYear != "1997" {
		t.Errorf("years = release %q original %q", tags.ReleaseYear, tags.OriginalYear)
	}
	if len(tags.ISRCs) != 2 || tags.ISRCs[0] != "GBAYE9700340" {
		t.Errorf("ISRCs = %v, want both codes as separate values", tags.ISRCs)
	}
	if tags.MBAlbumType != "album" || tags.MBAlbumStatus != "official" {
		t.Errorf("album type/status = %q/%q", tags.MBAlbumType, tags.MBAlbumStatus)
	}

	// Current artist name selected, and redundant contributing artists dropped for a
	// single-credit track (here still two credits, so the artist string is kept).
	currentCfg := models.ConfigStruct{AutotaggerrUseCurrentArtistName: true}
	current, err := BuildFileTags(track, resp.Media[0], resp, currentCfg)
	if err != nil {
		t.Fatalf("BuildFileTags (current names): %v", err)
	}
	if current.AlbumArtist != "Radiohead (current)" {
		t.Errorf("album artist = %q, want the current name", current.AlbumArtist)
	}
	if !slices.Contains(current.Artists, "Radiohead (current)") {
		t.Errorf("artists = %v, want the current names", current.Artists)
	}

	// A blank date leaves the year fields empty rather than erroring.
	noDate := resp
	noDate.Date = ""
	noDate.ReleaseGroup.FirstReleaseDate = ""
	if plain, err := BuildFileTags(track, resp.Media[0], noDate, models.ConfigStruct{}); err != nil {
		t.Fatalf("BuildFileTags (no date): %v", err)
	} else if plain.ReleaseYear != "" || plain.OriginalYear != "" {
		t.Errorf("years should be empty for a blank date: %q/%q", plain.ReleaseYear, plain.OriginalYear)
	}

	// No album artist credit at all is a refusal — the album artist is not optional.
	noArtist := resp
	noArtist.ArtistCredit = nil
	if _, err := BuildFileTags(track, resp.Media[0], noArtist, models.ConfigStruct{}); err == nil {
		t.Error("BuildFileTags should refuse a release with no album artist")
	}
}

// TestBuildFileTagsRedundantArtist covers what "redundant" means: the track credit
// says nothing the album artist does not already say. It is decided by comparing the
// two strings, so a lone track artist who is *not* the album artist (a compilation, or
// a guest-credited track on someone else's release) keeps its ARTIST tag.
func TestBuildFileTagsRedundantArtist(t *testing.T) {
	credit := func(name string) []models.ArtistCredit {
		return []models.ArtistCredit{{Name: name, Artist: models.Artist{ID: name, Name: name}}}
	}

	cases := []struct {
		name        string
		trackArtist string
		albumArtist string
		ignore      bool
		want        string
	}{
		{
			name:        "same artist is redundant",
			trackArtist: "Solo Artist", albumArtist: "Solo Artist", ignore: true, want: "",
		},
		{
			name:        "a different lone artist is not redundant",
			trackArtist: "Baby Keem", albumArtist: "Kendrick Lamar", ignore: true, want: "Baby Keem",
		},
		{
			name:        "punctuation and accents do not make it non-redundant",
			trackArtist: "Beyoncé", albumArtist: "Beyonce", ignore: true, want: "",
		},
		{
			name:        "the setting off always writes the track artist",
			trackArtist: "Solo Artist", albumArtist: "Solo Artist", ignore: false, want: "Solo Artist",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			track := models.Track{Position: 1, Title: "Solo", ArtistCredit: credit(c.trackArtist)}
			resp := models.MusicBrainzReleaseResponse{
				Title:        "Debut",
				ArtistCredit: credit(c.albumArtist),
				Media:        []models.MusicBrainzMedia{{Position: 1, Tracks: []models.Track{track}}},
			}
			resp.ReleaseGroup.PrimaryType = "Album"

			tags, err := BuildFileTags(track, resp.Media[0], resp,
				models.ConfigStruct{AutotaggerrIgnoreRedundantContributingArtists: c.ignore})
			if err != nil {
				t.Fatalf("BuildFileTags: %v", err)
			}
			if tags.Artist != c.want {
				t.Errorf("artist = %q, want %q", tags.Artist, c.want)
			}
			// Whatever ARTIST does, the full credit list stays on the file.
			if len(tags.Artists) != 1 || tags.Artists[0] != c.trackArtist {
				t.Errorf("artists = %v, want [%s]", tags.Artists, c.trackArtist)
			}
		})
	}
}
