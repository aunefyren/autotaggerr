package modules

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func renderSample(t *testing.T, templates models.NamingTemplates, sample NamingSample) string {
	t.Helper()
	path, err := RenderLibraryPath(templates, sample.Release, sample.TrackID, sample.File)
	if err != nil {
		t.Fatalf("render %s: %v", sample.Label, err)
	}
	return filepath.ToSlash(path.Rel())
}

// The defaults are the layout convention, so an unedited profile proposes the paths
// a conventionally laid-out library already has.
func TestRenderLibraryPathDefaults(t *testing.T) {
	want := []string{
		"Fleetwood Mac/Rumours (1977)/02 Dreams.flac",
		"The Beatles/The Beatles (1968)/CD 02/01 Birthday.flac",
		"AC+DC/Back in Black (1980)/01 Hells Bells.mp3",
		"Metallica/And Justice for All (1988)/02 ...And Justice for All.flac", // dots are only trimmed at the start of a name
	}
	for i, sample := range NamingSamples() {
		if got := renderSample(t, models.NamingTemplates{}, sample); got != want[i] {
			t.Errorf("%s: got %q, want %q", sample.Label, got, want[i])
		}
	}
}

// Lidarr's own default naming pastes in, validates and renders what Lidarr would.
func TestLidarrDefaultNamingIsAccepted(t *testing.T) {
	lidarr := models.NamingTemplates{
		ArtistFolder: "{Artist Name}",
		AlbumFolder:  "{Album Title} ({Release Year})",
		MediaFolder:  "{Medium Format} {medium:00}",
		TrackFile:    "{Artist Name} - {Album Title} - {track:00} - {Track Title}",
	}
	check := ValidateNamingTemplates(lidarr)
	if !check.OK() {
		t.Fatalf("Lidarr's defaults rejected: %v", check.Errors)
	}
	if check.Warnings[NamingTrackFile] == "" {
		t.Error("a track file that does not start with its number should warn about folder attach")
	}
	got := renderSample(t, lidarr, NamingSamples()[1])
	if want := "The Beatles/The Beatles (1968)/CD 02/The Beatles - The Beatles - 01 - Birthday.flac"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultNamingValidatesClean(t *testing.T) {
	check := ValidateNamingTemplates(models.DefaultNamingTemplates())
	if !check.OK() || len(check.Warnings) > 0 {
		t.Fatalf("defaults should validate without errors or warnings: %+v", check)
	}
}

// A CD+DVD whose DVD is all video is a one-disc album to an audio library.
func TestRenderLibraryPathIgnoresVideoMedia(t *testing.T) {
	release := sampleRumours
	dvd := models.MusicBrainzMedia{Position: 2, Format: "DVD", Tracks: []models.Track{sampleTrack("video", 1, "Documentary", nil)}}
	dvd.Tracks[0].Recording.Video = true
	release.Media = append(append([]models.MusicBrainzMedia{}, release.Media...), dvd)

	path, err := RenderLibraryPath(models.NamingTemplates{}, release, sampleID("32"), NamingFile{Ext: ".flac"})
	if err != nil {
		t.Fatal(err)
	}
	if path.Media != "" {
		t.Errorf("an all-video medium should not split the album into disc folders, got %q", path.Media)
	}

	// The video track's own medium still counts, so a file attached to it is not
	// put beside the audio as if it were track 1 of disc 1.
	path, err = RenderLibraryPath(models.NamingTemplates{}, release, "video", NamingFile{Ext: ".mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if path.Media != "CD 02" {
		t.Errorf("a file on the video medium should get its own folder, got %q", path.Media)
	}
}

func TestNamingTokenSpellings(t *testing.T) {
	sample := NamingSamples()[1] // The Beatles, disc 2
	cases := []struct {
		name, template, want string
	}{
		{"lower case renders lower", "{artist name}", "the beatles"},
		{"upper case renders upper", "{ARTIST NAME}", "THE BEATLES"},
		{"a separator replaces spaces", "{Artist.Name}", "The.Beatles"},
		{"underscore separator", "{Artist_Name}", "The_Beatles"},
		{"sort name", "{Artist SortName}", "Beatles, The"},
		{"empty prefixed token vanishes", "{Artist Name}{ (Artist Disambiguation)}", "The Beatles"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, err := RenderLibraryPath(models.NamingTemplates{ArtistFolder: tc.template}, sample.Release, sample.TrackID, sample.File)
			if err != nil {
				t.Fatal(err)
			}
			if path.Artist != tc.want {
				t.Errorf("got %q, want %q", path.Artist, tc.want)
			}
		})
	}
}

func TestNamingPrefixShownWhenValuePresent(t *testing.T) {
	release := sampleRumours
	release.ReleaseGroup.Disambiguation = "deluxe"
	path, err := RenderLibraryPath(models.NamingTemplates{AlbumFolder: "{Album Title}{ [Album Disambiguation]}"}, release, sampleID("32"), NamingFile{Ext: ".flac"})
	if err != nil {
		t.Fatal(err)
	}
	if path.Album != "Rumours [deluxe]" {
		t.Errorf("got %q", path.Album)
	}
}

// The album's year, not the edition's, is {Release Year} — Lidarr's meaning — and the
// album folder follows the release-group's artist, not a pressing's.
func TestNamingAlbumTokensReadTheReleaseGroup(t *testing.T) {
	release := sampleRumours
	release.Date = "2004-03-22"
	release.Title = "Rumours (remaster)"
	release.ArtistCredit = sampleArtist("old", "Fleetwood Mac (old credit)", "")
	templates := models.NamingTemplates{AlbumFolder: "{Album Title} ({Release Year}) [{Edition Title}, {Edition Year}]"}

	path, err := RenderLibraryPath(templates, release, sampleID("32"), NamingFile{Ext: ".flac"})
	if err != nil {
		t.Fatal(err)
	}
	if path.Album != "Rumours (1977) [Rumours (remaster), 2004]" {
		t.Errorf("album: got %q", path.Album)
	}
	if path.Artist != "Fleetwood Mac" {
		t.Errorf("artist: got %q, want the release-group's credit", path.Artist)
	}
}

func TestCleanPathSegment(t *testing.T) {
	cases := []struct {
		in, want string
		empty    bool
	}{
		{"AC/DC", "AC+DC", false},
		{`Back\Slash`, "Back+Slash", false},
		{"Live: 1991", "Live - 1991", false},
		{"12:34", "12-34", false},
		{"Why?", "Why!", false},
		{"Star*", "Star-", false},
		{`<Say "Hi"> |`, "Say Hi", false},
		{"...And Justice for All", "And Justice for All", false},
		{"Trailing dots...", "Trailing dots", false},
		{"Tab\there", "Tabhere", false},
		{"CON", "CON_", false},
		{"nul.flac", "nul.flac_", false},
		{"Concrete", "Concrete", false},
		{"Rumours ()", "Rumours ()", false},
		{"Rumours ()", "Rumours", true},
		{" - Dreams", "- Dreams", false},
		{" - Dreams", "Dreams", true},
		{"a  lot   of space", "a lot of space", false},
	}
	for _, tc := range cases {
		if got := CleanPathSegment(tc.in, tc.empty, maxSegmentBytes); got != tc.want {
			t.Errorf("CleanPathSegment(%q, %v) = %q, want %q", tc.in, tc.empty, got, tc.want)
		}
	}
}

func TestRenderLibraryPathCapsSegmentLength(t *testing.T) {
	release := sampleRumours
	release.Media = []models.MusicBrainzMedia{{Position: 1, Tracks: []models.Track{
		sampleTrack("long", 1, strings.Repeat("ø", 200), nil), // 400 bytes
	}}}
	path, err := RenderLibraryPath(models.NamingTemplates{}, release, "long", NamingFile{Ext: ".flac"})
	if err != nil {
		t.Fatal(err)
	}
	if len(path.File) > maxSegmentBytes {
		t.Errorf("file name is %d bytes, over the %d limit", len(path.File), maxSegmentBytes)
	}
	if !strings.HasSuffix(path.File, ".flac") {
		t.Errorf("the extension must survive truncation: %q", path.File)
	}
	if !strings.HasPrefix(path.File, "01 ø") || strings.ContainsRune(path.File, '�') {
		t.Errorf("truncation must cut on a rune boundary: %q", path.File)
	}
}

func TestRenderLibraryPathTrackNotFound(t *testing.T) {
	_, err := RenderLibraryPath(models.NamingTemplates{}, sampleRumours, "missing", NamingFile{Ext: ".flac"})
	if !errors.Is(err, ErrNamingTrackNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestValidateNamingTemplatesRejects(t *testing.T) {
	cases := []struct {
		name      string
		templates models.NamingTemplates
		field     NamingField
		contains  string
	}{
		{"unknown token", models.NamingTemplates{ArtistFolder: "{Artist Nickname}"}, NamingArtistFolder, "not a naming token"},
		{"album token in the artist folder", models.NamingTemplates{ArtistFolder: "{Artist Name} - {Album Title}"}, NamingArtistFolder, "shared by more than one"},
		{"track token in the album folder", models.NamingTemplates{AlbumFolder: "{Album Title} {track}"}, NamingAlbumFolder, "shared by more than one"},
		{"separator in a template", models.NamingTemplates{AlbumFolder: "{Release Year}/{Album Title}"}, NamingAlbumFolder, "path separator"},
		{"backslash in a template", models.NamingTemplates{AlbumFolder: `{Release Year}\{Album Title}`}, NamingAlbumFolder, "path separator"},
		{"unclosed brace", models.NamingTemplates{TrackFile: "{track:00 {Track Title}"}, NamingTrackFile, "no }"},
		{"stray closing brace", models.NamingTemplates{TrackFile: "{track:00}} {Track Title}"}, NamingTrackFile, "no {"},
		{"format on text", models.NamingTemplates{TrackFile: "{track:00} {Track Title:00}"}, NamingTrackFile, "only a number"},
		{"format not zeros", models.NamingTemplates{TrackFile: "{track:12} {Track Title}"}, NamingTrackFile, "zeros"},
		{"disc folder without a number", models.NamingTemplates{MediaFolder: "{Medium Format}"}, NamingMediaFolder, "needs {medium}"},
		{"disc folder not read back", models.NamingTemplates{MediaFolder: "Part {medium:00}"}, NamingMediaFolder, "not read back as disc 2"},
		{"track file without a number", models.NamingTemplates{TrackFile: "{Track Title}"}, NamingTrackFile, "needs {track}"},
		{"renders empty", models.NamingTemplates{ArtistFolder: "{Artist Disambiguation}"}, NamingArtistFolder, "renders empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := ValidateNamingTemplates(tc.templates)
			msg := check.Errors[tc.field]
			if !strings.Contains(msg, tc.contains) {
				t.Errorf("%s error = %q, want it to mention %q (all: %v)", tc.field, msg, tc.contains, check.Errors)
			}
			if check.Err() == nil {
				t.Error("Err() should report the blocking problem")
			}
		})
	}
}

func TestValidateNamingTemplatesAccepts(t *testing.T) {
	for _, templates := range []models.NamingTemplates{
		{MediaFolder: "Disc {medium}"},
		{MediaFolder: "Vinyl {medium:00}"},
		{MediaFolder: "{Medium Format} {medium:00}"},
		{TrackFile: "{medium}-{track:00} {Track Title}"},
		{ArtistFolder: "{Artist SortName}", AlbumFolder: "{Release Year} - {Album Title}"},
		{AlbumFolder: "{Album Title} ({Release Year}){ [Edition Disambiguation]}"},
	} {
		if check := ValidateNamingTemplates(templates); !check.OK() {
			t.Errorf("%+v rejected: %v", templates, check.Errors)
		}
	}
}

func TestDiscFolderNamesFromMediumFormats(t *testing.T) {
	cases := map[string]int{
		"CD 02":                2,
		"Disc 3":               3,
		"Vinyl 01":             1,
		"12 Vinyl 02":          2,
		"Digital Media 01":     1,
		"Cassette 2":           2,
		"SACD 1":               1,
		"CD-R 02":              2,
		"Blu-ray 1":            1,
		"HQCD 02":              2,
		"DualDisc 02":          2,
		"Shellac 02":           2,
		"Reel-to-reel 02":      2,
		"USB Flash Drive 2":    2,
		"SACD (2 channels) 03": 3,
		"Hybrid SACD (SACD layer, 2 channels) 03": 3,
		"LP1":            0,
		"Rumours (1977)": 0,
		"Social Media":   0,
		"The Other 2":    0,
	}
	for name, want := range cases {
		if got := discNumberFromFolderName(name); got != want {
			t.Errorf("discNumberFromFolderName(%q) = %d, want %d", name, got, want)
		}
	}
}

// {Medium Format} {medium:00}, Lidarr's default, renders a folder read back as its own
// disc whatever MusicBrainz calls the medium — the format where the pattern reads it,
// "Disc" where it would not.
func TestMediumFormatDiscFolderReadsBack(t *testing.T) {
	templates := models.NamingTemplates{MediaFolder: "{Medium Format} {medium:00}"}
	for format, want := range map[string]string{
		"CD":                     "CD 02",
		`12" Vinyl`:              "12 Vinyl 02",
		"HQCD":                   "HQCD 02",
		"SACD (2 channels)":      "SACD (2 channels) 02",
		"Hybrid SACD (CD layer)": "Hybrid SACD (CD layer) 02",
		"Blu-spec CD2":           "Disc 02",
		"Other":                  "Disc 02",
		"DAT":                    "Disc 02",
		"":                       "Disc 02",
	} {
		probe := namingDiscProbe()
		release := probe.Release
		release.Media = append([]models.MusicBrainzMedia(nil), release.Media...)
		for i := range release.Media {
			release.Media[i].Format = format
		}
		path, err := RenderLibraryPath(templates, release, probe.TrackID, probe.File)
		if err != nil {
			t.Fatalf("%q: %v", format, err)
		}
		if path.Media != want {
			t.Errorf("%q renders %q, want %q", format, path.Media, want)
		}
		if got := discNumberFromFolderName(path.Media); got != 2 {
			t.Errorf("%q: %q reads back as disc %d, want 2", format, path.Media, got)
		}
	}
}

// The Clean and The forms, Lidarr's spellings.
func TestCleanAndTheForms(t *testing.T) {
	for in, want := range map[string]string{
		"Guns N' Roses":                  "Guns N Roses",
		"AC/DC":                          "AC DC",
		"Simon & Garfunkel":              "Simon and Garfunkel",
		"Straight Checkn 'Em":            "Straight Checkn Em",
		"Title: The Subtitle":            "Title The Subtitle",
		"Live - In Concert (Remastered)": "Live In Concert Remastered",
		"Why?":                           "Why",
		"Björk":                          "Björk",
		"...And Justice for All":         "...And Justice for All",
		"Compton's Most Wanted":          "Comptons Most Wanted",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"The Beatles":                 "Beatles, The",
		"A Tribe Called Quest (live)": "Tribe Called Quest, A (live)",
		"An Pierlé":                   "Pierlé, An",
		"Theatre of Tragedy":          "Theatre of Tragedy",
		"Fleetwood Mac":               "Fleetwood Mac",
	} {
		if got := titleThe(in); got != want {
			t.Errorf("titleThe(%q) = %q, want %q", in, got, want)
		}
	}
}

// Two pressings of one album need something that tells them apart in the album
// folder; country, format and catalogue number do when MusicBrainz has no comment.
func TestNamingEditionTokens(t *testing.T) {
	release := sampleRumours
	release.Country = "XE"
	release.Media = []models.MusicBrainzMedia{
		{Position: 1, Format: "CD", Tracks: release.Media[0].Tracks},
		{Position: 2, Format: "CD"},
		{Position: 3, Format: "DVD-Video"},
	}
	// "[none]" is MusicBrainz saying there is no number, so the next label's is used.
	if err := json.Unmarshal([]byte(`[{"catalog-number":"[none]"},{"catalog-number":"WB 3010"}]`), &release.LabelInfo); err != nil {
		t.Fatal(err)
	}
	templates := models.NamingTemplates{AlbumFolder: "{Album Title} ({Release Year}) [{Edition Country} {Edition Format} {Edition CatalogNumber}]"}
	path, err := RenderLibraryPath(templates, release, sampleID("32"), NamingFile{Ext: ".flac"})
	if err != nil {
		t.Fatal(err)
	}
	if path.Album != "Rumours (1977) [XE 2xCD + DVD-Video WB 3010]" {
		t.Errorf("album: got %q", path.Album)
	}
}
