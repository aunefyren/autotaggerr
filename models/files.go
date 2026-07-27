package models

import "time"

type FileTags struct {
	Artist                string   `json:"artist"`
	ArtistSemicolon       string   `json:"artist_semicolon"`
	AlbumArtist           string   `json:"album_artist"`
	Genres                []string `json:"genres"`
	OriginalDate          string   `json:"original_date"`
	OriginalYear          string   `json:"original_year"`
	ReleaseDate           string   `json:"release_date"`
	ReleaseYear           string   `json:"release_year"`
	Album                 string   `json:"album"`
	Title                 string   `json:"title"`
	ISRC                  string   `json:"isrc"`
	Track                 string   `json:"track"`
	TrackTotal            string   `json:"track_total"`
	DiscNumber            string   `json:"disc_number"`
	DiscTotal             string   `json:"disc_total"`
	MBAlbumStatus         string   `json:"mm_album_status"`
	MBAlbumType           string   `json:"mm_album_type"`
	MBAlbumReleaseCountry string   `json:"mm_album_release_country"`
	MBAlbumID             string   `json:"mm_album_id"`
	MBArtistID            string   `json:"mm_artist_id"`
	MBAlbumArtistID       string   `json:"mm_album_artist_id"`
	MBReleaseGroupID      string   `json:"mm_release_group_id"`
	MBReleaseTrackID      string   `json:"mm_release_track_id"`
	MBRecordingID         string   `json:"mm_recording_id"`
	Script                string   `json:"script"`
	RecordLabel           string   `json:"record_label"`
	Media                 string   `json:"media"`
	Barcode               string   `json:"barcode"`
	ASIN                  string   `json:"asin"`
	CatalogNumber         string   `json:"catalog_number"`
	Composer              string   `json:"composer"`
	Author                string   `json:"author"`
}

type CachedMusicBrainzRelease struct {
	Release   MusicBrainzReleaseResponse `json:"release"`
	Timestamp time.Time                  `json:"timestamp"`
	// ExpiresAt is set at write time with a jittered TTL so that entries
	// fetched together during one scan do not all expire in the same window.
	// A zero value is treated as already expired (self-healing migration).
	ExpiresAt time.Time `json:"expires_at"`
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

type CachedLidarrTrackFilesRelease struct {
	TrackFiles []LidarrTrackFile `json:"track_files"`
	Timestamp  time.Time         `json:"timestamp"`
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
