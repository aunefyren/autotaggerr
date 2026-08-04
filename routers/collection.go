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
	// IdentityEditable is false when a manager (Lidarr, or the mixed case) owns this
	// artist's MB identity: the UI uses it to hide the attach / choose-edition / want
	// controls, which the API would reject anyway (see requireIdentityEditable).
	IdentityEditable bool `json:"identity_editable"`
}

func newArtistView(artist models.CollectionArtist) artistView {
	return artistView{
		CollectionArtist: artist,
		FollowGoverns:    collection.FollowGoverns(artist),
		IdentityEditable: collection.IdentityEditable(artist),
	}
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
	// IdentityEditable is false when the page's artist is manager-owned (Lidarr /
	// mixed): the row's edition and want controls are then Lidarr's to decide, so the
	// UI renders them as state rather than editable choices.
	IdentityEditable bool `json:"identity_editable"`
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

// newReleaseGroupView takes the group's desire *rows* rather than the three things
// derived from them (any-edition, editions, recordings), for two reasons.
//
// The first is that splitting them at the call site was forgettable: two of the three
// callers passed recordings and the third did not, so `GET /artists/:mbid` reported
// "whole album" for a want that had specific tracks while the discography endpoint
// reported it correctly — and the UI falls back from one to the other when
// MusicBrainz is down, so the two must agree. One argument cannot disagree with
// itself.
//
// The second is that the split threw away the row's provenance, and provenance is
// what decides whether the row is a toggle or a state. Only a hand-authored want is
// explicit; a want the rebuild narrowed or the manager selected is derived, and
// reporting it as explicit offered an unpick control that does not do what it says.
func newReleaseGroupView(
	rg models.CollectionReleaseGroup,
	catalogChecked bool,
	artist models.CollectionArtist,
	desires []models.CollectionDesire,
) releaseGroupView {
	// Empty slices, not nil: the UI reads .length on both, and a JSON null would
	// make every row check for it.
	desiredReleases := []string{}
	desiredRecordings := []string{}
	anyEdition := false
	var manual, manager, auto bool
	for _, d := range desires {
		if d.ReleaseMBID == "" {
			anyEdition = true
		} else {
			desiredReleases = append(desiredReleases, d.ReleaseMBID)
		}
		// Across every desire of the group, whichever edition each names: the row
		// reports what songs were asked for, not which edition they came from.
		desiredRecordings = append(desiredRecordings, d.RecordingMBIDs...)

		switch d.Source {
		case models.DesireSourceManager:
			manager = true
		case models.DesireSourceAuto:
			auto = true
		default:
			manual = true
		}
	}

	source := ""
	switch {
	// An authored pick outranks everything derived: it survives unfollowing, a
	// manager change, or the manager dropping the album.
	case manual:
		source = wantedSourceExplicit
	// A row the manager selected, and the manager's own monitoring, are the same
	// claim from the same authority — a native follow flag would be reporting an
	// authority the page does not offer a control for.
	case manager:
		source = wantedSourceManager
	// Narrowed from an "any edition" want to the edition whose files landed. Still
	// the user's want, but the rebuild maintains which edition it names, so the row
	// is state rather than a toggle.
	case auto:
		source = wantedSourceAuto
	case !collection.FollowGoverns(artist):
		if rg.InCatalog && rg.CatalogMonitored {
			source = wantedSourceManager
		}
	case artist.Monitored && collection.FollowWantsStored(artist, rg.PrimaryType, rg.SecondaryTypes):
		source = wantedSourceAuto
	}
	return releaseGroupView{
		CollectionReleaseGroup: rg,
		Complete:               rg.Complete(),
		Discrepancy:            rg.Discrepancy(catalogChecked),
		Wanted:                 source != "",
		WantedSource:           source,
		WantedAnyEdition:       anyEdition,
		DesiredReleases:        desiredReleases,
		DesiredRecordings:      desiredRecordings,
		IdentityEditable:       collection.IdentityEditable(artist),
	}
}

// catalogChecked reports, per artist MBID, whether a manager has been asked what that
// artist's catalogue holds — collection.CatalogChecked applied across a list, so the
// overview and the detail pages cannot disagree about it.
func catalogChecked(artists []models.CollectionArtist) map[string]bool {
	out := make(map[string]bool, len(artists))
	for _, artist := range artists {
		out[artist.MBID] = collection.CatalogChecked(artist)
	}
	return out
}

// desiresByGroup buckets an artist's wants by release-group, which is the shape
// newReleaseGroupView takes. A group with no wants is simply absent, and a nil slice
// is the correct "nothing wanted" input.
func desiresByGroup(desires []models.CollectionDesire) map[string][]models.CollectionDesire {
	out := make(map[string][]models.CollectionDesire, len(desires))
	for _, d := range desires {
		out[d.ReleaseGroupMBID] = append(out[d.ReleaseGroupMBID], d)
	}
	return out
}

// creditedArtists is collection.CreditedArtists — kept as a local alias because it is
// used per row in two loops here and the qualified name buries them.
func creditedArtists(rg models.CollectionReleaseGroup, credits map[string][]string) []string {
	return collection.CreditedArtists(rg, credits)
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
			// Hand-authored rows only: a want the rebuild narrowed or the manager
			// selected is not something this user picked, and counting it would put
			// a "picked" number on a Lidarr artist nobody has picked anything for.
			if d.Derived() {
				continue
			}
			if picked[d.ArtistMBID] == nil {
				picked[d.ArtistMBID] = map[string]bool{}
			}
			// Count albums asked for, not desire rows: wanting an album and one of
			// its editions is one pick, not two.
			picked[d.ArtistMBID][d.ReleaseGroupMBID] = true
		}
	}

	// A collaboration counts towards every artist credited on it, so it appears on
	// both artists' rows instead of only the one named in artist_mb_id.
	rgMBIDs := make([]string, 0, len(groups))
	for _, rg := range groups {
		rgMBIDs = append(rgMBIDs, rg.MBID)
	}
	credits, err := collection.ArtistsByReleaseGroup(a.DB, rgMBIDs)
	if err != nil {
		logger.Log.Warnf("failed to load release-group artist links: %s", err.Error())
	}

	catalogued := catalogChecked(artists)
	for _, rg := range groups {
		for _, artistMBID := range creditedArtists(rg, credits) {
			g := byArtist[artistMBID]
			if g == nil {
				g = &agg{}
				byArtist[artistMBID] = g
			}
			g.total++
			if rg.Owned {
				g.owned++
			}
			if rg.Complete() {
				g.complete++
			}
			if rg.Discrepancy(catalogued[artistMBID]) != models.DiscrepancyNone {
				g.mismatch++
			}
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
	// Through the credit links, not artist_mb_id: a collaboration belongs on both
	// artists' pages, and only one of them can be named in that column.
	groups, err := collection.ReleaseGroupsForArtist(a.DB, mbid)
	if err != nil {
		logger.Log.Warnf("failed to load release-groups for %s: %s", mbid, err.Error())
	}

	rgMBIDs := make([]string, 0, len(groups))
	for _, rg := range groups {
		rgMBIDs = append(rgMBIDs, rg.MBID)
	}

	desires, err := collection.DesiresForArtist(a.DB, mbid, rgMBIDs)
	if err != nil {
		logger.Log.Warnf("failed to load desires for %s: %s", mbid, err.Error())
	}
	byGroup := desiresByGroup(desires)

	// Whether there is a manager view to compare against is a property of the artist,
	// not of whichever of their albums happens to have been mirrored.
	catalogued := collection.CatalogChecked(artist)
	// Counted by release-group rather than by artist: an edition of a collaboration
	// is stored under its primary credit, so filtering by artist hid it from the
	// other one.
	editionCounts, err := collection.OwnedReleaseCounts(a.DB, rgMBIDs)
	if err != nil {
		logger.Log.Warnf("failed to count owned editions for %s: %s", mbid, err.Error())
	}
	views := make([]releaseGroupView, 0, len(groups))
	for _, rg := range groups {
		view := newReleaseGroupView(rg, catalogued, artist, byGroup[rg.MBID])
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
		n, err := collection.SyncArtist(a.DB, a.meta(), mbid)
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
	stats, err := collection.Rebuild(a.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rebuild collection"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"artists":              stats.Artists,
		"owned_release_groups": stats.Owned,
		"credit_changes":       stats.CreditChanges,
	})
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
	results, err := a.meta().SearchArtists(query)
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
	editions, err := collection.ReleaseGroupEditions(a.meta(), c.Param("mbid"))
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

	if !a.requireArtistIdentityEditable(c, c.Param("mbid")) {
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

// requireArtistIdentityEditable rejects a want when the artist is managed by Lidarr
// (or mixed): under "Lidarr owns identity" what is wanted and which edition is Lidarr's
// call, mirrored into the collection by the sync. It fails closed (500) if the artist's
// manager cannot be resolved. Returns false (response already written) when blocked.
func (a *API) requireArtistIdentityEditable(c *gin.Context, artistMBID string) bool {
	editable, err := collection.ArtistIdentityEditable(a.DB, artistMBID)
	if err != nil {
		logger.Log.Warnf("desire gate: failed to resolve identity authority for %s: %s", artistMBID, err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve the artist's manager"})
		return false
	}
	if !editable {
		c.JSON(http.StatusConflict, gin.H{"error": "this artist is managed by Lidarr — set what is wanted in Lidarr, not here"})
		return false
	}
	return true
}

// clearDesire drops a want. Owned files are never touched.
//
// Unlike setDesire this is deliberately *not* gated on the manager: clearing is a pure
// removal, and a want left over from before an artist became Lidarr-managed needs a way
// out. Blocking it would strand stale rows with no UI to remove them — the same reason
// detach stays allowed while attach is gated.
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

	info, err := a.meta().GetArtist(mbid)
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

	// Stored state, keyed by release-group, so the live list can be annotated. Read
	// through the credit links so a collaboration this artist is credited on is
	// annotated here too, rather than looking absent from the collection.
	stored, err := collection.ReleaseGroupsForArtist(a.DB, mbid)
	if err != nil {
		logger.Log.Warnf("failed to load stored release-groups for %s: %s", mbid, err.Error())
	}
	byMBID := map[string]models.CollectionReleaseGroup{}
	storedMBIDs := make([]string, 0, len(stored))
	for _, rg := range stored {
		byMBID[rg.MBID] = rg
		storedMBIDs = append(storedMBIDs, rg.MBID)
	}
	catalogued := collection.CatalogChecked(artist)

	desires, err := collection.DesiresForArtist(a.DB, mbid, storedMBIDs)
	if err != nil {
		logger.Log.Warnf("failed to load desires for %s: %s", mbid, err.Error())
	}
	byGroup := desiresByGroup(desires)

	editionCounts, err := collection.OwnedReleaseCounts(a.DB, storedMBIDs)
	if err != nil {
		logger.Log.Warnf("failed to count owned editions for %s: %s", mbid, err.Error())
	}

	out := make([]releaseGroupView, 0, len(groups))
	seen := make(map[string]bool, len(groups))
	for _, g := range groups {
		seen[g.ID] = true
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
		view := newReleaseGroupView(rg, catalogued, artist, byGroup[g.ID])
		view.OwnedEditions = editionCounts[g.ID]
		out = append(out, view)
	}

	// Add any release-group the collection owns that the live discography did not list.
	// The live list is capped (five pages) and, for the "Various Artists" placeholder,
	// deliberately empty — so owned compilations would otherwise vanish from the page.
	for _, rg := range stored {
		if seen[rg.MBID] {
			continue
		}
		view := newReleaseGroupView(rg, catalogued, artist, byGroup[rg.MBID])
		view.OwnedEditions = editionCounts[rg.MBID]
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
		n, err := collection.SyncArtist(a.DB, a.meta(), mbid)
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

	siblings, err := collection.ReleaseGroupsForArtist(a.DB, artistMBID)
	if err != nil {
		logger.Log.Warnf("failed to load release-groups for %s: %s", artistMBID, err.Error())
	}
	siblingMBIDs := make([]string, 0, len(siblings))
	for _, sib := range siblings {
		siblingMBIDs = append(siblingMBIDs, sib.MBID)
	}
	catalogued := collection.CatalogChecked(artist)

	desires, err := collection.DesiresForArtist(a.DB, artistMBID, siblingMBIDs)
	if err != nil {
		logger.Log.Warnf("failed to load desires for %s: %s", artistMBID, err.Error())
	}
	mine := make([]models.CollectionDesire, 0, len(desires))
	for _, d := range desires {
		if d.ReleaseGroupMBID == rgMBID {
			mine = append(mine, d)
		}
	}

	view := newReleaseGroupView(rg, catalogued, artist, mine)

	editions, err := collection.ReleaseGroupEditions(a.meta(), rgMBID)
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
