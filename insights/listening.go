package insights

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Edge kinds a graph can be drawn with (GraphOptions.Edges).
const (
	EdgesCredits   = "credits"
	EdgesListening = "listening"
	EdgesBoth      = "both"
)

// Edge kinds on a GraphEdge.
const (
	EdgeKindCredit    = "credit"
	EdgeKindListening = "listening"
)

// Listening overlay tuning (docs/listening.md).
const (
	// crossSourceWindow: two plays of one recording from two sources this close
	// together are one play scrobbled twice (Plex to ListenBrainz, say).
	crossSourceWindow = 10 * time.Minute
	// sessionGap ends a listening session.
	sessionGap = 30 * time.Minute
	// coListenWindow is how many following plays a play is paired with. A sliding
	// window rather than every pair in the session: a ten-hour session would otherwise
	// connect everything in it, at n² cost.
	coListenWindow = 5
	// coListenPartners is how many co-listening partners each artist keeps. Someone
	// who plays a few favourites daily has played every pair of them together, so
	// every co-listening pair drawn was a hairball (562 edges among 35 artists on the
	// small demo); each artist's strongest few say who goes with whom.
	coListenPartners = 3
)

// ListeningSummary is what the overlay counted, for the page to say so.
type ListeningSummary struct {
	// Plays in the period, after cross-source duplicates are dropped.
	Plays int `json:"plays"`
	// Matched plays are identified as a MusicBrainz recording; only they reach the graph.
	Matched   int        `json:"matched"`
	Unmatched int        `json:"unmatched"`
	Since     *time.Time `json:"since,omitempty"`
	Accounts  int        `json:"accounts"`
}

// play is one kept play, reduced to what the overlay reads.
type play struct {
	id        uint
	at        time.Time
	recording string
}

// listenGraph is one user's plays over one period, attributed to artists.
type listenGraph struct {
	// plays per artist, every play counted (not distinct recordings).
	plays map[string]int
	// weights are co-listening sessions per pair of artists.
	weights map[pairKey]int
	// names are artists the plays named that the credits may not (music not on disk).
	names   map[string]string
	summary ListeningSummary
}

var listenCache struct {
	sync.Mutex
	key  string
	data *listenGraph
}

// listenFingerprint changes when the user's plays do.
func listenFingerprint(db *gorm.DB, user uuid.UUID) string {
	var v string
	db.Raw("SELECT COUNT(*) || '/' || COALESCE(MAX(id), 0) FROM listens WHERE user_id = ?", user).Scan(&v)
	var accounts int64
	db.Model(&models.ListeningAccount{}).Where("user_id = ?", user).Count(&accounts)
	return fmt.Sprintf("%s|%d", v, accounts)
}

// cachedListenGraph attributes the listener's plays to artists through the credit
// graph's recordings, or returns the attribution already made for the same question
// over unchanged data. Nil when the user has no listening account.
func cachedListenGraph(db *gorm.DB, opts GraphOptions, roles []string, credits *creditGraph, creditKey string) (*listenGraph, error) {
	if opts.Listener == nil {
		return nil, nil
	}
	var accounts int64
	if err := db.Model(&models.ListeningAccount{}).Where("user_id = ?", *opts.Listener).Count(&accounts).Error; err != nil {
		return nil, err
	}
	if accounts == 0 {
		return nil, nil
	}
	since := opts.Since
	if !since.IsZero() {
		// To the day, so a page opened twice in a minute asks the same question.
		since = time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, since.Location())
	}
	library := ""
	if opts.Library != nil {
		library = opts.Library.String()
	}
	key := fmt.Sprintf("%s|%s|%s|%d|%s|%s", creditKey, opts.Listener, listenFingerprint(db, *opts.Listener),
		since.Unix(), library, strings.Join(roles, ","))
	listenCache.Lock()
	defer listenCache.Unlock()
	if listenCache.data != nil && listenCache.key == key {
		return listenCache.data, nil
	}
	g, err := buildListenGraph(db, *opts.Listener, since, opts, credits)
	if err != nil {
		return nil, err
	}
	g.summary.Accounts = int(accounts)
	listenCache.key, listenCache.data = key, g
	return g, nil
}

// resetListenCache drops the cached attribution (tests).
func resetListenCache() {
	listenCache.Lock()
	listenCache.key, listenCache.data = "", nil
	listenCache.Unlock()
}

// keptPlays reads the user's plays since a time, oldest first, dropping a play of a
// recording that another source already reported within crossSourceWindow. Raw rows
// are kept in the table; this is only how they are counted.
func keptPlays(db *gorm.DB, user uuid.UUID, since time.Time) ([]play, ListeningSummary, error) {
	var summary ListeningSummary
	if !since.IsZero() {
		s := since
		summary.Since = &s
	}
	q := db.Model(&models.Listen{}).Select("id", "listened_at", "recording_mb_id", "source").
		Where("user_id = ?", user).Order("listened_at, id")
	if !since.IsZero() {
		q = q.Where("listened_at >= ?", since)
	}
	rows, err := q.Rows()
	if err != nil {
		return nil, summary, err
	}
	defer rows.Close()
	type last struct {
		at     time.Time
		source string
	}
	lastKept := map[string]last{}
	var out []play
	for rows.Next() {
		var p play
		var source string
		if err := rows.Scan(&p.id, &p.at, &p.recording, &source); err != nil {
			return nil, summary, err
		}
		if p.recording == "" {
			summary.Plays++
			summary.Unmatched++
			continue
		}
		if l, ok := lastKept[p.recording]; ok && l.source != source && p.at.Sub(l.at) <= crossSourceWindow {
			continue
		}
		lastKept[p.recording] = last{p.at, source}
		summary.Plays++
		summary.Matched++
		out = append(out, p)
	}
	return out, summary, rows.Err()
}

func buildListenGraph(db *gorm.DB, user uuid.UUID, since time.Time, opts GraphOptions, credits *creditGraph) (*listenGraph, error) {
	plays, summary, err := keptPlays(db, user, since)
	if err != nil {
		return nil, err
	}
	g := &listenGraph{plays: map[string]int{}, weights: map[pairKey]int{}, names: map[string]string{}, summary: summary}

	// Artists the source named, for plays of recordings the credits do not cover —
	// music not on disk. Only without a library filter: a library's view is of what
	// that library holds.
	fallback := map[uint][]string{}
	if opts.Library == nil {
		q := db.Table("listen_artists AS la").Select("la.listen_id, la.artist_mb_id, la.artist_name").
			Joins("JOIN listens AS l ON l.id = la.listen_id").Where("l.user_id = ?", user)
		if !since.IsZero() {
			q = q.Where("l.listened_at >= ?", since)
		}
		rows, err := q.Rows()
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id uint
			var artist, name string
			if err := rows.Scan(&id, &artist, &name); err != nil {
				rows.Close()
				return nil, err
			}
			fallback[id] = append(fallback[id], artist)
			if name != "" {
				g.names[artist] = name
			}
		}
		rows.Close()
	}

	artistsOf := func(p play) []string {
		var raw []string
		if a, ok := credits.byRecording[p.recording]; ok {
			raw = a
		} else {
			raw = fallback[p.id]
		}
		if opts.IncludeSpecial {
			return raw
		}
		out := raw[:0:0]
		for _, a := range raw {
			if !specialArtists[a] {
				out = append(out, a)
			}
		}
		return out
	}

	attributed := make([][]string, len(plays))
	for i, p := range plays {
		attributed[i] = artistsOf(p)
		for _, a := range attributed[i] {
			g.plays[a]++
		}
	}

	// Co-listening: within a session, each play pairs with the next few plays'
	// artists; a pair counts once per session.
	sessionPairs := map[pairKey]bool{}
	flush := func() {
		for p := range sessionPairs {
			g.weights[p]++
		}
		clear(sessionPairs)
	}
	for i := range plays {
		if i > 0 && plays[i].at.Sub(plays[i-1].at) > sessionGap {
			flush()
		}
		for j := i + 1; j < len(plays) && j <= i+coListenWindow; j++ {
			if plays[j].at.Sub(plays[j-1].at) > sessionGap {
				break
			}
			for _, a := range attributed[i] {
				for _, b := range attributed[j] {
					if a != b {
						sessionPairs[orderedPair(a, b)] = true
					}
				}
			}
		}
	}
	flush()
	g.weights = backbone(g.weights, coListenPartners)
	return g, nil
}

// backbone keeps the pairs that are among the k strongest of either of their artists,
// ties broken by ID so the same plays always keep the same pairs.
func backbone(weights map[pairKey]int, k int) map[pairKey]int {
	partners := map[string][]weightedPair{}
	for p, w := range weights {
		partners[p.a] = append(partners[p.a], weightedPair{p, w})
		partners[p.b] = append(partners[p.b], weightedPair{p, w})
	}
	kept := map[pairKey]int{}
	for _, list := range partners {
		sort.Slice(list, func(i, j int) bool {
			if list[i].weight != list[j].weight {
				return list[i].weight > list[j].weight
			}
			return list[i].a+list[i].b < list[j].a+list[j].b
		})
		for _, wp := range list[:min(k, len(list))] {
			kept[wp.pairKey] = wp.weight
		}
	}
	return kept
}
