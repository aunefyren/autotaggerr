package routers

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// searchReleases proxies a MusicBrainz release search for the attach UI. It is
// rate-limited by the shared MusicBrainz limiter like every other MB call.
//
// The query is fielded (artist/release/date/country/format/tracks/…) because a
// single free-text box cannot separate the editions that actually differ, and
// results are paged for the same reason. `q` still accepts free text — and, as the
// escape hatch when search cannot surface a release at all, an MBID or a pasted
// musicbrainz.org URL, which is resolved directly instead of searched.
func (a *API) searchReleases(c *gin.Context) {
	query := metadata.ReleaseSearchQuery{
		Text:     c.Query("q"),
		Artist:   c.Query("artist"),
		ArtistID: c.Query("artist_id"),
		Release:  c.Query("release"),
		Date:     c.Query("date"),
		Country:  c.Query("country"),
		Format:   c.Query("format"),
		Status:   c.Query("status"),
		CatNo:    c.Query("catno"),
		Barcode:  c.Query("barcode"),
		Tracks:   parseIntDefault(c.Query("tracks"), 0),
		Limit:    parseIntDefault(c.Query("limit"), 0),
		Offset:   parseIntDefault(c.Query("offset"), 0),
	}
	if query.Empty() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a search query is required"})
		return
	}

	if ref, ok := modules.ParseMBIDInput(query.Text); ok {
		a.resolvePastedMBID(c, ref, query)
		return
	}

	page, err := a.meta().SearchReleases(query)
	if err != nil {
		logger.Log.Errorf("release search failed for %q: %s", query.Lucene(), err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "MusicBrainz search failed"})
		return
	}
	c.JSON(http.StatusOK, page)
}

// resolvePastedMBID turns a pasted ID or URL into the same result shape a search
// returns. An artist ID narrows the search rather than resolving to one release,
// since an artist is not something a file can be attached to.
func (a *API) resolvePastedMBID(c *gin.Context, ref modules.ParsedMBID, query metadata.ReleaseSearchQuery) {
	switch ref.Entity {
	case "artist":
		query.Text = ""
		query.ArtistID = ref.MBID
		page, err := a.meta().SearchReleases(query)
		if err != nil {
			logger.Log.Errorf("release search by artist %s failed: %s", ref.MBID, err.Error())
			c.JSON(http.StatusBadGateway, gin.H{"error": "MusicBrainz search failed"})
			return
		}
		c.JSON(http.StatusOK, page)
		return

	case "release-group":
		releases, err := a.meta().GetReleaseGroupReleases(ref.MBID)
		if err != nil {
			logger.Log.Errorf("editions lookup for release-group %s failed: %s", ref.MBID, err.Error())
			c.JSON(http.StatusBadGateway, gin.H{"error": "could not load that release group from MusicBrainz"})
			return
		}
		c.JSON(http.StatusOK, metadata.ReleaseSearchPage{Count: len(releases), Releases: releases})
		return
	}

	// "release" or a bare MBID: try the release, then fall back to treating it as a
	// release group, so pasting an ID without its URL still lands somewhere useful.
	release, err := a.meta().GetRelease(ref.MBID)
	if err == nil {
		c.JSON(http.StatusOK, metadata.ReleaseSearchPage{
			Count:    1,
			Releases: []models.MusicBrainzReleaseSearchResult{modules.SearchResultFromRelease(release)},
		})
		return
	}
	if ref.Entity == "" {
		if releases, groupErr := a.meta().GetReleaseGroupReleases(ref.MBID); groupErr == nil && len(releases) > 0 {
			c.JSON(http.StatusOK, metadata.ReleaseSearchPage{Count: len(releases), Releases: releases})
			return
		}
	}
	logger.Log.Errorf("pasted MBID %s did not resolve: %s", ref.MBID, err.Error())
	c.JSON(http.StatusNotFound, gin.H{"error": "no MusicBrainz release or release group has that ID"})
}

// releaseTracks returns a release's tracklist for the attach picker. It reads
// through the release cache, so re-opening a release the user already looked at
// costs no MusicBrainz call.
func (a *API) releaseTracks(c *gin.Context) {
	mbID := strings.TrimSpace(c.Param("mbid"))
	release, err := a.meta().GetRelease(mbID)
	if err != nil {
		logger.Log.Errorf("failed to load release %s: %s", mbID, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not load that release from MusicBrainz"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"release": gin.H{
			"mb_id":          release.ID,
			"title":          release.Title,
			"date":           release.Date,
			"country":        release.Country,
			"disambiguation": release.Disambiguation,
		},
		"tracks": modules.ReleaseTracks(release),
	})
}

// attachItem pins an unmatched file to a specific MusicBrainz release + track,
// then writes its tags. This is how a file with no usable metadata and no Lidarr
// entry gets identified.
//
// The pin is what stops automatic resolution from undoing the decision on the next
// scan. It is also self-limiting: once the tags are written the file carries the MB
// IDs itself, so it resolves natively from then on whether or not the pin survives.
func (a *API) attachItem(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}

	var body struct {
		MBReleaseID      string `json:"mb_release_id"`
		MBReleaseTrackID string `json:"mb_release_track_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	body.MBReleaseID = strings.TrimSpace(body.MBReleaseID)
	body.MBReleaseTrackID = strings.TrimSpace(body.MBReleaseTrackID)
	if body.MBReleaseID == "" || body.MBReleaseTrackID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mb_release_id and mb_release_track_id are required"})
		return
	}

	var item models.LibraryItem
	if err := a.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "library item not found"})
		return
	}

	if !a.requireIdentityEditable(c, []models.LibraryItem{item}) {
		return
	}

	// Validate the choice against the real release rather than trusting the body:
	// a wrong ID would otherwise be written into the file's tags.
	release, err := a.meta().GetRelease(body.MBReleaseID)
	if err != nil {
		logger.Log.Errorf("attach: failed to load release %s: %s", body.MBReleaseID, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not load that release from MusicBrainz"})
		return
	}
	track, found := modules.FindReleaseTrack(release, body.MBReleaseTrackID)
	if !found {
		c.JSON(http.StatusBadRequest, gin.H{"error": "that track is not part of that release"})
		return
	}

	if err := a.saveCorrelation(item.ID, release.ID, track); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save the correlation"})
		return
	}

	// Write the tags now, so the file stops being unmatched without waiting for the
	// next scan. A failure here leaves the correlation saved — it is a real decision
	// worth keeping — and is reported so the user can retry.
	written, err := a.Scan.RetagItem(item.ID)
	if err != nil {
		logger.Log.Warnf("attach: correlation saved but tagging failed for %s: %s", item.Path, err.Error())
		c.JSON(http.StatusAccepted, gin.H{
			"attached":     true,
			"tags_written": 0,
			"warning":      "Attached, but writing tags failed: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"attached": true, "tags_written": written})
}

// libraryIdentityEditable reports whether files in a library may have their MB identity
// set by hand — false for a Lidarr-managed library, where the release and track are
// Lidarr's to decide. It is the single resolver shared by the attach gate (which turns
// false into a 409) and the item listing (which turns it into a flag the UI uses to hide
// the control), so both agree on what "editable" means.
func (a *API) libraryIdentityEditable(libraryID uuid.UUID) (bool, error) {
	var library models.Library
	if err := a.DB.First(&library, "id = ?", libraryID).Error; err != nil {
		return false, err
	}
	manager, _, err := components.BuildForLibrary(a.DB, library)
	if err != nil {
		return false, err
	}
	return manager.Type() != models.ManagerTypeLidarr, nil
}

// requireIdentityEditable rejects a manual attach when any file in the set lives in a
// Lidarr-managed library. There the release and track are Lidarr's to decide, so a
// hand-attach would be reverted by the next scan; the honest answer is to refuse and
// point at Lidarr. It fails closed (500) if a manager cannot be resolved, and one
// Lidarr-governed file locks the whole action — a bulk attach is one album, and
// splitting it would be a worse surprise than rejecting it. Returns false (response
// already written) when the request must not proceed.
func (a *API) requireIdentityEditable(c *gin.Context, items []models.LibraryItem) bool {
	editableByLibrary := map[uuid.UUID]bool{}
	for _, item := range items {
		editable, seen := editableByLibrary[item.LibraryID]
		if !seen {
			var err error
			editable, err = a.libraryIdentityEditable(item.LibraryID)
			if err != nil {
				logger.Log.Warnf("attach gate: failed to resolve manager for library %s: %s", item.LibraryID, err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve the library manager"})
				return false
			}
			editableByLibrary[item.LibraryID] = editable
		}
		if !editable {
			c.JSON(http.StatusConflict, gin.H{"error": "these files are managed by Lidarr — change the release in Lidarr, not here"})
			return false
		}
	}
	return true
}

// saveCorrelation writes one validated manual attachment. Shared by the single and
// bulk paths so they cannot drift into recording different things — which is also
// why the collection rebuild is triggered here rather than in each handler: the
// disk view is derived from these rows, and a writer that forgets to re-derive
// leaves the collection reporting something the index no longer says.
func (a *API) saveCorrelation(itemID uuid.UUID, releaseID string, track modules.ReleaseTrack) error {
	now := time.Now()
	err := a.DB.Model(&models.LibraryItem{}).Where("id = ?", itemID).Updates(map[string]any{
		"mb_release_id":       releaseID,
		"mb_release_track_id": track.TrackID,
		"mb_recording_id":     track.RecordingID,
		"correlation_source":  models.CorrelationSourceManual,
		"correlated_at":       now,
		"pinned":              true,
		"status":              models.LibraryItemStatusOK,
		// A hand-picked correlation settles whatever the last automatic attempt
		// failed at, so the failure it recorded is cleared with it — leaving a dated
		// error on a file someone just identified would read as a live problem.
		"error":                "",
		"last_error_at":        nil,
		"last_error_transient": false,
	}).Error
	if err != nil {
		return err
	}

	// Coalesced and asynchronous: attaching a twelve-track folder calls this twelve
	// times, and the user should not wait on a re-derivation to see the file marked
	// as matched.
	a.Rebuilder.Request()
	return nil
}

// bulkMapping is one file → track pairing as it crosses the wire, in both the
// preview response and the attach request. An empty track ID means "skip this
// file" — the review step must be able to express "I do not know what this is".
type bulkMapping struct {
	ItemID           uuid.UUID `json:"item_id"`
	Path             string    `json:"path"`
	MBReleaseTrackID string    `json:"mb_release_track_id"`
	TrackNumber      string    `json:"track_number,omitempty"`
	TrackTitle       string    `json:"track_title,omitempty"`
	Medium           int       `json:"medium,omitempty"`
	How              string    `json:"how,omitempty"`
}

// loadItemsForBulk fetches the requested items, preserving the caller's order and
// rejecting IDs that do not exist. Ordering matters: it is what the review table
// shows, and re-ordering a mapping under the user is how the wrong track gets
// confirmed.
func (a *API) loadItemsForBulk(itemIDs []uuid.UUID) ([]models.LibraryItem, error) {
	if len(itemIDs) == 0 {
		return nil, errors.New("no files selected")
	}
	if len(itemIDs) > maxBulkAttachItems {
		return nil, fmt.Errorf("select at most %d files at a time", maxBulkAttachItems)
	}
	var found []models.LibraryItem
	if err := a.DB.Where("id IN ?", itemIDs).Find(&found).Error; err != nil {
		return nil, errors.New("failed to load the selected files")
	}
	byID := make(map[uuid.UUID]models.LibraryItem, len(found))
	for _, item := range found {
		byID[item.ID] = item
	}
	items := make([]models.LibraryItem, 0, len(itemIDs))
	for _, id := range itemIDs {
		item, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("library item %s not found", id)
		}
		items = append(items, item)
	}
	return items, nil
}

// maxBulkAttachItems bounds one bulk attach. Well above any real album (a boxed
// set is tens of tracks), low enough that a runaway selection cannot hold the scan
// guard while it rewrites thousands of files.
const maxBulkAttachItems = 200

// previewBulkAttach proposes a file → track pairing for a set of files against one
// release, without writing anything.
//
// This endpoint is the reason bulk attach is safe: mapping by filename is a guess,
// and the one way to mistag an entire album in a single click is to apply that
// guess silently. The proposal is always reviewed before attachBulk is called, and
// every pairing is validated again there.
func (a *API) previewBulkAttach(c *gin.Context) {
	var body struct {
		MBReleaseID string      `json:"mb_release_id"`
		ItemIDs     []uuid.UUID `json:"item_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	body.MBReleaseID = strings.TrimSpace(body.MBReleaseID)
	if body.MBReleaseID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mb_release_id is required"})
		return
	}

	items, err := a.loadItemsForBulk(body.ItemIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !a.requireIdentityEditable(c, items) {
		return
	}

	release, err := a.meta().GetRelease(body.MBReleaseID)
	if err != nil {
		logger.Log.Errorf("bulk preview: failed to load release %s: %s", body.MBReleaseID, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not load that release from MusicBrainz"})
		return
	}
	tracks := modules.ReleaseTracks(release)

	paths := make([]string, len(items))
	for i, item := range items {
		paths[i] = item.Path
	}
	proposed := modules.MapFilesToTracks(paths, tracks)

	mappings := make([]bulkMapping, len(items))
	for i, item := range items {
		mappings[i] = bulkMapping{
			ItemID:           item.ID,
			Path:             item.Path,
			MBReleaseTrackID: proposed[i].TrackID,
			TrackNumber:      proposed[i].TrackNumber,
			TrackTitle:       proposed[i].TrackTitle,
			Medium:           proposed[i].Medium,
			How:              proposed[i].How,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"release": gin.H{
			"mb_id":          release.ID,
			"title":          release.Title,
			"date":           release.Date,
			"country":        release.Country,
			"disambiguation": release.Disambiguation,
		},
		"tracks":   tracks,
		"mappings": mappings,
	})
}

// attachBulk attaches several files to one release in a single reviewed action —
// the folder-shaped workflow, since files arrive as albums and identifying them one
// at a time is what made manual attach unusable at scale.
//
// The release is fetched once and every track validated against it, exactly as the
// single attach does: the reviewed proposal is still a client-supplied body. Tag
// writes go through one batched re-tag holding the scan guard for the whole album.
func (a *API) attachBulk(c *gin.Context) {
	var body struct {
		MBReleaseID string        `json:"mb_release_id"`
		Mappings    []bulkMapping `json:"mappings"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	body.MBReleaseID = strings.TrimSpace(body.MBReleaseID)
	if body.MBReleaseID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mb_release_id is required"})
		return
	}

	// Skipped rows are dropped here rather than treated as an error: leaving one
	// file unidentified must not block attaching the rest of the album.
	var (
		itemIDs  []uuid.UUID
		trackIDs = map[uuid.UUID]string{}
	)
	for _, mapping := range body.Mappings {
		trackID := strings.TrimSpace(mapping.MBReleaseTrackID)
		if trackID == "" {
			continue
		}
		if _, duplicate := trackIDs[mapping.ItemID]; duplicate {
			c.JSON(http.StatusBadRequest, gin.H{"error": "a file appears twice in the mapping"})
			return
		}
		trackIDs[mapping.ItemID] = trackID
		itemIDs = append(itemIDs, mapping.ItemID)
	}

	items, err := a.loadItemsForBulk(itemIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !a.requireIdentityEditable(c, items) {
		return
	}

	release, err := a.meta().GetRelease(body.MBReleaseID)
	if err != nil {
		logger.Log.Errorf("bulk attach: failed to load release %s: %s", body.MBReleaseID, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not load that release from MusicBrainz"})
		return
	}

	// Validate every pairing before writing any of them. A half-applied album is
	// worse than a rejected one: the user reviewed the mapping as a whole.
	tracks := make(map[uuid.UUID]modules.ReleaseTrack, len(items))
	for _, item := range items {
		track, found := modules.FindReleaseTrack(release, trackIDs[item.ID])
		if !found {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("%s is mapped to a track that is not part of that release", filepath.Base(item.Path)),
			})
			return
		}
		tracks[item.ID] = track
	}

	for _, item := range items {
		if err := a.saveCorrelation(item.ID, release.ID, tracks[item.ID]); err != nil {
			logger.Log.Errorf("bulk attach: failed to save correlation for %s: %s", item.Path, err.Error())
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save the correlations"})
			return
		}
	}

	// One batched re-tag: the correlations are saved either way, so a tagging
	// failure is reported per file rather than losing the decision.
	results, err := a.Scan.RetagItems(itemIDs)
	if err != nil {
		c.JSON(http.StatusAccepted, gin.H{
			"attached": len(items), "tags_written": 0,
			"warning": "Attached, but tags were not written: " + err.Error(),
		})
		return
	}

	written, failures := 0, []gin.H{}
	for _, result := range results {
		written += result.Written
		if result.Err != nil {
			logger.Log.Warnf("bulk attach: correlation saved but tagging failed for %s: %s", result.Path, result.Err.Error())
			failures = append(failures, gin.H{"path": result.Path, "error": result.Err.Error()})
		}
	}
	if len(failures) > 0 {
		c.JSON(http.StatusAccepted, gin.H{
			"attached": len(items), "tags_written": written, "failures": failures,
			"warning": fmt.Sprintf("Attached %d file(s), but tagging failed for %d", len(items), len(failures)),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"attached": len(items), "tags_written": written})
}

// detachItem removes a manual pin, handing the file back to automatic resolution.
// The already-written tags are left alone — they are the correct tags for whatever
// it was attached to, and rewriting them is the next scan's business.
func (a *API) detachItem(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}

	var item models.LibraryItem
	if err := a.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "library item not found"})
		return
	}
	if err := a.DB.Model(&models.LibraryItem{}).Where("id = ?", item.ID).
		Update("pinned", false).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to remove the pin"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"pinned": false})
}
