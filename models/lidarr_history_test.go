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
