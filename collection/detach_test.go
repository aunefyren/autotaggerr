package collection

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// Detaching a manager: the point is that its decisions survive the change of
// authority, and that the change itself survives the next scan.

// managedCollection seeds one artist whose one file sits in a Lidarr-managed
// library, plus a cached release so Rebuild can re-derive provenance from it. It is
// the state detaching operates on: a real correlation under a real manager, not just
// a CollectionArtist row asserting a managed_by.
func managedCollection(t *testing.T) (*gorm.DB, models.Manager) {
	t.Helper()
	db := testDB(t)
	modules.SetDB(db)
	t.Cleanup(func() { modules.SetDB(nil) })

	release := models.MusicBrainzReleaseResponse{
		ID:           "rel-1",
		Title:        "Album One",
		ArtistCredit: []models.ArtistCredit{{Name: "The Band", Artist: models.Artist{ID: "art-1", Name: "The Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Album One", PrimaryType: "Album"},
		Media:        []models.MusicBrainzMedia{{Tracks: []models.Track{{ID: "t1"}}}},
	}
	payload, _ := json.Marshal(release)
	if err := db.Create(&models.MusicbrainzReleaseCache{
		MBID: "rel-1", Payload: string(payload),
		FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	manager := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := db.Create(&manager).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	lib := models.Library{Name: "L", Path: "/m", ManagerID: &manager.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-1",
	}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}

	// Rebuild materialises the artist with provenance derived from the library.
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	return db, manager
}

// managerWant records what the manager selected, the way reconcileManagerDesires
// would have.
func managerWant(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Create(&models.CollectionDesire{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-1",
		Source: models.DesireSourceManager,
	}).Error; err != nil {
		t.Fatalf("manager want: %v", err)
	}
}

func artistRow(t *testing.T, db *gorm.DB, mbID string) models.CollectionArtist {
	t.Helper()
	var a models.CollectionArtist
	if err := db.Where("mb_id = ?", mbID).First(&a).Error; err != nil {
		t.Fatalf("artist %s: %v", mbID, err)
	}
	return a
}

// TestDetachKeepsTheManagersDecisions is the promise the verb makes: the mirrored
// want is still there afterwards, and it is now the user's own — which is also what
// stops reconcileManagerDesires from pruning it as an orphan on its next run.
func TestDetachKeepsTheManagersDecisions(t *testing.T) {
	db, _ := managedCollection(t)
	managerWant(t, db)

	result, err := DetachArtist(db, "art-1")
	if err != nil {
		t.Fatalf("DetachArtist: %v", err)
	}
	if result.WantsKept != 1 {
		t.Errorf("wants kept = %d, want 1", result.WantsKept)
	}

	var desires []models.CollectionDesire
	if err := db.Where("release_group_mb_id = ?", "rg-1").Find(&desires).Error; err != nil {
		t.Fatalf("desires: %v", err)
	}
	if len(desires) != 1 {
		t.Fatalf("want the mirrored want kept, got %+v", desires)
	}
	if desires[0].Source != models.DesireSourceManual {
		t.Errorf("want source = %q, want manual", desires[0].Source)
	}
	if desires[0].ReleaseMBID != "rel-1" {
		t.Errorf("want edition = %q, want rel-1 — the manager's pick is the decision being kept", desires[0].ReleaseMBID)
	}
}

// TestDetachTakesAuthorityNatively: after detaching, the artist is native, and the
// two gates that read provenance open. Both are what "a change of authority" means
// in practice — following governs again, and identity becomes the user's to set.
func TestDetachTakesAuthorityNatively(t *testing.T) {
	db, _ := managedCollection(t)

	if before := artistRow(t, db, "art-1"); before.ManagedBy != models.ManagedByLidarr {
		t.Fatalf("seeded artist managed_by = %q, want lidarr", before.ManagedBy)
	}

	if _, err := DetachArtist(db, "art-1"); err != nil {
		t.Fatalf("DetachArtist: %v", err)
	}

	a := artistRow(t, db, "art-1")
	if !a.ManagerDetached {
		t.Error("manager_detached = false, want true")
	}
	if a.ManagedBy != models.ManagedByAutotaggerr {
		t.Errorf("managed_by = %q, want autotaggerr", a.ManagedBy)
	}
	if !FollowGoverns(a) || !IdentityEditable(a) {
		t.Errorf("detached artist still gated: follow_governs=%v identity_editable=%v", FollowGoverns(a), IdentityEditable(a))
	}
}

// TestDetachSwitchesFollowingOff guards the non-obvious half of the verb. Following
// is stored but does not govern under a manager, so a Lidarr artist can carry a
// stale Monitored flag. Detaching makes following govern again — so leaving that
// flag set would turn a detach into "and auto-want the whole back catalogue", an
// effect the user never asked for from a control they cannot see.
func TestDetachSwitchesFollowingOff(t *testing.T) {
	db, _ := managedCollection(t)
	if err := db.Model(&models.CollectionArtist{}).Where("mb_id = ?", "art-1").
		Update("monitored", true).Error; err != nil {
		t.Fatalf("set monitored: %v", err)
	}

	result, err := DetachArtist(db, "art-1")
	if err != nil {
		t.Fatalf("DetachArtist: %v", err)
	}
	if !result.FollowCleared {
		t.Error("follow_cleared = false, want true so the caller can say so")
	}
	if artistRow(t, db, "art-1").Monitored {
		t.Error("monitored = true after detaching, want false")
	}
}

// TestRebuildDoesNotRevertADetach is the reason ManagerDetached is stored at all.
// managed_by is re-derived from the library's manager on every scan, so without the
// override the detach would appear to work and then silently undo itself.
func TestRebuildDoesNotRevertADetach(t *testing.T) {
	db, _ := managedCollection(t)
	if _, err := DetachArtist(db, "art-1"); err != nil {
		t.Fatalf("DetachArtist: %v", err)
	}

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	a := artistRow(t, db, "art-1")
	if a.ManagedBy != models.ManagedByAutotaggerr || !a.ManagerDetached {
		t.Errorf("after Rebuild artist = {managed_by:%q detached:%v}, want autotaggerr/true", a.ManagedBy, a.ManagerDetached)
	}
}

// TestDetachIsIdempotent: a second detach must not re-clear a follow flag the user
// has switched back on since the first, which a blind re-run would.
func TestDetachIsIdempotent(t *testing.T) {
	db, _ := managedCollection(t)
	if _, err := DetachArtist(db, "art-1"); err != nil {
		t.Fatalf("first detach: %v", err)
	}
	if err := db.Model(&models.CollectionArtist{}).Where("mb_id = ?", "art-1").
		Update("monitored", true).Error; err != nil {
		t.Fatalf("re-follow: %v", err)
	}

	if _, err := DetachArtist(db, "art-1"); err != nil {
		t.Fatalf("second detach: %v", err)
	}
	if !artistRow(t, db, "art-1").Monitored {
		t.Error("a second detach cleared following again, want it left alone")
	}
}

// TestDetachRejectsANativeArtist: there is no authority to take back, so the verb
// does not apply and says so rather than quietly setting a flag that means nothing.
func TestDetachRejectsANativeArtist(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{
		MBID: "native", Name: "N", ManagedBy: models.ManagedByAutotaggerr,
	}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if _, err := DetachArtist(db, "native"); err != ErrNotManaged {
		t.Errorf("err = %v, want ErrNotManaged", err)
	}
}

// TestReattachRederivesProvenance: handing the artist back returns it to whatever
// manages its libraries, without waiting for the next scan to say so.
func TestReattachRederivesProvenance(t *testing.T) {
	db, _ := managedCollection(t)
	managerWant(t, db)
	if _, err := DetachArtist(db, "art-1"); err != nil {
		t.Fatalf("DetachArtist: %v", err)
	}

	a, err := ReattachArtist(db, "art-1")
	if err != nil {
		t.Fatalf("ReattachArtist: %v", err)
	}
	if a.ManagerDetached {
		t.Error("manager_detached = true after reattaching")
	}
	if a.ManagedBy != models.ManagedByLidarr {
		t.Errorf("managed_by = %q, want lidarr re-derived from the library", a.ManagedBy)
	}

	// Deliberately not an inverse: rows the user now owns are not handed back to a
	// pass that may re-point or prune them.
	var desire models.CollectionDesire
	if err := db.Where("release_group_mb_id = ?", "rg-1").First(&desire).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}
	if desire.Source != models.DesireSourceManual {
		t.Errorf("want source = %q, want it to stay manual", desire.Source)
	}
}

// TestDeletingAManagerDetachesItsArtists: once the manager row is gone nothing can
// reconcile its mirrored wants — SyncLidarr returns early with no managers, on
// purpose, since it cannot tell "unmonitored" from "gone". So deletion is the last
// moment the decisions can be kept, and this is where they are.
func TestDeletingAManagerDetachesItsArtists(t *testing.T) {
	db, manager := managedCollection(t)
	managerWant(t, db)

	detached, err := DetachManagerArtists(db, manager.ID)
	if err != nil {
		t.Fatalf("DetachManagerArtists: %v", err)
	}
	if detached != 1 {
		t.Fatalf("detached = %d, want 1", detached)
	}

	a := artistRow(t, db, "art-1")
	if !a.ManagerDetached || a.ManagedBy != models.ManagedByAutotaggerr {
		t.Errorf("artist = {managed_by:%q detached:%v}, want autotaggerr/true", a.ManagedBy, a.ManagerDetached)
	}

	var desire models.CollectionDesire
	if err := db.Where("release_group_mb_id = ?", "rg-1").First(&desire).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}
	if desire.Source != models.DesireSourceManual {
		t.Errorf("want source = %q, want manual — a provenance naming a dead authority is the bug", desire.Source)
	}
}

// TestDeletingAManagerLeavesOtherManagersArtistsAlone: detaching is scoped to the
// libraries of the manager going away, not to every managed artist.
func TestDeletingAManagerLeavesOtherManagersArtistsAlone(t *testing.T) {
	db, _ := managedCollection(t)

	other := models.Manager{Name: "Other", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}

	detached, err := DetachManagerArtists(db, other.ID)
	if err != nil {
		t.Fatalf("DetachManagerArtists: %v", err)
	}
	if detached != 0 {
		t.Errorf("detached = %d, want 0 — this manager owns no libraries", detached)
	}
	if artistRow(t, db, "art-1").ManagerDetached {
		t.Error("an unrelated manager's deletion detached the artist")
	}
}
