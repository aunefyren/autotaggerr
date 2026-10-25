package modules

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// Download clients: where finished downloads come from. See docs/downloads.md.
//
// A client is only a source and a trigger for the file import — it says which folder
// holds a finished download and when it is ready. Identification is still the files'
// own business (ScanImportSource's hints, the tags' release ID), because unlike
// Lidarr, Autotaggerr grabbed nothing and so knows nothing about a download before
// reading it.

// Download is one finished download as its client reports it.
type Download struct {
	// ID is the client's own identifier: the info-hash, for a torrent.
	ID       string
	Name     string
	Category string
	// Path is the download's content — its folder, or the file of a single-file
	// torrent — as the client sees it. Not yet mapped onto this host.
	Path string
	// FilesRoot is the folder the client's file names are relative to, as the client
	// sees it — a torrent's save path.
	FilesRoot string
	Size      int64
	// Seeding is whether the client still holds the files, so an import must copy
	// rather than move them. A torrent client holds them for as long as the torrent
	// exists, paused or not.
	Seeding     bool
	CompletedAt time.Time
	// Imported is whether the download already carries the imported mark — the second
	// record of an import, which outlives the database.
	Imported bool
}

// DownloadFile is one file of a download, as its client sees it.
type DownloadFile struct {
	Path string
	// FilesRoot is the folder the client's file names are relative to, as the client
	// sees it — a torrent's save path.
	FilesRoot string
	Size      int64
	// Complete is whether every byte of the file is on disk. A file deselected in the
	// client, or still partial, is not.
	Complete bool
}

// DownloadClient is what the poller and the import need from a client. Kept small so
// a second client type (Transmission, Deluge, a usenet client) is a new file rather
// than a reshaped feature.
type DownloadClient interface {
	// Finished lists the downloads in the given categories that have finished and are
	// not being checked, moved or repaired by the client.
	Finished(categories []string) ([]Download, error)
	// Files lists a download's files.
	Files(d Download) ([]DownloadFile, error)
	// MarkImported records in the client that a download has been imported. It is the
	// only write Autotaggerr makes to a download client.
	MarkImported(id string) error
	HealthCheck() (bool, error)
}

// NewDownloadClient builds the client for a configured row.
func NewDownloadClient(row models.DownloadClient) (DownloadClient, error) {
	switch row.Type {
	case models.DownloadClientTypeQBittorrent:
		if strings.TrimSpace(row.BaseURL) == "" {
			return nil, fmt.Errorf("download client %q has no base URL", row.Name)
		}
		return NewQBittorrentClient(row.BaseURL, row.Username, row.Password), nil
	}
	return nil, fmt.Errorf("download client %q has an unsupported type %q", row.Name, row.Type)
}

// MapRemotePath rewrites a path a remote host reported into the path this host knows
// it by, by the longest matching remote prefix. Prefixes match whole segments, so a
// mapping for /downloads does not rewrite /downloads2. The remote side is compared with
// either separator and without case for a drive letter, since a Windows-hosted client
// reports D:\Torrents to an Autotaggerr on Linux.
//
// A path no mapping matches comes back unchanged, which is right when the client and
// Autotaggerr share a filesystem.
func MapRemotePath(remote string, mappings []models.PathMapping) string {
	slashed := remoteSlash(remote)
	sorted := append([]models.PathMapping(nil), mappings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(remoteSlash(sorted[i].Remote)) > len(remoteSlash(sorted[j].Remote))
	})
	for _, m := range sorted {
		prefix := strings.TrimRight(remoteSlash(m.Remote), "/")
		if prefix == "" || strings.TrimSpace(m.Local) == "" {
			continue
		}
		rest, ok := cutPathPrefix(slashed, prefix)
		if !ok {
			continue
		}
		if rest == "" {
			return filepath.Clean(m.Local)
		}
		return filepath.Join(m.Local, filepath.FromSlash(rest))
	}
	return filepath.Clean(filepath.FromSlash(slashed))
}

// remoteSlash writes a remote path with forward slashes, cleaned. A backslash is a
// separator here even on a Linux host: the path came from somewhere else.
func remoteSlash(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if p == "" {
		return ""
	}
	return path.Clean(p)
}

// cutPathPrefix reports the remainder of p after prefix when prefix is a whole-segment
// prefix of it. A drive letter compares without case.
func cutPathPrefix(p, prefix string) (string, bool) {
	if len(p) < len(prefix) || !strings.EqualFold(p[:len(prefix)], prefix) {
		return "", false
	}
	// Case-folding is only for the drive letter; the rest must match exactly.
	if p[:len(prefix)] != prefix && !driveOnlyDiffers(p[:len(prefix)], prefix) {
		return "", false
	}
	rest := p[len(prefix):]
	switch {
	case rest == "":
		return "", true
	case strings.HasPrefix(rest, "/"):
		return strings.TrimPrefix(rest, "/"), true
	case strings.HasSuffix(prefix, "/"):
		return rest, true
	}
	return "", false
}

func driveOnlyDiffers(a, b string) bool {
	if len(a) < 2 || len(b) < 2 || a[1] != ':' || b[1] != ':' {
		return false
	}
	return strings.EqualFold(a[:1], b[:1]) && a[1:] == b[1:]
}
