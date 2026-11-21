package collection

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/quality"
	"github.com/google/uuid"
)

// TestJudgeAlbum: the profile resolves override → library → none, never in a Lidarr
// library; within a library the best edition decides; across libraries the one still
// asking for an upgrade wins; unmeasured files decide nothing.
func TestJudgeAlbum(t *testing.T) {
	db := testDB(t)
	tiers := quality.Tiers{
		{Qualities: []quality.ID{quality.MP3320}, Allowed: true},
		{Qualities: []quality.ID{quality.FLAC}, Allowed: true},
	}
	lossless := models.QualityProfile{Name: "Lossless", Tiers: tiers, CutoffTier: 1, UpgradeAllowed: true}
	lossy := models.QualityProfile{Name: "Lossy", Tiers: tiers, CutoffTier: 0}
	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	for _, row := range []any{&lossless, &lossy, &lidarr} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	a := models.Library{Name: "A", Path: "/a", Enabled: true, QualityProfileID: &lossless.ID}
	b := models.Library{Name: "B", Path: "/b", Enabled: true, QualityProfileID: &lossy.ID}
	none := models.Library{Name: "C", Path: "/c", Enabled: true}
	managed := models.Library{Name: "D", Path: "/d", Enabled: true, ManagerID: &lidarr.ID, QualityProfileID: &lossless.ID}
	for _, row := range []*models.Library{&a, &b, &none, &managed} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	hold := func(rg, release string, library uuid.UUID, qualities ...quality.ID) {
		t.Helper()
		db.Create(&models.CollectionReleaseCopy{ReleaseMBID: release, LibraryID: library, ReleaseGroupMBID: rg, Files: len(qualities)})
		for _, q := range qualities {
			db.Create(&models.LibraryItem{LibraryID: library, Path: "/f/" + uuid.NewString(), MBReleaseID: release, Quality: string(q)})
		}
	}
	hold("rg-1", "rel-mp3", a.ID, quality.MP3320, quality.FLAC)
	hold("rg-1", "rel-mp3", b.ID, quality.MP3320)
	hold("rg-1", "rel-mp3", none.ID, quality.MP3320)
	hold("rg-1", "rel-mp3", managed.ID, quality.MP3320)
	hold("rg-2", "rel-unmeasured", a.ID, "", quality.Unknown)

	idx, err := LoadLibraryIndex(db)
	if err != nil {
		t.Fatal(err)
	}
	q, err := LoadQualityIndex(db, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	artist := models.CollectionArtist{MBID: "art", ManagedBy: models.ManagedByAutotaggerr}

	if v := q.JudgeAlbum(artist, "rg-1", a.ID); !v.Known || v.CutoffMet || v.Lowest != quality.MP3320 || !v.NeedsUpgrade() || v.Profile != "Lossless" {
		t.Errorf("A = %+v", v)
	}
	if v := q.JudgeAlbum(artist, "rg-1", b.ID); !v.Known || !v.CutoffMet || v.NeedsUpgrade() {
		t.Errorf("B = %+v", v)
	}
	if v := q.JudgeAlbum(artist, "rg-1", none.ID); v.Known {
		t.Errorf("a library with no profile judges nothing: %+v", v)
	}
	if v := q.JudgeAlbum(artist, "rg-1", managed.ID); v.Known {
		t.Errorf("Lidarr's library is Lidarr's to judge: %+v", v)
	}
	if v := q.JudgeAlbum(artist, "rg-1", uuid.Nil); !v.NeedsUpgrade() || v.LibraryID != a.ID {
		t.Errorf("overview should report A, which still asks: %+v", v)
	}
	if v := q.JudgeAlbum(artist, "rg-2", a.ID); v.Known {
		t.Errorf("nothing measured, nothing judged: %+v", v)
	}

	// The override wins, everywhere a profile may apply.
	artist.QualityProfileID = &lossy.ID
	if v := q.JudgeAlbum(artist, "rg-1", uuid.Nil); v.NeedsUpgrade() || v.Profile != "Lossy" {
		t.Errorf("override = %+v", v)
	}
	// A dangling override falls back to the library's own.
	gone := uuid.New()
	artist.QualityProfileID = &gone
	if p, ok := q.QualityProfileFor(artist, a.ID); !ok || p.ID != lossless.ID {
		t.Errorf("dangling override = %+v %v", p, ok)
	}

	// Named albums only, and none named reads nothing.
	if q, _ := LoadQualityIndex(db, idx, []string{"rg-2"}); q.JudgeAlbum(artist, "rg-1", a.ID).Known {
		t.Error("an album not asked about was read")
	}
	if q, _ := LoadQualityIndex(db, idx, []string{}); len(q.copies) != 0 {
		t.Error("an empty list read copies")
	}
}
