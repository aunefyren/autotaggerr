package models

// DatabaseConfig is bootstrap-only config: it tells Autotaggerr how to connect to
// the database before any domain config (managers, libraries, ...) can be read
// from it. It therefore lives in config.json/env, not in the DB itself.
type DatabaseConfig struct {
	// Type selects the GORM dialector: "sqlite" (default, pure-Go/CGO-free),
	// "postgres", or "mysql".
	Type string `json:"type"`
	// DSN is the connection string. For sqlite it is a file path
	// (default "config/autotaggerr.db").
	DSN string `json:"dsn"`
}

// DefaultMaxGenres is the fallback cap on how many genres reach a file's GENRE
// tag. Five matches MusicBrainz Picard's own default and is the ceiling below
// which a genre list still reads as a description rather than a dump.
const DefaultMaxGenres = 5

// Activity retention. Both figures are shared by every emitter that prunes or writes
// the event tables — the processing runner and the metadata mirror — because they
// write the same two tables and a feed pruned to two different depths would drop
// history depending on which verb happened to run last.
const (
	// DefaultEventRetention is how many top-level runs the Activity feed keeps.
	// Counted in runs rather than rows, so a run's stages never prune each other
	// out (see events.Prune).
	DefaultEventRetention = 200
	// DefaultEventDetailRetention bounds the per-file (or per-entity) detail rows one
	// event stores. A cold scan can change tens of thousands of files; the detail
	// exists to show *what* happened, which the first few hundred rows do, so the
	// rest are counted and dropped rather than turned into a table nobody reads.
	DefaultEventDetailRetention = 500
)

// How the SMTP connection is encrypted. The default is Auto, which infers the answer
// from the port and is right for every hosted provider; the explicit modes exist for
// the self-hosted relay that gets it wrong — one that offers STARTTLS and fails the
// upgrade, or one that offers nothing and should be *refused* rather than spoken to
// in clear.
const (
	// SMTPTLSAuto infers from the port: 465 is implicit TLS, anything else is
	// upgraded with STARTTLS when the server advertises it, and left in clear when
	// it does not.
	SMTPTLSAuto = "auto"
	// SMTPTLSNone never encrypts, even if the server offers STARTTLS. For a relay on
	// localhost, where the alternative is a handshake against a certificate nobody
	// issued.
	SMTPTLSNone = "none"
	// SMTPTLSStartTLS requires the upgrade: a server that does not advertise STARTTLS
	// is an error rather than a plaintext send.
	SMTPTLSStartTLS = "starttls"
	// SMTPTLSImplicit wraps the connection in TLS before the greeting (SMTPS, port
	// 465), for a server that expects TLS on a non-standard port.
	SMTPTLSImplicit = "implicit"
)

// SMTPTLSModes lists the modes in the order they escalate, for the settings page.
var SMTPTLSModes = []string{SMTPTLSAuto, SMTPTLSNone, SMTPTLSStartTLS, SMTPTLSImplicit}

// ConfigStruct is what config.json holds: how this process starts and how it reaches
// the outside world. Nothing that describes a *library* belongs here — managers, data
// sources, tagger profiles and library folders are database rows, edited on their own
// pages. Keys that once seeded those rows have been removed rather than kept as dead
// weight: a key nothing reads is worse than a missing one, because editing it looks
// like it should do something.
type ConfigStruct struct {
	Timezone               string         `json:"timezone"`
	Database               DatabaseConfig `json:"database"`
	PrivateKey             string         `json:"private_key"`
	AutotaggerrPort        int            `json:"autotaggerr_port"`
	AutotaggerrName        string         `json:"autotaggerr_name"`
	AutotaggerrExternalURL string         `json:"autotaggerr_external_url"`
	AutotaggerrVersion     string         `json:"autotaggerr_version"`
	AutotaggerrEnvironment string         `json:"autotaggerr_environment"`
	AutotaggerrTestEmail   string         `json:"autotaggerr_test_email"`
	AutotaggerrLogLevel    string         `json:"autotaggerr_log_level"`

	AutotaggerrProcessCronSchedule string `json:"autotaggerr_process_cron_schedule"`
	AutotaggerrProcessConcurrency  int    `json:"autotaggerr_process_concurrency"`

	// AutotaggerrEventRetention and AutotaggerrEventDetailRetention size the Activity
	// feed: how many runs are kept, and how much per-file detail each one stores.
	// They trade history against database size — a busy library with the detail cap
	// raised keeps a far bigger event table — so they are one knob for someone who
	// wants a longer audit trail and one for someone who wants a smaller database.
	// Zero or less means the default.
	AutotaggerrEventRetention       int `json:"autotaggerr_event_retention"`
	AutotaggerrEventDetailRetention int `json:"autotaggerr_event_detail_retention"`

	// MusicBrainz mirror. The mirror refreshes the local copy of every MusicBrainz
	// entity the collection refers to on a schedule, so browsing reads the database
	// instead of a rate-limited API (see docs/mirror.md).
	//
	// Disabled expresses the opt-out rather than an "enabled" opt-in for the same
	// reason the migration review flags do: a bool absent from an existing
	// config.json decodes as false, and false has to mean the default behaviour.
	AutotaggerrMirrorDisabled     bool   `json:"autotaggerr_mirror_disabled"`
	AutotaggerrMirrorCronSchedule string `json:"autotaggerr_mirror_cron_schedule"`

	// Artwork refresh. Covers and artist images are warmed on their own schedule
	// rather than as part of the metadata refresh: they come from a different kind of
	// data source spending a different budget (the image hosts' throttle, not
	// MusicBrainz's one request per second), so there is nothing to coordinate and no
	// reason to make one wait for the other. See docs/artwork.md.
	//
	// Disabled rather than enabled, for the same reason the mirror key is phrased
	// that way: a bool absent from an existing config.json decodes as false, and
	// false has to mean the default behaviour.
	AutotaggerrArtworkDisabled     bool   `json:"autotaggerr_artwork_disabled"`
	AutotaggerrArtworkCronSchedule string `json:"autotaggerr_artwork_cron_schedule"`

	// Health-check schedule for the configured Lidarr/Plex connections. Checks are
	// cheap and only record an Activity event when a service's health changes, so a
	// frequent cadence does not flood the feed. Empty falls back to the default.
	AutotaggerrHealthCronSchedule string `json:"autotaggerr_health_cron_schedule"`

	// MusicBrainz migration review policy. These are phrased as *review* opt-ins
	// rather than auto-apply opt-outs on purpose: a bool absent from an existing
	// config.json decodes as false, and false must mean "apply it" — the default —
	// so upgrading cannot silently start queueing every merge for approval.
	AutotaggerrMigrationReviewReleases bool `json:"autotaggerr_migration_review_releases"`
	AutotaggerrMigrationReviewArtists  bool `json:"autotaggerr_migration_review_artists"`
	// AutotaggerrMigrationReviewPinned holds any migration that would rewrite a
	// manual correlation, whatever its entity type.
	AutotaggerrMigrationReviewPinned bool `json:"autotaggerr_migration_review_pinned"`
	// AutotaggerrMigrationReviewDeletions holds the 404 case, which un-identifies
	// files rather than re-pointing them.
	AutotaggerrMigrationReviewDeletions bool `json:"autotaggerr_migration_review_deletions"`

	SMTPEnabled bool   `json:"smtp_enabled"`
	SMTPHost    string `json:"smtp_host"`
	SMTPPort    int    `json:"smtp_port"`
	// SMTPTLS is how the connection is encrypted; see the SMTPTLS* constants.
	SMTPTLS      string `json:"smtp_tls"`
	SMTPUsername string `json:"smtp_username"`
	SMTPPassword string `json:"smtp_password"`
	SMTPFrom     string `json:"smtp_from"`

	// Plex is still configured here: it is not a manager row, and the process-level
	// client is built from these at startup (see main.go). The Lidarr equivalents used
	// to sit beside them and are gone — they seeded the first manager on the first
	// boot and were ignored forever after, so the manager row is the only copy of
	// those credentials now.
	PlexBaseURL string `json:"plex_base_url"`
	PlexToken   string `json:"plex_token"`
}
