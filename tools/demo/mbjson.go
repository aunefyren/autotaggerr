package main

import (
	"encoding/json"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
)

// MusicBrainz JSON, as the web service returns it. Built as plain maps because that
// is what the stand-in server sends; the cache pre-fill decodes the same JSON into the
// app's own models, so the two can never describe a release differently.

type obj = map[string]any

func artistJSON(a *Artist) obj {
	return obj{"id": a.ID, "name": a.Name, "sort-name": a.SortName, "disambiguation": "", "type": a.Type, "country": a.Country}
}

func creditJSON(credit []Credit) []obj {
	out := make([]obj, 0, len(credit))
	for _, c := range credit {
		out = append(out, obj{"name": c.Artist.Name, "joinphrase": c.Join, "artist": artistJSON(c.Artist)})
	}
	return out
}

func relsJSON(rels []Rel) []obj {
	out := make([]obj, 0, len(rels))
	for _, r := range rels {
		attrs := r.Attributes
		if attrs == nil {
			attrs = []string{}
		}
		out = append(out, obj{"type": r.Type, "target-type": "artist", "direction": "backward",
			"attributes": attrs, "artist": artistJSON(r.Artist)})
	}
	return out
}

func genresJSON(genre string) []obj {
	if genre == "" {
		return []obj{}
	}
	return []obj{{"name": genre, "count": 3}}
}

func secondaryJSON(rg *ReleaseGroup) []string {
	if rg.Secondary == nil {
		return []string{}
	}
	return rg.Secondary
}

// releaseGroupJSON is a release group as a release lookup embeds it.
func releaseGroupJSON(rg *ReleaseGroup) obj {
	return obj{"id": rg.ID, "title": rg.Title, "primary-type": rg.Primary, "secondary-types": secondaryJSON(rg),
		"first-release-date": rg.FirstDate, "genres": genresJSON(rg.Genre), "artist-credit": creditJSON(rg.Credit), "disambiguation": ""}
}

func recordingJSON(rec *Recording) obj {
	rels := relsJSON(rec.Rels)
	if rec.Work != nil {
		rels = append(rels, obj{"type": "performance", "target-type": "work", "direction": "forward", "attributes": []string{},
			"work": obj{"id": rec.Work.ID, "title": rec.Work.Title, "relations": relsJSON(rec.Work.Rels)}})
	}
	isrcs := []string{}
	if rec.ISRC != "" {
		isrcs = append(isrcs, rec.ISRC)
	}
	return obj{"id": rec.ID, "title": rec.Title, "length": rec.LengthMS, "video": false, "disambiguation": "",
		"first-release-date": rec.FirstDate, "artist-credit": creditJSON(rec.Credit), "isrcs": isrcs, "relations": rels}
}

// ReleaseJSON is the release lookup with the includes the app asks for
// (recordings, labels, artists, genres, release-groups, isrcs and relationships).
func ReleaseJSON(rel *Release) obj {
	var media []obj
	for _, m := range rel.Media {
		var tracks []obj
		for _, t := range m.Tracks {
			tracks = append(tracks, obj{"id": t.ID, "position": t.Position, "number": itoa(t.Position), "title": t.Recording.Title,
				"length": t.Recording.LengthMS, "artist-credit": creditJSON(t.Recording.Credit), "recording": recordingJSON(t.Recording)})
		}
		media = append(media, obj{"position": m.Position, "format": m.Format, "track-count": len(m.Tracks), "track-offset": 0, "title": "", "tracks": tracks})
	}
	return obj{
		"id": rel.ID, "title": rel.Title, "status": "Official", "date": rel.Date, "country": rel.Country,
		"barcode": rel.Barcode, "disambiguation": rel.Disambiguation, "quality": "normal",
		"artist-credit": creditJSON(rel.Group.Credit), "release-group": releaseGroupJSON(rel.Group),
		"label-info":          []obj{{"catalog-number": rel.CatalogNumber, "label": obj{"id": rel.Label.ID, "name": rel.Label.Name}}},
		"media":               media,
		"relations":           relsJSON(rel.Rels),
		"text-representation": obj{"language": "eng", "script": "Latn"},
		"cover-art-archive":   obj{"artwork": true, "front": true, "back": false, "count": 1},
		"release-events":      []obj{{"date": rel.Date, "area": obj{"name": rel.Country, "iso-3166-1-codes": []string{rel.Country}}}},
	}
}

// ReleaseModel decodes ReleaseJSON into the app's model, marked as fetched with the
// relationship includes — what the app stores after a real lookup.
func ReleaseModel(rel *Release) (models.MusicBrainzReleaseResponse, error) {
	var out models.MusicBrainzReleaseResponse
	raw, err := json.Marshal(ReleaseJSON(rel))
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	out.RelationsIncluded = true
	return out, nil
}

// releaseSummaryJSON is a release as browse and search return it.
func releaseSummaryJSON(rel *Release) obj {
	var media []obj
	for _, m := range rel.Media {
		media = append(media, obj{"format": m.Format, "track-count": len(m.Tracks)})
	}
	return obj{"id": rel.ID, "score": 100, "title": rel.Title, "status": "Official", "date": rel.Date, "country": rel.Country,
		"disambiguation": rel.Disambiguation, "barcode": rel.Barcode, "artist-credit": creditJSON(rel.Group.Credit),
		"release-group": obj{"id": rel.Group.ID, "title": rel.Group.Title, "primary-type": rel.Group.Primary, "secondary-types": secondaryJSON(rel.Group)},
		"media":         media,
		"label-info":    []obj{{"catalog-number": rel.CatalogNumber, "label": obj{"id": rel.Label.ID, "name": rel.Label.Name}}},
	}
}

// groupSummaryJSON is a release group as an artist browse and a group lookup return it.
func groupSummaryJSON(rg *ReleaseGroup) obj {
	return obj{"id": rg.ID, "title": rg.Title, "primary-type": rg.Primary, "secondary-types": secondaryJSON(rg), "first-release-date": rg.FirstDate}
}

// artistLookupJSON is the artist lookup with genres and tags.
func artistLookupJSON(a *Artist) obj {
	out := artistJSON(a)
	out["gender"] = a.Gender
	out["area"] = obj{"name": a.Country}
	out["begin-area"] = obj{"name": a.Country}
	out["life-span"] = obj{"begin": a.Begin, "end": "", "ended": false}
	out["genres"] = genresJSON(a.Genre)
	out["tags"] = []obj{}
	return out
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// matches reports whether every word of a query appears in the text, which is as much
// of Lucene as the demo needs: field prefixes and quotes are stripped first.
func matches(query, text string) bool {
	text = strings.ToLower(text)
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if i := strings.Index(word, ":"); i >= 0 {
			word = word[i+1:]
		}
		word = strings.Trim(word, `"()`)
		if word == "" || word == "and" || word == "or" {
			continue
		}
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}
