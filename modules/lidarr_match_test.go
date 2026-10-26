package modules

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// lidarrPinkFloyd is a Lidarr holding one Pink Floyd track file at trackFilePath.
func lidarrPinkFloyd(t *testing.T, trackFilePath string) *LidarrClient {
	t.Helper()
	resetLidarrCaches()
	mock := newLidarrMock(t, map[string]any{
		"/api/v1/artist": []models.LidarrArtist{
			{ID: 2, ForeignArtistID: "mbartist-floyd", Name: "Pink Floyd", Path: "/data/music/rock/Pink Floyd"},
			{ID: 3, ForeignArtistID: "mbartist-other", Name: "Someone Else", Path: "/data/music/rock/Someone Else"},
		},
		"/api/v1/trackfile": []models.LidarrTrackFile{{ID: 100, AlbumID: 42, ArtistID: 2, Path: trackFilePath}},
		"/api/v1/track": []models.LidarrTrack{
			{ID: 500, Title: "In the Flesh?", ForeignTrackID: "mbtrack-1", ForeignRecordingID: "mbrec-1", TrackFileID: i64ptr(100)},
		},
		"/api/v1/album": []models.LidarrAlbum{
			{ID: 42, ArtistID: 2, Releases: []models.LidarrAlbumRel{{ID: 2, Monitored: true, ForeignReleaseID: "mbrelease-1"}}},
		},
	})
	return NewLidarrClient(mock.server.URL, "test-key", nil)
}

// taggedMP3At writes a short MP3 at path carrying these tags.
func taggedMP3At(t *testing.T, path string, meta models.FileTags) {
	t.Helper()
	src := synthAudio(t, ".mp3")
	if _, _, _, err := SetMP3Tags(src, meta, models.TaggerSettings{}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLidarrMatchesALibraryRootAboveTheArtistFolders: the library is configured one
// level above Lidarr's artists (a folder per genre, each a Lidarr root folder), so the
// "artist folder" read from the path is "rock" — no Lidarr artist. The file's tags name
// its artist, and the track file is found by the file's path below the root.
func TestLidarrMatchesALibraryRootAboveTheArtistFolders(t *testing.T) {
	root := t.TempDir()
	for name, meta := range map[string]models.FileTags{
		"by id":   {AlbumArtist: "", MBAlbumArtistIDs: []string{"mbartist-floyd"}},
		"by name": {AlbumArtist: "Pink Floyd"},
	} {
		t.Run(name, func(t *testing.T) {
			track := filepath.Join(root, name, "rock", "Pink Floyd", "The Wall (1979)", "01 In the Flesh.mp3")
			taggedMP3At(t, track, meta)
			client := lidarrPinkFloyd(t, "/data/music/rock/Pink Floyd/The Wall (1979)/01 In the Flesh.mp3")

			details, err := ResolveMetadataDetailsFromLidarr(client, track, filepath.Join(root, name))
			if err != nil || details == nil || details.MBReleaseID != "mbrelease-1" || details.MBTrackID != "mbtrack-1" {
				t.Fatalf("resolved (%+v, %v), want the Pink Floyd track", details, err)
			}
		})
	}
}

// TestLidarrMatchesAnyDepthBelowTheArtist: the track file is found by its path, so a
// layout with more folders than <album>/[<disc>]/ — a disc and a side — still matches.
func TestLidarrMatchesAnyDepthBelowTheArtist(t *testing.T) {
	root := filepath.Join("/", "music")
	track := filepath.Join(root, "Pink Floyd", "The Wall (1979)", "Disc 1", "Side A", "01 In the Flesh.flac")
	client := lidarrPinkFloyd(t, "/data/music/Pink Floyd/The Wall (1979)/Disc 1/Side A/01 In the Flesh.flac")
	// The artist folder still finds the artist: Lidarr's folder is "Pink Floyd" too.
	details, err := ResolveMetadataDetailsFromLidarr(client, track, root)
	if err != nil || details == nil || details.MBTrackID != "mbtrack-1" {
		t.Fatalf("resolved (%+v, %v), want the track at four folders deep", details, err)
	}
}

// TestLidarrPathEndsWith compares whole segments from the end, without case, with
// either separator.
func TestLidarrPathEndsWith(t *testing.T) {
	rel := []string{"Pink Floyd", "The Wall (1979)", "01.flac"}
	for path, want := range map[string]bool{
		"/data/music/Pink Floyd/The Wall (1979)/01.flac":      true,
		`D:\Music\pink floyd\THE WALL (1979)\01.FLAC`:         true,
		"/data/music/Not Pink Floyd/The Wall (1979)/01.flac":  false,
		"/data/music/Pink Floyd/The Wall (1979)/Disc/01.flac": false,
		"The Wall (1979)/01.flac":                             false,
	} {
		if got := lidarrPathEndsWith(path, rel); got != want {
			t.Errorf("lidarrPathEndsWith(%q) = %v, want %v", path, got, want)
		}
	}
	if lidarrRelativeSegments("/music", "/elsewhere/a.flac") != nil || lidarrRelativeSegments("", "/a.flac") != nil {
		t.Error("a file outside the root has a relative path")
	}
}
