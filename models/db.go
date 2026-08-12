package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GORM domain models. These are the user-managed entities that live in the
// database (as opposed to bootstrap config in config.json).

// Component type / enum-ish string constants (stored as plain strings for
// forward-compatibility; validated at the API layer).
const (
	DataSourceTypeMusicBrainz = "musicbrainz"
	// DataSourceTypeAcoustID is audio fingerprinting. It identifies *recordings*,
	// not releases, so it never replaces MusicBrainz — it suggests what a file is
	// when nothing else can.
	DataSourceTypeAcoustID = "acoustid"
	// DataSourceTypeCoverArtArchive is album covers, keyed by release-group or
	// release MBID. It needs no credential, so it is seeded enabled — artwork is
	// browsing chrome, and nothing in the pipeline depends on it.
	DataSourceTypeCoverArtArchive = "coverartarchive"
	// DataSourceTypeFanart is artist portraits and backdrops. MusicBrainz has no
	// artist images at all, so this is the only source for them — and it is useless
	// without a personal API key, which is why it is not seeded.
	DataSourceTypeFanart = "fanart"

	// Data source categories. The four types share a table because they share every
	// field (URL, key, rate limit, enabled, health) and the same health-check code,
	// but they are *not* interchangeable: only a metadata provider can be a library's
	// data source, fingerprinting only ever suggests an identification, and artwork
	// feeds the browsing pages and nothing in the pipeline. Offering all four
	// wherever a "data source" is asked for is what made the model confusing.
	DataSourceCategoryMetadata    = "metadata"
	DataSourceCategoryFingerprint = "fingerprint"
	DataSourceCategoryArtwork     = "artwork"

	ManagerTypeLidarr      = "lidarr"
	ManagerTypeAutotaggerr = "autotaggerr"

	CorrelationSourceLidarr      = "lidarr"
	CorrelationSourceTags        = "tags"
	CorrelationSourceFingerprint = "fingerprint"
	CorrelationSourceManual      = "manual"

	LibraryItemStatusOK        = "ok"
	LibraryItemStatusUnmatched = "unmatched"
	LibraryItemStatusError     = "error"

	UserRoleAdmin = "admin"

	// EventTypeProcess is the full pipeline (walk, metadata, tag). It was recorded
	// as "scan" until the verbs were named apart: Scan is now the cheap re-derivation
	// of the collection from the index. Rows written under the old value are
	// rewritten once at startup by events.MigrateLegacyTypes.
	//
	// It is now a **parent**: the run's own row carries the scope and the outcome,
	// and each stage records its own event under it (see Event.ParentID). The file
	// counters that used to sit here belong to the tagging stage.
	EventTypeProcess    = "process"
	EventTypeLegacyScan = "scan"
	// EventTypeProcessFiles is the walk half of a processing run, recorded under its
	// own type until tagging became one event however it was reached. Rows written
	// under it are rewritten to EventTypeTagFiles at startup by
	// events.MigrateLegacyTypes.
	//
	// It was kept apart from Tag files on the grounds that a stage nobody pressed
	// should not appear in the feed under a verb's name. That held while a stage row
	// had nothing on it saying where it came from; the feed now names every row's
	// parent, so the same work under two type names was only ever two entries in the
	// type filter for one thing.
	EventTypeProcessFiles = "process_files"
	// EventTypeCountFiles is the walk that sizes a run before it starts: every root
	// read once to count the files the run will visit.
	//
	// It is a stage of its own because it is the first minutes of a cold run and used
	// to be invisible — it ran inside the refresh phase, with the progress bar sitting
	// at 0 of 0 throughout, which is exactly the shape a hang has.
	EventTypeCountFiles = "count_files"
	// EventTypeCollectionScan is the Scan verb: re-deriving what the collection holds
	// from the files already indexed. It records an event whether it was pressed on
	// its own or run as a stage of something larger — a rebuild that moved an album
	// between artists is news either way, and it was the one verb of the four that
	// reported nothing at all.
	EventTypeCollectionScan = "collection_scan"
	// EventTypeTagFiles is every pass that writes tags to files, whether a user
	// pressed *Tag files* or a processing run reached its tagging stage. One type,
	// one emitter, one rendering: a cascading activity and a hand-pressed one are the
	// same work, and the feed shows the difference by naming the run a row belongs to
	// rather than by filing it under a second name.
	//
	// Inside a run it covers both halves of the writing: the files found by the walk
	// and the files re-tagged because their release changed upstream. The two used to
	// be separate rows, which put the walk's counters next to a row whose only content
	// was a list of release MBIDs the metadata stage had already listed.
	//
	// It was recorded as "drift_sync" until the verbs were named apart, which made a
	// Tag files run read as "Metadata sync" in the feed — the name of the verb that
	// was split out of it. Old rows keep the old type rather than being migrated:
	// a pre-split drift sync really did refresh metadata *and* re-tag, so calling
	// those rows Tag files would misreport what they did.
	EventTypeTagFiles    = "tag_files"
	EventTypeDriftSync   = "drift_sync"
	EventTypeLidarrSync  = "lidarr_sync"
	EventTypePlexRefresh = "plex_refresh"
	EventTypeMigration   = "mb_migration"
	EventTypeMirror      = "mb_mirror"
	// EventTypeArtwork is a pass over the image cache. Its own type rather than a
	// share of EventTypeMirror because artwork providers are a different kind of
	// data source spending a different budget, and folding the two together put
	// counters measured in images beside counters measured in MusicBrainz requests.
	EventTypeArtwork = "artwork_refresh"
	EventTypeHealth  = "health_check"

	EventStatusRunning = "running"
	EventStatusOK      = "ok"
	EventStatusError   = "error"

	ManagedByAutotaggerr = "autotaggerr"
	ManagedByLidarr      = "lidarr"
	ManagedByMixed       = "mixed"
	// ManagedByUnknown: the artist's files live in a library whose manager cannot
	// be resolved (deleted row, dangling ManagerID). Reported as its own state
	// rather than folded into "autotaggerr", so missing information is never
	// presented as a positive claim about who manages the artist.
	ManagedByUnknown = "unknown"
)

// Base is the shared primary-key + timestamp mixin for domain models. IDs are
// UUIDv7 (time-ordered, so index locality stays reasonable) assigned by the
// BeforeCreate hook — sqlite has no server-side UUID default, so generating it in
// Go keeps the scheme portable across sqlite/postgres/mysql.
type Base struct {
	ID        uuid.UUID `gorm:"type:uuid;primarykey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// BeforeCreate assigns a UUIDv7 when the caller has not set one. It is promoted to
// every model that embeds Base, so GORM invokes it on their inserts.
func (b *Base) BeforeCreate(*gorm.DB) error {
	if b.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		b.ID = id
	}
	return nil
}

// DataSource is a metadata provider (MusicBrainz today; AcoustID later). Used by
// every manager to fetch the tag-payload data for a release.
type DataSource struct {
	Base
	Name    string `gorm:"uniqueIndex;not null" json:"name"`
	Type    string `gorm:"not null" json:"type"`
	BaseURL string `json:"base_url"`
	Contact string `json:"contact"`
	// APIKey is the provider credential (AcoustID's client key). Write-only, like
	// the Lidarr secrets: settable through the API, never returned by it.
	APIKey    string  `json:"-"`
	RateLimit float64 `json:"rate_limit"` // requests per second
	// No gorm default on bool fields: GORM omits a Go zero value (false) from the
	// INSERT when a column default is set, so a user-chosen false would be silently
	// overridden by the default. Callers set these explicitly instead.
	Enabled     bool       `json:"enabled"`
	Health      string     `json:"health"`
	LastChecked *time.Time `json:"last_checked"`
}

// DataSourceCategory maps a data source type to the role it can play. An unknown
// type reports "" — callers treat that as "not valid for anything" rather than
// guessing. This is the single Go-side definition; the SPA has the matching one.
func DataSourceCategory(sourceType string) string {
	switch sourceType {
	case DataSourceTypeMusicBrainz:
		return DataSourceCategoryMetadata
	case DataSourceTypeAcoustID:
		return DataSourceCategoryFingerprint
	case DataSourceTypeCoverArtArchive, DataSourceTypeFanart:
		return DataSourceCategoryArtwork
	}
	return ""
}

// DataSourceIsSingleton reports whether a second row of this type is meaningless.
// There is exactly one AcoustID service, one Cover Art Archive and one fanart.tv, so
// duplicates are a configuration mistake that silently does nothing — only the first
// row found is ever used. MusicBrainz is deliberately excluded: running a local
// mirror alongside the public service is legitimate.
func DataSourceIsSingleton(sourceType string) bool {
	switch sourceType {
	case DataSourceTypeAcoustID, DataSourceTypeCoverArtArchive, DataSourceTypeFanart:
		return true
	}
	return false
}

// Manager is the correlation authority for a library: it decides which MB
// release/track a file maps to. Lidarr reads that decision from Lidarr;
// Autotaggerr derives and owns it natively.
type Manager struct {
	Base
	Name    string `gorm:"uniqueIndex;not null" json:"name"`
	Type    string `gorm:"not null" json:"type"`
	Enabled bool   `json:"enabled"`

	// Lidarr-specific connection config. The API key and cookie are secrets, so
	// they are never serialized in API responses (json:"-"); write endpoints will
	// accept them via a dedicated input DTO.
	LidarrBaseURL      string `json:"lidarr_base_url,omitempty"`
	LidarrAPIKey       string `json:"-"`
	LidarrHeaderCookie string `json:"-"`

	// LidarrSkipArtistRefresh turns off the one write Autotaggerr makes to this
	// manager: asking it to refresh an artist whose album IDs have stopped resolving.
	// Nothing else is ever written — see modules/lidarr_command.go for why this one is
	// the exception.
	//
	// Phrased as "skip this" rather than "allow this" for the reason migration.Policy
	// is phrased that way: the zero value has to be the wanted behaviour. A manager row
	// written before this field existed decodes to false, and false must mean the
	// repair runs — otherwise every existing install quietly keeps accumulating albums
	// it cannot read, with no indication that one setting fixes it. A `default:true`
	// tag on the positive spelling would not do: GORM omits zero-valued fields that
	// carry a default, so an explicit "off" on create would be written as "on".
	//
	// The switch exists for the case where the API key is deliberately read-only, or
	// where something else owns refresh scheduling.
	LidarrSkipArtistRefresh bool `json:"lidarr_skip_artist_refresh"`

	// Autotaggerr-specific: which data source this manager resolves against.
	DefaultDataSourceID *uuid.UUID `gorm:"type:uuid" json:"default_data_source_id,omitempty"`

	Health      string     `json:"health"`
	LastChecked *time.Time `json:"last_checked"`
}

// TaggerProfile is a reusable set of tag-writing settings (mirrors the old
// autotaggerr_* tag flags). One built-in engine consumes it today; kept
// first-class so tag-schema dialects / NFO sidecars can be added as siblings.
type TaggerProfile struct {
	Base
	Name                               string `gorm:"uniqueIndex;not null" json:"name"`
	WriteTags                          bool   `json:"write_tags"`
	RemoveValues                       bool   `json:"remove_values"`
	UseCurrentArtistName               bool   `json:"use_current_artist_name"`
	UseCustomArtistDelimiter           bool   `json:"use_custom_artist_delimiter"`
	CustomArtistDelimiter              string `json:"custom_artist_delimiter"`
	CustomArtistDelimiterCommas        bool   `json:"custom_artist_delimiter_commas"`
	IgnoreRedundantContributingArtists bool   `json:"ignore_redundant_contributing_artists"`
	// MaxGenres caps how many genres are written to GENRE. Zero or less means
	// DefaultMaxGenres, so a profile row predating the column behaves like a new one.
	MaxGenres int `json:"max_genres"`
	// MP3MultiValueTags writes several values into one ID3 frame the way the format
	// means it — null-separated, ID3v2.4 — instead of joining them with "; ".
	// Off by default; see TaggerSettings.MP3MultiValueTags for why it is a choice at
	// all rather than simply correct.
	MP3MultiValueTags bool `json:"mp3_multi_value_tags"`
}

// TaggerSettings is the subset of a profile that the tag writers actually read: how
// artists are joined, how many genres are kept, what happens to a value the new
// metadata does not supply.
//
// It is its own type rather than a ConfigStruct because tagging is *not* process
// config. These knobs were global keys in config.json before tagger profiles existed,
// and the tagging code kept taking a ConfigStruct long after the values started coming
// from a profile row — which meant config.json carried eight keys nothing read and the
// signatures claimed a scope they did not have. One library can now tag differently
// from another, and the parameter says so.
type TaggerSettings struct {
	RemoveValues                       bool
	UseCurrentArtistName               bool
	UseCustomArtistDelimiter           bool
	CustomArtistDelimiter              string
	CustomArtistDelimiterCommas        bool
	IgnoreRedundantContributingArtists bool
	// MaxGenres caps how many genres reach GENRE. Zero or less means
	// DefaultMaxGenres.
	MaxGenres int
	// MP3MultiValueTags picks how an MP3 says that a field has several values. Off
	// (the default) joins them into one string with "; "; on writes the spec-correct
	// ID3v2.4 form, one frame whose values are separated by a null byte.
	//
	// It is a setting rather than a fix because the two forms serve different readers
	// and there is no representation that serves both. Picard, MusicBee, foobar2000
	// and Navidrome read the null-separated form natively and treat the joined string
	// as one long genre. ffmpeg reads only the *first* value out of a null-separated
	// frame, so anything built on it — Plex above all — sees one genre where the
	// joined string shows several.
	//
	// Off is the default because Autotaggerr ships a Plex client and refreshes Plex
	// after a write: turning this on by surprise would take genres away from the
	// setup the tool is most often pointed at. FLAC needs no such choice — ffmpeg
	// joins repeated Vorbis comments on read, so the spec-correct form costs nothing
	// there and is unconditional (see docs/tagging.md).
	MP3MultiValueTags bool
}

// Settings projects the stored profile onto the values the tag writers read.
func (t TaggerProfile) Settings() TaggerSettings {
	return TaggerSettings{
		RemoveValues:                       t.RemoveValues,
		UseCurrentArtistName:               t.UseCurrentArtistName,
		UseCustomArtistDelimiter:           t.UseCustomArtistDelimiter,
		CustomArtistDelimiter:              t.CustomArtistDelimiter,
		CustomArtistDelimiterCommas:        t.CustomArtistDelimiterCommas,
		IgnoreRedundantContributingArtists: t.IgnoreRedundantContributingArtists,
		MaxGenres:                          t.MaxGenres,
		MP3MultiValueTags:                  t.MP3MultiValueTags,
	}
}

// Library is a configured folder plus the components that govern it.
type Library struct {
	Base
	Name            string     `gorm:"not null" json:"name"`
	Path            string     `gorm:"uniqueIndex;not null" json:"path"`
	ManagerID       *uuid.UUID `gorm:"type:uuid" json:"manager_id"`
	DataSourceID    *uuid.UUID `gorm:"type:uuid" json:"data_source_id"`
	TaggerProfileID *uuid.UUID `gorm:"type:uuid" json:"tagger_profile_id"`
	Enabled         bool       `json:"enabled"`
	Cron            string     `json:"cron"`
	LastScan        *time.Time `json:"last_scan"`
	// UseAcoustID opts this library in to fingerprint identification. Off by
	// default and the third of three independent switches (a configured AcoustID
	// data source, fpcalc on PATH, this flag) — any one of them off and the library
	// behaves exactly as it did before the feature existed.
	//
	// The column name is pinned: GORM's default naming turns "AcoustID" into
	// "use_acoust_id", which reads like a typo and silently breaks any raw column
	// reference written from the Go field or JSON name.
	UseAcoustID bool `gorm:"column:use_acoustid" json:"use_acoustid"`
}

// LibraryItem is the owned correlation index: one row per file, recording which
// MB release/track it was matched to, who decided it, and when it was last
// scanned/tagged. This backbone lets scans skip unchanged files and powers drift
// detection and present/wanted reporting.
type LibraryItem struct {
	Base
	LibraryID   uuid.UUID  `gorm:"type:uuid;index;not null" json:"library_id"`
	Path        string     `gorm:"uniqueIndex;not null" json:"path"`
	Size        int64      `json:"size"`
	ModTime     *time.Time `json:"mod_time"`
	ContentHash string     `json:"content_hash"`

	MBReleaseID      string `gorm:"index" json:"mb_release_id"`
	MBRecordingID    string `json:"mb_recording_id"`
	MBReleaseTrackID string `json:"mb_release_track_id"`

	CorrelationSource string `json:"correlation_source"`
	// CorrelatedByManager is the manager *type* that produced the correlation. A
	// scan only skips an unchanged file when the library's current manager still
	// matches, so swapping or disabling a library's manager re-correlates its files
	// instead of leaving them reporting the old source (the same escape hatch
	// ProcessedVersion provides for tag-logic changes). Pinned items are exempt —
	// a manual correlation outlives any manager change.
	CorrelatedByManager string     `json:"correlated_by_manager"`
	CorrelatedAt        *time.Time `json:"correlated_at"`
	LastScannedAt       *time.Time `json:"last_scanned_at"`
	LastTaggedAt        *time.Time `json:"last_tagged_at"`
	TagStateHash        string     `json:"tag_state_hash"`
	// ProcessedVersion records the app version that last processed this file. A
	// scan only skips an unchanged file when the running version still matches, so
	// upgrades that change tag logic re-process everything once.
	ProcessedVersion string `json:"processed_version"`
	// Pinned marks a manual correlation that automatic resolution must never override.
	Pinned bool `json:"pinned"`
	// Status is what the *last attempt* did, not what the file is. The MB ID columns
	// above are the file's identity and outlive any failure to act on it — a lookup
	// that fails must not erase what a file is, or the file leaves the disk view and
	// its album reads as mismatched against the manager.
	//
	// Because of that it is never the test for membership — see models.TaggableItems
	// and collection.ArtistItems. It is indexed for the queries that legitimately ask
	// about outcomes: collection.disownedItems, and asking a library what failed.
	Status string `gorm:"index" json:"status"`
	Error  string `json:"error"`
	// LastErrorAt dates the failure in Error. A bare string cannot answer "is this
	// still happening or did it clear weeks ago", which is the first thing worth
	// knowing when a failure shows up in a list.
	LastErrorAt *time.Time `json:"last_error_at"`
	// LastErrorTransient marks a failure the app expects to survive on its own —
	// MusicBrainz unreachable, throttled, or 5xx (modules.ErrTransient). It is the
	// difference between "this will retry" and "an admin has to fix this", which
	// matters most during an outage, when every file in a run fails at once and
	// would otherwise be indistinguishable from a library full of broken files.
	LastErrorTransient bool `json:"last_error_transient"`
}

// TaggableItems is a GORM scope narrowing a LibraryItem query to the files a tag write
// has something to write: the ones carrying an identity the manager has not withdrawn.
// It is the membership test for every path that *writes*.
//
// It replaces `status = ok`, which was the wrong question in both directions:
//
//   - **An error is not a disqualification.** Status is what the last attempt did, not
//     what the file is, and identity survives a failure (see recordItem). Excluding an
//     errored file from a re-tag is exactly what stopped it recovering once the cause
//     was fixed — the failure kept the file out of the only verb that could clear it.
//   - **`unmatched` is a disqualification, and a different one.** It is not a failed
//     attempt: it is the manager answering that it does not know this file. Whatever
//     release ID the row still carries is an answer that has been withdrawn, and
//     writing tags from it would stamp the file with an identity its authority has
//     disclaimed. The same reasoning that keeps these out of the disk view
//     (collection.ownedItemRows), applied to writes.
//
// The two failure states part company here, which `status = ok` could not express.
// Folder resolution for the repair verbs deliberately reaches wider — see
// collection.ArtistTargets.
//
// It exists as a scope rather than as a predicate spelled out per call site because
// the guard and the work must agree: the API refuses a re-tag that would touch no
// files by counting them, and if that count and the runner's query drifted apart the
// button would either refuse work there was, or queue a run that tagged nothing.
// The columns are table-qualified so the scope survives a join: the collection-wide
// guard counts across `libraries`, and an unqualified name there relies on the other
// table not happening to have one too.
func TaggableItems(db *gorm.DB) *gorm.DB {
	return db.Where("library_items.mb_release_id <> '' AND library_items.status <> ?",
		LibraryItemStatusUnmatched)
}

// MusicbrainzReleaseCache replaces config/mb_releases.json. Its primary key is the
// MusicBrainz release ID (not a generated UUID). Payload is the raw release JSON;
// ExpiresAt keeps the jittered-TTL behavior and MBVersion enables upstream-drift
// detection (M4).
type MusicbrainzReleaseCache struct {
	MBID      string    `gorm:"primarykey" json:"mb_id"`
	Payload   string    `gorm:"type:text" json:"-"`
	FetchedAt time.Time `json:"fetched_at"`
	ExpiresAt time.Time `json:"expires_at"`
	MBVersion string    `json:"mb_version"`
}

// MusicBrainz entity kinds held in MusicbrainzEntityCache. These are cache
// namespaces, not MusicBrainz's own entity vocabulary: "discography" and
// "editions" are *browse results* keyed by the entity they were browsed for,
// which is why they cannot share a table keyed by MBID alone.
const (
	MBEntityArtist      = "artist"      // /artist/{id} — who the artist is
	MBEntityDiscography = "discography" // /release-group?artist={id} — their albums
	MBEntityEditions    = "editions"    // /release?release-group={id} — an album's pressings
)

// MusicbrainzEntityCache is the persistent cache for every MusicBrainz lookup
// other than the full release payload, which keeps its own table because it
// predates this one and carries drift-detection columns the others have no use
// for.
//
// The primary key is (Entity, MBID) rather than MBID alone: the same artist ID is
// both an `artist` lookup and a `discography` browse, and they have different
// payload shapes and different refresh costs. Payload is the raw JSON of whatever
// the lookup returned, so adding a cached endpoint needs no schema change.
//
// Before this table these three lookups lived in process memory only, which meant
// a restart re-paid a cold discography — up to five rate-limited requests — the
// first time anyone opened an artist page.
type MusicbrainzEntityCache struct {
	Entity    string    `gorm:"primaryKey;size:32" json:"entity"`
	MBID      string    `gorm:"primaryKey;size:64" json:"mb_id"`
	Payload   string    `gorm:"type:text" json:"-"`
	FetchedAt time.Time `json:"fetched_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Sources held in ProviderCache. Each is one endpoint of one service, keyed by
// whatever identifier that service answers to: Lidarr's own numeric IDs, and for
// Plex the album title, which is all the refresh path has to go on.
const (
	ProviderCacheLidarrArtists    = "lidarr_artists"
	ProviderCacheLidarrAlbums     = "lidarr_albums"
	ProviderCacheLidarrTracks     = "lidarr_tracks"
	ProviderCacheLidarrTrackFiles = "lidarr_trackfiles"
	ProviderCachePlexAlbumKeys    = "plex_album_keys"
)

// ProviderCache is the persistent cache for lookups against the services a library
// is managed by — Lidarr and Plex. It replaces five JSON files under config/.
//
// One table rather than five because all five are the same shape: a keyed blob with
// an expiry, differing only in which service answered. A sixth endpoint is a new
// constant, not a migration.
//
// Those files were not merely a format choice. They were written by a *batched*
// flusher that ran only during a scan, at the end of a refresh pass, or in one-shot
// mode — and with no shutdown handler anywhere, a restart between a lookup and the
// next flush dropped the writes. A Lidarr sync triggered from the Collection page
// routinely never reached disk at all. Every write here goes through as it happens,
// which is what removed the batching (and the whole dirty/flush mechanism) from the
// codebase.
type ProviderCache struct {
	Source string `gorm:"primaryKey;size:32" json:"source"`
	// Key is the service's own identifier. 191 characters is the classic index-safe
	// limit, kept so the store can still move to MySQL; the only key not bounded by
	// construction is a Plex album title, and one longer than that fails to cache
	// rather than failing the lookup.
	Key       string    `gorm:"primaryKey;size:191" json:"key"`
	Payload   string    `gorm:"type:text" json:"-"`
	FetchedAt time.Time `json:"fetched_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ArtworkCacheEntry is the index over the artwork disk cache under
// config/artwork/. The image bytes stay on disk — they are megabytes each and
// nothing queries them — while this row carries the two things the filesystem
// cannot answer: when the image was fetched, and whether the providers said there
// is no image at all.
//
// Negative results matter more than positive ones here. "This MBID has no cover"
// is the common case for obscure releases, and before this table it was remembered
// in a process-local map, so every restart re-asked the Cover Art Archive for
// thousands of covers it had already said it did not have.
type ArtworkCacheEntry struct {
	// Key is the artwork cache key (entity_mbid_kind_size), which is also the
	// image's file name on disk.
	Key string `gorm:"primarykey" json:"key"`
	// Missing records a negative result: the providers have no such image. The row
	// then has no file on disk.
	Missing     bool      `json:"missing"`
	ContentType string    `json:"content_type"`
	FetchedAt   time.Time `json:"fetched_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// MusicBrainz entity migration kinds and the entity types they apply to.
//
// MusicBrainz entities are mutable: they get merged into one another and, more
// rarely, deleted outright. Every MBID Autotaggerr stores is a key into its own
// state, so an unnoticed migration leaves the app keyed on an ID the service no
// longer serves.
const (
	// MigrationKindRedirect: the service answered 200 for OldMBID but the payload
	// carried a different id — the entity was merged into NewMBID. Unambiguous:
	// MusicBrainz has told us exactly what replaced what.
	MigrationKindRedirect = "redirect"
	// MigrationKindDeleted: the service answered 404/410. Nothing replaces it, so
	// NewMBID is empty and the affected files go back to needing identification.
	MigrationKindDeleted = "deleted"

	// Only releases and artists are ever fetched by ID, so they are the only two
	// entities whose redirects MusicBrainz can show us. A release-group change
	// arrives inside a release payload instead, where it cannot be told apart from
	// the release simply moving between groups — so that case is re-linked for the
	// one release rather than remapped globally (see migration.RelinkRelease).
	MigrationEntityRelease = "release"
	MigrationEntityArtist  = "artist"

	// MigrationEntityReleaseGroup carries deletions only, and that asymmetry is not an
	// omission. A *merged* release-group still resolves — MusicBrainz answers 200 with
	// the surviving entity — so a merge never reaches an error path and the old ID goes
	// on working; only a group that resolves nowhere produces a signal. The signal comes
	// from the editions browse (modules.GetMusicBrainzReleaseGroupReleases), confirmed by
	// a direct lookup before anything is recorded.
	//
	// These rows are overwhelmingly not MusicBrainz's doing. A manager mirrors albums
	// into the collection keyed by whatever ID it holds, and an ID its own metadata
	// service has since dropped is indistinguishable here from one MusicBrainz deleted.
	// Both mean the same thing to Autotaggerr — the group cannot be read — which is why
	// they share a row type rather than being told apart on evidence nobody has.
	MigrationEntityReleaseGroup = "release_group"

	// Migration lifecycle. A migration is detected as pending, and either applied
	// (immediately when its category is not held for review, or later by hand) or
	// dismissed. Failed keeps a migration that could not be applied visible rather
	// than silently dropping it.
	MigrationStatusPending   = "pending"
	MigrationStatusApplied   = "applied"
	MigrationStatusDismissed = "dismissed"
	MigrationStatusFailed    = "failed"
)

// MusicbrainzMigration is one upstream identity change and what Autotaggerr did
// about it.
//
// It exists for two reasons. Durability: without a record, every sync re-learns the
// same redirect and re-asks the same question. Review: applying one rewrites MB IDs
// across library_items, the collection tables and authored desires, and a user may
// reasonably want to see a merge before it reshapes their collection — which is what
// the pending status is for.
//
// The unique index is on (entity_type, old_mb_id): an entity migrates to exactly one
// successor, and re-detecting the same move must not queue it twice.
type MusicbrainzMigration struct {
	Base
	EntityType string `gorm:"index:idx_mb_migration,unique;not null" json:"entity_type"`
	OldMBID    string `gorm:"index:idx_mb_migration,unique;not null" json:"old_mb_id"`
	// NewMBID is empty for a deletion — there is nothing to point at.
	NewMBID string `json:"new_mb_id"`
	Kind    string `gorm:"not null" json:"kind"`
	Status  string `gorm:"index;not null" json:"status"`

	// Name is a human label for the entity (release or artist title), captured at
	// detection. The review UI would otherwise show two bare UUIDs, and after the
	// migration is applied the old ID can no longer be looked up to name it.
	Name string `json:"name"`

	// Impact counts, recorded at detection so a pending row can say what approving
	// it would touch. They are a snapshot, not a promise — the apply path re-counts.
	AffectedFiles   int `json:"affected_files"`
	AffectedDesires int `json:"affected_desires"`
	// TouchesPinned marks a migration that would rewrite a manual correlation. It is
	// its own review category: a pinned MB ID is a choice a person made by hand.
	TouchesPinned bool `json:"touches_pinned"`

	DetectedAt time.Time  `json:"detected_at"`
	AppliedAt  *time.Time `json:"applied_at"`
	Error      string     `json:"error,omitempty"`

	// RepairAttemptedAt records that the manager holding this entity was asked to
	// refresh it (release-groups only; see collection.RepairGhostReleaseGroups). It
	// means "asked", not "worked" — set whatever the outcome, because a refresh that
	// failed or changed nothing must not be repeated on every pass.
	//
	// It is also what makes automatic retirement safe. Until a repair has been tried,
	// a dead release-group ID might be a mis-keyed album one refresh from being
	// correct, and removing it unattended would destroy that. Afterwards, the manager
	// has had its say.
	RepairAttemptedAt *time.Time `json:"repair_attempted_at,omitempty"`
}

// User backs authentication. Starts as a single auto-generated admin; structured
// so OAuth/OIDC and multi-user can be layered on later.
type User struct {
	Base
	Username     string `gorm:"uniqueIndex;not null" json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	Role         string `gorm:"not null;default:admin" json:"role"`
	APIKey       string `gorm:"uniqueIndex" json:"-"`

	// External identity, set when the account is linked to an auth provider. The
	// pair (AuthProviderID, ExternalSubject) is the stable identity — OIDC `sub` is
	// immutable, whereas email and username can be changed at the provider.
	AuthProviderID  *uuid.UUID `gorm:"index" json:"auth_provider_id,omitempty"`
	ExternalSubject string     `gorm:"index" json:"-"`
}

// Auth provider types.
const AuthProviderTypeOIDC = "oidc"

// AuthProvider is a configured external login method (OpenID Connect today).
// Password login is always available and is not represented here — this table only
// holds the federated options shown alongside it.
type AuthProvider struct {
	Base
	Name    string `gorm:"not null" json:"name"` // label on the login button
	Type    string `gorm:"not null" json:"type"`
	Enabled bool   `json:"enabled"`

	// Issuer is the OIDC discovery base URL (no /.well-known suffix).
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"-"`
	// Scopes is space-separated; empty means "openid profile email".
	Scopes string `json:"scopes"`
	// RedirectURL overrides the callback URL sent to the provider. Leave empty to
	// derive it from the incoming request, which is right unless a proxy rewrites
	// the path.
	RedirectURL string `json:"redirect_url"`

	// AllowSignup creates a local account on first successful login. With it off,
	// only users who already exist (matched by subject, or by verified email) can
	// sign in — the safer default for a private instance.
	AllowSignup bool   `json:"allow_signup"`
	DefaultRole string `json:"default_role"`
}

// Event is a record of something the app did — a scan, a Plex refresh, etc. It
// powers the Activity feed. Details is a type-specific JSON payload (e.g. a scan's
// counts and error files) shown when an event is opened.
type Event struct {
	Base
	Type       string         `gorm:"index;not null" json:"type"`
	Status     string         `gorm:"not null" json:"status"` // running | ok | error
	StartedAt  time.Time      `gorm:"index" json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at"`
	Title      string         `json:"title"`
	Summary    string         `json:"summary"`
	Details    map[string]any `gorm:"serializer:json" json:"details"`
	RefType    string         `json:"ref_type,omitempty"`
	RefID      *uuid.UUID     `gorm:"type:uuid" json:"ref_id,omitempty"`

	// ParentID groups a stage under the run that ran it. A processing run does six
	// distinct things — refresh metadata, walk and tag, re-tag what drifted, tell
	// Plex, apply identity changes, re-derive the collection — and reporting them as
	// one row meant five of them had nowhere to put their counters, so they went into
	// Details keys nothing rendered.
	//
	// Each stage is therefore its own event, and the run is the parent: it carries
	// what only it knows (the scope, what narrowed it, the overall outcome) while the
	// counters live on the stage that earned them. Not the same as RefType/RefID,
	// which point at the artist or library a run was *about*.
	//
	// Nil means top-level — a run, or a verb invoked on its own. Every row that
	// existed before this column is top-level, which is the correct reading of them.
	ParentID *uuid.UUID `gorm:"type:uuid;index" json:"parent_id,omitempty"`

	// Live progress for a running event, so the Activity feed can draw a bar rather
	// than an indefinite "running". Total/Done are the bar; Phase names the current
	// stage; Current is the thing being worked on right now (a library, an artist).
	// They are written on a throttled ticker (see events.StartProgress) — never per
	// item — and left in place on the finished row, where the feed simply stops
	// showing the bar. `current` is a reserved word in some SQL dialects, hence the
	// explicit column name.
	Total   int    `json:"total,omitempty"`
	Done    int    `json:"done,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Current string `gorm:"column:current_item" json:"current,omitempty"`

	// Stats are the counters this event wants shown, declared by the emitter rather
	// than looked up per type by the UI.
	//
	// The detail view used to be one hardcoded branch per event type, reading the
	// `Details` keys it happened to know about and dumping raw JSON for anything else
	// — so a new event type rendered as a blob until someone wrote it a branch, and
	// facts the emitter recorded but nobody wired up (which releases changed, how many
	// credits moved) stayed invisible. An emitter knows which of its numbers matter;
	// this is where it says so.
	//
	// Details keeps everything, including what is not worth a counter. These are the
	// few that are.
	Stats []EventStat `gorm:"serializer:json" json:"stats,omitempty"`

	// Items is the per-file detail (EventItem rows), attached by the single-event
	// endpoint only — never stored on this row and never loaded for the feed, where
	// 50 events would drag thousands of rows behind them.
	Items []EventItem `gorm:"-" json:"items,omitempty"`

	// Children are the stage events this run owns, oldest first so they read as the
	// order things happened in. Attached by the single-event endpoint, like Items.
	Children []Event `gorm:"-" json:"children,omitempty"`

	// ChildCount is how many activities this run spawned, filled in by the feed so a
	// run can offer to narrow the feed to its own cascade without first fetching it.
	ChildCount int `gorm:"-" json:"child_count,omitempty"`

	// ParentTitle names the run a stage belongs to. The feed is a flat chronological
	// list — every activity is its own row, at its own start time — so this is what
	// stops "Tagging" being unmoored, and it is filled in for every stage row rather
	// than only for a filtered feed.
	ParentTitle string `gorm:"-" json:"parent_title,omitempty"`
}

// EventStat is one counter on an event's detail view.
//
// Kind is *semantic emphasis*, not a colour: the emitter says whether a number is
// incidental, worth noticing, or bad, and the UI decides how that looks. An emitter
// naming a CSS variable would put the design system in the Go package that can least
// afford to know about it.
//
// Filter is what turns a number into a control. A count is almost always read as a
// prelude to "show me which ones", so a stat that names an EventItem.Status becomes a
// chip over the detail list rather than a figure sitting above an unrelated table.
// Empty means the number has no rows behind it and stays a plain figure.
type EventStat struct {
	Label  string `json:"label"`
	Value  int    `json:"value"`
	Kind   string `json:"kind,omitempty"`
	Filter string `json:"filter,omitempty"`
}

// Emphasis for an EventStat. The default (empty) is an ordinary figure.
const (
	EventStatMuted   = "muted"   // incidental — the unchanged majority, the already-cached
	EventStatNotable = "notable" // the thing that actually happened
	EventStatBad     = "bad"     // failures; rendered as danger only when non-zero
)

// TagChange is one field's before/after from a tag write. Old is the file's previous
// value (empty when the field was absent), New the value written. Stored as JSON on
// the EventItem that owns it: a diff is only ever read together with its file, so it
// needs no table of its own.
type TagChange struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// EventItem is one file's outcome within an Event — the per-file detail behind a
// scan's counters: which file, what happened, and the exact fields that changed.
//
// It is a child table rather than more JSON in Event.Details for one reason: a large
// library produces tens of thousands of per-file results, and a single serialized
// blob per scan would have to be written and read whole. Rows also let retention
// cascade (see events.Prune) instead of growing an event row without bound.
//
// Only *interesting* files get a row — changed or failed. Recording the unchanged
// majority would multiply the table by the size of the library to say "nothing
// happened", which the counters already say.
type EventItem struct {
	Base
	EventID uuid.UUID `gorm:"type:uuid;index;not null" json:"event_id"`
	// Path is a file path, or — when Kind is EventItemKindEntity — the MBID of the
	// thing the row is about.
	Path string `json:"path"`
	// Kind says what this row describes, so the reader does not have to infer it from
	// the shape of the other fields. Empty means a file, which is what every row
	// written before metadata passes recorded any was.
	//
	// It matters because the two render as different things and one of them would
	// otherwise lie: a file row reports how many tags were written to it, and a
	// metadata refresh writes none — "0 tags written" beside a release MBID reads as a
	// claim about the user's audio, from the one verb that promises not to touch it.
	Kind string `json:"kind,omitempty"`
	// Status is EventItemStatus*: what happened to this one file or entity.
	//
	// Indexed for the query the detail modal does not need — it filters the ≤500 rows
	// one event holds in the browser — but any cross-run "every failed file" view does,
	// since that one selects on status alone across the whole table.
	Status string `gorm:"index" json:"status"`
	// Phase attributes the row to a stage of the run (EventItemPhase*), so the feed
	// can group, say, releases refreshed upstream apart from files changed by the
	// scan walk. Empty for the ordinary per-file scan row, which needs no qualifier.
	Phase string `json:"phase,omitempty"`
	// TagsWritten is the writer's own count. It can exceed len(Changes) for MP3s,
	// where one changed field forces its paired composite fields to be rewritten too.
	// On a "refreshed" row it instead carries how many of the release's files the
	// drift stage re-tagged as a result of the upstream change.
	TagsWritten int    `json:"tags_written"`
	Error       string `json:"error,omitempty"`
	// Changes is the field-level diff, empty for a failure that never got as far as
	// writing.
	Changes []TagChange `gorm:"serializer:json" json:"changes,omitempty"`

	// Related is what Autotaggerr itself knows about the MBID on an entity row: the
	// name it goes by, the artist it belongs to, and how many indexed files point at
	// it. Resolved on the single-event fetch, never stored — the row records what
	// happened at MusicBrainz, and what the collection holds is a fact about *now*
	// that would go stale the moment a file moved.
	//
	// It exists because the row's own identifier is a UUID. "404 on
	// 019fa765-…-c389e527ed21" is not a thing anyone can act on; "404 on OK Computer,
	// 12 files on disk" is.
	Related *EntityRef `gorm:"-" json:"related,omitempty"`
}

// EntityRef is the local side of a MusicBrainz identifier: what the collection calls
// it, where it sits, and how much of the library depends on it.
//
// ArtistMBID and GroupMBID are the SPA's own route parameters, so a row can link
// through to the artist or album page rather than only out to musicbrainz.org — the
// point of naming the entity is being able to go and look at it.
type EntityRef struct {
	// Kind is the MusicBrainz entity type (EntityKind*), which also names the path
	// segment on musicbrainz.org.
	Kind string `json:"kind"`
	// Name is the title of a release or release-group, or an artist's name. Empty when
	// the collection has no row for the MBID at all — which is itself an answer, and
	// the reason this is a pointer on the item rather than a bare string.
	Name string `json:"name,omitempty"`
	// Artist is who it is by, blank on an artist row where Name already says so.
	Artist     string `json:"artist,omitempty"`
	ArtistMBID string `json:"artist_mb_id,omitempty"`
	GroupMBID  string `json:"group_mb_id,omitempty"`
	// Files is how many indexed files point at this MBID. Only ever non-zero for a
	// release: a file is correlated to a release, and everything above that is reached
	// through it.
	Files int `json:"files"`
}

// MusicBrainz entity kinds, as both a label key and the path segment an MBID sits
// under on musicbrainz.org.
const (
	EntityKindArtist       = "artist"
	EntityKindReleaseGroup = "release-group"
	EntityKindRelease      = "release"
)

// Per-file outcomes inside an event.
const (
	EventItemStatusChanged = "changed"
	EventItemStatusError   = "error"
	// EventItemStatusRefreshed is a release (not a file) whose MusicBrainz metadata
	// changed upstream during a scan's refresh stage. It is not a file outcome, so it
	// carries no tag diff — its TagsWritten is the count of the release's files the
	// drift stage re-tagged in response.
	//
	// It is also the success outcome of an EventItemKindAlbum row: Plex accepted the
	// refresh. One word for "this thing was re-read from its source" rather than a
	// second status meaning the same thing on a different kind of row.
	EventItemStatusRefreshed = "refreshed"
	// EventItemStatusGone is an entity MusicBrainz no longer has. It is an answer
	// rather than a failure — the migration row was recorded at the point of the 404 —
	// so it is kept apart from EventItemStatusError, which means "we could not look".
	EventItemStatusGone = "gone"
	// EventItemStatusRelinked is a release that moved to a different release-group
	// upstream. Its own content may be unchanged; what moved is where it belongs.
	EventItemStatusRelinked = "relinked"
	// EventItemStatusUnknown is a thing the authority does not have: an artist the
	// collection files under Lidarr that Lidarr never listed. Distinct from Gone (the
	// source used to have it and says so) and from Error (we could not ask) — this is
	// a complete answer that happens to be "no".
	EventItemStatusUnknown = "unknown"
)

// What an EventItem describes. Empty (EventItemKindFile) is the default and covers
// every row written before metadata passes recorded any.
const (
	EventItemKindFile   = ""
	EventItemKindEntity = "entity"
	// EventItemKindAlbum is an album a Plex refresh was asked for. Neither a file nor
	// a MusicBrainz entity: the identifier is the album title Plex knows it by, and
	// the outcome is whether Plex accepted the request — no tags were written and no
	// MBID was read, so both of the other renderings would say something untrue.
	EventItemKindAlbum = "album"
)

// Stage of a run a detail row belongs to. Empty means the ordinary scan-walk file row.
//
// A metadata pass attributes its rows with its own phase names (artists,
// discographies, editions, releases — the mirror.Phase* constants) rather than these,
// because those name the four kinds of entity a pass reads and this names a stage of a
// processing run. Both are free-form strings on the row; the reader groups by whatever
// it finds.
const (
	EventItemPhaseRefresh = "refresh"
	// EventItemPhaseDrift is a file rewritten because its release changed upstream,
	// as opposed to one the walk found changed on disk. Both are written by the same
	// tagging event, and the phase is what keeps the two halves of it apart in the
	// detail list — they answer different questions about the same run.
	EventItemPhaseDrift = "drift"
)

// CollectionArtist is a MusicBrainz artist present in (or monitored for) the
// collection. Monitored artists get their full discography synced so missing
// release-groups can be listed. ManagedBy records whether the artist's files live
// under an Autotaggerr or Lidarr manager (or both), which governs the wanted view.
// (Named Collection* to avoid clashing with the MusicBrainz response types.)
type CollectionArtist struct {
	Base
	MBID      string `gorm:"uniqueIndex;not null" json:"mb_id"`
	Name      string `json:"name"`
	Monitored bool   `json:"monitored"`
	ManagedBy string `json:"managed_by"`
	// Origin distinguishes artists discovered from files on disk (rebuilt, and
	// re-derived on every scan) from artists a user added by hand. A manually added
	// artist has no files yet, so without this it would look like an anomaly and
	// Rebuild could not tell "not owned yet" from "stale row".
	Origin string `json:"origin"`

	// ManagerDetached records that the user took authority over this artist back from
	// the manager of the library its files sit in. It is stored rather than derived
	// because ManagedBy is *re-derived from the library's manager* on every Rebuild —
	// a detach that only wrote ManagedBy would be silently reverted by the next scan.
	//
	// While set, ManagedBy is held at ManagedByAutotaggerr however the library is
	// configured, which is what makes SyncLidarr and reconcileManagerDesires skip the
	// artist. See collection.DetachArtist.
	ManagerDetached bool `json:"manager_detached"`

	// FollowTypes is the comma-separated set of MusicBrainz primary types that
	// following this artist auto-wants, e.g. "Album,EP". Empty means the default
	// (album + EP). Following always includes releases that appear later — that is
	// what distinguishes it from picking albums by hand.
	FollowTypes string `json:"follow_types"`
	// FollowSecondary lets live albums, compilations, remixes and soundtracks count.
	// Off by default: including them buries the missing list under reissues.
	FollowSecondary bool `json:"follow_secondary"`
	// FollowFromYear is the earliest release year a follow wants, or 0 for no cutoff
	// (the whole back catalogue, which is what following meant before this existed).
	//
	// A *year* rather than a date, because that is the granularity MusicBrainz release
	// dates actually have: FirstReleaseDate is `YYYY`, `YYYY-MM` or `YYYY-MM-DD`
	// depending on what an editor knew, so a day-precision cutoff would be answering a
	// question the data cannot be asked. Setting it to the current year is the "only
	// releases from here on" case that following a new artist usually means; setting it
	// to 2010 is "I have the old stuff already", which the same field expresses for free.
	FollowFromYear int `json:"follow_from_year"`

	LastSyncedAt *time.Time `json:"last_synced_at"`
}

// DefaultFollowTypes is what following an artist wants when nothing is configured.
const DefaultFollowTypes = "Album,EP"

// VariousArtistsMBID is MusicBrainz's special-purpose "Various Artists" artist —
// the placeholder every compilation with no single album-artist is credited to. It
// is shared across the whole database, so its "discography" is hundreds of thousands
// of release-groups. Nothing about pulling that catalogue is useful: it is not a real
// artist to follow, and a full fetch is unbounded work. Callers use IsVariousArtists
// to short-circuit any discography pull for it.
const VariousArtistsMBID = "89ad4ac3-39f7-470e-963a-56509c546377"

// IsVariousArtists reports whether an MBID is the shared "Various Artists" placeholder.
func IsVariousArtists(mbID string) bool {
	return strings.EqualFold(strings.TrimSpace(mbID), VariousArtistsMBID)
}

// Collection artist origins.
const (
	CollectionOriginLibrary = "library" // materialised from files on disk
	CollectionOriginManual  = "manual"  // added by a user who owns none of it yet
)

// CollectionDesire is *authored* intent: something the user asked for. It is kept
// in its own table rather than as flags on CollectionReleaseGroup because desire is
// sparse and human-entered while ownership is derived and rebuilt on every scan —
// mixing the two is what let a scan silently wipe the Lidarr mirror in M5.
//
// One row expresses all five wanted-cases (see docs/wip.md, M6 desire model):
//
//	release_mb_id empty  -> any release of the group will do (the default)
//	release_mb_id set    -> only that edition satisfies it
//	recordings empty     -> the whole thing
//	recordings non-empty -> only those songs
//
// Songs are identified by *recording* MBID, not release-scoped track MBID, so a
// song desire survives without a release having been chosen. Recordings are unused
// until M6 pass C; pass B writes album-level desires only.
type CollectionDesire struct {
	Base
	ArtistMBID       string `gorm:"index;not null" json:"artist_mb_id"`
	ReleaseGroupMBID string `gorm:"index;not null" json:"release_group_mb_id"`
	ReleaseMBID      string `json:"release_mb_id"`
	// RecordingMBIDs is the desired-song set; empty means the whole release-group
	// or release. Stored as JSON so it needs no join table.
	RecordingMBIDs []string `gorm:"serializer:json" json:"recording_mb_ids"`
	// Source is who authored this want, and therefore who may rewrite it. Only
	// DesireSourceManual is the user's; the other two are maintained by a
	// reconciliation pass that may re-point or prune its *own* rows and must never
	// touch a hand-authored one. Empty is read as manual — see BackfillDesireSources.
	Source string `json:"source"`
}

// Desire provenance. The distinction exists because "never recomputed" is a
// guarantee about *authored* intent: a derived row has to be re-pointed as the
// thing it derives from moves, and the only way to keep both properties is to know
// which kind a row is.
const (
	// DesireSourceManual: the user asked for this. Never recomputed, outranks
	// everything derived, and survives unfollowing or a manager change.
	DesireSourceManual = "manual"
	// DesireSourceAuto: narrowed from an "any edition" want to the edition whose
	// files actually landed (native artists only). Re-pointed when the files change
	// edition, pruned when they go. See collection.reconcileAutoDesires.
	DesireSourceAuto = "auto"
	// DesireSourceManager: mirrored from the library manager's own selection — for
	// Lidarr, the monitored release of a monitored album. Lidarr owns identity for
	// its artists, so this is that decision recorded as Autotaggerr's own row rather
	// than read through the catalog columns, which is what lets the manager be
	// detached later without losing what it decided. See
	// collection.reconcileManagerDesires.
	DesireSourceManager = "manager"
)

// Derived reports whether a reconciliation pass owns this row rather than the user.
func (d CollectionDesire) Derived() bool {
	return d.Source == DesireSourceAuto || d.Source == DesireSourceManager
}

// CollectionReleaseGroupArtist links a release-group to every artist credited on it.
//
// It exists because a release-group can have more than one credited artist and
// CollectionReleaseGroup can only name one. Before this, a collaboration belonged to
// whichever sync wrote last — Rebuild claimed it for the *first* credited artist, the
// discography sync for whichever artist it was syncing — so the album appeared on one
// artist's page and vanished from the other's, flipping between them as syncs ran.
//
// The link table is additive: a row is added when an artist is seen to be credited and
// is not removed by a writer that simply does not know about it. CollectionReleaseGroup
// .ArtistMBID survives as the *primary* credit, for display and sorting.
type CollectionReleaseGroupArtist struct {
	Base
	ReleaseGroupMBID string `gorm:"index:idx_rg_artist,unique;not null" json:"release_group_mb_id"`
	ArtistMBID       string `gorm:"index:idx_rg_artist,unique;index;not null" json:"artist_mb_id"`
	// Position is the artist's place in the MusicBrainz artist credit (0 = primary).
	// It keeps "featuring" artists from being presented as the album's author.
	Position int `json:"position"`

	// FromDisk and FromCatalog record *which authority* put this link here, so the
	// one writer allowed to remove a link can tell whose claim it would be removing.
	// Both can be true — an album the files and the manager agree on — which is why
	// this is two flags rather than one source column: the row is uniquely indexed on
	// (release_group, artist), so a single value would have to pick a winner.
	//
	// FromDisk is set by collection.Rebuild, the only writer that reads a release's
	// real artist credit. FromCatalog is set by the manager mirrors (SyncLidarr) and
	// by native discography discovery (SyncArtist), which know only that *their*
	// artist is credited somehow.
	//
	// A row with neither predates the columns. It is treated as a disk claim, because
	// that is what makes a stale credit from before this existed cleanable at all; a
	// legacy catalog link caught by that reading is restored by the next sync.
	FromDisk    bool `json:"from_disk"`
	FromCatalog bool `json:"from_catalog"`
}

// CollectionReleaseGroup is an album/EP/etc for an artist.
//
// It carries two independently-written views of the same album, because the two
// authorities genuinely disagree and each is right about a different thing:
//
//   - The *disk* block is what Autotaggerr walked (collection.Rebuild). Autotaggerr
//     is authoritative here — it opened the files.
//   - The *catalog* block is what the manager says should exist (collection.SyncLidarr,
//     or native discography discovery). The manager is authoritative about what
//     *ought* to be there, and about monitoring, but not about what is on disk:
//     Lidarr's track-file counts are a cached scan result and go stale.
//
// Keeping them in separate columns means a mismatch (Lidarr reporting 3/21 for an
// album where every file is present, or files with no Lidarr album at all) surfaces
// as a reviewable discrepancy instead of the two syncs overwriting each other.
type CollectionReleaseGroup struct {
	Base
	MBID             string `gorm:"uniqueIndex;not null" json:"mb_id"`
	ArtistMBID       string `gorm:"index;not null" json:"artist_mb_id"`
	Title            string `json:"title"`
	PrimaryType      string `json:"primary_type"`
	SecondaryTypes   string `json:"secondary_types"`
	FirstReleaseDate string `json:"first_release_date"`

	// Disk state — written only by collection.Rebuild. Owned = at least one file
	// present. OwnedTracks files present out of TotalTracks on the best-owned
	// edition; complete when OwnedTracks >= TotalTracks > 0.
	Owned       bool `json:"owned"`
	OwnedTracks int  `json:"owned_tracks"`
	TotalTracks int  `json:"total_tracks"`

	// Catalog state — written only by the manager mirror. InCatalog = the manager
	// knows this release-group exists. Catalog track counts are the manager's own
	// have/total (0 total means "unknown", e.g. native MB discovery, which does not
	// fetch each release just to count tracks).
	InCatalog          bool `json:"in_catalog"`
	CatalogOwnedTracks int  `json:"catalog_owned_tracks"`
	CatalogTotalTracks int  `json:"catalog_total_tracks"`
	CatalogMonitored   bool `json:"catalog_monitored"`
	// CatalogReleaseMBID is the edition the manager selected — Lidarr's monitored
	// release. Empty when the manager did not say (native discovery, or an album
	// Lidarr has no monitored release for). It is stored rather than only read
	// through so the two selections can be compared: an album whose files sit on a
	// different release than the manager monitors is exactly the divergence force
	// re-correlate exists to fix, and it was previously invisible until a file
	// failed to tag.
	CatalogReleaseMBID string `json:"catalog_release_mb_id"`
}

// AcoustIDLookup caches one file's fingerprint and the candidates AcoustID
// returned for it.
//
// Fingerprinting decodes the whole audio file, so re-doing it per scan is not
// viable — this table is what makes the feature affordable at all. The cache is
// keyed by path and invalidated by size/mtime, the same identity rule the scan
// uses to skip unchanged files, so re-encoding a file re-fingerprints it.
type AcoustIDLookup struct {
	Path    string     `gorm:"primarykey" json:"path"`
	Size    int64      `json:"size"`
	ModTime *time.Time `json:"mod_time"`

	// Fingerprint and Duration are what fpcalc produced; keeping them means a
	// changed API key or a failed lookup does not cost another full decode.
	Fingerprint string `gorm:"type:text" json:"-"`
	Duration    int    `json:"duration"`

	// Candidates is the serialized lookup response, empty when AcoustID knew
	// nothing about the fingerprint. LookedUpAt is nil when only the fingerprint
	// has been computed so far.
	Candidates string     `gorm:"type:text" json:"-"`
	LookedUpAt *time.Time `json:"looked_up_at"`
	FetchedAt  time.Time  `json:"fetched_at"`
}

// CollectionRelease is one *edition* you own files of, under a release-group.
//
// It exists because collapsing a release-group to its best-owned edition throws
// away the question people actually ask about a reissued album: owning 5 tracks of
// the 1977 original and 7 of the 2017 remaster is two partial editions, not one
// album that is 12/17 complete. The release-group keeps its best-edition summary
// (that is the useful headline); this table is the detail behind it.
//
// It is pure disk state, written only by collection.Rebuild and pruned by it — a
// row exists exactly while at least one file resolves to that release. Desires
// reference releases by MBID, never by this row's ID, so rebuilding can never
// disturb authored intent.
type CollectionRelease struct {
	Base
	MBID             string `gorm:"uniqueIndex;not null" json:"mb_id"`
	ReleaseGroupMBID string `gorm:"index;not null" json:"release_group_mb_id"`
	ArtistMBID       string `gorm:"index" json:"artist_mb_id"`

	Title          string `json:"title"`
	Date           string `json:"date"`
	Country        string `json:"country"`
	Disambiguation string `json:"disambiguation"`
	// Format summarises the media, e.g. "2×CD" — what actually distinguishes one
	// edition from another in a list.
	Format string `json:"format"`

	OwnedTracks int `json:"owned_tracks"`
	TotalTracks int `json:"total_tracks"`
}

// Complete reports whether every track of this edition is on disk.
func (r CollectionRelease) Complete() bool {
	return r.TotalTracks > 0 && r.OwnedTracks >= r.TotalTracks
}

// Discrepancy classifications between the disk and catalog views.
const (
	DiscrepancyNone = ""
	// DiscrepancyUnmapped: files on disk the manager has no album for at all.
	DiscrepancyUnmapped = "unmapped"
	// DiscrepancyStaleCatalog: more files on disk than the manager thinks — the
	// manager needs a rescan (its counts lag until it re-reads the folder).
	DiscrepancyStaleCatalog = "stale_catalog"
	// DiscrepancyNotIndexed: the manager has more files than Autotaggerr indexed —
	// files outside the configured libraries, or not scanned yet.
	DiscrepancyNotIndexed = "not_indexed"
	// DiscrepancyNoEdition: the manager counted tracks for this album but has no
	// edition selected, so its counts describe an edition nobody chose. Reported
	// ahead of the count comparisons because it explains *why* they disagree — see
	// Discrepancy.
	DiscrepancyNoEdition = "no_edition"
)

// Complete reports whether every track of the best-owned edition is on disk.
func (rg CollectionReleaseGroup) Complete() bool {
	return rg.Owned && rg.TotalTracks > 0 && rg.OwnedTracks >= rg.TotalTracks
}

// Discrepancy compares the disk view against the catalog view. catalogChecked reports
// whether a manager has actually been *asked* about this album's artist — see
// collection.CatalogChecked, which reads the artist's LastSyncedAt. Without an answer
// there is nothing to compare against, so nothing is flagged; otherwise every album of
// an unmonitored native artist would look unmapped, and an album whose artist was
// never put to the manager would be reported as missing from a catalogue nobody
// consulted.
func (rg CollectionReleaseGroup) Discrepancy(catalogChecked bool) string {
	if !catalogChecked {
		return DiscrepancyNone
	}
	if rg.Owned && !rg.InCatalog {
		return DiscrepancyUnmapped
	}
	if !rg.InCatalog || rg.CatalogTotalTracks == 0 {
		return DiscrepancyNone
	}
	if rg.OwnedTracks == rg.CatalogOwnedTracks {
		return DiscrepancyNone
	}
	// The counts disagree. If the manager named no edition, that is the explanation:
	// Lidarr picks one release per album and its statistics describe that release,
	// but with none selected the counts still arrive, computed against an edition
	// nobody chose — a 7-track edition against a 44-track box set. Saying "stale
	// catalog" here sends the user to rescan something that will report the same
	// numbers again, and the actual fix (pick a release in Lidarr) is elsewhere.
	//
	// It explains a disagreement rather than raising one on its own, deliberately:
	// this column is also empty on rows written before it existed, and on a manager
	// that does not report editions. Those rows agree with the disk and must stay
	// silent until their next sync fills the column in.
	if rg.CatalogReleaseMBID == "" {
		return DiscrepancyNoEdition
	}
	if rg.OwnedTracks > rg.CatalogOwnedTracks {
		return DiscrepancyStaleCatalog
	}
	return DiscrepancyNotIndexed
}

// AllDBModels is the AutoMigrate set — keep in sync when adding tables.
func AllDBModels() []any {
	return []any{
		&DataSource{},
		&Manager{},
		&TaggerProfile{},
		&Library{},
		&LibraryItem{},
		&MusicbrainzReleaseCache{},
		&MusicbrainzEntityCache{},
		&ProviderCache{},
		&ArtworkCacheEntry{},
		&MusicbrainzMigration{},
		&User{},
		&AuthProvider{},
		&Event{},
		&EventItem{},
		&CollectionArtist{},
		&CollectionReleaseGroup{},
		&CollectionReleaseGroupArtist{},
		&CollectionRelease{},
		&CollectionDesire{},
		&AcoustIDLookup{},
	}
}
