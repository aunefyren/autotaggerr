package collection

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// TestSyncExplainsWhyItDidNothing covers the four ways a mirror pass can return
// before it makes an HTTP call. All four used to report "0 artists synced · 0 albums"
// and nothing else, so a Lidarr-first user pressing the button on a cold install got
// the same row they would get from a broken Lidarr — and the fix for each of these is
// different.
func TestSyncExplainsWhyItDidNothing(t *testing.T) {
	lidarrManager := func(url, key string) models.Manager {
		return models.Manager{
			Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true,
			LidarrBaseURL: url, LidarrAPIKey: key,
		}
	}

	cases := []struct {
		name  string
		seed  func(t *testing.T, db *gorm.DB)
		want  string
		scope SyncOptions
	}{
		{
			name: "no Lidarr manager at all",
			seed: func(t *testing.T, db *gorm.DB) {},
			want: SyncEmptyNoManager,
		},
		{
			name: "a manager, but the collection is empty",
			seed: func(t *testing.T, db *gorm.DB) {
				m := lidarrManager("http://lidarr.local", "key")
				if err := db.Create(&m).Error; err != nil {
					t.Fatalf("manager: %v", err)
				}
			},
			want: SyncEmptyNoArtists,
		},
		{
			name: "artists, but none of them is Lidarr's",
			seed: func(t *testing.T, db *gorm.DB) {
				m := lidarrManager("http://lidarr.local", "key")
				if err := db.Create(&m).Error; err != nil {
					t.Fatalf("manager: %v", err)
				}
				a := models.CollectionArtist{MBID: "art-native", Name: "Band", ManagedBy: models.ManagedByAutotaggerr}
				if err := db.Create(&a).Error; err != nil {
					t.Fatalf("artist: %v", err)
				}
			},
			want: SyncEmptyNoneManaged,
		},
		{
			name: "a Lidarr artist, but the manager cannot be called",
			seed: func(t *testing.T, db *gorm.DB) {
				m := lidarrManager("", "")
				if err := db.Create(&m).Error; err != nil {
					t.Fatalf("manager: %v", err)
				}
				a := models.CollectionArtist{MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByLidarr}
				if err := db.Create(&a).Error; err != nil {
					t.Fatalf("artist: %v", err)
				}
			},
			want: SyncEmptyNoManagerCredentials,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			tc.seed(t, db)

			stats, err := SyncLidarrWith(db, tc.scope)
			if err != nil {
				t.Fatalf("SyncLidarrWith: %v", err)
			}
			if stats.ArtistsSynced != 0 || stats.Groups != 0 {
				t.Fatalf("stats = %+v, want a pass that did nothing", stats)
			}
			if stats.EmptyReason != tc.want {
				t.Errorf("empty reason = %q, want %q", stats.EmptyReason, tc.want)
			}
		})
	}
}

// TestScanExplainsWhyItDidNothing is the same guarantee for the Scan verb, which
// re-derives the collection from `library_items` and so answers "0 artists, 0 albums"
// on an install where Process has never run.
//
// The two non-empty states are worth separating because the fix differs: no indexed
// files means "start a run", while indexed-but-unmatched means the files were seen and
// something went wrong resolving them.
func TestScanExplainsWhyItDidNothing(t *testing.T) {
	t.Run("nothing indexed", func(t *testing.T) {
		db := testDB(t)
		stats, err := Rebuild(db)
		if err != nil {
			t.Fatalf("Rebuild: %v", err)
		}
		if stats.EmptyReason != ScanEmptyNoFiles {
			t.Errorf("empty reason = %q, want %q", stats.EmptyReason, ScanEmptyNoFiles)
		}
	})

	t.Run("indexed but nothing matched", func(t *testing.T) {
		db := testDB(t)
		lib := models.Library{Name: "L", Path: "/music", Enabled: true}
		if err := db.Create(&lib).Error; err != nil {
			t.Fatalf("library: %v", err)
		}
		// Unmatched files are exactly what ownedItemRows excludes, so the pass sees an
		// empty index even though the walk found these.
		for _, path := range []string{"/music/a.flac", "/music/b.flac"} {
			item := models.LibraryItem{
				LibraryID: lib.ID, Path: path,
				Status: models.LibraryItemStatusUnmatched,
			}
			if err := db.Create(&item).Error; err != nil {
				t.Fatalf("item: %v", err)
			}
		}

		stats, err := Rebuild(db)
		if err != nil {
			t.Fatalf("Rebuild: %v", err)
		}
		if stats.EmptyReason != ScanEmptyNothingMatched {
			t.Errorf("empty reason = %q, want %q", stats.EmptyReason, ScanEmptyNothingMatched)
		}
	})
}

// TestScanReasonIsOnlyForAnEmptyPass is the guard on the whole idea: a pass that
// re-derived something must say nothing about missing inputs. A reason attached to a
// working scan would be worse than no reason at all — it would be wrong.
func TestScanReasonIsOnlyForAnEmptyPass(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedRelease(t, db, "rel-1", "rg-1", "art-1", "Album", 2)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	lib := models.Library{Name: "L", Path: "/music", Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	ownFile(t, db, "/music/a.flac", "rel-1", lib)

	stats, err := Rebuild(db)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.Artists == 0 || stats.Owned == 0 {
		t.Fatalf("stats = %+v, want the seeded album to be re-derived", stats)
	}
	if stats.EmptyReason != "" {
		t.Errorf("a pass that found %d artists reported %q as its reason for finding nothing",
			stats.Artists, stats.EmptyReason)
	}
}
