package settings

import (
	"context"
	"sync"

	"codnect.io/chrono"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/sirupsen/logrus"
)

// CronJob is one schedule the Runtime owns: what to run, and where in the config its
// expression and its on/off switch live. Owning the schedules here — rather than
// scheduling them inline at startup — is what makes a saved schedule take effect
// without a restart: the same description is used to schedule them the first time and
// to re-schedule them afterwards.
type CronJob struct {
	// Name identifies the job in logs and in the applied/deferred report.
	Name string
	// Run is the work itself. It must be safe to call from the scheduler's goroutine.
	Run func()
	// Schedule reads this job's cron expression out of the config.
	Schedule func(models.ConfigStruct) string
	// Enabled reads whether the job should be scheduled at all. Nil means always.
	Enabled func(models.ConfigStruct) bool
}

func (j CronJob) enabled(cfg models.ConfigStruct) bool {
	return j.Enabled == nil || j.Enabled(cfg)
}

// Runtime applies saved settings to the running process. Everything it can change is
// something the process reads through a handle it owns: the scheduler's tasks, the
// logger's level, the scan runner's worker count. Anything else is TierRestart, and
// the honesty of that split is the point — a settings page that claims to have
// applied a port change has lied to the user.
//
// A nil *Runtime is usable and does nothing, so a caller without a scheduler (tests,
// the one-shot file mode) needs no branch.
type Runtime struct {
	mu        sync.Mutex
	scheduler chrono.TaskScheduler
	jobs      []CronJob
	tasks     map[string]chrono.ScheduledTask

	// setConcurrency is the scan runner's worker-count setter. Kept as a function so
	// this package does not depend on the scan package (which depends on most of the
	// app), and so a test can observe the call.
	setConcurrency func(int)
}

// NewRuntime builds the applier. setConcurrency may be nil.
func NewRuntime(scheduler chrono.TaskScheduler, setConcurrency func(int), jobs ...CronJob) *Runtime {
	return &Runtime{
		scheduler:      scheduler,
		jobs:           jobs,
		tasks:          map[string]chrono.ScheduledTask{},
		setConcurrency: setConcurrency,
	}
}

// Schedule installs every job for the given config. It is the startup path and is
// idempotent: calling it again cancels what it scheduled before, which is exactly
// what Apply needs.
func (r *Runtime) Schedule(cfg models.ConfigStruct) {
	if r == nil || r.scheduler == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, job := range r.jobs {
		r.rescheduleLocked(job, cfg)
	}
}

// Apply pushes cfg onto the running process and reports what it did, in the caller's
// words: one short sentence per thing changed. Settings that cannot be applied live
// are not its business — Save decides that from the field tiers.
func (r *Runtime) Apply(cfg models.ConfigStruct, changed []string) []string {
	applied := []string{}

	for _, key := range changed {
		switch key {
		case "autotaggerr_log_level":
			if level, err := logrus.ParseLevel(cfg.AutotaggerrLogLevel); err == nil {
				logger.Log.SetLevel(level)
				applied = append(applied, "log level is now "+cfg.AutotaggerrLogLevel)
			}
		case "autotaggerr_process_concurrency":
			if r != nil && r.setConcurrency != nil {
				r.setConcurrency(cfg.AutotaggerrProcessConcurrency)
				applied = append(applied, "new scans will use the new worker count")
			}
		}
	}

	if r != nil && r.scheduler != nil {
		applied = append(applied, r.applySchedules(cfg, changed)...)
	}
	return applied
}

// applySchedules re-installs any job whose expression or on/off switch changed.
func (r *Runtime) applySchedules(cfg models.ConfigStruct, changed []string) []string {
	touched := map[string]bool{}
	for _, key := range changed {
		touched[key] = true
	}

	scheduleKeys := map[string]string{
		"autotaggerr_process_cron_schedule": "scan",
		"autotaggerr_mirror_cron_schedule":  "metadata refresh",
		"autotaggerr_health_cron_schedule":  "health check",
		"autotaggerr_mirror_enabled":        "metadata refresh",
	}

	needed := map[string]bool{}
	for key, job := range scheduleKeys {
		if touched[key] {
			needed[job] = true
		}
	}
	if len(needed) == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	applied := []string{}
	for _, job := range r.jobs {
		if !needed[job.Name] {
			continue
		}
		r.rescheduleLocked(job, cfg)
		if job.enabled(cfg) {
			applied = append(applied, "the "+job.Name+" schedule was reset to "+job.Schedule(cfg))
		} else {
			applied = append(applied, "the "+job.Name+" schedule was stopped")
		}
	}
	return applied
}

// rescheduleLocked cancels a job's current task, if any, and installs a new one when
// the job is enabled. Callers hold r.mu.
//
// A schedule that fails to install is logged and left uninstalled rather than
// retried: the expression was validated before it was saved, so a failure here is a
// scheduler problem, and silently keeping the old task would make the page's report
// wrong in the other direction.
func (r *Runtime) rescheduleLocked(job CronJob, cfg models.ConfigStruct) {
	if task, ok := r.tasks[job.Name]; ok && task != nil {
		task.Cancel()
		delete(r.tasks, job.Name)
	}
	if !job.enabled(cfg) {
		logger.Log.Infof("%s schedule is off", job.Name)
		return
	}

	expression := job.Schedule(cfg)
	task, err := r.scheduler.ScheduleWithCron(func(ctx context.Context) { job.Run() }, expression)
	if err != nil {
		logger.Log.Errorf("failed to schedule the %s job with %q: %s", job.Name, expression, err.Error())
		return
	}
	r.tasks[job.Name] = task
	logger.Log.Infof("%s scheduled with %q", job.Name, expression)
}
