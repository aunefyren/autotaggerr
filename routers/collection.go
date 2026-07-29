package routers

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/gin-gonic/gin"
)

// artistView is an artist plus whether following actually governs it. Derived
// server-side for the same reason complete/discrepancy/wanted are: one definition,
// used by the list, the detail and the UI alike. Monitored says what is *stored*;
// FollowGoverns says whether that stored flag currently decides anything.
type artistView struct {
	models.CollectionArtist
	FollowGoverns bool `json:"follow_governs"`
}

func newArtistView(artist models.CollectionArtist) artistView {
	return artistView{CollectionArtist: artist, FollowGoverns: collection.FollowGoverns(artist)}
}

type artistSummary struct {
	artistView
	OwnedCount    int `json:"owned_count"`
	CompleteCount int `json:"complete_count"`
	PartialCount  int `json:"partial_count"`
	MissingCount  int `json:"missing_count"`
	// MismatchCount is albums where the disk view and the manager's catalog view
	// disagree — worth a look, but not an error on either side.
	MismatchCount int `json:"mismatch_count"`
	// PickedCount is how many albums were explicitly asked for, as opposed to
	// wanted automatically by following the artist.
	PickedCount int `json:"picked_count"`
}

// releaseGroupView is a release-group plus the comparisons the UI renders, so the
// disk-vs-catalog and wanted rules live here rather than being reimplemented in TS.
type releaseGroupView struct {
	models.CollectionReleaseGroup
	Complete    bool   `json:"complete"`
	Discrepancy string `json:"discrepancy"`

	// Wanted and WantedSource explain *why* something is wanted, and therefore what
	// could change it: "explicit" = the user picked this album and can unpick it;
	// "auto" = it follows from following the artist; "manager" = the library's
	// manager (Lidarr) monitors it. Only an explicit want is editable on the row —
	// the other two are derived, and the UI renders them as state rather than as a
	// toggle that cannot actually be switched off.
	Wanted       bool   `json:"wanted"`
	WantedSource string `json:"wanted_source"`
	// WantedAnyEdition is the default shape of an album want: any release of the
	// group counts. Kept separate from DesiredReleases so the UI can tell "I want
	// this album" from "I want these specific editions of it".
	WantedAnyEdition bool `json:"wanted_any_edition"`
	// DesiredReleases are the specific editions asked for.
	DesiredReleases []string `json:"desired_releases"`
	// DesiredRecordings are the specific songs asked for across this group's
	// desires. Empty means the whole album or edition.
	DesiredRecordings []string `json:"desired_recordings"`
	// OwnedEditions is how many distinct editions files are owned of. The
	// owned/total counts above describe only the best-owned one, so without this a
	// row cannot say that two pressings are involved.
	OwnedEditions int `json:"owned_editions"`
}

// editionView is one MusicBrainz edition of a release-group plus what is owned of
// it. Ownership is per edition (models.CollectionRelease) because a release-group
// summary reports only its best-owned edition — the point of pass C.
type editionView struct {
	models.MusicBrainzReleaseSearchResult
	Owned       bool `json:"owned"`
	OwnedTracks int  `json:"owned_tracks"`
	// OwnedTotalTracks is the track count of the *owned* edition, which can differ
	// from what the browse response reports if MusicBrainz has since been edited.
	OwnedTotalTracks int  `json:"owned_total_tracks"`
	Complete         bool `json:"complete"`
}

// annotateEditions merges owned-edition state into the MusicBrainz edition list,
// and appends any owned edition the list does not contain — MusicBrainz can be
// unreachable, or an edition can be merged away upstream, and an edition you own
// files of must never vanish from the page that edits wants for it.
func annotateEditions(editions []models.MusicBrainzReleaseSearchResult, owned []models.CollectionRelease) []editionView {
	byMBID := make(map[string]models.CollectionRelease, len(owned))
	for _, rel := range owned {
		byMBID[rel.MBID] = rel
	}

	out := make([]editionView, 0, len(editions))
	seen := map[string]bool{}
	for _, edition := range editions {
		seen[edition.ID] = true
		view := editionView{MusicBrainzReleaseSearchResult: edition}
		if rel, ok := byMBID[edition.ID]; ok {
			view.Owned = true
			view.OwnedTracks, view.OwnedTotalTracks = rel.OwnedTracks, rel.TotalTracks
			view.Complete = rel.Complete()
		}
		out = append(out, view)
	}

	for _, rel := range owned {
		if seen[rel.MBID] {
			continue
		}
		hit := models.MusicBrainzReleaseSearchResult{
			ID: rel.MBID, Title: rel.Title, Date: rel.Date,
			Country: rel.Country, Disambiguation: rel.Disambiguation,
		}
		if rel.TotalTracks > 0 {
			hit.Media = append(hit.Media, struct {
				Format     string `json:"format"`
				TrackCount int    `json:"track-count"`
			}{Format: rel.Format, TrackCount: rel.TotalTracks})
		}
		out = append(out, editionView{
			MusicBrainzReleaseSearchResult: hit,
			Owned:                          true,
			OwnedTracks:                    rel.OwnedTracks,
			OwnedTotalTracks:               rel.TotalTracks,
			Complete:                       rel.Complete(),
		})
	}
	return out
}

// Wanted sources, in precedence order: an explicit pick outranks anything derived.
const (
	wantedSourceExplicit = "explicit"
	wantedSourceAuto     = "auto"
	wantedSourceManager  = "manager"
)

func newReleaseGroupView(
	rg models.CollectionReleaseGroup,
	hasCatalog bool,
	anyEdition bool,
	artist models.CollectionArtist,
	desiredReleases []string,
) releaseGroupView {
	explicit := anyEdition || len(desiredReleases) > 0
	source := ""
	switch {
	case explicit:
		source = wantedSourceExplicit
	// For a manager-owned artist the manager decides, and its own monitored flag is
	// the honest answer — a native follow flag would be reporting an authority the
	// page does not offer a control for.
	case !collection.FollowGoverns(artist):
		if rg.InCatalog && rg.CatalogMonitored {
			source = wantedSourceManager
		}
	case artist.Monitored && collection.FollowWantsStored(artist, rg.PrimaryType, rg.SecondaryTypes):
		source = wantedSourceAuto
	}
	if desiredReleases == nil {
		desiredReleases = []string{}
	}
	return releaseGroupView{
		CollectionReleaseGroup: rg,
		Complete:               rg.Complete(),
		Discrepancy:            rg.Discrepancy(hasCatalog),
		Wanted:                 source != "",
		WantedSource:           source,
		WantedAnyEdition:       anyEdition,
		DesiredReleases:        desiredReleases,
	}
}

// hasCatalog reports, per artist MBID, whether any release-group carries catalog
// state — i.e. whether there is a manager view to compare the disk against.
func hasCatalog(groups []models.CollectionReleaseGroup) map[string]bool {
	out := map[string]bool{}
	for _, rg := range groups {
		if rg.InCatalog {
			out[rg.ArtistMBID] = true
		}
	}
	return out
}

// listArtists returns the collection: artists with owned/missing counts.
func (a *API) listArtists(c *gin.Context) {
	var artists []models.CollectionArtist
	if err := a.DB.Order("name").Find(&artists).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list artists"})
		return
	}

	// Aggregated in Go rather than SQL so the complete/discrepancy rules have exactly
	// one definition (the model) shared by this list, the artist detail, and the UI.
	type agg struct{ total, owned, complete, mismatch int }
	byArtist := map[string]*agg{}

	var groups []models.CollectionReleaseGroup
	if err := a.DB.Find(&groups).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list release groups"})
		return
	}
	picked := map[string]map[string]bool{}
	var desires []models.CollectionDesire
	if err := a.DB.Find(&desires).Error; err == nil {
		for _, d := range desires {
			if picked[d.ArtistMBID] == nil {
				picked[d.ArtistMBID] = map[string]bool{}
			}
			// Count albums asked for, not desire rows: wanting an album and one of
			// its editions is one pick, not two.
			picked[d.ArtistMBID][d.ReleaseGroupMBID] = true
		}
	}

	catalogued := hasCatalog(groups)
	for _, rg := range groups {
		g := byArtist[rg.ArtistMBID]
		if g == nil {
			g = &agg{}
			byArtist[rg.ArtistMBID] = g
		}
		g.total++
		if rg.Owned {
			g.owned++
		}
		if rg.Complete() {
			g.complete++
		}
		if rg.Discrepancy(catalogued[rg.ArtistMBID]) != models.DiscrepancyNone {
			g.mismatch++
		}
	}

	out := make([]artistSummary, 0, len(artists))
	for _, ar := range artists {
		g := byArtist[ar.MBID]
		if g == nil {
			g = &agg{}
		}
		out = append(out, artistSummary{
			artistView:    newArtistView(ar),
			OwnedCount:    g.owned,
			CompleteCount: g.complete,
			PartialCount:  g.owned - g.complete,
			MissingCount:  g.total - g.owned,
			MismatchCount: g.mismatch,
			PickedCount:   len(picked[ar.MBID]),
		})
	}
	c.JSON(http.StatusOK, out)
}

// getArtist returns an artist and its release-groups (owned + wanted).
func (a *API) getArtist(c *gin.Context) {
	mbid := c.Param("mbid")
	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", mbid).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}
	var groups []models.CollectionReleaseGroup
	a.DB.Where("artist_mb_id = ?", mbid).Order("owned desc, first_release_date desc").Find(&groups)
	desires, err := collection.DesiresForArtist(a.DB, mbid)
	if err != nil {
		logger.Log.Warnf("failed to load desires for %s: %s", mbid, err.Error())
	}
	// An empty ReleaseMBID is a want for the release-group itself ("any edition");
	// a set one narrows the want to that edition.
	anyEdition := map[string]bool{}
	editions := map[string][]string{}
	for _, d := range desires {
		if d.ReleaseMBID == "" {
			anyEdition[d.ReleaseGroupMBID] = true
			continue
		}
		editions[d.ReleaseGroupMBID] = append(editions[d.ReleaseGroupMBID], d.ReleaseMBID)
	}

	catalogued := hasCatalog(groups)[mbid]
	editionCounts, err := collection.OwnedReleaseCounts(a.DB, mbid)
	if err != nil {
		logger.Log.Warnf("failed to count owned editions for %s: %s", mbid, err.Error())
	}
	views := make([]releaseGroupView, 0, len(groups))
	for _, rg := range groups {
		view := newReleaseGroupView(rg, catalogued, anyEdition[rg.MBID], artist, editions[rg.MBID])
		view.OwnedEditions = editionCounts[rg.MBID]
		views = append(views, view)
	}
	c.JSON(http.StatusOK, gin.H{"artist": newArtistView(artist), "release_groups": views, "desires": desires})
}

// setArtistMonitored toggles monitoring. Enabling triggers a discography sync so
// the wanted (missing) release-groups are discovered.
func (a *API) setArtistMonitored(c *gin.Context) {
	mbid := c.Param("mbid")
	var body struct {
		Monitored bool `json:"monitored"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", mbid).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}
	if err := a.DB.Model(&artist).Update("monitored", body.Monitored).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update artist"})
		return
	}

	wanted := 0
	if body.Monitored {
		n, err := collection.SyncArtist(a.DB, mbid)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to sync discography: " + err.Error()})
			return
		}
		wanted = n
	}
	c.JSON(http.StatusOK, gin.H{"monitored": body.Monitored, "wanted": wanted})
}

// rebuildCollection recomputes the owned (present) side from the index. Fast and
// network-free (reads only cached releases).
func (a *API) rebuildCollection(c *gin.Context) {
	artists, owned, err := collection.Rebuild(a.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rebuild collection"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"artists": artists, "owned_release_groups": owned})
}

// syncLidarr mirrors Lidarr's monitored/missing albums for Lidarr-managed artists
// in the background, recording the result as an Activity event.
func (a *API) syncLidarr(c *gin.Context) {
	var lidarrManagers int64
	a.DB.Model(&models.Manager{}).Where("type = ? AND enabled = ?", models.ManagerTypeLidarr, true).Count(&lidarrManagers)
	if lidarrManagers == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no enabled Lidarr manager configured"})
		return
	}

	go func() {
		ev := events.Begin(a.DB, models.EventTypeLidarrSync, "Sync from Lidarr")
		artists, groups, err := collection.SyncLidarr(a.DB)
		status := models.EventStatusOK
		if err != nil {
			status = models.EventStatusError
		}
		summary := fmt.Sprintf("%d artists synced · %d albums", artists, groups)
		details := map[string]any{"artists": artists, "albums": groups}
		if err != nil {
			details["error"] = err.Error()
		}
		events.Finish(a.DB, ev, status, summary, details)
	}()

	c.JSON(http.StatusAccepted, gin.H{"status": "lidarr sync started"})
}

// searchArtists proxies a MusicBrainz artist search, so an artist can be added
// before any of their files are owned.
func (a *API) searchArtists(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a search query is required"})
		return
	}
	results, err := modules.SearchMusicBrainzArtists(query)
	if err != nil {
		logger.Log.Errorf("artist search failed for %q: %s", query, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "MusicBrainz search failed"})
		return
	}
	c.JSON(http.StatusOK, results)
}

// addArtist adds an artist to the collection by MusicBrainz ID. Idempotent, so the
// UI can offer "add" without first checking whether it is already there.
func (a *API) addArtist(c *gin.Context) {
	var body struct {
		MBID string `json:"mb_id"`
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	artist, err := collection.AddArtist(a.DB, body.MBID, body.Name)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, artist)
}

// releaseGroupEditions lists a release-group's releases so a specific edition can
// be desired ("I want the 2017 remaster, not just the album").
func (a *API) releaseGroupEditions(c *gin.Context) {
	editions, err := collection.ReleaseGroupEditions(c.Param("mbid"))
	if err != nil {
		logger.Log.Errorf("failed to list editions for %s: %s", c.Param("mbid"), err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not list editions from MusicBrainz"})
		return
	}
	c.JSON(http.StatusOK, editions)
}

// setDesire records an explicit want for a release-group, optionally pinned to one
// specific release.
func (a *API) setDesire(c *gin.Context) {
	var body struct {
		ReleaseGroupMBID string `json:"release_group_mb_id"`
		ReleaseMBID      string `json:"release_mb_id"`
		// RecordingMBIDs narrows the want to specific songs; absent or empty means
		// the whole album or edition.
		RecordingMBIDs   []string `json:"recording_mb_ids"`
		Title            string   `json:"title"`
		PrimaryType      string   `json:"primary_type"`
		SecondaryTypes   string   `json:"secondary_types"`
		FirstReleaseDate string   `json:"first_release_date"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	desire, err := collection.SetDesire(a.DB, collection.DesireInput{
		ArtistMBID:       c.Param("mbid"),
		ReleaseGroupMBID: body.ReleaseGroupMBID,
		ReleaseMBID:      body.ReleaseMBID,
		RecordingMBIDs:   body.RecordingMBIDs,
		Title:            body.Title,
		PrimaryType:      body.PrimaryType,
		SecondaryTypes:   body.SecondaryTypes,
		FirstReleaseDate: body.FirstReleaseDate,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, desire)
}

// clearDesire drops a want. Owned files are never touched.
func (a *API) clearDesire(c *gin.Context) {
	if err := collection.ClearDesire(a.DB, c.Query("release_group_mb_id"), c.Query("release_mb_id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear the desire"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"cleared": true})
}

// discography returns everything MusicBrainz knows the artist released, annotated
// with what is owned and what is wanted.
//
// It is a live read and is deliberately **not** persisted: storing a discography
// would mark every single, live album and reissue as "should exist", inflating the
// missing count with things the user never asked for. Browsing a catalog is not the
// same as wanting it.
// artistInfoView is who an artist is, flattened for the artist page header. Kept
// separate from the artist row because none of it is stored: it is a live
// MusicBrainz read, and the page must render without it.
type artistInfoView struct {
	Type           string   `json:"type"`
	Disambiguation string   `json:"disambiguation"`
	Country        string   `json:"country"`
	Area           string   `json:"area"`
	Begin          string   `json:"begin"`
	End            string   `json:"end"`
	Ended          bool     `json:"ended"`
	Genres         []string `json:"genres"`
}

// artistInfoGenreLimit is how many genres the header shows. Enough to characterise
// an artist, few enough that the header stays a header.
const artistInfoGenreLimit = 4

// artistInfo returns the MusicBrainz artist entity behind a collection artist.
//
// Its own endpoint rather than part of getArtist: this is a rate-limited external
// call, and the page's own data must not wait on it. A failure is a 502 the UI
// ignores — the header simply shows less.
func (a *API) artistInfo(c *gin.Context) {
	mbid := c.Param("mbid")

	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", mbid).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}

	info, err := modules.GetMusicBrainzArtist(mbid)
	if err != nil {
		logger.Log.Warnf("failed to load artist info for %s: %s", mbid, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not load the artist from MusicBrainz"})
		return
	}

	// Genres beat tags: tags are a free-for-all ("seen live", "favourites"), while
	// genres are a curated vocabulary. Tags are the fallback only when an artist has
	// no genres at all, which is common for smaller artists.
	ranked := info.Genres
	if len(ranked) == 0 {
		ranked = info.Tags
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Count > ranked[j].Count })
	genres := make([]string, 0, artistInfoGenreLimit)
	for _, g := range ranked {
		if len(genres) == artistInfoGenreLimit {
			break
		}
		if g.Name != "" {
			genres = append(genres, g.Name)
		}
	}

	// Begin area is more specific than country and is what people actually mean by
	// where a band is from; country is the fallback.
	area := info.BeginArea.Name
	if area == "" {
		area = info.Area.Name
	}

	c.JSON(http.StatusOK, artistInfoView{
		Type:           info.Type,
		Disambiguation: info.Disambiguation,
		Country:        info.Country,
		Area:           area,
		Begin:          info.LifeSpan.Begin,
		End:            info.LifeSpan.End,
		Ended:          info.LifeSpan.Ended,
		Genres:         genres,
	})
}

func (a *API) discography(c *gin.Context) {
	mbid := c.Param("mbid")

	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", mbid).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}

	groups, err := modules.GetArtistDiscography(mbid)
	if err != nil {
		logger.Log.Errorf("failed to load discography for %s: %s", mbid, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not load the discography from MusicBrainz"})
		return
	}

	// Stored state, keyed by release-group, so the live list can be annotated.
	var stored []models.CollectionReleaseGroup
	a.DB.Where("artist_mb_id = ?", mbid).Find(&stored)
	byMBID := map[string]models.CollectionReleaseGroup{}
	for _, rg := range stored {
		byMBID[rg.MBID] = rg
	}
	catalogued := hasCatalog(stored)[mbid]

	desires, err := collection.DesiresForArtist(a.DB, mbid)
	if err != nil {
		logger.Log.Warnf("failed to load desires for %s: %s", mbid, err.Error())
	}
	anyEdition := map[string]bool{}
	editions := map[string][]string{}
	recordings := map[string][]string{}
	for _, d := range desires {
		if d.ReleaseMBID == "" {
			anyEdition[d.ReleaseGroupMBID] = true
		} else {
			editions[d.ReleaseGroupMBID] = append(editions[d.ReleaseGroupMBID], d.ReleaseMBID)
		}
		recordings[d.ReleaseGroupMBID] = append(recordings[d.ReleaseGroupMBID], d.RecordingMBIDs...)
	}

	editionCounts, err := collection.OwnedReleaseCounts(a.DB, mbid)
	if err != nil {
		logger.Log.Warnf("failed to count owned editions for %s: %s", mbid, err.Error())
	}

	out := make([]releaseGroupView, 0, len(groups))
	for _, g := range groups {
		rg, known := byMBID[g.ID]
		if !known {
			// Not in the collection: carry the MusicBrainz metadata so the row still
			// renders, with everything owned left at zero.
			rg = models.CollectionReleaseGroup{
				MBID: g.ID, ArtistMBID: mbid, Title: g.Title,
				PrimaryType: g.PrimaryType, SecondaryTypes: strings.Join(g.SecondaryTypes, ", "),
				FirstReleaseDate: g.FirstReleaseDate,
			}
		}
		view := newReleaseGroupView(rg, catalogued, anyEdition[g.ID], artist, editions[g.ID])
		view.DesiredRecordings = recordings[g.ID]
		view.OwnedEditions = editionCounts[g.ID]
		out = append(out, view)
	}

	c.JSON(http.StatusOK, out)
}

// updateFollow changes what following this artist auto-wants, then re-syncs so the
// missing list matches the new settings immediately.
func (a *API) updateFollow(c *gin.Context) {
	mbid := c.Param("mbid")
	var body struct {
		Monitored       *bool   `json:"monitored"`
		FollowTypes     *string `json:"follow_types"`
		FollowSecondary *bool   `json:"follow_secondary"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", mbid).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}

	updates := map[string]any{}
	if body.Monitored != nil {
		updates["monitored"] = *body.Monitored
	}
	if body.FollowTypes != nil {
		updates["follow_types"] = strings.TrimSpace(*body.FollowTypes)
	}
	if body.FollowSecondary != nil {
		updates["follow_secondary"] = *body.FollowSecondary
	}
	if len(updates) > 0 {
		if err := a.DB.Model(&artist).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update the artist"})
			return
		}
	}

	// Re-read so the sync uses the settings just saved.
	if err := a.DB.Where("mb_id = ?", mbid).First(&artist).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reload the artist"})
		return
	}

	wanted := 0
	if artist.Monitored {
		n, err := collection.SyncArtist(a.DB, mbid)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to sync discography: " + err.Error()})
			return
		}
		wanted = n
	}
	c.JSON(http.StatusOK, gin.H{"artist": artist, "wanted": wanted})
}

// releaseGroupDetail returns everything the release-group page needs in one call:
// the group with its owned/wanted state, every edition MusicBrainz lists, and the
// desires that exist for it. Tracklists stay a separate, lazy call — fetching one
// per edition up front would cost a rate-limited second each.
func (a *API) releaseGroupDetail(c *gin.Context) {
	artistMBID := c.Param("mbid")
	rgMBID := c.Param("rgid")

	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}

	// Prefer the stored row (it carries ownership); fall back to the cached
	// discography so a release-group nobody owns still renders.
	var rg models.CollectionReleaseGroup
	if err := a.DB.Where("mb_id = ?", rgMBID).First(&rg).Error; err != nil {
		rg = models.CollectionReleaseGroup{MBID: rgMBID, ArtistMBID: artistMBID}
		if groups, dErr := modules.GetArtistDiscography(artistMBID); dErr == nil {
			for _, g := range groups {
				if g.ID == rgMBID {
					rg.Title = g.Title
					rg.PrimaryType = g.PrimaryType
					rg.SecondaryTypes = strings.Join(g.SecondaryTypes, ", ")
					rg.FirstReleaseDate = g.FirstReleaseDate
					break
				}
			}
		}
	}

	var siblings []models.CollectionReleaseGroup
	a.DB.Where("artist_mb_id = ?", artistMBID).Find(&siblings)
	catalogued := hasCatalog(siblings)[artistMBID]

	desires, err := collection.DesiresForArtist(a.DB, artistMBID)
	if err != nil {
		logger.Log.Warnf("failed to load desires for %s: %s", artistMBID, err.Error())
	}
	mine := make([]models.CollectionDesire, 0, len(desires))
	anyEdition := false
	var editionIDs []string
	var recordings []string
	for _, d := range desires {
		if d.ReleaseGroupMBID != rgMBID {
			continue
		}
		mine = append(mine, d)
		if d.ReleaseMBID == "" {
			anyEdition = true
		} else {
			editionIDs = append(editionIDs, d.ReleaseMBID)
		}
		recordings = append(recordings, d.RecordingMBIDs...)
	}

	view := newReleaseGroupView(rg, catalogued, anyEdition, artist, editionIDs)
	view.DesiredRecordings = recordings

	editions, err := collection.ReleaseGroupEditions(rgMBID)
	if err != nil {
		logger.Log.Errorf("failed to list editions for %s: %s", rgMBID, err.Error())
		// The page is still useful without the edition list; report it as empty
		// rather than failing the whole request.
		editions = nil
	}
	owned, err := collection.OwnedReleases(a.DB, rgMBID)
	if err != nil {
		logger.Log.Warnf("failed to load owned editions for %s: %s", rgMBID, err.Error())
	}
	view.OwnedEditions = len(owned)

	c.JSON(http.StatusOK, gin.H{
		"artist":        newArtistView(artist),
		"release_group": view,
		"editions":      annotateEditions(editions, owned),
		"desires":       mine,
	})
}
