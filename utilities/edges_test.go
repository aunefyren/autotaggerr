package utilities

import (
	"testing"
	"time"
)

func TestPrintASCII(t *testing.T) {
	PrintASCII() // prints the banner; must not panic
}

func TestMBVorbisKeyForEveryType(t *testing.T) {
	for in, want := range map[string]string{
		"release_group": "MUSICBRAINZ_RELEASEGROUPID",
		"Recording":     "MUSICBRAINZ_TRACKID",
		"artist":        "MUSICBRAINZ_ALBUMARTISTID",
	} {
		if got, ok := MBVorbisKeyFor(in); !ok || got != want {
			t.Errorf("MBVorbisKeyFor(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// TestFindEarlierDayOnTheDayItself: a Monday's earlier Monday is itself (at
// midnight), and a Sunday's earlier Sunday is itself unchanged.
func TestFindEarlierDayOnTheDayItself(t *testing.T) {
	monday := time.Date(2026, 10, 5, 15, 30, 0, 0, time.Local) // a Monday
	got, err := FindEarlierMonday(monday)
	if err != nil {
		t.Fatal(err)
	}
	if got.Day() != 5 || got.Hour() != 0 {
		t.Errorf("FindEarlierMonday(Monday) = %v, want the same day at midnight", got)
	}

	sunday := time.Date(2026, 10, 4, 15, 30, 0, 0, time.Local)
	got, err = FindEarlierSunday(sunday)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(sunday) {
		t.Errorf("FindEarlierSunday(Sunday) = %v, want it unchanged", got)
	}
}

// TestExtractHelpersRejectMalformedPaths: every extractor fails the same way the
// splitter does, and a segment that is only whitespace counts as empty.
func TestExtractHelpersRejectMalformedPaths(t *testing.T) {
	root := "/music"

	// A relative root against an absolute path cannot be made relative.
	if _, _, _, _, err := SplitPathIntoMediaStrings("music", "/music/A/B/01.flac"); err == nil {
		t.Error("a relative root against an absolute track path should fail")
	}

	short := "/music/A/01.flac"
	if _, err := ExtractArtistNameFromTrackFilePath(root, short); err == nil {
		t.Error("artist from a too-short path should fail")
	}
	if _, err := ExtractAlbumNameFromTrackFilePath(root, short); err == nil {
		t.Error("album from a too-short path should fail")
	}
	if _, err := ExtractMediaNameFromTrackFilePath(root, short); err == nil {
		t.Error("media from a too-short path should fail")
	}

	if _, err := ExtractArtistNameFromTrackFilePath(root, "/music/ /B/01.flac"); err == nil {
		t.Error("a whitespace artist folder should be rejected as empty")
	}
	if _, err := ExtractAlbumNameFromTrackFilePath(root, "/music/A/ /01.flac"); err == nil {
		t.Error("a whitespace album folder should be rejected as empty")
	}
	if _, err := ExtractMediaNameFromTrackFilePath(root, "/music/A/B/ /01.flac"); err == nil {
		t.Error("a whitespace media folder should be rejected as empty")
	}

	if _, err := ExtractTrackFileName("/"); err == nil {
		t.Error("the filesystem root is not a track file")
	}
}
