package models

type LidarrArtist struct {
	Id   int64  `json:"id"`
	Name string `json:"artistName"` // Lidarr uses artistName
	Path string `json:"path"`
}

type LidarrTrackFile struct {
	Id      int64  `json:"id"`
	Path    string `json:"path"`
	AlbumID int64  `json:"albumId"`
}

type LidarrAlbum struct {
	Id       int64            `json:"id"`
	ArtistId int64            `json:"artistId"`
	Releases []LidarrAlbumRel `json:"releases"`
}

type LidarrAlbumRel struct {
	Id               int64  `json:"id"`
	Monitored        bool   `json:"monitored"`
	ForeignReleaseId string `json:"foreignReleaseId"` // MB release ID
}
