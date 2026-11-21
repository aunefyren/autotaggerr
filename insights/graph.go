package insights

import (
	"sort"
	"strings"

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
	report := GraphReport{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Collaborators: []Collaboration{}}
	roles := validRoles(opts.Roles)
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultGraphNodes
	}
	if limit > MaxGraphNodes {
		limit = MaxGraphNodes
	}

	// One row per (recording, artist) in scope: distinct, so a recording held on two
	// releases or in two libraries counts once, and an artist in two roles once.
	q := db.Table("insight_credits AS c").
		Select("DISTINCT c.recording_mb_id AS recording, c.artist_mb_id AS artist, c.artist_name AS name").
		Joins("JOIN library_items ON library_items.mb_release_id = c.release_mb_id AND library_items.mb_recording_id = c.recording_mb_id").
		Scopes(models.InDiskView, models.InEnabledLibrary).
		Where("c.role IN ?", roles)
	if opts.Library != nil {
		q = q.Where("library_items.library_id = ?", *opts.Library)
	}
	var rows []struct{ Recording, Artist, Name string }
	if err := q.Scan(&rows).Error; err != nil {
		return report, err
	}

	byRecording := map[string][]string{}
	tracks := map[string]int{}
	names := map[string]string{}
	seen := map[pairKey]bool{} // (recording, artist): DISTINCT over name too, so re-check
	for _, r := range rows {
		if !opts.IncludeSpecial && specialArtists[r.Artist] {
			continue
		}
		if seen[pairKey{r.Recording, r.Artist}] {
			continue
		}
		seen[pairKey{r.Recording, r.Artist}] = true
		byRecording[r.Recording] = append(byRecording[r.Recording], r.Artist)
		tracks[r.Artist]++
		if names[r.Artist] == "" {
			names[r.Artist] = r.Name
		}
	}

	weights := map[pairKey]int{}
	for _, artists := range byRecording {
		for i := 0; i < len(artists); i++ {
			for j := i + 1; j < len(artists); j++ {
				weights[orderedPair(artists[i], artists[j])]++
			}
		}
	}

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

	strength := func(id string) int {
		total := 0
		for _, w := range adjacent[id] {
			total += w
		}
		return total
	}
	byStrength := func(ids []string) {
		sort.Slice(ids, func(i, j int) bool {
			si, sj := strength(ids[i]), strength(ids[j])
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
		for id := range adjacent {
			chosen = append(chosen, id)
		}
		byStrength(chosen)
	}
	if len(chosen) > limit {
		chosen = chosen[:limit]
		report.Truncated = true
	}

	var inCollection []string
	if len(chosen) > 0 {
		db.Model(&models.CollectionArtist{}).Where("mb_id IN ?", chosen).Pluck("mb_id", &inCollection)
	}
	collected := map[string]bool{}
	for _, id := range inCollection {
		collected[id] = true
	}
	node := func(id string) GraphNode {
		return GraphNode{MBID: id, Name: names[id], Tracks: tracks[id], InCollection: collected[id], Hop: hop[id]}
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

	if opts.Artist != "" {
		if _, ok := tracks[opts.Artist]; ok {
			center := node(opts.Artist)
			report.Center = &center
			var pairs []Collaboration
			for n, w := range adjacent[opts.Artist] {
				pairs = append(pairs, Collaboration{A: center, B: node(n), Weight: w})
			}
			report.Collaborators = rankPairs(pairs)
		}
	} else {
		var pairs []Collaboration
		for p, w := range weights {
			if adjacent[p.a][p.b] > 0 {
				pairs = append(pairs, Collaboration{A: node(p.a), B: node(p.b), Weight: w})
			}
		}
		report.Collaborators = rankPairs(pairs)
	}

	var unsynced int64
	db.Model(&models.LibraryItem{}).Scopes(models.InDiskView, models.InEnabledLibrary).
		Where("library_items.mb_release_id NOT IN (?)", db.Model(&models.InsightCreditRelease{}).Select("release_mb_id")).
		Distinct("library_items.mb_release_id").Count(&unsynced)
	report.Unsynced = int(unsynced)
	return report, nil
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
