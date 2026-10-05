package collection

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

func live(ids ...string) []models.MusicBrainzArtistReleaseGroup {
	out := make([]models.MusicBrainzArtistReleaseGroup, 0, len(ids))
	for _, id := range ids {
		out = append(out, models.MusicBrainzArtistReleaseGroup{ID: id, Title: id})
	}
	return out
}

// storeGroup writes a release-group credited to an artist, the way both writers do:
// the primary-credit column plus a link row.
func storeGroup(t *testing.T, db *gorm.DB, mbID, artistMBID string, mutate func(*models.CollectionReleaseGroup)) {
	t.Helper()
	rg := models.CollectionReleaseGroup{MBID: mbID, ArtistMBID: artistMBID, Title: mbID}
	if mutate != nil {
		mutate(&rg)
	}
	if err := db.Create(&rg).Error; err != nil {
		t.Fatalf("create release-group: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: mbID, ArtistMBID: artistMBID,
	}).Error; err != nil {
		t.Fatalf("create credit link: %v", err)
	}
}

func groupExists(t *testing.T, db *gorm.DB, mbID string) bool {
	t.Helper()
	var n int64
	if err := db.Model(&models.CollectionReleaseGroup{}).Where("mb_id = ?", mbID).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n > 0
}

// The case this exists for: a release-group merged away upstream. It is never
// fetched by ID, so there is no redirect to observe — it simply stops appearing in
// the artist's discography, and nothing else would ever remove it.
func TestPruneRemovesGroupsMusicBrainzNoLongerLists(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-live", "art-1", nil)
	storeGroup(t, db, "rg-merged-away", "art-1", nil)

	pruned, err := PruneOrphanReleaseGroups(db, "art-1", live("rg-live"))
	if err != nil {
		t.Fatalf("PruneOrphanReleaseGroups: %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}
	if !groupExists(t, db, "rg-live") {
		t.Error("a group still in the discography was removed")
	}
	if groupExists(t, db, "rg-merged-away") {
		t.Error("the orphaned group survived")
	}

	// Its credit links go with it, or the artist page keeps listing a row that is
	// no longer there.
	var links int64
	if err := db.Model(&models.CollectionReleaseGroupArtist{}).
		Where("release_group_mb_id = ?", "rg-merged-away").Count(&links).Error; err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Errorf("%d credit links left dangling", links)
	}
}

// Absence from a discography is weak evidence. Each of these is a reason it can be
// innocent, and each must veto the prune on its own.
func TestPruneRefusesWhenSomethingElseClaimsTheGroup(t *testing.T) {
	cases := []struct {
		name  string
		claim func(t *testing.T, db *gorm.DB)
		why   string
	}{
		{
			name: "files on disk own it",
			claim: func(t *testing.T, db *gorm.DB) {
				if err := db.Model(&models.CollectionReleaseGroup{}).
					Where("mb_id = ?", "rg-gone").Update("owned", true).Error; err != nil {
					t.Fatalf("update: %v", err)
				}
			},
			why: "Rebuild derives ownership from real files; a browse result does not outrank them",
		},
		{
			name: "a manager lists it",
			claim: func(t *testing.T, db *gorm.DB) {
				if err := db.Model(&models.CollectionReleaseGroup{}).
					Where("mb_id = ?", "rg-gone").Update("in_catalog", true).Error; err != nil {
					t.Fatalf("update: %v", err)
				}
			},
			why: "Lidarr's catalog is a separate authority",
		},
		{
			name: "a desire references it",
			claim: func(t *testing.T, db *gorm.DB) {
				if err := db.Create(&models.CollectionDesire{
					ArtistMBID: "art-1", ReleaseGroupMBID: "rg-gone",
				}).Error; err != nil {
					t.Fatalf("create desire: %v", err)
				}
			},
			why: "authored intent is never collateral damage",
		},
		{
			name: "an owned edition points at it",
			claim: func(t *testing.T, db *gorm.DB) {
				if err := db.Create(&models.CollectionRelease{
					MBID: "rel-1", ReleaseGroupMBID: "rg-gone",
				}).Error; err != nil {
					t.Fatalf("create release: %v", err)
				}
			},
			why: "files resolved to this group even if the owned flag has not caught up",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			storeGroup(t, db, "rg-live", "art-1", nil)
			storeGroup(t, db, "rg-gone", "art-1", nil)
			tc.claim(t, db)

			pruned, err := PruneOrphanReleaseGroups(db, "art-1", live("rg-live"))
			if err != nil {
				t.Fatalf("PruneOrphanReleaseGroups: %v", err)
			}
			if pruned != 0 {
				t.Errorf("pruned %d rows; must not: %s", pruned, tc.why)
			}
			if !groupExists(t, db, "rg-gone") {
				t.Errorf("group was removed; must not: %s", tc.why)
			}
		})
	}
}

// A collaboration absent from one artist's discography loses that artist's credit and
// nothing more. Skipping it instead was the deadlock this replaced: each credited
// artist's prune deferred to the other, so a dead collaboration stayed on both pages.
func TestPruneWithdrawsOnlyThisArtistsCreditFromACollaboration(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-live", "art-1", nil)
	storeGroup(t, db, "rg-collab", "art-1", nil)
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: "rg-collab", ArtistMBID: "art-2", Position: 1,
	}).Error; err != nil {
		t.Fatalf("create link: %v", err)
	}

	pruned, err := PruneOrphanReleaseGroups(db, "art-1", live("rg-live"))
	if err != nil {
		t.Fatalf("PruneOrphanReleaseGroups: %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1 (the group left art-1's page)", pruned)
	}
	if !groupExists(t, db, "rg-collab") {
		t.Fatal("the row was deleted; art-2's credit still stands on it")
	}

	// Off art-1's page by both routes ReleaseGroupsForArtist reads — the link and the
	// primary-credit column — and still on art-2's.
	for artist, want := range map[string]bool{"art-1": false, "art-2": true} {
		groups, err := ReleaseGroupsForArtist(db, artist)
		if err != nil {
			t.Fatalf("ReleaseGroupsForArtist(%s): %v", artist, err)
		}
		found := false
		for _, g := range groups {
			if g.MBID == "rg-collab" {
				found = true
			}
		}
		if found != want {
			t.Errorf("%s sees the collaboration = %v, want %v", artist, found, want)
		}
	}

	// The second artist's prune finds the last credit and takes the row.
	storeGroup(t, db, "rg-art2-live", "art-2", nil)
	pruned, err = PruneOrphanReleaseGroups(db, "art-2", live("rg-art2-live"))
	if err != nil {
		t.Fatalf("PruneOrphanReleaseGroups: %v", err)
	}
	if pruned != 1 || groupExists(t, db, "rg-collab") {
		t.Errorf("pruned = %d, row exists = %v; the last credit's prune must remove it",
			pruned, groupExists(t, db, "rg-collab"))
	}
}

// Withdrawing a credit from a group art-1 is not primary on leaves the column alone.
func TestPruneWithdrawalKeepsAnotherArtistsPrimaryCredit(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-live", "art-1", nil)
	storeGroup(t, db, "rg-collab", "art-2", nil)
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: "rg-collab", ArtistMBID: "art-1", Position: 1,
	}).Error; err != nil {
		t.Fatalf("create link: %v", err)
	}

	if _, err := PruneOrphanReleaseGroups(db, "art-1", live("rg-live")); err != nil {
		t.Fatalf("PruneOrphanReleaseGroups: %v", err)
	}
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-collab").First(&rg).Error; err != nil {
		t.Fatalf("read group: %v", err)
	}
	if rg.ArtistMBID != "art-2" {
		t.Errorf("primary credit = %q, want art-2 untouched", rg.ArtistMBID)
	}
}

// An empty discography is far more likely to be a service quirk than an artist whose
// entire catalogue was merged away, so it is treated as no evidence at all.
func TestPruneRefusesOnAnEmptyDiscography(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-1", "art-1", nil)

	pruned, err := PruneOrphanReleaseGroups(db, "art-1", nil)
	if err != nil {
		t.Fatalf("PruneOrphanReleaseGroups: %v", err)
	}
	if pruned != 0 || !groupExists(t, db, "rg-1") {
		t.Error("an empty discography must never empty the collection")
	}
}

func TestPruneLeavesOtherArtistsAlone(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-1", "art-1", nil)
	storeGroup(t, db, "rg-2", "art-2", nil)

	pruned, err := PruneOrphanReleaseGroups(db, "art-1", live("rg-1"))
	if err != nil {
		t.Fatalf("PruneOrphanReleaseGroups: %v", err)
	}
	if pruned != 0 {
		t.Errorf("pruned = %d, want 0", pruned)
	}
	if !groupExists(t, db, "rg-2") {
		t.Error("another artist's release-group was pruned")
	}
}

func TestAllMBIDs(t *testing.T) {
	db := testDB(t)
	lib := models.Library{Name: "L", Path: t.TempDir(), Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	// Two files on one release, so the scope must dedupe rather than verify it twice.
	for _, p := range []string{"/m/a.flac", "/m/b.flac"} {
		if err := db.Create(&models.LibraryItem{
			LibraryID: lib.ID, Path: p, Status: models.LibraryItemStatusOK, MBReleaseID: "rel-1",
		}).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}
	// A file with no correlation contributes nothing.
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/c.flac", Status: models.LibraryItemStatusUnmatched,
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	// An edition with no file behind it is still worth verifying.
	if err := db.Create(&models.CollectionRelease{MBID: "rel-2", ReleaseGroupMBID: "rg-1"}).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "art-1", Name: "A"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	releases, artists, err := AllMBIDs(db)
	if err != nil {
		t.Fatalf("AllMBIDs: %v", err)
	}
	if len(releases) != 2 {
		t.Errorf("releases = %v, want rel-1 and rel-2 exactly once each", releases)
	}
	if len(artists) != 1 || artists[0] != "art-1" {
		t.Errorf("artists = %v, want [art-1]", artists)
	}
}
