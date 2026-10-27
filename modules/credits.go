package modules

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
)

// Credits: the people behind a track, from MusicBrainz's relationships — the release's
// own (a producer for the whole release), the recording's (a guitarist, a remixer) and
// the works it performs (the composer and lyricist of the song). This follows Picard
// with "Use track and release relationships" on (picard/mbjson.py,
// _relations_to_metadata), so a file tagged here and one tagged by Picard agree.

// creditRelTypes maps an artist relationship type to the credit it becomes — Picard's
// _ARTIST_REL_TYPES, without "director" (a video credit) and with Picard's commented-out
// "mastering" and "recording" left out the same way. "performer:" credits are
// performances, rendered with their attributes (performanceRole).
var creditRelTypes = map[string]string{
	"arranger":             "arranger",
	"audio":                "engineer",
	"chorus master":        "performer:chorus master",
	"composer":             "composer",
	"concertmaster":        "performer:concertmaster",
	"conductor":            "conductor",
	"engineer":             "engineer",
	"instrument arranger":  "arranger",
	"librettist":           "lyricist",
	"live sound":           "engineer",
	"lyricist":             "lyricist",
	"mix-DJ":               "djmixer",
	"mix":                  "mixer",
	"orchestrator":         "arranger",
	"performing orchestra": "performer:orchestra",
	"producer":             "producer",
	"remixer":              "remixer",
	"sound":                "engineer",
	"vocal arranger":       "arranger",
	"writer":               "writer",
}

// performancePrefixes are the attributes that qualify a performance rather than name
// it ("guest", "additional"), written ahead of the instrument as Picard does.
var performancePrefixes = map[string]bool{"guest": true, "additional": true, "minor": true, "solo": true}

// performanceRole renders a performance's attributes the way Picard does: qualifying
// attributes first, then the instruments or voices, the last two joined by "and" —
// "guest bass and lead vocals". With no instrument it is "instruments" or "vocals" for
// those relationship types, and empty for a plain "performer". Standardised names:
// Picard's default ignores how a release credits an instrument.
func performanceRole(relType string, attributes []string) string {
	var prefixes, nouns []string
	for _, attribute := range attributes {
		if performancePrefixes[attribute] {
			prefixes = append(prefixes, attribute)
		} else {
			nouns = append(nouns, attribute)
		}
	}
	var noun string
	switch {
	case len(nouns) > 1:
		noun = strings.Join(nouns[:len(nouns)-1], ", ") + " and " + nouns[len(nouns)-1]
	case len(nouns) == 1:
		noun = nouns[0]
	case relType == "instrument":
		noun = "instruments"
	case relType == "vocal":
		noun = "vocals"
	}
	return strings.TrimSpace(strings.Join(append(prefixes, noun), " "))
}

// TrackCredits collects one track's credits from the release's, the recording's and
// its works' relationships. A DJ-mix credit naming a medium ("medium 2") applies only to
// that medium, as Picard does. A work performed as instrumental takes no lyricist.
// Names follow the profile: the artist's current name, or the name the relationship
// credits them as.
func TrackCredits(release models.MusicBrainzReleaseResponse, media models.MusicBrainzMedia, track models.Track, tagger models.TaggerSettings) models.Credits {
	var credits models.Credits
	collect := func(relations []models.MusicBrainzRelation, instrumental bool) {
		for _, relation := range relations {
			if relation.TargetType == "artist" && relation.Artist != nil {
				addArtistCredit(&credits, relation, media.Position, instrumental, tagger)
			}
		}
	}
	collect(release.Relations, false)
	collect(track.Recording.Relations, false)
	for _, relation := range track.Recording.Relations {
		if relation.TargetType != "work" || relation.Type != "performance" || relation.Work == nil {
			continue
		}
		instrumental := false
		for _, attribute := range relation.Attributes {
			instrumental = instrumental || attribute == "instrumental"
		}
		credits.WorkIDs = addUnique(credits.WorkIDs, relation.Work.ID)
		credits.Works = addUnique(credits.Works, relation.Work.Title)
		collect(relation.Work.Relations, instrumental)
	}
	return credits
}

func addArtistCredit(c *models.Credits, relation models.MusicBrainzRelation, medium int, instrumental bool, tagger models.TaggerSettings) {
	artist := relation.Artist
	name := artist.Name
	if !tagger.UseCurrentArtistName && strings.TrimSpace(relation.TargetCredit) != "" {
		name = relation.TargetCredit
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}

	switch relation.Type {
	case "instrument", "vocal", "performer":
		c.Performers = addUniquePerformer(c.Performers, models.Performer{Role: performanceRole(relation.Type, relation.Attributes), Name: name})
		return
	case "mix-DJ":
		if len(relation.Attributes) > 0 {
			for _, attribute := range relation.Attributes {
				if n, ok := strings.CutPrefix(attribute, "medium "); ok {
					if number, err := strconv.Atoi(n); err == nil && number == medium {
						c.DJMixers = addUnique(c.DJMixers, name)
					}
				}
			}
			return
		}
	}

	credit, known := creditRelTypes[relation.Type]
	if !known {
		return
	}
	if role, ok := strings.CutPrefix(credit, "performer:"); ok {
		c.Performers = addUniquePerformer(c.Performers, models.Performer{Role: role, Name: name})
		return
	}
	switch credit {
	case "composer":
		c.Composers = addUnique(c.Composers, name)
		sort := strings.TrimSpace(artist.SortName)
		if sort == "" {
			sort = name
		}
		c.ComposerSorts = addUnique(c.ComposerSorts, sort)
		c.ComposerIDs = addUnique(c.ComposerIDs, artist.ID)
	case "lyricist":
		if !instrumental {
			c.Lyricists = addUnique(c.Lyricists, name)
		}
	case "writer":
		c.Writers = addUnique(c.Writers, name)
	case "arranger":
		c.Arrangers = addUnique(c.Arrangers, name)
	case "conductor":
		c.Conductors = addUnique(c.Conductors, name)
	case "producer":
		c.Producers = addUnique(c.Producers, name)
	case "engineer":
		c.Engineers = addUnique(c.Engineers, name)
	case "mixer":
		c.Mixers = addUnique(c.Mixers, name)
	case "remixer":
		c.Remixers = addUnique(c.Remixers, name)
	case "djmixer":
		c.DJMixers = addUnique(c.DJMixers, name)
	}
}

func addUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func addUniquePerformer(performers []models.Performer, p models.Performer) []models.Performer {
	for _, existing := range performers {
		if existing == p {
			return performers
		}
	}
	return append(performers, p)
}

// PerformerVorbis is a performer as Picard writes PERFORMER: "Name (role)", or the
// name alone when there is no role.
func PerformerVorbis(p models.Performer) string {
	if p.Role == "" {
		return p.Name
	}
	return fmt.Sprintf("%s (%s)", p.Name, p.Role)
}

// id3PairSeparator joins the two halves of one entry of an ID3v2.4 paired list frame
// (TIPL, TMCL) inside a single desired value, so the diff compares and deduplicates
// whole pairs — "producer" twice, for two producers, is two pairs, not one role. The
// writer joins values with the same null byte, which is what makes the frame
// "role\0name\0role\0name".
const id3PairSeparator = "\x00"

// id3Pair is one paired-frame entry as a desired value.
func id3Pair(role, name string) string { return role + id3PairSeparator + name }

// involvedPeople is TIPL: Picard's arranger, engineer, producer, DJ-mix and mix roles.
func involvedPeople(c models.Credits) []string {
	var out []string
	add := func(role string, names []string) {
		for _, name := range names {
			out = append(out, id3Pair(role, name))
		}
	}
	add("arranger", c.Arrangers)
	add("engineer", c.Engineers)
	add("producer", c.Producers)
	add("DJ-mix", c.DJMixers)
	add("mix", c.Mixers)
	return out
}

// musicianCredits is TMCL: each performance as instrument and name. A performance
// with no role is credited as "performer", since a TMCL entry needs one.
func musicianCredits(c models.Credits) []string {
	out := make([]string, 0, len(c.Performers))
	for _, p := range c.Performers {
		role := p.Role
		if role == "" {
			role = "performer"
		}
		out = append(out, id3Pair(role, p.Name))
	}
	return out
}
