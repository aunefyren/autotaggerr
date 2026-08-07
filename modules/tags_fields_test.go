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
		Artists:               []string{"Test Artist", "Feature"},
		AlbumArtist:           "Test Artist",
		AlbumArtists:          []string{"Test Artist", "Co-Headliner"},
		Genres:                []string{"Hip Hop", "Rap"},
		OriginalDate:          "2001-01-01",
		OriginalYear:          "2001",
		ReleaseDate:           "2002-02-02",
		ReleaseYear:           "2002",
		Album:                 "Test Album",
		Title:                 "Test Title",
		ISRCs:                 []string{"USABC1234567"},
		Track:                 "3",
		TrackTotal:            "12",
		DiscNumber:            "1",
		DiscTotal:             "2",
		MBAlbumStatus:         "official",
		MBAlbumType:           "album",
		MBAlbumReleaseCountry: "US",
		MBAlbumID:             "album-id",
		MBArtistIDs:           []string{"artist-id"},
		MBAlbumArtistIDs:      []string{"album-artist-id"},
		MBReleaseGroupID:      "rg-id",
		MBReleaseTrackID:      "track-id",
		MBRecordingID:         "recording-id",
		Script:                "Latn",
		RecordLabels:          []string{"Test Label"},
		Media:                 "CD",
		Barcode:               "0123456789",
		ASIN:                  "B000TEST",
		CatalogNumbers:        []string{"CAT-1"},
		Composer:              "Test Composer",
		Author:                "Test Author",
	}
}

func TestMP3FullFieldRoundTrip(t *testing.T) {
	path := synthAudio(t, ".mp3")

	unchanged, written, _, err := SetMP3Tags(path, fullFileTags(), models.ConfigStruct{})
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
		"GENRE":                "Hip Hop; Rap", // every genre, one shared separator
		"DATE":                 "2002-02-02",
		"TRACKNUMBER":          "3",
		"TRACKTOTAL":           "12", // parsed from the composite TRCK "3/12"
		"DISCNUMBER":           "1",
		"DISCTOTAL":            "2", // parsed from the composite TPOS "1/2"
		"ARTISTS":              "Test Artist; Feature",
		"ALBUMARTIST":          "Test Artist",               // single-valued for Plex
		"ALBUMARTISTS":         "Test Artist; Co-Headliner", // the whole credit
		"ISRC":                 "USABC1234567",              // TXXX:ISRC frame
		"PUBLISHER":            "Test Label",
		"TMED":                 "CD",
		"SCRIPT":               "Latn",
		"MUSICBRAINZ ALBUM ID": "album-id",
		// Parity with FLAC — absent from MP3 files written before this.
		"MUSICBRAINZ RECORDING ID": "recording-id",
		"BARCODE":                  "0123456789",
		"CATALOGNUMBER":            "CAT-1",
	}
	for key, want := range checks {
		if got := firstTag(tags, key); got != want {
			t.Errorf("MP3 tag %s = %q, want %q", key, got, want)
		}
	}

	// A second write with the same metadata must converge to a no-op — this guards
	// against the totals regressing (they must round-trip so the diff sees no change).
	unchanged2, written2, _, err := SetMP3Tags(path, fullFileTags(), models.ConfigStruct{})
	if err != nil {
		t.Fatalf("second SetMP3Tags: %v", err)
	}
	if !unchanged2 || written2 != 0 {
		t.Errorf("second full write should be a no-op, got unchanged=%v written=%d", unchanged2, written2)
	}
}

// TestMP3RemoveValuesClearsAndConverges is the property that makes remove_values safe
// on MP3: a field emptied by the profile must actually leave the file, and the second
// pass must be a no-op. The failure it guards is subtle — the reported diff is derived
// from the change set, so a key the writer decides to skip is reported as written
// while nothing happens, and every scan re-reports it forever.
//
// It covers the fields whose write blocks used to second-guess an empty value:
// ARTISTS, the original-date trio, the paired track/disc frames and the ISRC TXXX
// frame, plus a plain 1:1 field (ARTIST — the redundant-artist case).
func TestMP3RemoveValuesClearsAndConverges(t *testing.T) {
	path := synthAudio(t, ".mp3")

	removeValues := models.ConfigStruct{AutotaggerrRemoveValues: true}
	if _, _, _, err := SetMP3Tags(path, fullFileTags(), removeValues); err != nil {
		t.Fatalf("SetMP3Tags (full): %v", err)
	}

	cleared := fullFileTags()
	cleared.Artist = ""
	cleared.Artists = nil
	cleared.OriginalDate = ""
	cleared.OriginalYear = ""
	cleared.ISRCs = nil
	cleared.TrackTotal = ""
	cleared.DiscNumber = ""
	cleared.DiscTotal = ""

	unchanged, written, changes, err := SetMP3Tags(path, cleared, removeValues)
	if err != nil {
		t.Fatalf("SetMP3Tags (cleared): %v", err)
	}
	if unchanged || written == 0 {
		t.Fatalf("clearing fields should be a change: unchanged=%v written=%d", unchanged, written)
	}
	if len(changes) == 0 {
		t.Error("expected a field-level diff for the cleared fields")
	}

	tags, err := GetMP3Tags(path)
	if err != nil {
		t.Fatalf("GetMP3Tags: %v", err)
	}
	for _, key := range []string{"ARTIST", "ARTISTS", "ORIGINALDATE", "ORIGINALYEAR", "ISRC", "DISCNUMBER"} {
		if got := firstTag(tags, key); got != "" {
			t.Errorf("%s = %q, want it gone from the file", key, got)
		}
	}

	// The whole point: writing the same cleared set again must do nothing.
	unchanged2, written2, _, err := SetMP3Tags(path, cleared, removeValues)
	if err != nil {
		t.Fatalf("second SetMP3Tags (cleared): %v", err)
	}
	if !unchanged2 || written2 != 0 {
		t.Fatalf("clearing must converge, or every scan rewrites the file: unchanged=%v written=%d",
			unchanged2, written2)
	}
}

// TestMP3ISRCPreservesCase guards that a TXXX value round-trips with its original
// case (the reader used to force-uppercase it, which would flag a spurious change
// on the next scan for any lower/mixed-case value).
func TestMP3ISRCPreservesCase(t *testing.T) {
	path := synthAudio(t, ".mp3")

	meta := models.FileTags{Artist: "A", Album: "B", Title: "C", ISRCs: []string{"gb-abc-99-12345"}}
	if _, _, _, err := SetMP3Tags(path, meta, models.ConfigStruct{}); err != nil {
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
	unchanged, _, _, err := SetMP3Tags(path, meta, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("second SetMP3Tags: %v", err)
	}
	if !unchanged {
		t.Error("second write of the same mixed-case ISRC should be a no-op")
	}
}

func TestExtractFromID3v2(t *testing.T) {
	path := synthAudio(t, ".mp3")
	if _, _, _, err := SetMP3Tags(path, fullFileTags(), models.ConfigStruct{}); err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}

	cases := map[string]string{
		"release":       "album-id",
		"release_group": "rg-id",
		"track":         "track-id",
		"title":         "Test Title",
		// "recording" was a dead lookup for as long as it existed: this reader has
		// always known the key, but nothing wrote it until MP3 reached parity with
		// FLAC's MUSICBRAINZ_TRACKID. It is the identity that survives a release
		// merge, so it is what re-identifies an MP3 with no manager to ask.
		"recording": "recording-id",
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
	if _, _, _, err := SetFlacTags(path, models.FileTags{Artist: "A", Album: "B", Title: "C", Composer: "Someone"}, models.ConfigStruct{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Removal disabled: an empty Composer must NOT change anything.
	meta := models.FileTags{Artist: "A", Album: "B", Title: "C", Composer: ""}
	unchanged, _, _, err := SetFlacTags(path, meta, models.ConfigStruct{AutotaggerrRemoveValues: false})
	if err != nil {
		t.Fatalf("removal-off SetFlacTags: %v", err)
	}
	if !unchanged {
		t.Error("with removal disabled, empty values should not trigger a change")
	}

	// Removal enabled: the empty Composer should now clear the tag.
	_, _, _, err = SetFlacTags(path, meta, models.ConfigStruct{AutotaggerrRemoveValues: true})
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
