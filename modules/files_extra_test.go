package modules

import (
	"strings"
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
	if !strings.Contains(tags.ISRC, "GBAYE9700340") || !strings.Contains(tags.ISRC, ";") {
		t.Errorf("ISRC = %q, want both codes joined", tags.ISRC)
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
	if !strings.Contains(current.ArtistSemicolon, "Radiohead (current)") {
		t.Errorf("artist semicolon = %q, want current names", current.ArtistSemicolon)
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

// A single-credit track with redundant contributing artists ignored produces an empty
// primary artist tag — the branch the two-credit case above does not reach.
func TestBuildFileTagsIgnoresRedundantSingleArtist(t *testing.T) {
	track := models.Track{
		Position:     1,
		Title:        "Solo",
		ArtistCredit: []models.ArtistCredit{{Name: "Solo Artist", Artist: models.Artist{ID: "a", Name: "Solo Artist"}}},
	}
	resp := models.MusicBrainzReleaseResponse{
		Title:        "Debut",
		ArtistCredit: []models.ArtistCredit{{Name: "Solo Artist", Artist: models.Artist{ID: "a", Name: "Solo Artist"}}},
		Media:        []models.MusicBrainzMedia{{Position: 1, Tracks: []models.Track{track}}},
	}
	resp.ReleaseGroup.PrimaryType = "Album"

	tags, err := BuildFileTags(track, resp.Media[0], resp, models.ConfigStruct{AutotaggerrIgnoreRedundantContributingArtists: true})
	if err != nil {
		t.Fatalf("BuildFileTags: %v", err)
	}
	if tags.Artist != "" {
		t.Errorf("artist = %q, want empty (redundant single contributing artist dropped)", tags.Artist)
	}
}
