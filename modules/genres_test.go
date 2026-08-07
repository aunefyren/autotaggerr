package modules

import (
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestSelectGenresRanksByVote is the property that makes the cap meaningful:
// MusicBrainz returns genres unordered, so truncating the raw list discards the
// popular genre as readily as the obscure one.
func TestSelectGenresRanksByVote(t *testing.T) {
	genres := []models.MusicBrainzNamedCount{
		{Name: "ambient", Count: 1},
		{Name: "hip hop", Count: 42},
		{Name: "jazz rap", Count: 7},
		{Name: "trip hop", Count: 3},
	}

	got := selectGenres(genres, 3)
	want := []string{"hip hop", "jazz rap", "trip hop"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("selectGenres = %v, want %v", got, want)
	}
}

// TestSelectGenresTieBreakIsStable guards idempotency rather than taste: equally
// voted genres that came back in a different order on a later fetch would produce
// a different GENRE string and re-tag the whole release group for nothing.
func TestSelectGenresTieBreakIsStable(t *testing.T) {
	forward := []models.MusicBrainzNamedCount{
		{Name: "rock", Count: 5},
		{Name: "blues", Count: 5},
		{Name: "soul", Count: 5},
	}
	reversed := []models.MusicBrainzNamedCount{
		{Name: "soul", Count: 5},
		{Name: "blues", Count: 5},
		{Name: "rock", Count: 5},
	}

	a := strings.Join(selectGenres(forward, 5), "|")
	b := strings.Join(selectGenres(reversed, 5), "|")
	if a != b {
		t.Errorf("tie order is not stable: %q vs %q", a, b)
	}
	if a != "blues|rock|soul" {
		t.Errorf("ties should break by name: got %q", a)
	}
}

func TestSelectGenresLimits(t *testing.T) {
	genres := make([]models.MusicBrainzNamedCount, 0, 20)
	for i := 20; i > 0; i-- {
		genres = append(genres, models.MusicBrainzNamedCount{Name: string(rune('a'+i)) + "-genre", Count: i})
	}

	if got := selectGenres(genres, 2); len(got) != 2 {
		t.Errorf("limit 2 returned %d genres: %v", len(got), got)
	}

	// A profile row predating the column reads zero, which must behave like a new
	// one rather than writing no genres at all.
	for _, limit := range []int{0, -1} {
		got := selectGenres(genres, limit)
		if len(got) != models.DefaultMaxGenres {
			t.Errorf("limit %d returned %d genres, want the default %d", limit, len(got), models.DefaultMaxGenres)
		}
	}

	if got := selectGenres(nil, 5); len(got) != 0 {
		t.Errorf("no genres should yield none, got %v", got)
	}
}

// TestSelectGenresDropsBlanks keeps a blank name from occupying a slot and, worse,
// producing a dangling separator once joined — a value that never matches on
// read-back re-tags the file on every scan.
func TestSelectGenresDropsBlanks(t *testing.T) {
	genres := []models.MusicBrainzNamedCount{
		{Name: "  ", Count: 99},
		{Name: "hip hop", Count: 10},
		{Name: "", Count: 50},
	}
	got := selectGenres(genres, 5)
	if len(got) != 1 || got[0] != "hip hop" {
		t.Errorf("selectGenres = %v, want [hip hop]", got)
	}
}

// TestSelectGenresPreservesMusicBrainzCasing pins the decision not to Title-case.
// MusicBrainz genres are canonically lower case, and naive title-casing mangles
// the names it cannot know about ("UK garage" -> "Uk Garage").
func TestSelectGenresPreservesMusicBrainzCasing(t *testing.T) {
	genres := []models.MusicBrainzNamedCount{{Name: "UK garage", Count: 3}}
	if got := selectGenres(genres, 5); got[0] != "UK garage" {
		t.Errorf("genre casing was altered: %q", got[0])
	}
}

// TestBuildFileTagsAppliesGenrePolicy checks the plumbing rather than the ranking:
// the profile's cap has to survive the trip from ConfigStruct into FileTags, and the
// release group's genres have to arrive ranked rather than in API order.
func TestBuildFileTagsAppliesGenrePolicy(t *testing.T) {
	track := models.Track{Position: 1, Title: "Song"}
	resp := models.MusicBrainzReleaseResponse{
		Title:        "Album",
		ArtistCredit: []models.ArtistCredit{{Name: "Artist", Artist: models.Artist{ID: "art-1", Name: "Artist"}}},
		Media:        []models.MusicBrainzMedia{{Position: 1, Tracks: []models.Track{track}}},
	}
	// Deliberately worst-first, so API order and ranked order disagree.
	resp.ReleaseGroup.Genres = []struct {
		Count          int    `json:"count"`
		ID             string `json:"id"`
		Name           string `json:"name"`
		Disambiguation string `json:"disambiguation"`
	}{
		{Count: 1, Name: "ambient"},
		{Count: 30, Name: "hip hop"},
		{Count: 12, Name: "jazz rap"},
	}

	capped, err := BuildFileTags(track, resp.Media[0], resp, models.ConfigStruct{AutotaggerrMaxGenres: 2})
	if err != nil {
		t.Fatalf("BuildFileTags: %v", err)
	}
	if len(capped.Genres) != 2 {
		t.Fatalf("genres = %v, want the cap of 2 to be honoured", capped.Genres)
	}
	if capped.Genres[0] != "hip hop" || capped.Genres[1] != "jazz rap" {
		t.Errorf("genres = %v, want the two most-voted in rank order", capped.Genres)
	}

	// An unset cap falls back to the default rather than writing nothing.
	defaulted, err := BuildFileTags(track, resp.Media[0], resp, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("BuildFileTags (no cap): %v", err)
	}
	if len(defaulted.Genres) != 3 {
		t.Errorf("genres = %v, want all three under the default cap", defaulted.Genres)
	}
}
