package daemon

import (
	"errors"
	"testing"
	"time"
)

// A sync that keeps failing is reported, outside the journal, once it has
// been failing for syncFailureGrace -- with the time it started and the
// latest reason -- and stops being reported on the first clean cycle.
// grain-eu-host's settings went 36 hours without an export and the only
// trace was one journal line.
func TestStateSyncFailureIsReportedUntilASyncSucceeds(t *testing.T) {
	m := &StateManager{}
	m.noteOutcome(nil)
	if got := m.syncFailure(); got != nil {
		t.Fatalf("a working sync reported a failure: %+v", got)
	}
	m.noteOutcome(errors.New("first"))
	if got := m.syncFailure(); got != nil {
		t.Fatalf("a failure younger than the grace period was reported: %+v", got)
	}
	// Age the run of failures past the grace period.
	f := m.failure.Load()
	started := time.Now().Add(-2 * syncFailureGrace)
	f.Since = started
	m.noteOutcome(errors.New("ERROR: syntax error at or near \"$2\""))
	got := m.syncFailure()
	if got == nil {
		t.Fatal("a sync failing for longer than the grace period was not reported")
	}
	if !got.Since.Equal(started) {
		t.Errorf("since = %v, want the first failure's %v", got.Since, started)
	}
	if got.Error != "ERROR: syntax error at or near \"$2\"" {
		t.Errorf("error = %q, want the latest", got.Error)
	}
	if got.Repo == "" {
		t.Error("the report does not name the repository")
	}
	m.noteOutcome(nil)
	if got := m.syncFailure(); got != nil {
		t.Fatalf("a sync that succeeded is still reported failing: %+v", got)
	}
}
