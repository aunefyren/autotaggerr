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
	Name      string  `gorm:"uniqueIndex;not null" json:"name"`
	Type      string  `gorm:"not null" json:"type"`
	BaseURL   string  `json:"base_url"`
	Contact   string  `json:"contact"`
	RateLimit float64 `json:"rate_limit"` // requests per second
	// No gorm default on bool fields: GORM omits a Go zero value (false) from the
	// INSERT when a column default is set, so a user-chosen false would be silently
	// overridden by the default. Callers set these explicitly instead.
	Enabled     bool       `json:"enabled"`
	Health      string     `json:"health"`
	LastChecked *time.Time `json:"last_checked"`
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

	CorrelationSource string     `json:"correlation_source"`
	CorrelatedAt      *time.Time `json:"correlated_at"`
	LastScannedAt     *time.Time `json:"last_scanned_at"`
	LastTaggedAt      *time.Time `json:"last_tagged_at"`
	TagStateHash      string     `json:"tag_state_hash"`
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
}

// CollectionArtist is a MusicBrainz artist present in (or monitored for) the
// collection. Monitored artists get their full discography synced so missing
// release-groups can be listed. ManagedBy records whether the artist's files live
// under an Autotaggerr or Lidarr manager (or both), which governs the wanted view.
// (Named Collection* to avoid clashing with the MusicBrainz response types.)
type CollectionArtist struct {
	Base
	MBID         string     `gorm:"uniqueIndex;not null" json:"mb_id"`
	Name         string     `json:"name"`
	Monitored    bool       `json:"monitored"`
	ManagedBy    string     `json:"managed_by"`
	LastSyncedAt *time.Time `json:"last_synced_at"`
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
		&CollectionArtist{},
		&CollectionReleaseGroup{},
	}
}
