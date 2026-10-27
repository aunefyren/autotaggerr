package modules

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakePlex is a Plex server with music sections, albums found by title, and the files
// each album's tracks are. Plex mounts the library at /data/music; Autotaggerr sees it
// at /music, so only the path below the root agrees — which is what matching uses.
type fakePlex struct {
	// sections: key -> title. albums: section -> album key -> title. files: album key -> paths.
	sections map[string]string
	albums   map[string]map[string]string
	files    map[string][]string
	requests atomic.Int64
}

func (f *fakePlex) client(t *testing.T) *PlexClient {
	t.Helper()
	return newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		var b strings.Builder
		b.WriteString("<MediaContainer>")
		switch {
		case r.URL.Path == "/library/sections":
			for key, title := range f.sections {
				fmt.Fprintf(&b, `<Directory key=%q title=%q type="artist"><Location path="/data/music"/></Directory>`, key, title)
			}
			b.WriteString(`<Directory key="99" title="Films" type="movie"/>`)
		case strings.HasPrefix(r.URL.Path, "/library/sections/"):
			section := strings.Split(strings.TrimPrefix(r.URL.Path, "/library/sections/"), "/")[0]
			if r.URL.Query().Get("type") == "9" {
				for key, title := range f.albums[section] {
					fmt.Fprintf(&b, `<Directory key="%s/children" title=%q parentTitle="Artist" type="album"/>`, key, title)
				}
			}
		case strings.HasSuffix(r.URL.Path, "/children"):
			for _, file := range f.files[strings.TrimSuffix(r.URL.Path, "/children")] {
				fmt.Fprintf(&b, `<Track title="t"><Media><Part file=%q/></Media></Track>`, file)
			}
		}
		b.WriteString("</MediaContainer>")
		_, _ = w.Write([]byte(b.String()))
	}))
}

const ourFile = "/music/Artist/Album (2020)/01 Song.flac"

// TestResolveAlbumForFileTakesTheAlbumHoldingTheFile: two sections each hold an
// album titled "Album". Matching by title refreshed whichever came first; only the
// one listing this file is right.
func TestResolveAlbumForFileTakesTheAlbumHoldingTheFile(t *testing.T) {
	f := &fakePlex{
		sections: map[string]string{"1": "Music", "2": "Sandbox"},
		albums: map[string]map[string]string{
			"1": {"/library/metadata/10": "Album"},
			"2": {"/library/metadata/20": "Album"},
		},
		files: map[string][]string{
			"/library/metadata/10": {"/data/other/Artist/Album (1999)/01 Song.flac"},
			"/library/metadata/20": {"/data/music/Artist/Album (2020)/01 Song.flac"},
		},
	}
	key, err := f.client(t).ResolveAlbumForFile(ourFile, "/music", "Artist", "Album", "Song")
	if err != nil || key != "/library/metadata/20" {
		t.Fatalf("resolved (%q, %v), want the album holding the file", key, err)
	}
}

// TestResolveAlbumForFileRefusesATitleOnlyMatch: an album of the same title whose
// files are not this one is not this album — the refresh used to go there anyway.
func TestResolveAlbumForFileRefusesATitleOnlyMatch(t *testing.T) {
	f := &fakePlex{
		sections: map[string]string{"1": "Music"},
		albums:   map[string]map[string]string{"1": {"/library/metadata/10": "Album"}},
		files:    map[string][]string{"/library/metadata/10": {"/data/music/Someone Else/Album/01 Song.flac"}},
	}
	if key, err := f.client(t).ResolveAlbumForFile(ourFile, "/music", "Artist", "Album", "Song"); err != ErrPlexAlbumNotFound {
		t.Fatalf("resolved (%q, %v), want ErrPlexAlbumNotFound", key, err)
	}
}

// TestForLibraryNarrowsOrSkips: a library names its section, or opts out.
func TestForLibraryNarrowsOrSkips(t *testing.T) {
	f := &fakePlex{
		sections: map[string]string{"1": "Music", "2": "Sandbox"},
		albums: map[string]map[string]string{
			"1": {"/library/metadata/10": "Album"},
			"2": {"/library/metadata/20": "Album"},
		},
		files: map[string][]string{
			"/library/metadata/10": {"/data/music/Artist/Album (2020)/01 Song.flac"},
			"/library/metadata/20": {"/data/music/Artist/Album (2020)/01 Song.flac"},
		},
	}
	client := f.client(t)
	scoped := client.InSection(" 2 ")
	key, err := scoped.ResolveAlbumForFile(ourFile, "/music", "Artist", "Album", "Song")
	if err != nil || key != "/library/metadata/20" {
		t.Errorf("resolved (%q, %v) in section 2, want its album", key, err)
	}
	if client.SectionKey != "" {
		t.Error("ForLibrary changed the shared client")
	}
}

// TestPlexRefreshForFileCachesPerFolder: the answer is kept per album folder, the
// negative one too, so the second track of an album Plex lacks costs no request.
func TestPlexRefreshForFileCachesPerFolder(t *testing.T) {
	resetPlexCache()
	t.Cleanup(resetPlexCache)
	f := &fakePlex{
		sections: map[string]string{"1": "Music"},
		albums:   map[string]map[string]string{"1": {"/library/metadata/10": "Album"}},
		files:    map[string][]string{"/library/metadata/10": {"/data/music/Artist/Album (2020)/01 Song.flac"}},
	}
	client := f.client(t)

	set := NewAlbumRefreshSet()
	if err := PlexRefreshForFile(set, client, ourFile, "/music", "Album", "Artist", "Song"); err != nil {
		t.Fatal(err)
	}
	if got := set.Snapshot(); len(got) != 1 || got[0].Name != "Album" || got[0].Key != "/library/metadata/10" {
		t.Fatalf("queued %+v, want the album holding the file", got)
	}
	before := f.requests.Load()
	second := filepath.Join(filepath.Dir(ourFile), "02 Other.flac")
	if err := PlexRefreshForFile(set, client, second, "/music", "Album", "Artist", "Other"); err != nil {
		t.Fatal(err)
	}
	if f.requests.Load() != before {
		t.Error("the second track of a resolved album asked Plex again")
	}

	missing := "/music/Artist/Not In Plex/01 Song.flac"
	empty := NewAlbumRefreshSet()
	if err := PlexRefreshForFile(empty, client, missing, "/music", "Missing", "Artist", "Song"); err != nil {
		t.Fatalf("an album Plex lacks is not an error: %v", err)
	}
	before = f.requests.Load()
	if err := PlexRefreshForFile(empty, client, filepath.Join(filepath.Dir(missing), "02.flac"), "/music", "Missing", "Artist", "Song"); err != nil {
		t.Fatal(err)
	}
	if len(empty.Snapshot()) != 0 {
		t.Error("an album Plex does not hold was queued for refresh")
	}
	if f.requests.Load() != before {
		t.Error("an album Plex lacks was searched for again on its next track")
	}

	if err := PlexRefreshForFile(set, nil, ourFile, "/music", "Album", "Artist", "Song"); err != nil {
		t.Errorf("a nil client is a no-op, got %v", err)
	}
}

func TestPlexRefreshForFileReportsLookupFailures(t *testing.T) {
	resetPlexCache()
	t.Cleanup(resetPlexCache)
	down := newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	set := NewAlbumRefreshSet()
	err := PlexRefreshForFile(set, down, ourFile, "/music", "Album", "Artist", "Song")
	if err == nil || !strings.Contains(err.Error(), "resolve Plex album") {
		t.Errorf("err = %v, want the lookup named", err)
	}
	if len(set.Snapshot()) != 0 {
		t.Error("an unresolved album was queued")
	}
}

func TestMusicSectionsListsOnlyMusic(t *testing.T) {
	f := &fakePlex{sections: map[string]string{"1": "Music"}}
	sections, err := f.client(t).MusicSections()
	if err != nil || len(sections) != 1 || sections[0].Key != "1" || len(sections[0].Locations) != 1 {
		t.Errorf("sections = %+v, %v", sections, err)
	}
	none := newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<MediaContainer><Directory key="3" type="movie"/></MediaContainer>`))
	}))
	if _, err := none.MusicSections(); err == nil {
		t.Error("a server with no music section listed one")
	}
}

func TestPlexPathEndsWith(t *testing.T) {
	cases := []struct {
		plex, rel string
		want      bool
	}{
		{"/data/music/A/B/01.flac", "A/B/01.flac", true},
		{`D:\Music\A\B\01.flac`, "A/B/01.flac", true},
		{"/data/music/A/B/01.FLAC", "a/b/01.flac", true},
		{"/data/music/XA/B/01.flac", "A/B/01.flac", false}, // whole segments only
		{"/data/music/A/B/01.flac", "", false},
	}
	for _, c := range cases {
		if got := plexPathEndsWith(c.plex, c.rel); got != c.want {
			t.Errorf("plexPathEndsWith(%q, %q) = %v, want %v", c.plex, c.rel, got, c.want)
		}
	}
}
