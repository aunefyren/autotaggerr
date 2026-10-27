package health

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestStateKeyPrefersTheKey: a manager is tracked by its ID, so renaming it is not
// read as one connection disappearing and another appearing.
func TestStateKeyPrefersTheKey(t *testing.T) {
	if got := (service{key: "id-1", name: "Lidarr"}).stateKey(); got != "id-1" {
		t.Errorf("stateKey = %q, want the key", got)
	}
	if got := (service{name: "Plex"}).stateKey(); got != "Plex" {
		t.Errorf("stateKey without a key = %q, want the name", got)
	}
}

// TestProbesIncludeMediaServersAndNameUnnamedManagers: enabled media server rows are
// probed after the manager rows, keyed by row, and a manager saved without a name is
// still shown as something. A disabled server is not probed.
func TestProbesIncludeMediaServersAndNameUnnamedManagers(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.Manager{Name: "  ", Type: models.ManagerTypeLidarr, Enabled: true}).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}
	server := models.MediaServer{Name: "Home Plex", Type: models.MediaServerTypePlex, Enabled: true, BaseURL: "http://plex.invalid:32400", Token: "token"}
	off := models.MediaServer{Name: "Old Plex", Type: models.MediaServerTypePlex, Enabled: false, BaseURL: "http://old.invalid:32400", Token: "token"}
	for _, row := range []*models.MediaServer{&server, &off} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Model(&off).Update("enabled", false)

	c := NewChecker(db)
	got := c.probes()
	if len(got) != 2 {
		t.Fatalf("probes = %+v, want the manager and the enabled server", got)
	}
	if got[0].name != "Lidarr" {
		t.Errorf("unnamed manager probed as %q, want Lidarr", got[0].name)
	}
	if got[1].key != "media:"+server.ID.String() || got[1].name != "Home Plex" {
		t.Errorf("last probe = %+v, want the media server row", got[1])
	}
}

// TestProbesSurviveAnUnreadableDatabase: every probe is a row now, so an unreadable
// database leaves nothing to probe — and that is reported as nothing, not a panic.
func TestProbesSurviveAnUnreadableDatabase(t *testing.T) {
	db := testDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.Close()

	c := NewChecker(db)
	if got := c.probes(); len(got) != 0 {
		t.Errorf("probes = %+v, want none", got)
	}
}
