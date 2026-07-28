package events

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
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

func TestBeginFinish(t *testing.T) {
	db := testDB(t)

	ev := Begin(db, models.EventTypeScan, "Library scan")
	if ev.ID == uuid.Nil {
		t.Fatal("Begin did not assign an id")
	}
	if ev.Status != models.EventStatusRunning {
		t.Errorf("status = %q, want running", ev.Status)
	}

	Finish(db, ev, models.EventStatusOK, "3 processed", map[string]any{"changed": 2})

	var got models.Event
	if err := db.First(&got, "id = ?", ev.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != models.EventStatusOK || got.FinishedAt == nil {
		t.Errorf("event not finished: %+v", got)
	}
	if got.Summary != "3 processed" {
		t.Errorf("summary = %q", got.Summary)
	}
	// JSON numbers decode as float64.
	if v, ok := got.Details["changed"].(float64); !ok || v != 2 {
		t.Errorf("details did not round-trip: %#v", got.Details)
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	db := testDB(t)
	for i := 0; i < 5; i++ {
		Finish(db, Begin(db, models.EventTypeScan, "scan"), models.EventStatusOK, "", nil)
	}
	Prune(db, 2)

	var n int64
	if err := db.Model(&models.Event{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("after prune count = %d, want 2", n)
	}
}
