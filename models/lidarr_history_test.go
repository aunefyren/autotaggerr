package models

import (
	"encoding/json"
	"testing"
)

// Lidarr names its history event types in JSON; the numeric enum is accepted too,
// because which of the two Lidarr sends is a serializer setting on its side.
func TestLidarrHistoryEventTypeDecodesNamesAndNumbers(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want LidarrHistoryEventType
	}{
		{`"trackFileImported"`, LidarrHistoryTrackFileImported},
		{`3`, LidarrHistoryTrackFileImported},
		{`5`, LidarrHistoryTrackFileDeleted},
		{`99`, "unknown"},
	} {
		var got LidarrHistoryEventType
		if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("%s decoded to %q, want %q", tc.in, got, tc.want)
		}
	}

	var bad LidarrHistoryEventType
	if err := json.Unmarshal([]byte(`{}`), &bad); err == nil {
		t.Error("an object decoded as an event type")
	}
}

func TestLidarrHistoryEventTypeMovesFiles(t *testing.T) {
	moves := []LidarrHistoryEventType{
		LidarrHistoryTrackFileImported, LidarrHistoryTrackFileDeleted, LidarrHistoryTrackFileRenamed,
		LidarrHistoryDownloadImported, LidarrHistoryArtistFolderImported,
	}
	for _, e := range moves {
		if !e.MovesFiles() {
			t.Errorf("%s does not move files, want it to", e)
		}
	}
	for _, e := range []LidarrHistoryEventType{LidarrHistoryGrabbed, LidarrHistoryTrackFileRetagged, LidarrHistoryDownloadFailed, "unknown"} {
		if e.MovesFiles() {
			t.Errorf("%s moves files, want it not to", e)
		}
	}
}

// Only the two modes that rewrite already-imported files fight Autotaggerr. "For new
// downloads only" is the recommended setting, and "no" is compatible too.
func TestLidarrWriteAudioTags(t *testing.T) {
	for _, tc := range []struct {
		in       string
		want     LidarrWriteAudioTags
		rewrites bool
	}{
		{`"no"`, LidarrWriteAudioTagsNo, false},
		{`"newFiles"`, LidarrWriteAudioTagsNewFiles, false},
		{`"allFiles"`, LidarrWriteAudioTagsAllFiles, true},
		{`"sync"`, LidarrWriteAudioTagsSync, true},
		{`2`, LidarrWriteAudioTagsAllFiles, true},
		{`1`, LidarrWriteAudioTagsNewFiles, false},
	} {
		var got LidarrWriteAudioTags
		if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if got != tc.want || got.RewritesExistingFiles() != tc.rewrites {
			t.Errorf("%s → %q (rewrites %t), want %q (%t)", tc.in, got, got.RewritesExistingFiles(), tc.want, tc.rewrites)
		}
	}
}

// TestLidarrHistoryDecodesALiveResponse pins the decoder to a record captured from a
// real Lidarr (2026-10), trimmed: `data`, the quality block and most of the artist's
// links dropped, and the download URL — which carries an indexer API key — removed.
// What is left is every field the follower reads, in the shape Lidarr actually sends:
// the event type as a camelCase name, the date as RFC 3339, and the artist (asked for
// with includeArtist) under `artistName` / `foreignArtistId` / `path`. The same
// instance's import window confirmed the rest: `path` on every artist block, and the
// event names `grabbed`, `downloadImported`, `trackFileImported` and
// `trackFileRetagged` — the last arriving one per imported file, which is why it must
// not count as a file move.
func TestLidarrHistoryDecodesALiveResponse(t *testing.T) {
	const live = `[
  {
    "albumId": 62029,
    "artistId": 83,
    "trackId": 0,
    "sourceTitle": "Anderson .Paak & Cordae - Heavy Is The Crown (2026) [Album] [FLAC Lossless / WEB]",
    "quality": {"quality": {"id": 6, "name": "FLAC"}, "revision": {"version": 1, "real": 0, "isRepack": false}},
    "customFormats": [{"id": 3, "name": "FLAC"}, {"id": 4, "name": "WEB format"}],
    "customFormatScore": 4,
    "qualityCutoffNotMet": false,
    "date": "2026-10-23T09:14:23Z",
    "downloadId": "32B4D06E2A9320B785A667E3E5907CE34F2DBE8B",
    "eventType": "grabbed",
    "data": {"downloadClient": "qBittorrent", "size": "201796361"},
    "artist": {
      "status": "continuing",
      "ended": false,
      "artistName": "Anderson .Paak",
      "foreignArtistId": "d02dd67e-f655-4600-bc47-f789f59e7367",
      "tadbId": 0,
      "discogsId": 0,
      "artistType": "Person",
      "disambiguation": "American rapper and producer",
      "links": [{"url": "https://soundcloud.com/andersonpaak", "name": "soundcloud"}],
      "path": "/data/music/Anderson .Paak",
      "id": 83
    },
    "id": 912345
  }
]`
	var records []LidarrHistoryRecord
	if err := json.Unmarshal([]byte(live), &records); err != nil {
		t.Fatalf("a live Lidarr response did not decode: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("decoded %d records, want 1", len(records))
	}
	r := records[0]
	if r.ArtistID != 83 || r.AlbumID != 62029 || r.EventType != LidarrHistoryGrabbed {
		t.Errorf("record = %+v", r)
	}
	if want := "2026-10-23T09:14:23Z"; r.Date.UTC().Format("2006-01-02T15:04:05Z") != want {
		t.Errorf("date = %v, want %s", r.Date, want)
	}
	if r.Artist == nil || r.Artist.ForeignArtistID != "d02dd67e-f655-4600-bc47-f789f59e7367" ||
		r.Artist.Name != "Anderson .Paak" || r.Artist.Path != "/data/music/Anderson .Paak" {
		t.Errorf("artist = %+v, want the includeArtist block decoded", r.Artist)
	}
	if r.EventType.MovesFiles() {
		t.Error("a grab moves no file; following it would process an artist before the import")
	}
}

// TestLidarrMetadataProviderDecodesALiveResponse: the whole response from a real
// Lidarr set to *For new downloads only* — the recommended setting, which must not
// raise the tag-writing warning.
func TestLidarrMetadataProviderDecodesALiveResponse(t *testing.T) {
	const live = `{
  "metadataSource": "",
  "writeAudioTags": "newFiles",
  "scrubAudioTags": false,
  "embedCoverArt": true,
  "id": 1
}`
	var cfg LidarrMetadataProviderConfig
	if err := json.Unmarshal([]byte(live), &cfg); err != nil {
		t.Fatalf("a live Lidarr response did not decode: %v", err)
	}
	if cfg.WriteAudioTags != LidarrWriteAudioTagsNewFiles || cfg.WriteAudioTags.RewritesExistingFiles() {
		t.Errorf("writeAudioTags = %q, want newFiles and no warning", cfg.WriteAudioTags)
	}
}
