package events

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// seedCollection lays down one artist, one album and one edition of it, plus two files
// pointing at that edition — the smallest shape that exercises all three lookups and
// the file count together.
func seedCollection(t *testing.T, db *gorm.DB) {
	t.Helper()
	rows := []any{
		&models.CollectionArtist{MBID: "artist-1", Name: "Radiohead"},
		&models.CollectionReleaseGroup{MBID: "group-1", ArtistMBID: "artist-1", Title: "OK Computer"},
		&models.CollectionRelease{MBID: "release-1", ReleaseGroupMBID: "group-1", ArtistMBID: "artist-1", Title: "OK Computer (UK)"},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	libraryID := uuid.New()
	for _, path := range []string{"/music/a.flac", "/music/b.flac"} {
		if err := db.Create(&models.LibraryItem{LibraryID: libraryID, Path: path, MBReleaseID: "release-1"}).Error; err != nil {
			t.Fatalf("seed item: %v", err)
		}
	}
}

// TestResolveRefsNamesEachKind is the point of the whole feature: a detail row records
// a UUID because that is what the pass read, and a page of UUIDs cannot be acted on.
// Each of the three kinds has to come back named, and a release has to carry the count
// of files that depend on it — that count is what turns "404" into "these two files".
func TestResolveRefsNamesEachKind(t *testing.T) {
	db := testDB(t)
	seedCollection(t, db)

	items := []models.EventItem{
		{Path: "release-1", Kind: models.EventItemKindEntity, Status: models.EventItemStatusGone},
		{Path: "group-1", Kind: models.EventItemKindEntity, Status: models.EventItemStatusRefreshed},
		{Path: "artist-1", Kind: models.EventItemKindEntity, Status: models.EventItemStatusError},
	}
	ResolveRefs(db, items)

	release := items[0].Related
	if release == nil {
		t.Fatal("release row resolved to nothing")
	}
	if release.Kind != models.EntityKindRelease || release.Name != "OK Computer (UK)" {
		t.Errorf("release ref = %+v", release)
	}
	if release.Artist != "Radiohead" || release.ArtistMBID != "artist-1" || release.GroupMBID != "group-1" {
		t.Errorf("release ref lost its chain: %+v", release)
	}
	if release.Files != 2 {
		t.Errorf("release files = %d, want 2", release.Files)
	}

	group := items[1].Related
	if group == nil || group.Kind != models.EntityKindReleaseGroup || group.Name != "OK Computer" {
		t.Errorf("group ref = %+v", group)
	}
	if group != nil && group.Artist != "Radiohead" {
		t.Errorf("group ref did not name its artist: %+v", group)
	}

	artist := items[2].Related
	if artist == nil || artist.Kind != models.EntityKindArtist || artist.Name != "Radiohead" {
		t.Errorf("artist ref = %+v", artist)
	}
}

// TestResolveRefsFilesWithoutACollectionRow pins the case the feature exists for: files
// point at a release the collection cannot name. That is a finding, not a gap — the
// row must still report the count, and must still say it is a release, because a file
// is only ever correlated to one.
func TestResolveRefsFilesWithoutACollectionRow(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.LibraryItem{LibraryID: uuid.New(), Path: "/music/orphan.flac", MBReleaseID: "orphan-release"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	items := []models.EventItem{{Path: "orphan-release", Kind: models.EventItemKindEntity, Status: models.EventItemStatusGone}}
	ResolveRefs(db, items)

	ref := items[0].Related
	if ref == nil {
		t.Fatal("a release with files and no collection row resolved to nothing; that is the interesting case")
	}
	if ref.Name != "" {
		t.Errorf("name = %q, want empty — nothing local knows it", ref.Name)
	}
	if ref.Kind != models.EntityKindRelease || ref.Files != 1 {
		t.Errorf("ref = %+v, want a release with 1 file", ref)
	}
}

// TestResolveRefsLeavesOtherRowsAlone: a file row's path is already the most specific
// thing there is to say about it, and an identifier nothing points at gains nothing
// from an empty panel.
func TestResolveRefsLeavesOtherRowsAlone(t *testing.T) {
	db := testDB(t)

	items := []models.EventItem{
		{Path: "/music/a.flac", Status: models.EventItemStatusChanged},
		{Path: "nobody-knows-this", Kind: models.EventItemKindEntity, Status: models.EventItemStatusError},
	}
	ResolveRefs(db, items)

	for i, it := range items {
		if it.Related != nil {
			t.Errorf("item %d gained a ref it should not have: %+v", i, it.Related)
		}
	}
}
