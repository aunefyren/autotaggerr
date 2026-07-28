// Package components implements Autotaggerr's pluggable media-management model:
// Data Sources (metadata providers), Managers (the correlation authority that
// maps a file to a MusicBrainz release/track), and Taggers (tag-writing
// profiles). Each is built from a database row and wraps the lower-level
// operations in modules/, so the scan pipeline can be assembled per library.
package components

import (
	"fmt"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// DataSource is a metadata provider. MusicBrainz is the only implementation
// today; AcoustID (fingerprinting) will be another.
type DataSource interface {
	GetRelease(mbID string) (models.MusicBrainzReleaseResponse, error)
	HealthCheck() (bool, error)
	Type() string
}

// Manager is the correlation authority for a library: it decides which MB
// release/track a file maps to.
type Manager interface {
	Correlate(filePath, rootDir string) (models.Correlation, error)
	HealthCheck() (bool, error)
	Type() string
}

// --- Data sources -----------------------------------------------------------

// MusicBrainzDataSource wraps the cached, rate-limited MusicBrainz client in
// modules/. (The fetch still routes through modules' cache today; when the MB
// cache moves into the DB this stays the single seam that changes.)
type MusicBrainzDataSource struct {
	row models.DataSource
}

func (d *MusicBrainzDataSource) GetRelease(mbID string) (models.MusicBrainzReleaseResponse, error) {
	return modules.GetMusicBrainzRelease(mbID)
}

func (d *MusicBrainzDataSource) HealthCheck() (bool, error) { return true, nil }
func (d *MusicBrainzDataSource) Type() string               { return models.DataSourceTypeMusicBrainz }

// NewDataSource builds a DataSource from its DB row.
func NewDataSource(row models.DataSource) (DataSource, error) {
	switch row.Type {
	case models.DataSourceTypeMusicBrainz:
		return &MusicBrainzDataSource{row: row}, nil
	default:
		return nil, fmt.Errorf("unsupported data source type %q", row.Type)
	}
}

// --- Managers ---------------------------------------------------------------

// LidarrManager reads the correlation decision from Lidarr, falling back to the
// file's embedded tags — exactly the original ProcessTrackFile behavior.
type LidarrManager struct {
	client *modules.LidarrClient
}

func (m *LidarrManager) Correlate(filePath, rootDir string) (models.Correlation, error) {
	return modules.ResolveCorrelation(filePath, m.client, rootDir)
}

func (m *LidarrManager) HealthCheck() (bool, error) {
	if m.client == nil {
		return false, fmt.Errorf("lidarr client not configured")
	}
	return m.client.HealthCheck()
}

func (m *LidarrManager) Type() string { return models.ManagerTypeLidarr }

// AutotaggerrManager resolves natively, without Lidarr: today from the file's
// embedded MusicBrainz tags (fingerprinting and manual pins come later).
type AutotaggerrManager struct{}

func (m *AutotaggerrManager) Correlate(filePath, rootDir string) (models.Correlation, error) {
	return modules.ResolveCorrelation(filePath, nil, rootDir)
}

func (m *AutotaggerrManager) HealthCheck() (bool, error) { return true, nil }
func (m *AutotaggerrManager) Type() string               { return models.ManagerTypeAutotaggerr }

// NewManager builds a Manager from its DB row. A Lidarr manager with missing
// credentials still constructs (Correlate then just falls back to tags), so a
// half-configured row never hard-fails a scan.
func NewManager(row models.Manager) (Manager, error) {
	switch row.Type {
	case models.ManagerTypeLidarr:
		var client *modules.LidarrClient
		if row.LidarrBaseURL != "" && row.LidarrAPIKey != "" {
			cookie := row.LidarrHeaderCookie
			client = modules.NewLidarrClient(row.LidarrBaseURL, row.LidarrAPIKey, &cookie)
		}
		return &LidarrManager{client: client}, nil
	case models.ManagerTypeAutotaggerr:
		return &AutotaggerrManager{}, nil
	default:
		return nil, fmt.Errorf("unsupported manager type %q", row.Type)
	}
}

// --- Tagger -----------------------------------------------------------------

// Tagger applies a TaggerProfile's settings when writing tags. There is one
// built-in engine (modules' FLAC/MP3 writers); the profile only tunes it.
type Tagger struct {
	profile models.TaggerProfile
}

// NewTagger builds a Tagger from its profile row.
func NewTagger(profile models.TaggerProfile) *Tagger {
	return &Tagger{profile: profile}
}

// WriteEnabled reports whether this profile writes tags at all.
func (t *Tagger) WriteEnabled() bool { return t.profile.WriteTags }

// Config projects the profile onto the ConfigStruct fields the tag writers read,
// so the existing tagging code can be reused unchanged.
func (t *Tagger) Config() models.ConfigStruct {
	return models.ConfigStruct{
		AutotaggerrUseCurrentArtistName:               t.profile.UseCurrentArtistName,
		AutotaggerrIgnoreRedundantContributingArtists: t.profile.IgnoreRedundantContributingArtists,
		AutotaggerrUseCustomArtistDelimiter:           t.profile.UseCustomArtistDelimiter,
		AutotaggerrCustomArtistDelimiter:              t.profile.CustomArtistDelimiter,
		AutotaggerrCustomArtistDelimiterCommas:        t.profile.CustomArtistDelimiterCommas,
		AutotaggerrRemoveValues:                       t.profile.RemoveValues,
	}
}
