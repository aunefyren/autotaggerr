package models

import (
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

	EventTypeScan        = "scan"
	EventTypeDriftSync   = "drift_sync"
	EventTypeLidarrSync  = "lidarr_sync"
	EventTypePlexRefresh = "plex_refresh"

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
	Pinned bool   `json:"pinned"`
	Status string `json:"status"`
	Error  string `json:"error"`
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
	// TagsWritten is the writer's own count. It can exceed len(Changes) for MP3s,
	// where one changed field forces its paired composite fields to be rewritten too.
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
)

// Complete reports whether every track of the best-owned edition is on disk.
func (rg CollectionReleaseGroup) Complete() bool {
	return rg.Owned && rg.TotalTracks > 0 && rg.OwnedTracks >= rg.TotalTracks
}

// Discrepancy compares the disk view against the catalog view. hasCatalog reports
// whether the artist has a catalog at all — a manager mirror has run, or the artist
// is monitored. Without one there is nothing to compare against, so nothing is
// flagged; otherwise every album of an unmonitored native artist would look unmapped.
func (rg CollectionReleaseGroup) Discrepancy(hasCatalog bool) string {
	if !hasCatalog {
		return DiscrepancyNone
	}
	if rg.Owned && !rg.InCatalog {
		return DiscrepancyUnmapped
	}
	if !rg.InCatalog || rg.CatalogTotalTracks == 0 {
		return DiscrepancyNone
	}
	switch {
	case rg.OwnedTracks > rg.CatalogOwnedTracks:
		return DiscrepancyStaleCatalog
	case rg.CatalogOwnedTracks > rg.OwnedTracks:
		return DiscrepancyNotIndexed
	}
	return DiscrepancyNone
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
