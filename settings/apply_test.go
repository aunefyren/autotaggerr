package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"codnect.io/chrono"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

// countingJobs builds the three real jobs over counters, so a test can assert which
// schedules were installed without waiting for one to fire.
type jobRecorder struct {
	mu      sync.Mutex
	runs    map[string]int
	workers []int
}

func (r *jobRecorder) note(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[name]++
}

func newRuntimeForTest(t *testing.T) (*Runtime, *jobRecorder) {
	t.Helper()
	recorder := &jobRecorder{runs: map[string]int{}}
	scheduler := chrono.NewDefaultTaskScheduler()
	t.Cleanup(func() { <-scheduler.Shutdown() })

	runtime := NewRuntime(scheduler,
		func(n int) {
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			recorder.workers = append(recorder.workers, n)
		},
		CronJob{
			Name:     "scan",
			Run:      func() { recorder.note("scan") },
			Schedule: func(c models.ConfigStruct) string { return c.AutotaggerrProcessCronSchedule },
		},
		CronJob{
			Name:     "metadata refresh",
			Run:      func() { recorder.note("mirror") },
			Schedule: func(c models.ConfigStruct) string { return c.AutotaggerrMirrorCronSchedule },
			Enabled:  func(c models.ConfigStruct) bool { return !c.AutotaggerrMirrorDisabled },
		},
	)
	return runtime, recorder
}

func TestRuntimeSchedulesAndReschedules(t *testing.T) {
	runtime, _ := newRuntimeForTest(t)
	cfg := baseConfig()

	runtime.Schedule(cfg)
	runtime.mu.Lock()
	installed := len(runtime.tasks)
	runtime.mu.Unlock()
	if installed != 2 {
		t.Fatalf("installed %d tasks, want 2", installed)
	}

	// Changing the expression replaces the task rather than adding a second one — two
	// live tasks for one job would run the scan twice a night.
	cfg.AutotaggerrProcessCronSchedule = "0 30 4 * * *"
	applied := runtime.Apply(cfg, []string{"autotaggerr_process_cron_schedule"})
	if len(applied) != 1 || !strings.Contains(applied[0], "0 30 4 * * *") {
		t.Fatalf("applied = %v, want the new scan schedule", applied)
	}
	runtime.mu.Lock()
	stillInstalled := len(runtime.tasks)
	runtime.mu.Unlock()
	if stillInstalled != 2 {
		t.Errorf("tasks after reschedule = %d, want 2", stillInstalled)
	}
}

// TestRuntimeStopsDisabledJob covers the mirror switch: turning it off has to cancel
// the task, not just record the preference for the next restart.
func TestRuntimeStopsDisabledJob(t *testing.T) {
	runtime, _ := newRuntimeForTest(t)
	cfg := baseConfig()
	runtime.Schedule(cfg)

	cfg.AutotaggerrMirrorDisabled = true
	applied := runtime.Apply(cfg, []string{"autotaggerr_mirror_enabled"})
	if len(applied) != 1 || !strings.Contains(applied[0], "stopped") {
		t.Fatalf("applied = %v, want the refresh to be reported as stopped", applied)
	}

	runtime.mu.Lock()
	_, stillThere := runtime.tasks["metadata refresh"]
	runtime.mu.Unlock()
	if stillThere {
		t.Error("the disabled job kept its scheduled task")
	}
}

func TestRuntimeAppliesLogLevelAndConcurrency(t *testing.T) {
	runtime, recorder := newRuntimeForTest(t)
	original := logger.Log.GetLevel()
	t.Cleanup(func() { logger.Log.SetLevel(original) })

	cfg := baseConfig()
	cfg.AutotaggerrLogLevel = "debug"
	cfg.AutotaggerrProcessConcurrency = 9

	applied := runtime.Apply(cfg, []string{"autotaggerr_log_level", "autotaggerr_process_concurrency"})
	if len(applied) != 2 {
		t.Fatalf("applied = %v, want both", applied)
	}
	if logger.Log.GetLevel().String() != "debug" {
		t.Errorf("log level = %s, want debug", logger.Log.GetLevel())
	}
	recorder.mu.Lock()
	workers := recorder.workers
	recorder.mu.Unlock()
	if len(workers) != 1 || workers[0] != 9 {
		t.Errorf("concurrency calls = %v, want [9]", workers)
	}
}

// TestNilRuntimeIsUsable: a caller without a scheduler (the one-shot file mode, a
// test) must not need a nil check to save settings. Process-global effects still
// apply — the logger is a package global, not something the runtime owns — while
// anything needing the scheduler or the scan runner is skipped.
func TestNilRuntimeIsUsable(t *testing.T) {
	original := logger.Log.GetLevel()
	t.Cleanup(func() { logger.Log.SetLevel(original) })

	var runtime *Runtime
	runtime.Schedule(baseConfig())

	cfg := baseConfig()
	cfg.AutotaggerrLogLevel = "error"
	applied := runtime.Apply(cfg, []string{"autotaggerr_log_level", "autotaggerr_process_concurrency",
		"autotaggerr_process_cron_schedule"})

	if len(applied) != 1 || !strings.Contains(applied[0], "error") {
		t.Fatalf("applied = %v, want only the log level", applied)
	}
	if logger.Log.GetLevel().String() != "error" {
		t.Errorf("log level = %s, want error", logger.Log.GetLevel())
	}
}

// TestSaveWritesAndApplies is the whole path: validate, persist, then apply. The
// persisted file is what the next start reads, so it is asserted as JSON rather than
// through the package's own loader.
func TestSaveWritesAndApplies(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(files.SetConfigPaths(dir))

	previous := files.ConfigFile
	t.Cleanup(func() { files.ConfigFile = previous })
	files.ConfigFile = baseConfig()

	runtime, _ := newRuntimeForTest(t)
	runtime.Schedule(files.ConfigFile)

	result, err := Save(runtime, values(map[string]any{
		"autotaggerr_log_level": "warning",
		"autotaggerr_port":      9090,
	}))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(result.Changed) != 2 {
		t.Errorf("changed = %v", result.Changed)
	}
	if len(result.Applied) != 1 || !strings.Contains(result.Applied[0], "warning") {
		t.Errorf("applied = %v, want the log level", result.Applied)
	}
	if len(result.RestartRequired) != 1 || result.RestartRequired[0] != "autotaggerr_port" {
		t.Errorf("restart required = %v, want the port", result.RestartRequired)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("config.json was not written: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("config.json is not valid JSON: %v", err)
	}
	if onDisk["autotaggerr_port"] != float64(9090) {
		t.Errorf("port on disk = %v, want 9090", onDisk["autotaggerr_port"])
	}
	if onDisk["autotaggerr_log_level"] != "warning" {
		t.Errorf("log level on disk = %v", onDisk["autotaggerr_log_level"])
	}
	// The process global and the file must agree, or a restart silently reverts.
	if files.ConfigFile.AutotaggerrPort != 9090 {
		t.Error("the running config was not updated")
	}
}

// TestSaveRejectionLeavesEverythingAlone: a bad value must not write the file or move
// the process global.
func TestSaveRejectionLeavesEverythingAlone(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(files.SetConfigPaths(dir))

	previous := files.ConfigFile
	t.Cleanup(func() { files.ConfigFile = previous })
	files.ConfigFile = baseConfig()

	if _, err := Save(nil, values(map[string]any{"autotaggerr_port": -1})); err == nil {
		t.Fatal("expected a rejection")
	}
	if files.ConfigFile.AutotaggerrPort != 8080 {
		t.Error("a rejected save changed the running config")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Error("a rejected save wrote config.json")
	}
}

// TestSaveNoOp: re-sending stored values is not a change, and must not rewrite the
// file or report a restart the user does not need.
func TestSaveNoOp(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(files.SetConfigPaths(dir))

	previous := files.ConfigFile
	t.Cleanup(func() { files.ConfigFile = previous })
	files.ConfigFile = baseConfig()

	result, err := Save(nil, values(map[string]any{"autotaggerr_port": 8080}))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(result.Changed) != 0 || len(result.RestartRequired) != 0 {
		t.Errorf("result = %+v, want nothing changed", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Error("a no-op save rewrote config.json")
	}
}

func TestReveal(t *testing.T) {
	previous := files.ConfigFile
	t.Cleanup(func() { files.ConfigFile = previous })
	files.ConfigFile = baseConfig()

	value, err := Reveal("smtp_password")
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if value != "hunter2" {
		t.Errorf("revealed %q", value)
	}

	// The signing key is a secret the page shows as "set" and never hands back:
	// it is not editable, so revealing it serves nothing the user can act on.
	if _, err := Reveal("private_key"); err == nil {
		t.Error("the private key must not be revealable")
	}
	if _, err := Reveal("autotaggerr_port"); err == nil {
		t.Error("a non-secret must not be revealable")
	}
	if _, err := Reveal("nope"); err == nil {
		t.Error("an unknown key must not be revealable")
	}
}

// TestRuntimeStopCancelsEverySchedule: once the process is shutting down, a cron that
// fires would queue work that is about to be dropped. Stop is also idempotent and
// nil-safe, because shutdown paths are exactly where a second call is likely.
func TestRuntimeStopCancelsEverySchedule(t *testing.T) {
	runtime, _ := newRuntimeForTest(t)
	runtime.Schedule(baseConfig())

	runtime.mu.Lock()
	installed := len(runtime.tasks)
	runtime.mu.Unlock()
	if installed == 0 {
		t.Fatal("nothing was scheduled, so cancelling proves nothing")
	}

	runtime.Stop()

	runtime.mu.Lock()
	remaining := len(runtime.tasks)
	runtime.mu.Unlock()
	if remaining != 0 {
		t.Errorf("%d task(s) left after Stop, want 0", remaining)
	}

	runtime.Stop()         // idempotent
	(*Runtime)(nil).Stop() // nil-safe, like the rest of Runtime
}
