// Package health probes the configured external connections (Lidarr, Plex) on a
// schedule and records the result in the Activity feed. It records an event only when
// a connection's health *changes*, so a frequent cadence does not bury the feed under
// identical "healthy" rows — the state, not the heartbeat, is what is worth keeping.
package health

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// eventRetention caps how many Activity events are kept after a health check, matching
// the other event-recording paths.
const eventRetention = 200

// service is one monitored connection and the probe that reports its health. The key
// identifies it across runs (a manager's ID, which survives a rename); the name is what
// the feed shows. A service built without a key is identified by its name.
type service struct {
	key   string
	name  string
	check func() (bool, error)
}

func (s service) stateKey() string {
	if s.key != "" {
		return s.key
	}
	return s.name
}

// Checker holds the monitored services and the last health seen for each, so a run can
// tell a state change from a steady state. Its methods are safe for a scheduler and a
// startup call to share.
type Checker struct {
	db       *gorm.DB
	services []service // static probes; manager probes are read from the DB per run

	mu   sync.Mutex
	last map[string]bool // service key -> healthy at last check; absent = never checked
	seen bool            // whether any check has run this process
}

// NewChecker builds a checker for the Plex client plus whatever managers the database
// holds. Unlike the static clients, managers are read per run (see probes), so it
// returns a usable checker whenever there is a database to read — a manager added or
// re-credentialed later is picked up without a restart. Returns nil only without a DB,
// and the nil-receiver Run makes that a no-op the caller need not special-case.
func NewChecker(db *gorm.DB, plex *modules.PlexClient) *Checker {
	if db == nil {
		return nil
	}
	var svcs []service
	if plex != nil {
		svcs = append(svcs, service{key: "plex", name: "Plex", check: plex.HealthCheck})
	}
	return &Checker{db: db, services: svcs, last: map[string]bool{}}
}

// probes lists the connections to check, rebuilt on every run.
//
// Lidarr is probed through the enabled manager *rows*, constructed with the very same
// components.NewManager the pipeline calls, so the check authenticates with exactly the
// credentials a scan will use. It used to probe a client built in main from
// files.ConfigFile instead — a second, independent copy of the same credentials that
// config.json stops feeding the moment the manager row exists. The two drifted the
// first time either side was edited alone, and the failure that produced was the worst
// kind: a green "Lidarr healthy" beside a library where every single file failed to
// resolve. Reading the rows per run is also what makes a cookie pasted into the UI take
// effect on the next check rather than the next restart.
func (c *Checker) probes() []service {
	var rows []models.Manager
	if err := c.db.Where("enabled = ? AND type = ?", true, models.ManagerTypeLidarr).
		Order("name").Find(&rows).Error; err != nil {
		logger.Log.Warnf("health check could not read manager rows: %s", err.Error())
	}

	svcs := make([]service, 0, len(rows)+len(c.services))
	for _, row := range rows {
		manager, err := components.NewManager(row)
		if err != nil {
			logger.Log.Warnf("health check skipping manager %q: %s", row.Name, err.Error())
			continue
		}
		name := strings.TrimSpace(row.Name)
		if name == "" {
			name = "Lidarr"
		}
		svcs = append(svcs, service{key: row.ID.String(), name: name, check: manager.HealthCheck})
	}
	return append(svcs, c.services...)
}

// Run probes every monitored service and records one health event when any service's
// health has changed since the last run — always on the first run, to lay down a
// baseline. Every probe is logged regardless; only the event is state-gated.
func (c *Checker) Run() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	svcs := c.probes()
	if len(svcs) == 0 {
		// Nothing configured to probe. An event listing no services would say nothing,
		// and forgetting the previous state means a connection configured later gets a
		// fresh baseline rather than being compared against a stale one.
		c.last = map[string]bool{}
		return
	}

	type result struct {
		healthy bool
		err     error
	}
	results := make(map[string]result, len(svcs))
	// A probe appearing or disappearing is itself a change worth a row: a manager was
	// added, deleted or disabled, and the feed should say so.
	current := make(map[string]bool, len(svcs))
	changed := !c.seen || len(svcs) != len(c.last)
	for _, s := range svcs {
		healthy, err := s.check()
		if err != nil {
			healthy = false
		}
		results[s.name] = result{healthy: healthy, err: err}
		if prev, ok := c.last[s.stateKey()]; !ok || prev != healthy {
			changed = true
		}
		current[s.stateKey()] = healthy

		switch {
		case healthy:
			logger.Log.Infof("%s connection is healthy", s.name)
		case err != nil:
			logger.Log.Errorf("%s health check failed: %s", s.name, err.Error())
		default:
			logger.Log.Errorf("%s connection is unhealthy", s.name)
		}
	}
	c.last = current
	c.seen = true

	if !changed {
		return
	}

	// Stable order so the summary reads the same run to run.
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)

	allHealthy := true
	parts := make([]string, 0, len(names))
	states := make(map[string]any, len(names))
	for _, name := range names {
		r := results[name]
		state := "healthy"
		if !r.healthy {
			allHealthy = false
			state = "unreachable"
		}
		parts = append(parts, fmt.Sprintf("%s %s", name, state))
		entry := map[string]any{"healthy": r.healthy}
		if r.err != nil {
			entry["error"] = r.err.Error()
		}
		states[name] = entry
	}

	status := models.EventStatusOK
	if !allHealthy {
		status = models.EventStatusError
	}
	// One counter per connection, so the detail view answers "which one" rather than
	// leaving it to the summary line. A connection is healthy or it is not, so the
	// value is the honest 1/0 and the emphasis carries the meaning.
	stats := make([]models.EventStat, 0, len(names))
	for _, name := range names {
		stat := models.EventStat{Label: name, Value: 1}
		if !results[name].healthy {
			stat.Value = 0
			stat.Kind = models.EventStatBad
		}
		stats = append(stats, stat)
	}

	event := events.Begin(c.db, models.EventTypeHealth, "Health check")
	event.Stats = stats
	events.Finish(c.db, event, status, strings.Join(parts, " · "), map[string]any{"services": states})
	events.Prune(c.db, eventRetention)
}
