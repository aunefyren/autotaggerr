package insights

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// listenFixture is a library of four recordings and one user's plays of them:
// r1 credits a and b, r2 a and b, r3 b and c, r4 d alone.
type listenFixture struct {
	db   *gorm.DB
	user uuid.UUID
	t0   time.Time
}

func newListenFixture(t *testing.T) listenFixture {
	t.Helper()
	modules.ResetMusicBrainzCachesForTest(t)
	db := testDB(t)
	resetListenCache()
	t.Cleanup(resetListenCache)
	lib := addLibrary(t, db, "music", true)
	seedCredits(t, "rel-1", nil,
		creditTrack("r1", credited("a", "b")),
		creditTrack("r2", credited("a", "b")),
		creditTrack("r3", credited("b", "c")),
		creditTrack("r4", credited("d")),
	)
	for _, r := range []string{"r1", "r2", "r3", "r4"} {
		addItem(t, db, lib, "/music/"+r+".flac", "rel-1", r)
	}
	if _, err := SyncCredits(db); err != nil {
		t.Fatal(err)
	}

	f := listenFixture{db: db, user: uuid.New(), t0: time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)}
	plex := models.ListeningAccount{UserID: f.user, Source: models.ListeningSourcePlex, Enabled: true}
	lb := models.ListeningAccount{UserID: f.user, Source: models.ListeningSourceListenBrainz, Enabled: true, Username: "u"}
	for _, a := range []*models.ListeningAccount{&plex, &lb} {
		if err := db.Create(a).Error; err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	play := func(acc models.ListeningAccount, at time.Time, recording string, artists ...models.ListenArtist) {
		n++
		l := models.Listen{UserID: f.user, AccountID: acc.ID, Source: acc.Source, ListenedAt: at,
			ExternalKey: uuid.NewString(), RecordingMBID: recording}
		if err := db.Create(&l).Error; err != nil {
			t.Fatal(err)
		}
		for _, a := range artists {
			a.ListenID = l.ID
			if err := db.Create(&a).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	m := time.Minute
	// Session one. r1 reaches both sources two minutes apart: one play scrobbled twice.
	play(plex, f.t0, "r1")
	play(lb, f.t0.Add(2*m), "r1")
	play(plex, f.t0.Add(5*m), "r3")
	play(plex, f.t0.Add(10*m), "r4")
	// Session two, three hours on: music not on disk (artist e, named by the source), then d.
	play(lb, f.t0.Add(3*time.Hour), "x", models.ListenArtist{ArtistMBID: "e", ArtistName: "E"})
	play(plex, f.t0.Add(3*time.Hour+3*m), "r4")
	// A play nobody could identify.
	play(plex, f.t0.Add(4*time.Hour), "")
	// Two months ago.
	play(plex, f.t0.AddDate(0, -2, 0), "r2")
	return f
}

func nodeOf(g GraphReport, id string) (GraphNode, bool) {
	for _, n := range g.Nodes {
		if n.MBID == id {
			return n, true
		}
	}
	return GraphNode{}, false
}

func edgeOf(g GraphReport, x, y, kind string) int {
	for _, e := range g.Edges {
		if e.Kind == kind && ((e.Source == x && e.Target == y) || (e.Source == y && e.Target == x)) {
			return e.Weight
		}
	}
	return 0
}

// TestListeningPlays: plays reach artists through the credits, a play reported by two
// sources minutes apart counts once, and the period narrows them.
func TestListeningPlays(t *testing.T) {
	f := newListenFixture(t)

	g, err := Graph(f.db, GraphOptions{Listener: &f.user})
	if err != nil {
		t.Fatal(err)
	}
	if g.Listening == nil {
		t.Fatal("no listening summary for a user with accounts")
	}
	// r1, r3, r4, x, r4, r2 identified; one not.
	if s := *g.Listening; s.Plays != 7 || s.Matched != 6 || s.Unmatched != 1 || s.Accounts != 2 {
		t.Errorf("summary = %+v", s)
	}
	for id, want := range map[string]int{"a": 2, "b": 3, "c": 1} {
		if n, ok := nodeOf(g, id); !ok || n.Plays != want {
			t.Errorf("%s plays = %d, want %d", id, n.Plays, want)
		}
	}
	// Tracks is still recordings, whatever the plays.
	if n, _ := nodeOf(g, "b"); n.Tracks != 3 {
		t.Errorf("b tracks = %d, want 3 (recordings, not plays)", n.Tracks)
	}

	g, _ = Graph(f.db, GraphOptions{Listener: &f.user, Since: f.t0.Add(-24 * time.Hour)})
	if n, _ := nodeOf(g, "a"); n.Plays != 1 {
		t.Errorf("a plays in the period = %d, want 1 (the old play left out)", n.Plays)
	}
	if g.Listening.Since == nil || g.Listening.Plays != 6 {
		t.Errorf("period summary = %+v", *g.Listening)
	}

	// No listener, or one with no account: no overlay at all.
	g, _ = Graph(f.db, GraphOptions{})
	if g.Listening != nil {
		t.Error("overlay without a listener")
	}
	if n, _ := nodeOf(g, "b"); n.Plays != 0 {
		t.Errorf("plays without a listener = %d", n.Plays)
	}
	stranger := uuid.New()
	if g, _ = Graph(f.db, GraphOptions{Listener: &stranger}); g.Listening != nil {
		t.Error("overlay for a user with no account")
	}
}

// TestListenedOnly: an edge survives only between artists with enough plays; the
// centred artist is exempt.
func TestListenedOnly(t *testing.T) {
	f := newListenFixture(t)

	g, _ := Graph(f.db, GraphOptions{Listener: &f.user, ListenedOnly: true, MinPlays: 2})
	if edgeOf(g, "a", "b", "") != 2 || edgeOf(g, "b", "c", "") != 0 {
		t.Errorf("listened-only edges = %+v", g.Edges)
	}
	// Centred on c, who has one play: still drawn with b.
	g, _ = Graph(f.db, GraphOptions{Listener: &f.user, ListenedOnly: true, MinPlays: 2, Artist: "c"})
	if edgeOf(g, "b", "c", "") != 1 {
		t.Errorf("centred artist not exempt: %+v", g.Edges)
	}
	// Without the switch the floor does nothing.
	g, _ = Graph(f.db, GraphOptions{Listener: &f.user, MinPlays: 2})
	if edgeOf(g, "b", "c", "") != 1 {
		t.Errorf("floor applied without listened-only: %+v", g.Edges)
	}
}

// TestCoListening: sessions split on a long gap, each pair counts once per session,
// and artists not on disk are reached and named through the source's artists.
func TestCoListening(t *testing.T) {
	f := newListenFixture(t)

	g, err := Graph(f.db, GraphOptions{Listener: &f.user, Edges: EdgesListening})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]string{{"a", "b"}, {"a", "c"}, {"b", "c"}, {"a", "d"}, {"b", "d"}, {"c", "d"}, {"d", "e"}} {
		if w := edgeOf(g, p[0], p[1], EdgeKindListening); w != 1 {
			t.Errorf("co-listen %s×%s = %d, want 1", p[0], p[1], w)
		}
	}
	// e and a were in different sessions.
	if edgeOf(g, "a", "e", EdgeKindListening) != 0 {
		t.Error("a pair across sessions")
	}
	if e, ok := nodeOf(g, "e"); !ok || e.Name != "E" || e.Plays != 1 {
		t.Errorf("artist not on disk = %+v, %v", e, ok)
	}

	// Both: credits lay out; co-listening is added between the artists they chose.
	g, _ = Graph(f.db, GraphOptions{Listener: &f.user, Edges: EdgesBoth})
	if edgeOf(g, "a", "b", "") != 2 || edgeOf(g, "a", "b", EdgeKindListening) != 1 {
		t.Errorf("both = %+v", g.Edges)
	}
	if _, ok := nodeOf(g, "e"); ok {
		t.Error("both drew an artist only co-listening reaches")
	}
}

// TestKeptPlaysSameSource: the same recording twice from one source is two plays.
func TestKeptPlaysSameSource(t *testing.T) {
	db := testDB(t)
	user, acc := uuid.New(), uuid.New()
	at := time.Now().UTC()
	for i := 0; i < 2; i++ {
		db.Create(&models.Listen{UserID: user, AccountID: acc, Source: "plex", ListenedAt: at.Add(time.Duration(i) * time.Minute),
			ExternalKey: uuid.NewString(), RecordingMBID: "r"})
	}
	plays, summary, err := keptPlays(db, user, time.Time{})
	if err != nil || len(plays) != 2 || summary.Matched != 2 {
		t.Errorf("kept = %d, %+v, %v", len(plays), summary, err)
	}
}

// TestBackbone: a pair is kept when it is among the k strongest of either artist.
func TestBackbone(t *testing.T) {
	w := map[pairKey]int{
		orderedPair("hub", "a"): 9, orderedPair("hub", "b"): 8, orderedPair("hub", "c"): 7,
		orderedPair("hub", "d"): 1, // hub's weakest, but d's only partner: kept
		orderedPair("a", "b"):   1, // neither a's nor b's top 1: dropped
	}
	got := backbone(w, 1)
	if got[orderedPair("hub", "a")] != 9 || got[orderedPair("hub", "d")] != 1 || got[orderedPair("hub", "b")] != 8 {
		t.Errorf("kept = %v", got)
	}
	if _, ok := got[orderedPair("a", "b")]; ok {
		t.Errorf("a weak pair kept: %v", got)
	}
}

// TestListeningOff: the library alone, though the user has plays: no plays on any
// node, credit edges whatever Edges says, and a summary that says only "off".
func TestListeningOff(t *testing.T) {
	f := newListenFixture(t)
	g, err := Graph(f.db, GraphOptions{Listener: &f.user, ListeningOff: true, Edges: EdgesListening, ListenedOnly: true, MinPlays: 100})
	if err != nil {
		t.Fatal(err)
	}
	if g.Listening == nil || !g.Listening.Off || g.Listening.Plays != 0 || g.Listening.Accounts != 2 {
		t.Errorf("summary = %+v", g.Listening)
	}
	for _, n := range g.Nodes {
		if n.Plays != 0 {
			t.Errorf("%s has %d plays with the overlay off", n.MBID, n.Plays)
		}
	}
	if edgeOf(g, "a", "b", "") != 2 || edgeOf(g, "b", "c", "") != 1 {
		t.Errorf("edges with the overlay off = %+v", g.Edges)
	}
	stranger := uuid.New()
	if g, _ := Graph(f.db, GraphOptions{Listener: &stranger, ListeningOff: true}); g.Listening != nil {
		t.Error("off summary for a user with no account")
	}
}
