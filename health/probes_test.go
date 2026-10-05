package health

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
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

// TestProbesIncludePlexAndNameUnnamedManagers: the static Plex probe follows the
// manager rows, and a manager saved without a name is still shown as something.
func TestProbesIncludePlexAndNameUnnamedManagers(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.Manager{Name: "  ", Type: models.ManagerTypeLidarr, Enabled: true}).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}

	c := NewChecker(db, modules.NewPlexClient("http://plex.invalid:32400", "token"))
	got := c.probes()
	if len(got) != 2 {
		t.Fatalf("probes = %+v, want the manager and Plex", got)
	}
	if got[0].name != "Lidarr" {
		t.Errorf("unnamed manager probed as %q, want Lidarr", got[0].name)
	}
	if got[1].key != "plex" || got[1].name != "Plex" {
		t.Errorf("last probe = %+v, want Plex", got[1])
	}
}

// TestProbesSurviveAnUnreadableDatabase: losing the manager rows must not lose the
// probes that do not depend on them.
func TestProbesSurviveAnUnreadableDatabase(t *testing.T) {
	db := testDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.Close()

	c := NewChecker(db, modules.NewPlexClient("http://plex.invalid:32400", "token"))
	if got := c.probes(); len(got) != 1 || got[0].name != "Plex" {
		t.Errorf("probes = %+v, want only Plex", got)
	}
}
