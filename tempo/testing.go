package tempo

import (
	"context"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// NewRunnerForTest builds a running runner whose detector is the given function and
// is reported as installed, so a test in another package (the API handlers) can drive
// the verb without aubiotrack on the machine. Same trade as modules/testing.go: an
// exported symbol that only a caller holding a *testing.T can reach.
func NewRunnerForTest(t *testing.T, db *gorm.DB, detect func(ctx context.Context, path string) (modules.TempoAnalysis, error)) *Runner {
	t.Helper()
	r := newRunner(db, nil, models.ConfigStruct{})
	r.detect = detect
	r.available = func() bool { return true }
	r.concurrency = func() int { return 1 }
	go r.worker()
	return r
}
