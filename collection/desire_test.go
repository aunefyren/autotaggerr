package collection

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// TestAddArtistIsManualAndIdempotent: adding an artist you own nothing of is the
// whole point (Rebuild can only ever materialise artists from files), and re-adding
// must not reset anything.
func TestAddArtistIsManualAndIdempotent(t *testing.T) {
	db := testDB(t)

	artist, err := AddArtist(db, "art-1", "Kendrick Lamar")
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	if artist.Origin != models.CollectionOriginManual {
		t.Errorf("origin = %q, want manual", artist.Origin)
	}
	if artist.ManagedBy != models.ManagedByAutotaggerr {
		t.Errorf("managed_by = %q, want autotaggerr", artist.ManagedBy)
	}

	// Monitoring it, then re-adding, must not wipe the monitor flag.
	if err := db.Model(&models.CollectionArtist{}).Where("mb_id = ?", "art-1").
		Update("monitored", true).Error; err != nil {
		t.Fatalf("monitor: %v", err)
	}
	again, err := AddArtist(db, "art-1", "Kendrick Lamar")
	if err != nil {
		t.Fatalf("AddArtist (again): %v", err)
	}
	if again.ID != artist.ID || !again.Monitored {
		t.Errorf("re-add did not return the existing artist intact: %+v", again)
	}

	var count int64
	db.Model(&models.CollectionArtist{}).Count(&count)
	if count != 1 {
		t.Errorf("artist count = %d, want 1", count)
	}
}

func TestAddArtistRequiresMBID(t *testing.T) {
	db := testDB(t)
	if _, err := AddArtist(db, "  ", "Nameless"); err == nil {
		t.Fatal("AddArtist accepted a blank MusicBrainz ID")
	}
}

// TestSetDesireBypassesTypeFilter is the rule that matters: wantedType exists to
// stop automatic discovery burying the missing list under singles and live albums.
// An explicit request is not a guess, so the filter must not silently discard it.
func TestSetDesireBypassesTypeFilter(t *testing.T) {
	db := testDB(t)

	// A live album — the default follow filter would reject this outright.
	if FollowWants(models.CollectionArtist{}, "Album", []string{"Live"}) {
		t.Fatal("precondition: the default follow filter should reject a live album")
	}

	if _, err := SetDesire(db, DesireInput{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-live",
		Title: "Live at Wembley", PrimaryType: "Album", SecondaryTypes: "Live",
	}); err != nil {
		t.Fatalf("SetDesire: %v", err)
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-live").First(&rg).Error; err != nil {
		t.Fatalf("desired release-group was not made visible: %v", err)
	}
	if !rg.InCatalog {
		t.Errorf("desired release-group is not in the catalog: %+v", rg)
	}
	if rg.Owned {
		t.Errorf("desiring something must not mark it owned: %+v", rg)
	}
}

// TestSetDesireIdempotent: asking twice for the same thing is one want.
func TestSetDesireIdempotent(t *testing.T) {
	db := testDB(t)
	in := DesireInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", Title: "Album"}

	first, err := SetDesire(db, in)
	if err != nil {
		t.Fatalf("SetDesire: %v", err)
	}
	second, err := SetDesire(db, in)
	if err != nil {
		t.Fatalf("SetDesire (again): %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("duplicate desire rows created: %s vs %s", first.ID, second.ID)
	}

	var count int64
	db.Model(&models.CollectionDesire{}).Count(&count)
	if count != 1 {
		t.Errorf("desire count = %d, want 1", count)
	}
}

// TestDesireAnyAndSpecificAreExclusive: within one release-group a want is *either*
// "any release will do" *or* a set of specific editions. Holding both is a
// contradiction nobody can have meant, and the earlier model stored it happily —
// which is why the UI could not render a coherent state.
func TestDesireAnyAndSpecificAreExclusive(t *testing.T) {
	db := testDB(t)
	any := DesireInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", Title: "Album"}
	specific := DesireInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-2017", Title: "Album"}

	// Narrowing from "any" to a specific edition replaces the any-release want.
	if _, err := SetDesire(db, any); err != nil {
		t.Fatalf("any-release desire: %v", err)
	}
	if _, err := SetDesire(db, specific); err != nil {
		t.Fatalf("specific-release desire: %v", err)
	}
	desires, err := DesiresForArtist(db, "art-1")
	if err != nil {
		t.Fatalf("DesiresForArtist: %v", err)
	}
	if len(desires) != 1 || desires[0].ReleaseMBID != "rel-2017" {
		t.Fatalf("narrowing left %d desires (%+v); want only the specific edition", len(desires), desires)
	}

	// Widening back to "any" drops the specific editions.
	if _, err := SetDesire(db, any); err != nil {
		t.Fatalf("widening: %v", err)
	}
	desires, _ = DesiresForArtist(db, "art-1")
	if len(desires) != 1 || desires[0].ReleaseMBID != "" {
		t.Fatalf("widening left %d desires (%+v); want only the any-release want", len(desires), desires)
	}
}

// TestDesireSeveralEditionsCoexist: two specific editions of one release-group are
// a legitimate pair (the shape case 5 needs), unlike any-plus-specific.
func TestDesireSeveralEditionsCoexist(t *testing.T) {
	db := testDB(t)
	for _, rel := range []string{"rel-1977", "rel-2017"} {
		if _, err := SetDesire(db, DesireInput{
			ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: rel, Title: "Album",
		}); err != nil {
			t.Fatalf("desire %s: %v", rel, err)
		}
	}
	desires, _ := DesiresForArtist(db, "art-1")
	if len(desires) != 2 {
		t.Fatalf("got %d desires, want 2 specific editions", len(desires))
	}
}

// TestDesireRecordingsAreStoredAndUpdatable: song-level wants (cases 2/4/5) are
// recording MBIDs, and re-asking for the same scope replaces the selection rather
// than duplicating the want.
func TestDesireRecordingsAreStoredAndUpdatable(t *testing.T) {
	db := testDB(t)
	in := DesireInput{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", Title: "Album",
		RecordingMBIDs: []string{"rec-a", "rec-b"},
	}
	if _, err := SetDesire(db, in); err != nil {
		t.Fatalf("SetDesire: %v", err)
	}

	in.RecordingMBIDs = []string{"rec-c"}
	if _, err := SetDesire(db, in); err != nil {
		t.Fatalf("SetDesire (update): %v", err)
	}

	desires, _ := DesiresForArtist(db, "art-1")
	if len(desires) != 1 {
		t.Fatalf("got %d desires, want 1", len(desires))
	}
	if len(desires[0].RecordingMBIDs) != 1 || desires[0].RecordingMBIDs[0] != "rec-c" {
		t.Errorf("recordings = %v, want [rec-c]", desires[0].RecordingMBIDs)
	}
}

func TestSetDesireRequiresIDs(t *testing.T) {
	db := testDB(t)
	if _, err := SetDesire(db, DesireInput{ArtistMBID: "art-1"}); err == nil {
		t.Error("SetDesire accepted a missing release-group")
	}
	if _, err := SetDesire(db, DesireInput{ReleaseGroupMBID: "rg-1"}); err == nil {
		t.Error("SetDesire accepted a missing artist")
	}
}

// TestClearDesireKeepsOwnership: dropping a want means "stop tracking what I lack",
// never "forget what I have".
func TestClearDesireKeepsOwnership(t *testing.T) {
	db := testDB(t)

	if _, err := SetDesire(db, DesireInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", Title: "Album"}); err != nil {
		t.Fatalf("SetDesire: %v", err)
	}
	// Pretend a scan found files for it.
	if err := db.Model(&models.CollectionReleaseGroup{}).Where("mb_id = ?", "rg-1").
		Updates(map[string]any{"owned": true, "owned_tracks": 9, "total_tracks": 9}).Error; err != nil {
		t.Fatalf("mark owned: %v", err)
	}

	if err := ClearDesire(db, "rg-1", ""); err != nil {
		t.Fatalf("ClearDesire: %v", err)
	}

	var count int64
	db.Model(&models.CollectionDesire{}).Count(&count)
	if count != 0 {
		t.Errorf("desire not cleared: %d rows", count)
	}
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("release-group removed by ClearDesire: %v", err)
	}
	if !rg.Owned || rg.OwnedTracks != 9 {
		t.Errorf("ClearDesire discarded ownership: %+v", rg)
	}
}

// TestRebuildMarksLibraryOrigin: artists materialised from files are library-origin,
// and a manually added artist keeps its manual origin once files show up — origin
// records how the artist entered the collection, not its current state.
func TestRebuildMarksLibraryOrigin(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	release := models.MusicBrainzReleaseResponse{
		ID:           "rel-1",
		ArtistCredit: []models.ArtistCredit{{Artist: models.Artist{ID: "art-1", Name: "The Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Album", PrimaryType: "Album"},
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

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: "ok", MBReleaseID: "rel-1",
	}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}

	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	var a models.CollectionArtist
	if err := db.Where("mb_id = ?", "art-1").First(&a).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if a.Origin != models.CollectionOriginLibrary {
		t.Errorf("origin = %q, want library", a.Origin)
	}

	// Now the manual case: pre-add an artist, then let Rebuild see files for it.
	if _, err := AddArtist(db, "art-2", "Added By Hand"); err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	release.ArtistCredit[0].Artist.ID = "art-2"
	release.ID = "rel-2"
	release.ReleaseGroup.ID = "rg-2"
	payload2, _ := json.Marshal(release)
	if err := db.Create(&models.MusicbrainzReleaseCache{
		MBID: "rel-2", Payload: string(payload2),
		FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed cache 2: %v", err)
	}
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache 2: %v", err)
	}
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/b.flac", Status: "ok", MBReleaseID: "rel-2",
	}).Error; err != nil {
		t.Fatalf("item 2: %v", err)
	}
	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild 2: %v", err)
	}

	var manual models.CollectionArtist
	if err := db.Where("mb_id = ?", "art-2").First(&manual).Error; err != nil {
		t.Fatalf("manual artist: %v", err)
	}
	if manual.Origin != models.CollectionOriginManual {
		t.Errorf("Rebuild overwrote a manual origin: %q", manual.Origin)
	}
}
