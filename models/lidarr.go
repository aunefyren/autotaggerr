package models

type LidarrArtist struct {
	ID   int64  `json:"id"`
	Name string `json:"artistName"` // Lidarr uses artistName
	Path string `json:"path"`
}

type LidarrTrackFile struct {
	ID       int64  `json:"id"`
	Path     string `json:"path"`
	AlbumID  int64  `json:"albumId"`
	ArtistID int64  `json:"artistId"`
}

type LidarrTrack struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	ForeignTrackID string `json:"foreignTrackId"` // MusicBrainz Track ID
	TrackFileID    int64  `json:"trackFileId"`
}

type LidarrAlbum struct {
	ID       int64            `json:"id"`
	ArtistID int64            `json:"artistId"`
	Releases []LidarrAlbumRel `json:"releases"`
}

type LidarrAlbumRel struct {
	ID               int64  `json:"id"`
	Monitored        bool   `json:"monitored"`
	ForeignReleaseID string `json:"foreignReleaseId"` // MB release ID
}
