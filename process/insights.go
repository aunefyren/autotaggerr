package process

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/insights"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

// PhaseCheckDuplicates is the duplicate check's stage.
const PhaseCheckDuplicates = "check_duplicates"

// ErrNoDuplicatesToCheck is a press of *Check audio* with nothing to hash: no two
// files of one recording on one album in one format whose audio is not already known.
var ErrNoDuplicatesToCheck = errors.New("no duplicate has audio left to check")

// CheckDuplicates queues a pass hashing the audio of duplicate candidates (see
// insights.UncheckedCandidates), so the Insights page can say whether two copies
// are the same audio. It reads files and writes none, so it queues behind file work
// like a metadata job. The candidate list is read when the job runs, not here, so
// a scan that finished in between is reflected.
func (r *Runner) CheckDuplicates() error {
	candidates, err := insights.UncheckedCandidates(r.db)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return ErrNoDuplicatesToCheck
	}
	r.enqueue(job{jobCheckDuplicates, "check_duplicates", "Check duplicate audio", r.checkDuplicatesNow})
	return nil
}

// hashAudio is the hasher the duplicate check uses; a test swaps it.
var hashAudio insights.Hasher

func (r *Runner) checkDuplicatesNow() {
	candidates, err := insights.UncheckedCandidates(r.db)
	if err != nil {
		logger.Log.Warnf("failed to list duplicate candidates: %s", err.Error())
		return
	}
	if len(candidates) == 0 {
		return
	}

	event := events.Begin(r.db, models.EventTypeDuplicateCheck, "Check duplicate audio")
	r.progTotal.Store(int64(len(candidates)))
	r.setPhase(PhaseCheckDuplicates)
	stopProgress := events.StartProgress(r.db, event, r.progressSnapshot)

	res := insights.VerifyDuplicates(r.jobContext(), r.db, candidates, hashAudio, func(path string) {
		r.setCurrent(path)
		r.progDone.Add(1)
	})
	stopProgress()

	var items []models.EventItem
	for path, cause := range res.Failures {
		items = append(items, models.EventItem{Path: path, Status: models.EventItemStatusError, Error: cause.Error()})
	}
	parts := []string{fmt.Sprintf("%d %s", res.Hashed, plural(res.Hashed, "file hashed", "files hashed"))}
	if res.Reused > 0 {
		parts = append(parts, fmt.Sprintf("%d taken from analysis", res.Reused))
	}
	if res.Gone > 0 {
		parts = append(parts, fmt.Sprintf("%d no longer on disk", res.Gone))
	}
	if len(res.Failures) > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", len(res.Failures)))
	}
	summary := strings.Join(parts, " · ")
	if res.Cancelled {
		summary = "stopped early — " + summary
	}
	status := models.EventStatusOK
	if len(res.Failures) > 0 {
		status = models.EventStatusError
	}
	event.Stats = []models.EventStat{
		{Label: "Files hashed", Value: res.Hashed, Kind: models.EventStatNotable},
		{Label: "Taken from analysis", Value: res.Reused, Kind: models.EventStatMuted},
		{Label: "Already current", Value: res.Fresh, Kind: models.EventStatMuted},
		{Label: "Failed", Value: len(res.Failures), Kind: models.EventStatBad, Filter: models.EventItemStatusError},
	}
	if len(items) > r.detailRetention {
		items = items[:r.detailRetention]
	}
	events.Finish(r.db, event, status, summary, map[string]any{
		"candidates": len(candidates), "hashed": res.Hashed, "reused": res.Reused,
		"fresh": res.Fresh, "gone": res.Gone, "errors": len(res.Failures),
	})
	events.AddItems(r.db, event, items)
	events.Prune(r.db, r.eventRetention)
}
