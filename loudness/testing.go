package loudness

import (
	"context"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// NewRunnerForTest builds a running runner whose analyser is the given function and is
// reported as installed, so the API handlers' tests can drive the verb without rsgain —
// tempo.NewRunnerForTest's trade.
func NewRunnerForTest(t *testing.T, db *gorm.DB, analyse func(ctx context.Context, paths []string) (modules.AlbumLoudness, error)) *Runner {
	t.Helper()
	r := newRunner(db, nil, models.ConfigStruct{})
	if analyse != nil {
		r.analyse = analyse
	}
	r.available = func() bool { return true }
	go r.worker()
	return r
}
