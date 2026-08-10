package modules

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
)

// multiValueFileTags carries a genuinely multi-valued value in every field that can
// hold one, which is the case the joined representation exists to serve.
func multiValueFileTags() models.FileTags {
	return models.FileTags{
		Artist:           "A & B",
		Artists:          []string{"A", "B"},
		AlbumArtist:      "A",
		AlbumArtists:     []string{"A", "B"},
		Album:            "Alb",
		Title:            "Ttl",
		Track:            "1",
		TrackTotal:       "10",
		DiscNumber:       "1",
		DiscTotal:        "2",
		Genres:           []string{"hip hop", "rap", "jazz rap"},
		ISRCs:            []string{"USUM72108711", "USUM72108712"},
		MBArtistIDs:      []string{"aaa-111", "bbb-222"},
		MBAlbumArtistIDs: []string{"ccc-333", "ddd-444"},
		MBAlbumID:        "rel-1",
		MBRecordingID:    "rec-1",
		MBReleaseGroupID: "rg-1",
		MBReleaseTrackID: "rt-1",
		RecordLabels:     []string{"Label One", "Label Two"},
		CatalogNumbers:   []string{"CAT-1", "CAT-2"},
		Barcode:          "0123456789",
		Script:           "Latn",
		Media:            "CD",
		ReleaseDate:      "2001-05-04",
		ReleaseYear:      "2001",
		OriginalDate:     "1999-01-01",
		OriginalYear:     "1999",
	}
}

// TestMultiValueTagsSurviveBothEngines is the regression guard for both
// representations at once: FLAC writes one comment per value, MP3 writes one joined
// frame, and each must come back off disk exactly as written. The second write being
// a no-op is the property that matters — a field that reads back differently from how
// it was written is re-tagged on every scan forever.
func TestMultiValueTagsSurviveBothEngines(t *testing.T) {
	meta := multiValueFileTags()

	t.Run("flac", func(t *testing.T) {
		requireTool(t, "metaflac")
		path := synthAudio(t, ".flac")

		if _, _, _, err := SetFlacTags(path, meta, models.TaggerSettings{}); err != nil {
			t.Fatalf("first write: %v", err)
		}
		tags, err := getFlacTagsMap(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}

		// One Vorbis comment per value, in the order it was written: the order is
		// meaningful (genres are vote-ranked, credits read in credited order) and the
		// diff compares it.
		want := map[string][]string{
			"ARTISTS":                   meta.Artists,
			"ALBUMARTIST":               {meta.AlbumArtist},
			"ALBUMARTISTS":              meta.AlbumArtists,
			"GENRE":                     {"hip hop", "rap", "jazz rap"},
			"ISRC":                      meta.ISRCs,
			"MUSICBRAINZ_ARTISTID":      meta.MBArtistIDs,
			"MUSICBRAINZ_ALBUMARTISTID": meta.MBAlbumArtistIDs,
			"MUSICBRAINZ_TRACKID":       {meta.MBRecordingID},
			"LABEL":                     meta.RecordLabels,
			"CATALOGNUMBER":             meta.CatalogNumbers,
			"BARCODE":                   {meta.Barcode},
		}
		for key, values := range want {
			if got := tags[key]; !slices.Equal(got, values) {
				t.Errorf("FLAC %s = %v, want %v", key, got, values)
			}
		}

		assertSecondWriteIsNoOp(t, path, meta, SetFlacTags)
	})

	t.Run("mp3", func(t *testing.T) {
		path := synthAudio(t, ".mp3")

		if _, _, _, err := SetMP3Tags(path, meta, models.TaggerSettings{}); err != nil {
			t.Fatalf("first write: %v", err)
		}
		tags, err := GetMP3Tags(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}

		// One frame holding the joined string — ffprobe reads only the first value of
		// a null-separated frame, so this is all this writer can say. Steps 3 and 4.
		want := map[string]string{
			"ARTISTS":                     utilities.JoinTagValues(meta.Artists),
			"ALBUMARTIST":                 meta.AlbumArtist,
			"ALBUMARTISTS":                utilities.JoinTagValues(meta.AlbumArtists),
			"GENRE":                       "hip hop; rap; jazz rap",
			"ISRC":                        utilities.JoinTagValues(meta.ISRCs),
			"MUSICBRAINZ ARTIST ID":       utilities.JoinTagValues(meta.MBArtistIDs),
			"MUSICBRAINZ ALBUM ARTIST ID": utilities.JoinTagValues(meta.MBAlbumArtistIDs),
			"MUSICBRAINZ RECORDING ID":    meta.MBRecordingID,
			"PUBLISHER":                   utilities.JoinTagValues(meta.RecordLabels),
			"CATALOGNUMBER":               utilities.JoinTagValues(meta.CatalogNumbers),
			"BARCODE":                     meta.Barcode,
		}
		for key, value := range want {
			if got := firstTag(tags, key); got != value {
				t.Errorf("MP3 %s = %q, want %q", key, got, value)
			}
		}

		assertSecondWriteIsNoOp(t, path, meta, SetMP3Tags)
	})
}

// TestGenreParityBetweenEngines pins the fix for the split that started this: FLAC
// used to keep only the first genre while MP3 joined them all, so the same album
// carried less information as FLAC than as MP3.
//
// Parity is about *content*, not bytes. The engines carry the same genres in the same
// order; how many values that becomes on disk is the format's business.
func TestGenreParityBetweenEngines(t *testing.T) {
	meta := multiValueFileTags()
	flacGenre := buildFLACDesiredTags(meta)["GENRE"]
	mp3Genre := buildMP3DesiredTags(meta)["GENRE"]

	if !slices.Equal(flacGenre, mp3Genre) {
		t.Errorf("engines disagree on GENRE: FLAC %v, MP3 %v", flacGenre, mp3Genre)
	}
	if !slices.Equal(flacGenre, []string{"hip hop", "rap", "jazz rap"}) {
		t.Errorf("GENRE = %v, want every genre in rank order", flacGenre)
	}
}

func assertSecondWriteIsNoOp(
	t *testing.T,
	path string,
	meta models.FileTags,
	write func(string, models.FileTags, models.TaggerSettings) (bool, int, []models.TagChange, error),
) {
	t.Helper()
	unchanged, written, changed, err := write(path, meta, models.TaggerSettings{})
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if !unchanged || written != 0 {
		for _, change := range changed {
			t.Errorf("did not converge on %s: %q -> %q", change.Field, change.Old, change.New)
		}
		t.Fatalf("second identical write must be a no-op, got unchanged=%v written=%d", unchanged, written)
	}
}

// TestDesiredTagsCarryTrueMultiValue separates the two halves of the multi-value
// work: the builders already describe a field as the several values it has, while
// the renderers decide what reaches the file. Step 2 in wip.md flips the FLAC
// renderer; nothing above it has to move again.
func TestDesiredTagsCarryTrueMultiValue(t *testing.T) {
	meta := multiValueFileTags()

	for _, key := range []string{"GENRE", "ISRC", "ARTISTS", "LABEL", "CATALOGNUMBER"} {
		if got := buildFLACDesiredTags(meta)[key]; len(got) < 2 {
			t.Errorf("buildFLACDesiredTags[%s] = %v, want the separate values", key, got)
		}
	}
	if got := buildMP3DesiredTags(meta)["GENRE"]; len(got) != 3 {
		t.Errorf("buildMP3DesiredTags[GENRE] = %v, want all three genres", got)
	}
}

// TestEnginesRenderTheSameValuesDifferently pins the one place the two engines are
// allowed to disagree, and why. FLAC emits one comment per value because that is what
// the format means and ffmpeg joins them back on read anyway; MP3 emits one joined
// frame because ffprobe reads only the first value of a null-separated frame, so the
// spec-correct form would hide every genre after the first from Plex.
func TestEnginesRenderTheSameValuesDifferently(t *testing.T) {
	genres := []string{"hip hop", "rap"}

	if got := renderFLACValues(genres); !slices.Equal(got, genres) {
		t.Errorf("renderFLACValues = %v, want one comment per value", got)
	}
	if got := renderMP3Values(genres, false); len(got) != 1 || got[0] != "hip hop; rap" {
		t.Errorf("renderMP3Values = %v, want the joined single value", got)
	}

	// A field with nothing to say renders to nothing at all on either engine, so
	// remove_values clears the tag rather than leaving a blank value behind.
	if got := renderFLACValues([]string{"", "  "}); got != nil {
		t.Errorf("renderFLACValues(blanks) = %v, want nil", got)
	}
	if got := renderMP3Values([]string{"", "  "}, false); got != nil {
		t.Errorf("renderMP3Values(blanks) = %v, want nil", got)
	}
	if got := renderMP3Values([]string{"", "  "}, true); got != nil {
		t.Errorf("renderMP3Values(blanks, multi-value) = %v, want nil", got)
	}

	// With the profile's mp3_multi_value_tags on, MP3 says what FLAC says.
	if got := renderMP3Values(genres, true); !slices.Equal(got, genres) {
		t.Errorf("renderMP3Values(multi-value) = %v, want one value per genre", got)
	}
}

// TestFFmpegJoinsRepeatedVorbisComments encodes the measurement the FLAC decision
// rests on. Plex reads tags through ffmpeg, and ffmpeg's FLAC demuxer joins repeated
// comments into one delimited string by itself — so writing the spec-correct form
// costs the ffmpeg-backed players nothing and they need no separator convention of
// ours. If a future ffmpeg stops doing this, the trade-off changes and this test is
// where that shows up.
func TestFFmpegJoinsRepeatedVorbisComments(t *testing.T) {
	requireTool(t, "metaflac")
	requireTool(t, "ffprobe")

	path := synthAudio(t, ".flac")
	meta := models.FileTags{Artist: "A", Album: "B", Title: "C", Genres: []string{"hip hop", "rap"}}
	if _, _, _, err := SetFlacTags(path, meta, models.TaggerSettings{}); err != nil {
		t.Fatalf("SetFlacTags: %v", err)
	}

	// Two comments on disk...
	tags, err := getFlacTagsMap(path)
	if err != nil {
		t.Fatalf("getFlacTagsMap: %v", err)
	}
	if !slices.Equal(tags["GENRE"], []string{"hip hop", "rap"}) {
		t.Fatalf("GENRE on disk = %v, want one comment per genre", tags["GENRE"])
	}

	// ...and one delimited value to anything reading through ffmpeg.
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format_tags=GENRE", "-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if !strings.Contains(got, "hip hop") || !strings.Contains(got, "rap") {
		t.Errorf("ffprobe sees GENRE = %q, want both genres — Plex would be losing one", got)
	}
}
