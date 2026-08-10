package collection

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestFollowCutoffIsOffByDefault: a follow with no cutoff wants the whole back
// catalogue, which is what following meant before the field existed. Every artist
// already followed carries a zero here, so this is what stops an upgrade quietly
// emptying their missing list.
func TestFollowCutoffIsOffByDefault(t *testing.T) {
	artist := models.CollectionArtist{}
	for _, date := range []string{"", "1969", "1969-08", "1969-08-15", "2030-01-01"} {
		if !FollowWants(artist, "Album", nil, date) {
			t.Errorf("date %q was excluded with no cutoff set", date)
		}
	}
}

// TestFollowCutoffFiltersByYear covers the point of the feature, at every date
// precision MusicBrainz actually stores. The year is the prefix of all three, which is
// why the cutoff is a year rather than a date.
func TestFollowCutoffFiltersByYear(t *testing.T) {
	artist := models.CollectionArtist{FollowFromYear: 2020}
	cases := []struct {
		date string
		want bool
	}{
		{"2019-12-31", false},
		{"2019", false},
		{"2020", true},       // year-only, on the boundary: the cutoff year is included
		{"2020-01-01", true}, // the boundary itself
		{"2020-06", true},
		{"2024-03-11", true},
	}
	for _, c := range cases {
		if got := FollowWants(artist, "Album", nil, c.date); got != c.want {
			t.Errorf("FollowWants(from 2020, date %q) = %v, want %v", c.date, got, c.want)
		}
	}
}

// TestFollowCutoffExcludesUndatedReleases pins the judgement call. A cutoff is opt-in
// and exists to keep the back catalogue out; a release-group MusicBrainz has no date
// for cannot be shown to satisfy it, and anything actually being released is dated
// upstream before it comes out. Including them would let the noise back in through the
// one gap nobody can see.
func TestFollowCutoffExcludesUndatedReleases(t *testing.T) {
	artist := models.CollectionArtist{FollowFromYear: 2020}
	for _, date := range []string{"", "   ", "n/a", "20"} {
		if FollowWants(artist, "Album", nil, date) {
			t.Errorf("undated release %q was wanted despite a 2020 cutoff", date)
		}
	}

	// ...and the same values are wanted again the moment the cutoff is removed, so
	// nothing is lost permanently by an undated row.
	artist.FollowFromYear = 0
	if !FollowWants(artist, "Album", nil, "") {
		t.Error("an undated release should be wanted again once the cutoff is cleared")
	}
}

// TestFollowCutoffCombinesWithTheOtherRules: the cutoff narrows a follow, it does not
// widen one. A single released after the cutoff is still not wanted by an
// albums-and-EPs follow.
func TestFollowCutoffCombinesWithTheOtherRules(t *testing.T) {
	artist := models.CollectionArtist{FollowFromYear: 2020, FollowTypes: "Album"}
	if FollowWants(artist, "Single", nil, "2024") {
		t.Error("a recent single was wanted by an Album-only follow")
	}
	if FollowWants(artist, "Album", []string{"Live"}, "2024") {
		t.Error("a recent live album was wanted with secondary types off")
	}
	if !FollowWants(artist, "Album", nil, "2024") {
		t.Error("a recent studio album should be wanted")
	}
}

// TestFollowWantsStoredCarriesTheDate is the stored-row path the artist page renders
// through. It reads a different column set than the discography sync, so it has to be
// shown to apply the same cutoff — otherwise a page would label an album "wanted" that
// the sync would never have recorded.
func TestFollowWantsStoredCarriesTheDate(t *testing.T) {
	artist := models.CollectionArtist{FollowFromYear: 2020}
	if FollowWantsStored(artist, "Album", "", "2019-05-01") {
		t.Error("a stored row before the cutoff was reported as wanted")
	}
	if !FollowWantsStored(artist, "Album", "", "2021-05-01") {
		t.Error("a stored row after the cutoff should be wanted")
	}
}

func TestReleaseYear(t *testing.T) {
	cases := []struct {
		date string
		year int
		ok   bool
	}{
		{"1999", 1999, true},
		{"1999-06", 1999, true},
		{"1999-06-15", 1999, true},
		{" 2001-01-01 ", 2001, true},
		{"", 0, false},
		{"99", 0, false},
		{"abcd", 0, false},
		{"0000", 0, false},
	}
	for _, c := range cases {
		year, ok := releaseYear(c.date)
		if year != c.year || ok != c.ok {
			t.Errorf("releaseYear(%q) = (%d, %v), want (%d, %v)", c.date, year, ok, c.year, c.ok)
		}
	}
}
