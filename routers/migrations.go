package routers

import (
	"net/http"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/migration"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const defaultMigrationsLimit = 100

// listMigrations returns detected MusicBrainz identity changes, newest first.
// `?status=pending` is what the review UI asks for; unfiltered is the history.
func (a *API) listMigrations(c *gin.Context) {
	rows, err := migration.List(a.DB, c.Query("status"), defaultMigrationsLimit)
	if err != nil {
		logger.Log.Errorf("failed to list MusicBrainz migrations: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list migrations"})
		return
	}

	pending, err := migration.PendingCount(a.DB)
	if err != nil {
		logger.Log.Warnf("failed to count pending migrations: %s", err.Error())
	}

	// Decorated rather than raw: the stored row cannot say what is wrong, whether the
	// user has files under the entity, or what approving would do — see migration.Review.
	c.JSON(http.StatusOK, gin.H{"migrations": migration.Reviews(a.DB, rows), "pending": pending})
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
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

	a.Scan.RepairArtistAlbums(review.ArtistMBID)

	who := review.ArtistName
	if who == "" {
		who = "the artist"
	}
	c.JSON(http.StatusAccepted, gin.H{
		"status":      "manager refresh queued",
		"artist_mbid": review.ArtistMBID,
		"artist_name": review.ArtistName,
		"message": "Asking the manager to re-read " + who + ". If it corrects the album's " +
			"MusicBrainz ID this entry resolves itself; if it stops listing the album, the " +
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
	c.JSON(http.StatusOK, row)
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
