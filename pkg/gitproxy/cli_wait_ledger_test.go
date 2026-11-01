package gitproxy

import (
	"testing"
	"time"
)

// Waits report in the order their reports end, not their intervals: a
// wait started after a gap can report before one blocked across it, and
// the earlier wait's time in the gap is still blocked time to credit --
// once, however the reports overlap.
func TestCLIWaitLedgerCreditsTheUnionOfReportedIntervals(t *testing.T) {
	t0 := time.Now()
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	var l cliWaitLedger
	for _, step := range []struct {
		from, to int
		want     time.Duration
	}{
		{0, 90, 90 * time.Second},    // wait A, first report
		{100, 105, 95 * time.Second}, // wait B, started in A's next interval
		{90, 110, 110 * time.Second}, // A again: 90..100 and 105..110 are new
		{95, 108, 110 * time.Second}, // all already counted
		{110, 110, 110 * time.Second},
		{200, 210, 120 * time.Second},
		{105, 205, 210 * time.Second}, // bridges two spans
	} {
		if got := l.add("run", at(step.from), at(step.to)); got != step.want {
			t.Fatalf("add(%d, %d) = %s, want %s", step.from, step.to, got, step.want)
		}
	}
	if n := len(l.covered["run"]); n != 1 {
		t.Errorf("covered %v, want one merged span", l.covered["run"])
	}
	if got := l.add("other", at(0), at(1)); got != time.Second {
		t.Errorf("another run's ledger = %s, want its own 1s", got)
	}
}
