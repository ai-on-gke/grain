package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// The restart-loop count against restarts this deployment asked for
// itself (restart_requests.go). The incident is grain task 9's: two
// in-place upgrades at 18:07:44 and 18:08:32 on 2026-10-17, each
// followed by a start of the unit, reported as "started 2 times in the
// last 30 minutes -- it is restarting rather than running".

// fakeRequests stands in for restartRequests where a test only needs
// the times.
type fakeRequests []time.Time

func (f fakeRequests) times(since time.Time) []time.Time {
	var out []time.Time
	for _, t := range f {
		if !t.Before(since) {
			out = append(out, t)
		}
	}
	return out
}

func octAt(hms string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, "2026-10-17T"+hms+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

// startLine is systemd's line for one start of the unit, at a time, in
// --output=short-iso.
func startLine(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05-0700") + " host systemd[1]: Started grain-daemon.service - grain daemon."
}

// upgradeJournal is the journal of the incident: the daemon's own
// upgrade lines, systemd stopping the unit, and starting it again --
// twice.
func upgradeJournal() []string {
	return []string{
		"2026-10-17T18:07:43+0000 host grain[100]: upgrade: main: succeeded in 41s: installed abc; restarting",
		"2026-10-17T18:07:43+0000 host systemd[1]: Stopping grain-daemon.service - grain daemon...",
		startLine(octAt("18:07:44")),
		"2026-10-17T18:08:31+0000 host grain[200]: upgrade: main: succeeded in 38s: installed def; restarting",
		"2026-10-17T18:08:31+0000 host systemd[1]: Stopping grain-daemon.service - grain daemon...",
		startLine(octAt("18:08:32")),
	}
}

// requestedJournal is the same two starts with nothing in the journal
// saying anybody asked for them -- no "Stopping" line before either, as
// when the process exited and Restart= brought it back, or the window
// lost the stop -- so the restart requests are the only evidence there
// is of who asked.
func requestedJournal() []string {
	return []string{
		"2026-10-17T18:07:43+0000 host grain[100]: upgrade: main: succeeded in 41s: installed abc; restarting",
		startLine(octAt("18:07:44")),
		"2026-10-17T18:08:31+0000 host grain[200]: upgrade: main: succeeded in 38s: installed def; restarting",
		startLine(octAt("18:08:32")),
	}
}

func loggedLines(h *daemonHealth) []string {
	var out []string
	h.LogRestartLoop(func(format string, args ...any) {
		out = append(out, strings.TrimSpace(fmt.Sprintf(format, args...)))
	})
	return out
}

func TestTwoUpgradeRestartsInTheWindowAreNotARestartLoop(t *testing.T) {
	t.Parallel()
	// The requests the two upgrades wrote just before their restart
	// commands, a moment before each start.
	requests := fakeRequests{octAt("18:07:43.6"), octAt("18:08:31.9")}
	h := newDaemonHealth(octAt("18:08:32.5"), nil, &fakeJournal{lines: requestedJournal()}, requests)

	health := h.Health()
	if health.Starts != 2 {
		t.Fatalf("starts = %d, want 2 -- the count itself is still every start", health.Starts)
	}
	if health.RequestedStarts != 2 {
		t.Errorf("requestedStarts = %d, want 2", health.RequestedStarts)
	}
	if health.Restarting {
		t.Error("restarting = true, want false: both restarts were upgrades grain asked for")
	}
	for _, line := range loggedLines(h) {
		if strings.Contains(line, "WARNING") {
			t.Errorf("logged %q, want no warning for two upgrades", line)
		}
	}
}

func TestACrashRestartIsStillARestartLoop(t *testing.T) {
	t.Parallel()
	// The same two starts, and nobody asked for either.
	h := newDaemonHealth(octAt("18:08:32.5"), nil, &fakeJournal{lines: requestedJournal()}, fakeRequests{})
	health := h.Health()
	if !health.Restarting {
		t.Fatal("restarting = false, want true: two starts nobody asked for are a restart")
	}
	lines := loggedLines(h)
	if len(lines) != 1 || !strings.Contains(lines[0], "WARNING") || !strings.Contains(lines[0], "started 2 times") {
		t.Errorf("logged %q, want the restart-loop warning", lines)
	}
}

// The journal of an upgrade that restarts the unit with systemctl holds
// a "Stopping" line before each start, which already says somebody
// asked (countUnitStarts). With no restart request recorded either, the
// count, the requested starts and the loop verdict agree on that: one
// start counted, none of it a restart, and no warning.
func TestStartsAfterAStopSomebodyAskedForAreNotARestartLoop(t *testing.T) {
	t.Parallel()
	h := newDaemonHealth(octAt("18:08:32.5"), nil, &fakeJournal{lines: upgradeJournal()}, fakeRequests{})
	health := h.Health()
	if health.Starts != 1 || health.RequestedStarts != 0 || health.Restarting {
		t.Errorf("health = %+v, want 1 start, none requested, not restarting", health)
	}
	if lines := loggedLines(h); len(lines) != 0 {
		t.Errorf("logged %q, want nothing for a single counted start", lines)
	}
}

// An upgrade does not excuse a crash after it: the start nobody asked
// for is still a restart, and is still warned about -- with the
// upgrade's start labelled for what it was.
func TestACrashAfterAnUpgradeIsStillARestartLoop(t *testing.T) {
	t.Parallel()
	journal := &fakeJournal{lines: []string{
		startLine(octAt("18:00:00")), // the boot, before either
		startLine(octAt("18:07:44")), // the upgrade's restart
		startLine(octAt("18:08:32")), // a crash
	}}
	h := newDaemonHealth(octAt("18:08:33"), nil, journal, fakeRequests{octAt("18:07:43.6")})
	health := h.Health()
	if !health.Restarting {
		t.Fatal("restarting = false, want true: the crash after the upgrade was asked for by nobody")
	}
	if health.RequestedStarts != 1 {
		t.Errorf("requestedStarts = %d, want 1", health.RequestedStarts)
	}
	lines := loggedLines(h)
	if len(lines) != 1 || !strings.Contains(lines[0], "WARNING") || !strings.Contains(lines[0], "1 of them a restart grain asked for") {
		t.Errorf("logged %q, want the warning, with the upgrade's start labelled", lines)
	}
}

// A request is for the start right after it. One whose restart never
// happened -- a restart command that restarted nothing -- must not go on
// to excuse a crash much later, and one request explains one start.
func TestARequestExplainsOneStartAndOnlyASoonOne(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		starts         []time.Time
		requests       []time.Time
		wantRequested  int
		wantRestarting bool
	}{
		{"stale request", []time.Time{octAt("18:00:00"), octAt("18:25:00")}, []time.Time{octAt("18:01:00")}, 0, true},
		{"one request, two restarts", []time.Time{octAt("18:00:00"), octAt("18:07:44"), octAt("18:07:50")}, []time.Time{octAt("18:07:43")}, 1, true},
		{"same restart recorded twice", []time.Time{octAt("18:00:00"), octAt("18:07:44"), octAt("18:07:50")},
			[]time.Time{octAt("18:07:43.1"), octAt("18:07:43.1001")}, 1, true},
		{"request in the same second as its start", []time.Time{octAt("18:00:00"), octAt("18:07:44")}, []time.Time{octAt("18:07:44.4")}, 1, false},
		{"boot then upgrade", []time.Time{octAt("18:00:00"), octAt("18:07:44")}, []time.Time{octAt("18:07:43")}, 1, false},
		{"single start", []time.Time{octAt("18:00:00")}, nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requested, restarting := classifyStarts(tc.starts, tc.requests)
			if requested != tc.wantRequested || restarting != tc.wantRestarting {
				t.Errorf("classifyStarts = (%d, %v), want (%d, %v)", requested, restarting, tc.wantRequested, tc.wantRestarting)
			}
		})
	}
}

// A journal whose timestamps cannot be read cannot be matched against
// anything, and is read the way it was before requests existed: every
// start after the first is a restart.
func TestUnreadableStartTimesFallBackToCountingEveryStart(t *testing.T) {
	t.Parallel()
	journal := &fakeJournal{lines: []string{
		"Oct 17 18:07:44 host systemd[1]: Started grain-daemon.service - grain daemon.",
		"Oct 17 18:08:32 host systemd[1]: Started grain-daemon.service - grain daemon.",
	}}
	health := newDaemonHealth(octAt("18:08:33"), nil, journal, fakeRequests{octAt("18:07:43"), octAt("18:08:31")}).Health()
	if health.Starts != 2 || !health.Restarting || health.RequestedStarts != 0 {
		t.Errorf("health = %+v, want 2 starts, none attributed, and a restart", health)
	}
}

// The real record, end to end: the requests file the upgrade hook and
// the restart button write, read back by the next start's count.
func TestRecordedRestartRequestsAreReadBackByTheNextStart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clock := octAt("18:07:43.6")
	reqs := restartRequests{dataDir: dir, now: func() time.Time { return clock }}
	if err := reqs.record("upgrade to main"); err != nil {
		t.Fatal(err)
	}
	clock = octAt("18:08:31.9")
	if err := reqs.record("the restart button"); err != nil {
		t.Fatal(err)
	}

	h := newDaemonHealth(octAt("18:08:32.5"), nil, &fakeJournal{lines: requestedJournal()}, reqs)
	if health := h.Health(); health.Restarting || health.RequestedStarts != 2 {
		t.Errorf("health = %+v, want both starts attributed to their requests and no loop", health)
	}
}

// The upgrade that lands a build was run by the build before it, which
// may never have written a request. Its status file says the same thing.
func TestTheUpgradeStatusFileCountsAsARequest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	restartAt := octAt("18:08:31.9")
	finished := restartAt.Add(-100 * time.Millisecond)
	status := upgrade.Status{
		Branch: "main", Phase: upgrade.PhaseOK, Detail: "installed def; restarting", FinishedAt: &finished,
		Steps: []upgrade.Step{{Name: "restart", State: upgrade.StepRunning, StartedAt: &restartAt}},
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "upgrade-status.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	reqs := restartRequests{dataDir: dir, now: time.Now}
	h := newDaemonHealth(octAt("18:08:32.5"), nil, &fakeJournal{lines: requestedJournal()}, reqs)
	health := h.Health()
	// The second start is the status file's; the first is the window's
	// first start, which is never held against it on its own.
	if health.Restarting || health.RequestedStarts != 1 {
		t.Errorf("health = %+v, want the second start attributed to the upgrade and no loop", health)
	}
}

func TestRestartRequestsArePrunedAsTheyAreWritten(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clock := octAt("00:00:00")
	reqs := restartRequests{dataDir: dir, now: func() time.Time { return clock }}
	if err := reqs.record("old"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(restartRequestRetention + time.Hour)
	if err := reqs.record("new"); err != nil {
		t.Fatal(err)
	}
	got, err := reqs.read(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].reason != "new" {
		t.Errorf("requests = %+v, want only the one inside the retention", got)
	}
}

// No data dir records nothing and reads nothing, rather than writing
// into the working directory.
func TestRestartRequestsWithoutADataDirAreInert(t *testing.T) {
	t.Parallel()
	var reqs restartRequests
	if err := reqs.record("anything"); err != nil {
		t.Fatal(err)
	}
	if got := reqs.times(time.Time{}); len(got) != 0 {
		t.Errorf("times = %v, want none", got)
	}
}
