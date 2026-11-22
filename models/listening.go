package models

import (
	"time"

	"github.com/google/uuid"
)

// Listening sources. A ListeningAccount is one of these, linked by one user.
const (
	ListeningSourcePlex         = "plex"
	ListeningSourceListenBrainz = "listenbrainz"
)

// ListeningAccount is one place a user's plays are read from: an account on one Plex
// server, or a ListenBrainz user. History is per Autotaggerr user, so every row names
// its user, and every route that reads one is scoped to the caller. See
// docs/listening.md.
type ListeningAccount struct {
	Base
	UserID  uuid.UUID `gorm:"type:uuid;index;not null" json:"user_id"`
	Source  string    `gorm:"size:32;not null" json:"source"`
	Enabled bool      `json:"enabled"`

	// Plex: the server, and the account on it whose plays are read (the server's own
	// account id: 1 is the owner). History is read with the server's token.
	MediaServerID   *uuid.UUID `gorm:"type:uuid;index" json:"media_server_id,omitempty"`
	PlexAccountID   string     `json:"plex_account_id,omitempty"`
	PlexAccountName string     `json:"plex_account_name,omitempty"`

	// ListenBrainz: the user name, and an optional token. Public listens need none.
	// Write-only, like every other credential.
	Username string `json:"username,omitempty"`
	Token    string `json:"-"`

	// Cursor is the newest play read so far; the next pass reads from there. It moves
	// only when a pass over the account completes.
	Cursor       *time.Time `json:"cursor,omitempty"`
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

// Listen is one play. RecordingMBID is what attaches it to the collaboration graph;
// it is empty when the play could not be identified (a Plex track no longer on disk,
// a scrobble ListenBrainz could not map), and the raw strings are kept for counting
// and showing those.
type Listen struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uuid.UUID `gorm:"type:uuid;index:idx_listen_user_time;not null" json:"user_id"`
	AccountID  uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_listen_external;not null" json:"account_id"`
	Source     string    `gorm:"size:32;not null" json:"source"`
	ListenedAt time.Time `gorm:"index:idx_listen_user_time;not null" json:"listened_at"`
	// ExternalKey identifies the play at its source (Plex historyKey; ListenBrainz
	// listened_at and recording_msid), so reading a window twice stores it once.
	ExternalKey   string `gorm:"size:255;uniqueIndex:idx_listen_external;not null" json:"external_key"`
	RecordingMBID string `gorm:"size:64;index" json:"recording_mb_id"`
	ReleaseMBID   string `gorm:"size:64" json:"release_mb_id"`
	// Path is the library file a Plex play was matched to.
	Path   string `json:"path,omitempty"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
}

// ListenArtist is an artist a source named for a play (ListenBrainz's MBID mapping).
// The graph reads it only for recordings with no credits in insight_credits — music
// not in the library — so co-listening can reach, and name, artists not on disk.
type ListenArtist struct {
	ListenID   uint   `gorm:"primaryKey" json:"listen_id"`
	ArtistMBID string `gorm:"primaryKey;size:64;index" json:"artist_mb_id"`
	ArtistName string `json:"artist_name"`
}
