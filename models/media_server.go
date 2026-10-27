package models

import "github.com/google/uuid"

// Media server types. Plex is the only one; the row is typed so a second kind of server
// (Jellyfin) is a new client and a new value here, not a second table and page.
const (
	MediaServerTypePlex = "plex"
)

// MediaServer is one media server Autotaggerr tells to refresh an album after its
// files' tags change. Zero, one or several may be configured, and each library names
// the ones that scan it (LibraryMediaServer) — two Plex servers sharing one share is
// two links, not two libraries. See docs/media-servers.md.
//
// It replaces the plex_base_url / plex_token keys in config.json, which made Plex one
// server for the whole process; database.MigrateLegacyPlex turns those into a row.
type MediaServer struct {
	Base
	Name    string `gorm:"uniqueIndex;not null" json:"name"`
	Type    string `gorm:"not null" json:"type"`
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
	// WebURL is where a browser reaches this server, for links into it — the server's
	// own web app rather than app.plex.tv. Empty links through app.plex.tv, which
	// reaches the server from any browser signed in to the account.
	WebURL string `json:"web_url"`
	// Token authenticates every request (X-Plex-Token for Plex). Write-only, like the
	// other credentials: settable through the API, never returned.
	Token string `json:"-"`
}

// LibraryMediaServer says a media server scans a library, and in which of its
// sections. An empty SectionKey searches every music section on that server: the
// album a refresh goes to is the one the server lists the file in, so a wider search
// costs requests, never a wrong album. Naming the section only saves those requests.
type LibraryMediaServer struct {
	LibraryID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"library_id"`
	MediaServerID uuid.UUID `gorm:"type:uuid;primaryKey;index" json:"media_server_id"`
	SectionKey    string    `json:"section_key"`
}
