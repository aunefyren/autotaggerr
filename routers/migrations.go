package routers

import (
	"net/http"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/migration"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
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

	c.JSON(http.StatusOK, gin.H{"migrations": rows, "pending": pending})
}

// approveMigration applies one held migration. Approving is itself the decision the
// review policy was waiting for, so the policy is not re-consulted.
func (a *API) approveMigration(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
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
