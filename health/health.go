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

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// eventRetention caps how many Activity events are kept after a health check, matching
// the other event-recording paths.
const eventRetention = 200

// service is one monitored connection and the probe that reports its health.
type service struct {
	name  string
	check func() (bool, error)
}

// Checker holds the monitored services and the last health seen for each, so a run can
// tell a state change from a steady state. Its methods are safe for a scheduler and a
// startup call to share.
type Checker struct {
	db       *gorm.DB
	services []service

	mu   sync.Mutex
	last map[string]bool // service name -> healthy at last check; absent = never checked
	seen bool            // whether any check has run this process
}

// NewChecker builds a checker for whichever of Lidarr/Plex are configured; a nil
// client is simply not monitored. Returns nil when nothing is configured, and the
// nil-receiver Run makes that a no-op the caller need not special-case.
func NewChecker(db *gorm.DB, lidarr *modules.LidarrClient, plex *modules.PlexClient) *Checker {
	var svcs []service
	if lidarr != nil {
		svcs = append(svcs, service{name: "Lidarr", check: lidarr.HealthCheck})
	}
	if plex != nil {
		svcs = append(svcs, service{name: "Plex", check: plex.HealthCheck})
	}
	if len(svcs) == 0 {
		return nil
	}
	return &Checker{db: db, services: svcs, last: map[string]bool{}}
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

	type result struct {
		healthy bool
		err     error
	}
	results := make(map[string]result, len(c.services))
	changed := !c.seen // the first run always records a baseline
	for _, s := range c.services {
		healthy, err := s.check()
		if err != nil {
			healthy = false
		}
		results[s.name] = result{healthy: healthy, err: err}
		if prev, ok := c.last[s.name]; !ok || prev != healthy {
			changed = true
		}
		c.last[s.name] = healthy

		switch {
		case healthy:
			logger.Log.Infof("%s connection is healthy", s.name)
		case err != nil:
			logger.Log.Errorf("%s health check failed: %s", s.name, err.Error())
		default:
			logger.Log.Errorf("%s connection is unhealthy", s.name)
		}
	}
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
