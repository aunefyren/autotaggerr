package modules

import (
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func twoMediumRelease() models.MusicBrainzReleaseResponse {
	return models.MusicBrainzReleaseResponse{
		ID: "rel-1",
		Media: []models.MusicBrainzMedia{
			{
				Position: 1, Title: "Disc one",
				Tracks: []models.Track{
					{ID: "t1", Title: "Opener", Position: 1, Number: "1"},
					{ID: "t2", Title: "Second", Position: 2, Number: "2"},
				},
			},
			{
				Position: 2, Title: "Disc two",
				Tracks: []models.Track{
					{ID: "t3", Title: "Bonus", Position: 1, Number: "1"},
				},
			},
		},
	}
}

// TestReleaseTracksFlattensMedia: the attach picker shows one list, so a multi-disc
// release must flatten in order while keeping which medium each track came from —
// two discs both have a track at position 1.
func TestReleaseTracksFlattensMedia(t *testing.T) {
	release := twoMediumRelease()
	release.Media[1].Tracks[0].Recording.ID = "rec-3"

	tracks := ReleaseTracks(release)
	if len(tracks) != 3 {
		t.Fatalf("got %d tracks, want 3", len(tracks))
	}
	if tracks[0].TrackID != "t1" || tracks[2].TrackID != "t3" {
		t.Errorf("order = %+v", tracks)
	}
	if tracks[2].Medium != 2 || tracks[2].MediumTitle != "Disc two" {
		t.Errorf("medium not carried: %+v", tracks[2])
	}
	// The recording ID is the cross-release identity; losing it here would break
	// song-level desire later (M6 pass C).
	if tracks[2].RecordingID != "rec-3" {
		t.Errorf("recording id = %q, want rec-3", tracks[2].RecordingID)
	}
}

// TestFindReleaseTrack: attach validates the caller's track against the real
// release, so a track from a different release must not be found.
func TestFindReleaseTrack(t *testing.T) {
	release := twoMediumRelease()

	got, ok := FindReleaseTrack(release, "t2")
	if !ok || got.Title != "Second" {
		t.Errorf("FindReleaseTrack(t2) = %+v, %v", got, ok)
	}
	if _, ok := FindReleaseTrack(release, "not-on-this-release"); ok {
		t.Error("FindReleaseTrack accepted a track that is not on the release")
	}
}

func TestReleaseTracksEmpty(t *testing.T) {
	if got := ReleaseTracks(models.MusicBrainzReleaseResponse{}); len(got) != 0 {
		t.Errorf("got %d tracks for an empty release", len(got))
	}
}

// TestSearchEmptyQuery: an empty query must not reach MusicBrainz at all (it would
// burn a rate-limit slot for a guaranteed-useless request). Blank-but-present
// fields count as empty too, since the UI submits every field on every search.
func TestSearchEmptyQuery(t *testing.T) {
	for _, query := range []ReleaseSearchQuery{
		{Text: "   "},
		{Artist: " ", Release: "\t", Country: " "},
		{Tracks: 0, Limit: 50},
	} {
		got, err := SearchMusicBrainzReleases(query)
		if err != nil || len(got.Releases) != 0 {
			t.Errorf("SearchMusicBrainzReleases(%+v) = %v, %v", query, got, err)
		}
	}
}

// TestLuceneQueryBuilding: the fielded query is the fix for "search cannot find
// the right release", so the clauses it renders are the contract.
func TestLuceneQueryBuilding(t *testing.T) {
	tests := []struct {
		name  string
		query ReleaseSearchQuery
		want  string
	}{
		{"free text only", ReleaseSearchQuery{Text: "bee gees"}, "(bee gees)"},
		{
			"fields are ANDed",
			ReleaseSearchQuery{Artist: "Bee Gees", Release: "Saturday Night Fever", Date: "1977"},
			`artist:"Bee Gees" AND release:"Saturday Night Fever" AND date:"1977"`,
		},
		{
			"edition-narrowing fields",
			ReleaseSearchQuery{Release: "Greatest Hits", Country: "GB", Format: "CD", Tracks: 17, Status: "Official"},
			`release:"Greatest Hits" AND country:"GB" AND format:"CD" AND status:"Official" AND tracks:17`,
		},
		{"artist mbid", ReleaseSearchQuery{ArtistID: "abc-123"}, `arid:"abc-123"`},
		{
			// Quoting is what neutralises a colon or bracket in an album title: they
			// are literal inside a phrase, so "Alien: Covenant" searches the release
			// field for the whole string rather than parsing "alien" as a field.
			"lucene syntax in a field value is inert",
			ReleaseSearchQuery{Release: `Alien: Covenant (OST) [Deluxe]`},
			`release:"Alien: Covenant (OST) [Deluxe]"`,
		},
		{
			// The quote is the one character that could still break out of the phrase.
			"a quote in a title cannot end the phrase",
			ReleaseSearchQuery{Release: `Rock 'n' "Roll"`},
			`release:"Rock 'n' \"Roll\""`,
		},
		{
			"free text keeps its syntax",
			ReleaseSearchQuery{Text: `artist:Bee AND date:1977`},
			`(artist:Bee AND date:1977)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.query.Lucene(); got != tt.want {
				t.Errorf("Lucene() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestParseMBIDInput: pasting a musicbrainz.org URL is the escape hatch for a
// release the search form cannot surface, so the entity has to be read from the
// URL path — a release-group URL must not resolve as a release.
func TestParseMBIDInput(t *testing.T) {
	const id = "1b022e01-4da6-387b-8658-8678046e4cef"

	tests := []struct {
		input      string
		wantOK     bool
		wantEntity string
	}{
		{"https://musicbrainz.org/release/" + id, true, "release"},
		{"https://musicbrainz.org/release-group/" + id + "/aliases", true, "release-group"},
		{"https://musicbrainz.org/artist/" + id, true, "artist"},
		{"  " + strings.ToUpper(id) + "  ", true, ""},
		{"Saturday Night Fever", false, ""},
		{"", false, ""},
	}
	for _, tt := range tests {
		got, ok := ParseMBIDInput(tt.input)
		if ok != tt.wantOK {
			t.Errorf("ParseMBIDInput(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if got.Entity != tt.wantEntity {
			t.Errorf("ParseMBIDInput(%q) entity = %q, want %q", tt.input, got.Entity, tt.wantEntity)
		}
		// Lower-cased so a pasted upper-case ID hits the same cache key as a fetched one.
		if got.MBID != id {
			t.Errorf("ParseMBIDInput(%q) mbid = %q, want %q", tt.input, got.MBID, id)
		}
	}
}
