package utilities

import (
	"io"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/sirupsen/logrus"
)

// Several functions under test log through the package-level logger.Log, which is
// nil until InitLogger runs. Point it at a discarding logger so tests exercise the
// logic without touching the filesystem or panicking on a nil logger.
func init() {
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

func TestCanon(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"  ABBA ", "abba"},
		{"Beyoncé", "beyoncé"}, // Canon lowercases + NFC-normalizes but keeps diacritics
		{"", ""},
	}
	for _, tt := range tests {
		if got := Canon(tt.in); got != tt.want {
			t.Errorf("Canon(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	// Canon does NOT fold diacritics — that's CanonLoose's job.
	if Canon("Beyoncé") == "beyonce" {
		t.Error("Canon unexpectedly folded a diacritic")
	}
}

// TestCanonLoose covers the typography/diacritic folding that underpins fuzzy
// artist/album matching — the same class of difference behind the known Jellyfin
// duplicate-artist issue (curly vs straight apostrophe).
func TestCanonLoose(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Schindler's List", "schindlers list"},
		{"Schindler’s List", "schindlers list"}, // curly apostrophe → same as straight
		{"Beyoncé’s", "beyonces"},               // diacritic + curly apostrophe folded
		{"AC/DC", "acdc"},                       // punctuation stripped
		{"  multiple   spaces ", "multiple spaces"},
	}
	for _, tt := range tests {
		if got := CanonLoose(tt.in); got != tt.want {
			t.Errorf("CanonLoose(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEqLoose(t *testing.T) {
	if !EqLoose("Schindler's", "Schindler’s") {
		t.Error("EqLoose should treat straight and curly apostrophes as equal")
	}
	if EqLoose("Beyoncé", "Adele") {
		t.Error("EqLoose should not match different names")
	}
}

func TestNormPath(t *testing.T) {
	if got := NormPath("/Music/Artist/Album/"); got != "/music/artist/album" {
		t.Errorf("NormPath = %q, want /music/artist/album", got)
	}
}

// TestPathBaseHelpers pins the album-vs-media disambiguation used by
// FindTrackFileByPath: BaseDir is the immediate folder, Grandfather is one above.
func TestPathBaseHelpers(t *testing.T) {
	withMedia := "/root/Artist/Album (2020)/CD1/01 Track.flac"
	if got := BaseOfPathAny(withMedia); got != "01 Track.flac" {
		t.Errorf("BaseOfPathAny = %q", got)
	}
	if got := BaseDirOfPathAny(withMedia); got != "CD1" {
		t.Errorf("BaseDirOfPathAny = %q, want CD1", got)
	}
	if got := GrandfatherDirOfPathAny(withMedia); got != "Album (2020)" {
		t.Errorf("GrandfatherDirOfPathAny = %q, want 'Album (2020)'", got)
	}

	noMedia := "/root/Artist/Album (2020)/01 Track.flac"
	if got := BaseDirOfPathAny(noMedia); got != "Album (2020)" {
		t.Errorf("BaseDirOfPathAny(noMedia) = %q, want 'Album (2020)'", got)
	}
	if got := GrandfatherDirOfPathAny(noMedia); got != "Artist" {
		t.Errorf("GrandfatherDirOfPathAny(noMedia) = %q, want Artist", got)
	}
}

func TestSplitPathIntoMediaStrings(t *testing.T) {
	root := filepath.Join("/", "music")

	t.Run("no media folder", func(t *testing.T) {
		track := filepath.Join(root, "Artist", "Album (2020)", "01 Track.flac")
		artist, album, containers, file, err := SplitPathIntoMediaStrings(root, track)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if artist != "Artist" || album != "Album (2020)" || file != "01 Track.flac" {
			t.Errorf("got artist=%q album=%q file=%q", artist, album, file)
		}
		if len(containers) != 0 {
			t.Errorf("expected no containers, got %v", containers)
		}
	})

	t.Run("with media folder", func(t *testing.T) {
		track := filepath.Join(root, "Artist", "Album", "CD1", "01.flac")
		_, _, containers, _, err := SplitPathIntoMediaStrings(root, track)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reflect.DeepEqual(containers, []string{"CD1"}) {
			t.Errorf("containers = %v, want [CD1]", containers)
		}
	})

	t.Run("too short", func(t *testing.T) {
		track := filepath.Join(root, "Artist", "track.flac")
		if _, _, _, _, err := SplitPathIntoMediaStrings(root, track); err == nil {
			t.Error("expected error for path too short")
		}
	})

	t.Run("last segment not a file", func(t *testing.T) {
		track := filepath.Join(root, "Artist", "Album", "TrackNoExtension")
		if _, _, _, _, err := SplitPathIntoMediaStrings(root, track); err == nil {
			t.Error("expected error when last segment has no extension")
		}
	})

	t.Run("outside root", func(t *testing.T) {
		track := filepath.Join("/", "other", "Artist", "Album", "01.flac")
		if _, _, _, _, err := SplitPathIntoMediaStrings(root, track); err == nil {
			t.Error("expected error for path outside root")
		}
	})
}

func TestExtractHelpers(t *testing.T) {
	root := filepath.Join("/", "music")
	withMedia := filepath.Join(root, "Artist", "Album", "CD2", "05.flac")
	noMedia := filepath.Join(root, "Artist", "Album", "05.flac")

	if got, err := ExtractArtistNameFromTrackFilePath(root, noMedia); err != nil || got != "Artist" {
		t.Errorf("ExtractArtistName = %q, err=%v", got, err)
	}
	if got, err := ExtractAlbumNameFromTrackFilePath(root, noMedia); err != nil || got != "Album" {
		t.Errorf("ExtractAlbumName = %q, err=%v", got, err)
	}
	if got, err := ExtractMediaNameFromTrackFilePath(root, withMedia); err != nil || got != "CD2" {
		t.Errorf("ExtractMediaName(withMedia) = %q, err=%v", got, err)
	}
	// no media folder present -> empty string, no error
	if got, err := ExtractMediaNameFromTrackFilePath(root, noMedia); err != nil || got != "" {
		t.Errorf("ExtractMediaName(noMedia) = %q, err=%v (want empty)", got, err)
	}
	if got, err := ExtractTrackFileName(noMedia); err != nil || got != "05.flac" {
		t.Errorf("ExtractTrackFileName = %q, err=%v", got, err)
	}
}

func TestMBVorbisKeyFor(t *testing.T) {
	tests := []struct {
		in    string
		want  string
		wantK bool
	}{
		{"track", "MUSICBRAINZ_RELEASETRACKID", true},
		{"TRACK", "MUSICBRAINZ_RELEASETRACKID", true}, // case-insensitive
		{"recording", "MUSICBRAINZ_TRACKID", true},
		{"release", "MUSICBRAINZ_ALBUMID", true},
		{"bogus", "", false},
	}
	for _, tt := range tests {
		got, ok := MBVorbisKeyFor(tt.in)
		if got != tt.want || ok != tt.wantK {
			t.Errorf("MBVorbisKeyFor(%q) = (%q,%v), want (%q,%v)", tt.in, got, ok, tt.want, tt.wantK)
		}
	}
}

func TestNormalizeTagValueAndPadding(t *testing.T) {
	if got := NormalizeTagValue("  hello  "); got != "hello" {
		t.Errorf("NormalizeTagValue = %q, want hello", got)
	}
	if got := IntToPaddedString(5); got != "05" {
		t.Errorf("IntToPaddedString(5) = %q, want 05", got)
	}
	if got := IntToPaddedString(12); got != "12" {
		t.Errorf("IntToPaddedString(12) = %q, want 12", got)
	}
}

// TestDiffFlacTags exercises the "unchanged -> skip" logic that governs whether a
// file is rewritten, including the remove-values policy branch.
func TestDiffFlacTags(t *testing.T) {
	t.Run("unchanged ignores key case and trailing space", func(t *testing.T) {
		existing := map[string][]string{"TITLE": {"Song "}}
		desired := map[string][]string{"title": {"Song"}}
		changes, has := DiffFlacTags(existing, desired, models.ConfigStruct{})
		if has || len(changes) != 0 {
			t.Errorf("expected no changes, got %v (has=%v)", changes, has)
		}
	})

	t.Run("detects a changed value", func(t *testing.T) {
		existing := map[string][]string{"ARTIST": {"Old"}}
		desired := map[string][]string{"artist": {"New"}}
		changes, has := DiffFlacTags(existing, desired, models.ConfigStruct{})
		if !has || !reflect.DeepEqual(changes, map[string][]string{"ARTIST": {"New"}}) {
			t.Errorf("changes = %v (has=%v), want {ARTIST:New}", changes, has)
		}
	})

	t.Run("empty desired is skipped when RemoveValues is off", func(t *testing.T) {
		existing := map[string][]string{"COMMENT": {"present"}}
		desired := map[string][]string{"comment": nil}
		changes, has := DiffFlacTags(existing, desired, models.ConfigStruct{AutotaggerrRemoveValues: false})
		if has || len(changes) != 0 {
			t.Errorf("expected empty value to be skipped, got %v", changes)
		}
	})

	t.Run("empty desired removes value when RemoveValues is on", func(t *testing.T) {
		existing := map[string][]string{"COMMENT": {"present"}}
		desired := map[string][]string{"comment": nil}
		changes, has := DiffFlacTags(existing, desired, models.ConfigStruct{AutotaggerrRemoveValues: true})
		if !has || !reflect.DeepEqual(changes, map[string][]string{"COMMENT": nil}) {
			t.Errorf("changes = %v (has=%v), want {COMMENT:\"\"}", changes, has)
		}
	})
}

// TestDiffID3Tags mirrors TestDiffFlacTags: the remove-values policy has to mean the
// same thing on both engines, or one tagger profile produces two behaviours depending
// on the file's format.
func TestDiffID3Tags(t *testing.T) {
	t.Run("empty desired is skipped when RemoveValues is off", func(t *testing.T) {
		changes, has := DiffID3Tags(map[string][]string{"TIT2": {"x"}}, map[string][]string{"tit2": nil},
			models.ConfigStruct{AutotaggerrRemoveValues: false})
		if has || len(changes) != 0 {
			t.Errorf("expected empty desired to be skipped, got %v", changes)
		}
	})

	t.Run("empty desired removes value when RemoveValues is on", func(t *testing.T) {
		changes, has := DiffID3Tags(map[string][]string{"TIT2": {"x"}}, map[string][]string{"tit2": nil},
			models.ConfigStruct{AutotaggerrRemoveValues: true})
		if !has || !reflect.DeepEqual(changes, map[string][]string{"TIT2": nil}) {
			t.Errorf("changes = %v (has=%v), want {TIT2:\"\"}", changes, has)
		}
	})

	t.Run("an already absent tag is not a change", func(t *testing.T) {
		changes, has := DiffID3Tags(map[string][]string{}, map[string][]string{"tit2": nil},
			models.ConfigStruct{AutotaggerrRemoveValues: true})
		if has || len(changes) != 0 {
			t.Errorf("removing a tag that is not there is not a change, got %v", changes)
		}
	})

	t.Run("detects a changed value", func(t *testing.T) {
		changes, has := DiffID3Tags(map[string][]string{"TPE1": {"Old"}}, map[string][]string{"tpe1": {"New"}},
			models.ConfigStruct{})
		if !has || !reflect.DeepEqual(changes, map[string][]string{"TPE1": {"New"}}) {
			t.Errorf("changes = %v (has=%v), want {TPE1:New}", changes, has)
		}
	})
}

// SortTagChanges orders a diff by field name in place, so the same write always
// reports its fields in the same order rather than shuffling on each map iteration.
func TestSortTagChanges(t *testing.T) {
	changes := []models.TagChange{
		{Field: "title"}, {Field: "album"}, {Field: "artist"},
	}
	SortTagChanges(changes)
	got := []string{changes[0].Field, changes[1].Field, changes[2].Field}
	want := []string{"album", "artist", "title"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	// Empty and single-element slices must not panic.
	SortTagChanges(nil)
	SortTagChanges([]models.TagChange{{Field: "solo"}})
}

// TestJoinTagValues covers the one spelling every multi-value tag field uses. The
// blank-dropping matters more than it looks: a dangling separator never matches on
// read-back, so it would re-tag the file on every scan forever.
func TestJoinTagValues(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ""},
		{"single", []string{"hip hop"}, "hip hop"},
		{"several", []string{"hip hop", "rap"}, "hip hop; rap"},
		{"drops blanks", []string{"hip hop", "", "  ", "rap"}, "hip hop; rap"},
		{"all blank", []string{"", "  "}, ""},
		{"trims", []string{" hip hop ", "rap "}, "hip hop; rap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := JoinTagValues(tc.in); got != tc.want {
				t.Errorf("JoinTagValues(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestJoinTagValuesRoundTripsThroughTheDiff is the property the separator has to
// have: a joined value read back as one string must compare equal to itself, or
// the writers never converge.
func TestJoinTagValuesRoundTripsThroughTheDiff(t *testing.T) {
	joined := JoinTagValues([]string{"USUM72108711", "USUM72108712"})
	existing := map[string][]string{"ISRC": {joined}}
	desired := map[string][]string{"ISRC": {joined}}

	if _, has := DiffFlacTags(existing, desired, models.ConfigStruct{}); has {
		t.Error("a joined value must not read back as a change on FLAC")
	}
	if _, has := DiffID3Tags(existing, desired, models.ConfigStruct{}); has {
		t.Error("a joined value must not read back as a change on MP3")
	}
}

// TestDiffIsOrderSensitive pins the comparison change that came with multi-value
// support. Values used to be sorted before being compared, which was harmless while
// every field was one joined string — but genres are ranked by community vote and an
// artist credit reads in its credited order, so a re-ordering the diff cannot see is
// a re-ordering that never gets written.
func TestDiffIsOrderSensitive(t *testing.T) {
	existing := map[string][]string{"GENRE": {"rap", "hip hop"}}
	desired := map[string][]string{"GENRE": {"hip hop", "rap"}}

	changes, has := DiffFlacTags(existing, desired, models.ConfigStruct{})
	if !has {
		t.Fatal("the same genres in a different order must register as a change")
	}
	if !reflect.DeepEqual(changes["GENRE"], []string{"hip hop", "rap"}) {
		t.Errorf("changes[GENRE] = %v, want the desired order", changes["GENRE"])
	}

	// Same order, no change — the other half of the property.
	if _, has := DiffFlacTags(desired, desired, models.ConfigStruct{}); has {
		t.Error("identical values in identical order must not be a change")
	}
}

// TestDiffDedupsBothSides covers the file another tagger wrote the same value into
// twice: without a dedup on the have side it can never match, and the file is
// rewritten on every scan forever.
func TestDiffDedupsBothSides(t *testing.T) {
	existing := map[string][]string{"GENRE": {"rock", "Rock"}}
	desired := map[string][]string{"GENRE": {"rock"}}

	if _, has := DiffFlacTags(existing, desired, models.ConfigStruct{}); has {
		t.Error("a duplicated existing value must not read back as a difference")
	}
}

// TestNormalizeTagValuesDoesNotDedup is the counterpart: whether a release that
// credits one catalogue number under two labels should say it once or twice is a
// question about the data, so the build-time helper leaves it alone and only the
// comparison dedups.
func TestNormalizeTagValuesDoesNotDedup(t *testing.T) {
	got := NormalizeTagValues([]string{"CAT-1", " CAT-1 ", "", "CAT-2"})
	want := []string{"CAT-1", "CAT-1", "CAT-2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NormalizeTagValues = %v, want %v", got, want)
	}
	if got := NormalizeTagValues([]string{"", "  "}); got != nil {
		t.Errorf("all-blank input = %v, want nil", got)
	}
}
