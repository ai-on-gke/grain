// daemon_restarts_test.go covers the figure that was missing on
// 2026-09-18: "this service has started N times in the last M minutes",
// taken from the unit's own journal at every start (daemon_restarts.go).
package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/systemlog"
)

// fakeJournal stands in for pkg/systemlog.Journalctl -- the lines a
// journalctl --unit read would have returned, without a systemd on the
// machine running the test.
type fakeJournal struct {
	lines []string
	err   error
	calls int
	query systemlog.Query
}

func (f *fakeJournal) Read(_ context.Context, q systemlog.Query) ([]string, error) {
	f.calls++
	f.query = q
	return f.lines, f.err
}

// The journal of the outage, in the shape journalctl --output=short-iso
// prints: systemd's own "Started" line per restart, the daemon's refusal
// between them, and the restart counter systemd keeps.
func loopJournal(starts int) []string {
	var lines []string
	for i := 0; i < starts; i++ {
		if i > 0 {
			lines = append(lines,
				"2026-09-18T05:35:0"+string(rune('0'+i%10))+"+0000 host systemd[1]: grain-daemon.service: Scheduled restart job, restart counter is at "+
					string(rune('0'+i%10))+".")
		}
		lines = append(lines,
			"2026-09-18T05:35:0"+string(rune('0'+i%10))+"+0000 host systemd[1]: Started grain-daemon.service - grain daemon (task orchestrator, UI and API), from ghcr.io/bwsalmon/grain/grain.",
			"2026-09-18T05:35:0"+string(rune('0'+i%10))+"+0000 host grain[3614028]: flag provided but not defined: -root-shell-control-dir",
		)
	}
	return lines
}

func TestARestartLoopIsCountedFromTheUnitsOwnJournal(t *testing.T) {
	t.Parallel()
	journal := &fakeJournal{lines: loopJournal(52)}
	started := time.Date(2026, 9, 18, 5, 43, 0, 0, time.UTC)
	health := newDaemonHealth(started, nil, journal, nil).Health()
	if health.Starts != 52 {
		t.Fatalf("starts = %d, want 52", health.Starts)
	}
	if health.WindowMinutes != int(restartWindow/time.Minute) {
		t.Errorf("windowMinutes = %d, want %d -- the count means nothing without the window it was taken over",
			health.WindowMinutes, int(restartWindow/time.Minute))
	}
	if !health.StartedAt.Equal(started) {
		t.Errorf("startedAt = %v, want %v", health.StartedAt, started)
	}
	if health.Error != "" {
		t.Errorf("error = %q, want none", health.Error)
	}
	// The read is bounded at both ends, because in a loop this window
	// holds thousands of lines and this runs at every start.
	if want := started.Add(-restartWindow); !journal.query.Since.Equal(want) {
		t.Errorf("read since %v, want %v", journal.query.Since, want)
	}
	if journal.query.Lines != restartWindowLineCap {
		t.Errorf("read with no line cap (%d), want %d", journal.query.Lines, restartWindowLineCap)
	}
}

// The pane polls; the journal is read once. A crash-looping deployment
// is the last one that should be paying for a subprocess every three
// seconds.
func TestTheStartCountIsTakenOnceAndServedFromMemory(t *testing.T) {
	t.Parallel()
	journal := &fakeJournal{lines: loopJournal(3)}
	h := newDaemonHealth(time.Now(), nil, journal, nil)
	for i := 0; i < 5; i++ {
		if got := h.Health().Starts; got != 3 {
			t.Fatalf("starts on call %d = %d, want 3", i, got)
		}
	}
	if journal.calls != 1 {
		t.Errorf("the journal was read %d times, want once", journal.calls)
	}
}

// One start and no restarts is the ordinary answer, and it is reported
// as the number 1 rather than as nothing: "started once" and "nobody
// could tell" have to be distinguishable.
func TestAHealthyDaemonReportsASingleStart(t *testing.T) {
	t.Parallel()
	h := newDaemonHealth(time.Now(), nil, &fakeJournal{lines: loopJournal(1)}, nil)
	health := h.Health()
	if health.Starts != 1 {
		t.Fatalf("starts = %d, want 1", health.Starts)
	}
	var logged []string
	h.LogRestartLoop(func(format string, args ...any) { logged = append(logged, format) })
	if len(logged) != 0 {
		t.Errorf("a single start logged %v, want nothing said at all", logged)
	}
}

func TestARestartLoopIsSaidOutLoudInTheJournalToo(t *testing.T) {
	t.Parallel()
	h := newDaemonHealth(time.Now(), nil, &fakeJournal{lines: loopJournal(52)}, nil)
	var logged []string
	h.LogRestartLoop(func(format string, args ...any) {
		logged = append(logged, strings.TrimSpace(fmt.Sprintf(format, args...)))
	})
	if len(logged) != 1 {
		t.Fatalf("logged %d lines, want exactly 1: a loop must not multiply its own report", len(logged))
	}
	if !strings.Contains(logged[0], "started 52 times") {
		t.Errorf("logged %q, want the count in it", logged[0])
	}
}

// A journal that cannot be read is reported as unreadable rather than as
// zero restarts: those are the same number and opposite facts.
func TestAnUncountableJournalIsSaidSoRatherThanCountedAsZero(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		journal journalReader
		want    string
	}{
		{"journalctl failed", &fakeJournal{err: errors.New("journalctl -u grain-daemon.service: exit status 1")}, "exit status 1"},
		{"no journal at all", nil, "no journal"},
		{"a journal without this unit in it", &fakeJournal{lines: []string{
			"2026-09-18T05:35:01+0000 host grain[1]: something else entirely",
		}}, "holds no start of it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			health := newDaemonHealth(time.Now(), nil, tc.journal, nil).Health()
			if health.Starts != 0 {
				t.Fatalf("starts = %d, want none", health.Starts)
			}
			if !strings.Contains(health.Error, tc.want) {
				t.Errorf("error = %q, want it to mention %q", health.Error, tc.want)
			}
		})
	}
}

// A start is systemd's own line about the unit. The daemon's own output
// is in this same journal and quotes all sorts of things.
func TestOnlySystemdsOwnStartLinesAreCountedAsStarts(t *testing.T) {
	t.Parallel()
	lines := []string{
		"2026-09-18T05:35:01+0000 host systemd[1]: Started grain-daemon.service - grain daemon.",
		"2026-09-18T05:35:02+0000 host grain[9]: Started the reconcile loop",
		"2026-09-18T05:35:02+0000 host grain[9]: Started run 219-1 for task 219",
		"2026-09-18T05:35:03+0000 host systemd[1]: grain-daemon.service: Scheduled restart job, restart counter is at 1.",
		"2026-09-18T05:35:08+0000 host systemd[1]: Started grain-daemon.service - grain daemon.",
	}
	if got := countUnitStarts(lines); got != 2 {
		t.Errorf("countUnitStarts = %d, want 2 -- the daemon's own log lines are not starts of the service", got)
	}
}

// A restart somebody asked for -- systemctl restart, an installer re-run
// -- is not a crash loop: systemd says "Stopping" first, which an exit
// Restart= recovers from never does.
func TestDeliberateRestartsAreNotCountedAsStarts(t *testing.T) {
	t.Parallel()
	lines := []string{
		"2026-09-18T05:30:01+0000 host systemd[1]: Started grain-daemon.service - grain daemon.",
		"2026-09-18T05:31:00+0000 host systemd[1]: Stopping grain-daemon.service - grain daemon...",
		"2026-09-18T05:31:01+0000 host systemd[1]: grain-daemon.service: Deactivated successfully.",
		"2026-09-18T05:31:01+0000 host systemd[1]: Stopped grain-daemon.service - grain daemon.",
		"2026-09-18T05:31:02+0000 host systemd[1]: Started grain-daemon.service - grain daemon.",
		"2026-09-18T05:32:00+0000 host systemd[1]: Stopping grain-daemon.service - grain daemon...",
		"2026-09-18T05:32:02+0000 host systemd[1]: Started grain-daemon.service - grain daemon.",
	}
	if got := countUnitStarts(lines); got != 1 {
		t.Errorf("countUnitStarts = %d, want 1 -- two restarts on purpose are not a loop", got)
	}
	crash := append(lines,
		"2026-09-18T05:33:00+0000 host systemd[1]: grain-daemon.service: Main process exited, code=exited, status=1/FAILURE",
		"2026-09-18T05:33:00+0000 host systemd[1]: grain-daemon.service: Failed with result 'exit-code'.",
		"2026-09-18T05:33:05+0000 host systemd[1]: grain-daemon.service: Scheduled restart job, restart counter is at 1.",
		"2026-09-18T05:33:05+0000 host systemd[1]: Stopped grain-daemon.service - grain daemon.",
		"2026-09-18T05:33:05+0000 host systemd[1]: Started grain-daemon.service - grain daemon.",
	)
	if got := countUnitStarts(crash); got != 2 {
		t.Errorf("countUnitStarts = %d, want 2 -- a restart after a crash still counts", got)
	}
	// A journal whose window opens on a deliberate stop still holds this
	// start.
	if got := countUnitStarts(lines[1:5]); got != 1 {
		t.Errorf("countUnitStarts = %d, want 1 -- the start this process is still counts", got)
	}
}

// The flags this build is ignoring travel with the same reading, so the
// pane can say "and it is running without these settings" beside "and it
// has restarted this many times" -- the two halves of grain/task-233's
// own failure.
func TestTheIgnoredFlagsAreReportedBesideTheRestartCount(t *testing.T) {
	t.Parallel()
	health := newDaemonHealth(time.Now(), []string{"-root-shell-control-dir"}, &fakeJournal{lines: loopJournal(1)}, nil).Health()
	if len(health.UnknownFlags) != 1 || health.UnknownFlags[0] != "-root-shell-control-dir" {
		t.Errorf("unknownFlags = %v, want the flag this build ignored", health.UnknownFlags)
	}
	// And with the advice for *this* shape: a host has a unit, and the
	// script that wrote it is what rewrites it.
	if !strings.Contains(health.FlagSource, "grain-daemon.service") {
		t.Errorf("flagSource = %q, want the unit that passed the flags", health.FlagSource)
	}
	if !strings.Contains(health.FlagFix, "scripts/setup.sh") {
		t.Errorf("flagFix = %q, want the script that rewrites the unit", health.FlagFix)
	}
	if !strings.Contains(health.LogCommand, "journalctl -u grain-daemon.service") {
		t.Errorf("logCommand = %q, want the journal this count came out of", health.LogCommand)
	}
	if health.StartsScope != "in the last 30 minutes" {
		t.Errorf("startsScope = %q, want the window the count was taken over", health.StartsScope)
	}
}

// fakePods stands in for pkg/k8s.Client -- the kubelet's restartCount
// for this pod's own container, without a cluster behind the test.
type fakePods struct {
	restarts       int
	err            error
	calls          int
	pod, container string
}

func (f *fakePods) ContainerRestarts(_ context.Context, pod, container string) (int, error) {
	f.calls++
	f.pod, f.container = pod, container
	return f.restarts, f.err
}

// The Kubernetes half of the same question (grain/task-367). There is no
// journal in that image and no unit behind it: a crash loop there is a
// CrashLoopBackOff of the one replica, and the kubelet has the number.
func TestAPodsRestartsAreCountedFromTheKubelet(t *testing.T) {
	t.Parallel()
	pods := &fakePods{restarts: 51}
	h := newPodDaemonHealth(time.Now(), nil, pods, "grain-7d9f-abcde", "grain", kubeShape("grain", "grain", "grain"))
	health := h.Health()

	// restartCount counts restarts and Starts counts starts, so that the
	// figure means the same thing on both shapes -- 52 starts is 51
	// restarts, and a container that has never restarted has started
	// once.
	if health.Starts != 52 {
		t.Fatalf("starts = %d, want 52 (51 restarts plus the start running now)", health.Starts)
	}
	if pods.pod != "grain-7d9f-abcde" || pods.container != "grain" {
		t.Errorf("read pod %q container %q, want this pod's own container", pods.pod, pods.container)
	}
	// The count has no window, and saying it had one would make the pane
	// claim those restarts happened in the last half hour.
	if health.WindowMinutes != 0 {
		t.Errorf("windowMinutes = %d, want none: a kubelet's restartCount counts from the pod's creation", health.WindowMinutes)
	}
	if health.StartsScope != "since this pod was created" {
		t.Errorf("startsScope = %q, want what this count is a count of", health.StartsScope)
	}
	// And with this shape's advice: args on a Deployment, an apply, and
	// the previous container's log rather than a journal.
	if !strings.Contains(health.FlagSource, "deployment/grain") {
		t.Errorf("flagSource = %q, want the Deployment whose args they are", health.FlagSource)
	}
	if !strings.Contains(health.FlagFix, "re-apply") || strings.Contains(health.FlagFix, "setup.sh") {
		t.Errorf("flagFix = %q, want the edit-and-apply a cluster takes", health.FlagFix)
	}
	if !strings.Contains(health.LogCommand, "kubectl -n grain logs deploy/grain") {
		t.Errorf("logCommand = %q, want the pod log this shape has instead of a journal", health.LogCommand)
	}

	// Read once and served from memory, like the journal reading: the
	// pane polls every few seconds and this is a subprocess.
	for i := 0; i < 4; i++ {
		h.Health()
	}
	if pods.calls != 1 {
		t.Errorf("the kubelet was asked %d times, want once", pods.calls)
	}
}

// A pod that has never restarted is the ordinary answer, and it is the
// same number a healthy host reports: one start, no alarm.
func TestAHealthyPodReportsASingleStart(t *testing.T) {
	t.Parallel()
	h := newPodDaemonHealth(time.Now(), nil, &fakePods{restarts: 0}, "grain-1", "grain", kubeShape("grain", "grain", "grain"))
	if got := h.Health().Starts; got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	var logged []string
	h.LogRestartLoop(func(format string, args ...any) { logged = append(logged, format) })
	if len(logged) != 0 {
		t.Errorf("a pod that has never restarted logged %v, want nothing said at all", logged)
	}
}

// The loop is said out loud here too, and it points at the log this
// shape keeps rather than at a journalctl the image does not carry.
func TestAPodsRestartLoopIsSaidOutLoudInItsOwnLogToo(t *testing.T) {
	t.Parallel()
	h := newPodDaemonHealth(time.Now(), nil, &fakePods{restarts: 51}, "grain-1", "grain", kubeShape("grain", "grain", "grain"))
	var logged []string
	h.LogRestartLoop(func(format string, args ...any) {
		logged = append(logged, strings.TrimSpace(fmt.Sprintf(format, args...)))
	})
	if len(logged) != 1 {
		t.Fatalf("logged %d lines, want exactly 1", len(logged))
	}
	if !strings.Contains(logged[0], "started 52 times since this pod was created") {
		t.Errorf("logged %q, want the count and what it is a count of", logged[0])
	}
	if !strings.Contains(logged[0], "kubectl -n grain logs deploy/grain") {
		t.Errorf("logged %q, want the log an operator would read it in", logged[0])
	}
}

// The same refusal to report a count nobody took, on the shape whose two
// ways of not having one are a pod that was never told its own name and
// an API server that would not answer.
func TestAnUncountablePodIsSaidSoRatherThanCountedAsZero(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		pods podRestartReader
		pod  string
		want string
	}{
		{"no pod name in the environment", &fakePods{}, "", PodNameEnv},
		{"no client at all", nil, "grain-1", PodNameEnv},
		{"the API server refused", &fakePods{err: errors.New(`pods "grain-1" is forbidden`)}, "grain-1", "forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			health := newPodDaemonHealth(time.Now(), nil, tc.pods, tc.pod, "grain", kubeShape("grain", "grain", "grain")).Health()
			if health.Starts != 0 {
				t.Fatalf("starts = %d, want none", health.Starts)
			}
			if !strings.Contains(health.Error, tc.want) {
				t.Errorf("error = %q, want it to mention %q", health.Error, tc.want)
			}
			// Even with no count, the advice for this shape is still
			// worth reporting: the flags it is ignoring are a fact this
			// reading can still carry.
			if health.FlagFix == "" {
				t.Error("a reading with no count also dropped the advice that does not depend on one")
			}
		})
	}
}
