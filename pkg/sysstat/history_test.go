package sysstat_test

// The hourly rollups behind the metrics report's host series
// (grain/task-495): what a fold of readings says, and what it costs.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/sysstat"
)

func TestHistoryFoldsReadingsIntoTheHourTheyWereTakenIn(t *testing.T) {
	t.Parallel()
	h := sysstat.NewHistory(0)
	hour := time.Date(2026, 9, 21, 20, 0, 0, 0, time.UTC)
	h.Add(hour.Add(time.Minute), sysstat.Snapshot{
		LoadAverage1: 10, CPUUtilization: 0.4, MemUsedMB: 1000, CPUs: 28, MemTotalMB: 64000,
	})
	h.Add(hour.Add(30*time.Minute), sysstat.Snapshot{
		LoadAverage1: 47.4, CPUUtilization: 0.994, MemUsedMB: 2000, CPUs: 28, MemTotalMB: 64000, SwapUsedMB: 12,
	})
	h.Add(hour.Add(time.Hour+time.Minute), sysstat.Snapshot{
		LoadAverage1: 2, CPUUtilization: 0.1, MemUsedMB: 500, CPUs: 28, MemTotalMB: 64000,
	})

	rollups := h.Rollups()
	if len(rollups) != 2 {
		t.Fatalf("got %d rollups, want one per hour: %+v", len(rollups), rollups)
	}
	busy := rollups[0]
	if !busy.Since.Equal(hour) || busy.Samples != 2 {
		t.Fatalf("first rollup is %+v, want the two readings of %s", busy, hour)
	}
	// The mean and the worst, because they answer different questions: an
	// hour averaging 70% CPU with a peak of 99% is an hour in which jobs
	// were fighting, and the mean alone reads as an hour with room in it.
	if got, want := busy.Load1Mean, 28.7; got != want {
		t.Errorf("Load1Mean = %v, want %v", got, want)
	}
	if busy.Load1Max != 47.4 || busy.CPUUtilizationMax != 0.994 {
		t.Errorf("worst of the hour is load %v / cpu %v, want 47.4 and 0.994", busy.Load1Max, busy.CPUUtilizationMax)
	}
	if busy.MemUsedMeanMB != 1500 || busy.MemUsedMaxMB != 2000 || busy.SwapUsedMaxMB != 12 {
		t.Errorf("memory is %+v, want 1500 mean, 2000 worst, 12 of swap", busy)
	}
	// The machine itself rides on every rollup: a load average read
	// against the wrong core count is worse than no load average.
	if busy.CPUs != 28 || busy.MemTotalMB != 64000 {
		t.Errorf("the machine is %d cores / %d MiB, want 28 and 64000", busy.CPUs, busy.MemTotalMB)
	}
	if rollups[1].Samples != 1 || !rollups[1].Since.Equal(hour.Add(time.Hour)) {
		t.Errorf("second rollup is %+v, want the next hour's single reading", rollups[1])
	}
}

// The bound is the point: this is a ring, and a deployment left running
// for a month holds the hours it was told to and forgets the rest.
func TestHistoryKeepsOnlyTheHoursItWasToldTo(t *testing.T) {
	t.Parallel()
	h := sysstat.NewHistory(3)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		h.Add(start.Add(time.Duration(i)*time.Hour), sysstat.Snapshot{LoadAverage1: float64(i), CPUs: 4})
	}
	rollups := h.Rollups()
	if len(rollups) != 3 {
		t.Fatalf("got %d rollups, want the 3 it was told to keep", len(rollups))
	}
	if !rollups[0].Since.Equal(start.Add(7 * time.Hour)) {
		t.Errorf("oldest rollup is %s, want the third from the end", rollups[0].Since)
	}
	if h.Hours() != 3 {
		t.Errorf("Hours() = %d, want the bound the report states", h.Hours())
	}
}

// An hour that has already fallen out of the ring has nothing to fold a
// late reading into, and inventing one would put a rollup out of order in
// front of every reader of the series.
func TestHistoryDropsAReadingOlderThanItHolds(t *testing.T) {
	t.Parallel()
	h := sysstat.NewHistory(2)
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		h.Add(start.Add(time.Duration(i)*time.Hour), sysstat.Snapshot{LoadAverage1: 1, CPUs: 4})
	}
	h.Add(start, sysstat.Snapshot{LoadAverage1: 99, CPUs: 4})
	rollups := h.Rollups()
	if len(rollups) != 2 {
		t.Fatalf("got %d rollups, want 2: %+v", len(rollups), rollups)
	}
	for _, r := range rollups {
		if r.Load1Max == 99 {
			t.Errorf("the late reading was folded into %s, which is not the hour it was taken in", r.Since)
		}
	}
	if !rollups[0].Since.Before(rollups[1].Since) {
		t.Errorf("rollups are out of order: %s then %s", rollups[0].Since, rollups[1].Since)
	}
}

// A nil history is usable: the daemon allocates one at process start, but
// everything that takes one has to survive a caller that has none.
func TestANilHistoryIsSafeToUse(t *testing.T) {
	t.Parallel()
	var h *sysstat.History
	h.Add(time.Now(), sysstat.Snapshot{})
	if got := h.Rollups(); got != nil {
		t.Errorf("Rollups() = %+v, want nothing", got)
	}
	if h.Hours() != 0 {
		t.Error("a nil history claims to hold hours")
	}
}
