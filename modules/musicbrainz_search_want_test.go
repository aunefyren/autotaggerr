package modules

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The album and song searches behind Add → Album and Add → Track. Beyond parsing,
// the song search must carry each release's group: that is what a song want is
// stored under.

func TestSearchMusicBrainzReleaseGroups(t *testing.T) {
	resetBrowseCaches(t)
	var gotPath, gotQuery string
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":1,"release-groups":[
			{"id":"rg1","score":100,"title":"OK Computer","primary-type":"Album","secondary-types":[],
			 "first-release-date":"1997-05-21","count":40,
			 "artist-credit":[{"name":"Radiohead","joinphrase":"","artist":{"id":"a1","name":"Radiohead"}}]}
		]}`)
	})

	hits, err := SearchMusicBrainzReleaseGroups(" ok computer ")
	if err != nil {
		t.Fatalf("SearchMusicBrainzReleaseGroups: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/release-group") || gotQuery != "ok computer" {
		t.Errorf("request = %s ?query=%q", gotPath, gotQuery)
	}
	if len(hits) != 1 || hits[0].Title != "OK Computer" || hits[0].ArtistCredit[0].Artist.ID != "a1" || hits[0].Count != 40 {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestSearchMusicBrainzRecordings(t *testing.T) {
	resetBrowseCaches(t)
	var gotPath string
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":1,"recordings":[
			{"id":"rec1","score":100,"title":"Karma Police","length":264066,
			 "artist-credit":[{"name":"Radiohead","joinphrase":"","artist":{"id":"a1","name":"Radiohead"}}],
			 "releases":[{"id":"rel1","title":"OK Computer","status":"Official","date":"1997-05-21","country":"GB","track-count":12,
			   "release-group":{"id":"rg1","title":"OK Computer","primary-type":"Album","secondary-types":[]},
			   "media":[{"position":1,"format":"CD","track":[{"number":"6"}],"track-count":12}]}]}
		]}`)
	})

	hits, err := SearchMusicBrainzRecordings("karma police")
	if err != nil {
		t.Fatalf("SearchMusicBrainzRecordings: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/recording") {
		t.Errorf("path = %s", gotPath)
	}
	if len(hits) != 1 || hits[0].Length != 264066 || len(hits[0].Releases) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	rel := hits[0].Releases[0]
	if rel.ReleaseGroup.ID != "rg1" || rel.TrackCount != 12 || rel.Media[0].Track[0].Number != "6" {
		t.Errorf("release = %+v", rel)
	}

	if none, err := SearchMusicBrainzRecordings("  "); err != nil || none != nil {
		t.Errorf("blank query = (%v, %v), want (nil, nil)", none, err)
	}
}
