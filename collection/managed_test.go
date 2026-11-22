package collection

import (
	"errors"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// Each way into the managed set gives its reason, and an artist only files credit is
// present-only.
func TestManagedReasons(t *testing.T) {
	db := testDB(t)
	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	off := models.Manager{Name: "Old Lidarr", Type: models.ManagerTypeLidarr}
	for _, m := range []*models.Manager{&lidarr, &off} {
		if err := db.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Model(&off).Update("enabled", false)
	profile := uuid.New()

	artists := []models.CollectionArtist{
		{MBID: "on", Origin: models.CollectionOriginLibrary, ManageOverride: models.ManageOn},
		{MBID: "off", Origin: models.CollectionOriginManual, Monitored: true, ManageOverride: models.ManageOff},
		{MBID: "manual", Origin: models.CollectionOriginManual},
		{MBID: "followed", Origin: models.CollectionOriginLibrary, Monitored: true},
		{MBID: "wanted", Origin: models.CollectionOriginLibrary},
		{MBID: "listed", Origin: models.CollectionOriginLibrary},
		{MBID: "listed-off", Origin: models.CollectionOriginLibrary},
		{MBID: "album", Origin: models.CollectionOriginLibrary},
		{MBID: "compiler", Origin: models.CollectionOriginLibrary},
		{MBID: "catalog-only", Origin: models.CollectionOriginLibrary},
		{MBID: "featured", Origin: models.CollectionOriginLibrary},
		{MBID: "profiled", Origin: models.CollectionOriginLibrary, QualityProfileID: &profile},
		{MBID: "default-types", Origin: models.CollectionOriginLibrary, FollowTypes: models.DefaultFollowTypes},
		{MBID: models.VariousArtistsMBID, Origin: models.CollectionOriginLibrary},
	}
	for i := range artists {
		if err := db.Create(&artists[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows := []any{
		&models.CollectionDesire{ArtistMBID: "wanted", ReleaseGroupMBID: "rg-w", Source: models.DesireSourceManual},
		&models.ManagerLink{ManagerID: lidarr.ID, EntityType: models.ManagerLinkArtist, MBID: "listed"},
		&models.ManagerLink{ManagerID: off.ID, EntityType: models.ManagerLinkArtist, MBID: "listed-off"},
		// The album credited album-then-featured: only the first is its artist.
		&models.CollectionReleaseGroup{MBID: "rg-a", ArtistMBID: "album", Owned: true, PrimaryType: "Album"},
		&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-a", ArtistMBID: "featured", Position: 1, FromDisk: true},
		&models.CollectionReleaseGroup{MBID: "rg-c", ArtistMBID: "compiler", Owned: true, PrimaryType: "Album", SecondaryTypes: "Compilation"},
		&models.CollectionReleaseGroup{MBID: "rg-n", ArtistMBID: "catalog-only", PrimaryType: "Album", InCatalog: true},
		&models.CollectionReleaseGroup{MBID: "rg-v", ArtistMBID: models.VariousArtistsMBID, Owned: true, PrimaryType: "Album"},
	}
	for _, r := range rows {
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}

	got, err := ManagedArtists(db)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"on": ManagedAdded, "off": "", "manual": ManagedManual, "followed": ManagedFollowed,
		"wanted": ManagedWanted, "listed": ManagedLidarr, "listed-off": "", "album": ManagedAlbumArtist,
		"compiler": "", "catalog-only": "", "featured": "", "profiled": ManagedSettings,
		"default-types": "", models.VariousArtistsMBID: "",
	}
	for mbid, reason := range want {
		if got[mbid] != reason {
			t.Errorf("%s: reason = %q, want %q", mbid, got[mbid], reason)
		}
		var a models.CollectionArtist
		db.Where("mb_id = ?", mbid).First(&a)
		if one, err := ManagedReasonFor(db, a); err != nil || one != reason {
			t.Errorf("%s: ManagedReasonFor = %q, %v; want %q", mbid, one, err, reason)
		}
	}
}

// Stopping to manage an artist is refused while they are followed or hold a pick, and
// managing one by hand keeps them through their last file leaving.
func TestSetManaged(t *testing.T) {
	db := testDB(t)
	for _, a := range []models.CollectionArtist{
		{MBID: "followed", Origin: models.CollectionOriginLibrary, Monitored: true},
		{MBID: "picked", Origin: models.CollectionOriginLibrary},
		{MBID: "plain", Origin: models.CollectionOriginLibrary},
	} {
		if err := db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Create(&models.CollectionDesire{ArtistMBID: "picked", ReleaseGroupMBID: "rg", Source: models.DesireSourceManual})

	for _, mbid := range []string{"followed", "picked"} {
		if _, err := SetManaged(db, mbid, false); !errors.Is(err, ErrManagedByIntent) {
			t.Errorf("%s: stop managing err = %v, want ErrManagedByIntent", mbid, err)
		}
	}
	if a, err := SetManaged(db, "plain", false); err != nil || a.ManageOverride != models.ManageOff {
		t.Errorf("stop managing plain = %+v, %v", a, err)
	}
	if a, err := SetManaged(db, "plain", true); err != nil || a.ManageOverride != models.ManageOn {
		t.Errorf("manage plain = %+v, %v", a, err)
	}
	if _, err := SetManaged(db, "nobody", true); err == nil {
		t.Error("managing an unknown artist succeeded")
	}

	// Nothing credits "plain", yet the prune keeps them: they were chosen.
	if n, err := pruneOrphanArtists(db); err != nil || n != 0 {
		t.Errorf("prune = %d, %v; want the managed artist kept", n, err)
	}
	db.Model(&models.CollectionArtist{}).Where("mb_id = ?", "plain").Update("manage_override", models.ManageAuto)
	if n, _ := pruneOrphanArtists(db); n != 1 {
		t.Errorf("prune after clearing the override = %d, want 1", n)
	}
}

// The cleanup unfollows co-credits the policy followed, keeps every follow something
// else accounts for, and runs once.
func TestUnfollowPolicyCoCredits(t *testing.T) {
	db := testDB(t)
	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	db.Create(&lidarr)
	followed := func(mbid string) models.CollectionArtist {
		return models.CollectionArtist{MBID: mbid, Name: mbid, Origin: models.CollectionOriginLibrary,
			Monitored: true, FollowTypes: "Album", FollowFromYear: 2020}
	}
	artists := []models.CollectionArtist{
		followed("cocredit"), followed("album"), followed("picked"), followed("listed"), followed("chosen"),
		{MBID: "manual", Origin: models.CollectionOriginManual, Monitored: true},
	}
	artists[4].ManageOverride = models.ManageOn
	for i := range artists {
		if err := db.Create(&artists[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Create(&models.CollectionReleaseGroup{MBID: "rg-a", ArtistMBID: "album", Owned: true, PrimaryType: "Album"})
	db.Create(&models.CollectionDesire{ArtistMBID: "picked", ReleaseGroupMBID: "rg-p", Source: models.DesireSourceManual})
	db.Create(&models.ManagerLink{ManagerID: lidarr.ID, EntityType: models.ManagerLinkArtist, MBID: "listed"})

	if err := UnfollowPolicyCoCredits(db); err != nil {
		t.Fatal(err)
	}
	for _, mbid := range []string{"cocredit", "album", "picked", "listed", "chosen", "manual"} {
		var a models.CollectionArtist
		db.Where("mb_id = ?", mbid).First(&a)
		wantFollowed := mbid != "cocredit"
		if a.Monitored != wantFollowed {
			t.Errorf("%s: monitored = %v, want %v", mbid, a.Monitored, wantFollowed)
		}
		if mbid == "cocredit" && (a.FollowTypes != "" || a.FollowFromYear != 0) {
			t.Errorf("the policy's follow settings were kept: %+v", a)
		}
	}
	var items []models.EventItem
	db.Where("status = ?", models.EventItemStatusUnfollowed).Find(&items)
	if len(items) != 1 || items[0].Path != "cocredit" {
		t.Errorf("activity rows = %+v, want one for cocredit", items)
	}

	// Once only: a follow made after the cleanup is the user's.
	db.Model(&models.CollectionArtist{}).Where("mb_id = ?", "cocredit").Update("monitored", true)
	if err := UnfollowPolicyCoCredits(db); err != nil {
		t.Fatal(err)
	}
	var again models.CollectionArtist
	db.Where("mb_id = ?", "cocredit").First(&again)
	if !again.Monitored {
		t.Error("the cleanup ran a second time")
	}
}

// TestClearCoCreditDetaches: the detaches a manager deletion gave co-credits are
// taken back, once — and only for the shape it left. A detached album artist, one with
// a want, one a manager listed (its manager row gone), one with a quality profile, one
// followed and one the user chose to manage all keep the flag.
func TestClearCoCreditDetaches(t *testing.T) {
	db := testDB(t)
	profile := uuid.New()
	detached := func(mbid string) models.CollectionArtist {
		return models.CollectionArtist{MBID: mbid, Name: mbid, Origin: models.CollectionOriginLibrary,
			ManagedBy: models.ManagedByAutotaggerr, ManagerDetached: true}
	}
	artists := []models.CollectionArtist{
		detached("cocredit"), detached("album"), detached("picked"), detached("listed"),
		detached("profiled"), detached("followed"), detached("chosen"),
	}
	artists[4].QualityProfileID = &profile
	artists[5].Monitored = true
	artists[6].ManageOverride = models.ManageOn
	for i := range artists {
		if err := db.Create(&artists[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Create(&models.CollectionReleaseGroup{MBID: "rg-a", ArtistMBID: "album", Owned: true, PrimaryType: "Album"})
	db.Create(&models.CollectionDesire{ArtistMBID: "picked", ReleaseGroupMBID: "rg-p", Source: models.DesireSourceManual})
	// The manager that listed them was deleted; its link lingers.
	db.Create(&models.ManagerLink{ManagerID: uuid.New(), EntityType: models.ManagerLinkArtist, MBID: "listed"})

	if got, _ := ManagedArtists(db); got["cocredit"] != ManagedSettings {
		t.Fatalf("before: cocredit reason = %q, want settings — the bug this repairs", got["cocredit"])
	}
	if err := ClearCoCreditDetaches(db); err != nil {
		t.Fatal(err)
	}
	for _, a := range artists {
		got := artistRow(t, db, a.MBID)
		if want := a.MBID != "cocredit"; got.ManagerDetached != want {
			t.Errorf("%s: detached = %v, want %v", a.MBID, got.ManagerDetached, want)
		}
	}
	if got, _ := ManagedArtists(db); got["cocredit"] != "" {
		t.Errorf("after: cocredit reason = %q, want present-only", got["cocredit"])
	}
	var items []models.EventItem
	db.Where("status = ?", models.EventItemStatusUndetached).Find(&items)
	if len(items) != 1 || items[0].Path != "cocredit" {
		t.Errorf("activity rows = %+v, want one for cocredit", items)
	}

	// Once only: a detach made after the cleanup is the user's.
	db.Model(&models.CollectionArtist{}).Where("mb_id = ?", "cocredit").Update("manager_detached", true)
	if err := ClearCoCreditDetaches(db); err != nil {
		t.Fatal(err)
	}
	if !artistRow(t, db, "cocredit").ManagerDetached {
		t.Error("the cleanup ran a second time")
	}
}
