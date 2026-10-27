package modules

import (
	"path/filepath"
	"regexp"
	"strings"
)

// ImportNameClues are the edition words a download's or album folder's name carries —
// "(2016 Remaster)", "[Deluxe Edition]", "WEB", "24-96" — read the way the edition tags
// are (ImportEditionClues), but weaker: a name is typed by whoever made the download,
// and every word here is shared by many editions. They are marked where an edition
// agrees with them and never pick, scroll to or rank one.
type ImportNameClues struct {
	// Name is the name they were read from, for the page to say where they came from.
	Name string `json:"name"`
	// Words are edition words ("Deluxe", "Remaster"), matched against an edition's
	// disambiguation.
	Words []string `json:"words,omitempty"`
	// Format is a MusicBrainz medium format: "Digital Media", "CD", "Vinyl", "SACD",
	// "Cassette".
	Format string `json:"format,omitempty"`
	// Country is an ISO 3166 code, the way MusicBrainz states a release's country.
	Country string `json:"country,omitempty"`
	// Quality is bit depth and sample rate ("24-96"). Shown only: MusicBrainz has no
	// such fact to match.
	Quality string `json:"quality,omitempty"`
	// ReleaseGroup is the group that put out the download (ParseReleaseGroup) — not an
	// edition clue, but read from the same name, and what the import review prefills
	// for the {Release Group} naming token.
	ReleaseGroup string `json:"release_group,omitempty"`
}

var (
	nameBracketed = regexp.MustCompile(`\[([^\]]*)\]|\{([^}]*)\}|\(([^)]*)\)`)
	nameToken     = regexp.MustCompile(`[\p{L}\p{N}']+`)
	nameQuality   = regexp.MustCompile(`(?i)\b(16|24|32)\s*(?:bit)?\s*[-_/ ]\s*(44(?:\.1)?|48|88(?:\.2)?|96|176(?:\.4)?|192|352(?:\.8)?|384)\s*(?:khz)?\b`)
	nameBitDepth  = regexp.MustCompile(`(?i)\b(16|24|32)\s*-?\s*bit\b`)
)

// nameEditionWords maps a lower-case token to the word it is shown as. A stem match
// against the disambiguation does the rest ("remaster" finds "remastered").
var nameEditionWords = map[string]string{
	"deluxe": "Deluxe", "remaster": "Remaster", "remastered": "Remaster", "remasters": "Remaster",
	"expanded": "Expanded", "anniversary": "Anniversary", "bonus": "Bonus", "special": "Special",
	"limited": "Limited", "collector's": "Collector", "collectors": "Collector", "reissue": "Reissue",
	"mono": "Mono", "stereo": "Stereo",
}

// nameFormats maps a lower-case token to a MusicBrainz format. Stores count as web:
// what they sell is MusicBrainz's "Digital Media".
var nameFormats = map[string]string{
	"web": "Digital Media", "webflac": "Digital Media", "itunes": "Digital Media", "bandcamp": "Digital Media",
	"qobuz": "Digital Media", "tidal": "Digital Media", "deezer": "Digital Media", "amazon": "Digital Media",
	"cd": "CD", "cdda": "CD", "cdrip": "CD",
	"vinyl": "Vinyl", "lp": "Vinyl", "vinylrip": "Vinyl",
	"sacd":     "SACD",
	"cassette": "Cassette", "tape": "Cassette",
}

// nameCountries maps a lower-case token to an ISO code. Only tokens that do not
// double as ordinary words: "us" and "uk" are left out for the one in a title.
var nameCountries = map[string]string{
	"japan": "JP", "japanese": "JP", "jpn": "JP", "jp": "JP",
	"usa": "US", "gbr": "GB", "germany": "DE", "ger": "DE", "france": "FR",
	"europe": "XE", "eu": "XE", "worldwide": "XW",
}

// ParseImportNameClues reads the edition words out of a download or album folder name.
// A name with spaces is read only inside its brackets: "Japan - Tin Drum" names a band,
// "Tin Drum [Japan]" a pressing. A scene name ("Artist-Album-Deluxe_Edition-WEB-2014-GRP")
// has no brackets and no spaces, so every token but the first (the artist) is read.
// Nil when the name says nothing about an edition.
func ParseImportNameClues(name string) *ImportNameClues {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	var parts []string
	if strings.Contains(name, " ") {
		for _, m := range nameBracketed.FindAllStringSubmatch(name, -1) {
			parts = append(parts, m[1]+m[2]+m[3])
		}
	} else {
		tokens := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '.' || r == '_' })
		if len(tokens) > 1 {
			parts = tokens[1:]
		}
	}

	clues := ImportNameClues{Name: name, ReleaseGroup: ParseReleaseGroup(name)}
	seen := map[string]bool{}
	for _, part := range parts {
		if m := nameQuality.FindStringSubmatch(part); m != nil && clues.Quality == "" {
			clues.Quality = m[1] + "-" + m[2]
		} else if m := nameBitDepth.FindStringSubmatch(part); m != nil && clues.Quality == "" {
			clues.Quality = m[1] + "-bit"
		}
		for _, token := range nameToken.FindAllString(strings.ToLower(part), -1) {
			if word, ok := nameEditionWords[token]; ok && !seen[word] {
				seen[word] = true
				clues.Words = append(clues.Words, word)
			}
			if format, ok := nameFormats[token]; ok && clues.Format == "" {
				clues.Format = format
			}
			if country, ok := nameCountries[token]; ok && clues.Country == "" {
				clues.Country = country
			}
		}
	}
	if len(clues.Words) == 0 && clues.Format == "" && clues.Country == "" && clues.Quality == "" && clues.ReleaseGroup == "" {
		return nil
	}
	return &clues
}

// ReadEditionClues reads what a set of files says about which edition they are: their
// tags (as the import scan does) and their album folder's name. It is what attach asks
// for, where the files are already in a library and no scan has read them.
func ReadEditionClues(files []string) (*ImportEditionClues, *ImportNameClues) {
	if len(files) == 0 {
		return nil, nil
	}
	_, _, _, edition := tagHint(files)
	folder := importAlbumFolder(filepath.Dir(filepath.Dir(files[0])), files[0])
	return edition, ParseImportNameClues(filepath.Base(folder))
}

var (
	// releaseGroupScene is a scene name's last part: "Artist-Album-WEB-2014-GRP".
	releaseGroupScene = regexp.MustCompile(`^[^\s]+-([A-Za-z0-9]+)$`)
	// releaseGroupAfterTags is a spaced name whose group is hyphenated onto its last
	// bracket: "Artist - Album (2014) [FLAC]-GRP".
	releaseGroupAfterTags = regexp.MustCompile(`[\])}]-([A-Za-z0-9]+)$`)
	releaseGroupYear      = regexp.MustCompile(`^(?:19|20)\d{2}$`)
)

// releaseGroupNoise are the words a scene name's last part may be that are not a group:
// what the release is, not who released it.
var releaseGroupNoise = map[string]bool{
	"web": true, "flac": true, "mp3": true, "cd": true, "cdda": true, "vinyl": true, "lp": true, "sacd": true,
	"16bit": true, "24bit": true, "320": true, "v0": true, "v2": true, "aac": true, "alac": true, "hires": true,
	"remaster": true, "remastered": true, "deluxe": true, "retail": true, "proper": true, "repack": true,
}

// ParseReleaseGroup reads the release group out of a download's or album folder's name
// the way Lidarr's parser does for the common shapes: the last hyphenated part of a
// scene name ("Artist-Album-WEB-FLAC-2014-GRP"), or a part hyphenated onto a spaced
// name's last bracket ("Artist - Album (2014) [FLAC]-GRP"). It is a guess the import
// review shows for confirmation, so it errs towards "": a scene name needs three parts
// (so "Jay-Z" is an artist, not Jay of group Z), a spaced name's hyphen must follow a
// bracket (so "Album - Remix" is a title), and a year or a format word is never a group.
func ParseReleaseGroup(name string) string {
	name = strings.TrimSpace(name)
	var group string
	switch {
	case !strings.Contains(name, " "):
		m := releaseGroupScene.FindStringSubmatch(name)
		if m == nil || len(strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '.' || r == '_' })) < 3 {
			return ""
		}
		group = m[1]
	default:
		m := releaseGroupAfterTags.FindStringSubmatch(name)
		if m == nil {
			return ""
		}
		group = m[1]
	}
	if releaseGroupYear.MatchString(group) || releaseGroupNoise[strings.ToLower(group)] {
		return ""
	}
	return group
}
