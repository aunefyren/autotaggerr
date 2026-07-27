package modules

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// sampleFileTags returns a FileTags with a distinct sentinel value per field so a
// mis-wired mapping (e.g. recording ID written to the wrong tag) is caught.
func sampleFileTags() models.FileTags {
	return models.FileTags{
		Artist:                "v_artist",
		ArtistSemicolon:       "v_artists_semicolon",
		AlbumArtist:           "v_albumartist",
		Genres:                []string{"Rock", "Pop"},
		OriginalDate:          "v_originaldate",
		OriginalYear:          "v_originalyear",
		ReleaseDate:           "v_releasedate",
		ReleaseYear:           "v_releaseyear",
		Album:                 "v_album",
		Title:                 "v_title",
		ISRC:                  "v_isrc",
		Track:                 "v_track",
		TrackTotal:            "v_tracktotal",
		DiscNumber:            "v_discnumber",
		DiscTotal:             "v_disctotal",
		MBAlbumStatus:         "v_albumstatus",
		MBAlbumType:           "v_albumtype",
		MBAlbumReleaseCountry: "v_releasecountry",
		MBAlbumID:             "v_albumid",
		MBArtistID:            "v_artistid",
		MBAlbumArtistID:       "v_albumartistid",
		MBReleaseGroupID:      "v_releasegroupid",
		MBReleaseTrackID:      "v_releasetrackid",
		MBRecordingID:         "v_recordingid",
		Script:                "v_script",
		RecordLabel:           "v_recordlabel",
		Media:                 "v_media",
		Barcode:               "v_barcode",
		ASIN:                  "v_asin",
		CatalogNumber:         "v_catalognumber",
		Composer:              "v_composer",
		Author:                "v_author",
	}
}

func assertTag(t *testing.T, m map[string]string, key, want string) {
	t.Helper()
	if got, ok := m[key]; !ok {
		t.Errorf("missing key %q", key)
	} else if got != want {
		t.Errorf("key %q = %q, want %q", key, got, want)
	}
}

func TestBuildFLACDesiredTags(t *testing.T) {
	m := buildFLACDesiredTags(sampleFileTags())

	// The subtle MusicBrainz wirings that are easy to swap.
	assertTag(t, m, "MUSICBRAINZ_RELEASETRACKID", "v_releasetrackid")
	assertTag(t, m, "MUSICBRAINZ_TRACKID", "v_recordingid") // TRACKID <- recording, not release-track
	assertTag(t, m, "MUSICBRAINZ_ALBUMID", "v_albumid")

	// FLAC writes only the FIRST genre.
	assertTag(t, m, "GENRE", "Rock")

	// Duplicated source fields must all be present.
	assertTag(t, m, "DATE", "v_releasedate")
	assertTag(t, m, "RELEASEDATE", "v_releasedate")
	assertTag(t, m, "DISCTOTAL", "v_disctotal")
	assertTag(t, m, "TOTALDISCS", "v_disctotal")

	// FLAC-only fields absent from MP3.
	assertTag(t, m, "COMPOSER", "v_composer")
	assertTag(t, m, "AUTHOR", "v_author")
	assertTag(t, m, "BARCODE", "v_barcode")
	assertTag(t, m, "ASIN", "v_asin")
	assertTag(t, m, "CATALOGNUMBER", "v_catalognumber")

	// Empty genres -> empty GENRE, not a panic.
	tags := sampleFileTags()
	tags.Genres = nil
	if got := buildFLACDesiredTags(tags)["GENRE"]; got != "" {
		t.Errorf("GENRE with no genres = %q, want empty", got)
	}
}

func TestBuildMP3DesiredTags(t *testing.T) {
	m := buildMP3DesiredTags(sampleFileTags())

	// MP3 joins ALL genres with ";" (differs from FLAC).
	assertTag(t, m, "GENRE", "Rock;Pop")

	// MP3 uses human-readable MusicBrainz TXXX keys.
	assertTag(t, m, "MusicBrainz Album Id", "v_albumid")
	assertTag(t, m, "MusicBrainz Release Track Id", "v_releasetrackid")
	assertTag(t, m, "publisher", "v_recordlabel")
	assertTag(t, m, "TMED", "v_media")
	assertTag(t, m, "DATE", "v_releasedate")
	assertTag(t, m, "TDOR", "v_originaldate")

	// Fields that are FLAC-only must NOT leak into the MP3 map.
	for _, absent := range []string{"COMPOSER", "AUTHOR", "BARCODE", "ASIN", "CATALOGNUMBER", "MUSICBRAINZ_TRACKID"} {
		if _, ok := m[absent]; ok {
			t.Errorf("MP3 desired tags unexpectedly contains %q", absent)
		}
	}
}
