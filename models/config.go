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

type ConfigStruct struct {
	Timezone                                      string         `json:"timezone"`
	Database                                      DatabaseConfig `json:"database"`
	PrivateKey                                    string         `json:"private_key"`
	AutotaggerrPort                               int            `json:"autotaggerr_port"`
	AutotaggerrName                               string         `json:"autotaggerr_name"`
	AutotaggerrExternalURL                        string         `json:"autotaggerr_external_url"`
	AutotaggerrVersion                            string         `json:"autotaggerr_version"`
	AutotaggerrEnvironment                        string         `json:"autotaggerr_environment"`
	AutotaggerrTestEmail                          string         `json:"autotaggerr_test_email"`
	AutotaggerrLogLevel                           string         `json:"autotaggerr_log_level"`
	AutotaggerrLibraries                          []string       `json:"autotaggerr_libraries"`
	AutotaggerrProcessOnStartUp                   bool           `json:"autotaggerr_process_on_start_up"`
	AutotaggerrProcessCronSchedule                string         `json:"autotaggerr_process_cron_schedule"`
	AutotaggerrProcessConcurrency                 int            `json:"autotaggerr_process_concurrency"`
	AutotaggerrUseCurrentArtistName               bool           `json:"autotaggerr_use_current_artist_name"`
	AutotaggerrIgnoreRedundantContributingArtists bool           `json:"autotaggerr_ignore_redundant_contributing_artists"`
	AutotaggerrUseCustomArtistDelimiter           bool           `json:"autotaggerr_use_custom_artist_delimiter"`
	AutotaggerrCustomArtistDelimiter              string         `json:"autotaggerr_custom_artist_delimiter"`
	AutotaggerrCustomArtistDelimiterCommas        bool           `json:"autotaggerr_custom_artist_delimiter_commas"`
	AutotaggerrRemoveValues                       bool           `json:"autotaggerr_remove_values"`
	// AutotaggerrMaxGenres caps how many of a release group's genres are written.
	// MusicBrainz returns every folksonomy genre that cleared the vote threshold,
	// which on a popular release group is dozens — so the cap is what keeps GENRE
	// readable rather than a wall of near-synonyms. Zero or less means the default.
	AutotaggerrMaxGenres int `json:"autotaggerr_max_genres"`
	// AutotaggerrMP3MultiValueTags picks how an MP3 says that a field has several
	// values. Off (the default) joins them into one string with "; "; on writes the
	// spec-correct ID3v2.4 form, one frame whose values are separated by a null byte.
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
	AutotaggerrMP3MultiValueTags bool `json:"autotaggerr_mp3_multi_value_tags"`

	// MusicBrainz mirror. The mirror refreshes the local copy of every MusicBrainz
	// entity the collection refers to on a schedule, so browsing reads the database
	// instead of a rate-limited API (see docs/mirror.md).
	//
	// Disabled expresses the opt-out rather than an "enabled" opt-in for the same
	// reason the migration review flags do: a bool absent from an existing
	// config.json decodes as false, and false has to mean the default behaviour.
	AutotaggerrMirrorDisabled     bool   `json:"autotaggerr_mirror_disabled"`
	AutotaggerrMirrorCronSchedule string `json:"autotaggerr_mirror_cron_schedule"`
	AutotaggerrMirrorOnStartUp    bool   `json:"autotaggerr_mirror_on_start_up"`

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

	SMTPEnabled        bool   `json:"smtp_enabled"`
	SMTPHost           string `json:"smtp_host"`
	SMTPPort           int    `json:"smtp_port"`
	SMTPUsername       string `json:"smtp_username"`
	SMTPPassword       string `json:"smtp_password"`
	SMTPFrom           string `json:"smtp_from"`
	LidarrBaseURL      string `json:"lidarr_base_url"`
	LidarrAPIKey       string `json:"lidarr_api_key"`
	LidarrHeaderCookie string `json:"lidarr_header_cookie"`
	PlexBaseURL        string `json:"plex_base_url"`
	PlexToken          string `json:"plex_token"`
}
