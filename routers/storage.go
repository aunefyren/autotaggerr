package routers

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/aunefyren/autotaggerr/storage"
	"github.com/gin-gonic/gin"
)

// storage returns the injected measurer, or one over the real config paths.
func (a *API) storage() *storage.Measurer {
	a.storageOnce.Do(func() {
		if a.Storage != nil {
			return
		}
		artworkDir, _ := filepath.Abs(modules.ArtworkCacheDir())
		a.Storage = storage.NewMeasurer(a.DB, storage.Paths{
			ConfigDir:  files.ConfigDir(),
			ArtworkDir: artworkDir,
			LogFiles:   logger.LogFiles,
		})
	})
	return a.Storage
}

// getStorage reports what takes space and where. A measurement is cached for
// storage.CacheFor; ?refresh=true takes a new one, for an admin only — sizing the
// database reads every page of it, which is not something any signed-in user should
// be able to make the server do on repeat.
//
// A non-admin is sent the music and the disks, not the breakdown of config/: that is
// paths and database internals, the same reason Settings is an admin's. The volume
// holding config/ keeps its total of Autotaggerr's data but not its path when no
// library shares it.
func (a *API) getStorage(c *gin.Context) {
	admin := auth.IsAdmin(c)
	report, err := a.storage().Report(admin && c.Query("refresh") == "true")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to measure storage: " + err.Error()})
		return
	}
	if !admin {
		report.App = nil
		volumes := make([]storage.Volume, len(report.Volumes))
		for i, v := range report.Volumes {
			if len(v.Libraries) == 0 {
				v.Path = ""
			}
			volumes[i] = v
		}
		report.Volumes = volumes
	}
	c.JSON(http.StatusOK, report)
}

// compactDatabase VACUUMs the database. Refused while a job is running: VACUUM holds
// the database exclusively for as long as it takes, and a scan waiting on it would
// time out its writes rather than wait.
func (a *API) compactDatabase(c *gin.Context) {
	if a.Scan != nil && a.Scan.Status().Running {
		c.JSON(http.StatusConflict, gin.H{"error": "a job is running — compact once it has finished"})
		return
	}
	a.storageOutcome(c, a.storage().Compact)
}

func (a *API) checkpointDatabase(c *gin.Context) { a.storageOutcome(c, a.storage().Checkpoint) }

// pruneActivity applies the configured Activity retention now.
func (a *API) pruneActivity(c *gin.Context) {
	keep := files.ConfigFile.AutotaggerrEventRetention
	a.storageOutcome(c, func() (storage.Outcome, error) { return a.storage().PruneActivity(keep) })
}

func (a *API) removeArtworkLeftovers(c *gin.Context) {
	a.storageOutcome(c, a.storage().RemoveArtworkLeftovers)
}

func (a *API) storageOutcome(c *gin.Context, action func() (storage.Outcome, error)) {
	out, err := action()
	switch {
	case errors.Is(err, storage.ErrNotSQLite):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusOK, out)
	}
}
