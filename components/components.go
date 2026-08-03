// Package components implements Autotaggerr's pluggable media-management model:
// Data Sources (metadata providers), Managers (the correlation authority that
// maps a file to a MusicBrainz release/track), and Taggers (tag-writing
// profiles). Each is built from a database row and wraps the lower-level
// operations in modules/, so the scan pipeline can be assembled per library.
package components

import (
	"fmt"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
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

// MusicBrainzDataSource adapts the MetadataSource port onto the DataSource
// interface. The fetch routes through the injected port (the real one wraps
// modules' cached, rate-limited client); this stays the single seam that changes
// when the MB cache moves into the DB.
type MusicBrainzDataSource struct {
	row  models.DataSource
	meta metadata.MetadataSource
}

func (d *MusicBrainzDataSource) GetRelease(mbID string) (models.MusicBrainzReleaseResponse, error) {
	return d.meta.GetRelease(mbID)
}

func (d *MusicBrainzDataSource) HealthCheck() (bool, error) { return true, nil }
func (d *MusicBrainzDataSource) Type() string               { return models.DataSourceTypeMusicBrainz }

// NewDataSource builds a DataSource from its DB row, backed by the real
// MusicBrainz-backed metadata source.
func NewDataSource(row models.DataSource) (DataSource, error) {
	switch row.Type {
	case models.DataSourceTypeMusicBrainz:
		return &MusicBrainzDataSource{row: row, meta: modules.NewMetadataSource()}, nil
	default:
		return nil, fmt.Errorf("unsupported data source type %q", row.Type)
	}
}

// ApplyDataSourceRateLimits pushes the configured MusicBrainz request rate into the
// client's limiter. Until this ran, `rate_limit` was editable through the API and
// seeded in the DB but never read — the limiter was a hardcoded 1 req/s. It is
// called at startup and again whenever a data source is edited, so raising the rate
// (only sensible against a local MusicBrainz mirror) takes effect without a restart.
//
// An enabled MusicBrainz row with a non-positive rate is left at the current
// interval by the setter rather than being treated as "unlimited".
func ApplyDataSourceRateLimits(db *gorm.DB) {
	if db == nil {
		return
	}

	var row models.DataSource
	err := db.Where("type = ? AND enabled = ?", models.DataSourceTypeMusicBrainz, true).First(&row).Error
	if err != nil {
		return // no enabled MusicBrainz source: keep the safe default
	}
	if row.RateLimit <= 0 {
		return
	}
	modules.SetMusicBrainzRateLimit(row.RateLimit)
	logger.Log.Infof("MusicBrainz rate limit set to %.3g req/s from data source %q", row.RateLimit, row.Name)
}

// --- Managers ---------------------------------------------------------------

// LidarrManager reads the correlation decision from Lidarr. When it has a client to
// ask, it owns identity: a file Lidarr does not match is left unmatched rather than
// tagged from the file's own (possibly stale) embedded tags. Only a misconfigured
// manager — a Lidarr row with no usable client — keeps the legacy tag fallback, so an
// outage or a bad config does not silently orphan a whole library.
type LidarrManager struct {
	client *modules.LidarrClient
}

func (m *LidarrManager) Correlate(filePath, rootDir string) (models.Correlation, error) {
	// No client means we cannot be authoritative, so keep the permissive tag fallback;
	// with a client, Lidarr is the authority and "no match" means unmatched.
	allowTagFallback := m.client == nil
	return modules.ResolveCorrelation(filePath, m.client, rootDir, allowTagFallback)
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
	// Native has no manager to defer to: embedded tags are its only source, so the
	// fallback is not a fallback here — it is the whole resolution.
	return modules.ResolveCorrelation(filePath, nil, rootDir, true)
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
