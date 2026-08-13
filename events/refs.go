package events

import (
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// ResolveRefs fills in models.EventItem.Related for every entity row in items: what
// the collection calls the MBID, whose it is, and how many indexed files point at it.
//
// # Why an event needs this at all
//
// A metadata pass records outcomes against MusicBrainz identifiers, because that is
// what it read. But an identifier is not a subject: a list of forty UUIDs that
// returned 404 tells you something is wrong and nothing about what. The answer is
// already in the database — `library_items.mb_release_id` names the files, and the
// three collection tables name the entities — it had simply never been asked for from
// an event.
//
// # Resolved, never stored
//
// The rows say what happened at MusicBrainz at the time; this says what the collection
// holds *now*. Writing it onto the row would freeze one against the other, so a file
// moved or an artist detached a week later would be reported as the state during a run
// that predates it. It is four queries for a whole event, which is cheaper than the
// staleness would be.
//
// The queries are batched over the whole item set for the same reason the feed
// annotates in two queries rather than fifty: a 500-row detail list must not become
// 500 round trips to name itself.
func ResolveRefs(db *gorm.DB, items []models.EventItem) {
	if db == nil || len(items) == 0 {
		return
	}

	// Only entity rows have an MBID in Path. A file row's identifier is already the
	// most specific thing there is to say about it.
	ids := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		if it.Kind != models.EventItemKindEntity || it.Path == "" || seen[it.Path] {
			continue
		}
		seen[it.Path] = true
		ids = append(ids, it.Path)
	}
	if len(ids) == 0 {
		return
	}

	type groupRow struct {
		MBID       string
		ArtistMBID string
		Title      string
	}
	var groups []groupRow
	if err := db.Model(&models.CollectionReleaseGroup{}).
		Select("mb_id, artist_mb_id, title").Where("mb_id IN ?", ids).Scan(&groups).Error; err != nil {
		logger.Log.Warnf("failed to resolve release-groups for event detail: %s", err.Error())
	}

	type releaseRow struct {
		MBID             string
		ReleaseGroupMBID string
		ArtistMBID       string
		Title            string
	}
	var releases []releaseRow
	if err := db.Model(&models.CollectionRelease{}).
		Select("mb_id, release_group_mb_id, artist_mb_id, title").Where("mb_id IN ?", ids).Scan(&releases).Error; err != nil {
		logger.Log.Warnf("failed to resolve releases for event detail: %s", err.Error())
	}

	// Artists are looked up for the identifiers themselves *and* for the artists the
	// two lookups above named, in one query — a release row wants to say who it is by,
	// and that is the same table.
	artistIDs := append([]string{}, ids...)
	for _, g := range groups {
		artistIDs = append(artistIDs, g.ArtistMBID)
	}
	for _, r := range releases {
		artistIDs = append(artistIDs, r.ArtistMBID)
	}
	type artistRow struct {
		MBID string
		Name string
	}
	var artists []artistRow
	if err := db.Model(&models.CollectionArtist{}).
		Select("mb_id, name").Where("mb_id IN ?", artistIDs).Scan(&artists).Error; err != nil {
		logger.Log.Warnf("failed to resolve artists for event detail: %s", err.Error())
	}

	// How much of the library depends on each identifier. Only releases can carry a
	// count: a file is correlated to a release, and every entity above one is reached
	// through it.
	type fileRow struct {
		MBReleaseID string
		N           int
	}
	var files []fileRow
	if err := db.Model(&models.LibraryItem{}).
		Select("mb_release_id, count(*) as n").Where("mb_release_id IN ?", ids).
		Group("mb_release_id").Scan(&files).Error; err != nil {
		logger.Log.Warnf("failed to count files for event detail: %s", err.Error())
	}

	artistNames := make(map[string]string, len(artists))
	for _, a := range artists {
		artistNames[a.MBID] = a.Name
	}
	groupByID := make(map[string]groupRow, len(groups))
	for _, g := range groups {
		groupByID[g.MBID] = g
	}
	releaseByID := make(map[string]releaseRow, len(releases))
	for _, r := range releases {
		releaseByID[r.MBID] = r
	}
	fileCounts := make(map[string]int, len(files))
	for _, f := range files {
		fileCounts[f.MBReleaseID] = f.N
	}

	// The last resort: what a migration row remembers being called.
	//
	// Retiring an album deletes the only row that knows its title, and un-matching a
	// deleted release does the same for the edition — so the rows most in need of a name
	// are exactly the ones the three lookups above cannot answer. The migration captured
	// the name at detection for this reason, and reading it here means every event that
	// reports on a dead identifier gets the benefit, not only the migration ones.
	type migrationRow struct {
		OldMBID    string
		EntityType string
		Name       string
	}
	var remembered []migrationRow
	if err := db.Model(&models.MusicbrainzMigration{}).
		Select("old_mb_id, entity_type, name").
		Where("old_mb_id IN ? AND name <> ''", ids).Scan(&remembered).Error; err != nil {
		logger.Log.Warnf("failed to resolve migrated identifiers for event detail: %s", err.Error())
	}
	migrationNames := make(map[string]migrationRow, len(remembered))
	for _, row := range remembered {
		migrationNames[row.OldMBID] = row
	}

	refs := make(map[string]*models.EntityRef, len(ids))
	for _, id := range ids {
		ref := &models.EntityRef{Files: fileCounts[id]}
		switch {
		case releaseByID[id].MBID != "":
			row := releaseByID[id]
			ref.Kind = models.EntityKindRelease
			ref.Name = row.Title
			ref.GroupMBID = row.ReleaseGroupMBID
			ref.ArtistMBID = row.ArtistMBID
			ref.Artist = artistNames[row.ArtistMBID]
		case groupByID[id].MBID != "":
			row := groupByID[id]
			ref.Kind = models.EntityKindReleaseGroup
			ref.Name = row.Title
			ref.GroupMBID = row.MBID
			ref.ArtistMBID = row.ArtistMBID
			ref.Artist = artistNames[row.ArtistMBID]
		case artistNames[id] != "":
			ref.Kind = models.EntityKindArtist
			ref.Name = artistNames[id]
			ref.ArtistMBID = id
		case migrationNames[id].Name != "":
			// The collection cannot name it because the collection no longer holds it.
			// Checked after the live rows and before the file-count case, so a name is
			// only ever taken from memory when there is nothing current to take it from.
			row := migrationNames[id]
			ref.Kind = migrationEntityKind(row.EntityType)
			ref.Name = row.Name
			if ref.Kind == models.EntityKindArtist {
				ref.ArtistMBID = id
			}
		case ref.Files > 0:
			// Files point at it and the collection has no row for it. A file is only
			// ever correlated to a release, so the kind is not in doubt — what is
			// missing is the collection, and that is the finding.
			ref.Kind = models.EntityKindRelease
		}
		// Nothing found and nothing depending on it: the row keeps its bare MBID
		// rather than gaining an empty panel that says the collection has no opinion.
		// Files without a collection row is *not* that case — it is the interesting
		// one, an identifier the library depends on that the collection cannot name.
		if ref.Name == "" && ref.Files == 0 {
			continue
		}
		refs[id] = ref
	}

	for i := range items {
		if ref, ok := refs[items[i].Path]; ok {
			items[i].Related = ref
		}
	}
}

// migrationEntityKind translates a migration row's entity type into the vocabulary the
// detail rows use — which is MusicBrainz's own, because the link beside the name opens
// musicbrainz.org and a translated label would disagree with the page it opens.
func migrationEntityKind(entityType string) string {
	switch entityType {
	case models.MigrationEntityArtist:
		return models.EntityKindArtist
	case models.MigrationEntityReleaseGroup:
		return models.EntityKindReleaseGroup
	default:
		return models.EntityKindRelease
	}
}
