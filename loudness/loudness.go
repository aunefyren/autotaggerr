// Package loudness is the **analyse loudness** verb: measuring each album's ReplayGain
// with rsgain and storing it in track_loudnesses (docs/replaygain.md).
//
// It is tempo's design (package tempo), with one difference that shapes the rest: the
// unit is the **album**, not the file. Album gain is a property of an album's files
// together, so they are measured together, and a file added to or gone from an album
// makes every member's album gain stale. A file's row records the album it was measured
// in and a hash of that album's members; an album is redone when any member's audio
// changed or its membership did.
//
// As with tempo: it stores, tagging writes — the REPLAYGAIN_* tags reach a file through
// the next tagging pass, under the library's ReplayGain setting. It has its own runner,
// pauses while file-writing work runs, and resumes by skipping what is current.
package loudness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// yieldPollInterval is how often a paused pass asks whether file work has finished.
const yieldPollInterval = 5 * time.Second

// ErrNothingOptedIn is a press of the button with no library to analyse.
var ErrNothingOptedIn = errors.New("no enabled library is opted in to loudness analysis — turn it on under Libraries → edit")

// ErrAnalyserMissing is a press of the button on a deployment without rsgain.
var ErrAnalyserMissing = errors.New("rsgain is not installed, so loudness cannot be analysed — install rsgain (the Docker image has it)")

// outcome is what one album's visit did.
type outcome int

const (
	outcomeFresh     outcome = iota // every member current; nothing read
	outcomeRestamped                // some members changed only in their tags
	outcomeAnalysed                 // rsgain measured the album
	outcomeFailed                   // rsgain or a member failed
	outcomeGone                     // no member is on disk any more
	outcomeCancelled
)

// Summary is the status of the current or most recent pass. Counts are albums,
// except Files.
type Summary struct {
	Running    bool       `json:"running"`
	Paused     bool       `json:"paused"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	Total int `json:"total"`
	Done  int `json:"done"`
	Files int `json:"files"`

	Analysed  int `json:"analysed"`
	Fresh     int `json:"fresh"`
	Restamped int `json:"restamped"`
	Failed    int `json:"failed"`

	LastError string `json:"last_error,omitempty"`

	// Read live on every Status call, as tempo's are: why a pass would do nothing.
	AnalyserAvailable bool `json:"analyser_available"`
	Libraries         int  `json:"libraries"`
	// Stored is how many files have a loudness on record.
	Stored int64 `json:"stored"`
}

// album is one unit of measurement: an album's files in one library.
type album struct {
	key     string
	label   string
	members []string // sorted paths
}

// Runner owns loudness analysis. One instance is shared by the cron job and the API.
type Runner struct {
	db      *gorm.DB
	yieldTo func() bool

	// Seams, defaulted in newRunner, so a test can run a pass without rsgain.
	analyse   func(ctx context.Context, paths []string) (modules.AlbumLoudness, error)
	hash      func(path string) (string, error)
	available func() bool
	pollEvery time.Duration

	queueMu sync.Mutex
	queued  bool
	wake    chan struct{}

	running atomic.Bool

	statusMu sync.Mutex
	summary  Summary

	cancelMu sync.Mutex
	cancel   context.CancelFunc

	eventRetention  int
	detailRetention int
}

// NewRunner builds a loudness runner and starts its worker. yieldTo may be nil.
func NewRunner(db *gorm.DB, yieldTo func() bool, cfg models.ConfigStruct) *Runner {
	r := newRunner(db, yieldTo, cfg)
	go r.worker()
	return r
}

func newRunner(db *gorm.DB, yieldTo func() bool, cfg models.ConfigStruct) *Runner {
	return &Runner{
		db:              db,
		yieldTo:         yieldTo,
		analyse:         modules.AnalyseAlbumLoudness,
		hash:            modules.AudioPayloadHash,
		available:       modules.RsgainAvailable,
		pollEvery:       yieldPollInterval,
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

// Check reports why a pass would do nothing, or nil when it has work to do.
func (r *Runner) Check() error {
	if !r.available() {
		return ErrAnalyserMissing
	}
	count, err := optedInLibraries(r.db)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNothingOptedIn
	}
	return nil
}

// AnalyseAll queues a pass. Requests while one is queued collapse into it.
func (r *Runner) AnalyseAll() {
	if r == nil {
		return
	}
	r.queueMu.Lock()
	r.queued = true
	r.queueMu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Scheduled is the cron entry point: AnalyseAll when there is something to do.
func (r *Runner) Scheduled() {
	if r == nil || r.Check() != nil {
		return
	}
	r.AnalyseAll()
}

// Cancel stops a running pass. Albums already measured keep their results.
func (r *Runner) Cancel() {
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

// Running reports whether a pass is in progress.
func (r *Runner) Running() bool { return r.running.Load() }

// Status returns the current/last pass with the live availability facts folded in.
func (r *Runner) Status() Summary {
	r.statusMu.Lock()
	summary := r.summary
	r.statusMu.Unlock()

	summary.AnalyserAvailable = r.available()
	if count, err := optedInLibraries(r.db); err == nil {
		summary.Libraries = int(count)
	}
	if r.db != nil {
		r.db.Model(&models.TrackLoudness{}).Where("error = ''").Count(&summary.Stored)
	}
	return summary
}

// Progress reads the live counters for the event-progress flusher.
func (r *Runner) Progress() events.Progress {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	phase := ""
	if r.summary.Paused {
		phase = "paused while files are written"
	}
	return events.Progress{Total: r.summary.Total, Done: r.summary.Done, Phase: phase}
}

func (r *Runner) setStatus(mutate func(*Summary)) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	mutate(&r.summary)
}

func (r *Runner) worker() {
	for range r.wake {
		for r.takeQueued() {
			r.run()
		}
	}
}

func (r *Runner) takeQueued() bool {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	queued := r.queued
	r.queued = false
	return queued
}

func optedInLibraries(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, nil
	}
	var count int64
	err := db.Model(&models.Library{}).Where("enabled = ? AND analyse_loudness = ?", true, true).Count(&count).Error
	return count, err
}

// albums groups the indexed audio files of opted-in libraries into albums: one per
// library and release. A file with no release identified yet is grouped with the
// others in its folder, which is what an album is on disk until something names it.
func albums(db *gorm.DB) ([]album, error) {
	var rows []struct {
		LibraryID   string
		Path        string
		MBReleaseID string
	}
	err := db.Model(&models.LibraryItem{}).
		Joins("JOIN libraries ON libraries.id = library_items.library_id").
		Where("libraries.enabled = ? AND libraries.analyse_loudness = ?", true, true).
		Select("library_items.library_id AS library_id, library_items.path AS path, library_items.mb_release_id AS mb_release_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	byKey := map[string]*album{}
	for _, row := range rows {
		if !modules.IsSupportedAudioFile(row.Path) {
			continue
		}
		key, label := row.LibraryID+"|release:"+row.MBReleaseID, row.MBReleaseID
		if row.MBReleaseID == "" {
			dir := filepath.Dir(row.Path)
			key, label = row.LibraryID+"|folder:"+dir, dir
		}
		a, ok := byKey[key]
		if !ok {
			a = &album{key: key, label: label}
			byKey[key] = a
		}
		a.members = append(a.members, row.Path)
	}
	out := make([]album, 0, len(byKey))
	for _, a := range byKey {
		sort.Strings(a.members)
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].members[0] < out[j].members[0] })
	return out, nil
}

// membersHash identifies an album's membership at measurement time.
func membersHash(paths []string) string {
	sum := sha256.Sum256([]byte(strings.Join(paths, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// result is what one pass did.
type result struct {
	analysed, fresh, restamped, failed, gone, files int
	lastError                                       string
	items                                           []models.EventItem
	itemsTotal                                      int
	limit                                           int
}

func (res *result) add(a album, out outcome, err error) {
	switch out {
	case outcomeFresh:
		res.fresh++
	case outcomeRestamped:
		res.restamped++
	case outcomeAnalysed:
		res.analysed++
	case outcomeGone:
		res.gone++
	case outcomeFailed:
		res.failed++
		message := ""
		if err != nil {
			message = err.Error()
		}
		res.lastError = message
		res.itemsTotal++
		if len(res.items) < res.limit {
			res.items = append(res.items, models.EventItem{Path: a.members[0], Status: models.EventItemStatusError, Error: message})
		}
	}
}

// run executes one pass over everything opted in. Albums are measured one at a time:
// rsgain is single-threaded per album, and a pass pauses for file work between albums.
func (r *Runner) run() {
	if r.Check() != nil {
		return
	}
	list, err := albums(r.db)
	if err != nil {
		logger.Log.Warnf("could not enumerate albums for loudness analysis: %s", err.Error())
		return
	}

	r.running.Store(true)
	defer r.running.Store(false)

	ctx, cancel := context.WithCancel(context.Background())
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
		*s = Summary{Running: true, StartedAt: &started, Total: len(list)}
	})

	ev := events.Begin(r.db, models.EventTypeLoudness, "Loudness analysis")
	stopProgress := events.StartProgress(r.db, ev, r.Progress)

	res := &result{limit: r.detailRetention}
	cancelled := false
	for _, a := range list {
		if err := r.waitForTurn(ctx); err != nil {
			cancelled = true
			break
		}
		out, err := r.analyseAlbum(ctx, a)
		if out == outcomeCancelled {
			cancelled = true
			break
		}
		res.add(a, out, err)
		res.files += len(a.members)
		r.setStatus(func(s *Summary) {
			s.Done++
			s.Files += len(a.members)
			switch out {
			case outcomeAnalysed:
				s.Analysed++
			case outcomeFresh:
				s.Fresh++
			case outcomeRestamped:
				s.Restamped++
			case outcomeFailed:
				s.Failed++
				if err != nil {
					s.LastError = err.Error()
				}
			}
		})
	}
	if !cancelled {
		r.pruneOrphans()
	}

	stopProgress()
	finished := time.Now()
	r.setStatus(func(s *Summary) {
		s.Running = false
		s.Paused = false
		s.FinishedAt = &finished
	})
	r.record(ev, finished.Sub(started), res, cancelled, len(list))
}

// waitForTurn blocks while file-writing work runs, and returns an error once the pass
// is cancelled.
func (r *Runner) waitForTurn(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.yieldTo == nil || !r.yieldTo() {
		return nil
	}
	r.setStatus(func(s *Summary) { s.Paused = true })
	defer r.setStatus(func(s *Summary) { s.Paused = false })
	logger.Log.Debug("loudness analysis pausing while files are written")

	ticker := time.NewTicker(r.pollEvery)
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

// memberState is one member file as it is on disk now.
type memberState struct {
	path    string
	size    int64
	modTime time.Time
}

// analyseAlbum decides what an album needs, cheapest test first, and measures it only
// when something changed:
//
//  1. every member has a current row for this album and membership, and its size and
//     mtime match → fresh, nothing read;
//  2. the members that differ in size or mtime still hash to the stored audio → their
//     tags changed, not their audio: re-stamp them;
//  3. otherwise measure the whole album — new audio, a member added or gone, or a row
//     from another analyser version.
//
// A failed album is not retried until it changes, by the same rule: its rows record the
// failure against this membership and these files.
func (r *Runner) analyseAlbum(ctx context.Context, a album) (outcome, error) {
	var present []memberState
	for _, path := range a.members {
		info, err := os.Stat(path)
		if err != nil {
			// Gone: the scan prunes the index; measuring without it is the album now.
			continue
		}
		present = append(present, memberState{path: path, size: info.Size(), modTime: info.ModTime()})
	}
	if len(present) == 0 {
		return outcomeGone, nil
	}
	paths := make([]string, len(present))
	for i, m := range present {
		paths[i] = m.path
	}
	members := membersHash(paths)
	version := modules.LoudnessAnalyserVersion()

	var stored []models.TrackLoudness
	if err := r.db.Where("path IN ?", paths).Find(&stored).Error; err != nil {
		return outcomeFailed, fmt.Errorf("could not read stored loudness: %w", err)
	}
	byPath := make(map[string]models.TrackLoudness, len(stored))
	for _, row := range stored {
		byPath[row.Path] = row
	}

	var restamp []memberState
	current := true
	for _, m := range present {
		row, ok := byPath[m.path]
		if !ok || row.Album != a.key || row.AlbumMembers != members || row.Analyser != modules.LoudnessAnalyser || row.AnalyserVersion != version {
			current = false
			break
		}
		if row.Size == m.size && row.ModTime != nil && row.ModTime.Equal(m.modTime) {
			continue
		}
		hash, err := r.hash(m.path)
		if err != nil || hash == "" || hash != row.AudioHash {
			current = false
			break
		}
		restamp = append(restamp, m)
	}
	if current {
		if len(restamp) == 0 {
			// Includes an album that failed before and has not changed since: not retried.
			return outcomeFresh, nil
		}
		for _, m := range restamp {
			if err := r.db.Model(&models.TrackLoudness{}).Where("path = ?", m.path).
				Updates(map[string]any{"size": m.size, "mod_time": m.modTime}).Error; err != nil {
				return outcomeFailed, fmt.Errorf("could not update the stored loudness: %w", err)
			}
		}
		return outcomeRestamped, nil
	}

	measured, analyseErr := r.analyse(ctx, paths)
	if ctx.Err() != nil {
		return outcomeCancelled, nil
	}

	now := time.Now()
	for i, m := range present {
		hash, err := r.hash(m.path)
		if err != nil {
			hash = ""
		}
		modTime := m.modTime
		row := models.TrackLoudness{
			Path:            m.path,
			Size:            m.size,
			ModTime:         &modTime,
			AudioHash:       hash,
			Album:           a.key,
			AlbumMembers:    members,
			Analyser:        modules.LoudnessAnalyser,
			AnalyserVersion: version,
			AnalysedAt:      now,
		}
		if analyseErr != nil {
			row.Error = analyseErr.Error()
		} else {
			track := measured.Tracks[i]
			row.Loudness, row.TrackGain, row.TrackPeak = track.Loudness, track.Gain, track.Peak
			row.AlbumGain, row.AlbumPeak = measured.Album.Gain, measured.Album.Peak
		}
		if err := r.db.Save(&row).Error; err != nil {
			return outcomeFailed, fmt.Errorf("could not store the loudness: %w", err)
		}
	}
	if analyseErr != nil {
		return outcomeFailed, analyseErr
	}
	return outcomeAnalysed, nil
}

// pruneOrphans drops stored results whose file has left the index, after a completed
// pass.
func (r *Runner) pruneOrphans() {
	err := r.db.Where("path NOT IN (?)", r.db.Model(&models.LibraryItem{}).Select("path")).
		Delete(&models.TrackLoudness{}).Error
	if err != nil {
		logger.Log.Warnf("could not prune stored loudness for files that are gone: %s", err.Error())
	}
}

// record writes a pass's outcome onto its event.
func (r *Runner) record(ev *models.Event, took time.Duration, res *result, cancelled bool, total int) {
	if ev == nil {
		return
	}
	line := fmt.Sprintf("%d album(s), %d file(s) — %d measured, %d already measured", total, res.files, res.analysed, res.fresh+res.restamped)
	if res.failed > 0 {
		line += fmt.Sprintf(", %d failed", res.failed)
	}
	line += fmt.Sprintf(" (%s)", took.Round(time.Second))
	if cancelled {
		line = "stopped early — " + line
	}

	details := map[string]any{
		"albums":    total,
		"files":     res.files,
		"analysed":  res.analysed,
		"fresh":     res.fresh,
		"restamped": res.restamped,
		"failed":    res.failed,
		"gone":      res.gone,
		"cancelled": cancelled,
		"analyser":  modules.LoudnessAnalyser,
		"version":   modules.LoudnessAnalyserVersion(),
		"detail": map[string]any{
			"recorded": len(res.items),
			"total":    res.itemsTotal,
			"limit":    res.limit,
		},
	}
	if res.lastError != "" {
		details["last_error"] = res.lastError
	}

	ev.Stats = []models.EventStat{
		{Label: "Albums checked", Value: total},
		{Label: "Measured", Value: res.analysed, Kind: models.EventStatNotable},
		{Label: "Already measured", Value: res.fresh, Kind: models.EventStatMuted},
		// Same audio, new tags — almost always the ReplayGain tags themselves.
		{Label: "Tags changed, audio did not", Value: res.restamped, Kind: models.EventStatMuted},
		{Label: "Failed", Value: res.failed, Kind: models.EventStatBad, Filter: models.EventItemStatusError},
	}

	events.AddItems(r.db, ev, res.items)
	events.Finish(r.db, ev, models.EventStatusOK, line, details)
	events.Prune(r.db, r.eventRetention)
}
