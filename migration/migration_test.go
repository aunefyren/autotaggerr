package migration

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func init() {
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

func pendingRedirect(t *testing.T, db *gorm.DB, entity, old, new string) models.MusicbrainzMigration {
	t.Helper()
	m := models.MusicbrainzMigration{
		EntityType: entity,
		OldMBID:    old,
		NewMBID:    new,
		Kind:       models.MigrationKindRedirect,
		Status:     models.MigrationStatusPending,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}
	return m
}

func pendingDeletion(t *testing.T, db *gorm.DB, entity, old string) models.MusicbrainzMigration {
	t.Helper()
	m := models.MusicbrainzMigration{
		EntityType: entity,
		OldMBID:    old,
		Kind:       models.MigrationKindDeleted,
		Status:     models.MigrationStatusPending,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}
	return m
}

func newLibrary(t *testing.T, db *gorm.DB) models.Library {
	t.Helper()
	lib := models.Library{Name: "L", Path: t.TempDir(), Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	return lib
}

// --- release merges -------------------------------------------------------

func TestReleaseRedirectRemapsFiles(t *testing.T) {
	db := testDB(t)
	lib := newLibrary(t, db)
	for _, p := range []string{"/m/a.flac", "/m/b.flac"} {
		if err := db.Create(&models.LibraryItem{
			LibraryID: lib.ID, Path: p, Status: models.LibraryItemStatusOK,
			MBReleaseID: "rel-old", ProcessedVersion: "1.0.0",
		}).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}

	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}

	var items []models.LibraryItem
	if err := db.Find(&items).Error; err != nil {
		t.Fatalf("find items: %v", err)
	}
	for _, item := range items {
		if item.MBReleaseID != "rel-new" {
			t.Errorf("%s still points at %q", item.Path, item.MBReleaseID)
		}
		// Track and recording MBIDs are release-scoped, so they are dead too. The
		// blanked version is what makes the next scan re-correlate these files
		// instead of skipping them as unchanged.
		if item.ProcessedVersion != "" {
			t.Errorf("%s kept processed_version %q; the next scan will skip it and leave stale track IDs",
				item.Path, item.ProcessedVersion)
		}
	}

	var applied models.MusicbrainzMigration
	if err := db.First(&applied, "id = ?", m.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if applied.Status != models.MigrationStatusApplied || applied.AppliedAt == nil {
		t.Errorf("status = %q, applied_at = %v", applied.Status, applied.AppliedAt)
	}
}

// The collision case: both editions already exist as rows because files were owned
// under each ID. collection_releases.mb_id is uniquely indexed, so a blind update
// would fail — the stale row has to go and let Rebuild recount.
func TestReleaseRedirectMergesCollidingEditionRows(t *testing.T) {
	db := testDB(t)
	for _, mbID := range []string{"rel-old", "rel-new"} {
		if err := db.Create(&models.CollectionRelease{
			MBID: mbID, ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1",
			Title: "Album", OwnedTracks: 3, TotalTracks: 10,
		}).Error; err != nil {
			t.Fatalf("create release: %v", err)
		}
	}

	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}

	var rows []models.CollectionRelease
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(rows) != 1 || rows[0].MBID != "rel-new" {
		t.Fatalf("editions after merge = %+v, want one row on rel-new", rows)
	}
}

func TestReleaseRedirectMovesEditionRowWhenNoCollision(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionRelease{
		MBID: "rel-old", ReleaseGroupMBID: "rg-1", Title: "Album",
	}).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}

	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}

	var row models.CollectionRelease
	if err := db.Where("mb_id = ?", "rel-new").First(&row).Error; err != nil {
		t.Fatalf("edition was not moved to the surviving MBID: %v", err)
	}
	if row.Title != "Album" {
		t.Errorf("title = %q, want it preserved through the move", row.Title)
	}
}

// A pinned item is a release someone identified by hand. A merge renames that
// release rather than substituting a different one, so the pin follows it — leaving
// it on a dead ID would break the one file the user took trouble over.
func TestReleaseRedirectRemapsPinnedItems(t *testing.T) {
	db := testDB(t)
	lib := newLibrary(t, db)
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/pinned.flac", Status: models.LibraryItemStatusOK,
		MBReleaseID: "rel-old", Pinned: true, CorrelationSource: models.CorrelationSourceManual,
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}

	var item models.LibraryItem
	if err := db.Where("path = ?", "/m/pinned.flac").First(&item).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if item.MBReleaseID != "rel-new" {
		t.Errorf("pinned item still on %q", item.MBReleaseID)
	}
	if !item.Pinned || item.CorrelationSource != models.CorrelationSourceManual {
		t.Errorf("the pin itself must survive the remap: pinned=%v source=%q", item.Pinned, item.CorrelationSource)
	}
}

// --- artist merges --------------------------------------------------------

// Monitoring and follow types are authored, not derived: nothing can reconstruct
// them from disk. A merge must union them, never pick a winner — silently
// un-following someone because their MBID moved is the worst outcome available.
func TestArtistRedirectUnionsAuthoredState(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{
		MBID: "art-old", Name: "The Beatles", Monitored: true,
		FollowTypes: "Album,EP", Origin: models.CollectionOriginManual,
	}).Error; err != nil {
		t.Fatalf("create source artist: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{
		MBID: "art-new", Name: "Beatles", Monitored: false,
		FollowTypes: "Album,Single", FollowSecondary: true, Origin: models.CollectionOriginLibrary,
	}).Error; err != nil {
		t.Fatalf("create target artist: %v", err)
	}

	m := pendingRedirect(t, db, models.MigrationEntityArtist, "art-old", "art-new")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}

	var rows []models.CollectionArtist
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("artists after merge = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.MBID != "art-new" {
		t.Errorf("survivor = %q, want art-new", got.MBID)
	}
	if !got.Monitored {
		t.Error("monitoring was lost: either side monitored means the survivor is monitored")
	}
	if !got.FollowSecondary {
		t.Error("follow-secondary was lost")
	}
	if got.FollowTypes != "Album,Single,EP" {
		t.Errorf("follow types = %q, want the union of both sides", got.FollowTypes)
	}
	if got.Origin != models.CollectionOriginManual {
		t.Errorf("origin = %q; a manual addition outranks a library-derived row", got.Origin)
	}
}

func TestArtistRedirectMovesRowWhenTargetAbsent(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "art-old", Name: "Aphex Twin", Monitored: true}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	m := pendingRedirect(t, db, models.MigrationEntityArtist, "art-old", "art-new")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}

	var row models.CollectionArtist
	if err := db.Where("mb_id = ?", "art-new").First(&row).Error; err != nil {
		t.Fatalf("artist was not moved: %v", err)
	}
	if !row.Monitored || row.Name != "Aphex Twin" {
		t.Errorf("row = %+v, want it carried over intact", row)
	}
}

// The composite unique index on (release_group, artist) is the one place a blind
// UPDATE errors outright: a collaboration credited to both sides of a merge — which
// is what a merge means — collides.
func TestArtistRedirectDedupesCreditLinks(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: "rg-1", ArtistMBID: "art-old", Position: 0,
	}).Error; err != nil {
		t.Fatalf("create link: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: "rg-1", ArtistMBID: "art-new", Position: 2,
	}).Error; err != nil {
		t.Fatalf("create link: %v", err)
	}
	// A second group credited only to the old ID, which must simply move across.
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: "rg-2", ArtistMBID: "art-old", Position: 1,
	}).Error; err != nil {
		t.Fatalf("create link: %v", err)
	}

	m := pendingRedirect(t, db, models.MigrationEntityArtist, "art-old", "art-new")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}

	var links []models.CollectionReleaseGroupArtist
	if err := db.Order("release_group_mb_id").Find(&links).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("links = %d, want 2 (one per release-group)", len(links))
	}
	for _, l := range links {
		if l.ArtistMBID != "art-new" {
			t.Errorf("%s still credited to %q", l.ReleaseGroupMBID, l.ArtistMBID)
		}
	}
	if links[0].Position != 0 {
		t.Errorf("position = %d, want the more prominent (lower) of the two credits kept", links[0].Position)
	}
}

func TestArtistRedirectRepointsEverythingKeyedOnTheArtist(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-1", ArtistMBID: "art-old", Title: "A"}).Error; err != nil {
		t.Fatalf("create rg: %v", err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-1", ArtistMBID: "art-old"}).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := db.Create(&models.CollectionDesire{ArtistMBID: "art-old", ReleaseGroupMBID: "rg-1"}).Error; err != nil {
		t.Fatalf("create desire: %v", err)
	}

	m := pendingRedirect(t, db, models.MigrationEntityArtist, "art-old", "art-new")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}

	var rg models.CollectionReleaseGroup
	if err := db.First(&rg).Error; err != nil || rg.ArtistMBID != "art-new" {
		t.Errorf("release-group artist = %q (err %v)", rg.ArtistMBID, err)
	}
	var rel models.CollectionRelease
	if err := db.First(&rel).Error; err != nil || rel.ArtistMBID != "art-new" {
		t.Errorf("release artist = %q (err %v)", rel.ArtistMBID, err)
	}
	var desire models.CollectionDesire
	if err := db.First(&desire).Error; err != nil || desire.ArtistMBID != "art-new" {
		t.Errorf("desire artist = %q (err %v)", desire.ArtistMBID, err)
	}
}

// --- desires --------------------------------------------------------------

// Two wants that turn out to name the same album are one want. The recording sets
// are unioned because each was a real choice, and an empty set means "the whole
// thing", which must not be narrowed back down by merging.
func TestDesiresAreDedupedAfterRemap(t *testing.T) {
	cases := []struct {
		name     string
		a, b     []string
		wantLen  int
		wantWhat string
	}{
		{"track selections union", []string{"rec-1"}, []string{"rec-2"}, 2, "both selections"},
		{"whole-album subsumes a selection", nil, []string{"rec-2"}, 0, "the whole release-group"},
		{"selection subsumed by whole-album", []string{"rec-1"}, nil, 0, "the whole release-group"},
		{"identical selections collapse", []string{"rec-1"}, []string{"rec-1"}, 1, "one copy"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			if err := db.Create(&models.CollectionDesire{
				ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-old", RecordingMBIDs: tc.a,
			}).Error; err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := db.Create(&models.CollectionDesire{
				ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-new", RecordingMBIDs: tc.b,
			}).Error; err != nil {
				t.Fatalf("create: %v", err)
			}

			m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")
			if _, err := ApplyByID(db, m.ID); err != nil {
				t.Fatalf("ApplyByID: %v", err)
			}

			var rows []models.CollectionDesire
			if err := db.Find(&rows).Error; err != nil {
				t.Fatalf("find: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("desires = %d, want 1 after dedupe", len(rows))
			}
			if rows[0].ReleaseMBID != "rel-new" {
				t.Errorf("desire release = %q, want rel-new", rows[0].ReleaseMBID)
			}
			if len(rows[0].RecordingMBIDs) != tc.wantLen {
				t.Errorf("recordings = %v, want %s (%d entries)", rows[0].RecordingMBIDs, tc.wantWhat, tc.wantLen)
			}
		})
	}
}

// TestDedupedDesireKeepsTheStrongerProvenance: an upstream merge must not demote a
// hand-authored want into a derived one. The reconciliation passes may re-point or
// prune the rows they own, so a manual want that came out of a merge labelled
// `manager` would be the user's pick quietly handed to the mirror.
func TestDedupedDesireKeepsTheStrongerProvenance(t *testing.T) {
	// Either creation order: the survivor is whichever row was written first, so the
	// provenance must be merged rather than inherited from the keeper.
	for _, order := range []struct {
		name           string
		first, second  string
		firstRel, next string
	}{
		{"derived first", models.DesireSourceManager, models.DesireSourceManual, "rel-old", "rel-new"},
		{"authored first", models.DesireSourceManual, models.DesireSourceManager, "rel-old", "rel-new"},
	} {
		t.Run(order.name, func(t *testing.T) {
			db := testDB(t)
			if err := db.Create(&models.CollectionDesire{
				ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: order.firstRel,
				Source: order.first,
			}).Error; err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := db.Create(&models.CollectionDesire{
				ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: order.next,
				Source: order.second,
			}).Error; err != nil {
				t.Fatalf("create: %v", err)
			}

			m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")
			if _, err := ApplyByID(db, m.ID); err != nil {
				t.Fatalf("ApplyByID: %v", err)
			}

			var rows []models.CollectionDesire
			if err := db.Find(&rows).Error; err != nil {
				t.Fatalf("find: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("desires = %d, want 1 after dedupe", len(rows))
			}
			if rows[0].Source != models.DesireSourceManual {
				t.Errorf("source = %q, want the authored one to survive", rows[0].Source)
			}
		})
	}
}

// --- deletions ------------------------------------------------------------

// Deletion is non-destructive by design: the files stay indexed and keep the MB IDs
// they had, because a dead ID is still the best record of what the file was thought
// to be. Only the status changes, into the queue that already means "needs
// identifying".
func TestDeletedReleaseUnmatchesFilesWithoutDestroyingAnything(t *testing.T) {
	db := testDB(t)
	lib := newLibrary(t, db)
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: models.LibraryItemStatusOK,
		MBReleaseID: "rel-dead", MBRecordingID: "rec-1",
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: "rel-dead", ReleaseGroupMBID: "rg-1"}).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := db.Create(&models.CollectionDesire{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-dead",
	}).Error; err != nil {
		t.Fatalf("create desire: %v", err)
	}

	m := pendingDeletion(t, db, models.MigrationEntityRelease, "rel-dead")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}

	var item models.LibraryItem
	if err := db.First(&item).Error; err != nil {
		t.Fatalf("find item: %v", err)
	}
	if item.Status != models.LibraryItemStatusUnmatched {
		t.Errorf("status = %q, want unmatched", item.Status)
	}
	if item.MBReleaseID != "rel-dead" || item.MBRecordingID != "rec-1" {
		t.Errorf("MB IDs were cleared (%q/%q); they are the record of what the file was",
			item.MBReleaseID, item.MBRecordingID)
	}
	if item.Error == "" {
		t.Error("no explanation recorded on the item")
	}

	var editions int64
	if err := db.Model(&models.CollectionRelease{}).Count(&editions).Error; err != nil {
		t.Fatalf("count editions: %v", err)
	}
	if editions != 0 {
		t.Error("the owned-edition row survived a release that no longer exists")
	}

	// The one thing that must never be collateral damage.
	var desires int64
	if err := db.Model(&models.CollectionDesire{}).Count(&desires).Error; err != nil {
		t.Fatalf("count desires: %v", err)
	}
	if desires != 1 {
		t.Errorf("desires = %d, want the authored want kept: it is now the only record of what was asked for", desires)
	}
}

// A deleted artist orphans no file — files are keyed by release, and MusicBrainz
// re-credits releases rather than deleting them along with the artist.
func TestDeletedArtistLeavesFilesAlone(t *testing.T) {
	db := testDB(t)
	lib := newLibrary(t, db)
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-1",
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "art-dead", Name: "Gone"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	m := pendingDeletion(t, db, models.MigrationEntityArtist, "art-dead")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}

	var item models.LibraryItem
	if err := db.First(&item).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if item.Status != models.LibraryItemStatusOK || item.MBReleaseID != "rel-1" {
		t.Errorf("file was disturbed by an artist deletion: %+v", item)
	}
	var artists int64
	if err := db.Model(&models.CollectionArtist{}).Count(&artists).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if artists != 0 {
		t.Error("the artist row survived a deletion")
	}
}

// --- policy ---------------------------------------------------------------

func TestPolicyHoldsOnlyWhatItIsToldTo(t *testing.T) {
	release := models.MusicbrainzMigration{EntityType: models.MigrationEntityRelease, Kind: models.MigrationKindRedirect}
	artist := models.MusicbrainzMigration{EntityType: models.MigrationEntityArtist, Kind: models.MigrationKindRedirect}
	deletion := models.MusicbrainzMigration{EntityType: models.MigrationEntityRelease, Kind: models.MigrationKindDeleted}
	pinned := models.MusicbrainzMigration{EntityType: models.MigrationEntityRelease, Kind: models.MigrationKindRedirect, TouchesPinned: true}

	// The zero policy is the default, and it must apply everything: a config.json
	// written before this feature existed decodes to exactly this.
	var zero Policy
	for _, m := range []models.MusicbrainzMigration{release, artist, deletion, pinned} {
		if zero.heldForReview(m) {
			t.Errorf("zero policy held %s/%s; the default must be to apply", m.EntityType, m.Kind)
		}
	}

	if !(Policy{ReviewReleases: true}).heldForReview(release) {
		t.Error("release review did not hold a release redirect")
	}
	if (Policy{ReviewReleases: true}).heldForReview(artist) {
		t.Error("release review held an artist redirect")
	}
	if !(Policy{ReviewArtists: true}).heldForReview(artist) {
		t.Error("artist review did not hold an artist redirect")
	}
	if !(Policy{ReviewDeletions: true}).heldForReview(deletion) {
		t.Error("deletion review did not hold a deletion")
	}
	if (Policy{ReviewDeletions: true}).heldForReview(release) {
		t.Error("deletion review held a redirect")
	}
	// Pinned is an override, not another category: it holds regardless of type.
	if !(Policy{ReviewPinned: true}).heldForReview(pinned) {
		t.Error("pinned review did not hold a migration touching a manual correlation")
	}
	if (Policy{ReviewPinned: true}).heldForReview(release) {
		t.Error("pinned review held a migration that touches no pinned item")
	}
}

func TestProcessPendingAppliesAndHolds(t *testing.T) {
	db := testDB(t)
	lib := newLibrary(t, db)
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-old",
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")
	pendingRedirect(t, db, models.MigrationEntityArtist, "art-old", "art-new")

	res, err := ProcessPending(db, Policy{ReviewArtists: true})
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if res.Applied != 1 || res.Pending != 1 {
		t.Errorf("applied/pending = %d/%d, want 1/1", res.Applied, res.Pending)
	}
	if res.Files != 1 {
		t.Errorf("files remapped = %d, want 1", res.Files)
	}

	var held models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", "art-old").First(&held).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if held.Status != models.MigrationStatusPending {
		t.Errorf("held migration status = %q, want it still pending", held.Status)
	}
}

// The impact snapshot is what lets a queued row describe itself in the review UI,
// and it is also where TouchesPinned is discovered — detection runs in modules,
// which cannot see library_items.
func TestProcessPendingMeasuresImpact(t *testing.T) {
	db := testDB(t)
	lib := newLibrary(t, db)
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: models.LibraryItemStatusOK,
		MBReleaseID: "rel-old", Pinned: true,
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	if err := db.Create(&models.CollectionDesire{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-old",
	}).Error; err != nil {
		t.Fatalf("create desire: %v", err)
	}
	pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")

	res, err := ProcessPending(db, Policy{ReviewPinned: true})
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if res.Pending != 1 || res.Applied != 0 {
		t.Fatalf("applied/pending = %d/%d, want 0/1 — a pinned item under pinned review", res.Applied, res.Pending)
	}

	var m models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", "rel-old").First(&m).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if !m.TouchesPinned {
		t.Error("TouchesPinned was not detected")
	}
	if m.AffectedFiles != 1 || m.AffectedDesires != 1 {
		t.Errorf("impact = %d files / %d desires, want 1/1", m.AffectedFiles, m.AffectedDesires)
	}
}

// Approving is itself the decision the policy was deferring, so it applies even
// when the policy still says "hold this kind".
func TestApproveOverridesPolicy(t *testing.T) {
	db := testDB(t)
	lib := newLibrary(t, db)
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: models.LibraryItemStatusOK,
		MBReleaseID: "rel-old", Pinned: true,
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")
	if _, err := ProcessPending(db, Policy{ReviewPinned: true}); err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}

	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}
	var item models.LibraryItem
	if err := db.First(&item).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if item.MBReleaseID != "rel-new" {
		t.Errorf("approval did not apply the migration: %q", item.MBReleaseID)
	}
}

func TestDismissKeepsTheRowSoItIsNotReQueued(t *testing.T) {
	db := testDB(t)
	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")

	if _, err := Dismiss(db, m.ID); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}

	var row models.MusicbrainzMigration
	if err := db.First(&row, "id = ?", m.ID).Error; err != nil {
		t.Fatalf("the dismissed row must survive, or the next fetch re-queues it: %v", err)
	}
	if row.Status != models.MigrationStatusDismissed {
		t.Errorf("status = %q, want dismissed", row.Status)
	}

	res, err := ProcessPending(db, Policy{})
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if res.Applied != 0 {
		t.Error("a dismissed migration was applied by the next run")
	}
}

func TestApplyingTwiceIsRefused(t *testing.T) {
	db := testDB(t)
	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}
	if _, err := ApplyByID(db, m.ID); err == nil {
		t.Error("re-applying an applied migration was allowed")
	}
}

func TestRedirectWithoutTargetFails(t *testing.T) {
	db := testDB(t)
	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "")

	if _, err := ApplyByID(db, m.ID); err == nil {
		t.Fatal("a redirect with no target was applied")
	}

	var row models.MusicbrainzMigration
	if err := db.First(&row, "id = ?", m.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.Status != models.MigrationStatusFailed || row.Error == "" {
		t.Errorf("status/error = %q/%q, want a failed row that stays visible", row.Status, row.Error)
	}
}

// --- release-group relinking ---------------------------------------------

// A release naming a different release-group is either a group merge or that one
// release moving. The payload cannot tell them apart, so only the release in hand is
// re-pointed — remapping the group globally would be right for a merge and
// destructive for a move.
func TestRelinkReleaseMovesOnlyTheReleaseInHand(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-old"}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: "rel-2", ReleaseGroupMBID: "rg-old"}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	moved, err := RelinkRelease(db, "rel-1", "rg-new")
	if err != nil {
		t.Fatalf("RelinkRelease: %v", err)
	}
	if !moved {
		t.Error("relink reported no change")
	}

	var one, two models.CollectionRelease
	if err := db.Where("mb_id = ?", "rel-1").First(&one).Error; err != nil {
		t.Fatalf("find rel-1: %v", err)
	}
	if err := db.Where("mb_id = ?", "rel-2").First(&two).Error; err != nil {
		t.Fatalf("find rel-2: %v", err)
	}
	if one.ReleaseGroupMBID != "rg-new" {
		t.Errorf("rel-1 group = %q, want rg-new", one.ReleaseGroupMBID)
	}
	if two.ReleaseGroupMBID != "rg-old" {
		t.Errorf("rel-2 group = %q; the sibling release must not be dragged along", two.ReleaseGroupMBID)
	}
}

func TestRelinkReleaseIsANoOpWhenUnchanged(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-1"}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	moved, err := RelinkRelease(db, "rel-1", "rg-1")
	if err != nil || moved {
		t.Errorf("moved = %v, err = %v; want no change", moved, err)
	}

	// A release nobody owns is not an error either.
	moved, err = RelinkRelease(db, "rel-unknown", "rg-1")
	if err != nil || moved {
		t.Errorf("moved = %v, err = %v for an unknown release; want a quiet no-op", moved, err)
	}
}

func TestListAndPendingCount(t *testing.T) {
	db := testDB(t)
	pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")
	m := pendingRedirect(t, db, models.MigrationEntityArtist, "art-old", "art-new")
	if _, err := Dismiss(db, m.ID); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}

	all, err := List(db, "", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("List(all) = %d rows, want 2", len(all))
	}

	pending, err := List(db, models.MigrationStatusPending, 0)
	if err != nil {
		t.Fatalf("List(pending): %v", err)
	}
	if len(pending) != 1 || pending[0].OldMBID != "rel-old" {
		t.Errorf("List(pending) = %+v, want just rel-old", pending)
	}

	n, err := PendingCount(db)
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	if n != 1 {
		t.Errorf("PendingCount = %d, want 1", n)
	}
}

func TestApplyByIDUnknownMigration(t *testing.T) {
	db := testDB(t)
	if _, err := ApplyByID(db, uuid.New()); err == nil {
		t.Error("applying an unknown migration was allowed")
	}
	if _, err := Dismiss(db, uuid.New()); err == nil {
		t.Error("dismissing an unknown migration was allowed")
	}
}

// The identity sweep drains the queue once after releases and again after artists.
// Without accumulation the summary would report only the second drain, hiding
// everything the first one did.
func TestResultAdd(t *testing.T) {
	first := Result{Applied: 2, Pending: 1, Files: 5, Errors: []string{"a"}}
	first.Add(Result{Applied: 1, Failed: 1, Unmatched: 3, Errors: []string{"b"}})

	want := Result{
		Applied: 3, Pending: 1, Failed: 1, Files: 5, Unmatched: 3,
		Errors: []string{"a", "b"},
	}
	if first.Applied != want.Applied || first.Pending != want.Pending || first.Failed != want.Failed {
		t.Errorf("counts = %+v, want %+v", first, want)
	}
	if first.Files != want.Files || first.Unmatched != want.Unmatched {
		t.Errorf("file counts = %+v, want %+v", first, want)
	}
	if len(first.Errors) != 2 {
		t.Errorf("errors = %v, want both runs' errors", first.Errors)
	}
}

func TestPolicyFromConfig(t *testing.T) {
	got := PolicyFromConfig(models.ConfigStruct{
		AutotaggerrMigrationReviewReleases:  true,
		AutotaggerrMigrationReviewDeletions: true,
	})
	want := Policy{ReviewReleases: true, ReviewDeletions: true}
	if got != want {
		t.Errorf("PolicyFromConfig = %+v, want %+v", got, want)
	}
}
