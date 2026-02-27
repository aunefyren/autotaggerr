package models

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
	ID             int64            `json:"id"`
	Title          string           `json:"title"`
	ForeignAlbumID string           `json:"foreignAlbumId"`
	ArtistID       int64            `json:"artistId"`
	Releases       []LidarrAlbumRel `json:"releases"`
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
