// Package artwork is the **refresh artwork** verb: filling the local image cache
// ahead of the page that wants it.
//
// Album covers and artist portraits are the one thing nothing warmed. Every other
// cache Autotaggerr keeps is filled by a scheduled pass (see docs/mirror.md); artwork
// was filled entirely by whoever opened the page, so an artist with an eighty-album
// discography fired eighty Cover Art Archive requests at roughly two per second and
// painted its rows over the following forty seconds. It recurred, too: "there is no
// cover for this MBID" is remembered for seven days only, and most of a followed
// artist's back catalogue has no cover, so a large artist page re-paid a big share of
// that cost every week.
//
// # Why this is not a phase of the metadata refresh
//
// It was, once, and that was wrong on three counts. Artwork providers are their own
// kind of data source — /data-sources and /libraries both say so — so attaching them
// to the verb named after the *other* kind works against a distinction the product
// already makes. *Refresh metadata* means one thing and has been carefully kept to
// meaning it (docs/mirror.md, "One name, two forms"), and "…and downloads a few
// hundred megabytes of images" is not it. And the counters did not fit: a mirror
// pass's Fetched is what cost a **MusicBrainz** rate-limit slot, while images come
// from a different budget entirely and their negatives expire weekly by design, so
// folding them in left Fetched permanently large and meaningless.
//
// # Refresh is the same word on purpose
//
// This verb honours a TTL, skips what is fresh, and re-reads what expired — exactly
// what *Refresh metadata* means. Two operations that behave identically get the same
// word. Following the same rule: a control says **Refresh artwork**, a record says
// **Artwork refresh**.
//
// # It does not yield to file-writing work
//
// The metadata mirror pauses while a scan runs, because both spend the same
// one-request-per-second MusicBrainz budget and the scan is the one with a user
// attached. Nothing here touches that budget: images come from the artwork hosts'
// own throttle (~2 req/s per host, modules.artworkRateLimit), and the only disk this
// writes is config/artwork/. Yielding would buy a scan nothing measurable and would
// make the case this exists for — an artist added mid-scan whose covers a user is
// about to want — the slowest one.
package artwork

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// rowCoverSize is the only cover size warmed ahead of time: the one a *list* asks
// for. The discography table and the collection rows render covers at 250
// (webui's Artwork component doubles a 26px tile and floors at 250), and a list is
// where the cost is — one page can ask for a hundred of them.
//
// The 500px hero on the album page is deliberately left on demand. It is a single
// image on a page a user opened one album at a time, so it costs one throttled
// request; warming it would double this pass and the disk it uses to make an
// already-fast page marginally faster.
const rowCoverSize = 250

// Targets is what one pass covers. Every entry is a list of MBIDs, so narrowing
// later is a constructor rather than new machinery.
type Targets struct {
	Artists []string
	Groups  []string
}

// Empty reports a target set with nothing in it, which is the common case for a
// hook that fired on an update rather than a create.
func (t Targets) Empty() bool { return len(t.Artists) == 0 && len(t.Groups) == 0 }

// merge folds another set in, dropping duplicates. A rebuild creating two hundred
// release-groups notifies two hundred times, and the accumulated set is what the
// worker actually warms — so this runs on every notification and must not grow
// without bound.
func (t *Targets) merge(other Targets) {
	t.Artists = mergeIDs(t.Artists, other.Artists)
	t.Groups = mergeIDs(t.Groups, other.Groups)
}

func mergeIDs(into, add []string) []string {
	if len(add) == 0 {
		return into
	}
	seen := make(map[string]bool, len(into))
	for _, id := range into {
		seen[id] = true
	}
	for _, id := range add {
		if id != "" && !seen[id] {
			seen[id] = true
			into = append(into, id)
		}
	}
	return into
}

// CollectionTargets covers every artist and release-group the collection holds. This
// is the scheduled pass, and the one the manual button starts.
//
// Release-groups with a confirmed deletion are excluded: their ID resolves nowhere,
// so asking the Cover Art Archive about them buys a guaranteed 404 every night. The
// metadata mirror skips them for the same reason.
func CollectionTargets(db *gorm.DB) (Targets, error) {
	var targets Targets
	if db == nil {
		return targets, nil
	}

	if err := db.Model(&models.CollectionArtist{}).
		Where("mb_id <> ''").Distinct().Pluck("mb_id", &targets.Artists).Error; err != nil {
		return targets, err
	}

	retired, err := retiredGroups(db)
	if err != nil {
		return targets, err
	}
	var groups []string
	if err := db.Model(&models.CollectionReleaseGroup{}).
		Where("mb_id <> ''").Distinct().Pluck("mb_id", &groups).Error; err != nil {
		return targets, err
	}
	for _, id := range groups {
		if !retired[id] {
			targets.Groups = append(targets.Groups, id)
		}
	}
	return targets, nil
}

// retiredGroups is the release-group MBIDs whose deletion is already on record.
// Every status counts, including dismissed and failed: those mean "do not retire this
// row", which is a decision about the collection and not a claim that the ID resolves
// upstream.
func retiredGroups(db *gorm.DB) (map[string]bool, error) {
	var mbids []string
	if err := db.Model(&models.MusicbrainzMigration{}).
		Where("entity_type = ? AND kind = ?", models.MigrationEntityReleaseGroup, models.MigrationKindDeleted).
		Pluck("old_mb_id", &mbids).Error; err != nil {
		return nil, err
	}
	retired := make(map[string]bool, len(mbids))
	for _, id := range mbids {
		retired[id] = true
	}
	return retired, nil
}

// unit is one image to warm. Artist images carry no size that matters — fanart.tv
// serves whatever it has and modules.ArtworkFresh folds every request onto a single
// key — so the field is only ever meaningful for covers.
type unit struct {
	entity string
	mbid   string
	kind   string
	size   int
}

// plan is what a pass would warm, given what the providers can actually serve.
//
// The capability check is not an optimisation. modules.GetArtwork records a provider
// that is switched off as ErrNoArtwork, and ErrNoArtwork is *remembered* — so a pass
// that asked anyway would write "there is no cover for this album" for every album in
// the collection, and the UI would keep showing monograms for a week after the source
// was enabled. The UI's own /artwork-capabilities guard exists for the same reason;
// this is that guard on the server side, reading the same two predicates.
func plan(providers modules.ArtworkProviders, targets Targets) []unit {
	var units []unit

	if components.CanServeArtistImages(providers) {
		// Two kinds, two cache keys, two lookups: the portrait beside the artist name
		// and the backdrop behind it are separate images even though one fanart.tv
		// document describes both.
		for _, id := range targets.Artists {
			units = append(units,
				unit{modules.ArtworkEntityArtist, id, modules.ArtworkKindThumb, 0},
				unit{modules.ArtworkEntityArtist, id, modules.ArtworkKindBackground, 0},
			)
		}
	}

	if components.CanServeCovers(providers) {
		for _, id := range targets.Groups {
			units = append(units, unit{
				modules.ArtworkEntityReleaseGroup, id, modules.ArtworkKindFront, rowCoverSize,
			})
		}
	}

	return units
}

// Summary is the status of the current or most recent pass.
//
// Fetched and Fresh are the pair worth reading, the same way they are for the
// metadata mirror: Fetched cost an upstream request, Fresh was already on disk. A
// healthy steady state is almost all Fresh.
type Summary struct {
	Running    bool       `json:"running"`
	Title      string     `json:"title,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	Total int `json:"total"`
	Done  int `json:"done"`

	Fetched int `json:"fetched"`
	Fresh   int `json:"fresh"`
	// Missing is the ordinary answer, not a failure: the provider was asked and has
	// no image. Its own counter rather than a share of Fetched, because on a page
	// about artwork providers it is the figure that explains a screen full of
	// monogram tiles.
	Missing int `json:"missing"`
	Errors  int `json:"errors"`

	LastError string `json:"last_error,omitempty"`

	// Cached is what the image cache holds right now, meaningful between passes too —
	// which is why it is not reset with the counters. Images is what a forced pass
	// would re-download, so it is the number the confirm dialog's estimate rests on.
	Images        int `json:"images"`
	MissingCached int `json:"missing_cached"`

	// Capable reports whether anything can be fetched at all. Without it the page
	// cannot tell "nothing warmed because it is all fresh" from "nothing warmed
	// because no provider is configured", which are opposite situations.
	CoversEnabled bool `json:"covers_enabled"`
	ArtistEnabled bool `json:"artist_enabled"`
}

// Result is what one pass did.
type Result struct {
	Checked int
	Fetched int
	Fresh   int
	Missing int
	Errors  int

	LastError string

	// Items is the per-image detail, and holds **failures only**. A row per coverless
	// album would be thousands of rows saying nothing happened, and "no image" is the
	// ordinary outcome this pass exists to record cheaply.
	Items      []models.EventItem
	ItemsTotal int

	detailLimit int
}

func (r *Result) note(item models.EventItem) {
	r.ItemsTotal++
	if len(r.Items) >= r.detailCap() {
		return
	}
	r.Items = append(r.Items, item)
}

func (r *Result) detailCap() int {
	if r.detailLimit < 1 {
		return models.DefaultEventDetailRetention
	}
	return r.detailLimit
}

// Runner owns artwork execution, its queue and its status. One instance is shared by
// the cron job, the API and the collection hooks.
type Runner struct {
	db *gorm.DB

	// providers is swappable so a test can point the pass at an httptest server
	// without seeding data-source rows.
	providers func(*gorm.DB) modules.ArtworkProviders

	// queue state. hooks accumulates the targeted work that row creation notified
	// about; full says a collection-wide pass is wanted, and force whether it should
	// ignore cached copies.
	queueMu sync.Mutex
	hooks   Targets
	full    bool
	force   bool

	wake chan struct{}

	running atomic.Bool

	statusMu sync.Mutex
	summary  Summary

	cancelMu sync.Mutex
	cancel   context.CancelFunc

	eventRetention  int
	detailRetention int
}

// NewRunner builds an artwork runner and starts its queue worker. A zero-valued cfg
// is legitimate — every field it reads falls back to its default.
func NewRunner(db *gorm.DB, cfg models.ConfigStruct) *Runner {
	r := newRunner(db, cfg)
	go r.worker()
	return r
}

// newRunner builds the runner without starting its worker, which is what lets a test
// drive the queue deterministically. With the worker running there is no way to
// observe a queued item — it is drained the instant it is enqueued — so every test of
// what the queue *holds* would race the goroutine draining it.
func newRunner(db *gorm.DB, cfg models.ConfigStruct) *Runner {
	return &Runner{
		db:              db,
		providers:       components.ArtworkProviders,
		wake:            make(chan struct{}, 1),
		eventRetention:  retentionOrDefault(cfg.AutotaggerrEventRetention, models.DefaultEventRetention),
		detailRetention: retentionOrDefault(cfg.AutotaggerrEventDetailRetention, models.DefaultEventDetailRetention),
	}
}

func retentionOrDefault(configured, fallback int) int {
	if configured < 1 {
		return fallback
	}
	return configured
}

// Running reports whether a pass is in progress.
func (r *Runner) Running() bool { return r.running.Load() }

// Status returns a copy of the current/last summary with live cache coverage and
// provider capability folded in.
func (r *Runner) Status() Summary {
	r.statusMu.Lock()
	summary := r.summary
	r.statusMu.Unlock()

	summary.Images, summary.MissingCached = modules.ArtworkCacheCounts()
	providers := r.resolveProviders()
	summary.CoversEnabled = components.CanServeCovers(providers)
	summary.ArtistEnabled = components.CanServeArtistImages(providers)
	return summary
}

// Cancel stops a running pass at the next image boundary. Safe and cheap at any
// point, because a pass keeps no cursor — the next one resumes by skipping whatever
// is already fresh.
func (r *Runner) Cancel() {
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *Runner) resolveProviders() modules.ArtworkProviders {
	if r.providers == nil {
		return modules.ArtworkProviders{}
	}
	return r.providers(r.db)
}

func (r *Runner) setStatus(mutate func(*Summary)) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	mutate(&r.summary)
}

// warmOne fetches one image into the cache, or skips it when the cache already holds
// an answer.
//
// force reaches both halves of the cache: an image already on disk is re-downloaded
// and a remembered "no image" is re-asked. Those cost wildly different amounts — the
// first is a transfer, the second a request — which is why the confirm dialog's
// estimate is driven by the image count rather than by the total.
func (r *Runner) warmOne(providers modules.ArtworkProviders, u unit, force bool, res *Result, track bool) {
	res.Checked++

	if force {
		// Reaching past GetArtwork's own cache check, which is what actually decides
		// whether a request happens.
		modules.ArtworkExpire(u.entity, u.mbid, u.kind, u.size)
	} else if modules.ArtworkFresh(u.entity, u.mbid, u.kind, u.size) {
		res.Fresh++
		if track {
			r.setStatus(func(s *Summary) { s.Fresh++ })
		}
		return
	}

	_, err := modules.GetArtwork(providers, u.entity, u.mbid, u.kind, u.size)

	switch {
	case err == nil:
		res.Fetched++
		if track {
			r.setStatus(func(s *Summary) { s.Fetched++ })
		}
	case isNoArtwork(err):
		// The provider was asked and has nothing. Most artists have no fanart.tv
		// entry and most catalogue-only releases have no cover, so this is the
		// ordinary outcome — and it is now remembered, which is the whole point.
		res.Missing++
		if track {
			r.setStatus(func(s *Summary) { s.Missing++ })
		}
	default:
		message := fmt.Sprintf("%s %s %s: %s", u.entity, u.kind, u.mbid, err.Error())
		logger.Log.Warnf("artwork refresh failed for %s", message)
		res.Errors++
		res.LastError = message
		res.note(models.EventItem{
			Path:   u.mbid,
			Kind:   models.EventItemKindEntity,
			Status: models.EventItemStatusError,
			Error:  err.Error(),
		})
		if track {
			r.setStatus(func(s *Summary) {
				s.Errors++
				s.LastError = message
			})
		}
	}
}

func isNoArtwork(err error) bool { return errors.Is(err, modules.ErrNoArtwork) }

// summaryLine is the one sentence a reader sees without opening the event.
func summaryLine(res Result, took time.Duration) string {
	line := fmt.Sprintf("%d image(s) — %d fetched, %d already cached", res.Checked, res.Fetched, res.Fresh)
	if res.Missing > 0 {
		line += fmt.Sprintf(", %d with no image available", res.Missing)
	}
	if res.Errors > 0 {
		line += fmt.Sprintf(", %d failed", res.Errors)
	}
	return line + fmt.Sprintf(" (%s)", took.Round(time.Second))
}

// record writes a pass's outcome onto its event.
func (r *Runner) record(ev *models.Event, started, finished time.Time, res Result, cancelled bool, total, done int) {
	if ev == nil {
		return
	}
	line := summaryLine(res, finished.Sub(started))
	if cancelled {
		line = "stopped early — " + line
	}

	details := map[string]any{
		"total":     total,
		"done":      done,
		"fetched":   res.Fetched,
		"fresh":     res.Fresh,
		"missing":   res.Missing,
		"errors":    res.Errors,
		"cancelled": cancelled,
		"detail": map[string]any{
			"recorded": len(res.Items),
			"total":    res.ItemsTotal,
			"limit":    res.detailCap(),
		},
	}
	if res.LastError != "" {
		details["last_error"] = res.LastError
	}

	ev.Stats = []models.EventStat{
		{Label: "Images checked", Value: res.Checked},
		{Label: "Fetched", Value: res.Fetched},
		{Label: "Already cached", Value: res.Fresh, Kind: models.EventStatMuted},
		{Label: "No image available", Value: res.Missing, Kind: models.EventStatMuted},
		{Label: "Failed", Value: res.Errors, Kind: models.EventStatBad, Filter: models.EventItemStatusError},
	}

	events.AddItems(r.db, ev, res.Items)
	// Errors are per-image and the pass carried on regardless, so a pass that hit
	// some is still an "ok" outcome with a count — not a failed job.
	events.Finish(r.db, ev, models.EventStatusOK, line, details)
	events.Prune(r.db, r.eventRetention)
}
