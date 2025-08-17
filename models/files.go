package models

import "time"

type FileTags struct {
	Artist      string `json:"artist"`
	AlbumArtist string `json:"album_artist"`
	Genre       string `json:"genre"`
	Date        string `json:"date"`
	Year        string `json:"year"`
	Album       string `json:"album"`
	Title       string `json:"string"`
	ISRC        string `json:"isrc"`
	Track       string `json:"track"`
	TrackTotal  string `json:"track_total"`
	DiscNumber  string `json:"disc_number"`
	DiscTotal   string `json:"disc_total"`
}

type CachedMusicBrainzRelease struct {
	Release   MusicBrainzReleaseResponse `json:"release"`
	Timestamp time.Time                  `json:"timestamp"`
}

type CachedLidarrArtistRelease struct {
	Artist    LidarrArtist `json:"artist"`
	Timestamp time.Time    `json:"timestamp"`
}

type CachedLidarrAlbumRelease struct {
	Album     LidarrAlbum `json:"album"`
	Timestamp time.Time   `json:"timestamp"`
}

type CachedLidarrTracksRelease struct {
	Tracks    []LidarrTrack `json:"track"`
	Timestamp time.Time     `json:"timestamp"`
}

type PlexAlbumKeyCache struct {
	AlbumKey  string    `json:"album_key"`
	Timestamp time.Time `json:"timestamp"`
}

type FfprobeFormat struct {
	Format struct {
		Tags map[string]string `json:"tags"`
	} `json:"format"`
}
