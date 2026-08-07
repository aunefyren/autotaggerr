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
	// of the collection from the index, which records no event of its own. Rows
	// written under the old value are rewritten once at startup by
	// events.MigrateLegacyTypes.
	EventTypeProcess    = "process"
	EventTypeLegacyScan = "scan"
	// EventTypeTagFiles is the Tag files verb: rewrite indexed files from what is
	// already known, walking nothing and fetching nothing.
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
	EventTypeHealth      = "health_check"

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
	// Off by default; see ConfigStruct.AutotaggerrMP3MultiValueTags for why it is a
	// choice at all rather than simply correct.
	MP3MultiValueTags bool `json:"mp3_multi_value_tags"`
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
	Status string `json:"status"`
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

	// Items is the per-file detail (EventItem rows), attached by the single-event
	// endpoint only — never stored on this row and never loaded for the feed, where
	// 50 events would drag thousands of rows behind them.
	Items []EventItem `gorm:"-" json:"items,omitempty"`
}

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
	Path    string    `json:"path"`
	// Status is EventItemStatus*: what happened to this one file.
	Status string `json:"status"`
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
}

// Per-file outcomes inside an event.
const (
	EventItemStatusChanged = "changed"
	EventItemStatusError   = "error"
	// EventItemStatusRefreshed is a release (not a file) whose MusicBrainz metadata
	// changed upstream during a scan's refresh stage. It is not a file outcome, so it
	// carries no tag diff — its TagsWritten is the count of the release's files the
	// drift stage re-tagged in response.
	EventItemStatusRefreshed = "refreshed"
)

// Stage of a run a detail row belongs to. Empty means the ordinary scan-walk file row.
const (
	EventItemPhaseRefresh = "refresh"
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
