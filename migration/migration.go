// Package migration applies MusicBrainz identity changes to Autotaggerr's own data.
//
// Detection lives in modules (it happens on the fetch path, under the rate limiter);
// this package is the other half — deciding whether a detected change may be applied
// yet, and rewriting the rows if so.
//
// The split matters because applying a migration is not a field update. Every table
// that stores an MBID has a unique index on it, so a merge whose target row already
// exists — the *common* case, since you probably own files under both IDs — has to
// merge two rows and dedupe, not update one. And the tables divide into two kinds
// that must be treated differently:
//
//   - Derived state (ownership counts, which release-group is owned) is rebuilt from
//     disk by collection.Rebuild after every scan and sync. It needs no careful
//     merging; it needs the stale row gone so Rebuild can recompute cleanly.
//   - Authored state (desires, monitoring, follow types) exists nowhere else. Losing
//     it to a merge would be the one genuinely unrecoverable outcome here, so the
//     merge rules below always union rather than pick a winner.
package migration

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Policy decides which detected migrations are applied automatically and which are
// held for a human. It mirrors the autotaggerr_migration_review_* config keys.
//
// The fields are phrased as "review this" rather than "auto-apply this" so that the
// zero value means apply — a config.json written before this feature existed decodes
// to all-false, and that must keep the app moving rather than silently filling a
// queue nobody knows to look at.
type Policy struct {
	ReviewReleases  bool
	ReviewArtists   bool
	ReviewPinned    bool
	ReviewDeletions bool
}

// PolicyFromConfig reads the policy out of the process config.
func PolicyFromConfig(cfg models.ConfigStruct) Policy {
	return Policy{
		ReviewReleases:  cfg.AutotaggerrMigrationReviewReleases,
		ReviewArtists:   cfg.AutotaggerrMigrationReviewArtists,
		ReviewPinned:    cfg.AutotaggerrMigrationReviewPinned,
		ReviewDeletions: cfg.AutotaggerrMigrationReviewDeletions,
	}
}

// heldForReview reports whether this migration must wait for approval. The pinned
// rule is an override, not another category: a migration that would rewrite a manual
// correlation is held whatever its entity type, because the thing being second-
// guessed is a decision a person made by hand.
//
// repairable is whether asking a manager could still change the answer — a caller
// without that fact to hand passes false, which is the reading that applies rather
// than the one that queues. It is a parameter rather than a lookup so this stays a
// pure function of the row and one fact about it: policy is the part of this package
// worth being able to read in one screen.
func (p Policy) heldForReview(m models.MusicbrainzMigration, repairable bool) bool {
	if m.TouchesPinned && p.ReviewPinned {
		return true
	}
	// A release-group deletion is held while a repair is still possible and untried,
	// and this is the one place the zero-value-means-apply convention above is
	// deliberately not followed.
	//
	// The convention is safe where a deletion is MusicBrainz's own act, because then
	// there is nothing to recover: the entity is gone and the row is dead weight. A
	// release-group deletion is usually not that. It is typically a manager holding an
	// ID that upstream dropped or re-keyed, and the album is alive in MusicBrainz under
	// a different ID — so applying it unattended would remove an album that one
	// manager refresh repairs.
	//
	// Two things end that objection, and both have to, because the hold is on the
	// *possibility* of a repair rather than on the ceremony of having tried one:
	//
	//   - The manager has been asked (RepairAttemptedAt). It re-read the artist and
	//     either corrected the ID or stopped listing the album, so a row still pointing
	//     at an unresolvable ID is genuinely dead and can go without asking again.
	//   - No manager lists the album at all (repairable == false). Then there is nobody
	//     to ask and nothing to recover: the ID resolves nowhere and the only authority
	//     that could have re-keyed it has already let go. Holding on RepairAttemptedAt
	//     alone deadlocked exactly these rows — the repair pass takes its candidates
	//     from collection.GhostReleaseGroups, which selects albums a manager still
	//     lists, so an album outside the catalog could never be stamped and waited for
	//     an event that could not happen. What it waited for was a person pressing a
	//     button that did what this pass would have done.
	//
	// Retirement is guarded regardless (collection.RetireReleaseGroup), so an
	// auto-applied deletion that still has a claim on it refuses and says why.
	if m.EntityType == models.MigrationEntityReleaseGroup {
		return m.RepairAttemptedAt == nil && repairable
	}
	if m.Kind == models.MigrationKindDeleted {
		return p.ReviewDeletions
	}
	switch m.EntityType {
	case models.MigrationEntityArtist:
		return p.ReviewArtists
	default:
		return p.ReviewReleases
	}
}

// Result summarises a processing run, for the caller's event payload.
type Result struct {
	Applied int `json:"applied"`
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
	// Resolved is rows closed without being applied because the reason they were queued
	// stopped existing. Counted apart from Applied because nothing was rewritten: a run
	// reporting a manager's repair as an application would credit itself with work the
	// manager did.
	Resolved  int      `json:"resolved,omitempty"`
	Files     int      `json:"files_remapped"`
	Unmatched int      `json:"files_unmatched"`
	Retired   int      `json:"release_groups_retired,omitempty"`
	Errors    []string `json:"errors,omitempty"`

	// Outcomes is one entry per row this run settled, so the Activity event can list
	// *which* identities changed rather than only how many. The counts above answer
	// "did anything happen"; a person reading the feed a day later wants the names.
	Outcomes []Outcome `json:"outcomes,omitempty"`
}

// Outcome is what happened to one migration, in the shape an event detail row needs.
//
// It carries the entity's name because the name is about to stop being lookup-able:
// retiring an album deletes the only row that knows its title, so a detail list built
// from the collection afterwards would be a column of bare UUIDs.
type Outcome struct {
	EntityType string `json:"entity_type"`
	Kind       string `json:"kind"`
	OldMBID    string `json:"old_mb_id"`
	NewMBID    string `json:"new_mb_id,omitempty"`
	Name       string `json:"name,omitempty"`
	// Status is a models.EventItemStatus*, so the caller can hand it to an event row
	// without translating.
	Status string `json:"status"`
	// Detail is the sentence for a resolution or a failure, empty for a plain apply.
	Detail string `json:"detail,omitempty"`
	Files  int    `json:"files,omitempty"`
}

// outcomeOf describes a settled row.
func outcomeOf(m models.MusicbrainzMigration, status, detail string) Outcome {
	return Outcome{
		EntityType: m.EntityType,
		Kind:       m.Kind,
		OldMBID:    m.OldMBID,
		NewMBID:    m.NewMBID,
		Name:       m.Name,
		Status:     status,
		Detail:     detail,
		Files:      m.AffectedFiles,
	}
}

// Add accumulates another run's result into this one. A run that drains the queue
// more than once (the identity sweep drains after releases and again after artists)
// would otherwise report only the last drain, hiding everything the first one did.
func (r *Result) Add(other Result) {
	r.Applied += other.Applied
	r.Pending += other.Pending
	r.Failed += other.Failed
	r.Resolved += other.Resolved
	r.Files += other.Files
	r.Unmatched += other.Unmatched
	r.Retired += other.Retired
	r.Errors = append(r.Errors, other.Errors...)
	r.Outcomes = append(r.Outcomes, other.Outcomes...)
}

// ProcessPending measures every pending migration, applies the ones policy allows,
// and leaves the rest queued. Returns what happened, for the Activity event.
//
// Measuring first is what lets a pending row describe itself in the review UI ("12
// files, 1 desire") without re-querying, and is also how TouchesPinned is known —
// detection runs in modules, which has no view of library_items.
func ProcessPending(db *gorm.DB, policy Policy) (Result, error) {
	res := Result{}
	if db == nil {
		return res, errors.New("no database configured")
	}

	// Pending, plus failed release-group retirements — the one failure whose cause is
	// expected to clear on its own.
	//
	// Every other migration fails for a reason a retry cannot change: a redirect with
	// no target stays targetless. A retirement fails because something still claims the
	// album, and the usual claim is the manager listing it, which is exactly what the
	// repair pass sets out to change. Leaving those permanently failed meant a row that
	// became retirable an hour later stayed failed forever, waiting on a discography
	// prune or a human.
	var candidates []models.MusicbrainzMigration
	if err := db.
		Where("status = ?", models.MigrationStatusPending).
		Or("status = ? AND entity_type = ? AND kind = ?",
			models.MigrationStatusFailed, models.MigrationEntityReleaseGroup, models.MigrationKindDeleted).
		Find(&candidates).Error; err != nil {
		return res, err
	}

	for _, m := range candidates {
		// Settled elsewhere while it sat here. Checked before anything else, because
		// every step below assumes there is still something to migrate: measuring a
		// vanished entity writes zeroes, and applying one reports a rewrite of nothing
		// as an application.
		if reason, moot := mootReason(db, m); moot {
			if err := closeExternally(db, &m, reason); err != nil {
				logger.Log.Warnf("failed to close migration %s: %s", m.OldMBID, err.Error())
				continue
			}
			res.Resolved++
			res.Outcomes = append(res.Outcomes, outcomeOf(m, models.EventItemStatusResolved, reason))
			continue
		}

		// Checked before measuring, not after: a failed row was measured on the pass
		// that failed it, so re-measuring a still-blocked one would re-save the row to
		// write back the values it already holds. Retrying regardless would also rewrite
		// the same refusal every run, telling the reader nothing new, and would report a
		// failure the run did not cause.
		if m.Status == models.MigrationStatusFailed && !retirementUnblocked(db, m) {
			continue
		}

		if err := measure(db, &m); err != nil {
			logger.Log.Warnf("failed to measure migration %s: %s", m.OldMBID, err.Error())
			continue
		}

		if policy.heldForReview(m, repairable(db, m)) {
			res.Pending++
			continue
		}

		applied, err := apply(db, &m, models.MigrationResolutionAutomatic)
		if err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("%s %s: %s", m.EntityType, m.OldMBID, err.Error()))
			res.Outcomes = append(res.Outcomes, outcomeOf(m, models.EventItemStatusError, err.Error()))
			continue
		}
		res.Applied++
		res.Files += applied.files
		res.Unmatched += applied.unmatched
		res.Retired += applied.retired
		res.Outcomes = append(res.Outcomes, outcomeOf(m, models.EventItemStatusMigrated, ""))
	}

	return res, nil
}

// repairable reports whether a manager could still be asked about this row — which,
// for a release-group deletion, is the same question as whether one still lists the
// album. Nothing else is repairable through a manager, so nothing else claims to be.
//
// It is the live catalog flag rather than a stored one because that is the fact the
// hold is about: an album drops out of a manager's catalog between runs, and a
// snapshot taken at detection would hold a row on a claim that expired weeks ago.
//
// A read error reports true — the answer that keeps the row queued. Being wrong that
// way costs a person one press; being wrong the other way retires an album on a
// database hiccup.
func repairable(db *gorm.DB, m models.MusicbrainzMigration) bool {
	if db == nil || m.EntityType != models.MigrationEntityReleaseGroup {
		return false
	}
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", m.OldMBID).First(&rg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// No collection row at all: nothing to retire and nobody listing it. The
			// moot check ahead of this settles the row; it is not held for a repair.
			return false
		}
		return true
	}
	return rg.InCatalog
}

// mootReason reports whether there is anything left to migrate, and if not, the
// sentence saying why.
//
// The queue is not the only thing that can settle an identity change. A manager
// re-keys an album and the row it was holding stops existing; an artist prune takes a
// release-group; the last file pointing at a merged release is re-correlated by a scan.
// The migration then describes a change to state nobody holds any more — and applying
// it would rewrite nothing while reporting an application, which is the one outcome
// worse than leaving it queued.
//
// Absence is read as "nothing to do", never as "do something": every branch here is a
// count of what still references the old ID, so being wrong means leaving a row in the
// queue rather than closing one that mattered.
func mootReason(db *gorm.DB, m models.MusicbrainzMigration) (string, bool) {
	// A redirect with nowhere to point is malformed, not moot. Closing it quietly
	// because nothing happens to reference it today would file a data problem under
	// "resolved itself"; failing it leaves the row saying what is wrong with it.
	if m.Kind == models.MigrationKindRedirect && m.NewMBID == "" {
		return "", false
	}

	switch m.EntityType {
	case models.MigrationEntityReleaseGroup:
		// A retirement's whole content is deleting this row. Without it there is no
		// retirement to perform, whoever removed it.
		if countRows(db, &models.CollectionReleaseGroup{}, "mb_id = ?", m.OldMBID) > 0 {
			return "", false
		}
		return "the album is no longer in the collection — a manager re-keyed it, or it " +
			"was removed while this was waiting", true

	case models.MigrationEntityArtist:
		refs := countRows(db, &models.CollectionArtist{}, "mb_id = ?", m.OldMBID) +
			countRows(db, &models.CollectionReleaseGroup{}, "artist_mb_id = ?", m.OldMBID) +
			countRows(db, &models.CollectionRelease{}, "artist_mb_id = ?", m.OldMBID) +
			countRows(db, &models.CollectionDesire{}, "artist_mb_id = ?", m.OldMBID) +
			countRows(db, &models.CollectionReleaseGroupArtist{}, "artist_mb_id = ?", m.OldMBID)
		if refs > 0 {
			return "", false
		}
		return "nothing in the collection is filed under this artist ID any more", true

	default:
		refs := countRows(db, &models.LibraryItem{}, "mb_release_id = ?", m.OldMBID) +
			countRows(db, &models.CollectionRelease{}, "mb_id = ?", m.OldMBID) +
			countRows(db, &models.CollectionDesire{}, "release_mb_id = ?", m.OldMBID)
		if refs > 0 {
			return "", false
		}
		return "no file and no collection row is keyed on this release any more", true
	}
}

// closeExternally settles a row nothing here had to apply.
//
// The cached entity goes with it, exactly as an application drops it: the old ID is
// dead weight either way, and leaving it cached means the next drift sync re-reads it,
// re-detects the same change and re-opens the row this just closed. Dropping it is also
// what makes modules.reopenIfClosedExternally a signal rather than a loop — after this,
// only something that genuinely still points at the old ID can cause another fetch.
func closeExternally(db *gorm.DB, m *models.MusicbrainzMigration, detail string) error {
	now := time.Now()
	m.Status = models.MigrationStatusResolved
	m.Resolution = models.MigrationResolutionExternal
	m.ResolutionDetail = detail
	m.ResolvedAt = &now
	m.Error = ""
	m.RepairQueuedAt = nil
	if err := db.Save(m).Error; err != nil {
		return err
	}
	forgetCachedEntity(*m)
	logger.Log.Infof("%s migration %s closed without applying: %s", m.EntityType, m.OldMBID, detail)
	return nil
}

// retirementUnblocked reports whether a previously failed release-group retirement
// would succeed now. A read error is treated as "still blocked": the next run asks
// again, which is the harmless direction to be wrong in.
func retirementUnblocked(db *gorm.DB, m models.MusicbrainzMigration) bool {
	ok, _, err := collection.ReleaseGroupRetirable(db, m.OldMBID)
	if err != nil {
		logger.Log.Warnf("could not re-check retirement for %s: %s", m.OldMBID, err.Error())
		return false
	}
	return ok
}

// ApplyByID applies one migration on demand — the approve button. Policy is not
// consulted: an explicit approval *is* the decision the policy was deferring to.
//
// A row that has become moot while it sat in the queue is closed rather than applied,
// on the same reasoning as the drain: pressing approve on an album a manager already
// re-keyed should report what actually happened to it, not claim a retirement that
// removed nothing.
func ApplyByID(db *gorm.DB, id uuid.UUID) (models.MusicbrainzMigration, error) {
	var m models.MusicbrainzMigration
	if err := db.First(&m, "id = ?", id).Error; err != nil {
		return m, err
	}
	if err := settled(m); err != nil {
		return m, err
	}
	if reason, moot := mootReason(db, m); moot {
		return m, closeExternally(db, &m, reason)
	}
	if err := measure(db, &m); err != nil {
		return m, err
	}
	if _, err := apply(db, &m, models.MigrationResolutionApproved); err != nil {
		return m, err
	}
	return m, nil
}

// Dismiss marks a migration as deliberately not applied. The row is kept rather than
// deleted, because deleting it would let the next fetch of the same old ID re-detect
// the identical move and re-queue it, which is exactly the nagging the user just
// declined.
func Dismiss(db *gorm.DB, id uuid.UUID) (models.MusicbrainzMigration, error) {
	var m models.MusicbrainzMigration
	if err := db.First(&m, "id = ?", id).Error; err != nil {
		return m, err
	}
	if err := settled(m); err != nil {
		return m, err
	}
	now := time.Now()
	m.Status = models.MigrationStatusDismissed
	m.Resolution = models.MigrationResolutionDismissed
	m.ResolvedAt = &now
	m.RepairQueuedAt = nil
	return m, db.Save(&m).Error
}

// settled refuses a second decision on a row that already has one. A dismissed or
// failed row is still open — both are re-decidable — but an applied or resolved one is
// a statement about work that has happened.
func settled(m models.MusicbrainzMigration) error {
	switch m.Status {
	case models.MigrationStatusApplied:
		return errors.New("migration has already been applied")
	case models.MigrationStatusResolved:
		return errors.New("this change was already resolved without needing to be applied")
	}
	return nil
}

// ListOptions is one page of the migrations table: which rows, in what order.
type ListOptions struct {
	// Status filters to one lifecycle state; empty is every row. "open" is the queue —
	// pending plus failed — since a failed retirement is still waiting on something and
	// belongs with the work rather than with the history.
	Status string
	// Query matches the entity's name or its old ID. Both, because half the reasons to
	// open this page start from a UUID someone pasted out of a log or a manager.
	Query  string
	Limit  int
	Offset int
	// Sort is a ListSort* key and Dir is "asc"/"desc". An unknown key falls back to the
	// default for the status being asked for, rather than erroring: a stale bookmark
	// should show the list, not a message about a query parameter.
	Sort string
	Dir  string
}

// The orderings the table offers. They are named for the fact rather than the column,
// because two of them are computed: a row's resolution time is whichever of three
// stamps it actually has, and older rows have none of them.
const (
	ListSortDetected = "detected"
	ListSortResolved = "resolved"
	ListSortName     = "name"
	ListSortEntity   = "entity"
	ListSortStatus   = "status"
)

// StatusOpen asks for everything still awaiting a decision, and StatusClosed for
// everything that is over. Neither is a stored status: a failed row is one whose
// application was refused for a reason that may clear (see ProcessPending), so it
// belongs in the queue with the pending ones and not in a history of settled things —
// which is also the only place a person can see what is failing and why.
const (
	StatusOpen   = "open"
	StatusClosed = "closed"
)

// openStatuses is the queue: awaiting a decision, or refused and re-tried.
var openStatuses = []string{models.MigrationStatusPending, models.MigrationStatusFailed}

// resolvedAtExpr is when a row left the queue, for rows written before there was a
// column for it. A dismissal recorded nothing at all, and an application recorded only
// applied_at, so history ordered by any single column put half the table in an
// arbitrary place — which is what made it look alphabetical, since detection order
// follows the order a sweep walks artists in.
const resolvedAtExpr = "COALESCE(resolved_at, applied_at, updated_at)"

// List returns one page of migrations plus the total matching the filter.
//
// The total is what makes paging honest: the page itself cannot say whether there are
// three more rows or three hundred, and this table is one people scroll looking for a
// specific album.
func List(db *gorm.DB, opts ListOptions) ([]models.MusicbrainzMigration, int64, error) {
	var rows []models.MusicbrainzMigration
	if db == nil {
		return rows, 0, errors.New("no database configured")
	}

	q := db.Model(&models.MusicbrainzMigration{})
	switch opts.Status {
	case "":
	case StatusOpen:
		q = q.Where("status IN ?", openStatuses)
	case StatusClosed:
		q = q.Where("status NOT IN ?", openStatuses)
	default:
		q = q.Where("status = ?", opts.Status)
	}
	if query := strings.TrimSpace(opts.Query); query != "" {
		like := "%" + query + "%"
		q = q.Where("name LIKE ? OR old_mb_id LIKE ?", like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return rows, 0, err
	}

	q = q.Order(orderClause(opts))
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	err := q.Find(&rows).Error
	return rows, total, err
}

// orderClause turns a sort key into SQL, defaulting per status: the queue is read
// newest-detected-first, and the history newest-resolved-first. Every ordering ends in
// a detection tiebreak so a page boundary cannot repeat or skip a row when two stamps
// are equal — which they routinely are, since a sweep records many rows in one second.
func orderClause(opts ListOptions) string {
	dir := "desc"
	if strings.EqualFold(opts.Dir, "asc") {
		dir = "asc"
	}

	sort := opts.Sort
	if sort == "" {
		sort = ListSortDetected
		if opts.Status != "" && opts.Status != StatusOpen && opts.Status != models.MigrationStatusPending {
			sort = ListSortResolved
		}
	}

	switch sort {
	case ListSortResolved:
		return resolvedAtExpr + " " + dir + ", detected_at desc"
	case ListSortName:
		// NOCASE so "the beatles" files with "The Beatles" rather than after every
		// capitalised title, which is how a reader looking for a name expects it.
		return "name COLLATE NOCASE " + dir + ", detected_at desc"
	case ListSortEntity:
		return "entity_type " + dir + ", name COLLATE NOCASE asc, detected_at desc"
	case ListSortStatus:
		return "status " + dir + ", detected_at desc"
	default:
		return "detected_at " + dir
	}
}

// PendingCount is the badge number for the UI.
func PendingCount(db *gorm.DB) (int64, error) {
	var n int64
	err := db.Model(&models.MusicbrainzMigration{}).
		Where("status = ?", models.MigrationStatusPending).Count(&n).Error
	return n, err
}

// MarkRepairQueued stamps every open row belonging to an artist as having a manager
// repair in flight, and reports how many that was.
//
// Every row, not the one that was approved, because the repair is per *artist*: one
// refresh answers for all the albums of theirs holding unresolvable IDs. A table that
// marked only the pressed row would leave its siblings looking untouched while the very
// job that will settle them runs, and the first thing anyone does with a queue of eight
// albums by one artist is press the second one too.
func MarkRepairQueued(db *gorm.DB, artistMBID string) (int64, error) {
	if db == nil || artistMBID == "" {
		return 0, nil
	}
	res := db.Model(&models.MusicbrainzMigration{}).
		Where("status IN ?", []string{models.MigrationStatusPending, models.MigrationStatusFailed}).
		Where("old_mb_id IN (?)", releaseGroupsOfArtist(db, artistMBID)).
		Where("entity_type = ?", models.MigrationEntityReleaseGroup).
		Update("repair_queued_at", time.Now())
	return res.RowsAffected, res.Error
}

// ClearRepairQueued removes the in-flight mark for one artist, whatever the repair
// concluded. It is called on the way out of the job rather than only on success: a
// refresh that failed has still stopped running, and a row left marked would claim work
// is happening that is not.
//
// It reaches the rows still *open* after the job. The ones it settled are out of its
// reach — the album is the only route from a migration row to its artist, and retiring
// the album deletes it — so those clear their own mark as they close (see apply and
// closeExternally). Between them every row the job touched ends up unmarked, and this
// one no longer has to be the half that succeeded.
func ClearRepairQueued(db *gorm.DB, artistMBID string) error {
	if db == nil || artistMBID == "" {
		return nil
	}
	return db.Model(&models.MusicbrainzMigration{}).
		Where("repair_queued_at IS NOT NULL").
		Where("old_mb_id IN (?)", releaseGroupsOfArtist(db, artistMBID)).
		Update("repair_queued_at", nil).Error
}

// ReconcileQueued clears in-flight marks left behind by a process that died holding
// them, in the same spirit as events.ReconcileRunning: nothing is running at startup,
// so a mark that survived a restart can only be a lie. Startup-only by contract — it
// cannot tell a stale mark from one this process just made.
func ReconcileQueued(db *gorm.DB) {
	if db == nil {
		return
	}
	res := db.Model(&models.MusicbrainzMigration{}).
		Where("repair_queued_at IS NOT NULL").
		Update("repair_queued_at", nil)
	if res.Error != nil {
		logger.Log.Warnf("failed to clear stale migration repair marks: %s", res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		logger.Log.Infof("cleared %d migration repair mark(s) left by a previous process", res.RowsAffected)
	}
}

// releaseGroupsOfArtist is the album IDs an artist is credited on, as a subquery. Both
// credit routes count: the release-group's own artist column and the credit link table,
// so a collaboration is not missed on the artist who is not first-billed.
func releaseGroupsOfArtist(db *gorm.DB, artistMBID string) *gorm.DB {
	return db.Model(&models.CollectionReleaseGroup{}).
		Select("mb_id").
		Where("artist_mb_id = ?", artistMBID).
		Or("mb_id IN (?)", db.Model(&models.CollectionReleaseGroupArtist{}).
			Select("release_group_mb_id").Where("artist_mb_id = ?", artistMBID))
}

// measure fills in the impact snapshot and whether a pinned correlation is involved.
func measure(db *gorm.DB, m *models.MusicbrainzMigration) error {
	files, pinned, err := affectedFiles(db, *m)
	if err != nil {
		return err
	}
	desires, err := affectedDesires(db, *m)
	if err != nil {
		return err
	}

	m.AffectedFiles = files
	m.AffectedDesires = desires
	m.TouchesPinned = pinned
	if m.Name == "" {
		m.Name = describe(db, *m)
	}
	return db.Save(m).Error
}

// affectedFiles counts the indexed files a migration would touch, and whether any of
// them is a manual attachment.
func affectedFiles(db *gorm.DB, m models.MusicbrainzMigration) (int, bool, error) {
	if m.EntityType != models.MigrationEntityRelease {
		// An artist merge rewrites collection rows, not file correlations: files are
		// keyed by release, and the release is unaffected by its artist merging.
		return 0, false, nil
	}

	var items []models.LibraryItem
	if err := db.Where("mb_release_id = ?", m.OldMBID).Find(&items).Error; err != nil {
		return 0, false, err
	}
	pinned := false
	for _, item := range items {
		if item.Pinned {
			pinned = true
			break
		}
	}
	return len(items), pinned, nil
}

// affectedDesires counts authored wants that reference the old ID.
func affectedDesires(db *gorm.DB, m models.MusicbrainzMigration) (int, error) {
	var n int64
	var err error
	switch m.EntityType {
	case models.MigrationEntityArtist:
		err = db.Model(&models.CollectionDesire{}).Where("artist_mb_id = ?", m.OldMBID).Count(&n).Error
	case models.MigrationEntityReleaseGroup:
		err = db.Model(&models.CollectionDesire{}).Where("release_group_mb_id = ?", m.OldMBID).Count(&n).Error
	default:
		err = db.Model(&models.CollectionDesire{}).Where("release_mb_id = ?", m.OldMBID).Count(&n).Error
	}
	return int(n), err
}

// describe names the entity for the review UI, from whatever row already knows it.
func describe(db *gorm.DB, m models.MusicbrainzMigration) string {
	switch m.EntityType {
	case models.MigrationEntityArtist:
		var artist models.CollectionArtist
		if err := db.Where("mb_id = ?", m.OldMBID).First(&artist).Error; err == nil {
			return artist.Name
		}
	case models.MigrationEntityReleaseGroup:
		// Captured before the row can be retired: applying this migration deletes the
		// only thing that knows the title, and a review queue of bare UUIDs is not a
		// review queue.
		var rg models.CollectionReleaseGroup
		if err := db.Where("mb_id = ?", m.OldMBID).First(&rg).Error; err == nil {
			return rg.Title
		}
	default:
		var release models.CollectionRelease
		if err := db.Where("mb_id = ?", m.OldMBID).First(&release).Error; err == nil {
			return release.Title
		}
		if cached, ok := modules.CachedRelease(m.OldMBID); ok {
			return cached.Title
		}
	}
	return ""
}

// applyCounts is what one application actually changed.
type applyCounts struct {
	files     int
	unmatched int
	// retired is release-groups withdrawn from the catalogue. Counted apart from
	// files because nothing on disk moved: the run's summary would otherwise report a
	// retirement as "0 files remapped, 0 unmatched" and read as having done nothing.
	retired int
}

// apply performs a migration in a single transaction and records the outcome on the
// row. All-or-nothing is the point: a half-remapped merge leaves some tables keyed
// on the old ID and some on the new, which is worse than not having started.
func apply(db *gorm.DB, m *models.MusicbrainzMigration, resolution string) (applyCounts, error) {
	counts := applyCounts{}

	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		switch {
		case m.Kind == models.MigrationKindDeleted:
			counts, err = applyDeletion(tx, *m)
		case m.EntityType == models.MigrationEntityArtist:
			counts, err = applyArtistRedirect(tx, *m)
		default:
			counts, err = applyReleaseRedirect(tx, *m)
		}
		return err
	})

	now := time.Now()
	if err != nil {
		m.Status = models.MigrationStatusFailed
		m.Error = err.Error()
		if saveErr := db.Save(m).Error; saveErr != nil {
			logger.Log.Warnf("failed to record migration failure for %s: %s", m.OldMBID, saveErr.Error())
		}
		return counts, err
	}

	m.Status = models.MigrationStatusApplied
	m.Error = ""
	m.AppliedAt = &now
	m.Resolution = resolution
	m.ResolvedAt = &now
	m.ResolutionDetail = appliedDetail(*m, counts)
	// A settled row is not waiting on a manager, whatever mark it was carrying. Cleared
	// here rather than only by the job that set it, because the job clears by looking
	// the artist up through the album — and retiring the album is precisely what this
	// just did. The rows a repair succeeded on were the ones left claiming it was still
	// running.
	m.RepairQueuedAt = nil
	if err := db.Save(m).Error; err != nil {
		return counts, err
	}

	forgetCachedEntity(*m)

	logger.Log.Infof("applied %s %s migration %s -> %s (%d files)",
		m.SourceLabel(), m.EntityType, m.OldMBID, m.NewMBID, counts.files)
	return counts, nil
}

// appliedDetail is the sentence the history shows beside an applied row.
//
// A closed row's status says where it ended up and its resolution says who put it
// there; neither says what was done. Both questions a history is opened for — what did
// it decide while I was not looking, and why did this one settle itself — need the
// third fact, and for a retirement it is the one that reassures: an album vanishing
// from the collection view reads very differently once the line beneath it says no file
// was touched.
//
// Written in the past tense from what actually happened, which is why it is not the
// review payload's Effect: that sentence is an offer ("Approving removes…") composed
// before the fact, and the counts here are the ones the transaction produced.
func appliedDetail(m models.MusicbrainzMigration, counts applyCounts) string {
	source := m.SourceLabel()

	switch {
	case m.EntityType == models.MigrationEntityReleaseGroup:
		if counts.retired == 0 {
			return "There was nothing left to remove — the album had already left the collection."
		}
		// Which of the two roads it took here is the whole point of the sentence. One
		// says a manager was consulted and let go of the album; the other says no
		// manager ever claimed it. Both end in the same deletion, and a reader who
		// cannot tell them apart has to go and check whether Lidarr still lists it.
		if m.RepairAttemptedAt != nil {
			return "The manager was re-read and no longer lists this album, and the ID " +
				"still resolves nowhere, so the album was removed from your collection " +
				"view. No files were touched and nothing was deleted from the manager."
		}
		return "No manager lists this album and its ID resolves nowhere, so it was " +
			"removed from your collection view. No files were touched."

	case m.Kind == models.MigrationKindDeleted && m.EntityType == models.MigrationEntityArtist:
		return "The artist was removed from your collection. Their albums are keyed by " +
			"release, so no file and no album row was affected."

	case m.Kind == models.MigrationKindDeleted:
		// Phrased to sit either side of the singular/plural line: "1 file must be" and
		// "3 files must be" both read, where anything with its own verb needs two
		// sentences to say one thing.
		return fmt.Sprintf("%s no longer has this release, so %s must be re-identified "+
			"and the owned edition was dropped. The identifiers stay on the files.",
			source, plural(counts.unmatched, "file", "files"))

	case m.EntityType == models.MigrationEntityArtist:
		return "The artist's albums, editions and wants were re-pointed at the surviving " +
			"ID. Monitoring and follow settings were merged, not dropped."

	default:
		return fmt.Sprintf("Re-pointed %s at the surviving ID and cleared the processed "+
			"marker, so the next run re-reads the track IDs from the new release and "+
			"re-tags them.", plural(counts.files, "file", "files"))
	}
}

// forgetCachedEntity drops what the cache holds under a migrated ID.
//
// The cache is keyed by MBID too, and once a row is settled the old key is dead
// weight: left in place it expires and gets re-fetched on every drift sync, spending
// rate limit to re-learn a change that has already been dealt with.
func forgetCachedEntity(m models.MusicbrainzMigration) {
	switch m.EntityType {
	case models.MigrationEntityRelease:
		modules.DropCachedRelease(m.OldMBID)
	case models.MigrationEntityArtist:
		// Two entries are keyed on an artist MBID, not one: who they are, and what
		// they released. Both describe an ID the app has just stopped believing in.
		modules.MusicbrainzForgetEntity(models.MBEntityArtist, m.OldMBID)
		modules.MusicbrainzForgetEntity(models.MBEntityDiscography, m.OldMBID)
	}
}

// applyReleaseRedirect repoints everything keyed on a merged release.
func applyReleaseRedirect(tx *gorm.DB, m models.MusicbrainzMigration) (applyCounts, error) {
	counts := applyCounts{}
	if m.NewMBID == "" {
		return counts, errors.New("redirect has no target MBID")
	}

	// Files. Pinned items are remapped along with the rest: a merge renames an
	// entity, it does not substitute a different one, so the release the user chose
	// by hand *is* the surviving one. Leaving the pin on a dead ID would quietly
	// break the very file they took the trouble to identify.
	//
	// ProcessedVersion is cleared at the same time, which is the deliberate part.
	// Track and recording MBIDs are scoped to the release they came from, so a merge
	// leaves those two columns pointing into a release that no longer exists — and
	// nothing in this transaction can derive the replacements without fetching the
	// new release and re-matching every track, under the rate limiter, in the middle
	// of a sync. Blanking the version instead trips the existing skip-unchanged
	// escape hatch: the next scan re-correlates exactly these files and writes the
	// correct track IDs, using machinery that already exists for the case where the
	// app's behaviour changed underneath a file.
	res := tx.Model(&models.LibraryItem{}).
		Where("mb_release_id = ?", m.OldMBID).
		Updates(map[string]any{
			"mb_release_id":     m.NewMBID,
			"processed_version": "",
		})
	if res.Error != nil {
		return counts, res.Error
	}
	counts.files = int(res.RowsAffected)

	// Owned editions: derived state, so a collision is resolved by dropping the
	// stale row and letting collection.Rebuild recount from the files above.
	var target models.CollectionRelease
	targetExists := tx.Where("mb_id = ?", m.NewMBID).First(&target).Error == nil
	if targetExists {
		if err := tx.Where("mb_id = ?", m.OldMBID).Delete(&models.CollectionRelease{}).Error; err != nil {
			return counts, err
		}
	} else if err := tx.Model(&models.CollectionRelease{}).
		Where("mb_id = ?", m.OldMBID).
		Update("mb_id", m.NewMBID).Error; err != nil {
		return counts, err
	}

	// Authored intent. A desire naming the old edition now names the surviving one.
	if err := tx.Model(&models.CollectionDesire{}).
		Where("release_mb_id = ?", m.OldMBID).
		Update("release_mb_id", m.NewMBID).Error; err != nil {
		return counts, err
	}

	return counts, dedupeDesires(tx)
}

// applyArtistRedirect merges two artists into one.
//
// Every field here is unioned rather than overwritten. Monitoring and follow types
// are the only record of what the user asked for, and a merge is not an occasion to
// quietly stop following someone: if either side was monitored, the survivor is.
func applyArtistRedirect(tx *gorm.DB, m models.MusicbrainzMigration) (applyCounts, error) {
	counts := applyCounts{}
	if m.NewMBID == "" {
		return counts, errors.New("redirect has no target MBID")
	}

	var source models.CollectionArtist
	haveSource := tx.Where("mb_id = ?", m.OldMBID).First(&source).Error == nil

	var target models.CollectionArtist
	haveTarget := tx.Where("mb_id = ?", m.NewMBID).First(&target).Error == nil

	switch {
	case haveSource && haveTarget:
		target.Monitored = target.Monitored || source.Monitored
		target.FollowSecondary = target.FollowSecondary || source.FollowSecondary
		target.FollowTypes = unionCSV(target.FollowTypes, source.FollowTypes)
		// Every other follow setting merges toward wanting *more*, and the year cutoff
		// has to follow the same rule or a merge would silently drop albums the user
		// was already being told about. No cutoff on either side wins outright; two
		// cutoffs merge to the earlier one.
		target.FollowFromYear = earlierCutoff(target.FollowFromYear, source.FollowFromYear)
		// A manually added artist outranks a library-derived one: it records that
		// someone wanted this artist before owning any of them, which rebuilding
		// from disk cannot reconstruct.
		if source.Origin == models.CollectionOriginManual {
			target.Origin = models.CollectionOriginManual
		}
		if target.Name == "" {
			target.Name = source.Name
		}
		if err := tx.Save(&target).Error; err != nil {
			return counts, err
		}
		if err := tx.Where("mb_id = ?", m.OldMBID).Delete(&models.CollectionArtist{}).Error; err != nil {
			return counts, err
		}
	case haveSource:
		if err := tx.Model(&models.CollectionArtist{}).
			Where("mb_id = ?", m.OldMBID).
			Update("mb_id", m.NewMBID).Error; err != nil {
			return counts, err
		}
	}

	// Everything that points at an artist by MBID.
	for _, ref := range []struct {
		model  any
		column string
	}{
		{&models.CollectionReleaseGroup{}, "artist_mb_id"},
		{&models.CollectionRelease{}, "artist_mb_id"},
		{&models.CollectionDesire{}, "artist_mb_id"},
	} {
		if err := tx.Model(ref.model).
			Where(ref.column+" = ?", m.OldMBID).
			Update(ref.column, m.NewMBID).Error; err != nil {
			return counts, err
		}
	}

	if err := remapCreditLinks(tx, m.OldMBID, m.NewMBID); err != nil {
		return counts, err
	}
	return counts, dedupeDesires(tx)
}

// remapCreditLinks moves an artist's release-group credits onto the surviving MBID.
//
// This is the one table where a blind UPDATE fails rather than merely being wrong:
// collection_release_group_artists has a composite unique index on (release_group,
// artist), so a collaboration credited to both sides of a merge — precisely what a
// merge means — would violate it. Rows are therefore moved one at a time, and a row
// that would collide is dropped in favour of the existing one, keeping the earlier
// (more prominent) credit position of the two.
func remapCreditLinks(tx *gorm.DB, oldMBID, newMBID string) error {
	var links []models.CollectionReleaseGroupArtist
	if err := tx.Where("artist_mb_id = ?", oldMBID).Find(&links).Error; err != nil {
		return err
	}

	for _, link := range links {
		var existing models.CollectionReleaseGroupArtist
		err := tx.Where("release_group_mb_id = ? AND artist_mb_id = ?", link.ReleaseGroupMBID, newMBID).
			First(&existing).Error
		if err == nil {
			if link.Position < existing.Position {
				existing.Position = link.Position
				if err := tx.Save(&existing).Error; err != nil {
					return err
				}
			}
			if err := tx.Delete(&models.CollectionReleaseGroupArtist{}, "id = ?", link.ID).Error; err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Model(&models.CollectionReleaseGroupArtist{}).
			Where("id = ?", link.ID).
			Update("artist_mb_id", newMBID).Error; err != nil {
			return err
		}
	}
	return nil
}

// applyDeletion handles an entity that no longer exists upstream.
//
// Nothing is destroyed. The files stay indexed and keep the MB IDs they had — a dead
// ID is still the best available record of what the file was thought to be, and it
// is what makes the deletion diagnosable afterwards. What changes is the status:
// "unmatched" is the state that already means "this file needs identifying", so
// these files land in the same queue as a file that never matched, instead of in the
// error bucket next to genuine failures like an unreadable disk.
//
// Desires are never touched. A release disappearing upstream is exactly the moment
// the user's stated want becomes the only surviving record of what they were after.
func applyDeletion(tx *gorm.DB, m models.MusicbrainzMigration) (applyCounts, error) {
	counts := applyCounts{}

	if m.EntityType == models.MigrationEntityArtist {
		// An artist deletion does not orphan any file: files are keyed by release,
		// and MusicBrainz re-credits the releases rather than deleting them. Only the
		// artist's own collection row goes.
		if err := tx.Where("mb_id = ?", m.OldMBID).Delete(&models.CollectionArtist{}).Error; err != nil {
			return counts, err
		}
		return counts, nil
	}

	if m.EntityType == models.MigrationEntityReleaseGroup {
		// No file touches a release-group directly — files are keyed by release — so
		// there is nothing to un-match here, only a catalogue row to withdraw. The
		// guards live in collection because they are prune's guards, and the two paths
		// deleting the same row on different evidence must not drift apart.
		removed, reason, err := collection.RetireReleaseGroup(tx, m.OldMBID)
		if err != nil {
			return counts, err
		}
		if !removed && reason != "" {
			// A refusal is a real outcome rather than a crash, so it is recorded on the
			// row as the sentence that explains it — the person who approved this can
			// then read why nothing happened. It is not necessarily final: a retirement
			// blocked by the manager still listing the album becomes possible once a
			// refresh drops it, which is why ProcessPending re-picks these.
			return counts, errors.New(reason)
		}
		if removed {
			// An absent row is a success — there is nothing left to retire, which is the
			// state the migration wanted — but it is not a retirement, and counting it
			// as one would report albums removed by a run that removed nothing.
			counts.retired = 1
		}
		return counts, nil
	}

	res := tx.Model(&models.LibraryItem{}).
		Where("mb_release_id = ?", m.OldMBID).
		Updates(map[string]any{
			"status": models.LibraryItemStatusUnmatched,
			"error":  "release no longer exists on MusicBrainz — re-identify this file",
		})
	if res.Error != nil {
		return counts, res.Error
	}
	counts.unmatched = int(res.RowsAffected)

	// The owned-edition row is derived from files that no longer resolve, so it
	// would otherwise keep the release counting towards a complete album.
	if err := tx.Where("mb_id = ?", m.OldMBID).Delete(&models.CollectionRelease{}).Error; err != nil {
		return counts, err
	}
	return counts, nil
}

// dedupeDesires collapses desires that have become identical through a remap.
//
// Wanting an album under two IDs that turn out to be the same album is one want, and
// leaving both would show the user a duplicate row they cannot tell apart. Recording
// selections are unioned rather than one row winning: each was a real choice, and the
// union is the only merge that cannot silently drop a track someone asked for.
func dedupeDesires(tx *gorm.DB) error {
	var all []models.CollectionDesire
	if err := tx.Order("created_at asc").Find(&all).Error; err != nil {
		return err
	}

	type key struct{ artist, group, release string }
	seen := map[key]*models.CollectionDesire{}

	for i := range all {
		d := all[i]
		k := key{d.ArtistMBID, d.ReleaseGroupMBID, d.ReleaseMBID}
		keeper, ok := seen[k]
		if !ok {
			kept := d
			seen[k] = &kept
			continue
		}

		merged := unionStrings(keeper.RecordingMBIDs, d.RecordingMBIDs)
		// An empty recording set means "the whole thing", which subsumes any track
		// selection — unioning it with a partial set must not narrow it back down.
		if len(keeper.RecordingMBIDs) == 0 || len(d.RecordingMBIDs) == 0 {
			merged = nil
		}
		keeper.RecordingMBIDs = merged
		// The survivor takes the stronger provenance. A hand-authored want that
		// merged into a derived one would become a row the reconciliation passes may
		// re-point or prune — the user's pick quietly demoted to the mirror's, by an
		// upstream merge they had no part in. Authored intent outranks derived here
		// for the same reason it outranks it everywhere else.
		if !d.Derived() {
			keeper.Source = d.Source
		}
		if err := tx.Save(keeper).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.CollectionDesire{}, "id = ?", d.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// RelinkRelease follows a release that has changed release-group upstream.
//
// This is deliberately *not* a migration row. A release payload naming a different
// release-group than the one on record can mean the two groups were merged, or that
// this single release was moved to another group — and the payload cannot tell them
// apart. Re-pointing only the release in hand is correct under both readings, and
// collection.Rebuild recomputes the group's ownership from there. Remapping the group
// globally would be right for a merge and destructive for a move.
func RelinkRelease(db *gorm.DB, releaseMBID, releaseGroupMBID string) (bool, error) {
	if db == nil || releaseMBID == "" || releaseGroupMBID == "" {
		return false, nil
	}

	var row models.CollectionRelease
	if err := db.Where("mb_id = ?", releaseMBID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if row.ReleaseGroupMBID == releaseGroupMBID {
		return false, nil
	}

	logger.Log.Infof("release %s moved from release-group %s to %s", releaseMBID, row.ReleaseGroupMBID, releaseGroupMBID)
	return true, db.Model(&models.CollectionRelease{}).
		Where("mb_id = ?", releaseMBID).
		Update("release_group_mb_id", releaseGroupMBID).Error
}
