// Package components implements Autotaggerr's pluggable media-management model:
// Data Sources (metadata providers), Managers (the correlation authority that
// maps a file to a MusicBrainz release/track), and Taggers (tag-writing
// profiles). Each is built from a database row and wraps the lower-level
// operations in modules/, so the scan pipeline can be assembled per library.
package components

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
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

// Advisor is a Manager that can be reachable and still be set up in a way that works
// against Autotaggerr. Advice is "" when there is nothing to say; an error means the
// setting could not be read, which is not the same as the manager being unhealthy.
type Advisor interface {
	Advice() (string, error)
}

// freshCorrelator is a Manager whose answers come from a cache, and which can be told
// that a file changed on disk so the cache is not trusted for it. ProcessFile asks for
// it optionally, so a manager with nothing cached need not implement it.
type freshCorrelator interface {
	CorrelateFresh(filePath, rootDir string) (models.Correlation, error)
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

// MusicBrainzSourceInUse is the MusicBrainz data source every request goes through:
// the oldest enabled one. There is one MusicBrainz client for the whole process, so
// with a local mirror beside the public service exactly one of them is read, and
// "oldest" makes that the same row on every start rather than whichever the database
// returns first. ok is false when no MusicBrainz source is enabled.
func MusicBrainzSourceInUse(db *gorm.DB) (row models.DataSource, ok bool, err error) {
	err = db.Where("type = ? AND enabled = ?", models.DataSourceTypeMusicBrainz, true).
		Order("created_at, id").Limit(1).Find(&row).Error
	return row, err == nil && row.ID != uuid.Nil, err
}

// ApplyMusicBrainzSource pushes the MusicBrainz source in use into the client: its
// base URL, its contact (for the User-Agent) and its request rate. Each of them was
// once editable and stored without being read — the rate until the limiter learned
// it, the base URL and contact until this — so pointing a source at a local mirror
// did nothing. It runs at startup and whenever a data source is created, edited or
// deleted, so a change applies without a restart.
//
// With no source enabled, or a blank URL or a non-positive rate, the client falls
// back to the public service at 1 req/s. Leaving the previous values in place
// instead would keep asking a mirror that was just disabled until the next restart.
func ApplyMusicBrainzSource(db *gorm.DB) {
	if db == nil {
		return
	}
	row, ok, err := MusicBrainzSourceInUse(db)
	if err != nil {
		logger.Log.Warnf("failed to read the MusicBrainz data source: %s", err.Error())
		return
	}
	if !ok {
		modules.SetMusicBrainzEndpoint("", "")
		modules.SetMusicBrainzRateLimit(defaultMusicBrainzRate)
		logger.Log.Infof("no MusicBrainz data source is enabled; using %s at %g req/s", modules.DefaultMusicBrainzBaseURL, defaultMusicBrainzRate)
		return
	}
	rate := row.RateLimit
	if rate <= 0 {
		rate = defaultMusicBrainzRate
	}
	modules.SetMusicBrainzEndpoint(row.BaseURL, row.Contact)
	modules.SetMusicBrainzRateLimit(rate)
	logger.Log.Infof("MusicBrainz requests go to %s at %.3g req/s, from data source %q",
		nameOr(row.BaseURL, modules.DefaultMusicBrainzBaseURL), rate, row.Name)
}

// defaultMusicBrainzRate is what the public service permits.
const defaultMusicBrainzRate = 1.0

func nameOr(s, fallback string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return fallback
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

// CorrelateFresh is Correlate for a file whose bytes changed since the index last saw
// it, resolved against Lidarr answers no older than a couple of minutes (see
// modules.ResolveMetadataDetailsFromLidarrFresh).
func (m *LidarrManager) CorrelateFresh(filePath, rootDir string) (models.Correlation, error) {
	allowTagFallback := m.client == nil
	return modules.ResolveCorrelationFresh(filePath, m.client, rootDir, allowTagFallback)
}

func (m *LidarrManager) HealthCheck() (bool, error) {
	if m.client == nil {
		return false, fmt.Errorf("lidarr client not configured")
	}
	return m.client.HealthCheck()
}

func (m *LidarrManager) Type() string { return models.ManagerTypeLidarr }

// Advice reports Lidarr being set to rewrite tags on files it already imported, which
// is reachable, working, and still wrong alongside Autotaggerr (see
// models.LidarrWriteAudioTags.RewritesExistingFiles).
func (m *LidarrManager) Advice() (string, error) {
	if m.client == nil {
		return "", nil
	}
	cfg, err := m.client.GetMetadataProviderConfig()
	if err != nil {
		return "", err
	}
	return modules.LidarrTagWritingAdvice(cfg.WriteAudioTags), nil
}

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
		// Say which credentials a scan is about to use. These come from the manager
		// *row*, which is a different copy from the config.json values the startup
		// health checker probes — so "Lidarr is healthy" and "every file fails to
		// resolve" can both be true, and the log has to make that visible.
		logger.Log.Debugf("Lidarr manager %q: base URL %q, API key set: %t, cookie set: %t, client built: %t",
			row.Name, row.LidarrBaseURL, row.LidarrAPIKey != "", row.LidarrHeaderCookie != "", client != nil)
		return &LidarrManager{client: client}, nil
	case models.ManagerTypeAutotaggerr:
		return &AutotaggerrManager{}, nil
	default:
		return nil, fmt.Errorf("unsupported manager type %q", row.Type)
	}
}

// --- Tagger -----------------------------------------------------------------

// Tagger applies a TaggerProfile's settings when writing tags. There is one
// built-in engine (modules' FLAC/MP3 writers); the profile only tunes it. The BPM
// settings are the library's, merged in by ForLibrary.
type Tagger struct {
	profile          models.TaggerProfile
	bpmWrite         string
	bpmMinConfidence float64
}

// NewTagger builds a Tagger from its profile row. It writes no BPM until ForLibrary
// gives it a library's BPM settings.
func NewTagger(profile models.TaggerProfile) *Tagger {
	return &Tagger{profile: profile, bpmMinConfidence: models.DefaultBPMMinConfidence}
}

// ForLibrary takes the library's BPM settings. Every path that tags a library's files
// builds its tagger through this, so the settings cannot be dropped by one of them.
func (t *Tagger) ForLibrary(library models.Library) *Tagger {
	t.bpmWrite = library.BPMWrite
	t.bpmMinConfidence = library.BPMMinConfidence
	if t.bpmMinConfidence <= 0 {
		t.bpmMinConfidence = models.DefaultBPMMinConfidence
	}
	return t
}

// Naming is the profile's naming templates, defaults filled in. It is not part of
// Settings or the Fingerprint: where a file lives changes nothing about its tags.
func (t *Tagger) Naming() models.NamingTemplates { return t.profile.Naming() }

// WriteEnabled reports whether this profile writes tags at all.
func (t *Tagger) WriteEnabled() bool { return t.profile.WriteTags }

// Settings is what the tag writers read: the profile's knobs plus the library's BPM
// settings.
func (t *Tagger) Settings() models.TaggerSettings {
	s := t.profile.Settings()
	s.BPMWrite = t.bpmWrite
	s.BPMMinConfidence = t.bpmMinConfidence
	return s
}

// WritesBPM reports whether this tagger puts the stored tempo into files at all.
func (t *Tagger) WritesBPM() bool { return t.WriteEnabled() && t.bpmWrite != models.BPMWriteOff }

// Fingerprint identifies everything that decides what this tagger writes: whether it
// writes, and every setting the writers read. Skip-unchanged compares it with the one
// recorded on the file (LibraryItem.TaggedWith), so a file is not walked past after
// the settings that tagged it have changed. Nothing about the *file* changes when a
// profile does, so without this only Tag files ever applied a profile edit.
//
// Hashed from the settings struct itself, so a setting added later is covered without
// anyone remembering to list it here.
func (t *Tagger) Fingerprint() string {
	b, err := json.Marshal(struct {
		Write    bool
		Settings models.TaggerSettings
	}{t.WriteEnabled(), t.Settings()})
	if err != nil {
		return "" // never matches a stored fingerprint, so the file is processed
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
