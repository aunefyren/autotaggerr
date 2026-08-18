package collection

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// audioTrack and videoTrack build the two kinds of track the totals rule turns on.
func audioTrack(id string, position int) models.Track {
	return models.Track{ID: id, Position: position, Title: id}
}

func videoTrack(id string, position int) models.Track {
	t := models.Track{ID: id, Position: position, Title: id}
	t.Recording.Video = true
	return t
}

// endlessRelease is Frank Ocean's *Endless*, 2018 CD+DVD
// (c14006ec-8b09-4fcd-addd-e5a2960013d0), in miniature: an audio medium and a video
// one. The real edition is 19 + 22; the shape is what matters, not the size.
func endlessRelease(audio, video int) models.MusicBrainzReleaseResponse {
	cd := models.MusicBrainzMedia{Position: 1, Format: "CD"}
	for i := 1; i <= audio; i++ {
		cd.Tracks = append(cd.Tracks, audioTrack("a"+string(rune('0'+i)), i))
	}
	dvd := models.MusicBrainzMedia{Position: 2, Format: "DVD"}
	for i := 1; i <= video; i++ {
		dvd.Tracks = append(dvd.Tracks, videoTrack("v"+string(rune('0'+i)), i))
	}
	return models.MusicBrainzReleaseResponse{
		ID:           "rel-endless",
		Title:        "Endless",
		ArtistCredit: []models.ArtistCredit{{Name: "Frank Ocean", Artist: models.Artist{ID: "art-fo", Name: "Frank Ocean"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-endless", Title: "Endless", PrimaryType: "Album"},
		Media:        []models.MusicBrainzMedia{cd, dvd},
	}
}

// ownFiles indexes one file per track ID, each on disk, and returns the library.
func ownFiles(t *testing.T, db *gorm.DB, releaseID string, trackIDs ...string) models.Library {
	t.Helper()
	root := t.TempDir()
	lib := models.Library{Name: "L", Path: root}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	for _, id := range trackIDs {
		path := filepath.Join(root, id+".flac")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if err := db.Create(&models.LibraryItem{
			LibraryID: lib.ID, Path: path, Status: "ok",
			MBReleaseID: releaseID, MBReleaseTrackID: id,
		}).Error; err != nil {
			t.Fatalf("item %s: %v", id, err)
		}
	}
	return lib
}

// TestVideoTracksAreNotTracksYouAreMissing is the reported bug: a complete album on a
// CD+DVD edition read 19/41 because every video on the DVD counted as an audio track
// absent from disk. Lidarr, which ignores video media, said 19/19 and was right.
func TestVideoTracksAreNotTracksYouAreMissing(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedCachedRelease(t, db, endlessRelease(3, 4))
	ownFiles(t, db, "rel-endless", "a1", "a2", "a3")

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-endless").First(&rg).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if rg.OwnedTracks != 3 || rg.TotalTracks != 3 {
		t.Errorf("release-group = %d/%d, want 3/3 — the 4 videos are not missing audio", rg.OwnedTracks, rg.TotalTracks)
	}
	if !rg.Complete() {
		t.Error("an album whose every audio track is on disk must read as complete")
	}

	var rel models.CollectionRelease
	if err := db.Where("mb_id = ?", "rel-endless").First(&rel).Error; err != nil {
		t.Fatalf("edition: %v", err)
	}
	if rel.OwnedTracks != 3 || rel.TotalTracks != 3 {
		t.Errorf("edition = %d/%d, want 3/3", rel.OwnedTracks, rel.TotalTracks)
	}
}

// TestOwnedVideoTracksStillCount is the other half of the rule, and the reason the
// total is not simply "audio tracks". Someone who ripped the bonus DVD's audio has
// files that genuinely resolve to those tracks; excluding them regardless would
// report owning more of the album than it contains.
func TestOwnedVideoTracksStillCount(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedCachedRelease(t, db, endlessRelease(3, 4))
	// Every audio track plus two of the four videos.
	ownFiles(t, db, "rel-endless", "a1", "a2", "a3", "v1", "v2")

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-endless").First(&rg).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if rg.OwnedTracks != 5 || rg.TotalTracks != 5 {
		t.Errorf("release-group = %d/%d, want 5/5 — owned videos count, unowned ones do not",
			rg.OwnedTracks, rg.TotalTracks)
	}
	if rg.OwnedTracks > rg.TotalTracks {
		t.Error("the owned count must never exceed the total")
	}
}

// TestReleaseTrackTotalRule pins the predicate itself, away from the database, so the
// three cases read as one table rather than three fixtures.
func TestReleaseTrackTotalRule(t *testing.T) {
	release := endlessRelease(3, 4)
	cases := []struct {
		name  string
		owned map[string]bool
		want  int
	}{
		{"no files yet", nil, 3},
		{"audio only", map[string]bool{"a1": true}, 3},
		{"one video owned", map[string]bool{"a1": true, "v3": true}, 4},
		{"every video owned", map[string]bool{"v1": true, "v2": true, "v3": true, "v4": true}, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := releaseTrackTotal(release, c.owned); got != c.want {
				t.Errorf("releaseTrackTotal = %d, want %d", got, c.want)
			}
		})
	}
}

// TestAllAudioReleaseIsUnchanged guards the ordinary album: nothing about the common
// case may move because of a rule written for bonus DVDs.
func TestAllAudioReleaseIsUnchanged(t *testing.T) {
	release := endlessRelease(5, 0)
	if got := releaseTrackTotal(release, nil); got != 5 {
		t.Errorf("releaseTrackTotal = %d, want 5", got)
	}
}
