package insights

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GraphRoles is every role a graph can be built from, in display order.
var GraphRoles = []string{models.InsightRoleCredit, models.InsightRolePerformer, models.InsightRoleProduction, models.InsightRoleWriting}

// specialArtists are MusicBrainz's special-purpose artists. Various Artists is
// credited on every compilation track, so it would be the hub of any graph that let it
// in; the others are placeholders ([unknown], [traditional]) rather than people.
var specialArtists = map[string]bool{
	"89ad4ac3-39f7-470e-963a-56509c546377": true, // Various Artists
	"125ec42a-7229-4250-afc5-e057484327fe": true, // [unknown]
	"f731ccc4-e22a-43af-a747-64213329e088": true, // [anonymous]
	"33cf029c-63b0-41a0-9855-be2a3665fb3b": true, // [data]
	"314e1c25-dde7-4e4d-b2f4-0a7b9f7c56dc": true, // [dialogue]
	"eec63d3c-3b81-4ad4-b1e4-7c147d4d2b61": true, // [no artist]
	"9be7f096-97ec-4615-8957-8d40b5dcbc41": true, // [traditional]
}

// Graph defaults. The node cap is what keeps an unfiltered graph readable: a library
// of thousands of artists drawn at once is a hairball.
const (
	DefaultGraphNodes = 150
	MaxGraphNodes     = 500
	topCollaborators  = 25
)

// GraphOptions narrows a collaboration graph. Adding a filter means adding a field
// here and the matching query parameter in routers/insights.go.
type GraphOptions struct {
	// Library limits the graph to one library's files. Nil means every enabled one.
	Library *uuid.UUID
	// Roles are which credits connect artists. Empty means credited artists only.
	Roles []string
	// MinWeight drops edges with fewer shared recordings.
	MinWeight int
	// MinTracks drops artists credited on fewer recordings in scope.
	MinTracks int
	// IncludeSpecial lets Various Artists and the placeholder artists in.
	IncludeSpecial bool
	// Artist centres the graph on one artist: their collaborators, and with Hops 2,
	// their collaborators' collaborators.
	Artist string
	Hops   int
	// Limit caps the nodes drawn (DefaultGraphNodes when zero).
	Limit int
}

// GraphNode is one artist.
type GraphNode struct {
	MBID string `json:"mb_id"`
	Name string `json:"name"`
	// Tracks is how many recordings in scope the artist is credited on, in the roles
	// asked for.
	Tracks int `json:"tracks"`
	// InCollection marks an artist with a collection page to link to.
	InCollection bool `json:"in_collection"`
	// Hop is the distance from the centred artist (0 for it); 0 for every node of an
	// uncentred graph.
	Hop int `json:"hop"`
	// Genre is the artist's strongest genre across the recordings in the asked roles
	// that credit them; Role the role they hold on the most of those recordings. Both
	// are for colouring the node.
	Genre string `json:"genre,omitempty"`
	Role  string `json:"role,omitempty"`
}

// GraphEdge is two artists credited on the same recordings.
type GraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	// Weight is how many recordings in scope credit both.
	Weight int `json:"weight"`
}

// Collaboration is one pair, named, for the ranked list beside the graph.
type Collaboration struct {
	A      GraphNode `json:"a"`
	B      GraphNode `json:"b"`
	Weight int       `json:"weight"`
}

// GraphReport is the answer to GET /insights/graph.
type GraphReport struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	// Collaborators ranks the strongest pairs — with Artist set, that artist's.
	Collaborators []Collaboration `json:"collaborators"`
	// Connected is how many artists had at least one edge passing the filters, before
	// the node cap; Truncated says the cap cut some.
	Connected int  `json:"connected"`
	Truncated bool `json:"truncated"`
	// Center is the centred artist, when Artist was set and is in scope.
	Center *GraphNode `json:"center,omitempty"`
	// Unsynced is how many releases on disk have no credits read yet (not cached).
	Unsynced int `json:"unsynced"`
	// Genres are the library's most common genres, most common first: the ones that
	// get a colour. Library-wide rather than per answer, so changing a filter never
	// repaints a genre (a colour follows its genre, not its rank in the view).
	Genres []string `json:"genres"`
}

type pairKey struct{ a, b string }

func orderedPair(x, y string) pairKey {
	if x < y {
		return pairKey{x, y}
	}
	return pairKey{y, x}
}

// Graph builds the collaboration graph for the files in scope from insight_credits.
// Every query is against derived tables; nothing reads a file or the network.
func Graph(db *gorm.DB, opts GraphOptions) (GraphReport, error) {
	report := GraphReport{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Collaborators: []Collaboration{}, Genres: []string{}}
	roles := validRoles(opts.Roles)
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultGraphNodes
	}
	if limit > MaxGraphNodes {
		limit = MaxGraphNodes
	}

	counted, err := cachedCreditGraph(db, opts, roles)
	if err != nil {
		return report, err
	}
	weights, tracks := counted.weights, counted.tracks

	// The filtered graph: edges at or above the weight floor between artists at or
	// above the track floor.
	adjacent := map[string]map[string]int{}
	for p, w := range weights {
		if w < opts.MinWeight || tracks[p.a] < opts.MinTracks || tracks[p.b] < opts.MinTracks {
			continue
		}
		for _, e := range [][2]string{{p.a, p.b}, {p.b, p.a}} {
			if adjacent[e[0]] == nil {
				adjacent[e[0]] = map[string]int{}
			}
			adjacent[e[0]][e[1]] = w
		}
	}
	report.Connected = len(adjacent)

	// Summed once: computing it inside the sort's comparator walked every neighbour
	// on every comparison.
	strength := make(map[string]int, len(adjacent))
	for id, neighbours := range adjacent {
		for _, w := range neighbours {
			strength[id] += w
		}
	}
	byStrength := func(ids []string) {
		sort.Slice(ids, func(i, j int) bool {
			si, sj := strength[ids[i]], strength[ids[j]]
			if si != sj {
				return si > sj
			}
			return ids[i] < ids[j]
		})
	}

	hop := map[string]int{}
	var chosen []string
	if opts.Artist != "" {
		if _, ok := tracks[opts.Artist]; ok {
			hops := opts.Hops
			if hops < 1 {
				hops = 1
			}
			if hops > 2 {
				hops = 2
			}
			hop[opts.Artist] = 0
			chosen = []string{opts.Artist}
			frontier := []string{opts.Artist}
			for depth := 1; depth <= hops; depth++ {
				var next []string
				for _, id := range frontier {
					for n := range adjacent[id] {
						if _, done := hop[n]; !done {
							hop[n] = depth
							next = append(next, n)
						}
					}
				}
				// Nearest first, strongest first within a ring, so the cap trims the
				// outer ring's weakest.
				byStrength(next)
				chosen = append(chosen, next...)
				frontier = next
			}
		}
	} else {
		chosen = byCollaboration(weights, adjacent, strength, limit)
	}
	if len(chosen) > limit {
		chosen = chosen[:limit]
	}
	report.Truncated = len(chosen) < len(adjacent) && (opts.Artist == "" || len(chosen) == limit)

	// The strongest pairs, picked before anything is named: only the artists drawn or
	// listed need a name and a collection lookup.
	var top []weightedPair
	if opts.Artist != "" {
		if _, ok := tracks[opts.Artist]; ok {
			for n, w := range adjacent[opts.Artist] {
				top = append(top, weightedPair{orderedPair(opts.Artist, n), w})
			}
		}
	} else {
		for p, w := range weights {
			if adjacent[p.a][p.b] > 0 {
				top = append(top, weightedPair{p, w})
			}
		}
	}
	top = strongest(top, topCollaborators)

	shown := append([]string{}, chosen...)
	for _, p := range top {
		shown = append(shown, p.a, p.b)
	}
	names, err := artistNames(db, shown)
	if err != nil {
		return report, err
	}
	genres, mainRoles, err := artistColours(db, shown, roles)
	if err != nil {
		return report, err
	}
	collected := map[string]bool{}
	for _, ids := range chunk(shown, 500) {
		var inCollection []string
		db.Model(&models.CollectionArtist{}).Where("mb_id IN ?", ids).Pluck("mb_id", &inCollection)
		for _, id := range inCollection {
			collected[id] = true
		}
	}
	node := func(id string) GraphNode {
		return GraphNode{MBID: id, Name: names[id], Tracks: tracks[id], InCollection: collected[id], Hop: hop[id],
			Genre: genres[id], Role: mainRoles[id]}
	}

	included := map[string]bool{}
	for _, id := range chosen {
		included[id] = true
		report.Nodes = append(report.Nodes, node(id))
	}
	for p, w := range weights {
		if included[p.a] && included[p.b] && adjacent[p.a][p.b] > 0 {
			report.Edges = append(report.Edges, GraphEdge{Source: p.a, Target: p.b, Weight: w})
		}
	}
	sort.Slice(report.Edges, func(i, j int) bool {
		a, b := report.Edges[i], report.Edges[j]
		if a.Weight != b.Weight {
			return a.Weight > b.Weight
		}
		return a.Source+a.Target < b.Source+b.Target
	})

	var pairs []Collaboration
	if opts.Artist != "" {
		if _, ok := tracks[opts.Artist]; ok {
			center := node(opts.Artist)
			report.Center = &center
			for _, p := range top {
				other := p.a
				if other == opts.Artist {
					other = p.b
				}
				pairs = append(pairs, Collaboration{A: center, B: node(other), Weight: p.weight})
			}
		}
	} else {
		for _, p := range top {
			pairs = append(pairs, Collaboration{A: node(p.a), B: node(p.b), Weight: p.weight})
		}
	}
	report.Collaborators = rankPairs(pairs)

	if err := db.Model(&models.InsightCreditRelease{}).Where("genre <> ''").Group("genre").
		Order("COUNT(*) DESC, genre").Limit(ColouredGenres).Pluck("genre", &report.Genres).Error; err != nil {
		return report, err
	}

	var unsynced int64
	db.Model(&models.LibraryItem{}).Scopes(models.InDiskView, models.InEnabledLibrary).
		Where("library_items.mb_release_id NOT IN (?)", db.Model(&models.InsightCreditRelease{}).Select("release_mb_id")).
		Distinct("library_items.mb_release_id").Count(&unsynced)
	report.Unsynced = int(unsynced)
	return report, nil
}

// scopedCredits reads the credits of the recordings in scope: by recording, the
// artists credited on it (each once, whatever their roles or however many copies of
// the recording are held); by artist, how many recordings credit them; and names.
//
// Not one SQL join. Joining insight_credits to library_items on (release, recording)
// probed library_items by release and filtered a dozen rows per credit, and scanning
// the result through GORM cost as much again: 12 s for every role at 100k tracks
// (measured on the demo instance, docs/demo.md). Reading the in-scope pairs once and
// streaming the credits past them is one indexed scan each.
func scopedCredits(db *gorm.DB, opts GraphOptions, roles []string) (map[string][]string, map[string]int, error) {
	type pair struct{ release, recording string }
	scope := db.Model(&models.LibraryItem{}).Scopes(models.InDiskView, models.InEnabledLibrary).
		Distinct("library_items.mb_release_id", "library_items.mb_recording_id")
	if opts.Library != nil {
		scope = scope.Where("library_items.library_id = ?", *opts.Library)
	}
	pairRows, err := scope.Rows()
	if err != nil {
		return nil, nil, err
	}
	inScope := map[pair]bool{}
	for pairRows.Next() {
		var p pair
		if err := pairRows.Scan(&p.release, &p.recording); err != nil {
			pairRows.Close()
			return nil, nil, err
		}
		inScope[p] = true
	}
	pairRows.Close()

	// No names here: one string per row was most of the garbage this made. The few
	// artists drawn are named afterwards (artistNames).
	rows, err := db.Model(&models.InsightCredit{}).
		Select("release_mb_id", "recording_mb_id", "artist_mb_id").
		Where("role IN ?", roles).Rows()
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byRecording := map[string][]string{}
	tracks := map[string]int{}
	seen := map[pairKey]bool{}
	var release, recording, artist string
	for rows.Next() {
		if err := rows.Scan(&release, &recording, &artist); err != nil {
			return nil, nil, err
		}
		if !inScope[pair{release, recording}] || (!opts.IncludeSpecial && specialArtists[artist]) {
			continue
		}
		k := pairKey{recording, artist}
		if seen[k] {
			continue
		}
		seen[k] = true
		byRecording[recording] = append(byRecording[recording], artist)
		tracks[artist]++
	}
	return byRecording, tracks, rows.Err()
}

// byCollaboration picks an uncentred graph's artists by following its strongest
// edges: each edge, heaviest first, brings in both of its artists until the cap is
// reached. Picking the strongest artists one by one instead chose artists that mostly
// did not connect to each other: on the 110k-track demo, 150 of 4,152 connected
// artists with 71 edges among them, a cloud of dots. Following edges keeps every
// chosen artist next to a partner, and the picture says what the ranked list beside
// it says.
func byCollaboration(weights map[pairKey]int, adjacent map[string]map[string]int, strength map[string]int, limit int) []string {
	edges := make([]weightedPair, 0, len(weights))
	for p, w := range weights {
		if adjacent[p.a][p.b] > 0 {
			edges = append(edges, weightedPair{p, w})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].weight != edges[j].weight {
			return edges[i].weight > edges[j].weight
		}
		si := strength[edges[i].a] + strength[edges[i].b]
		sj := strength[edges[j].a] + strength[edges[j].b]
		if si != sj {
			return si > sj
		}
		return edges[i].a+edges[i].b < edges[j].a+edges[j].b
	})
	in := map[string]bool{}
	var chosen []string
	for _, e := range edges {
		if len(chosen) >= limit {
			break
		}
		missing := 0
		for _, id := range []string{e.a, e.b} {
			if !in[id] {
				missing++
			}
		}
		// Never half an edge: with one slot left, only an edge that brings one new
		// artist (its partner is already drawn) fits.
		if len(chosen)+missing > limit {
			continue
		}
		pair := []string{e.a, e.b}
		if strength[e.b] > strength[e.a] {
			pair = []string{e.b, e.a} // the stronger artist first
		}
		for _, id := range pair {
			if !in[id] {
				in[id] = true
				chosen = append(chosen, id)
			}
		}
	}
	return chosen
}

// creditGraph is the counted graph before any filter: recordings shared per pair of
// artists, and recordings per artist.
type creditGraph struct {
	weights map[pairKey]int
	tracks  map[string]int
}

// graphCache keeps the last counted graph. Moving a slider or the node cap on the page
// asks the same question of the same data; at 100k tracks the scan and count behind it
// are seconds, the filters after it milliseconds. One entry, because the page asks one
// question at a time and every role is about 20 MB of pairs.
var graphCache struct {
	sync.Mutex
	key  string
	data *creditGraph
}

// cachedCreditGraph counts the graph, or returns the count already made for the same
// scope over unchanged data. The key carries a fingerprint of everything the count
// reads — the credits, the index and the libraries — so any change recounts.
func cachedCreditGraph(db *gorm.DB, opts GraphOptions, roles []string) (*creditGraph, error) {
	library := ""
	if opts.Library != nil {
		library = opts.Library.String()
	}
	// The connection pool names the database, so two databases with equal counts and
	// timestamps (tests) never share an entry.
	key := fmt.Sprintf("%p|%s|%s|%v|%s", db.ConnPool, graphFingerprint(db), library, opts.IncludeSpecial, strings.Join(roles, ","))
	graphCache.Lock()
	defer graphCache.Unlock()
	if graphCache.data != nil && graphCache.key == key {
		return graphCache.data, nil
	}
	byRecording, tracks, err := scopedCredits(db, opts, roles)
	if err != nil {
		return nil, err
	}
	weights := map[pairKey]int{}
	for _, artists := range byRecording {
		for i := 0; i < len(artists); i++ {
			for j := i + 1; j < len(artists); j++ {
				weights[orderedPair(artists[i], artists[j])]++
			}
		}
	}
	graphCache.key, graphCache.data = key, &creditGraph{weights: weights, tracks: tracks}
	return graphCache.data, nil
}

// graphFingerprint changes whenever anything a count reads does: a release's credits
// re-read or dropped, a file indexed, changed or removed, a library switched on or off.
func graphFingerprint(db *gorm.DB) string {
	var parts []string
	for _, q := range []string{
		"SELECT COUNT(*) || '/' || COALESCE(MAX(cached_at), '') FROM insight_credit_releases",
		"SELECT COUNT(*) || '/' || COALESCE(MAX(updated_at), '') FROM library_items",
		"SELECT COUNT(*) || '/' || COALESCE(MAX(updated_at), '') FROM libraries",
	} {
		var v string
		db.Raw(q).Scan(&v)
		parts = append(parts, v)
	}
	return strings.Join(parts, "|")
}

// resetGraphCache drops the cached count (tests that rewrite a table in place).
func resetGraphCache() {
	graphCache.Lock()
	graphCache.key, graphCache.data = "", nil
	graphCache.Unlock()
}

// weightedPair is a pair of artists and the recordings they share.
type weightedPair struct {
	pairKey
	weight int
}

// strongest keeps the k heaviest pairs, ties broken by ID, without sorting them all:
// every connected pair at 100k tracks is a few hundred thousand, and the list wants 25.
func strongest(pairs []weightedPair, k int) []weightedPair {
	if len(pairs) > k {
		weights := make([]int, len(pairs))
		for i, p := range pairs {
			weights[i] = p.weight
		}
		sort.Sort(sort.Reverse(sort.IntSlice(weights)))
		floor := weights[k-1]
		kept := pairs[:0:0]
		for _, p := range pairs {
			if p.weight >= floor {
				kept = append(kept, p)
			}
		}
		pairs = kept
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].weight != pairs[j].weight {
			return pairs[i].weight > pairs[j].weight
		}
		return pairs[i].a+pairs[i].b < pairs[j].a+pairs[j].b
	})
	if len(pairs) > k {
		pairs = pairs[:k]
	}
	return pairs
}

// ColouredGenres is how many genres get a colour of their own. Three, because a node
// graph puts any two colours side by side, and only the first three categorical
// slots stay apart for colour-blind viewers when every pair can touch (docs/style-guide.md,
// Collaboration graph). The rest share a neutral "Other".
const ColouredGenres = 3

// artistColours finds, for each artist, their strongest genre and their main role
// across the recordings in the asked roles that credit them: what the node is
// coloured by. Counted library-wide, which for a few hundred artists is two grouped,
// indexed queries.
func artistColours(db *gorm.DB, ids, roles []string) (map[string]string, map[string]string, error) {
	genre, genreN := map[string]string{}, map[string]int{}
	role, roleN := map[string]string{}, map[string]int{}
	rank := map[string]int{}
	for i, r := range GraphRoles {
		rank[r] = i
	}
	for _, part := range chunk(ids, 500) {
		var genres []struct {
			Artist, Genre string
			N             int
		}
		if err := db.Table("insight_credits AS c").
			Select("c.artist_mb_id AS artist, r.genre AS genre, COUNT(DISTINCT c.recording_mb_id) AS n").
			Joins("JOIN insight_credit_releases AS r ON r.release_mb_id = c.release_mb_id").
			Where("c.artist_mb_id IN ? AND c.role IN ? AND r.genre <> ''", part, roles).
			Group("c.artist_mb_id, r.genre").Scan(&genres).Error; err != nil {
			return nil, nil, err
		}
		for _, g := range genres {
			if g.N > genreN[g.Artist] || (g.N == genreN[g.Artist] && g.Genre < genre[g.Artist]) {
				genre[g.Artist], genreN[g.Artist] = g.Genre, g.N
			}
		}
		var held []struct {
			Artist, Role string
			N            int
		}
		if err := db.Model(&models.InsightCredit{}).
			Select("artist_mb_id AS artist, role, COUNT(DISTINCT recording_mb_id) AS n").
			Where("artist_mb_id IN ? AND role IN ?", part, roles).
			Group("artist_mb_id, role").Scan(&held).Error; err != nil {
			return nil, nil, err
		}
		for _, h := range held {
			current, ok := role[h.Artist]
			if !ok || h.N > roleN[h.Artist] || (h.N == roleN[h.Artist] && rank[h.Role] < rank[current]) {
				role[h.Artist], roleN[h.Artist] = h.Role, h.N
			}
		}
	}
	return genre, role, nil
}

// artistNames names artists by MBID from their credits.
func artistNames(db *gorm.DB, ids []string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	for _, part := range chunk(ids, 500) {
		var rows []struct{ ArtistMBID, ArtistName string }
		if err := db.Model(&models.InsightCredit{}).Select("artist_mb_id, MAX(artist_name) AS artist_name").
			Where("artist_mb_id IN ?", part).Group("artist_mb_id").Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			names[r.ArtistMBID] = r.ArtistName
		}
	}
	return names, nil
}

func rankPairs(pairs []Collaboration) []Collaboration {
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Weight != pairs[j].Weight {
			return pairs[i].Weight > pairs[j].Weight
		}
		return strings.ToLower(pairs[i].A.Name+pairs[i].B.Name) < strings.ToLower(pairs[j].A.Name+pairs[j].B.Name)
	})
	if len(pairs) > topCollaborators {
		pairs = pairs[:topCollaborators]
	}
	if pairs == nil {
		return []Collaboration{}
	}
	return pairs
}

// validRoles keeps the known roles asked for, defaulting to credited artists.
func validRoles(asked []string) []string {
	known := map[string]bool{}
	for _, r := range GraphRoles {
		known[r] = true
	}
	var out []string
	for _, r := range asked {
		if known[r] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return []string{models.InsightRoleCredit}
	}
	return out
}
