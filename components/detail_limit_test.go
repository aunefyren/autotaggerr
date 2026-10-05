package components

import "testing"

// TestDetailCollectorLimit: the collector reports the cap it was built with, and a
// nil collector (a run that records no detail) reports zero rather than panicking.
func TestDetailCollectorLimit(t *testing.T) {
	if got := NewDetailCollector(500).Limit(); got != 500 {
		t.Errorf("Limit() = %d, want 500", got)
	}
	var none *DetailCollector
	if got := none.Limit(); got != 0 {
		t.Errorf("nil Limit() = %d, want 0", got)
	}
}
