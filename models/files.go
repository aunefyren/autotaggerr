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
	// ReleaseGroup is the download's release group when the source knows it (Lidarr's
	// track file); nil when it does not, which leaves what the index has alone.
	ReleaseGroup *string `json:"release_group,omitempty"`
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
	AlbumArtist  string   `json:"album_artist"`
	AlbumArtists []string `json:"album_artists"`
	// ArtistSort and AlbumArtistSort are MusicBrainz's sort names for the same two,
	// shaped the same way: the whole track credit, and the first album artist. Plex
	// reads both when it first ingests an artist.
	ArtistSort      string   `json:"artist_sort"`
	AlbumArtistSort string   `json:"album_artist_sort"`
	Genres          []string `json:"genres"`
	OriginalDate    string   `json:"original_date"`
	OriginalYear    string   `json:"original_year"`
	ReleaseDate     string   `json:"release_date"`
	ReleaseYear     string   `json:"release_year"`
	// Date and Year are what DATE and YEAR carry: the edition's release date, or the
	// album's original one when the profile asks (TaggerSettings.OriginalDateAsDate).
	// ReleaseDate stays the edition's either way, in RELEASEDATE.
	Date          string   `json:"date"`
	Year          string   `json:"year"`
	Album         string   `json:"album"`
	Title         string   `json:"title"`
	ISRCs         []string `json:"isrcs"`
	Track         string   `json:"track"`
	TrackTotal    string   `json:"track_total"`
	DiscNumber    string   `json:"disc_number"`
	DiscTotal     string   `json:"disc_total"`
	MBAlbumStatus string   `json:"mm_album_status"`
	// MBAlbumTypes is the release type: MusicBrainz's primary and secondary types as
	// separate values, or the one Plex-shaped value ("album; live") the tagger
	// profile can ask for instead (see modules.ReleaseTypes).
	MBAlbumTypes          []string `json:"mm_album_types"`
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
	CatalogNumbers        []string `json:"catalog_numbers"`
	// DiscSubtitle is the medium's own title, when MusicBrainz gives it one.
	DiscSubtitle string `json:"disc_subtitle"`
	// Compilation is "1" for a Various Artists release and "" otherwise — Picard's
	// meaning since 1.3, not MusicBrainz's "compilation" secondary type.
	Compilation string `json:"compilation"`
	// Language is the release's text language, ISO 639-3 ("eng").
	Language string `json:"language"`
	ASIN     string `json:"asin"`
	// BPM is the stored tempo rounded to a whole number, or "" when the profile does
	// not write it or there is nothing good enough to write. It does not come from
	// MusicBrainz: modules.TempoTagValue fills it from track_tempos after the rest of
	// the tags are built.
	BPM string `json:"bpm"`
	// Composer and Author used to sit here, hardcoded to "" and read by neither tag
	// map, so their only possible effect was clearing another tagger's value under
	// remove_values. MusicBrainz can supply composer via work relations; adding it
	// back is a fetch and a mapping (docs/wip.md), not a field on this struct.
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

// PlexAlbumKeyCache is one album folder's answer from Plex: the album holding its
// files, or an empty AlbumKey for "Plex does not have these files", which is cached
// too so an album missing from Plex is not searched for once per track.
type PlexAlbumKeyCache struct {
	// ServerID is the media server row the key belongs to: a rating key means nothing
	// on another server. Empty on entries cached before there could be several.
	ServerID  string    `json:"server_id,omitempty"`
	AlbumKey  string    `json:"album_key"`
	Title     string    `json:"title,omitempty"`
	Folder    string    `json:"folder,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}
