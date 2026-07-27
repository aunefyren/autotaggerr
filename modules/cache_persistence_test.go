package modules

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// redirectCachePaths points every cache file at a fresh temp dir and restores the
// originals on cleanup, so persistence tests never touch the real ./config files.
func redirectCachePaths(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	saved := map[*string]string{}
	for _, pp := range []*string{
		&musicbrainzReleaseCachePath,
		&lidarrArtistsCachePath, &lidarrAlbumsCachePath,
		&lidarrTracksCachePath, &lidarrTrackFilesCachePath,
		&plexAlbumKeyCachePath,
	} {
		saved[pp] = *pp
		*pp = filepath.Join(dir, filepath.Base(*pp))
	}
	t.Cleanup(func() {
		for pp, orig := range saved {
			*pp = orig
		}
	})
}

func TestCachePersistenceRoundTrip(t *testing.T) {
	redirectCachePaths(t)

	// Seed each in-memory cache with one entry.
	lidarrArtistsCacheMu.Lock()
	lidarrArtistsCache = map[string]models.CachedLidarrArtistRelease{
		"1": {Artist: models.LidarrArtist{ID: 1, Name: "Radiohead"}, Timestamp: time.Now()},
	}
	lidarrArtistsCacheMu.Unlock()

	lidarrAlbumsCacheMu.Lock()
	lidarrAlbumsCache = map[string]models.CachedLidarrAlbumRelease{
		"42": {Album: models.LidarrAlbum{ID: 42, Title: "OK Computer"}, Timestamp: time.Now()},
	}
	lidarrAlbumsCacheMu.Unlock()

	lidarrTracksCacheMu.Lock()
	lidarrTracksCache = map[string]models.CachedLidarrTracksRelease{
		"42": {Tracks: []models.LidarrTrack{{ID: 500, Title: "Airbag"}}, Timestamp: time.Now()},
	}
	lidarrTracksCacheMu.Unlock()

	lidarrTrackFilesCacheMu.Lock()
	lidarrTrackFilesCache = map[string]models.CachedLidarrTrackFilesRelease{
		"2": {TrackFiles: []models.LidarrTrackFile{{ID: 100, Path: "/x.flac"}}, Timestamp: time.Now()},
	}
	lidarrTrackFilesCacheMu.Unlock()

	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache = map[string]models.CachedMusicBrainzRelease{
		"rel-1": {Release: models.MusicBrainzReleaseResponse{ID: "rel-1", Title: "Kid A"}, Timestamp: time.Now()},
	}
	musicbrainzReleaseCacheMu.Unlock()

	plexAlbumKeyCacheMu.Lock()
	plexAlbumKeyCache = map[string]models.PlexAlbumKeyCache{
		"OK Computer": {AlbumKey: "/library/metadata/1", Timestamp: time.Now()},
	}
	plexAlbumKeyCacheMu.Unlock()

	// Save everything to disk.
	for _, save := range []func() error{
		LidarrSaveArtistsCache, LidarrSaveAlbumsCache, LidarrSaveTracksCache,
		LidarrSaveTrackFilesCache, MusicbrainzSaveCache, PlexSaveAlbumKeyCache,
	} {
		if err := save(); err != nil {
			t.Fatalf("save failed: %v", err)
		}
	}

	// Wipe in-memory state, then reload from disk via the central loader.
	lidarrArtistsCacheMu.Lock()
	lidarrArtistsCache = map[string]models.CachedLidarrArtistRelease{}
	lidarrArtistsCacheMu.Unlock()
	lidarrAlbumsCacheMu.Lock()
	lidarrAlbumsCache = map[string]models.CachedLidarrAlbumRelease{}
	lidarrAlbumsCacheMu.Unlock()
	lidarrTracksCacheMu.Lock()
	lidarrTracksCache = map[string]models.CachedLidarrTracksRelease{}
	lidarrTracksCacheMu.Unlock()
	lidarrTrackFilesCacheMu.Lock()
	lidarrTrackFilesCache = map[string]models.CachedLidarrTrackFilesRelease{}
	lidarrTrackFilesCacheMu.Unlock()
	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache = map[string]models.CachedMusicBrainzRelease{}
	musicbrainzReleaseCacheMu.Unlock()
	plexAlbumKeyCacheMu.Lock()
	plexAlbumKeyCache = map[string]models.PlexAlbumKeyCache{}
	plexAlbumKeyCacheMu.Unlock()

	LoadAllCaches()

	// Verify each round-tripped.
	if lidarrArtistsCache["1"].Artist.Name != "Radiohead" {
		t.Error("lidarr artists cache did not round-trip")
	}
	if lidarrAlbumsCache["42"].Album.Title != "OK Computer" {
		t.Error("lidarr albums cache did not round-trip")
	}
	if len(lidarrTracksCache["42"].Tracks) != 1 || lidarrTracksCache["42"].Tracks[0].Title != "Airbag" {
		t.Error("lidarr tracks cache did not round-trip")
	}
	if len(lidarrTrackFilesCache["2"].TrackFiles) != 1 || lidarrTrackFilesCache["2"].TrackFiles[0].ID != 100 {
		t.Error("lidarr trackfiles cache did not round-trip")
	}
	if musicbrainzReleaseCache["rel-1"].Release.Title != "Kid A" {
		t.Error("musicbrainz cache did not round-trip")
	}
	if plexAlbumKeyCache["OK Computer"].AlbumKey != "/library/metadata/1" {
		t.Error("plex album key cache did not round-trip")
	}
}

// LoadAllCaches must be a safe no-op when no cache files exist yet.
func TestLoadAllCachesNoFiles(t *testing.T) {
	redirectCachePaths(t)
	LoadAllCaches() // must not panic or error out on a fresh install
}
