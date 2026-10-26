package routers

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/migration"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
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
	// Detachable is whether there is an authority to take back — derived here rather
	// than inferred in the UI from managed_by, so the button and the endpoint agree on
	// when detaching is a meaningful thing to offer.
	Detachable bool `json:"detachable"`
}

func newArtistView(artist models.CollectionArtist) artistView {
	return artistView{
		CollectionArtist: artist,
		FollowGoverns:    collection.FollowGoverns(artist),
		IdentityEditable: collection.IdentityEditable(artist),
		Detachable:       collection.Detachable(artist),
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
	// LibraryIDs are the libraries holding any of the artist's albums, so the list
	// can be filtered by library and say where an artist lives.
	LibraryIDs []uuid.UUID `json:"library_ids"`
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
	// Libraries says where the album lives: one entry per library holding files of
	// it. Empty for an album nobody owns.
	Libraries []collection.LibraryCopy `json:"libraries"`
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
	// Files is how many files resolve to this edition across every library; more
	// than OwnedTracks means duplicate copies.
	Files int `json:"files"`
	// Libraries says which libraries hold this edition, and how much of it.
	Libraries []collection.LibraryCopy `json:"libraries"`
}

// annotateEditions merges owned-edition state into the MusicBrainz edition list,
// and appends any owned edition the list does not contain — MusicBrainz can be
// unreachable, or an edition can be merged away upstream, and an edition you own
// files of must never vanish from the page that edits wants for it.
func annotateEditions(editions []models.MusicBrainzReleaseSearchResult, owned []models.CollectionRelease, copies map[string][]collection.LibraryCopy) []editionView {
	byMBID := make(map[string]models.CollectionRelease, len(owned))
	for _, rel := range owned {
		byMBID[rel.MBID] = rel
	}

	out := make([]editionView, 0, len(editions))
	seen := map[string]bool{}
	for _, edition := range editions {
		seen[edition.ID] = true
		view := editionView{MusicBrainzReleaseSearchResult: edition, Libraries: librariesOf(copies, edition.ID)}
		if rel, ok := byMBID[edition.ID]; ok {
			view.Owned = true
			view.OwnedTracks, view.OwnedTotalTracks = rel.OwnedTracks, rel.TotalTracks
			view.Complete = rel.Complete()
			view.Files = rel.Files
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
			Files:                          rel.Files,
			Libraries:                      librariesOf(copies, rel.MBID),
		})
	}
	return out
}

// librariesOf is one key's copies, never nil: the UI reads .length on it.
func librariesOf(copies map[string][]collection.LibraryCopy, key string) []collection.LibraryCopy {
	if c := copies[key]; c != nil {
		return c
	}
	return []collection.LibraryCopy{}
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
	case artist.Monitored && collection.FollowWantsStored(artist, rg.PrimaryType, rg.SecondaryTypes, rg.FirstReleaseDate):
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
		Libraries:              []collection.LibraryCopy{},
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

// artistListCache holds the last computed collection list and the fingerprint of the
// tables it was computed from.
type artistListCache struct {
	mu          sync.Mutex
	fingerprint string
	list        []artistSummary
}

// collectionFingerprint names the state of the five tables the collection list is
// computed from: each one's row count and newest updated_at. An insert or an update
// moves the newest stamp, a delete moves the count, so any write the list could show
// changes it — four aggregate queries instead of loading every row of all four.
//
// It holds because every write to these tables goes through GORM, which stamps
// updated_at on Create, Save and Updates alike. A raw Exec or UpdateColumn would slip
// past it; there are none, and one added later would show up as a list that does not
// change until the next unrelated write.
func collectionFingerprint(db *gorm.DB) (string, error) {
	var parts []string
	for _, model := range []any{&models.CollectionArtist{}, &models.CollectionReleaseGroup{},
		&models.CollectionDesire{}, &models.CollectionReleaseGroupArtist{}, &models.CollectionReleaseCopy{}} {
		// Newest as `any`: an aggregate over a time column comes back as text from
		// SQLite and as a timestamp elsewhere, and all the fingerprint needs is that the
		// same state prints the same way twice.
		var n int64
		var newest any
		if err := db.Model(model).Select("count(*), max(updated_at)").Row().Scan(&n, &newest); err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%d@%v", n, newest))
	}
	return strings.Join(parts, "|"), nil
}

// listArtists returns the collection: artists with owned/missing counts.
//
// The list is computed from every artist, release-group, want and credit link — the
// complete/discrepancy rules have one definition, shared with the artist page, and
// it runs in Go — so its cost is the size of the collection. The answer only changes
// when one of those tables does, which is rarely between two page loads, so it is kept
// and served again until collectionFingerprint says otherwise.
func (a *API) listArtists(c *gin.Context) {
	out, err := a.artistList()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, out)
}

// artistList is the collection list behind GET /artists and the MCP list_artists
// tool: served from the memo while collectionFingerprint says nothing changed,
// computed afresh otherwise. Its errors are safe to show a client.
func (a *API) artistList() ([]artistSummary, error) {
	fingerprint, err := collectionFingerprint(a.DB)
	if err == nil {
		a.artists.mu.Lock()
		if a.artists.fingerprint == fingerprint && a.artists.list != nil {
			list := a.artists.list
			a.artists.mu.Unlock()
			return list, nil
		}
		a.artists.mu.Unlock()
	} else {
		logger.Log.Warnf("failed to fingerprint the collection, computing the list afresh: %s", err.Error())
	}

	out, err := a.computeArtistList()
	if err != nil {
		return nil, err
	}
	if fingerprint != "" {
		a.artists.mu.Lock()
		a.artists.fingerprint, a.artists.list = fingerprint, out
		a.artists.mu.Unlock()
	}
	return out, nil
}

// computeArtistList is the uncached body of artistList. The store's own error is
// logged here and replaced by one naming what could not be read, which is what the
// caller shows.
func (a *API) computeArtistList() ([]artistSummary, error) {
	var artists []models.CollectionArtist
	if err := a.DB.Order("name").Find(&artists).Error; err != nil {
		logger.Log.Errorf("failed to list artists: %s", err.Error())
		return nil, errors.New("failed to list artists")
	}

	// Aggregated in Go rather than SQL so the complete/discrepancy rules have exactly
	// one definition (the model) shared by this list, the artist detail, and the UI.
	type agg struct{ total, owned, complete, mismatch int }
	byArtist := map[string]*agg{}

	var groups []models.CollectionReleaseGroup
	if err := a.DB.Find(&groups).Error; err != nil {
		logger.Log.Errorf("failed to list release groups: %s", err.Error())
		return nil, errors.New("failed to list release groups")
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

	groupLibraries, err := collection.LibrariesByReleaseGroup(a.DB)
	if err != nil {
		logger.Log.Warnf("failed to load which libraries hold each album: %s", err.Error())
	}
	artistLibraries := map[string]map[uuid.UUID]bool{}

	catalogued := catalogChecked(artists)
	for _, rg := range groups {
		for _, artistMBID := range creditedArtists(rg, credits) {
			g := byArtist[artistMBID]
			if g == nil {
				g = &agg{}
				byArtist[artistMBID] = g
			}
			for _, lib := range groupLibraries[rg.MBID] {
				if artistLibraries[artistMBID] == nil {
					artistLibraries[artistMBID] = map[uuid.UUID]bool{}
				}
				artistLibraries[artistMBID][lib] = true
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
			LibraryIDs:    sortedLibraryIDs(artistLibraries[ar.MBID]),
		})
	}
	return out, nil
}

// sortedLibraryIDs is a set of library IDs as a stable, never-nil list.
func sortedLibraryIDs(set map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// errArtistNotFound is the query methods' "no such artist in the collection".
var errArtistNotFound = errors.New("artist not found")

// getArtist returns an artist and its release-groups (owned + wanted).
func (a *API) getArtist(c *gin.Context) {
	detail, err := a.artistDetail(c.Param("mbid"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	body := gin.H{"artist": detail.Artist, "release_groups": detail.ReleaseGroups, "desires": detail.Desires}
	if detail.Repair != nil {
		body["repair"] = detail.Repair
	}
	c.JSON(http.StatusOK, body)
}

// artistDetailData is what GET /artists/:mbid and the MCP get_artist tool share.
type artistDetailData struct {
	Artist        artistView
	ReleaseGroups []releaseGroupView
	Desires       []models.CollectionDesire
	Repair        *artistRepairView
}

// artistDetail loads one artist with its release-groups judged by the shared rules.
// The only error is errArtistNotFound: the parts after the artist degrade to empty
// with a warning, as the page always has.
func (a *API) artistDetail(mbid string) (artistDetailData, error) {
	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", mbid).First(&artist).Error; err != nil {
		return artistDetailData{}, errArtistNotFound
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
	copies, err := collection.ReleaseGroupCopies(a.DB, rgMBIDs)
	if err != nil {
		logger.Log.Warnf("failed to load which libraries hold %s's albums: %s", mbid, err.Error())
	}
	views := make([]releaseGroupView, 0, len(groups))
	for _, rg := range groups {
		view := newReleaseGroupView(rg, catalogued, artist, byGroup[rg.MBID])
		view.OwnedEditions = editionCounts[rg.MBID]
		view.Libraries = librariesOf(copies, rg.MBID)
		views = append(views, view)
	}
	return artistDetailData{
		Artist:        newArtistView(artist),
		ReleaseGroups: views,
		Desires:       desires,
		Repair:        a.artistRepair(mbid),
	}, nil
}

// markSameAlbum records that this album row is the same album as another of the
// artist's, under a new ID, and applies it: the wants move across and the old row is
// retired (collection.MarkSameAlbum). For the duplicates the sync's own re-key
// detection could not pair. A refusal is a 409 naming what has to change first.
func (a *API) markSameAlbum(c *gin.Context) {
	var body struct {
		ReleaseGroupMBID string `json:"release_group_mb_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.ReleaseGroupMBID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "release_group_mb_id is required"})
		return
	}
	oldMBID := c.Param("rgid")
	var old models.CollectionReleaseGroup
	if err := a.DB.Where("mb_id = ? AND artist_mb_id = ?", oldMBID, c.Param("mbid")).First(&old).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "album not found under this artist"})
		return
	}

	row, err := collection.MarkSameAlbum(a.DB, oldMBID, strings.TrimSpace(body.ReleaseGroupMBID))
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	applied, err := migration.ApplyByID(a.DB, row.ID)
	if err != nil {
		// Recorded, not applied: something still claims the old row (files on disk,
		// most likely). The row waits on Migrations with that reason, like any other.
		a.recordMigrationDecision(applied, models.EventItemStatusError, err.Error())
		c.JSON(http.StatusAccepted, gin.H{"applied": false, "reason": err.Error(), "migration": applied})
		return
	}
	a.recordMigrationDecision(applied, models.EventItemStatusMigrated, "")
	a.rebuildAfterMigration()
	c.JSON(http.StatusOK, gin.H{"applied": true, "migration": applied})
}

// managerArtistCandidates lists the artists a manager holds that the collection does
// not — what an artist the manager stopped listing may have become. Asked live, when
// the join dialog opens.
func (a *API) managerArtistCandidates(c *gin.Context) {
	candidates, err := collection.ManagerArtistCandidates(a.DB)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not read the manager's artists: " + err.Error()})
		return
	}
	if candidates == nil {
		candidates = []collection.ManagerCandidate{}
	}
	c.JSON(http.StatusOK, candidates)
}

// markSameArtist joins an artist the manager no longer lists to the artist it lists
// now, and applies it: albums, editions and wants re-pointed, follow settings merged
// — the artist-merge path. A person chose the pair, so it is applied rather than held.
func (a *API) markSameArtist(c *gin.Context) {
	var body struct {
		ArtistMBID string `json:"artist_mb_id"`
		Name       string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.ArtistMBID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "artist_mb_id is required"})
		return
	}
	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", c.Param("mbid")).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}
	row, err := collection.MarkSameArtist(a.DB, artist.MBID, strings.TrimSpace(body.ArtistMBID), artist.Name)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	applied, err := migration.ApplyByID(a.DB, row.ID)
	if err != nil {
		a.recordMigrationDecision(applied, models.EventItemStatusError, err.Error())
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	a.recordMigrationDecision(applied, models.EventItemStatusMigrated, "")
	a.rebuildAfterMigration()
	c.JSON(http.StatusOK, gin.H{"applied": true, "artist_mb_id": applied.NewMBID})
}

// artistRepairView is what the artist page needs to offer a manager refresh: which of
// the artist's albums hold an ID that resolves nowhere, and — when nobody can be asked
// — the managers whose setting is in the way.
type artistRepairView struct {
	Albums []string `json:"albums"`
	HeldBy []string `json:"held_by,omitempty"`
	Queued bool     `json:"queued"`
}

// artistRepair answers for the artist page, or nil when the artist holds nothing to
// repair. Best-effort: a failed lookup costs the offer, never the page.
func (a *API) artistRepair(artistMBID string) *artistRepairView {
	albums, err := collection.ArtistGhostReleaseGroups(a.DB, artistMBID)
	if err != nil {
		logger.Log.Warnf("failed to list unresolvable albums for %s: %s", artistMBID, err.Error())
		return nil
	}
	if len(albums) == 0 {
		return nil
	}
	view := &artistRepairView{Albums: albums}
	if view.HeldBy, err = collection.RefreshTurnedOff(a.DB); err != nil {
		logger.Log.Warnf("failed to read manager refresh settings: %s", err.Error())
	}
	var queued int64
	a.DB.Model(&models.MusicbrainzMigration{}).
		Where("entity_type = ? AND old_mb_id IN ? AND repair_queued_at IS NOT NULL", models.MigrationEntityReleaseGroup, albums).
		Count(&queued)
	view.Queued = queued > 0
	return view
}

// repairArtistAlbums asks the manager to re-read one artist whose albums hold IDs
// that resolve nowhere — the Migrations page's *Ask the manager*, offered on the
// artist page for someone who has not opened that page. Same verb, same marks, same
// 202: the refresh is waited on for minutes, so the request does not wait with it.
func (a *API) repairArtistAlbums(c *gin.Context) {
	artist, ok := a.artistAction(c)
	if !ok {
		return
	}
	albums, err := collection.ArtistGhostReleaseGroups(a.DB, artist.MBID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list the albums to repair"})
		return
	}
	if len(albums) == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "nothing to repair — every album of this artist resolves"})
		return
	}
	if off, err := collection.RefreshTurnedOff(a.DB); err == nil && len(off) > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "artist refresh is turned off for " + strings.Join(off, ", ") +
			" — turn it on under Managers, or refresh the artist in Lidarr yourself"})
		return
	}
	if _, err := migration.MarkRepairQueued(a.DB, artist.MBID); err != nil {
		logger.Log.Warnf("failed to mark migrations as repairing for %s: %s", artist.MBID, err.Error())
	}
	a.Scan.RepairArtistAlbums(artist.MBID)
	c.JSON(http.StatusAccepted, gin.H{"status": "manager refresh queued", "artist": artist.Name, "albums": len(albums)})
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

// scanCollection re-derives the owned (present) side from the index — the *Scan*
// verb at collection scope. Network-free (it reads only cached releases) and no
// directory walk, which is why it answers inline instead of queueing; it does stat
// each indexed file, cheap enough to stay inline but still a real disk read.
//
// It is the cheapest of the four verbs and the one that makes the collection view
// agree with what the disk actually holds. What it cannot do is *discover* a file —
// that needs a walk, which is Process, or this verb with walk_disk set: then it is
// queued, walks every enabled library first and records what it finds without
// writing to any audio file (see process.DiscoverAll).
func (a *API) scanCollection(c *gin.Context) {
	res, err := a.runScan(verbTarget{}, walkDiskRequested(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	if res.Queued {
		c.JSON(http.StatusAccepted, gin.H{"status": "scan queued"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"artists":              res.Stats.Artists,
		"owned_release_groups": res.Stats.Owned,
		"credit_changes":       res.Stats.CreditChanges,
		"files_removed":        res.Stats.FilesRemoved,
		// Present only when the pass had nothing to read. The page shows it instead of
		// "Scanned — 0 artists, 0 albums", which is true and tells nobody anything.
		"empty_reason": res.Stats.EmptyReason,
	})
}

// syncLidarr mirrors Lidarr's monitored/missing albums for Lidarr-managed artists
// in the background, recording the result as an Activity event.
func (a *API) syncLidarr(c *gin.Context) {
	if !a.lidarrConfigured(c) {
		return
	}
	a.startLidarrSync(collection.SyncOptions{IgnoreCache: ignoreCacheRequested(c)}, "Sync from Lidarr")
	c.JSON(http.StatusAccepted, gin.H{"status": "lidarr sync started"})
}

// syncLidarrArtist is syncLidarr narrowed to one artist — the granularity a repair
// needs, since one album's counts going stale should not cost a mirror pass over every
// Lidarr artist in the collection.
//
// An artist the mirror does not govern is rejected rather than silently syncing nothing:
// the pass would report "0 artists synced" and read as a failure of Lidarr rather than
// as the artist not being Lidarr's to answer for.
func (a *API) syncLidarrArtist(c *gin.Context) {
	if !a.lidarrConfigured(c) {
		return
	}
	mbid := c.Param("mbid")

	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", mbid).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown artist"})
		return
	}
	if artist.ManagedBy != models.ManagedByLidarr && artist.ManagedBy != models.ManagedByMixed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this artist is not managed by Lidarr"})
		return
	}

	a.startLidarrSync(
		collection.SyncOptions{ArtistMBID: mbid, IgnoreCache: ignoreCacheRequested(c)},
		"Sync "+artist.Name+" from Lidarr",
	)
	c.JSON(http.StatusAccepted, gin.H{"status": "lidarr sync started"})
}

// lidarrConfigured answers the 400 both sync handlers owe when there is no Lidarr to
// sync from, and reports whether the caller may continue.
func (a *API) lidarrConfigured(c *gin.Context) bool {
	var lidarrManagers int64
	a.DB.Model(&models.Manager{}).Where("type = ? AND enabled = ?", models.ManagerTypeLidarr, true).Count(&lidarrManagers)
	if lidarrManagers == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no enabled Lidarr manager configured"})
		return false
	}
	return true
}

// ignoreCacheRequested reads the opt-in flag. It is deliberately tolerant of an absent
// or unparseable body: the flag defaults to off, so the worst a malformed request can
// do is get the ordinary pass it would have got without the field.
func ignoreCacheRequested(c *gin.Context) bool {
	var in struct {
		IgnoreCache bool `json:"ignore_cache"`
	}
	_ = c.ShouldBindJSON(&in)
	return in.IgnoreCache
}

// walkDiskRequested reads the Scan dialog's opt-in disk walk, tolerant of an absent
// body for the same reason as ignoreCacheRequested: off is the ordinary inline Scan.
func walkDiskRequested(c *gin.Context) bool {
	var in struct {
		WalkDisk bool `json:"walk_disk"`
	}
	_ = c.ShouldBindJSON(&in)
	return in.WalkDisk
}

// startLidarrSync runs a mirror pass in the background under its own Activity event.
// Both sync verbs report identically — same event type, same counters — because they
// are the same work at two scopes, and a scoped pass that reported differently would
// read as a different verb in the feed.
func (a *API) startLidarrSync(opts collection.SyncOptions, title string) {
	go func() {
		ev := events.Begin(a.DB, models.EventTypeLidarrSync, title)
		stats, err := collection.SyncLidarrWith(a.DB, opts)
		status := models.EventStatusOK
		if err != nil {
			status = models.EventStatusError
		}
		summary := collection.SyncSummaryLine(stats)
		if opts.IgnoreCache {
			summary += " · cache dropped"
		}
		details := collection.SyncEventDetails(stats)
		details["ignore_cache"] = opts.IgnoreCache
		if opts.ArtistMBID != "" {
			details["artist_mb_id"] = opts.ArtistMBID
		}
		// A pass that returned before its first HTTP call says so. Without it this row
		// is indistinguishable from one where Lidarr was asked and had nothing — the
		// same counters, and no way to tell a missing precondition from an answer.
		// The status stays OK: nothing failed.
		if stats.EmptyReason != "" {
			summary += " — " + stats.EmptyReason
			details["empty_reason"] = stats.EmptyReason
		}
		if err != nil {
			details["error"] = err.Error()
		}
		ev.Stats = collection.SyncEventStats(stats)
		events.Finish(a.DB, ev, status, summary, details)
		events.AddItems(a.DB, ev, collection.SyncEventItems(stats))
	}()
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

// searchArtistReleaseGroups lists any MusicBrainz artist's release-groups, for
// browsing from an artist search hit to an album. Unlike discography it needs no
// collection row: an import is how an artist the collection has never seen arrives.
func (a *API) searchArtistReleaseGroups(c *gin.Context) {
	groups, _, err := a.meta().GetArtistReleaseGroups(c.Param("mbid"))
	if err != nil {
		logger.Log.Errorf("failed to list release-groups for %s: %s", c.Param("mbid"), err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not load the artist's albums from MusicBrainz"})
		return
	}
	if groups == nil {
		groups = []models.MusicBrainzArtistReleaseGroup{}
	}
	c.JSON(http.StatusOK, groups)
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

	desire, err := a.addDesire(collection.DesireInput{
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
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, desire)
}

// detachArtist takes authority over an artist back from its library's manager,
// keeping the manager's selections as the user's own wants. See collection.DetachArtist
// for what changes and why following is switched off with it.
func (a *API) detachArtist(c *gin.Context) {
	result, err := collection.DetachArtist(a.DB, c.Param("mbid"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}
	if errors.Is(err, collection.ErrNotManaged) {
		// 409, not 400: the request is well-formed and was true of some earlier state
		// of the artist — the page is simply out of date.
		c.JSON(http.StatusConflict, gin.H{"error": "no manager governs this artist, so there is nothing to detach"})
		return
	}
	if err != nil {
		logger.Log.Warnf("failed to detach artist %s: %s", c.Param("mbid"), err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to detach the artist"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"artist":         newArtistView(result.Artist),
		"wants_kept":     result.WantsKept,
		"follow_cleared": result.FollowCleared,
	})
}

// reattachArtist hands an artist back to whatever manages its libraries. The wants
// detaching made manual stay manual — see collection.ReattachArtist.
func (a *API) reattachArtist(c *gin.Context) {
	artist, err := collection.ReattachArtist(a.DB, c.Param("mbid"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}
	if err != nil {
		logger.Log.Warnf("failed to reattach artist %s: %s", c.Param("mbid"), err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reattach the artist"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"artist": newArtistView(artist)})
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
	a.writeArtistInfo(c, mbid)
}

// searchArtistInfo is artistInfo for any MusicBrainz artist, collection row or not:
// what the Add artist dialog shows to tell twenty artists of one name apart before
// one of them is added.
func (a *API) searchArtistInfo(c *gin.Context) {
	a.writeArtistInfo(c, c.Param("mbid"))
}

func (a *API) writeArtistInfo(c *gin.Context, mbid string) {
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

	groups, _, err := modules.GetArtistDiscography(mbid)
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
	copies, err := collection.ReleaseGroupCopies(a.DB, storedMBIDs)
	if err != nil {
		logger.Log.Warnf("failed to load which libraries hold %s's albums: %s", mbid, err.Error())
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
		view.Libraries = librariesOf(copies, g.ID)
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
		view.Libraries = librariesOf(copies, rg.MBID)
		out = append(out, view)
	}

	c.JSON(http.StatusOK, out)
}

// The range a follow cutoff may name. The lower bound is roughly the start of
// commercial recording, the upper one leaves room for an announced release; both exist
// only to catch a typo before it silently empties or fills the missing list.
const (
	minFollowFromYear = 1900
	maxFollowFromYear = 2200
)

// updateFollow changes what following this artist auto-wants, then re-syncs so the
// missing list matches the new settings immediately.
func (a *API) updateFollow(c *gin.Context) {
	var body struct {
		Monitored       *bool   `json:"monitored"`
		FollowTypes     *string `json:"follow_types"`
		FollowSecondary *bool   `json:"follow_secondary"`
		FollowFromYear  *int    `json:"follow_from_year"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	artist, wanted, err := a.updateFollowSettings(c.Param("mbid"), followUpdate{
		Monitored: body.Monitored, FollowTypes: body.FollowTypes,
		FollowSecondary: body.FollowSecondary, FollowFromYear: body.FollowFromYear,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"artist": artist, "wanted": wanted})
}

// validFollowYear reports whether a follow cutoff is one MusicBrainz dates can occupy,
// or zero for none.
func validFollowYear(year int) bool {
	return year == 0 || (year >= minFollowFromYear && year <= maxFollowFromYear)
}

// followArtists follows several artists with one set of follow settings — the bulk
// form of the artist page's Follow. The settings are written here; the discography
// syncs that list what they want are a queued job, since each is a rate-limited read.
// An artist a manager governs is skipped and named: following would be stored, not
// obeyed.
func (a *API) followArtists(c *gin.Context) {
	var body struct {
		ArtistMBIDs     []string `json:"artist_mb_ids"`
		FollowTypes     string   `json:"follow_types"`
		FollowSecondary bool     `json:"follow_secondary"`
		FollowFromYear  int      `json:"follow_from_year"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.ArtistMBIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "choose at least one artist"})
		return
	}
	if !validFollowYear(body.FollowFromYear) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("follow_from_year must be 0 (no cutoff) or between %d and %d", minFollowFromYear, maxFollowFromYear)})
		return
	}
	var artists []models.CollectionArtist
	if err := a.DB.Where("mb_id IN ?", body.ArtistMBIDs).Find(&artists).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load the artists"})
		return
	}
	followed, skipped := []string{}, []string{}
	for _, artist := range artists {
		if !collection.FollowGoverns(artist) {
			skipped = append(skipped, artist.Name)
			continue
		}
		if err := a.DB.Model(&artist).Updates(map[string]any{
			"monitored": true, "follow_types": strings.TrimSpace(body.FollowTypes),
			"follow_secondary": body.FollowSecondary, "follow_from_year": body.FollowFromYear,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update " + artist.Name})
			return
		}
		followed = append(followed, artist.MBID)
	}
	a.Scan.FollowArtists(followed)
	c.JSON(http.StatusAccepted, gin.H{"followed": len(followed), "skipped": skipped})
}

// getCollectionPolicy and updateCollectionPolicy read and write what the collection
// does by itself: today, whether an artist found in your files is followed.
func (a *API) getCollectionPolicy(c *gin.Context) {
	policy, err := collection.LoadPolicy(a.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load the collection policy"})
		return
	}
	c.JSON(http.StatusOK, policy)
}

func (a *API) updateCollectionPolicy(c *gin.Context) {
	var in models.CollectionPolicy
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	policy, err := collection.SavePolicy(a.DB, in)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, policy)
}

// releaseGroupDetail returns everything the release-group page needs in one call:
// the group with its owned/wanted state, every edition MusicBrainz lists, and the
// desires that exist for it. Tracklists stay a separate, lazy call — fetching one
// per edition up front would cost a rate-limited second each.
func (a *API) releaseGroupDetail(c *gin.Context) {
	detail, err := a.releaseGroupDetailData(c.Param("mbid"), c.Param("rgid"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"artist":        detail.Artist,
		"release_group": detail.ReleaseGroup,
		"editions":      detail.Editions,
		"desires":       detail.Desires,
	})
}

// releaseGroupDetailData is what GET /artists/:mbid/release-groups/:rgid and the MCP
// get_album tool share.
type releaseGroupDetailData struct {
	Artist       artistView
	ReleaseGroup releaseGroupView
	Editions     []editionView
	Desires      []models.CollectionDesire
}

// releaseGroupDetailData loads one release-group under an artist. It may reach
// MusicBrainz (cache-first) for the edition list and, for a group nobody owns, the
// discography. The only error is errArtistNotFound; an unreachable MusicBrainz leaves
// the edition list to what is owned.
func (a *API) releaseGroupDetailData(artistMBID, rgMBID string) (releaseGroupDetailData, error) {
	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		return releaseGroupDetailData{}, errArtistNotFound
	}
	// Prefer the stored row (it carries ownership); fall back to the cached
	// discography so a release-group nobody owns still renders.
	var rg models.CollectionReleaseGroup
	if err := a.DB.Where("mb_id = ?", rgMBID).First(&rg).Error; err != nil {
		rg = models.CollectionReleaseGroup{MBID: rgMBID, ArtistMBID: artistMBID}
		if groups, _, dErr := modules.GetArtistDiscography(artistMBID); dErr == nil {
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
	if groupCopies, err := collection.ReleaseGroupCopies(a.DB, []string{rgMBID}); err != nil {
		logger.Log.Warnf("failed to load which libraries hold %s: %s", rgMBID, err.Error())
	} else {
		view.Libraries = librariesOf(groupCopies, rgMBID)
	}
	ownedIDs := make([]string, 0, len(owned))
	for _, rel := range owned {
		ownedIDs = append(ownedIDs, rel.MBID)
	}
	copies, err := collection.ReleaseCopies(a.DB, ownedIDs)
	if err != nil {
		logger.Log.Warnf("failed to load which libraries hold the editions of %s: %s", rgMBID, err.Error())
	}

	return releaseGroupDetailData{
		Artist:       newArtistView(artist),
		ReleaseGroup: view,
		Editions:     annotateEditions(editions, owned, copies),
		Desires:      mine,
	}, nil
}
