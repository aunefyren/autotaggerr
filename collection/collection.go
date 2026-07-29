// Package collection materializes the "present vs wanted" view.
//
// Two authorities write to CollectionReleaseGroup, and they are kept in separate
// column blocks so they never overwrite each other:
//
//   - Rebuild owns the *disk* view — the release-groups a library actually holds,
//     aggregated from library_items + the cached MusicBrainz releases.
//   - SyncArtist (native MusicBrainz discography) and SyncLidarr (the Lidarr mirror)
//     own the *catalog* view — what the manager says should exist, and what it
//     believes is monitored.
//
// Present is universal; wanted is manager-owned. Where the two views disagree — a
// manager reporting fewer files than are on disk because it needs a rescan, or files
// no manager has an album for — the row exposes a Discrepancy rather than one side
// silently winning.
package collection

import (
	"fmt"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Rebuild recomputes the disk ("present") side of the collection from the index. It
// reads only cached releases, so it never triggers MusicBrainz fetches. Catalog state
// — including wanted release-groups with no files — is left untouched.
func Rebuild(db *gorm.DB) (artistCount int, ownedCount int, err error) {
	// library -> manager type (for the per-artist "managed by" provenance)
	managerType := map[uuid.UUID]string{}
	var managers []models.Manager
	if err := db.Find(&managers).Error; err != nil {
		return 0, 0, err
	}
	for _, m := range managers {
		managerType[m.ID] = m.Type
	}
	libraryManager := map[uuid.UUID]string{}
	var libraries []models.Library
	if err := db.Find(&libraries).Error; err != nil {
		return 0, 0, err
	}
	for _, l := range libraries {
		switch {
		case l.ManagerID == nil:
			// No manager assigned is the documented native default, not unknown.
			libraryManager[l.ID] = models.ManagerTypeAutotaggerr
		case managerType[*l.ManagerID] != "":
			libraryManager[l.ID] = managerType[*l.ManagerID]
		default:
			// Dangling reference: the manager row is gone. Say so rather than
			// letting it fall through to "native".
			libraryManager[l.ID] = models.ManagedByUnknown
		}
	}

	// Clear the disk view wholesale; it is re-established below. Track counts must
	// be cleared too, or a row that stops being owned keeps stale counts and renders
	// as "missing, 10/12". The catalog columns are untouched — they belong to the
	// manager mirror, which is the whole point of keeping them separate.
	if err := db.Model(&models.CollectionReleaseGroup{}).Where("owned = ?", true).
		Updates(map[string]any{"owned": false, "owned_tracks": 0, "total_tracks": 0}).Error; err != nil {
		return 0, 0, err
	}

	type itemRow struct {
		MBReleaseID string
		LibraryID   uuid.UUID
	}
	var rows []itemRow
	if err := db.Model(&models.LibraryItem{}).
		Where("status = ? AND mb_release_id <> ''", models.LibraryItemStatusOK).
		Find(&rows).Error; err != nil {
		return 0, 0, err
	}

	// Pass 1: count owned files per release, and gather artist + manager provenance.
	releaseOwned := map[string]int{}
	artistName := map[string]string{}
	artistManagers := map[string]map[string]bool{}
	for _, r := range rows {
		releaseOwned[r.MBReleaseID]++

		release, ok := modules.CachedRelease(r.MBReleaseID)
		if !ok || len(release.ArtistCredit) == 0 {
			continue
		}
		artistID := release.ArtistCredit[0].Artist.ID
		if artistID == "" {
			continue
		}
		artistName[artistID] = release.ArtistCredit[0].Artist.Name
		if artistManagers[artistID] == nil {
			artistManagers[artistID] = map[string]bool{}
		}
		mt := libraryManager[r.LibraryID]
		if mt == "" {
			// The item points at a library that no longer exists.
			mt = models.ManagedByUnknown
		}
		artistManagers[artistID][mt] = true
	}

	// Pass 2: per release-group, keep the best-owned edition and its track counts —
	// and, separately, every owned edition. The release-group summary stays
	// best-edition (that is the useful headline: "how close am I to having this
	// album"), while the per-edition rows keep the detail that summary hides.
	type rgInfo struct {
		artistID, title, primary, secondary, date string
		owned, total                              int
	}
	rgBest := map[string]rgInfo{}
	ownedEditions := make([]models.CollectionRelease, 0, len(releaseOwned))
	for relID, ownedTracks := range releaseOwned {
		release, ok := modules.CachedRelease(relID)
		if !ok {
			continue
		}
		rgID := release.ReleaseGroup.ID
		if rgID == "" || len(release.ArtistCredit) == 0 {
			continue
		}
		total := 0
		for _, m := range release.Media {
			total += len(m.Tracks)
		}
		artistID := release.ArtistCredit[0].Artist.ID

		ownedEditions = append(ownedEditions, models.CollectionRelease{
			MBID: relID, ReleaseGroupMBID: rgID, ArtistMBID: artistID,
			Title: release.Title, Date: release.Date, Country: release.Country,
			Disambiguation: release.Disambiguation, Format: mediaSummary(release),
			OwnedTracks: ownedTracks, TotalTracks: total,
		})

		if cur, exists := rgBest[rgID]; !exists || ownedTracks > cur.owned {
			rgBest[rgID] = rgInfo{
				artistID:  artistID,
				title:     release.ReleaseGroup.Title,
				primary:   release.ReleaseGroup.PrimaryType,
				secondary: strings.Join(release.ReleaseGroup.SecondaryTypes, ", "),
				date:      release.ReleaseGroup.FirstReleaseDate,
				owned:     ownedTracks,
				total:     total,
			}
		}
	}
	syncOwnedReleases(db, ownedEditions)

	for artistID, mgrs := range artistManagers {
		upsertArtist(db, artistID, artistName[artistID], managedByLabel(mgrs))
	}
	for rgID, info := range rgBest {
		upsertReleaseGroup(db, rgWrite{
			mbID: rgID, artistMBID: info.artistID, title: info.title,
			primary: info.primary, secondary: info.secondary, date: info.date,
			disk: &diskState{owned: true, ownedTracks: info.owned, totalTracks: info.total},
		})
	}

	return len(artistManagers), len(rgBest), nil
}

// syncOwnedReleases makes the per-edition disk table match exactly the editions
// currently owned: upsert what is owned, delete what is not.
//
// Pruning matters more here than upserting. A file re-attached from the original to
// the remaster (which manual attach now makes easy) leaves the original owning
// nothing, and a stale row would keep claiming files that moved — the same class of
// bug as the release-group counts that used to survive losing their files.
func syncOwnedReleases(db *gorm.DB, owned []models.CollectionRelease) {
	keep := make([]string, 0, len(owned))
	for _, rel := range owned {
		keep = append(keep, rel.MBID)

		var existing models.CollectionRelease
		if err := db.Where("mb_id = ?", rel.MBID).First(&existing).Error; err == nil {
			if err := db.Model(&existing).Updates(map[string]any{
				"release_group_mb_id": rel.ReleaseGroupMBID,
				"artist_mb_id":        rel.ArtistMBID,
				"title":               rel.Title,
				"date":                rel.Date,
				"country":             rel.Country,
				"disambiguation":      rel.Disambiguation,
				"format":              rel.Format,
				"owned_tracks":        rel.OwnedTracks,
				"total_tracks":        rel.TotalTracks,
			}).Error; err != nil {
				logger.Log.Warnf("failed to update owned edition %s: %s", rel.MBID, err.Error())
			}
			continue
		}
		if err := db.Create(&rel).Error; err != nil {
			logger.Log.Warnf("failed to record owned edition %s: %s", rel.MBID, err.Error())
		}
	}

	// Owning nothing is a real state, and it must still clear the table — a
	// "NOT IN ()" with no values would delete nothing at all.
	prune := db.Session(&gorm.Session{AllowGlobalUpdate: true})
	if len(keep) > 0 {
		prune = prune.Where("mb_id NOT IN ?", keep)
	}
	if err := prune.Delete(&models.CollectionRelease{}).Error; err != nil {
		logger.Log.Warnf("failed to prune owned editions: %s", err.Error())
	}
}

// mediaSummary describes a release's media the way an edition list needs it:
// "2×CD", "CD + DVD", "Digital Media". It is what distinguishes one edition from
// another at a glance, alongside the year.
func mediaSummary(release models.MusicBrainzReleaseResponse) string {
	if len(release.Media) == 0 {
		return ""
	}
	// Consecutive identical formats collapse to a count; a mixed set is listed.
	var parts []string
	count := 0
	current := ""
	flush := func() {
		if current == "" && count == 0 {
			return
		}
		name := current
		if name == "" {
			name = "Unknown"
		}
		if count > 1 {
			name = fmt.Sprintf("%d×%s", count, name)
		}
		parts = append(parts, name)
	}
	for _, m := range release.Media {
		if m.Format != current {
			flush()
			current, count = m.Format, 0
		}
		count++
	}
	flush()
	return strings.Join(parts, " + ")
}

// SyncArtist fetches an artist's discography and records the release-groups that
// following the artist wants but does not own. Owned rows keep their owned flag.
// The artist's own follow settings decide which types count.
func SyncArtist(db *gorm.DB, artistMBID string) (wanted int, err error) {
	var artist models.CollectionArtist
	if err := db.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		return 0, err
	}

	groups, err := modules.GetMusicBrainzArtistReleaseGroups(artistMBID)
	if err != nil {
		return 0, err
	}
	for _, rg := range groups {
		if !FollowWants(artist, rg.PrimaryType, rg.SecondaryTypes) {
			continue
		}
		// The MusicBrainz discography is the native manager's catalog. Track counts
		// stay unknown (0) — counting them would mean fetching every release.
		upsertReleaseGroup(db, rgWrite{
			mbID: rg.ID, artistMBID: artistMBID, title: rg.Title,
			primary: rg.PrimaryType, secondary: strings.Join(rg.SecondaryTypes, ", "),
			date:    rg.FirstReleaseDate,
			catalog: &catalogState{},
		})
		wanted++
	}
	now := time.Now()
	if err := db.Model(&models.CollectionArtist{}).Where("mb_id = ?", artistMBID).Update("last_synced_at", now).Error; err != nil {
		logger.Log.Warnf("failed to set last_synced_at for artist %s: %s", artistMBID, err.Error())
	}
	return wanted, nil
}

// FollowWants reports whether following this artist auto-wants a release-group of
// the given types. It is the single definition of that rule — the discography
// sync, the API's "why is this wanted" label, and the UI all go through it, so
// changing an artist's follow settings changes every view at once.
//
// Unset FollowTypes means the default (studio albums + EPs), which is what keeps a
// missing list readable; a discography is mostly singles and reissues.
func FollowWants(artist models.CollectionArtist, primaryType string, secondaryTypes []string) bool {
	if !artist.FollowSecondary && len(secondaryTypes) > 0 {
		return false
	}

	wanted := strings.TrimSpace(artist.FollowTypes)
	if wanted == "" {
		wanted = models.DefaultFollowTypes
	}
	primary := strings.ToLower(strings.TrimSpace(primaryType))
	for _, t := range strings.Split(wanted, ",") {
		if strings.ToLower(strings.TrimSpace(t)) == primary {
			return true
		}
	}
	return false
}

// FollowGoverns reports whether the *native* follow settings decide what is wanted
// for this artist. They do not when a manager owns the artist: Lidarr is the
// authority on wanted there, and the artist page says so.
//
// Without this, a stale Monitored flag — set before the artist became
// Lidarr-managed, or by the old artist-level "Monitored" toggle — kept producing
// automatic wants on a page that offers no follow control to turn them off. The
// state was real, but nothing on screen could explain or change it. Following is
// still recorded for such an artist; it just does not govern until the artist is
// natively managed again.
func FollowGoverns(artist models.CollectionArtist) bool {
	return artist.ManagedBy != models.ManagedByLidarr && artist.ManagedBy != models.ManagedByMixed
}

// FollowWantsStored is FollowWants for a stored CollectionReleaseGroup row, whose
// secondary types are comma-joined rather than a slice.
func FollowWantsStored(artist models.CollectionArtist, primary, secondaryCSV string) bool {
	return FollowWants(artist, primary, splitTypes(secondaryCSV))
}

func splitTypes(csv string) []string {
	var out []string
	for _, part := range strings.Split(csv, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// managedByLabel summarises which managers own an artist's files. Unknown is a
// real answer, not a fallback: an artist whose provenance cannot be determined must
// not be reported as natively managed, because that is a claim rather than an
// absence of one.
func managedByLabel(mgrs map[string]bool) string {
	lidarr := mgrs[models.ManagerTypeLidarr]
	native := mgrs[models.ManagerTypeAutotaggerr]
	switch {
	case lidarr && native:
		return models.ManagedByMixed
	case lidarr:
		return models.ManagedByLidarr
	case native:
		return models.ManagedByAutotaggerr
	default:
		return models.ManagedByUnknown
	}
}

func upsertArtist(db *gorm.DB, mbID, name, managedBy string) {
	var a models.CollectionArtist
	if err := db.Where("mb_id = ?", mbID).First(&a).Error; err == nil {
		// Preserve Monitored / LastSyncedAt / Origin; refresh name + provenance.
		// Origin records how the artist *entered* the collection, so an artist added
		// by hand keeps that origin once files for it show up.
		db.Model(&a).Updates(map[string]any{"name": name, "managed_by": managedBy})
		return
	}
	if err := db.Create(&models.CollectionArtist{
		MBID: mbID, Name: name, ManagedBy: managedBy,
		Origin: models.CollectionOriginLibrary,
	}).Error; err != nil {
		logger.Log.Warnf("failed to upsert artist %s: %s", mbID, err.Error())
	}
}

// diskState is what Rebuild observed on disk.
type diskState struct {
	owned                    bool
	ownedTracks, totalTracks int
}

// catalogState is what a manager reports should exist. Zero totalTracks means the
// manager did not say (native MB discovery).
type catalogState struct {
	ownedTracks, totalTracks int
	monitored                bool
}

// rgWrite is one caller's knowledge of a release-group. Metadata is always written;
// the disk and catalog blocks are written only when the caller owns that view, so
// Rebuild and the manager mirror can run in any order without clobbering each other.
type rgWrite struct {
	mbID, artistMBID, title, primary, secondary, date string
	disk                                              *diskState
	catalog                                           *catalogState
}

func upsertReleaseGroup(db *gorm.DB, w rgWrite) {
	updates := map[string]any{
		"artist_mb_id": w.artistMBID, "title": w.title, "primary_type": w.primary,
		"secondary_types": w.secondary, "first_release_date": w.date,
	}
	if w.disk != nil {
		updates["owned"] = w.disk.owned
		updates["owned_tracks"] = w.disk.ownedTracks
		updates["total_tracks"] = w.disk.totalTracks
	}
	if w.catalog != nil {
		updates["in_catalog"] = true
		updates["catalog_owned_tracks"] = w.catalog.ownedTracks
		updates["catalog_total_tracks"] = w.catalog.totalTracks
		updates["catalog_monitored"] = w.catalog.monitored
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", w.mbID).First(&rg).Error; err == nil {
		db.Model(&rg).Updates(updates)
		return
	}

	row := models.CollectionReleaseGroup{
		MBID: w.mbID, ArtistMBID: w.artistMBID, Title: w.title, PrimaryType: w.primary,
		SecondaryTypes: w.secondary, FirstReleaseDate: w.date,
	}
	if w.disk != nil {
		row.Owned, row.OwnedTracks, row.TotalTracks = w.disk.owned, w.disk.ownedTracks, w.disk.totalTracks
	}
	if w.catalog != nil {
		row.InCatalog = true
		row.CatalogOwnedTracks, row.CatalogTotalTracks = w.catalog.ownedTracks, w.catalog.totalTracks
		row.CatalogMonitored = w.catalog.monitored
	}
	if err := db.Create(&row).Error; err != nil {
		logger.Log.Warnf("failed to upsert release-group %s: %s", w.mbID, err.Error())
	}
}

// SyncLidarr mirrors Lidarr's albums for the collection's Lidarr-managed artists:
// it reads each artist's albums (with have/total track counts + monitoring) and
// records them as *catalog* state. Lidarr is authoritative about which albums exist
// and what is monitored; it is not authoritative about what is on disk, so its
// counts never touch the disk columns Rebuild owns. Where the two disagree the row
// reports a Discrepancy (see models.CollectionReleaseGroup).
func SyncLidarr(db *gorm.DB) (artistsSynced, groups int, err error) {
	var managers []models.Manager
	if err := db.Where("type = ? AND enabled = ?", models.ManagerTypeLidarr, true).Find(&managers).Error; err != nil {
		return 0, 0, err
	}
	if len(managers) == 0 {
		return 0, 0, nil
	}

	var artists []models.CollectionArtist
	if err := db.Where("managed_by IN ?", []string{models.ManagedByLidarr, models.ManagedByMixed}).Find(&artists).Error; err != nil {
		return 0, 0, err
	}
	want := map[string]bool{}
	for _, a := range artists {
		want[a.MBID] = true
	}
	if len(want) == 0 {
		return 0, 0, nil
	}

	for _, m := range managers {
		if strings.TrimSpace(m.LidarrBaseURL) == "" || strings.TrimSpace(m.LidarrAPIKey) == "" {
			continue
		}
		cookie := m.LidarrHeaderCookie
		client := modules.NewLidarrClient(m.LidarrBaseURL, m.LidarrAPIKey, &cookie)

		lidarrArtists, err := client.GetArtists()
		if err != nil {
			logger.Log.Warnf("failed to list Lidarr artists: %s", err.Error())
			continue
		}
		for _, la := range lidarrArtists {
			if la.ForeignArtistID == "" || !want[la.ForeignArtistID] {
				continue
			}
			albums, err := client.GetArtistAlbums(la.ID)
			if err != nil {
				logger.Log.Warnf("failed to list Lidarr albums for %s: %s", la.Name, err.Error())
				continue
			}
			// Drop the previous catalog view for this artist so albums removed from
			// Lidarr stop being listed. Done only after the fetch succeeded, and it
			// leaves the disk columns intact.
			if err := db.Model(&models.CollectionReleaseGroup{}).
				Where("artist_mb_id = ? AND in_catalog = ?", la.ForeignArtistID, true).
				Updates(map[string]any{
					"in_catalog": false, "catalog_owned_tracks": 0,
					"catalog_total_tracks": 0, "catalog_monitored": false,
				}).Error; err != nil {
				logger.Log.Warnf("failed to reset catalog state for %s: %s", la.Name, err.Error())
			}

			for _, al := range albums {
				if al.ForeignAlbumID == "" {
					continue
				}
				// Lidarr's album type / release date; no MB secondary types here.
				upsertReleaseGroup(db, rgWrite{
					mbID: al.ForeignAlbumID, artistMBID: la.ForeignArtistID, title: al.Title,
					primary: al.AlbumType, date: al.ReleaseDate,
					catalog: &catalogState{
						ownedTracks: al.Statistics.TrackFileCount,
						totalTracks: al.Statistics.TrackCount,
						monitored:   al.Monitored,
					},
				})
				groups++
			}
			artistsSynced++
		}
	}
	return artistsSynced, groups, nil
}
