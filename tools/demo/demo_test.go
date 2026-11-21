package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/insights"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

func tiny(t *testing.T) Preset {
	t.Helper()
	p, ok := presetByName("tiny")
	if !ok {
		t.Fatal("no tiny preset")
	}
	return p
}

// TestUniverseDeterministic: the same seed and size give the same IDs and names (the
// stand-in server relies on it), and another seed gives another universe.
func TestUniverseDeterministic(t *testing.T) {
	a, b := GenerateUniverse(7, tiny(t)), GenerateUniverse(7, tiny(t))
	if len(a.Releases) == 0 || len(a.Releases) != len(b.Releases) || a.Releases[3].ID != b.Releases[3].ID || a.Artists[5].Name != b.Artists[5].Name {
		t.Fatal("the same seed gave a different universe")
	}
	if c := GenerateUniverse(8, tiny(t)); c.Releases[3].ID == a.Releases[3].ID {
		t.Error("another seed gave the same IDs")
	}
	for _, rel := range a.Releases {
		if rel.Tracks() == 0 || rel.Group == nil || len(rel.Group.Credit) == 0 {
			t.Fatalf("incomplete release %+v", rel)
		}
	}
}

// TestStub: every endpoint the app calls answers in the shape the app decodes, and an
// unknown ID is a 404.
func TestStub(t *testing.T) {
	u := GenerateUniverse(1, tiny(t))
	stub := NewStub(u)
	stub.Quiet = true
	server := httptest.NewServer(stub)
	defer server.Close()
	rel := u.Holdings[0].Release
	artist := rel.Group.Credit[0].Artist

	get := func(path string, out any) int {
		t.Helper()
		res, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil && res.StatusCode == http.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(out); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
		}
		return res.StatusCode
	}

	var release models.MusicBrainzReleaseResponse
	if get("/ws/2/release/"+rel.ID+"?inc=recordings&fmt=json", &release) != 200 || release.ID != rel.ID || len(release.Media) == 0 || release.ReleaseGroup.ID != rel.Group.ID {
		t.Errorf("release lookup = %+v", release.ID)
	}
	if credits := insights.ReleaseCredits(release); len(credits) == 0 {
		t.Error("the served release has no credits for the graph")
	}
	var lookup models.MusicBrainzArtistLookup
	if get("/ws/2/artist/"+artist.ID+"?fmt=json", &lookup) != 200 || lookup.Name != artist.Name {
		t.Errorf("artist lookup = %+v", lookup)
	}
	var groups models.MusicBrainzArtistReleaseGroups
	if get("/ws/2/release-group?artist="+artist.ID+"&limit=100&offset=0&fmt=json", &groups) != 200 || groups.Count == 0 || len(groups.ReleaseGroups) == 0 {
		t.Errorf("discography browse = %+v", groups)
	}
	var editions models.MusicBrainzReleaseBrowseResponse
	if get("/ws/2/release?release-group="+rel.Group.ID+"&inc=media+labels&limit=100&fmt=json", &editions) != 200 || editions.Count != len(rel.Group.Releases) {
		t.Errorf("edition browse = %+v", editions)
	}
	var found models.MusicBrainzReleaseSearchResponse
	if get("/ws/2/release?query=release:"+urlWord(rel.Title)+"&limit=25&offset=0&fmt=json", &found) != 200 || found.Count == 0 {
		t.Errorf("release search found nothing for %q", rel.Title)
	}
	var artists models.MusicBrainzArtistSearchResponse
	if get("/ws/2/artist?query="+urlWord(artist.Name)+"&limit=25&fmt=json", &artists) != 200 || len(artists.Artists) == 0 {
		t.Errorf("artist search found nothing for %q", artist.Name)
	}
	if code := get("/ws/2/release-group/"+rel.Group.ID+"?fmt=json", nil); code != 200 {
		t.Errorf("release-group lookup: %d", code)
	}
	if code := get("/ws/2/release/00000000-0000-0000-0000-000000000000?fmt=json", nil); code != 404 {
		t.Errorf("unknown release: %d, want 404", code)
	}
	res, err := http.Get(server.URL + "/caa/release-group/" + rel.Group.ID + "/front-500")
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
		t.Errorf("cover: %v %v", err, res)
	}
}

func urlWord(s string) string {
	for i, r := range s {
		if r == ' ' {
			return s[:i]
		}
	}
	return s
}

// TestGenerate: a tiny instance end to end — files, the marker, a database the app can
// open with admin/admin and the stand-in configured, and a pre-filled index on which
// Insights finds every duplicate level and a graph.
func TestGenerate(t *testing.T) {
	modules.ResetMusicBrainzCachesForTest(t)
	dir := filepath.Join(t.TempDir(), "demo")
	p, _ := presetByName("small")
	u, err := Generate(GenerateOptions{Dir: dir, Seed: 1, Preset: p})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readMarker(dir); err != nil {
		t.Fatal(err)
	}
	if err := checkTarget(dir); err != nil {
		t.Errorf("a demo directory is refused for regeneration: %v", err)
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "keep.txt"), []byte("x"), 0o644)
	if _, err := Generate(GenerateOptions{Dir: other, Seed: 1, Preset: tiny(t)}); err == nil {
		t.Error("generated into a non-empty directory that is not a demo")
	}

	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(dir, "config", "autotaggerr.db")})
	if err != nil {
		t.Fatal(err)
	}
	var items int64
	db.Model(&models.LibraryItem{}).Count(&items)
	if int(items) != u.TrackFiles()+u.Unmatched {
		t.Errorf("%d items indexed, want %d", items, u.TrackFiles()+u.Unmatched)
	}
	var source models.DataSource
	db.Where("type = ?", models.DataSourceTypeMusicBrainz).First(&source)
	if source.BaseURL != "http://"+stubAddr+"/ws/2" {
		t.Errorf("MusicBrainz points at %q", source.BaseURL)
	}

	// Check the audio, as the button would, then every level is present.
	candidates, err := insights.UncheckedCandidates(db)
	if err != nil {
		t.Fatal(err)
	}
	insights.VerifyDuplicates(context.Background(), db, candidates, nil, nil)
	report, err := insights.Duplicates(db, insights.DuplicateOptions{IncludeAcrossReleases: true, IncludeCompilations: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range insights.Levels {
		if report.Counts[level] == 0 {
			t.Errorf("no %s duplicates in the small demo: %v", level, report.Counts)
		}
	}
	graph, err := insights.Graph(db, insights.GraphOptions{})
	if err != nil || len(graph.Edges) == 0 {
		t.Errorf("graph: %d edges, %v", len(graph.Edges), err)
	}
}
