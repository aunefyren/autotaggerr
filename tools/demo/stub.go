package main

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

// The stand-in MusicBrainz and Cover Art Archive. It answers every request the app
// makes (modules/musicbrainz*.go, modules/artwork.go) from the universe in memory, so
// a demo instance never reaches the network.
//
// Unknown IDs answer 404 as MusicBrainz does, and the app records a 404 as a deletion
// — which is correct here too: every ID the demo files carry is in the universe.

// Stub serves one universe.
type Stub struct {
	u        *Universe
	Requests atomic.Int64
	// Quiet stops the per-request log line (tests).
	Quiet bool
}

func NewStub(u *Universe) *Stub { return &Stub{u: u} }

func (s *Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Requests.Add(1)
	if !s.Quiet {
		log.Printf("stub %s %s", r.Method, r.URL.RequestURI())
	}
	path := strings.Trim(r.URL.Path, "/")
	switch {
	case strings.HasPrefix(path, "ws/2/"):
		s.musicbrainz(w, r, strings.TrimPrefix(path, "ws/2/"))
	case strings.HasPrefix(path, "caa/"):
		s.coverArt(w, strings.TrimPrefix(path, "caa/"))
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func notFound(w http.ResponseWriter) { writeJSON(w, http.StatusNotFound, obj{"error": "Not Found"}) }

func intParam(r *http.Request, key string, fallback int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil && n >= 0 {
		return n
	}
	return fallback
}

func (s *Stub) musicbrainz(w http.ResponseWriter, r *http.Request, path string) {
	q := r.URL.Query()
	parts := strings.Split(path, "/")
	entity := parts[0]
	if len(parts) == 2 {
		id := parts[1]
		switch entity {
		case "release":
			if rel, ok := s.u.releaseByID[id]; ok {
				writeJSON(w, http.StatusOK, ReleaseJSON(rel))
				return
			}
		case "artist":
			if a, ok := s.u.artistByID[id]; ok {
				writeJSON(w, http.StatusOK, artistLookupJSON(a))
				return
			}
		case "release-group":
			if rg, ok := s.u.groupByID[id]; ok {
				writeJSON(w, http.StatusOK, groupSummaryJSON(rg))
				return
			}
		}
		notFound(w)
		return
	}

	limit, offset := intParam(r, "limit", 25), intParam(r, "offset", 0)
	switch {
	case entity == "release-group" && q.Get("artist") != "":
		groups := s.u.groupsByArtist[q.Get("artist")]
		if _, ok := s.u.artistByID[q.Get("artist")]; !ok {
			notFound(w)
			return
		}
		page := []obj{}
		for i := offset; i < len(groups) && i < offset+limit; i++ {
			page = append(page, groupSummaryJSON(groups[i]))
		}
		writeJSON(w, http.StatusOK, obj{"release-group-count": len(groups), "release-group-offset": offset, "release-groups": page})
	case entity == "release" && q.Get("release-group") != "":
		rg, ok := s.u.groupByID[q.Get("release-group")]
		if !ok {
			notFound(w)
			return
		}
		page := []obj{}
		for _, rel := range rg.Releases {
			page = append(page, releaseSummaryJSON(rel))
		}
		writeJSON(w, http.StatusOK, obj{"release-count": len(page), "releases": page})
	case entity == "release" && q.Get("query") != "":
		var hits []obj
		for _, rel := range s.u.Releases {
			if matches(q.Get("query"), rel.Title+" "+CreditName(rel.Group.Credit)+" "+rel.Barcode+" "+rel.CatalogNumber) {
				hits = append(hits, releaseSummaryJSON(rel))
			}
		}
		page := []obj{}
		for i := offset; i < len(hits) && i < offset+limit; i++ {
			page = append(page, hits[i])
		}
		writeJSON(w, http.StatusOK, obj{"count": len(hits), "offset": offset, "releases": page})
	case entity == "artist" && q.Get("query") != "":
		page := []obj{}
		for _, a := range s.u.sortedArtists() {
			if len(page) < limit && matches(q.Get("query"), a.Name) {
				hit := artistJSON(a)
				hit["score"] = 100
				page = append(page, hit)
			}
		}
		writeJSON(w, http.StatusOK, obj{"count": len(page), "artists": page})
	default:
		writeJSON(w, http.StatusBadRequest, obj{"error": "the demo stand-in does not serve " + r.URL.RequestURI()})
	}
}

// coverArt answers <entity>/<mbid>/front-<size> with a generated cover: two colour
// bands derived from the MBID, so each album is recognisable and stable.
func (s *Stub) coverArt(w http.ResponseWriter, path string) {
	parts := strings.Split(path, "/")
	if len(parts) != 3 || !strings.HasPrefix(parts[2], "front") {
		http.NotFound(w, nil)
		return
	}
	known := false
	switch parts[0] {
	case "release-group":
		_, known = s.u.groupByID[parts[1]]
	case "release":
		_, known = s.u.releaseByID[parts[1]]
	}
	if !known {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(cover(parts[1]))
}

func cover(id string) []byte {
	h := fnv.New64a()
	h.Write([]byte(id))
	sum := h.Sum64()
	a := color.RGBA{uint8(sum), uint8(sum >> 8), uint8(sum >> 16), 255}
	b := color.RGBA{uint8(sum >> 24), uint8(sum >> 32), uint8(sum >> 40), 255}
	const size = 250
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	split := 60 + int(sum>>48)%130
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if x+y/2 < split {
				img.Set(x, y, a)
			} else {
				img.Set(x, y, b)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
