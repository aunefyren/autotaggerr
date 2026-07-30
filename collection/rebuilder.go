package collection

import (
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"gorm.io/gorm"
)

// Rebuilder re-derives the collection after something changes the file index,
// coalescing bursts into as few passes as possible.
//
// It exists because the disk view is *derived* state: every writer that changes
// library_items has to re-derive, or the collection silently reports something the
// index no longer says. The scan does this at the end of its run, and applying a
// migration does it too — but manual attach did not, so attaching files by hand
// left the collection stale until the next scan. That gap is why "Rebuild from
// library" had to be a button a user was expected to know about.
//
// Coalescing rather than a plain goroutine per request: attaching a twelve-track
// folder calls the attach path twelve times, and twelve full re-derivations for one
// logical action is absurd. A rebuild already in flight covers work that arrived
// before it started, so a burst collapses to at most two passes — the one running,
// and one more for whatever landed while it ran.
type Rebuilder struct {
	db *gorm.DB

	mu      sync.Mutex
	running bool
	pending bool

	// done is signalled after each pass, for tests that need to wait for one
	// without polling the database.
	done chan struct{}
}

// NewRebuilder builds a rebuilder. A nil db makes Request a no-op, which keeps
// DB-less callers and tests working unchanged.
func NewRebuilder(db *gorm.DB) *Rebuilder {
	return &Rebuilder{db: db, done: make(chan struct{}, 1)}
}

// Request asks for a rebuild and returns immediately. It never blocks the caller:
// an attach is an interactive action, and making the user wait on a full
// re-derivation to see their file marked as matched would be the wrong trade.
//
// Failures are logged, not surfaced. The correlation the caller just saved is the
// real decision and it is already committed; a stale derived view is a display
// problem the next scan or the manual button fixes.
func (r *Rebuilder) Request() {
	if r == nil || r.db == nil {
		return
	}

	r.mu.Lock()
	if r.running {
		// A pass is already in flight. It may have read the index before this
		// caller's write landed, so mark that one more is owed rather than
		// assuming the running one covers it.
		r.pending = true
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()

	go r.loop()
}

func (r *Rebuilder) loop() {
	for {
		if _, _, err := Rebuild(r.db); err != nil {
			logger.Log.Warnf("failed to rebuild collection: %s", err.Error())
		}

		select {
		case r.done <- struct{}{}:
		default:
		}

		r.mu.Lock()
		if !r.pending {
			r.running = false
			r.mu.Unlock()
			return
		}
		r.pending = false
		r.mu.Unlock()
	}
}

// Wait blocks until at least one pass has completed. For tests only — production
// callers deliberately do not wait.
func (r *Rebuilder) Wait() {
	<-r.done
}

// Quiesce blocks until no pass is running or owed. Used at shutdown, and by tests
// so a background pass cannot outlive the database it is writing to.
func (r *Rebuilder) Quiesce() {
	if r == nil {
		return
	}
	for {
		r.mu.Lock()
		idle := !r.running && !r.pending
		r.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
