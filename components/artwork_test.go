package components

import (
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

func artworkTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{
		Type: "sqlite",
		DSN:  filepath.Join(t.TempDir(), "components.db"),
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

func TestArtworkProvidersResolvesEnabledSourcesOnly(t *testing.T) {
	db := artworkTestDB(t)

	if p := ArtworkProviders(db); p.CoverArtEnabled || p.FanartEnabled {
		t.Errorf("empty database resolved providers: %+v", p)
	}
	// The warm pass constructs a runner before it knows there is a database, so a
	// nil handle must answer "nothing configured" rather than panic.
	if p := ArtworkProviders(nil); p.CoverArtEnabled || p.FanartEnabled {
		t.Errorf("nil database resolved providers: %+v", p)
	}

	if err := db.Create(&models.DataSource{
		Name: "Cover Art Archive", Type: models.DataSourceTypeCoverArtArchive,
		BaseURL: "https://caa.example", Enabled: false,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if p := ArtworkProviders(db); p.CoverArtEnabled {
		t.Error("a disabled source resolved as enabled")
	}
}

// "Configured" and "able to deliver" differ for the artist half, and the difference
// matters more than it looks: GetArtwork records a provider that cannot deliver as
// ErrNoArtwork, which is *remembered*, so a warm pass that asked anyway would write
// "no image" for every artist in the collection and keep saying so for a week.
func TestCanServeDistinguishesConfiguredFromUsable(t *testing.T) {
	keyless := modules.ArtworkProviders{FanartEnabled: true}
	if CanServeArtistImages(keyless) {
		t.Error("keyless fanart reported as able to serve artist images")
	}
	if CanServeArtistImages(modules.ArtworkProviders{FanartEnabled: true, FanartAPIKey: "   "}) {
		t.Error("a whitespace-only key reported as usable")
	}
	if !CanServeArtistImages(modules.ArtworkProviders{FanartEnabled: true, FanartAPIKey: "k"}) {
		t.Error("a configured fanart source reported as unusable")
	}

	// Covers need no credential, so enabled is the whole question.
	if !CanServeCovers(modules.ArtworkProviders{CoverArtEnabled: true}) {
		t.Error("an enabled cover source reported as unusable")
	}
	if CanServeCovers(modules.ArtworkProviders{}) {
		t.Error("an absent cover source reported as usable")
	}
}
