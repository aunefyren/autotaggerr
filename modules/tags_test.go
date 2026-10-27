package modules

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
)

// sampleFileTags returns a FileTags with a distinct sentinel value per field so a
// mis-wired mapping (e.g. recording ID written to the wrong tag) is caught.
func sampleFileTags() models.FileTags {
	return models.FileTags{
		Artist:                "v_artist",
		Artists:               []string{"v_artists_one", "v_artists_two"},
		AlbumArtist:           "v_albumartist",
		AlbumArtists:          []string{"v_albumartists_one", "v_albumartists_two"},
		Genres:                []string{"Rock", "Pop"},
		OriginalDate:          "v_originaldate",
		OriginalYear:          "v_originalyear",
		ReleaseDate:           "v_releasedate",
		ReleaseYear:           "v_releaseyear",
		Date:                  "v_date",
		Year:                  "v_year",
		ArtistSort:            "v_artistsort",
		AlbumArtistSort:       "v_albumartistsort",
		DiscSubtitle:          "v_discsubtitle",
		Compilation:           "1",
		Language:              "v_language",
		ASIN:                  "v_asin",
		Album:                 "v_album",
		Title:                 "v_title",
		ISRCs:                 []string{"v_isrc"},
		Track:                 "v_track",
		TrackTotal:            "v_tracktotal",
		DiscNumber:            "v_discnumber",
		DiscTotal:             "v_disctotal",
		MBAlbumStatus:         "v_albumstatus",
		MBAlbumTypes:          []string{"v_albumtype"},
		MBAlbumReleaseCountry: "v_releasecountry",
		MBAlbumID:             "v_albumid",
		MBArtistIDs:           []string{"v_artistid"},
		MBAlbumArtistIDs:      []string{"v_albumartistid"},
		MBReleaseGroupID:      "v_releasegroupid",
		MBReleaseTrackID:      "v_releasetrackid",
		MBRecordingID:         "v_recordingid",
		Script:                "v_script",
		RecordLabels:          []string{"v_recordlabel"},
		Media:                 "v_media",
		Barcode:               "v_barcode",
		CatalogNumbers:        []string{"v_catalognumber"},
	}
}

// assertTag checks a desired-tag key by its rendered value. The maps are
// multi-valued now; what a field is *wired* to is still one readable string.
func assertTag(t *testing.T, m map[string][]string, key, want string) {
	t.Helper()
	if _, ok := m[key]; !ok {
		t.Errorf("missing key %q", key)
	} else if got := utilities.JoinTagValues(m[key]); got != want {
		t.Errorf("key %q = %q, want %q", key, got, want)
	}
}

func TestBuildFLACDesiredTags(t *testing.T) {
	m := buildFLACDesiredTags(sampleFileTags())

	// The subtle MusicBrainz wirings that are easy to swap.
	assertTag(t, m, "MUSICBRAINZ_RELEASETRACKID", "v_releasetrackid")
	assertTag(t, m, "MUSICBRAINZ_TRACKID", "v_recordingid") // TRACKID <- recording, not release-track
	assertTag(t, m, "MUSICBRAINZ_ALBUMID", "v_albumid")

	// Both engines join every genre with the one shared separator.
	assertTag(t, m, "GENRE", "Rock; Pop")

	// ALBUMARTIST stays single-valued for Plex; the full credit rides on ALBUMARTISTS.
	assertTag(t, m, "ALBUMARTIST", "v_albumartist")
	assertTag(t, m, "ALBUMARTISTS", "v_albumartists_one; v_albumartists_two")

	// Duplicated source fields must all be present. DATE is FileTags.Date — the
	// edition's date, or the original's when the profile asks — and RELEASEDATE is
	// always the edition's.
	assertTag(t, m, "DATE", "v_date")
	assertTag(t, m, "YEAR", "v_year")
	assertTag(t, m, "RELEASEDATE", "v_releasedate")
	assertTag(t, m, "DISCTOTAL", "v_disctotal")
	assertTag(t, m, "TOTALDISCS", "v_disctotal")
	assertTag(t, m, "TRACKTOTAL", "v_tracktotal")
	assertTag(t, m, "TOTALTRACKS", "v_tracktotal")

	// Picard's fields from the release itself.
	assertTag(t, m, "ARTISTSORT", "v_artistsort")
	assertTag(t, m, "ALBUMARTISTSORT", "v_albumartistsort")
	assertTag(t, m, "DISCSUBTITLE", "v_discsubtitle")
	assertTag(t, m, "COMPILATION", "1")
	assertTag(t, m, "LANGUAGE", "v_language")
	assertTag(t, m, "ASIN", "v_asin")

	// FLAC-only fields (Vorbis has no agreed ID3 spelling for these).
	assertTag(t, m, "BARCODE", "v_barcode")
	assertTag(t, m, "CATALOGNUMBER", "v_catalognumber")

	// Nothing resolves a value for these — they are not even fields on FileTags any
	// more — so listing them would only ever clear another tagger's value once
	// remove_values is on.
	for _, absent := range []string{"COMPOSER", "AUTHOR"} {
		if _, ok := m[absent]; ok {
			t.Errorf("FLAC desired tags unexpectedly contains %q", absent)
		}
	}

	// Empty genres -> empty GENRE, not a panic.
	tags := sampleFileTags()
	tags.Genres = nil
	if got := buildFLACDesiredTags(tags)["GENRE"]; len(got) != 0 {
		t.Errorf("GENRE with no genres = %v, want empty", got)
	}
}

func TestBuildMP3DesiredTags(t *testing.T) {
	m := buildMP3DesiredTags(sampleFileTags())

	// Both engines join every genre with the one shared separator.
	assertTag(t, m, "GENRE", "Rock; Pop")

	// MP3 uses human-readable MusicBrainz TXXX keys.
	assertTag(t, m, "MusicBrainz Album Id", "v_albumid")
	assertTag(t, m, "MusicBrainz Release Track Id", "v_releasetrackid")
	assertTag(t, m, "publisher", "v_recordlabel")
	assertTag(t, m, "TMED", "v_media")
	assertTag(t, m, "DATE", "v_date")
	assertTag(t, m, "year", "v_year")
	assertTag(t, m, "RELEASEDATE", "v_releasedate")
	assertTag(t, m, "TDOR", "v_originaldate")
	assertTag(t, m, "ARTISTSORT", "v_artistsort")
	assertTag(t, m, "ALBUMARTISTSORT", "v_albumartistsort")
	assertTag(t, m, "DISCSUBTITLE", "v_discsubtitle")
	assertTag(t, m, "COMPILATION", "1")
	assertTag(t, m, "LANGUAGE", "v_language")
	assertTag(t, m, "ASIN", "v_asin")

	// Parity with FLAC: the recording MBID and the release's identifiers.
	assertTag(t, m, "MusicBrainz Recording Id", "v_recordingid")
	assertTag(t, m, "BARCODE", "v_barcode")
	assertTag(t, m, "CATALOGNUMBER", "v_catalognumber")
	assertTag(t, m, "ALBUMARTISTS", "v_albumartists_one; v_albumartists_two")

	// Never populated, so never written by either engine.
	for _, absent := range []string{"COMPOSER", "AUTHOR", "MUSICBRAINZ_TRACKID"} {
		if _, ok := m[absent]; ok {
			t.Errorf("MP3 desired tags unexpectedly contains %q", absent)
		}
	}
}
