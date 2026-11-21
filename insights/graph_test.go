package insights

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

const variousArtists = "89ad4ac3-39f7-470e-963a-56509c546377"

func artist(id string) models.Artist { return models.Artist{ID: id, Name: "Name " + id} }

func credited(ids ...string) []models.ArtistCredit {
	var out []models.ArtistCredit
	for _, id := range ids {
		out = append(out, models.ArtistCredit{Artist: artist(id), Name: "as " + id})
	}
	return out
}

func rel(relType, id string) models.MusicBrainzRelation {
	a := artist(id)
	return models.MusicBrainzRelation{Type: relType, TargetType: "artist", Artist: &a}
}

// creditTrack is a track credited to artists, with relationships on its recording.
func creditTrack(recording string, credit []models.ArtistCredit, relations ...models.MusicBrainzRelation) models.Track {
	var tr models.Track
	tr.ID = "track-" + recording
	tr.Recording.ID = recording
	tr.ArtistCredit = credit
	tr.Recording.Relations = relations
	return tr
}

func seedCredits(t *testing.T, id string, releaseRels []models.MusicBrainzRelation, tracks ...models.Track) models.MusicBrainzReleaseResponse {
	t.Helper()
	var release models.MusicBrainzReleaseResponse
	release.ID = id
	release.Relations = releaseRels
	release.Media = []models.MusicBrainzMedia{{Tracks: tracks}}
	modules.SeedMusicBrainzReleaseForTest(t, release)
	return release
}

// TestReleaseCredits: the track credit, release-level, recording-level and work-level
// relationships each become a role; a credited-as name is not a second artist; video
// tracks are left out.
func TestReleaseCredits(t *testing.T) {
	var release models.MusicBrainzReleaseResponse
	release.ID = "rel"
	release.Relations = []models.MusicBrainzRelation{rel("producer", "prod")}
	song := creditTrack("rec", credited("a", "b"), rel("instrument", "bass"), rel("instrument", "bass"))
	work := models.MusicBrainzWork{ID: "w", Relations: []models.MusicBrainzRelation{rel("composer", "comp"), rel("unknown-type", "x")}}
	song.Recording.Relations = append(song.Recording.Relations, models.MusicBrainzRelation{Type: "performance", TargetType: "work", Work: &work})
	video := creditTrack("vid", credited("v"))
	video.Recording.Video = true
	release.Media = []models.MusicBrainzMedia{{Tracks: []models.Track{song, video}}}

	got := map[string]string{}
	for _, c := range ReleaseCredits(release) {
		if c.RecordingMBID != "rec" || c.ReleaseMBID != "rel" {
			t.Errorf("credit on %s/%s", c.ReleaseMBID, c.RecordingMBID)
		}
		got[c.ArtistMBID] += c.Role
		if c.ArtistMBID == "a" && c.ArtistName != "Name a" {
			t.Errorf("credited-as name %q used for the node", c.ArtistName)
		}
	}
	want := map[string]string{"a": "credit", "b": "credit", "prod": "production", "bass": "performer", "comp": "writing"}
	if len(got) != len(want) {
		t.Errorf("credits = %v, want %v", got, want)
	}
	for id, role := range want {
		if got[id] != role {
			t.Errorf("%s = %q, want %q (one row per role)", id, got[id], role)
		}
	}
}

// TestSyncCredits: credits are read for releases on disk, not read again while the
// cache entry is the same, read again when it is replaced, and dropped with the
// release's last file.
func TestSyncCredits(t *testing.T) {
	modules.ResetMusicBrainzCachesForTest(t)
	db := testDB(t)
	lib := addLibrary(t, db, "music", true)
	seedCredits(t, "rel-1", nil, creditTrack("r1", credited("a", "b")))
	item := addItem(t, db, lib, "/music/1.flac", "rel-1", "r1")
	addItem(t, db, lib, "/music/2.flac", "rel-uncached", "r2")

	res, err := SyncCredits(db)
	if err != nil {
		t.Fatal(err)
	}
	if res.Read != 1 || res.Uncached != 1 {
		t.Errorf("first pass = %+v, want 1 read, 1 uncached", res)
	}
	var n int64
	db.Model(&models.InsightCredit{}).Count(&n)
	if n != 2 {
		t.Errorf("%d credits stored, want 2", n)
	}
	if res, _ = SyncCredits(db); res.Read != 0 {
		t.Errorf("an unchanged cache was read again: %+v", res)
	}

	// The release is re-fetched with a third artist.
	seedCredits(t, "rel-1", nil, creditTrack("r1", credited("a", "b", "c")))
	if res, _ = SyncCredits(db); res.Read != 1 {
		t.Errorf("a replaced cache entry was not read: %+v", res)
	}
	db.Model(&models.InsightCredit{}).Count(&n)
	if n != 3 {
		t.Errorf("%d credits after re-read, want 3", n)
	}

	db.Delete(&item)
	if res, _ = SyncCredits(db); res.Dropped != 1 {
		t.Errorf("a release with no file left was not dropped: %+v", res)
	}
	db.Model(&models.InsightCredit{}).Count(&n)
	if n != 0 {
		t.Errorf("%d credits left, want 0", n)
	}
}

// TestGraph: edges weigh shared recordings once each, roles choose which credits
// connect, Various Artists is out unless asked for, floors and the cap apply, and a
// centred graph reaches one or two hops.
func TestGraph(t *testing.T) {
	modules.ResetMusicBrainzCachesForTest(t)
	db := testDB(t)
	lib := addLibrary(t, db, "music", true)
	other := addLibrary(t, db, "other", true)
	db.Create(&models.CollectionArtist{MBID: "a", Name: "A"})

	// a+b on two recordings, b+c on one, c produced by p, d+VA on one.
	seedCredits(t, "rel-1", nil,
		creditTrack("r1", credited("a", "b")),
		creditTrack("r2", credited("a", "b")),
		creditTrack("r3", credited("b", "c"), rel("producer", "p")),
		creditTrack("r4", credited("d", variousArtists)),
	)
	// r1 again on a second release, in another library: still one shared recording.
	seedCredits(t, "rel-2", nil, creditTrack("r1", credited("a", "b")))
	for _, r := range []string{"r1", "r2", "r3", "r4"} {
		addItem(t, db, lib, "/music/"+r+".flac", "rel-1", r)
	}
	addItem(t, db, other, "/other/r1.flac", "rel-2", "r1")
	if _, err := SyncCredits(db); err != nil {
		t.Fatal(err)
	}

	weight := func(g GraphReport, x, y string) int {
		for _, e := range g.Edges {
			if (e.Source == x && e.Target == y) || (e.Source == y && e.Target == x) {
				return e.Weight
			}
		}
		return 0
	}

	g, err := Graph(db, GraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 3 || weight(g, "a", "b") != 2 || weight(g, "b", "c") != 1 || g.Connected != 3 {
		t.Fatalf("default graph = %+v", g)
	}
	// Nodes follow the strongest edge first: a and b share two recordings, b the
	// stronger of the two.
	if g.Nodes[0].MBID != "b" || g.Nodes[1].MBID != "a" || g.Nodes[0].Tracks != 3 {
		t.Errorf("first nodes = %+v, want b then a", g.Nodes[:2])
	}
	for _, n := range g.Nodes {
		if n.MBID == "a" && !n.InCollection {
			t.Error("a collection artist is not marked")
		}
	}
	if len(g.Collaborators) != 2 || g.Collaborators[0].Weight != 2 {
		t.Errorf("collaborators = %+v", g.Collaborators)
	}

	// Managed only: an edge needs both ends managed, unless it touches the centre.
	g, _ = Graph(db, GraphOptions{ManagedOnly: true, Managed: map[string]bool{"a": true}})
	if len(g.Nodes) != 0 || g.Connected != 0 {
		t.Errorf("one managed artist with no managed partner = %+v, want no graph", g)
	}
	g, _ = Graph(db, GraphOptions{ManagedOnly: true, Managed: map[string]bool{"a": true, "b": true}})
	if len(g.Nodes) != 2 || weight(g, "a", "b") != 2 || weight(g, "b", "c") != 0 {
		t.Errorf("managed a and b = %+v, want a-b only", g)
	}
	for _, n := range g.Nodes {
		if !n.Managed {
			t.Errorf("%s is not marked managed", n.MBID)
		}
	}
	g, _ = Graph(db, GraphOptions{ManagedOnly: true, Managed: map[string]bool{"a": true}, Artist: "b"})
	if len(g.Nodes) != 2 || weight(g, "a", "b") != 2 || g.Center == nil {
		t.Errorf("centred on unmanaged b = %+v, want b and its managed partner a", g)
	}

	g, _ = Graph(db, GraphOptions{Roles: []string{"credit", "production"}})
	if weight(g, "c", "p") != 1 {
		t.Error("a producer does not connect with production asked for")
	}
	g, _ = Graph(db, GraphOptions{IncludeSpecial: true})
	if weight(g, "d", variousArtists) != 1 {
		t.Error("Various Artists left out when asked for")
	}
	g, _ = Graph(db, GraphOptions{MinWeight: 2})
	if len(g.Nodes) != 2 {
		t.Errorf("min weight 2: %d nodes, want a and b", len(g.Nodes))
	}
	// The cap never splits an edge: limit 1 fits no whole edge, limit 2 the strongest.
	g, _ = Graph(db, GraphOptions{Limit: 1})
	if len(g.Nodes) != 0 || !g.Truncated {
		t.Errorf("limit 1 = %+v", g)
	}
	g, _ = Graph(db, GraphOptions{Limit: 2})
	if len(g.Nodes) != 2 || weight(g, "a", "b") != 2 || !g.Truncated {
		t.Errorf("limit 2 = %+v", g)
	}
	g, _ = Graph(db, GraphOptions{Library: &other.ID})
	if weight(g, "a", "b") != 1 || len(g.Nodes) != 2 {
		t.Errorf("one library's graph = %+v", g)
	}

	g, _ = Graph(db, GraphOptions{Artist: "a", Hops: 1})
	if len(g.Nodes) != 2 || g.Center == nil || g.Center.MBID != "a" || len(g.Collaborators) != 1 || g.Collaborators[0].B.MBID != "b" {
		t.Errorf("a, one hop = %+v", g)
	}
	g, _ = Graph(db, GraphOptions{Artist: "a", Hops: 2})
	if len(g.Nodes) != 3 || g.Nodes[2].MBID != "c" || g.Nodes[2].Hop != 2 {
		t.Errorf("a, two hops = %+v", g.Nodes)
	}
	g, _ = Graph(db, GraphOptions{Artist: "nobody"})
	if len(g.Nodes) != 0 || g.Center != nil {
		t.Errorf("an unknown centre = %+v", g)
	}
}

// TestGraphColours: a node carries its strongest genre and main role, the report names
// the library's top genres, and a release synced by an older derivation is read again
// so its genre arrives.
func TestGraphColours(t *testing.T) {
	modules.ResetMusicBrainzCachesForTest(t)
	db := testDB(t)
	lib := addLibrary(t, db, "music", true)
	release := seedCredits(t, "rel-1", nil,
		creditTrack("r1", credited("a", "b"), rel("producer", "p")),
		creditTrack("r2", credited("a"), rel("producer", "p")),
	)
	release.ReleaseGroup.Genres = append(release.ReleaseGroup.Genres, struct {
		Count          int    `json:"count"`
		ID             string `json:"id"`
		Name           string `json:"name"`
		Disambiguation string `json:"disambiguation"`
	}{Count: 5, Name: "jazz"})
	modules.SeedMusicBrainzReleaseForTest(t, release)
	addItem(t, db, lib, "/music/1.flac", "rel-1", "r1")
	addItem(t, db, lib, "/music/2.flac", "rel-1", "r2")

	// As if synced before genres were derived: version 1, no genre.
	db.Create(&models.InsightCreditRelease{ReleaseMBID: "rel-1", Version: 1})
	if res, _ := SyncCredits(db); res.Read != 1 {
		t.Fatalf("an older derivation was not read again: %+v", res)
	}

	g, err := Graph(db, GraphOptions{Roles: []string{"credit", "production"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Genres) != 1 || g.Genres[0] != "jazz" {
		t.Errorf("genres = %v, want [jazz]", g.Genres)
	}
	for _, n := range g.Nodes {
		want := "credit"
		if n.MBID == "p" {
			want = "production"
		}
		if n.Genre != "jazz" || n.Role != want {
			t.Errorf("%s: genre %q role %q, want jazz %s", n.MBID, n.Genre, n.Role, want)
		}
	}
}
