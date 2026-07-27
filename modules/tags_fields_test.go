package modules

import (
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// fullFileTags populates every field so a first write exercises all the per-field
// branches in the writers.
func fullFileTags() models.FileTags {
	return models.FileTags{
		Artist:                "Test Artist",
		ArtistSemicolon:       "Test Artist; Feature",
		AlbumArtist:           "Test Artist",
		Genres:                []string{"Hip Hop", "Rap"},
		OriginalDate:          "2001-01-01",
		OriginalYear:          "2001",
		ReleaseDate:           "2002-02-02",
		ReleaseYear:           "2002",
		Album:                 "Test Album",
		Title:                 "Test Title",
		ISRC:                  "USABC1234567",
		Track:                 "3",
		TrackTotal:            "12",
		DiscNumber:            "1",
		DiscTotal:             "2",
		MBAlbumStatus:         "official",
		MBAlbumType:           "album",
		MBAlbumReleaseCountry: "US",
		MBAlbumID:             "album-id",
		MBArtistID:            "artist-id",
		MBAlbumArtistID:       "album-artist-id",
		MBReleaseGroupID:      "rg-id",
		MBReleaseTrackID:      "track-id",
		MBRecordingID:         "recording-id",
		Script:                "Latn",
		RecordLabel:           "Test Label",
		Media:                 "CD",
		Barcode:               "0123456789",
		ASIN:                  "B000TEST",
		CatalogNumber:         "CAT-1",
		Composer:              "Test Composer",
		Author:                "Test Author",
	}
}

func TestMP3FullFieldRoundTrip(t *testing.T) {
	requireTool(t, "ffprobe")
	path := synthAudio(t, ".mp3")

	unchanged, written, err := SetMP3Tags(path, fullFileTags())
	if err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}
	if unchanged || written == 0 {
		t.Fatalf("expected changes, got unchanged=%v written=%d", unchanged, written)
	}

	tags, err := GetMP3Tags(path)
	if err != nil {
		t.Fatalf("GetMP3Tags: %v", err)
	}
	// Assert the fields that round-trip reliably through ffmpeg/ffprobe.
	checks := map[string]string{
		"ARTIST":               "Test Artist",
		"ALBUM":                "Test Album",
		"TITLE":                "Test Title",
		"GENRE":                "Hip Hop;Rap", // MP3 joins genres
		"DATE":                 "2002-02-02",
		"TRACKNUMBER":          "3",
		"TRACKTOTAL":           "12", // parsed from the composite TRCK "3/12"
		"DISCNUMBER":           "1",
		"DISCTOTAL":            "2", // parsed from the composite TPOS "1/2"
		"ARTISTS":              "Test Artist; Feature",
		"ISRC":                 "USABC1234567", // TXXX:ISRC frame
		"PUBLISHER":            "Test Label",
		"TMED":                 "CD",
		"SCRIPT":               "Latn",
		"MUSICBRAINZ ALBUM ID": "album-id",
	}
	for key, want := range checks {
		if got := firstTag(tags, key); got != want {
			t.Errorf("MP3 tag %s = %q, want %q", key, got, want)
		}
	}

	// A second write with the same metadata must converge to a no-op — this guards
	// against the totals regressing (they must round-trip so the diff sees no change).
	unchanged2, written2, err := SetMP3Tags(path, fullFileTags())
	if err != nil {
		t.Fatalf("second SetMP3Tags: %v", err)
	}
	if !unchanged2 || written2 != 0 {
		t.Errorf("second full write should be a no-op, got unchanged=%v written=%d", unchanged2, written2)
	}
}

// TestMP3ISRCPreservesCase guards that a TXXX value round-trips with its original
// case (the reader used to force-uppercase it, which would flag a spurious change
// on the next scan for any lower/mixed-case value).
func TestMP3ISRCPreservesCase(t *testing.T) {
	requireTool(t, "ffprobe")
	path := synthAudio(t, ".mp3")

	meta := models.FileTags{Artist: "A", Album: "B", Title: "C", ISRC: "gb-abc-99-12345"}
	if _, _, err := SetMP3Tags(path, meta); err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}

	tags, err := GetMP3Tags(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := firstTag(tags, "ISRC"); got != "gb-abc-99-12345" {
		t.Errorf("ISRC = %q, want gb-abc-99-12345 (case preserved)", got)
	}

	// And the mixed-case value must not trigger a rewrite on the second pass.
	unchanged, _, err := SetMP3Tags(path, meta)
	if err != nil {
		t.Fatalf("second SetMP3Tags: %v", err)
	}
	if !unchanged {
		t.Error("second write of the same mixed-case ISRC should be a no-op")
	}
}

func TestExtractFromID3v2(t *testing.T) {
	requireTool(t, "ffprobe")
	path := synthAudio(t, ".mp3")
	if _, _, err := SetMP3Tags(path, fullFileTags()); err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}

	cases := map[string]string{
		"release":       "album-id",
		"release_group": "rg-id",
		"track":         "track-id",
		"title":         "Test Title",
	}
	for metaType, want := range cases {
		got, err := extractFromID3v2(path, metaType)
		if err != nil {
			t.Fatalf("extractFromID3v2(%s): %v", metaType, err)
		}
		if got != want {
			t.Errorf("extractFromID3v2(%s) = %q, want %q", metaType, got, want)
		}
	}

	if _, err := extractFromID3v2(path, "bogus"); err == nil {
		t.Error("expected error for unsupported metadata type")
	}
}

// TestSetFlacTagsRemoveValues exercises the AutotaggerrRemoveValues branch: an
// empty desired value removes the existing tag only when removal is enabled.
func TestSetFlacTagsRemoveValues(t *testing.T) {
	requireTool(t, "metaflac")
	path := filepath.Join(t.TempDir(), "track.flac")
	synthInto(t, path)

	// Seed a COMPOSER tag.
	if _, _, err := SetFlacTags(path, models.FileTags{Artist: "A", Album: "B", Title: "C", Composer: "Someone"}, models.ConfigStruct{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Removal disabled: an empty Composer must NOT change anything.
	meta := models.FileTags{Artist: "A", Album: "B", Title: "C", Composer: ""}
	unchanged, _, err := SetFlacTags(path, meta, models.ConfigStruct{AutotaggerrRemoveValues: false})
	if err != nil {
		t.Fatalf("removal-off SetFlacTags: %v", err)
	}
	if !unchanged {
		t.Error("with removal disabled, empty values should not trigger a change")
	}

	// Removal enabled: the empty Composer should now clear the tag.
	_, _, err = SetFlacTags(path, meta, models.ConfigStruct{AutotaggerrRemoveValues: true})
	if err != nil {
		t.Fatalf("removal-on SetFlacTags: %v", err)
	}
	tags, err := getFlacTagsMap(path)
	if err != nil {
		t.Fatal(err)
	}
	if v := firstTag(tags, "COMPOSER"); v != "" {
		t.Errorf("COMPOSER should be cleared, got %q", v)
	}
}
