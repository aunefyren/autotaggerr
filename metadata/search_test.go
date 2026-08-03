package metadata

import "testing"

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

// TestReleaseSearchQueryEmpty: a query is empty when every field is blank or
// whitespace — the UI submits all fields on every search, so blank-but-present
// must count as empty and a single filled field must not.
func TestReleaseSearchQueryEmpty(t *testing.T) {
	empty := []ReleaseSearchQuery{
		{},
		{Text: "   "},
		{Artist: " ", Release: "\t", Country: " "},
		{Tracks: 0, Limit: 50, Offset: 5},
	}
	for _, q := range empty {
		if !q.Empty() {
			t.Errorf("Empty() = false for %+v, want true", q)
		}
	}

	filled := []ReleaseSearchQuery{
		{Text: "bee gees"},
		{ArtistID: "abc-123"},
		{Tracks: 17},
		{Barcode: "0602537"},
	}
	for _, q := range filled {
		if q.Empty() {
			t.Errorf("Empty() = true for %+v, want false", q)
		}
	}
}
