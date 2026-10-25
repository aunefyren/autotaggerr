package routers

import (
	"fmt"
	"net/http"
	"strings"

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
	out, err := a.approveMigrationByID(id)
	if err != nil {
		respondErr(c, err)
		return
	}
	if out.Queued {
		// 202 with the artist it is asking about: the manager's refresh is waited on
		// for minutes, far longer than a request may stay open behind a proxy.
		c.JSON(http.StatusAccepted, gin.H{
			"status":      "manager refresh queued",
			"queued":      true,
			"artist_mbid": out.ArtistMBID,
			"artist_name": out.ArtistName,
			"marked":      out.Marked,
			"message":     out.Message,
		})
		return
	}
	c.JSON(http.StatusOK, out.Migration)
}

// releaseMigrationFiles clears the one blocker that used to have no control: an album
// whose ID resolves nowhere, held because files on disk still resolve to it.
//
// What clears it depends on who decides those files' identity:
//
//   - **Autotaggerr**: nobody else will. The files are marked as needing
//     re-identification — what applying a release deletion does to its files — the
//     collection is re-derived, and the migration is applied, all before answering.
//     The files keep their tags and show under Items → Unmatched, ready to attach.
//   - **A manager**: marking them would last only until the next run asked the manager
//     again and got the dead ID back. The manager has to correct the album first, so a
//     job refreshes the artist there, re-reads the album's files from it, and drains
//     the queue (Runner.RepairAlbumFiles). That waits on the manager, so it is a 202.
//
// Both can apply to one album, which is split across a native and a managed library.
func (a *API) releaseMigrationFiles(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var row models.MusicbrainzMigration
	if err := a.DB.First(&row, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "migration not found"})
		return
	}
	review := migration.NewReview(a.DB, row)
	if row.EntityType != models.MigrationEntityReleaseGroup || row.Kind != models.MigrationKindDeleted || !review.Owned {
		c.JSON(http.StatusConflict, gin.H{"error": "no files on disk are holding this change"})
		return
	}

	releases, err := collection.ReleaseGroupReleaseMBIDs(a.DB, row.OldMBID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to find the album's editions"})
		return
	}
	var items []models.LibraryItem
	if len(releases) > 0 {
		if err := a.DB.Where("mb_release_id IN ?", releases).Scopes(models.InDiskView).Find(&items).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load the album's files"})
			return
		}
	}

	var native []uuid.UUID
	managed := 0
	editable := map[uuid.UUID]bool{}
	for _, item := range items {
		ok, seen := editable[item.LibraryID]
		if !seen {
			var err error
			if ok, err = a.libraryIdentityEditable(item.LibraryID); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve a library's manager"})
				return
			}
			editable[item.LibraryID] = ok
		}
		if ok {
			native = append(native, item.ID)
		} else {
			managed++
		}
	}

	if managed > 0 {
		if a.Scan == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "processor unavailable"})
			return
		}
		if off, err := collection.RefreshTurnedOff(a.DB); err == nil && len(off) > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "these files are the manager's, and artist refresh is turned off for " +
				strings.Join(off, ", ") + " — turn it on under Managers, or refresh the artist in Lidarr and re-correlate it"})
			return
		}
	}

	if len(native) > 0 {
		if err := a.DB.Model(&models.LibraryItem{}).Where("id IN ?", native).Updates(map[string]any{
			"status":               models.LibraryItemStatusUnmatched,
			"error":                "the album this file resolved to no longer exists on " + row.SourceLabel() + " — re-identify this file",
			"last_error_transient": false,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark the files"})
			return
		}
	}

	if managed > 0 {
		if _, err := migration.MarkRepairQueued(a.DB, review.ArtistMBID); err != nil {
			logger.Log.Warnf("failed to mark migrations as repairing for %s: %s", review.ArtistMBID, err.Error())
		}
		a.Scan.RepairAlbumFiles(row.OldMBID, review.ArtistMBID)
		c.JSON(http.StatusAccepted, gin.H{
			"queued":  true,
			"marked":  len(native),
			"managed": managed,
			"message": fmt.Sprintf("Asking the manager to re-read %s, then re-reading %d of its files from it. "+
				"If the manager corrects the album, the files follow and this entry settles. Follow it in Activity.",
				nameOr(review.ArtistName, "the artist"), managed),
		})
		return
	}

	// Native only: settle it now. Re-derived first, so the album no longer reads as
	// owned when the migration is applied.
	if _, err := collection.RebuildScoped(a.DB, collection.RebuildScope{ArtistMBID: review.ArtistMBID}); err != nil {
		logger.Log.Warnf("failed to re-derive %s after marking files: %s", review.ArtistMBID, err.Error())
	}
	applied, err := migration.ApplyByID(a.DB, id)
	if err != nil {
		a.recordMigrationDecision(applied, models.EventItemStatusError, err.Error())
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "marked": len(native)})
		return
	}
	a.recordMigrationDecision(applied, models.EventItemStatusMigrated, "")
	a.rebuildAfterMigration()
	c.JSON(http.StatusOK, gin.H{"marked": len(native), "migration": applied})
}

func nameOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}

// dismissMigration records that a detected change was deliberately not applied.
func (a *API) dismissMigration(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	row, err := a.dismissMigrationByID(id)
	if err != nil {
		respondErr(c, err)
		return
	}
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
