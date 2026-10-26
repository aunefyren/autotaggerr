package models

import (
	"time"

	"github.com/google/uuid"
)

// Download clients: where finished downloads are picked up for import. See
// docs/downloads.md.
//
// A download client is not a data source. Data sources answer questions about music;
// a download client is a place files arrive, and what it needs — a login, the
// categories it watches, the library each one feeds, how its paths read on this host —
// is shared with nothing in that table.

const (
	DownloadClientTypeQBittorrent  = "qbittorrent"
	DownloadClientTypeTransmission = "transmission"
	DownloadClientTypeSABnzbd      = "sabnzbd"
)

// DownloadClientTypes lists the client types the app can talk to, in the order the UI
// offers them.
var DownloadClientTypes = []string{DownloadClientTypeQBittorrent, DownloadClientTypeTransmission, DownloadClientTypeSABnzbd}

// DownloadClientSeeds reports whether a client type holds a download's files after it
// finishes. A torrent client does, for as long as the torrent exists, so an import
// copies; a usenet client is done with them, so an import may move them, and nothing
// is left to seed or remove.
func DownloadClientSeeds(clientType string) bool {
	return clientType != DownloadClientTypeSABnzbd
}

// DownloadClientImportedTag is the tag a download is given in its client once every
// album in it has been imported. It is the one thing Autotaggerr writes to a download
// client, and it doubles as a second record: a torrent carrying it is never offered
// again, even to a database that has never seen it.
const DownloadClientImportedTag = "autotaggerr-imported"

// PathMapping rewrites the start of a path as a remote host reports it into the same
// path as this host sees it — a client in another container, or on another machine,
// reports "/downloads/music" for what is mounted here at "/mnt/torrents/music".
type PathMapping struct {
	Remote string `json:"remote"`
	Local  string `json:"local"`
}

// DownloadClient is one configured download client.
type DownloadClient struct {
	Base
	Name    string `gorm:"uniqueIndex;not null" json:"name"`
	Type    string `gorm:"not null" json:"type"`
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
	// Username and Password log in to the client's Web API. Both may be empty for a
	// client that skips authentication for this host (qBittorrent's localhost and
	// subnet bypass). The password is write-only, like the other credentials. For
	// SABnzbd, which has no login, Password holds the API key and Username is unused.
	Username string `json:"username"`
	Password string `json:"-"`
	// ImportedCategory, when set, is the category a download is moved to once every
	// album in it is imported, beside the imported tag — out of the watched category,
	// into one the client may treat differently (a slower seed, a different folder).
	// Torrent clients only: a usenet client's history has no category to change.
	ImportedCategory string `json:"imported_category"`
	// RemoveAfterSeeding removes an imported download, files and all, once the client
	// has stopped it for reaching its own share limits — when the album's second copy
	// stops being worth its space. Off unless asked for; torrent clients only.
	RemoveAfterSeeding bool `json:"remove_after_seeding"`
	// PathMappings are tried longest remote prefix first; a path no mapping matches
	// is used as the client reported it, which is right when both run on one host.
	PathMappings []PathMapping `gorm:"serializer:json" json:"path_mappings"`

	// What the last poll found, so the Downloads list can say why it is empty. Written
	// by the poller only — a save from the form omits both, for the reason a Lidarr
	// manager's history cursor is omitted.
	LastPollAt    *time.Time `json:"last_poll_at"`
	LastPollError string     `json:"last_poll_error,omitempty"`

	Categories []DownloadClientCategory `gorm:"foreignKey:ClientID;constraint:OnDelete:CASCADE" json:"categories"`
}

// DownloadClientCategory routes one of a client's categories into a library. Only
// mapped categories are ever read, so a Lidarr sharing the client under its own
// category is never touched.
type DownloadClientCategory struct {
	Base
	ClientID  uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_download_category;not null" json:"client_id"`
	Category  string    `gorm:"uniqueIndex:idx_download_category;not null" json:"category"`
	LibraryID uuid.UUID `gorm:"type:uuid;not null" json:"library_id"`
	// AutoImport imports this category's downloads without a review when the match is
	// certain (see process.autoImportPlan); anything less certain waits in the list as
	// before. Off unless asked for, so the zero value of a row predating it is the
	// behaviour it always had.
	AutoImport bool `json:"auto_import"`
}

// What a download is waiting on.
const (
	// DownloadStateReady: finished, its audio is readable here, and nothing has been
	// imported from it yet — or only some of its albums.
	DownloadStateReady = "ready"
	// DownloadStateUnavailable: finished, but there is nothing here to import — its
	// files are not where the path mappings say, or it holds no audio. Reason says
	// which. Looked at again on every poll, so fixing a mapping clears it.
	DownloadStateUnavailable = "unavailable"
	// DownloadStateImported: every album in it was imported, and the client was told.
	DownloadStateImported = "imported"
	// DownloadStateDismissed: someone decided not to import it. Kept, so it is not
	// offered again on the next poll.
	DownloadStateDismissed = "dismissed"
)

// What happened to one album of a download.
const (
	DownloadGroupQueued   = "queued"
	DownloadGroupImported = "imported"
	DownloadGroupFailed   = "failed"
)

// DownloadItem is one finished download in a watched category, from the moment the
// poller first sees it complete. Its identity is the client's own ID for it (a
// torrent's info-hash), so a download is offered once however often it is polled.
type DownloadItem struct {
	Base
	ClientID uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_download_item;not null" json:"client_id"`
	// RemoteID is the client's identifier: the info-hash, for a torrent.
	RemoteID  string    `gorm:"uniqueIndex:idx_download_item;not null" json:"remote_id"`
	Name      string    `json:"name"`
	Category  string    `json:"category"`
	LibraryID uuid.UUID `gorm:"type:uuid" json:"library_id"`
	// Path is the download's content as this host sees it: its folder, or the file
	// itself for a single-file torrent. RemotePath is the same as the client reported
	// it, kept so a wrong path mapping can be seen for what it is.
	Path       string `json:"path"`
	RemotePath string `json:"remote_path"`
	// Files are the download's audio files, absolute on this host. Only files the
	// client reports as fully downloaded are listed, so a deselected file or a
	// partial one is never offered.
	Files []string `gorm:"serializer:json" json:"files"`
	Size  int64    `json:"size"`
	State string   `gorm:"index" json:"state"`
	// Reason says why a download is unavailable, or why telling the client failed.
	Reason string `json:"reason,omitempty"`
	// Groups records what happened to each album folder of the download, by folder:
	// DownloadGroup*. An album absent from it has not been imported.
	Groups map[string]string `gorm:"serializer:json" json:"groups"`
	// Notes says, by album folder, why auto-import left an album for review.
	Notes map[string]string `gorm:"serializer:json" json:"notes"`
	// AutoCheckedAt is when auto-import last looked at the download. Each download is
	// looked at once; a failure worth retrying (MusicBrainz unreachable) leaves it unset.
	AutoCheckedAt *time.Time `json:"auto_checked_at"`
	// Seeding is whether the client still holds the files. While it does, an import
	// copies them: moving them would pull them out from under the client.
	Seeding     bool       `json:"seeding"`
	CompletedAt *time.Time `json:"completed_at"`
	ImportedAt  *time.Time `json:"imported_at"`
	// RemovedAt is when Autotaggerr removed the download from its client after it
	// finished seeding (DownloadClient.RemoveAfterSeeding).
	RemovedAt *time.Time `json:"removed_at,omitempty"`
	// SeenAt is the last poll that found the download in its client.
	SeenAt *time.Time `json:"seen_at"`
}
