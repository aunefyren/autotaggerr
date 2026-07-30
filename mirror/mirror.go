// Package mirror is the **refresh metadata** verb: everything Autotaggerr does
// that reads MusicBrainz and writes nothing to disk.
//
// It is one implementation driven by a Scope, so "refresh this artist" and
// "refresh the whole collection on a schedule" are the same code with a different
// set of MBIDs in it. Before this they were separate functions, and they had
// silently drifted apart — the per-artist path never fetched edition lists at all,
// so an artist page stayed cold right after the user pressed Refresh on it.
//
// # It never writes files
//
// This package fetches, stores in the cache, re-links releases that moved
// release-group, and reports what changed. It does not re-tag files and does not
// tell Plex anything. That is a deliberate split: a button labelled *Refresh
// metadata* that also rewrote four hundred files would be lying about its scope,
// and tag writes touch the user's actual audio files.
//
// What acts on the result is the scan (which owns file writes) and the tag verb.
// A run reports the releases whose content changed upstream; `scan.Runner` re-tags
// those in its own pass, and a user who wants it now presses Tag files.
//
// # Why it is scheduled
//
// MusicBrainz is rate limited to roughly one request per second, and that budget
// is spent by whoever asks first. On demand, the person who discovers every
// expired entry is the user, waiting on a page. On a schedule, the cost lands in a
// job nobody is watching.
//
// Two properties make a multi-hour collection pass tolerable. It is **resumable**:
// a pass fetches only what is missing or expired, so an interrupted run costs
// nothing on the next one and no cursor has to be persisted. And it **yields**:
// while file-writing work (a scan, a re-tag) is running, the pass pauses. Both draw
// on the same limiter, and the one with a user attached wins.
package mirror

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/migration"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// eventRetention caps how many Activity events are kept after a pass. It matches
// the scan runner's figure — they prune the same table.
const eventRetention = 200

// yieldPollInterval is how often a paused pass re-checks whether the file-writing
// work it yielded to has finished. Long enough to be free, short enough that a pass
// resumes promptly. A var so tests need not wait it out.
var yieldPollInterval = 15 * time.Second

// coldReleaseTTL is how long a release nobody owns stays fresh.
//
// Freshness is tiered by how much the collection actually cares. A release with
// files on disk drives the tags on those files, so it keeps the ordinary 7–14 day
// TTL. A release that is only in the catalogue — pulled in by following an artist,
// there to answer "what could I have" — is reference data, and re-reading it weekly
// spends the rate limit that owned releases need. Most of a followed artist's
// discography is this, so the saving is the difference between a pass that finishes
// overnight and one that does not.
var coldReleaseTTL = 30 * 24 * time.Hour

// Phase names, reported in Summary.Phase so a long pass is legible while it runs.
const (
	PhaseIdle          = ""
	PhaseArtists       = "artists"
	PhaseDiscographies = "discographies"
	PhaseEditions      = "editions"
	PhaseReleases      = "releases"
	PhasePaused        = "paused"
)

// Summary is the status of the current or most recent pass.
//
// Fetched and Fresh together are the useful pair: Fetched is what actually cost a
// rate-limit slot, Fresh is what the cache already had. A healthy steady state is
// almost all Fresh, and a pass that is mostly Fetched means the TTLs are shorter
// than the schedule.
type Summary struct {
	Running    bool       `json:"running"`
	Phase      string     `json:"phase,omitempty"`
	Title      string     `json:"title,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// Total is how many entities this pass will consider; Done is how many it has
	// reached so far. Together they are the progress bar.
	Total int `json:"total"`
	Done  int `json:"done"`

	Fetched int `json:"fetched"`
	Fresh   int `json:"fresh"`
	Errors  int `json:"errors"`

	// ChangedReleases counts releases whose content differs from the cached copy.
	// Nothing here acts on that — it is the handover to the verbs that write files.
	ChangedReleases int `json:"changed_releases"`
	GoneReleases    int `json:"gone_releases"`
	Relinked        int `json:"relinked"`

	LastError string `json:"last_error,omitempty"`

	// Cached is the coverage the local mirror has right now, per entity kind. It is
	// meaningful between passes too, which is why it is not reset with the counters.
	Cached map[string]int `json:"cached,omitempty"`
}

// Result is what a pass learned, handed to whoever is allowed to act on it.
type Result struct {
	Checked int
	Fetched int
	Fresh   int
	Errors  int

	// ChangedReleases are the release MBIDs whose payload differs from the copy the
	// cache held. The scan re-tags the files that use them; nothing in this package
	// does.
	ChangedReleases []string
	GoneReleases    int
	Relinked        int
}

// Scope is what one pass covers. Every entry is a list of MBIDs, so narrowing to a
// release-group later needs a constructor rather than new machinery.
//
// Force ignores the cache TTL. It is what a manual refresh means: a user who
// suspects a release is wrong is not helped by "it was checked recently, come back
// in a week". Scheduled passes leave it false and re-fetch only what has expired.
type Scope struct {
	Title    string
	Artists  []string // artist MBIDs — the entity and their discography
	Groups   []string // release-group MBIDs — the edition list
	Releases []string // release MBIDs — the full payload
	Force    bool
	Detail   map[string]any

	// Cold marks releases the collection does not own, which get a longer TTL after
	// they are read (see coldReleaseTTL).
	Cold map[string]bool
}

func (s Scope) size() int { return len(s.Artists)*2 + len(s.Groups) + len(s.Releases) }

// CollectionScope covers everything the collection refers to. This is the
// scheduled pass, and — with force — the "re-read every ID now" sweep.
//
// force ignores every cached copy. It is the same modifier ArtistScope always
// applies, at collection scope: there is no separate "verify identities" verb,
// because detecting a merge is not an activity of its own. Merges and deletions
// are recorded on the HTTP path by whatever fetch happens to see them, so any
// refresh that actually goes to the network finds them.
func CollectionScope(db *gorm.DB, force bool) (Scope, error) {
	scope := Scope{Title: "Metadata refresh", Force: force}
	if force {
		scope.Title = "Full metadata refresh"
	}
	if db == nil {
		return scope, nil
	}

	// The release set comes from collection.AllMBIDs, which unions the file index
	// with the owned-editions table. Reading only the editions table is what made a
	// collection refresh skip releases that files on disk actually point at.
	releaseIDs, artistIDs, err := collection.AllMBIDs(db)
	if err != nil {
		return scope, err
	}
	scope.Artists = artistIDs
	scope.Releases = releaseIDs

	var groups []models.CollectionReleaseGroup
	if err := db.Select("mb_id").Find(&groups).Error; err != nil {
		return scope, err
	}
	for _, g := range groups {
		if g.MBID != "" {
			scope.Groups = append(scope.Groups, g.MBID)
		}
	}

	// Tiering only applies to an unforced pass: forcing is the user saying they do
	// not trust any cached copy, and honouring a long TTL then would be answering a
	// different question.
	if !force {
		var owned []models.CollectionRelease
		if err := db.Select("mb_id", "owned_tracks").Find(&owned).Error; err != nil {
			return scope, err
		}
		scope.Cold = map[string]bool{}
		for _, rel := range owned {
			if rel.MBID != "" && rel.OwnedTracks == 0 {
				scope.Cold[rel.MBID] = true
			}
		}
	}
	return scope, nil
}

// ArtistScope covers one artist: who they are, their discography, the editions of
// every release-group of theirs, and every release of theirs the collection holds.
//
// The edition lists are the reason this exists as a scope rather than as a
// bespoke function. The old per-artist refresh skipped them, so opening an album
// after refreshing its artist still blocked on the rate limiter — the exact stall
// the refresh was pressed to avoid.
func ArtistScope(db *gorm.DB, artistMBID string) (Scope, error) {
	var artist models.CollectionArtist
	if err := db.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		return Scope{}, err
	}

	scope := Scope{
		Title:   "Metadata refresh for " + artist.Name,
		Artists: []string{artistMBID},
		Force:   true,
		Detail:  map[string]any{"artist": artist.Name, "artist_mb_id": artistMBID},
	}

	var groups []models.CollectionReleaseGroup
	if err := db.Select("mb_id").Where("artist_mb_id = ?", artistMBID).Find(&groups).Error; err != nil {
		return scope, err
	}
	for _, g := range groups {
		if g.MBID != "" {
			scope.Groups = append(scope.Groups, g.MBID)
		}
	}

	var releases []models.CollectionRelease
	if err := db.Select("mb_id").Where("artist_mb_id = ?", artistMBID).Find(&releases).Error; err != nil {
		return scope, err
	}
	for _, rel := range releases {
		if rel.MBID != "" {
			scope.Releases = append(scope.Releases, rel.MBID)
		}
	}
	return scope, nil
}

// LibraryScope covers everything one library's files point at: their releases, the
// release-groups and artists those belong to, and each artist's discography.
//
// Derived from the file index rather than from the collection tables, because a
// library is a set of *files* — the collection is aggregated across libraries and
// cannot say which of them a release came from. Forced, like ArtistScope: asking for
// one library by hand is asking for it to be checked now.
func LibraryScope(db *gorm.DB, libraryID uuid.UUID) (Scope, error) {
	var library models.Library
	if err := db.First(&library, "id = ?", libraryID).Error; err != nil {
		return Scope{}, err
	}

	scope := Scope{
		Title:  "Metadata refresh for " + library.Name,
		Force:  true,
		Detail: map[string]any{"library": library.Name, "library_id": libraryID.String()},
	}

	var releaseIDs []string
	if err := db.Model(&models.LibraryItem{}).
		Where("library_id = ? AND mb_release_id <> ''", libraryID).
		Distinct().Pluck("mb_release_id", &releaseIDs).Error; err != nil {
		return scope, err
	}
	if len(releaseIDs) == 0 {
		return scope, nil
	}
	scope.Releases = releaseIDs

	// The groups and artists behind those releases come from the collection's own
	// edition rows, which is the only place the release -> group -> artist chain is
	// recorded without re-reading MusicBrainz to find out.
	var editions []models.CollectionRelease
	if err := db.Select("mb_id", "release_group_mb_id", "artist_mb_id").
		Where("mb_id IN ?", releaseIDs).Find(&editions).Error; err != nil {
		return scope, err
	}
	groups := map[string]bool{}
	artists := map[string]bool{}
	for _, e := range editions {
		if e.ReleaseGroupMBID != "" {
			groups[e.ReleaseGroupMBID] = true
		}
		if e.ArtistMBID != "" {
			artists[e.ArtistMBID] = true
		}
	}
	for id := range groups {
		scope.Groups = append(scope.Groups, id)
	}
	for id := range artists {
		scope.Artists = append(scope.Artists, id)
	}
	return scope, nil
}

// DueScope covers only cached releases whose TTL has elapsed. It is what a scan
// runs as its refresh stage: the cheap, incremental version that keeps a scheduled
// scan from re-reading the whole collection every night.
func DueScope(releases []string) Scope {
	return Scope{Title: "Metadata refresh", Releases: releases}
}

// Runner owns refresh execution and status. One instance is shared by the cron
// job, the API, and the scan's refresh stage.
type Runner struct {
	db *gorm.DB

	// yieldTo reports whether file-writing work is running. May be nil, in which
	// case a pass never yields.
	yieldTo func() bool

	running atomic.Bool // drops overlapping passes
	jobMu   sync.Mutex  // serializes pass bodies

	statusMu sync.Mutex
	summary  Summary

	cancelMu sync.Mutex
	cancel   context.CancelFunc
}

// NewRunner builds a refresh runner. yieldTo may be nil.
func NewRunner(db *gorm.DB, yieldTo func() bool) *Runner {
	return &Runner{db: db, yieldTo: yieldTo}
}

// Status returns a copy of the current/last summary, with live cache-coverage
// counts folded in.
func (r *Runner) Status() Summary {
	r.statusMu.Lock()
	summary := r.summary
	r.statusMu.Unlock()

	cached := modules.MusicbrainzEntityCounts()
	cached["release"] = modules.MusicbrainzReleaseCacheSize()
	summary.Cached = cached
	return summary
}

// Running reports whether a pass is in progress.
func (r *Runner) Running() bool { return r.running.Load() }

// Cancel stops a running pass at the next entity boundary.
func (r *Runner) Cancel() {
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

// ErrAlreadyRunning is returned when a pass is asked for while one is in flight.
var ErrAlreadyRunning = fmt.Errorf("a metadata refresh is already running")

// RunCollection refreshes everything. force ignores every cached copy, which is
// what the "re-read every ID now" action asks for; the cron job leaves it off.
func (r *Runner) RunCollection(ctx context.Context, force bool) error {
	scope, err := CollectionScope(r.db, force)
	if err != nil {
		return err
	}
	_, err = r.Run(ctx, scope)
	return err
}

// Run executes a scope and blocks until it finishes. Overlapping calls are dropped
// rather than queued: a pass is idempotent and skips what is fresh, so a second
// would only compete for the same rate-limit budget.
func (r *Runner) Run(ctx context.Context, scope Scope) (Result, error) {
	if !r.running.CompareAndSwap(false, true) {
		logger.Log.Info("metadata refresh already running, dropping this trigger")
		return Result{}, ErrAlreadyRunning
	}
	defer r.running.Store(false)
	return r.run(ctx, scope)
}

// Start begins a pass in the background and returns as soon as it is under way.
// The guard is taken synchronously, before the goroutine starts, so an API caller
// gets a truthful "already running" instead of racing the pass it just asked for.
func (r *Runner) Start(ctx context.Context, scope Scope) error {
	if !r.running.CompareAndSwap(false, true) {
		return ErrAlreadyRunning
	}
	go func() {
		defer r.running.Store(false)
		if _, err := r.run(ctx, scope); err != nil {
			logger.Log.Warnf("metadata refresh failed: %s", err.Error())
		}
	}()
	return nil
}

// RunInline executes a scope without taking the pass guard or recording its own
// Activity event. It is for the scan's refresh stage, which already holds the
// file-writing guard and reports under its own event — taking the refresh guard
// there would also mean a scan could be blocked by a scheduled pass it is
// perfectly able to run alongside.
func (r *Runner) RunInline(ctx context.Context, scope Scope) Result {
	res, _ := r.execute(ctx, scope, false)
	return res
}

func (r *Runner) run(ctx context.Context, scope Scope) (Result, error) {
	r.jobMu.Lock()
	defer r.jobMu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	r.cancelMu.Lock()
	r.cancel = cancel
	r.cancelMu.Unlock()
	defer func() {
		cancel()
		r.cancelMu.Lock()
		r.cancel = nil
		r.cancelMu.Unlock()
	}()

	started := time.Now()
	r.setStatus(func(s *Summary) {
		*s = Summary{Running: true, StartedAt: &started, Phase: PhaseArtists, Title: scope.Title, Total: scope.size()}
	})

	event := events.Begin(r.db, models.EventTypeMirror, scope.Title)
	res, cancelled := r.execute(ctx, scope, true)
	r.finish(event, started, scope, res, cancelled)
	return res, nil
}

// execute is the pass body. track says whether to publish progress into the shared
// Summary — the scan's inline stage does not, because the scan is reporting its own.
func (r *Runner) execute(ctx context.Context, scope Scope, track bool) (Result, bool) {
	res := Result{}

	type unit struct {
		entity string
		mbid   string
		phase  string
	}
	units := make([]unit, 0, scope.size())
	for _, id := range scope.Artists {
		units = append(units, unit{models.MBEntityArtist, id, PhaseArtists})
	}
	// Identity before catalogs before the heavy per-release payloads, so a pass cut
	// short still leaves a mirror that can name what it holds rather than one
	// sitting on release payloads for artists it cannot label.
	for _, id := range scope.Artists {
		units = append(units, unit{models.MBEntityDiscography, id, PhaseDiscographies})
	}
	for _, id := range scope.Groups {
		units = append(units, unit{models.MBEntityEditions, id, PhaseEditions})
	}
	for _, id := range scope.Releases {
		units = append(units, unit{entityRelease, id, PhaseReleases})
	}

	for _, u := range units {
		if err := r.waitForTurn(ctx, track); err != nil {
			return res, true
		}
		if track {
			r.setStatus(func(s *Summary) { s.Phase = u.phase })
		}
		r.refreshOne(u.entity, u.mbid, scope.Force, scope.Cold[u.mbid], &res, track)
		if track {
			r.setStatus(func(s *Summary) { s.Done++ })
		}
	}

	// After the fetches, not during: anything above may have detected a redirect,
	// and the queue is drained once for the whole run.
	r.applyMigrations()
	return res, false
}

// entityRelease is the cache namespace for a full release payload. It is not one of
// the models.MBEntity* constants because releases have their own table, but a pass
// treats it as a fourth kind.
const entityRelease = "release"

func (r *Runner) refreshOne(entity, mbid string, force, cold bool, res *Result, track bool) {
	res.Checked++

	if !force && r.fresh(entity, mbid) {
		res.Fresh++
		if track {
			r.setStatus(func(s *Summary) { s.Fresh++ })
		}
		return
	}

	// Forcing has to reach past the *lookup's own* cache check, not just this one:
	// GetMusicBrainzArtist and friends consult the cache themselves, so skipping the
	// gate above is not enough to make a request happen. Expiring rather than
	// deleting keeps the stale copy as a fallback if the refetch then fails.
	if force {
		switch entity {
		case models.MBEntityArtist, models.MBEntityDiscography, models.MBEntityEditions:
			modules.MusicbrainzExpireEntity(entity, mbid)
		}
	}

	var err error
	switch entity {
	case models.MBEntityArtist:
		_, err = modules.GetMusicBrainzArtist(mbid)
	case models.MBEntityDiscography:
		_, err = modules.GetArtistDiscography(mbid)
	case models.MBEntityEditions:
		_, err = modules.GetMusicBrainzReleaseGroupReleases(mbid)
	case entityRelease:
		err = r.refreshRelease(mbid, res, track)
		if err == nil && cold {
			modules.MusicbrainzExtendExpiry(mbid, coldReleaseTTL)
		}
	}

	if _, _, gone := modules.GoneEntity(err); gone {
		// Gone is what a refresh is partly there to discover, not a failure to
		// report. The migration row was recorded at the point of the 404.
		logger.Log.Infof("%s no longer exists upstream, queued as a migration: %s", entity, mbid)
		res.Fetched++
		if track {
			r.setStatus(func(s *Summary) { s.Fetched++ })
		}
		return
	}
	if err != nil {
		message := fmt.Sprintf("%s %s: %s", entity, mbid, err.Error())
		logger.Log.Warnf("metadata refresh failed for %s", message)
		res.Errors++
		if track {
			r.setStatus(func(s *Summary) {
				s.Errors++
				s.LastError = message
			})
		}
		return
	}

	res.Fetched++
	if track {
		r.setStatus(func(s *Summary) { s.Fetched++ })
	}
}

// refreshRelease force-fetches one release and records what changed about it.
//
// Releases go through the drift-aware fetch rather than the cache-aware one
// because a content hash cannot be compared against a copy that was never re-read.
// The *result* of that comparison is reported, not acted on — re-tagging the files
// of a changed release is the scan's job.
func (r *Runner) refreshRelease(mbID string, res *Result, track bool) error {
	fresh, changed, err := modules.RefreshMusicBrainzRelease(mbID)
	if err != nil {
		// A release that is *gone* is an answer, not a failure, and must not be
		// retried on every subsequent pass. The migration row was recorded at the
		// point of the 404.
		if _, _, gone := modules.GoneEntity(err); gone {
			res.GoneReleases++
			if track {
				r.setStatus(func(s *Summary) { s.GoneReleases++ })
			}
			logger.Log.Infof("release no longer exists upstream, queued as a migration: %s", mbID)
			return nil
		}
		return err
	}

	// A release can move between release-groups without any of its own content
	// changing, so this is checked outside the changed-gate. Re-linking rewrites a
	// row, not a file, so it belongs to the refresh verb.
	if r.db != nil {
		if relinked, relinkErr := migration.RelinkRelease(r.db, mbID, fresh.ReleaseGroup.ID); relinkErr != nil {
			logger.Log.Warnf("failed to re-link release %s: %s", mbID, relinkErr.Error())
		} else if relinked {
			res.Relinked++
			if track {
				r.setStatus(func(s *Summary) { s.Relinked++ })
			}
		}
	}

	if changed {
		res.ChangedReleases = append(res.ChangedReleases, mbID)
		if track {
			r.setStatus(func(s *Summary) { s.ChangedReleases++ })
		}
		logger.Log.Infof("release changed upstream: %s", mbID)
	}
	return nil
}

func (r *Runner) fresh(entity, mbid string) bool {
	if entity == entityRelease {
		return modules.MusicbrainzReleaseFresh(mbid)
	}
	return modules.MusicbrainzEntityFresh(entity, mbid)
}

// applyMigrations drains the pending MusicBrainz migration queue at a pass
// boundary, the same way a scan does.
func (r *Runner) applyMigrations() {
	if r.db == nil {
		return
	}
	if _, err := migration.ProcessPending(r.db, migration.PolicyFromConfig(files.ConfigFile)); err != nil {
		logger.Log.Warnf("failed to process MusicBrainz migrations: %s", err.Error())
	}
}

// waitForTurn blocks while file-writing work holds the rate-limit budget, and
// returns an error once the context is cancelled. Polling rather than signalling:
// the thing being waited on is another package's boolean, and a poll every fifteen
// seconds is free next to the one-request-per-second work it is pacing.
func (r *Runner) waitForTurn(ctx context.Context, track bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.yieldTo == nil || !r.yieldTo() {
		return nil
	}

	if track {
		r.setStatus(func(s *Summary) { s.Phase = PhasePaused })
	}
	logger.Log.Debug("metadata refresh yielding to file-writing work")

	ticker := time.NewTicker(yieldPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !r.yieldTo() {
				return nil
			}
		}
	}
}

func (r *Runner) setStatus(mutate func(*Summary)) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	mutate(&r.summary)
}

// finish closes out a pass, recording the Activity event and the final summary.
func (r *Runner) finish(ev *models.Event, started time.Time, scope Scope, res Result, cancelled bool) {
	finished := time.Now()

	r.statusMu.Lock()
	r.summary.Running = false
	r.summary.Phase = PhaseIdle
	r.summary.FinishedAt = &finished
	summary := r.summary
	r.statusMu.Unlock()

	line := summaryLine(res, finished.Sub(started))
	if cancelled {
		line = "stopped early — " + line
	}

	details := map[string]any{
		"total":            summary.Total,
		"done":             summary.Done,
		"fetched":          res.Fetched,
		"fresh":            res.Fresh,
		"errors":           res.Errors,
		"changed_releases": len(res.ChangedReleases),
		"gone_releases":    res.GoneReleases,
		"relinked":         res.Relinked,
		"cancelled":        cancelled,
		"duration":         finished.Sub(started).String(),
	}
	if summary.LastError != "" {
		details["last_error"] = summary.LastError
	}
	for k, v := range scope.Detail {
		details[k] = v
	}

	// Errors are per-entity and the pass carried on regardless, so a pass that hit
	// some is still an "ok" outcome with a count — not a failed job.
	events.Finish(r.db, ev, models.EventStatusOK, line, details)
	events.Prune(r.db, eventRetention)

	// Releases persist per row as they are fetched, but the Lidarr and Plex JSON
	// caches still batch — flush so a pass that ran for hours is not lost to a
	// restart minutes later.
	modules.FlushCaches()
}

func summaryLine(res Result, took time.Duration) string {
	line := fmt.Sprintf("%d entities — %d fetched, %d already cached", res.Checked, res.Fetched, res.Fresh)
	if len(res.ChangedReleases) > 0 {
		line += fmt.Sprintf(", %d release(s) changed upstream", len(res.ChangedReleases))
	}
	if res.Errors > 0 {
		line += fmt.Sprintf(", %d failed", res.Errors)
	}
	return line + fmt.Sprintf(" (%s)", took.Round(time.Second))
}
