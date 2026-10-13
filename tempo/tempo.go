// Package tempo is the **analyse tempo** verb: detecting each file's BPM from its
// audio and storing the result in track_tempos.
//
// # It stores; tagging writes
//
// Nothing here writes to an audio file. A stored tempo reaches the file's tags through
// the next tagging pass — *Process* or *Tag files* — under the rules of the library's
// tagger profile, the same way a release that changed upstream does. A verb about
// *reading* audio that could rewrite a library would be exactly the quiet cascade the
// four verbs were separated to remove (docs/scanning.md).
//
// # Why it has its own runner
//
// Analysis decodes every file it has not seen, which on a first pass over a large
// library is hours. On process.Runner's serial queue that would hold every scan and
// re-tag behind it; it spends nothing that queue exists to serialise (no MusicBrainz
// budget, no file writes). So it has its own worker, like artwork, and instead
// *pauses between files* while a file-writing job runs — the machine's CPU and disk go
// to the job a user is waiting on.
//
// # Resuming is skipping
//
// A pass keeps no cursor. Every result is written as it is produced, and a file whose
// stored result is still current is skipped, so a cancelled or crashed pass resumes on
// the next one at the cost of a stat per file it already finished.
package tempo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// yieldPollInterval is how often a paused pass asks whether file work has finished.
// Polling, as the metadata runner does: the thing waited on is another package's
// boolean, and a poll every few seconds is free next to a decode.
const yieldPollInterval = 5 * time.Second

// ErrNothingOptedIn is a press of the button with no library to analyse. It is a
// refusal with a reason (docs/scanning.md, "A verb that does nothing says why").
var ErrNothingOptedIn = errors.New("no enabled library is opted in to tempo analysis — turn it on under Libraries → edit")

// ErrDetectorMissing is a press of the button on a deployment without aubiotrack.
var ErrDetectorMissing = errors.New("aubiotrack is not installed, so tempo cannot be analysed — install aubio (aubio-tools on Debian/Ubuntu)")

// outcome is what one file's visit did.
type outcome int

const (
	outcomeFresh     outcome = iota // stored result still current; nothing read
	outcomeRestamped                // file changed, audio did not: identity updated
	outcomeCarried                  // same audio stored under another path: result copied
	outcomeAnalysed                 // detector ran and found a tempo
	outcomeNoTempo                  // detector ran and found too few beats
	outcomeFailed                   // detector or file failed
	outcomeGone                     // file no longer on disk; left to the scan to prune
	outcomeCancelled
)

// Summary is the status of the current or most recent pass.
type Summary struct {
	Running    bool       `json:"running"`
	Paused     bool       `json:"paused"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	Total int `json:"total"`
	Done  int `json:"done"`

	Analysed  int `json:"analysed"`
	Fresh     int `json:"fresh"`
	Restamped int `json:"restamped"`
	Carried   int `json:"carried"`
	NoTempo   int `json:"no_tempo"`
	Failed    int `json:"failed"`

	LastError string `json:"last_error,omitempty"`

	// DetectorAvailable and Libraries are read live on every Status call, so the UI
	// can say *why* a pass would do nothing — no binary, or nothing opted in — instead
	// of showing a row of zeroes for two opposite situations.
	DetectorAvailable bool `json:"detector_available"`
	Libraries         int  `json:"libraries"`
	// Stored is how many files have a tempo on record, meaningful between passes.
	Stored int64 `json:"stored"`
}

// Runner owns tempo analysis: its queue, its worker and its status. One instance is
// shared by the cron job and the API.
type Runner struct {
	db *gorm.DB

	// yieldTo reports whether file-writing work is running. May be nil (never yield).
	yieldTo func() bool

	// Seams, defaulted to the real implementations in newRunner so a test can run a
	// pass with no aubiotrack installed and no real audio.
	detect      func(ctx context.Context, path string) (modules.TempoAnalysis, error)
	hash        func(path string) (string, error)
	available   func() bool
	concurrency func() int
	pollEvery   time.Duration

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

// NewRunner builds a tempo runner and starts its worker. yieldTo may be nil.
func NewRunner(db *gorm.DB, yieldTo func() bool, cfg models.ConfigStruct) *Runner {
	r := newRunner(db, yieldTo, cfg)
	go r.worker()
	return r
}

// newRunner builds the runner without starting its worker, so a test can drive a pass
// synchronously through run.
func newRunner(db *gorm.DB, yieldTo func() bool, cfg models.ConfigStruct) *Runner {
	return &Runner{
		db:        db,
		yieldTo:   yieldTo,
		detect:    modules.DetectTempo,
		hash:      modules.AudioPayloadHash,
		available: modules.AubiotrackAvailable,
		// Read per pass rather than captured here, so a saved setting applies from the
		// next pass without a restart — the tier it is declared at.
		concurrency:     func() int { return files.ConfigFile.AutotaggerrTempoConcurrency },
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

// Check reports why a pass would do nothing, or nil when it has work to do. The API
// calls it before queueing so a press with nothing to analyse is answered rather than
// silently dropped.
func (r *Runner) Check() error {
	if !r.available() {
		return ErrDetectorMissing
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

// AnalyseAll queues a pass over every opted-in library. Requests while one is queued
// collapse into it; a request while one is running queues exactly one more, which
// picks up whatever arrived during the first.
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

// Scheduled is the cron entry point. It is AnalyseAll gated on there being something
// to do, so a deployment without the binary, or with no library opted in, records
// nothing every night — the absence of the binary is logged once, at first check.
func (r *Runner) Scheduled() {
	if r == nil || r.Check() != nil {
		return
	}
	r.AnalyseAll()
}

// Cancel stops a running pass. Files already analysed keep their results, so the next
// pass resumes where this one stopped.
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

	summary.DetectorAvailable = r.available()
	if count, err := optedInLibraries(r.db); err == nil {
		summary.Libraries = int(count)
	}
	if r.db != nil {
		r.db.Model(&models.TrackTempo{}).Where("bpm > 0").Count(&summary.Stored)
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

// optedInLibraries counts the enabled libraries that asked for analysis.
func optedInLibraries(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, nil
	}
	var count int64
	err := db.Model(&models.Library{}).Where("enabled = ? AND analyse_tempo = ?", true, true).Count(&count).Error
	return count, err
}

// targets is every indexed audio file in an opted-in library. The index rather than a
// directory walk: discovering files is Process's job, and a file the index does not
// hold has nowhere for its tags to come from anyway.
func targets(db *gorm.DB) ([]string, error) {
	var paths []string
	err := db.Model(&models.LibraryItem{}).
		Joins("JOIN libraries ON libraries.id = library_items.library_id").
		Where("libraries.enabled = ? AND libraries.analyse_tempo = ?", true, true).
		Order("library_items.path").
		Pluck("library_items.path", &paths).Error
	if err != nil {
		return nil, err
	}
	out := paths[:0]
	for _, path := range paths {
		if modules.IsSupportedAudioFile(path) {
			out = append(out, path)
		}
	}
	return out, nil
}

// result is what one pass did.
type result struct {
	mu sync.Mutex

	analysed, fresh, restamped, carried, noTempo, failed, gone int
	lastError                                                  string

	// items holds failures only. A row per analysed file would be thousands of rows
	// on a first pass saying the expected thing happened; the stored results are
	// where the numbers live.
	items      []models.EventItem
	itemsTotal int
	limit      int
}

func (res *result) add(path string, out outcome, err error) {
	res.mu.Lock()
	defer res.mu.Unlock()
	switch out {
	case outcomeFresh:
		res.fresh++
	case outcomeRestamped:
		res.restamped++
	case outcomeCarried:
		res.carried++
	case outcomeAnalysed:
		res.analysed++
	case outcomeNoTempo:
		res.noTempo++
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
			res.items = append(res.items, models.EventItem{
				Path:   path,
				Status: models.EventItemStatusError,
				Error:  message,
			})
		}
	}
}

// run executes one pass over everything opted in.
func (r *Runner) run() {
	if r.Check() != nil {
		return
	}
	paths, err := targets(r.db)
	if err != nil {
		logger.Log.Warnf("could not enumerate files for tempo analysis: %s", err.Error())
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
		*s = Summary{Running: true, StartedAt: &started, Total: len(paths)}
	})

	ev := events.Begin(r.db, models.EventTypeTempo, "Tempo analysis")
	stopProgress := events.StartProgress(r.db, ev, r.Progress)

	res := &result{limit: r.detailRetention}
	cancelled := r.execute(ctx, paths, res)
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
	r.record(ev, finished.Sub(started), res, cancelled, len(paths))
}

// execute visits every path with a bounded pool. It reports whether the pass was
// cancelled.
func (r *Runner) execute(ctx context.Context, paths []string, res *result) bool {
	workers := r.concurrency()
	if workers < 1 {
		workers = models.DefaultTempoConcurrency
	}

	work := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range work {
				out, err := r.analyseOne(ctx, path)
				if out == outcomeCancelled {
					continue
				}
				res.add(path, out, err)
				r.setStatus(func(s *Summary) {
					s.Done++
					switch out {
					case outcomeAnalysed:
						s.Analysed++
					case outcomeFresh:
						s.Fresh++
					case outcomeRestamped:
						s.Restamped++
					case outcomeCarried:
						s.Carried++
					case outcomeNoTempo:
						s.NoTempo++
					case outcomeFailed:
						s.Failed++
						if err != nil {
							s.LastError = err.Error()
						}
					}
				})
			}
		}()
	}

	cancelled := false
	for _, path := range paths {
		if err := r.waitForTurn(ctx); err != nil {
			cancelled = true
			break
		}
		select {
		case work <- path:
		case <-ctx.Done():
			cancelled = true
		}
		if cancelled {
			break
		}
	}
	close(work)
	wg.Wait()
	return cancelled || ctx.Err() != nil
}

// waitForTurn blocks while file-writing work runs, and returns an error once the pass
// is cancelled. Checked before each file is handed out, so a scan that starts mid-pass
// waits at most for the files already being decoded.
func (r *Runner) waitForTurn(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.yieldTo == nil || !r.yieldTo() {
		return nil
	}

	r.setStatus(func(s *Summary) { s.Paused = true })
	defer r.setStatus(func(s *Summary) { s.Paused = false })
	logger.Log.Debug("tempo analysis pausing while files are written")

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

// analyseOne decides what a file needs, cheapest test first:
//
//  1. size and mtime match the stored result → fresh, nothing read;
//  2. the audio hash matches the stored result → a tag write, not new audio: re-stamp;
//  3. the audio hash matches another path's result → a moved or copied file: carry it;
//  4. otherwise run the detector.
//
// A result from another detector version never satisfies 1–3: a changed analysis is
// what the version exists to invalidate.
func (r *Runner) analyseOne(ctx context.Context, path string) (outcome, error) {
	info, err := os.Stat(path)
	if err != nil {
		// Gone or unreadable. A missing file is the scan's to prune, and an index row
		// that outlives its file is not this verb's failure.
		if os.IsNotExist(err) {
			return outcomeGone, nil
		}
		return outcomeFailed, err
	}
	size, modTime := info.Size(), info.ModTime()
	version := modules.TempoDetectorVersion()

	var stored models.TrackTempo
	found := r.db.Where("path = ?", path).Limit(1).Find(&stored).RowsAffected > 0
	current := found && stored.Detector == modules.TempoDetector && stored.DetectorVersion == version
	if current && stored.Size == size && stored.ModTime != nil && stored.ModTime.Equal(modTime) {
		return outcomeFresh, nil
	}

	audioHash, hashErr := r.hash(path)
	if hashErr != nil {
		logger.Log.Debugf("could not hash the audio of %s: %s", path, hashErr.Error())
		audioHash = ""
	}

	if audioHash != "" {
		if current && stored.AudioHash == audioHash {
			err := r.db.Model(&models.TrackTempo{}).Where("path = ?", path).
				Updates(map[string]any{"size": size, "mod_time": modTime}).Error
			if err != nil {
				return outcomeFailed, fmt.Errorf("could not update the stored tempo: %w", err)
			}
			return outcomeRestamped, nil
		}

		var twin models.TrackTempo
		if r.db.Where("audio_hash = ? AND detector = ? AND detector_version = ? AND path <> ?",
			audioHash, modules.TempoDetector, version, path).Limit(1).Find(&twin).RowsAffected > 0 {
			twin.Path, twin.Size, twin.ModTime = path, size, &modTime
			if err := r.db.Save(&twin).Error; err != nil {
				return outcomeFailed, fmt.Errorf("could not store the tempo: %w", err)
			}
			return outcomeCarried, nil
		}
	}

	analysis, detectErr := r.detect(ctx, path)
	if ctx.Err() != nil {
		// Cancelled mid-decode: store nothing, so the next pass redoes this file
		// rather than trusting a result that was never produced.
		return outcomeCancelled, nil
	}

	row := models.TrackTempo{
		Path:            path,
		Size:            size,
		ModTime:         &modTime,
		AudioHash:       audioHash,
		BPM:             analysis.BPM,
		Confidence:      analysis.Confidence,
		Beats:           analysis.Beats,
		Detector:        modules.TempoDetector,
		DetectorVersion: version,
		AnalysedAt:      time.Now(),
	}
	out := outcomeAnalysed
	if detectErr != nil {
		row.BPM, row.Confidence = 0, 0
		row.Error = detectErr.Error()
		out = outcomeFailed
		if errors.Is(detectErr, modules.ErrTooFewBeats) {
			out = outcomeNoTempo
		}
	}
	if err := r.db.Save(&row).Error; err != nil {
		return outcomeFailed, fmt.Errorf("could not store the tempo: %w", err)
	}
	if out == outcomeFailed {
		return out, detectErr
	}
	return out, nil
}

// pruneOrphans drops stored results whose file has left the index. Run only after a
// pass that completed, and after it rather than before, so a file moved during the
// pass was already carried to its new path before its old row goes.
func (r *Runner) pruneOrphans() {
	err := r.db.Where("path NOT IN (?)", r.db.Model(&models.LibraryItem{}).Select("path")).
		Delete(&models.TrackTempo{}).Error
	if err != nil {
		logger.Log.Warnf("could not prune stored tempos for files that are gone: %s", err.Error())
	}
}

func summaryLine(res *result, total int, took time.Duration) string {
	line := fmt.Sprintf("%d file(s) — %d analysed, %d already analysed", total, res.analysed, res.fresh+res.restamped+res.carried)
	if res.noTempo > 0 {
		line += fmt.Sprintf(", %d with no measurable tempo", res.noTempo)
	}
	if res.failed > 0 {
		line += fmt.Sprintf(", %d failed", res.failed)
	}
	return line + fmt.Sprintf(" (%s)", took.Round(time.Second))
}

// record writes a pass's outcome onto its event.
func (r *Runner) record(ev *models.Event, took time.Duration, res *result, cancelled bool, total int) {
	if ev == nil {
		return
	}
	line := summaryLine(res, total, took)
	if cancelled {
		line = "stopped early — " + line
	}

	details := map[string]any{
		"total":      total,
		"analysed":   res.analysed,
		"fresh":      res.fresh,
		"restamped":  res.restamped,
		"carried":    res.carried,
		"no_tempo":   res.noTempo,
		"failed":     res.failed,
		"gone":       res.gone,
		"cancelled":  cancelled,
		"detector":   modules.TempoDetector,
		"version":    modules.TempoDetectorVersion(),
		"concurrent": r.concurrency(),
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
		{Label: "Files checked", Value: total},
		{Label: "Analysed", Value: res.analysed, Kind: models.EventStatNotable},
		{Label: "Already analysed", Value: res.fresh, Kind: models.EventStatMuted},
		// Same audio, new tags — almost always the BPM tag itself, written since the
		// last pass. Its own figure so a pass after a re-tag reads as cheap, not empty.
		{Label: "Tags changed, audio did not", Value: res.restamped, Kind: models.EventStatMuted},
		{Label: "Moved, result kept", Value: res.carried, Kind: models.EventStatMuted},
		{Label: "No measurable tempo", Value: res.noTempo, Kind: models.EventStatMuted},
		{Label: "Failed", Value: res.failed, Kind: models.EventStatBad, Filter: models.EventItemStatusError},
	}

	events.AddItems(r.db, ev, res.items)
	// Per-file failures do not fail the pass; it carried on regardless.
	events.Finish(r.db, ev, models.EventStatusOK, line, details)
	events.Prune(r.db, r.eventRetention)
}
