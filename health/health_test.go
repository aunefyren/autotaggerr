package health

import (
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func init() {
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

func healthEvents(t *testing.T, db *gorm.DB) []models.Event {
	t.Helper()
	var evs []models.Event
	if err := db.Where("type = ?", models.EventTypeHealth).Order("started_at").Find(&evs).Error; err != nil {
		t.Fatalf("load health events: %v", err)
	}
	return evs
}

// The point of the state gate: a five-minute cadence must not record an event every
// tick, only when a connection's health actually flips (with a baseline on first run).
func TestCheckerRecordsOnlyOnStateChange(t *testing.T) {
	db := testDB(t)
	healthy := true
	c := &Checker{db: db, last: map[string]bool{}, services: []service{
		{name: "Lidarr", check: func() (bool, error) { return healthy, nil }},
	}}

	c.Run() // first run: baseline
	if n := len(healthEvents(t, db)); n != 1 {
		t.Fatalf("after first run: %d events, want 1", n)
	}
	if got := healthEvents(t, db)[0].Status; got != models.EventStatusOK {
		t.Errorf("baseline status = %q, want ok", got)
	}

	c.Run() // unchanged: no event
	if n := len(healthEvents(t, db)); n != 1 {
		t.Fatalf("after unchanged run: %d events, want 1", n)
	}

	healthy = false
	c.Run() // flipped to unhealthy: event
	evs := healthEvents(t, db)
	if len(evs) != 2 {
		t.Fatalf("after state change: %d events, want 2", len(evs))
	}
	if evs[1].Status != models.EventStatusError {
		t.Errorf("unhealthy event status = %q, want error", evs[1].Status)
	}

	c.Run() // still unhealthy: no event
	if n := len(healthEvents(t, db)); n != 2 {
		t.Errorf("after steady unhealthy: %d events, want 2", n)
	}
}

// A probe error is an unhealthy result whose message is kept in the event details.
func TestCheckerRecordsProbeError(t *testing.T) {
	db := testDB(t)
	c := &Checker{db: db, last: map[string]bool{}, services: []service{
		{name: "Plex", check: func() (bool, error) { return false, errors.New("connection refused") }},
	}}
	c.Run()

	evs := healthEvents(t, db)
	if len(evs) != 1 || evs[0].Status != models.EventStatusError {
		t.Fatalf("want one error event, got %+v", evs)
	}
	services, _ := evs[0].Details["services"].(map[string]any)
	plex, _ := services["Plex"].(map[string]any)
	if plex == nil || plex["error"] != "connection refused" {
		t.Errorf("probe error not recorded in details: %#v", evs[0].Details)
	}
}

// A checker with nothing to probe is still built, because managers live in the database
// and one can be added at any time — returning nil would freeze the decision at boot.
// The run is what turns into a no-op, without recording an event about no services.
func TestNewCheckerWithNothingToProbe(t *testing.T) {
	db := testDB(t)
	c := NewChecker(db, nil)
	if c == nil {
		t.Fatal("NewChecker with a database = nil, want a usable checker")
	}
	c.Run()
	if n := len(healthEvents(t, db)); n != 0 {
		t.Errorf("a run with no services recorded %d events, want 0", n)
	}

	if c := NewChecker(nil, nil); c != nil {
		t.Errorf("NewChecker without a database = %v, want nil", c)
	}
	// A nil checker's Run must be a safe no-op.
	var nilChecker *Checker
	nilChecker.Run()
}

// The probe list comes from the manager rows, so the credentials checked are the ones a
// scan would use. A row edited between runs is picked up without a restart — the whole
// point of reading them per run instead of at boot.
func TestCheckerProbesManagerRows(t *testing.T) {
	db := testDB(t)
	row := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: "http://lidarr.invalid", LidarrAPIKey: "key"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}

	c := NewChecker(db, nil)
	if got := c.probes(); len(got) != 1 || got[0].name != "Lidarr" || got[0].key != row.ID.String() {
		t.Fatalf("probes = %+v, want one keyed by the manager ID", got)
	}

	// A disabled manager is not probed: it governs nothing, so its credentials are not
	// a fact about whether this install works.
	row.Enabled = false
	if err := db.Save(&row).Error; err != nil {
		t.Fatalf("save manager: %v", err)
	}
	if got := c.probes(); len(got) != 0 {
		t.Errorf("probes after disabling = %+v, want none", got)
	}
}
