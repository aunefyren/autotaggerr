package metadata

import (
	"fmt"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
)

// ReleaseSearchQuery is a fielded release search. Every field is optional and
// ANDed with the others; Text is free text matched against the whole document.
//
// Fielded search exists because free text alone cannot separate the editions that
// actually differ: "Greatest Hits" matches thousands of releases, but
// artist + year + track count identifies one. The field names map onto
// MusicBrainz's Lucene schema for /ws/2/release.
type ReleaseSearchQuery struct {
	Text     string // free text, matched against all fields
	Artist   string // artist credit name
	ArtistID string // artist MBID — exact, and immune to spelling

	Release string // release title
	Date    string // year ("1977") or full date ("1977-11-15")
	Country string // release country code ("GB", "US", "XW" for worldwide)
	Format  string // medium format ("CD", "Vinyl", "Digital Media")
	Tracks  int    // total track count across all media
	Status  string // "Official", "Promotion", "Bootleg", "Pseudo-Release"
	CatNo   string // label catalogue number
	Barcode string // UPC/EAN

	Limit  int
	Offset int
}

// ReleaseSearchPage is one page of search hits. Count is MusicBrainz's total match
// count, not the page size — it is what tells the user there is more to page to.
type ReleaseSearchPage struct {
	Count    int                                     `json:"count"`
	Offset   int                                     `json:"offset"`
	Releases []models.MusicBrainzReleaseSearchResult `json:"releases"`
}

// Empty reports whether the query would search for nothing. Used to avoid burning
// a rate-limit slot on a request that cannot return anything useful.
func (q ReleaseSearchQuery) Empty() bool {
	return strings.TrimSpace(q.Text) == "" && strings.TrimSpace(q.Artist) == "" &&
		strings.TrimSpace(q.ArtistID) == "" &&
		strings.TrimSpace(q.Release) == "" && strings.TrimSpace(q.Date) == "" &&
		strings.TrimSpace(q.Country) == "" && strings.TrimSpace(q.Format) == "" &&
		strings.TrimSpace(q.Status) == "" && strings.TrimSpace(q.CatNo) == "" &&
		strings.TrimSpace(q.Barcode) == "" && q.Tracks <= 0
}

// Lucene renders the query in MusicBrainz's search syntax. Free text is passed
// through unescaped so a user who knows the syntax can write their own clause
// (`artist:Bee AND date:1977`); the structured fields are escaped and quoted,
// because those come from form inputs where a stray colon or bracket is a typo,
// not an operator.
func (q ReleaseSearchQuery) Lucene() string {
	var clauses []string
	if text := strings.TrimSpace(q.Text); text != "" {
		clauses = append(clauses, "("+text+")")
	}
	field := func(name, value string) {
		if value = strings.TrimSpace(value); value != "" {
			clauses = append(clauses, name+":"+quoteLucene(value))
		}
	}
	field("artist", q.Artist)
	field("arid", q.ArtistID)
	field("release", q.Release)
	field("date", q.Date)
	field("country", q.Country)
	field("format", q.Format)
	field("status", q.Status)
	field("catno", q.CatNo)
	field("barcode", q.Barcode)
	if q.Tracks > 0 {
		clauses = append(clauses, fmt.Sprintf("tracks:%d", q.Tracks))
	}
	return strings.Join(clauses, " AND ")
}

// luceneEscaper escapes what a quoted phrase still treats as syntax: the quote
// that would end it and the backslash that escapes. Everything else — colons,
// brackets, hyphens — is literal inside quotes, and escaping those would corrupt
// the very values that need to match exactly (an MBID is full of hyphens).
var luceneEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func quoteLucene(value string) string { return `"` + luceneEscaper.Replace(value) + `"` }
