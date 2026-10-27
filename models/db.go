package models

import (
	"crypto/sha256"
	"encoding/hex"
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
	// CorrelationSourceRematched is a file whose track ID had gone from its release
	// (MusicBrainz rebuilt the tracklist under the same release ID) and was matched
	// to the rebuilt track by recording, or by disc and position with the title
	// agreeing (modules.rematchRebuiltTrack). Its own source so a re-match can be
	// found and checked afterwards.
	CorrelationSourceRematched = "rematched"

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
	// EventTypeDiscoverFiles is a Scan asked to walk the disk as well: find files the
	// index does not hold, carry moved files' identities to their new paths, and ask the
	// manager about the rest — writing no audio file. It is the parent of the ordinary
	// collection scan that follows it, which is the same verb doing its usual work on
	// what the walk just recorded.
	EventTypeDiscoverFiles = "discover_files"
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
	// EventTypeTempo is a pass of the *Analyse tempo* verb: BPM detection over the
	// files of the libraries that opted in. It reads audio and writes only the
	// track_tempos table — the tags are written by the next tagging pass.
	EventTypeTempo  = "tempo_analysis"
	EventTypeHealth = "health_check"
	// EventTypeLoudness is a pass of the *Analyse loudness* verb: ReplayGain measured
	// per album over the files of the libraries that opted in. Like tempo, it writes
	// only track_loudnesses; the tags are written by the next tagging pass.
	EventTypeLoudness = "loudness_analysis"
	// EventTypeLidarrFollow is a pass over Lidarr's history that found files Lidarr
	// imported, deleted or renamed since the last one: the artists it named had their
	// cached Lidarr answers dropped and a processing run queued. Recorded only when
	// there was something to follow — the pass runs every few minutes.
	EventTypeLidarrFollow = "lidarr_follow"
	// EventTypeFileImport is a file import: loose files identified by hand, moved or
	// copied into a library at the path its naming templates give them, pinned and
	// tagged. Its items carry where each file came from (EventItem.From).
	EventTypeFileImport = "file_import"
	// EventTypeDiscographyRead is a pass reading artists' MusicBrainz discographies:
	// every artist's catalogue, so coverage is measured against what they released
	// rather than what happens to be stored, and the discography sync of artists just
	// followed. Writes no file.
	EventTypeDiscographyRead = "discography_read"
	// EventTypeDownloadRemoved is imported downloads removed from their client, files
	// and all, once it stopped seeding them (DownloadClient.RemoveAfterSeeding). The one
	// place Autotaggerr deletes files it did not put somewhere, so each is listed.
	EventTypeDownloadRemoved = "download_removed"
	// EventTypeOrganise is files moved to where their library's naming templates put
	// them, after a review. Its items carry where each came from (EventItem.From).
	EventTypeOrganise = "organise"

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

// DataSourceLabel is how a source type is written in a sentence. The type strings are
// lowercase keys and none of them is spelled the way the service spells itself, so a
// UI printing them raw says "musicbrainz" and "coverartarchive" in prose.
//
// An unknown type is returned unchanged rather than blanked: a source this build does
// not know about is still better named badly than not at all.
// MigrationSourceManual is the source of an identity change a person recorded by hand.
const MigrationSourceManual = "manual"

func DataSourceLabel(sourceType string) string {
	switch sourceType {
	case DataSourceTypeMusicBrainz:
		return "MusicBrainz"
	case DataSourceTypeAcoustID:
		return "AcoustID"
	case DataSourceTypeCoverArtArchive:
		return "Cover Art Archive"
	case DataSourceTypeFanart:
		return "fanart.tv"
	// Not a data source, but a migration's source can be a manager: Lidarr re-keying
	// an album is reported the same way MusicBrainz merging one is.
	case ManagerTypeLidarr:
		return "Lidarr"
	// Nor a person, who can say two album rows are one (collection.MarkSameAlbum).
	case MigrationSourceManual:
		return "You"
	}
	return sourceType
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
	// LidarrWebURL is where a browser reaches this Lidarr, for links into it. Empty
	// means LidarrBaseURL, which is right unless the API address is one only the
	// server can reach — a Docker service name, say.
	LidarrWebURL string `json:"lidarr_web_url,omitempty"`

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

	// LidarrHistoryCursor is how far this manager's history has been followed: every
	// file change Lidarr recorded up to it has been acted on (see
	// process.Runner.FollowLidarr). Nil until the first pass, which starts following
	// from that moment rather than replaying everything Lidarr ever did. Persisted
	// so that what Lidarr upgraded while Autotaggerr was down is still caught up on.
	LidarrHistoryCursor *time.Time `json:"lidarr_history_cursor,omitempty"`

	// DefaultDataSourceID is stored and never read: MusicBrainz is one process-wide
	// client (a fixed base URL, the rate from the enabled source). Kept so existing rows
	// load; nothing offers it for editing. See docs/media-manager.md.
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
	// SingleRecordLabel writes only the first of a release's labels; see
	// TaggerSettings.SingleRecordLabel.
	SingleRecordLabel bool `json:"single_record_label"`
	// MusicBrainzReleaseType, IgnoreRemixEditions and OriginalDateAsDate: see
	// TaggerSettings. The first two are stored the other way up from how the form
	// shows them ("Write release types the way Plex reads them", "Mark remix editions
	// as remixes"), so a row predating them keeps the behaviour it always had.
	MusicBrainzReleaseType bool `json:"musicbrainz_release_type"`
	IgnoreRemixEditions    bool `json:"ignore_remix_editions"`
	OriginalDateAsDate     bool `json:"original_date_as_date"`
	// The naming templates: where a file belongs under its library root, one template
	// per path level. Empty means the default (see NamingTemplates), so a profile row
	// predating the columns names files the way the layout convention always has.
	NamingArtistFolder string `json:"naming_artist_folder"`
	NamingAlbumFolder  string `json:"naming_album_folder"`
	NamingMediaFolder  string `json:"naming_media_folder"`
	NamingTrackFile    string `json:"naming_track_file"`
	// The BPM settings used to live here and moved to Library, beside the analysis
	// opt-in they depend on. Their old columns (bpm_write, bpm_min_confidence) are
	// left in place by AutoMigrate and read once by the move_bpm_to_library data
	// migration.
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
	// SingleRecordLabel writes only the first label MusicBrainz lists for a release,
	// in FLAC and MP3 alike. Off (the default) writes every label.
	//
	// Plex's Record Label is one value: given several, ffmpeg hands it a joined string
	// ("APESHIT;EMPIRE;Mass Appeal") that it shows, and groups by, as one label no other
	// album shares. "First" is MusicBrainz's order, which is the order editors entered
	// the labels in — not alphabetical, and usually the label the release is known by.
	// Catalogue numbers stay multi-valued: Plex does not show them.
	//
	// omitempty keeps it out of Tagger.Fingerprint while off, so adding the field did
	// not change every existing profile's fingerprint and re-process every file.
	SingleRecordLabel bool `json:",omitempty"`
	// MusicBrainzReleaseType writes the release type as MusicBrainz states it: the
	// primary type and every secondary type, as separate values ("ep", "live"), as
	// Picard does. Off (the default) writes one Plex-shaped value instead — "album",
	// "ep", "single", or "album; <first secondary>" for a soundtrack, compilation,
	// remix, live or demo release whatever its primary type — which is the form Plex
	// sorts into its Live, Compilations and Soundtracks shelves; it does not read
	// "ep; live" (see modules.ReleaseTypes).
	MusicBrainzReleaseType bool `json:",omitempty"`
	// IgnoreRemixEditions turns off the remix-edition rule: by default an edition
	// whose title or disambiguation says "remix" is typed as a remix, for the remix
	// editions MusicBrainz files in a release group that is not itself typed remix.
	IgnoreRemixEditions bool `json:",omitempty"`
	// OriginalDateAsDate writes the album's original release date as DATE (and YEAR)
	// rather than the edition's. Jellyfin reads no original date at all and Plex users
	// see the reissue's year, so a 2011 remaster of a 1977 album shows as 2011.
	// ORIGINALDATE keeps the original either way, and RELEASEDATE the edition's.
	//
	// All three are omitempty for the reason SingleRecordLabel is: a field present
	// while it means today's behaviour would have changed every profile's fingerprint.
	OriginalDateAsDate bool `json:",omitempty"`
	// BPMWrite and BPMMinConfidence govern the BPM tag. They come from the library,
	// not the profile (Library.BPMWrite); components.Tagger merges them in.
	BPMWrite         string
	BPMMinConfidence float64
	// ReplayGainWrite governs the REPLAYGAIN_* tags, from the library like BPMWrite
	// (Library.ReplayGainWrite). omitempty, so a profile's fingerprint did not change
	// when it was added.
	ReplayGainWrite string `json:",omitempty"`
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
		SingleRecordLabel:                  t.SingleRecordLabel,
		MusicBrainzReleaseType:             t.MusicBrainzReleaseType,
		IgnoreRemixEditions:                t.IgnoreRemixEditions,
		OriginalDateAsDate:                 t.OriginalDateAsDate,
	}
}

// Library is a configured folder plus the components that govern it.
type Library struct {
	Base
	Name      string     `gorm:"not null" json:"name"`
	Path      string     `gorm:"uniqueIndex;not null" json:"path"`
	ManagerID *uuid.UUID `gorm:"type:uuid" json:"manager_id"`
	// DataSourceID and Cron are stored and never read — MusicBrainz is process-wide and
	// every library runs on the one processing schedule. The API still accepts them so
	// older clients do not fail, but the UI no longer offers them: a field that changes
	// nothing reads as a setting that does not work. See docs/media-manager.md.
	DataSourceID    *uuid.UUID `gorm:"type:uuid" json:"data_source_id"`
	TaggerProfileID *uuid.UUID `gorm:"type:uuid" json:"tagger_profile_id"`
	Enabled         bool       `json:"enabled"`
	Cron            string     `json:"cron"`
	LastScan        *time.Time `json:"last_scan"`
	// MediaServers are the media servers that scan this library, each with the section
	// it is in — none, one or several. Replaced as a set by the library form; the rows
	// go with the library or the server.
	MediaServers []LibraryMediaServer `gorm:"foreignKey:LibraryID;constraint:OnDelete:CASCADE" json:"media_servers"`
	// UseAcoustID opts this library in to fingerprint identification. Off by
	// default and the third of three independent switches (a configured AcoustID
	// data source, fpcalc on PATH, this flag) — any one of them off and the library
	// behaves exactly as it did before the feature existed.
	//
	// The column name is pinned: GORM's default naming turns "AcoustID" into
	// "use_acoust_id", which reads like a typo and silently breaks any raw column
	// reference written from the Go field or JSON name.
	UseAcoustID bool `gorm:"column:use_acoustid" json:"use_acoustid"`
	// AnalyseTempo opts this library in to BPM detection (see docs/tempo.md). Off by
	// default: analysis decodes every file, so it is something a library asks for,
	// and a library that never does behaves exactly as it did before the feature.
	AnalyseTempo bool `json:"analyse_tempo"`
	// BPMWrite is whether and how the stored tempo reaches the BPM tag (BPMWrite*):
	// off, only where the file has none, or always. Off by default — analysing and
	// writing are separate decisions, since a stored tempo is useful without touching
	// a file. On the library rather than the tagger profile so that both halves of the
	// feature are set in one place (docs/tempo.md).
	BPMWrite string `json:"bpm_write"`
	// BPMMinConfidence is the confidence a stored tempo needs to be written. Zero or
	// less means DefaultBPMMinConfidence, so a row predating the column behaves like a
	// new one.
	BPMMinConfidence float64 `json:"bpm_min_confidence"`
	// AnalyseLoudness opts this library in to ReplayGain analysis (docs/replaygain.md).
	// Off by default, for tempo's reason: analysis decodes every file.
	AnalyseLoudness bool `json:"analyse_loudness"`
	// ReplayGainWrite is whether and how the stored loudness reaches the REPLAYGAIN_*
	// tags — the same three modes as BPMWrite: "" off, fill_empty (only files with no
	// ReplayGain yet), overwrite.
	ReplayGainWrite string `json:"replaygain_write"`
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
	// ReleaseGroup is the group of the download the file came from — what the
	// {Release Group} naming token renders. Recorded by the import (parsed from the
	// download's name, confirmed in the review) and from Lidarr's track file. Nil means
	// never recorded; for naming it is the same as "" (none) — Lidarr's rule.
	ReleaseGroup *string `json:"release_group,omitempty"`

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
	// TaggedWith fingerprints the tag settings the file was last processed under
	// (components.Tagger.Fingerprint). Skip-unchanged requires it to match, so a
	// changed tagger profile, or a library switched to another one, reaches every file
	// on the next Process instead of only on Tag files.
	TaggedWith string `json:"tagged_with"`
	// Pinned marks a manual correlation that automatic resolution must never override.
	Pinned bool `json:"pinned"`
	// PinnedAudioHash is modules.AudioPayloadHash of the audio a pin was made for (or
	// last confirmed against). A pin names what a *recording* is, so it must not
	// follow a file whose audio was replaced underneath it — Lidarr upgrading the
	// file, another tool re-encoding it. Tag writes leave the hash alone, which is
	// what tells "we rewrote the tags" apart from "this is different audio": both
	// move size and mtime. Empty means not yet known (a pin from before the column,
	// whose file could not be read when it was backfilled).
	PinnedAudioHash string `json:"-"`
	// PinHeld marks a pin whose file's audio no longer matches PinnedAudioHash. The
	// pin is neither applied nor dropped: the file is not tagged, by Process or by
	// Tag files, until someone confirms the pin for the new audio or releases it to
	// automatic resolution (Items → *Pin needs review*).
	PinHeld bool `json:"pin_held"`
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
//
//   - **`unmatched` is a disqualification, and a different one.** It is not a failed
//     attempt: it is the manager answering that it does not know this file. Whatever
//     release ID the row still carries is an answer that has been withdrawn, and
//     writing tags from it would stamp the file with an identity its authority has
//     disclaimed. The same reasoning that keeps these out of the disk view
//     (collection.ownedItemRows), applied to writes.
//
//   - **A held pin is a third.** The identity on the row was chosen for audio the file
//     no longer holds (LibraryItem.PinHeld); writing it would tag the new audio with a
//     decision nobody made about it. Unlike `unmatched` it stays in the disk view — the
//     file is still there, and the hold is waiting on a person, not on the manager.
//
// The failure states part company here, which `status = ok` could not express.
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
	return db.Where("library_items.mb_release_id <> '' AND library_items.status <> ? AND library_items.pin_held = ?",
		LibraryItemStatusUnmatched, false)
}

// InDiskView is a GORM scope narrowing a LibraryItem query to the files the disk
// view counts: every file with an identity, whatever its last attempt did, except one
// the manager has disowned (collection.ownedItemRows has the reasoning).
//
// "Disowned" takes two misses in a row. An `unmatched` row carrying
// LastErrorTransient is a file the manager knew and failed to recognise once — the
// shape of Lidarr part-way through replacing it, which Lidarr does on its own schedule
// and which the history follower re-processes minutes later. Dropping its album from
// the collection for those minutes reported a change nobody made; the row is counted
// on its old identity until the next attempt either resolves it or misses again. It is
// still never *written* from (TaggableItems excludes every `unmatched` row): an
// identity is good enough to count, not to tag.
func InDiskView(db *gorm.DB) *gorm.DB {
	return db.Where("library_items.mb_release_id <> '' AND (library_items.status <> ? OR library_items.last_error_transient = ?)",
		LibraryItemStatusUnmatched, true)
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
// Plex a hash of the album folder (see modules.PlexRefreshForFile).
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
	// limit, kept so the store can still move to MySQL. Every key is bounded by
	// construction; Plex's, once an album title, is a fixed-length hash of the folder.
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
	// MigrationStatusResolved is a change that stopped needing to be applied before
	// anyone applied it: the manager re-keyed the album, a prune took the row, the last
	// file pointing at the old ID went away. Kept apart from applied because nothing
	// here rewrote anything, and a history that reports both the same way cannot answer
	// "what did it decide while I was not looking" — which is the question the page is
	// for. The row is still kept, so the change is not re-raised from scratch.
	MigrationStatusResolved = "resolved"

	// How a migration left the queue. Status says *where* the row ended up; the
	// resolution says who or what put it there, which is the question a history table
	// is actually read for. Without it, an album the manager repaired and an album
	// somebody approved by hand are both "Applied" with a timestamp, and the two are
	// not the same event at all.
	//
	// MigrationResolutionApproved: a person pressed the button.
	MigrationResolutionApproved = "approved"
	// MigrationResolutionAutomatic: policy did not hold it, so a run applied it.
	MigrationResolutionAutomatic = "automatic"
	// MigrationResolutionDismissed: a person declined it.
	MigrationResolutionDismissed = "dismissed"
	// MigrationResolutionExternal: nothing here applied it — the reason it was queued
	// stopped existing. A manager re-keyed the album, another migration took the row,
	// a prune removed it. The row is closed with a sentence saying which, because
	// "resolved itself" is only useful if it says what did the resolving.
	MigrationResolutionExternal = "external"
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
	// Source is the data source that reported the change (DataSourceType*). The table
	// is named for MusicBrainz because MusicBrainz is the only source that reports one
	// today, but nothing about a merged identity is MusicBrainz-specific, and the row
	// is what the UI reads to decide whether to name a source at all. Empty means
	// MusicBrainz: every row written before this column existed came from there.
	Source string `json:"source"`
	// NewMBID is empty for a deletion — there is nothing to point at.
	NewMBID string `json:"new_mb_id"`
	Kind    string `gorm:"not null" json:"kind"`
	Status  string `gorm:"index;not null" json:"status"`

	// Name is a human label for the entity (release or artist title), captured at
	// detection. The review UI would otherwise show two bare UUIDs, and after the
	// migration is applied the old ID can no longer be looked up to name it.
	Name string `json:"name"`

	// SubjectArtistMBID is the artist a release-group row's album belongs to, captured
	// by the drain while the album's collection row still exists (migration.remember).
	// Once a manager re-keys the album and the old row goes, it is the only route left
	// to what the album became. Not serialized: the review exposes the artist as its
	// own computed field.
	SubjectArtistMBID string `json:"-"`

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

	// Resolution is MigrationResolution*: how this row left the queue, and
	// ResolutionDetail is the sentence saying why, for the cases where the reason is
	// not implied by the word. ResolvedAt is when it happened — for anything but an
	// application that is not AppliedAt, which is why history sorts on this instead.
	//
	// A dismissal used to record none of the three, so a dismissed row showed an empty
	// timestamp column and a history ordered by detection read as ordered by nothing.
	Resolution       string     `gorm:"index" json:"resolution,omitempty"`
	ResolutionDetail string     `json:"resolution_detail,omitempty"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`

	// RepairQueuedAt marks that a manager repair is in flight for this row's artist.
	// It is set on *every* pending row that artist holds, because one refresh answers
	// for all of them, and cleared when the job ends (or at startup, if the process
	// died holding it).
	//
	// It exists because approving a blocked album applies nothing: it queues a job and
	// returns 202. Without something on the row, the table answered a press with an
	// unchanged row and two still-clickable buttons, which reads as nothing having
	// happened — see RepairAttemptedAt for the durable half of the same story.
	RepairQueuedAt *time.Time `json:"repair_queued_at,omitempty"`

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

// SourceType is the migration's data source, defaulting to MusicBrainz for rows
// written before the column existed. Read through this rather than the field, so the
// backfill lives in one place instead of at every renderer.
func (m MusicbrainzMigration) SourceType() string {
	if m.Source == "" {
		return DataSourceTypeMusicBrainz
	}
	return m.Source
}

// SourceLabel is the source written the way it spells itself, for a sentence.
func (m MusicbrainzMigration) SourceLabel() string {
	return DataSourceLabel(m.SourceType())
}

// User backs authentication. Starts as a single auto-generated admin; structured
// so OAuth/OIDC and multi-user can be layered on later.
type User struct {
	Base
	Username     string `gorm:"uniqueIndex;not null" json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	Role         string `gorm:"not null;default:admin" json:"role"`

	// External identity, set when the account is linked to an auth provider. The
	// pair (AuthProviderID, ExternalSubject) is the stable identity — OIDC `sub` is
	// immutable, whereas email and username can be changed at the provider.
	AuthProviderID  *uuid.UUID `gorm:"index" json:"auth_provider_id,omitempty"`
	ExternalSubject string     `gorm:"index" json:"-"`
}

// API key scopes. A read key may only read: GET on the REST API, and on the MCP
// endpoint only the tools that read. A write key may do anything its user may.
const (
	APIKeyScopeRead  = "read"
	APIKeyScopeWrite = "write"
)

// APIKey is one named credential for scripts and AI assistants. A user may hold
// several, each with its own scope, so an assistant can be handed a key that cannot
// write and revoked without touching the others.
//
// Only a hash is stored. Keys are 32 random bytes, so an unsalted SHA-256 is enough
// to make a leaked table useless — there is nothing to guess — while keeping lookup
// a single indexed equality (HashAPIKey). The plaintext is shown once, when made.
type APIKey struct {
	Base
	UserID uuid.UUID `gorm:"type:uuid;index;not null" json:"-"`
	Name   string    `gorm:"not null" json:"name"`
	Scope  string    `gorm:"not null" json:"scope"`
	Hash   string    `gorm:"uniqueIndex;not null" json:"-"`
	// Prefix is the key's first characters, so a list of keys can be matched to the
	// one a client holds without storing anything that would let it be used.
	Prefix     string     `json:"prefix"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// ValidAPIKeyScope reports whether scope is one a key may carry.
func ValidAPIKeyScope(scope string) bool {
	return scope == APIKeyScopeRead || scope == APIKeyScopeWrite
}

// HashAPIKey is how a key is stored and looked up.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// APIKeyPrefixLength is how much of a key APIKey.Prefix keeps.
const APIKeyPrefixLength = 6

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

// Keys in Event.Details the Activity page renders as more than raw JSON.
const (
	// EventDetailWarnings: things the run went ahead despite, which the user should
	// fix — a library without a manager. Unlike an error they do not fail the run,
	// which is why they need their own place: a successful run reads as nothing to see.
	EventDetailWarnings = "warnings"
	// EventDetailLibraryErrors: libraries the run skipped whole, each with the reason.
	EventDetailLibraryErrors = "library_errors"
)

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
	// From is where a file was before the activity put it at Path — set by a file
	// import, empty for everything that leaves files where they are.
	From string `json:"from,omitempty"`
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
	// The three outcomes of an identity change, on an entity row. They are what turns
	// "3 applied · 1 failed" in a summary into a list naming which three.
	//
	// EventItemStatusMigrated: the change was applied — a merge re-pointed, a deletion
	// un-matched its files, an album retired.
	EventItemStatusMigrated = "migrated"
	// EventItemStatusResolved: the change stopped needing to be applied. Its own row
	// says what did the resolving; the point of the status is that nothing here did.
	EventItemStatusResolved = "resolved"
	// EventItemStatusDismissed: a person declined it.
	EventItemStatusDismissed = "dismissed"
	// EventItemStatusUnknown is a thing the authority does not have: an artist the
	// collection files under Lidarr that Lidarr never listed. Distinct from Gone (the
	// source used to have it and says so) and from Error (we could not ask) — this is
	// a complete answer that happens to be "no".
	EventItemStatusUnknown = "unknown"
	// EventItemStatusQueued is an artist that had work queued on their behalf — a
	// processing run, because their manager reported files changing under them.
	EventItemStatusQueued = "queued"
	// EventItemStatusSkipped is an artist the same pass could not queue work for, with
	// the reason on the row: not in the collection, or no folder of theirs indexed yet.
	// Their cached manager answers were still dropped, so the next run sees them fresh.
	EventItemStatusSkipped = "skipped"
	// The three things a Scan moves, on entity rows: an artist or album that entered
	// the collection's disk view, one that left it, and an album whose credit moved
	// between artists upstream. They are what turns "3 albums gone" into the three.
	// "Left" rather than "gone", which already means MusicBrainz answered 404.
	EventItemStatusAdded      = "added"
	EventItemStatusLeft       = "left"
	EventItemStatusRecredited = "recredited"
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
	// EventItemPhaseArtists and EventItemPhaseAlbums group a Scan's movement rows by
	// what moved, which is also what tells the reader an MBID is an artist's or a
	// release-group's.
	EventItemPhaseArtists = "collection_artists"
	EventItemPhaseAlbums  = "collection_albums"
	// EventItemPhaseImport is a file a file import placed and tagged.
	EventItemPhaseImport = "import"
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
	// FollowLibraryIDs are the native libraries following wants into, all with the one
	// set of follow settings above. Empty means every library Autotaggerr decides for —
	// what following has always meant for an artist no manager governs. An artist
	// Lidarr governs in one library can still be followed natively in others, and then
	// this names them: following has to say where, or Lidarr's library would read as
	// missing everything the follow wants. See collection.FollowAppliesIn. It replaced
	// a single FollowLibraryID; data migration follow_library_sets carried it across.
	FollowLibraryIDs UUIDSet `gorm:"type:text" json:"follow_library_ids"`

	LastSyncedAt *time.Time `json:"last_synced_at"`

	// CatalogueCount is how many release-groups of this artist's MusicBrainz
	// discography the artist's follow shape takes in (collection.FollowWants: by
	// default studio albums and EPs), whether or not anything follows them — the
	// denominator of "3 of 11 on disk". It is a count rather than stored rows on
	// purpose: rows are what the collection holds and wants, and storing an
	// unfollowed artist's whole catalogue would fill the missing list with albums
	// nobody asked for. CatalogueReadAt is when the discography was last read; nil
	// means never, which the UI says rather than showing owned-of-owned as complete.
	CatalogueCount  int        `json:"catalogue_count"`
	CatalogueReadAt *time.Time `json:"catalogue_read_at"`

	// ManagerMissingAt is when a manager sync found this artist filed under a manager
	// that no longer lists them — the *Not in Lidarr* finding, kept on the artist so
	// their own page can say it and offer to join them to the artist the manager now
	// lists instead. Cleared by the next sync that finds them.
	ManagerMissingAt *time.Time `json:"manager_missing_at"`
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
	// LibraryID is the library this want is for, and only files there fulfil it.
	// uuid.Nil is a want not tied to one library, which belongs to the artist's
	// default authority: Lidarr's when Lidarr governs any of the artist's files,
	// otherwise every library Autotaggerr decides for. Every want predating libraries
	// is that kind, and a manager-derived want always is — Lidarr's selection is one
	// answer for all its libraries. See collection.DesireInScope.
	LibraryID uuid.UUID `gorm:"type:uuid;index" json:"library_id"`
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
	// present, in any library. OwnedTracks distinct tracks present out of TotalTracks
	// on the best-owned edition; complete when OwnedTracks >= TotalTracks > 0. A second
	// copy of a track, in another library or the same one, is not a second track.
	Owned       bool `json:"owned"`
	OwnedTracks int  `json:"owned_tracks"`
	TotalTracks int  `json:"total_tracks"`
	// AuthorityOwned / AuthorityOwnedTracks are the same summary counted only over the
	// libraries whose manager governs this album's artist: the Lidarr libraries for a
	// Lidarr artist, every library for a native one. They are what Discrepancy compares
	// with the catalog, because the catalog is one manager's account of its own
	// libraries — comparing it with every disk made a copy in a second library read as
	// "Lidarr needs a rescan", and an album only that library holds as unmapped.
	AuthorityOwned       bool `json:"authority_owned"`
	AuthorityOwnedTracks int  `json:"authority_owned_tracks"`

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

// TrackTempo is one file's BPM analysis (see docs/tempo.md). Keyed by path, like
// AcoustIDLookup, because analysis is about the audio on disk rather than about
// which release it was matched to — an unidentified file has a tempo too.
//
// BPM is stored raw. A detector that halves or doubles a tempo is wrong in a way
// only the *use* of the number can resolve (a 160 steps/min playlist accepts 80 as
// half-time), so nothing here corrects it.
type TrackTempo struct {
	Path string `gorm:"primarykey" json:"path"`

	// Size and ModTime are the cheap identity check: unchanged means fresh, with no
	// read of the file at all. When they differ, AudioHash decides — it covers the
	// audio payload only, with every tag block excluded, so a tag write (ours
	// included, which is exactly what the BPM tag causes) is recognised as the same
	// audio and re-stamped instead of re-analysed. It is also what lets a moved file
	// keep its result.
	Size      int64      `json:"size"`
	ModTime   *time.Time `json:"mod_time"`
	AudioHash string     `gorm:"index" json:"audio_hash"`

	// BPM is the detector's tempo; Confidence is how regular the beat grid behind it
	// was, 0–1. Both are zero when analysis failed or found too few beats.
	BPM        float64 `json:"bpm"`
	Confidence float64 `json:"confidence"`
	Beats      int     `json:"beats"`

	// Detector names the binary and DetectorVersion pins the result to the analysis
	// that produced it: when either changes, every row is stale and is redone.
	Detector        string `json:"detector"`
	DetectorVersion string `json:"detector_version"`

	// Error is why the last analysis produced no tempo. A file that fails is not
	// retried until it, or the detector, changes — a corrupt file failing every night
	// would be noise, not information.
	Error string `json:"error,omitempty"`

	AnalysedAt time.Time `json:"analysed_at"`
}

// TrackLoudness is one file's ReplayGain, measured by rsgain as part of its album
// (docs/replaygain.md). Album gain is a property of the album's files together, so
// a row also records which album it was measured in and with which members: a file
// added to or gone from the album makes every member's album gain stale.
type TrackLoudness struct {
	Path string `gorm:"primarykey" json:"path"`

	// Size, ModTime and AudioHash: the same identity rules as TrackTempo — a tag write
	// changes the first two but not the audio, and is re-stamped rather than redone.
	Size      int64      `json:"size"`
	ModTime   *time.Time `json:"mod_time"`
	AudioHash string     `gorm:"index" json:"audio_hash"`

	// Album identifies the album the file was measured in (library and release), and
	// AlbumMembers is a hash of that album's member paths at the time.
	Album        string `gorm:"index" json:"album"`
	AlbumMembers string `json:"album_members"`

	// Loudness is the track's integrated loudness (LUFS). The gains are dB to reach
	// ReplayGain 2.0's -18 LUFS reference; the peaks are linear sample peaks.
	Loudness  float64 `json:"loudness"`
	TrackGain float64 `json:"track_gain"`
	TrackPeak float64 `json:"track_peak"`
	AlbumGain float64 `json:"album_gain"`
	AlbumPeak float64 `json:"album_peak"`

	// Analyser and AnalyserVersion pin the result to what produced it, as tempo's
	// detector fields do.
	Analyser        string `json:"analyser"`
	AnalyserVersion string `json:"analyser_version"`

	// Error is why the album's analysis failed; not retried until the album changes.
	Error string `json:"error,omitempty"`

	AnalysedAt time.Time `json:"analysed_at"`
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

	// OwnedTracks is distinct tracks on disk, across every library. Files counts the
	// files behind them, so Files > OwnedTracks means duplicate copies — reported as
	// such, never as completeness.
	OwnedTracks int `json:"owned_tracks"`
	TotalTracks int `json:"total_tracks"`
	Files       int `json:"files"`
}

// CollectionReleaseCopy is one library's copy of an owned edition: which library
// holds files of this release, and how many of its tracks. A CollectionRelease is the
// edition, deduplicated across libraries; these rows say where it lives.
//
// Pure disk state like the edition it belongs to — written only by collection.Rebuild,
// one row per (release, library) with at least one file, pruned with the rest of the
// disk view. Stored rather than read through from library_items so the collection
// list can filter by library without aggregating the whole index per request.
type CollectionReleaseCopy struct {
	Base
	ReleaseMBID      string    `gorm:"uniqueIndex:idx_release_copy;not null" json:"release_mb_id"`
	LibraryID        uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_release_copy;not null" json:"library_id"`
	ReleaseGroupMBID string    `gorm:"index;not null" json:"release_group_mb_id"`
	ArtistMBID       string    `gorm:"index" json:"artist_mb_id"`
	OwnedTracks      int       `json:"owned_tracks"`
	Files            int       `json:"files"`
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
	// The authority's own libraries only: the catalog cannot know about the rest.
	if rg.AuthorityOwned && !rg.InCatalog {
		return DiscrepancyUnmapped
	}
	if !rg.InCatalog || rg.CatalogTotalTracks == 0 {
		return DiscrepancyNone
	}
	if rg.AuthorityOwnedTracks == rg.CatalogOwnedTracks {
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
	if rg.AuthorityOwnedTracks > rg.CatalogOwnedTracks {
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
		&APIKey{},
		&AuthProvider{},
		&Event{},
		&EventItem{},
		&CollectionArtist{},
		&CollectionReleaseGroup{},
		&CollectionReleaseGroupArtist{},
		&CollectionRelease{},
		&CollectionReleaseCopy{},
		&CollectionDesire{},
		&AcoustIDLookup{},
		&TrackTempo{},
		&DataMigration{},
		&DownloadClient{},
		&DownloadClientCategory{},
		&DownloadItem{},
		&CollectionPolicy{},
		&ManagerLink{},
		&MediaServer{},
		&LibraryMediaServer{},
		&TrackLoudness{},
	}
}

// Entity types a ManagerLink can name.
const (
	ManagerLinkArtist       = "artist"
	ManagerLinkReleaseGroup = "release_group"
)

// ManagerLink records that one manager holds an artist or an album, and under which
// of its own IDs. Written by the Lidarr mirror as it lists each manager, so "which
// Lidarr has this?" is answered from what the managers said rather than inferred
// from file locations — with two managers, that inference is the wrong one exactly
// when it matters. It is what a link into the manager's own UI is built from.
//
// Replaced per listing: a manager's artist links when its artist list is read, and
// its album links for one artist when that artist's albums are. A manager deleted
// leaves rows no read joins to.
type ManagerLink struct {
	Base
	ManagerID  uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_manager_link;not null" json:"manager_id"`
	EntityType string    `gorm:"size:32;uniqueIndex:idx_manager_link;not null" json:"entity_type"`
	MBID       string    `gorm:"size:64;uniqueIndex:idx_manager_link;index;not null" json:"mb_id"`
	// ArtistMBID is, for an album, the artist the manager holds it under — which for a
	// collaboration may not be the artist whose page links to it.
	ArtistMBID string `gorm:"size:64;index" json:"artist_mb_id,omitempty"`
	// ExternalID is the manager's own numeric ID (Lidarr's artist or album id).
	ExternalID int64 `json:"external_id"`
}

// DataMigration records a one-time data change that has been applied. AutoMigrate
// keeps the *schema* current on its own; a change to what existing rows mean needs
// running exactly once, and running it again later would undo choices made since.
type DataMigration struct {
	Name      string    `gorm:"primaryKey" json:"name"`
	AppliedAt time.Time `json:"applied_at"`
}
