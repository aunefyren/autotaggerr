package routers

import (
	"fmt"
	"net/http"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/migration"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	defaultMigrationsLimit = 50
	// A page nobody asked for still costs what it decorates: every row in a response
	// runs the review queries that say what it would do (migration.NewReview). The cap
	// is what stops `?limit=100000` from turning one request into tens of thousands of
	// counts.
	maxMigrationsLimit = 200
)

// listMigrations returns one page of detected identity changes.
//
// Paged on the server rather than in the browser, unlike the collection tables. Those
// hold rows the client already has; this one decorates every row it returns with the
// several queries that say what approving it would do, so "fetch them all and slice"
// would mean paying for the whole table to show fifty of it. The two lists the page
// shows — the queue and the history — are two requests for the same reason: they page
// independently, and a shared fetch would have to hold both in full to do that.
func (a *API) listMigrations(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), defaultMigrationsLimit)
	if limit <= 0 || limit > maxMigrationsLimit {
		limit = maxMigrationsLimit
	}

	opts := migration.ListOptions{
		Status: c.Query("status"),
		Query:  c.Query("q"),
		Limit:  limit,
		Offset: parseIntDefault(c.Query("offset"), 0),
		Sort:   c.Query("sort"),
		Dir:    c.Query("dir"),
	}
	rows, total, err := migration.List(a.DB, opts)
	if err != nil {
		logger.Log.Errorf("failed to list identity migrations: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list migrations"})
		return
	}

	pending, err := migration.PendingCount(a.DB)
	if err != nil {
		logger.Log.Warnf("failed to count pending migrations: %s", err.Error())
	}

	// Decorated rather than raw: the stored row cannot say what is wrong, whether the
	// user has files under the entity, or what approving would do — see migration.Review.
	c.JSON(http.StatusOK, gin.H{
		"migrations": migration.Reviews(a.DB, rows),
		"total":      total,
		"limit":      limit,
		"offset":     opts.Offset,
		"pending":    pending,
	})
}

// approveMigration applies one held migration. Approving is itself the decision the
// review policy was waiting for, so the policy is not re-consulted.
//
// One case does not apply anything: an album whose retirement is blocked only because
// the manager still lists it. There, approving asks the manager to re-read the artist
// and the migration settles itself afterwards — see Runner.RepairArtistAlbums for why
// that is the right answer rather than the refusal this used to return.
func (a *API) approveMigration(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}

	if handled := a.approveViaManagerRefresh(c, id); handled {
		return
	}

	row, err := migration.ApplyByID(a.DB, id)
	if err != nil {
		logger.Log.Errorf("failed to apply migration %s: %s", id, err.Error())
		// A refusal to decide twice is not an event. The row is settled, nothing was
		// attempted, and recording it would put a feed entry in front of anyone who
		// double-clicks — reporting their second press as a failure of the first.
		if row.Status != models.MigrationStatusApplied && row.Status != models.MigrationStatusResolved {
			a.recordMigrationDecision(row, models.EventItemStatusError, err.Error())
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Approving is a decision, and a decision that leaves no trace is indistinguishable
	// afterwards from the nightly pass having applied it — which is exactly the question
	// the feed exists to answer. Recorded after the fact so a failure is recorded too:
	// "approved and refused" is more useful than silence.
	if row.Status == models.MigrationStatusResolved {
		a.recordMigrationDecision(row, models.EventItemStatusResolved, row.ResolutionDetail)
	} else {
		a.recordMigrationDecision(row, models.EventItemStatusMigrated, "")
	}

	// Applying rewrites which releases the collection is keyed on, so the derived
	// ownership view is stale until it is rebuilt. Doing it here means the user sees
	// a consistent collection immediately after approving, rather than at whatever
	// point the next scan happens to run.
	a.rebuildAfterMigration()

	c.JSON(http.StatusOK, row)
}

// approveViaManagerRefresh handles the one approval that is a question rather than an
// application, reporting whether it took the request.
//
// It returns 202 with the artist it is asking about: the manager's refresh command is
// waited on for minutes, so holding the request open for the outcome would time out
// behind any reverse proxy long before the answer arrived. The queue's dedup is what
// makes a second press harmless.
//
// Everything it needs is already computed for the review list, so the check costs the
// same query the UI made to render the row.
func (a *API) approveViaManagerRefresh(c *gin.Context, id uuid.UUID) bool {
	if a.Scan == nil {
		return false
	}

	var row models.MusicbrainzMigration
	if err := a.DB.First(&row, "id = ?", id).Error; err != nil {
		return false
	}
	review := migration.NewReview(a.DB, row)
	if !review.NeedsManagerRefresh || review.ArtistMBID == "" {
		return false
	}

	// Marked before queueing, not after: the job may start on the worker goroutine
	// before this handler returns, and a mark written afterwards could land on a row the
	// job has already settled. Every open row of the artist's is marked, because the
	// refresh answers for all of them — see migration.MarkRepairQueued.
	marked, err := migration.MarkRepairQueued(a.DB, review.ArtistMBID)
	if err != nil {
		// The mark is how the table shows work in flight, not how the work happens.
		// Losing it costs a spinner, so it must not cost the repair.
		logger.Log.Warnf("failed to mark migrations as repairing for %s: %s", review.ArtistMBID, err.Error())
	}

	a.Scan.RepairArtistAlbums(review.ArtistMBID)

	who := review.ArtistName
	if who == "" {
		who = "the artist"
	}
	c.JSON(http.StatusAccepted, gin.H{
		"status":      "manager refresh queued",
		"queued":      true,
		"artist_mbid": review.ArtistMBID,
		"artist_name": review.ArtistName,
		"marked":      marked,
		"message": "Asking the manager to re-read " + who + ". If it corrects the album's " +
			"identifier this entry resolves itself; if it stops listing the album, the " +
			"album is removed from the collection. Follow it in Activity.",
	})
	return true
}

// dismissMigration records that a detected change was deliberately not applied.
func (a *API) dismissMigration(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}

	row, err := migration.Dismiss(a.DB, id)
	if err != nil {
		logger.Log.Errorf("failed to dismiss migration %s: %s", id, err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	a.recordMigrationDecision(row, models.EventItemStatusDismissed, "")
	c.JSON(http.StatusOK, row)
}

// recordMigrationDecision puts a human's decision about one identity change into the
// Activity feed.
//
// The counts on a run's own migration event answer "how many"; this answers "who
// decided, about what, and when" — the two questions a history is read for that a
// summary cannot carry. The entity row is the same shape a run writes, so the feed's
// existing resolution names it and offers its files without any special case.
//
// Best-effort, like everything in events: the decision is already committed, and
// failing the request because the feed could not be written would undo nothing.
func (a *API) recordMigrationDecision(row models.MusicbrainzMigration, outcome, detail string) {
	if a.DB == nil || row.ID == uuid.Nil {
		return
	}

	verb := map[string]string{
		models.EventItemStatusMigrated:  "Applied",
		models.EventItemStatusResolved:  "Closed",
		models.EventItemStatusDismissed: "Dismissed",
		models.EventItemStatusError:     "Could not apply",
	}[outcome]

	what := row.Name
	if what == "" {
		what = row.OldMBID
	}

	ev := events.Begin(a.DB, models.EventTypeMigration, verb+" an identity change")
	events.AddItems(a.DB, ev, []models.EventItem{{
		Path:   row.OldMBID,
		Kind:   models.EventItemKindEntity,
		Status: outcome,
		Error:  detail,
	}})

	status := models.EventStatusOK
	if outcome == models.EventItemStatusError {
		status = models.EventStatusError
	}
	summary := fmt.Sprintf("%s · %s", entityLabel(row.EntityType), what)
	if detail != "" {
		summary += " — " + detail
	}
	events.Finish(a.DB, ev, status, summary, map[string]any{
		"decision":    outcome,
		"by":          "user",
		"source":      row.SourceType(),
		"entity_type": row.EntityType,
		"kind":        row.Kind,
		"old_mb_id":   row.OldMBID,
		"new_mb_id":   row.NewMBID,
		"name":        row.Name,
	})
}

// entityLabel names an entity type the way the collection speaks about it — an "album"
// rather than a "release group". The one place a migration's type is written into a
// sentence the user reads.
func entityLabel(entityType string) string {
	switch entityType {
	case models.MigrationEntityReleaseGroup:
		return "Album"
	case models.MigrationEntityArtist:
		return "Artist"
	case models.MigrationEntityRelease:
		return "Release"
	}
	return entityType
}

// rebuildAfterMigration refreshes the derived collection view. Failures are logged,
// not returned: the migration itself succeeded and is committed, and reporting the
// rebuild as a failed approval would be misleading.
func (a *API) rebuildAfterMigration() {
	if _, err := collection.Rebuild(a.DB); err != nil {
		logger.Log.Warnf("failed to rebuild collection after migration: %s", err.Error())
	}
}

// verifyIdentities kicks off a full sweep of every stored MBID. It returns
// immediately with 202: the sweep is one rate-limited request per release and per
// artist, so a large collection takes hours — holding the request open for that would
// time out long before it finished. Progress shows up on the Activity feed, and the
// queue's dedup is what stops a second press from doubling the load.
func (a *API) verifyIdentities(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scanner unavailable"})
		return
	}
	a.Scan.VerifyIdentities()
	c.JSON(http.StatusAccepted, gin.H{"status": "identity check queued"})
}

// migrationPolicy reports the current review policy, so the UI can explain why a
// migration is sitting in the queue instead of having been applied.
func (a *API) migrationPolicy(c *gin.Context) {
	policy := migration.PolicyFromConfig(files.ConfigFile)
	c.JSON(http.StatusOK, gin.H{
		"review_releases":  policy.ReviewReleases,
		"review_artists":   policy.ReviewArtists,
		"review_pinned":    policy.ReviewPinned,
		"review_deletions": policy.ReviewDeletions,
		"entity_types":     []string{models.MigrationEntityRelease, models.MigrationEntityArtist},
	})
}
