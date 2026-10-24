package models

import (
	"encoding/json"
	"fmt"
	"time"
)

type LidarrArtist struct {
	ID              int64  `json:"id"`
	ForeignArtistID string `json:"foreignArtistId"`
	Name            string `json:"artistName"` // Lidarr uses artistName
	Path            string `json:"path"`
}

type LidarrTrackFile struct {
	ID       int64  `json:"id"`
	Path     string `json:"path"`
	AlbumID  int64  `json:"albumId"`
	ArtistID int64  `json:"artistId"`
}

type LidarrTrack struct {
	ID                 int64  `json:"id"`
	Title              string `json:"title"`
	ForeignTrackID     string `json:"foreignTrackId"`     // MusicBrainz Track ID
	ForeignRecordingID string `json:"foreignRecordingId"` // MusicBrainz Recording ID
	TrackFileID        *int64 `json:"trackFileId"`
}

type LidarrAlbum struct {
	ID             int64                 `json:"id"`
	Title          string                `json:"title"`
	ForeignAlbumID string                `json:"foreignAlbumId"` // MusicBrainz release-group ID
	ArtistID       int64                 `json:"artistId"`
	Monitored      bool                  `json:"monitored"`
	AlbumType      string                `json:"albumType"`
	ReleaseDate    string                `json:"releaseDate"`
	Statistics     LidarrAlbumStatistics `json:"statistics"`
	Releases       []LidarrAlbumRel      `json:"releases"`
}

// LidarrAlbumStatistics carries Lidarr's have/total track counts for an album,
// which the collection mirror maps to owned/partial state.
type LidarrAlbumStatistics struct {
	TrackCount     int `json:"trackCount"`
	TrackFileCount int `json:"trackFileCount"`
}

type LidarrAlbumRel struct {
	ID               int64  `json:"id"`
	Monitored        bool   `json:"monitored"`
	ForeignReleaseID string `json:"foreignReleaseId"` // MB release ID
}

type LidarrTrackMetadataDetails struct {
	MBRecordingID string `json:"mb_recording_id"`
	MBTrackID     string `json:"mb_track_id"`
	TrackTitle    string `json:"track_title"`
	MBReleaseID   string `json:"mb_release_id"`
}

// LidarrCommand is one entry in Lidarr's command queue — what a POST to
// /api/v1/command returns and what GET /api/v1/command/{id} reports on.
//
// Only the fields a caller waits on are modelled. Lidarr returns a good deal more
// (trigger, queued/started/ended timestamps, a nested body echoing the request), none
// of which changes what Autotaggerr does with the answer.
type LidarrCommand struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Status is queued / started / completed / failed / aborted.
	Status string `json:"status"`
	// Message is Lidarr's own progress or failure text, worth quoting in an error
	// because it names the metadata-service problem when there is one.
	Message string `json:"message"`
}

// LidarrHistoryRecord is one entry in Lidarr's history, as /api/v1/history/since
// returns it. Only what following Lidarr's file changes needs is modelled: which
// artist and album, when, and what happened. Artist is filled when the request asks
// for includeArtist, which is how a record names the MusicBrainz artist it is about.
type LidarrHistoryRecord struct {
	ID          int64                  `json:"id"`
	ArtistID    int64                  `json:"artistId"`
	AlbumID     int64                  `json:"albumId"`
	Date        time.Time              `json:"date"`
	EventType   LidarrHistoryEventType `json:"eventType"`
	SourceTitle string                 `json:"sourceTitle"`
	Artist      *LidarrArtist          `json:"artist,omitempty"`
}

// LidarrHistoryEventType is Lidarr's EntityHistoryEventType. Lidarr serialises it as a
// camelCase name; the numeric form is accepted too, because the enum's JSON shape is a
// serializer setting on Lidarr's side rather than part of its API contract.
type LidarrHistoryEventType string

const (
	LidarrHistoryGrabbed               LidarrHistoryEventType = "grabbed"
	LidarrHistoryArtistFolderImported  LidarrHistoryEventType = "artistFolderImported"
	LidarrHistoryTrackFileImported     LidarrHistoryEventType = "trackFileImported"
	LidarrHistoryDownloadFailed        LidarrHistoryEventType = "downloadFailed"
	LidarrHistoryTrackFileDeleted      LidarrHistoryEventType = "trackFileDeleted"
	LidarrHistoryTrackFileRenamed      LidarrHistoryEventType = "trackFileRenamed"
	LidarrHistoryAlbumImportIncomplete LidarrHistoryEventType = "albumImportIncomplete"
	LidarrHistoryDownloadImported      LidarrHistoryEventType = "downloadImported"
	LidarrHistoryTrackFileRetagged     LidarrHistoryEventType = "trackFileRetagged"
	LidarrHistoryDownloadIgnored       LidarrHistoryEventType = "downloadIgnored"
)

// lidarrHistoryEventNames is EntityHistoryEventType's numeric order (0 is Unknown).
var lidarrHistoryEventNames = []LidarrHistoryEventType{
	"unknown",
	LidarrHistoryGrabbed,
	LidarrHistoryArtistFolderImported,
	LidarrHistoryTrackFileImported,
	LidarrHistoryDownloadFailed,
	LidarrHistoryTrackFileDeleted,
	LidarrHistoryTrackFileRenamed,
	LidarrHistoryAlbumImportIncomplete,
	LidarrHistoryDownloadImported,
	LidarrHistoryTrackFileRetagged,
	LidarrHistoryDownloadIgnored,
}

func (t *LidarrHistoryEventType) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		*t = LidarrHistoryEventType(name)
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("lidarr history event type %s is neither a name nor a number", string(data))
	}
	if n < 0 || n >= len(lidarrHistoryEventNames) {
		*t = "unknown"
		return nil
	}
	*t = lidarrHistoryEventNames[n]
	return nil
}

// MovesFiles reports whether the event means files appeared, vanished or moved on
// disk — which is what makes the cached Lidarr answers about the artist wrong and the
// artist worth processing again.
//
// trackFileRetagged is deliberately not one of them: Lidarr wrote tags, the files did
// not move, and the identity they carry did not change. Following it would also put
// Autotaggerr in a loop with a Lidarr that writes tags, each re-tagging after the other.
func (t LidarrHistoryEventType) MovesFiles() bool {
	switch t {
	case LidarrHistoryTrackFileImported, LidarrHistoryTrackFileDeleted, LidarrHistoryTrackFileRenamed,
		LidarrHistoryDownloadImported, LidarrHistoryArtistFolderImported:
		return true
	}
	return false
}

// LidarrMetadataProviderConfig is Lidarr's Settings → Metadata → *Write Metadata to
// Audio Files* section, as /api/v1/config/metadataprovider returns it. Only the tag
// writing mode is modelled — it is the one setting that decides whether Lidarr and
// Autotaggerr write the same files.
type LidarrMetadataProviderConfig struct {
	WriteAudioTags LidarrWriteAudioTags `json:"writeAudioTags"`
}

// LidarrWriteAudioTags is Lidarr's WriteAudioTagsType. Like the history event type it
// is decoded from either its name or its number.
type LidarrWriteAudioTags string

const (
	LidarrWriteAudioTagsNo       LidarrWriteAudioTags = "no"
	LidarrWriteAudioTagsNewFiles LidarrWriteAudioTags = "newFiles"
	LidarrWriteAudioTagsAllFiles LidarrWriteAudioTags = "allFiles"
	LidarrWriteAudioTagsSync     LidarrWriteAudioTags = "sync"
)

var lidarrWriteAudioTagsNames = []LidarrWriteAudioTags{
	LidarrWriteAudioTagsNo, LidarrWriteAudioTagsNewFiles, LidarrWriteAudioTagsAllFiles, LidarrWriteAudioTagsSync,
}

func (w *LidarrWriteAudioTags) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		*w = LidarrWriteAudioTags(name)
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("lidarr writeAudioTags %s is neither a name nor a number", string(data))
	}
	if n < 0 || n >= len(lidarrWriteAudioTagsNames) {
		*w = ""
		return nil
	}
	*w = lidarrWriteAudioTagsNames[n]
	return nil
}

// RewritesExistingFiles reports whether Lidarr is set to write tags to files it has
// already imported — *All files* (on every change to an album's metadata) or *Sync*
// (keeping tags in step with Lidarr, removing what it does not know). Either one
// rewrites files Autotaggerr has tagged, with Lidarr's narrower set of tags, and the
// two then take turns. *For new downloads only* is the recommended setting: Lidarr tags
// a file once on import, and Autotaggerr owns it from then on.
func (w LidarrWriteAudioTags) RewritesExistingFiles() bool {
	return w == LidarrWriteAudioTagsAllFiles || w == LidarrWriteAudioTagsSync
}
