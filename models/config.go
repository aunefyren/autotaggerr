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
	SMTPEnabled                                   bool           `json:"smtp_enabled"`
	SMTPHost                                      string         `json:"smtp_host"`
	SMTPPort                                      int            `json:"smtp_port"`
	SMTPUsername                                  string         `json:"smtp_username"`
	SMTPPassword                                  string         `json:"smtp_password"`
	SMTPFrom                                      string         `json:"smtp_from"`
	LidarrBaseURL                                 string         `json:"lidarr_base_url"`
	LidarrAPIKey                                  string         `json:"lidarr_api_key"`
	LidarrHeaderCookie                            string         `json:"lidarr_header_cookie"`
	PlexBaseURL                                   string         `json:"plex_base_url"`
	PlexToken                                     string         `json:"plex_token"`
}
