package models

import "time"

// Correlation is the resolved identity of a track file: which MusicBrainz
// release/track/recording it maps to, plus which source decided that. It is the
// output of a Manager and the unit persisted into the library_items index.
type Correlation struct {
	MBReleaseID      string `json:"mb_release_id"`
	MBReleaseTrackID string `json:"mb_release_track_id"`
	MBRecordingID    string `json:"mb_recording_id"`
	TrackTitle       string `json:"track_title"`
	Source           string `json:"source"`
}

// TagDiffEntry is one tag's current-vs-desired state for the read-only diff view.
type TagDiffEntry struct {
	Key     string `json:"key"`
	Current string `json:"current"`
	Desired string `json:"desired"`
	Changed bool   `json:"changed"`
}

// FileTags is what Autotaggerr wants a file to say, one field per concept rather
// than one per tag key — the engines decide which keys carry it.
//
// A field that can genuinely hold several values holds them as several values. They
// used to arrive here pre-joined, which meant the only representation a writer could
// produce was the joined one and the split between "a field with two values" and "a
// field whose value contains a separator" had already been lost by the time anything
// could act on it.
type FileTags struct {
	// Artist is the whole credit rendered with MusicBrainz's own join phrases
	// ("A feat. B"), which is a single name, not a list. Artists is the same credit
	// as its separate parts.
	Artist  string   `json:"artist"`
	Artists []string `json:"artists"`
	// AlbumArtist is the first credited album artist only, because Plex has no
	// concept of several and renders a joined string as one artist named "A; B".
	// AlbumArtists carries the whole credit for players that can read it — without
	// it the names disagreed with MBAlbumArtistIDs, which has always listed every
	// credited artist.
	AlbumArtist           string   `json:"album_artist"`
	AlbumArtists          []string `json:"album_artists"`
	Genres                []string `json:"genres"`
	OriginalDate          string   `json:"original_date"`
	OriginalYear          string   `json:"original_year"`
	ReleaseDate           string   `json:"release_date"`
	ReleaseYear           string   `json:"release_year"`
	Album                 string   `json:"album"`
	Title                 string   `json:"title"`
	ISRCs                 []string `json:"isrcs"`
	Track                 string   `json:"track"`
	TrackTotal            string   `json:"track_total"`
	DiscNumber            string   `json:"disc_number"`
	DiscTotal             string   `json:"disc_total"`
	MBAlbumStatus         string   `json:"mm_album_status"`
	MBAlbumType           string   `json:"mm_album_type"`
	MBAlbumReleaseCountry string   `json:"mm_album_release_country"`
	MBAlbumID             string   `json:"mm_album_id"`
	MBArtistIDs           []string `json:"mm_artist_ids"`
	MBAlbumArtistIDs      []string `json:"mm_album_artist_ids"`
	MBReleaseGroupID      string   `json:"mm_release_group_id"`
	MBReleaseTrackID      string   `json:"mm_release_track_id"`
	MBRecordingID         string   `json:"mm_recording_id"`
	Script                string   `json:"script"`
	RecordLabels          []string `json:"record_labels"`
	Media                 string   `json:"media"`
	Barcode               string   `json:"barcode"`
	ASIN                  string   `json:"asin"`
	CatalogNumbers        []string `json:"catalog_numbers"`
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
