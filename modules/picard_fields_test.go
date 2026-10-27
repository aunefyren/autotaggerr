package modules

import (
	"encoding/json"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// picardRelease is a two-artist reissue of a Various Artists compilation's shape, in
// one fixture: sort names on the credits, a disc title, language, ASIN, an edition date
// later than the album's.
func picardRelease(t *testing.T, albumArtistID string) models.MusicBrainzReleaseResponse {
	t.Helper()
	var release models.MusicBrainzReleaseResponse
	if err := json.Unmarshal([]byte(`{"id":"rel","title":"Rumours","date":"2004-03-22","asin":" B0001X ",
		"text-representation":{"language":"eng","script":"Latn"},
		"artist-credit":[{"name":"Fleetwood Mac","artist":{"id":"`+albumArtistID+`","name":"Fleetwood Mac","sort-name":"Fleetwood Mac"}}],
		"release-group":{"id":"rg","title":"Rumours","primary-type":"Album","first-release-date":"1977-02-04"},
		"media":[{"position":1,"title":" Bonus Disc ","tracks":[{"id":"t1","position":1,"title":"Dreams",
			"artist-credit":[
				{"name":"Stevie Nicks","joinphrase":" & ","artist":{"id":"a1","name":"Stevie Nicks","sort-name":"Nicks, Stevie"}},
				{"name":"Lindsey Buckingham","artist":{"id":"a2","name":"Lindsey Buckingham","sort-name":"Buckingham, Lindsey"}}]}]}]}`), &release); err != nil {
		t.Fatal(err)
	}
	return release
}

func TestBuildFileTagsPicardFields(t *testing.T) {
	release := picardRelease(t, "art-fm")
	build := func(settings models.TaggerSettings) models.FileTags {
		t.Helper()
		tags, err := BuildFileTags(release.Media[0].Tracks[0], release.Media[0], release, settings)
		if err != nil {
			t.Fatal(err)
		}
		return tags
	}

	tags := build(models.TaggerSettings{})
	if tags.ArtistSort != "Nicks, Stevie & Buckingham, Lindsey" || tags.AlbumArtistSort != "Fleetwood Mac" {
		t.Errorf("sort = %q / %q", tags.ArtistSort, tags.AlbumArtistSort)
	}
	if tags.DiscSubtitle != "Bonus Disc" || tags.Language != "eng" || tags.ASIN != "B0001X" || tags.Compilation != "" {
		t.Errorf("release fields = subtitle %q, language %q, asin %q, compilation %q", tags.DiscSubtitle, tags.Language, tags.ASIN, tags.Compilation)
	}
	// By default DATE is the edition's, as Picard writes it.
	if tags.Date != "2004-03-22" || tags.Year != "2004" || tags.ReleaseDate != "2004-03-22" || tags.OriginalDate != "1977-02-04" {
		t.Errorf("dates = date %q year %q release %q original %q", tags.Date, tags.Year, tags.ReleaseDate, tags.OriginalDate)
	}

	// The option moves DATE and YEAR to the album's first date; the edition's stays in
	// RELEASEDATE and the original in ORIGINALDATE.
	original := build(models.TaggerSettings{OriginalDateAsDate: true})
	if original.Date != "1977-02-04" || original.Year != "1977" || original.ReleaseDate != "2004-03-22" || original.OriginalDate != "1977-02-04" {
		t.Errorf("original as date = date %q year %q release %q original %q", original.Date, original.Year, original.ReleaseDate, original.OriginalDate)
	}
	// With no first date known, the edition's is kept rather than none.
	release.ReleaseGroup.FirstReleaseDate = ""
	if got := build(models.TaggerSettings{OriginalDateAsDate: true}); got.Date != "2004-03-22" {
		t.Errorf("no original date = %q, want the edition's", got.Date)
	}

	// A redundant ARTIST takes its sort name with it.
	solo := picardRelease(t, "art-fm")
	solo.Media[0].Tracks[0].ArtistCredit = solo.ArtistCredit
	if got, _ := BuildFileTags(solo.Media[0].Tracks[0], solo.Media[0], solo, models.TaggerSettings{IgnoreRedundantContributingArtists: true}); got.Artist != "" || got.ArtistSort != "" {
		t.Errorf("redundant artist = %q / sort %q, want both blank", got.Artist, got.ArtistSort)
	}

	// Credited to Various Artists, it is a compilation in Picard's sense.
	va := picardRelease(t, variousArtistsMBID)
	if got, _ := BuildFileTags(va.Media[0].Tracks[0], va.Media[0], va, models.TaggerSettings{}); got.Compilation != "1" {
		t.Errorf("Various Artists compilation = %q, want 1", got.Compilation)
	}
}

// TestPicardFieldsRoundTrip writes the new fields to a real MP3 and FLAC, reads them
// back from the frames Picard uses — TSOP, TSO2, TSST, TCMP, TLAN, TDRL, TXXX:ASIN —
// and converges: a second write with the same metadata changes nothing. MusicBrainz's
// release types are two values, and stay two.
func TestPicardFieldsRoundTrip(t *testing.T) {
	meta := fullFileTags()
	meta.ArtistSort, meta.AlbumArtistSort = "Artist, Test & Feature", "Artist, Test"
	meta.DiscSubtitle, meta.Compilation, meta.Language, meta.ASIN = "Bonus Disc", "1", "eng", "B0001X"
	meta.MBAlbumTypes = []string{"ep", "live"}

	t.Run("mp3", func(t *testing.T) {
		path := synthAudio(t, ".mp3")
		if _, _, _, err := SetMP3Tags(path, meta, models.TaggerSettings{}); err != nil {
			t.Fatal(err)
		}
		tags, err := GetMP3Tags(path)
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{
			"ARTISTSORT": "Artist, Test & Feature", "ALBUMARTISTSORT": "Artist, Test", "DISCSUBTITLE": "Bonus Disc",
			"COMPILATION": "1", "LANGUAGE": "eng", "ASIN": "B0001X", "RELEASEDATE": "2002-02-02",
			"MUSICBRAINZ ALBUM TYPE": "ep; live",
		} {
			if got := firstTag(tags, key); got != want {
				t.Errorf("MP3 %s = %q, want %q", key, got, want)
			}
		}
		frames := dumpID3Frames(t, path)
		for _, frame := range []string{"TSOP=Artist, Test & Feature", "TSO2=Artist, Test", "TSST=Bonus Disc", "TCMP=1", "TLAN=eng", "TDRL=2002-02-02"} {
			if !contains(frames, frame) {
				t.Errorf("MP3 frames lack %s: %v", frame, frames)
			}
		}
		if unchanged, written, _, err := SetMP3Tags(path, meta, models.TaggerSettings{}); err != nil || !unchanged || written != 0 {
			t.Errorf("second MP3 write = unchanged %v, written %d, %v; want a no-op", unchanged, written, err)
		}
	})

	t.Run("flac", func(t *testing.T) {
		requireTool(t, "metaflac")
		path := synthAudio(t, ".flac")
		if _, _, _, err := SetFlacTags(path, meta, models.TaggerSettings{}); err != nil {
			t.Fatal(err)
		}
		tags, err := ReadFileTagMap(path)
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{
			"ARTISTSORT": "Artist, Test & Feature", "ALBUMARTISTSORT": "Artist, Test", "DISCSUBTITLE": "Bonus Disc",
			"COMPILATION": "1", "LANGUAGE": "eng", "ASIN": "B0001X", "TOTALTRACKS": "12", "TRACKTOTAL": "12",
		} {
			if got := firstTag(tags, key); got != want {
				t.Errorf("FLAC %s = %q, want %q", key, got, want)
			}
		}
		if got := tags["RELEASETYPE"]; len(got) != 2 || got[0] != "ep" || got[1] != "live" {
			t.Errorf("FLAC RELEASETYPE = %v, want two values", got)
		}
		if unchanged, written, _, err := SetFlacTags(path, meta, models.TaggerSettings{}); err != nil || !unchanged || written != 0 {
			t.Errorf("second FLAC write = unchanged %v, written %d, %v; want a no-op", unchanged, written, err)
		}
	})
}
