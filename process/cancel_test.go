package process

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// TestCancelStopsTheRunningJob: a job that checks between units of work stops when
// asked, the status says it is stopping while it does, and the queue behind it still
// runs — stopping one run is not clearing the queue.
func TestCancelStopsTheRunningJob(t *testing.T) {
	r := NewRunner(newTestDB(t), models.ConfigStruct{AutotaggerrVersion: "test"})

	if _, ok := r.CancelCurrent(); ok {
		t.Fatal("cancelled something with nothing running")
	}

	started := make(chan struct{})
	stopped := make(chan bool, 1)
	nextRan := make(chan struct{})
	r.enqueue(job{jobRetagAll, "long", "Long job", func() {
		close(started)
		deadline := time.After(5 * time.Second)
		for !r.stopRequested() {
			select {
			case <-deadline:
				stopped <- false
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		if !r.Status().Stopping {
			t.Error("status did not say the job is stopping")
		}
		stopped <- true
	}})
	r.enqueue(job{jobRetagLibrary, "next", "Next job", func() { close(nextRan) }})

	<-started
	view, ok := r.CancelCurrent()
	if !ok || view.Title != "Long job" {
		t.Fatalf("CancelCurrent = %+v, %v", view, ok)
	}
	if !<-stopped {
		t.Fatal("the job never saw the stop")
	}
	select {
	case <-nextRan:
	case <-time.After(5 * time.Second):
		t.Fatal("the job queued behind the stopped one did not run")
	}
	r.Wait()
	// The flag is cleared just after the job is, so give it the moment it takes.
	for i := 0; i < 100 && r.Status().Stopping; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if r.Status().Stopping {
		t.Error("stopping survived the job it was about")
	}
}
