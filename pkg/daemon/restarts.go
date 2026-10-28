package daemon

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/systemlog"
	"github.com/bwsalmon/grain/pkg/ui"
)

// restartWindow is how far back the count looks. Half an hour is long
// enough to hold a loop that has already given up (systemd's default
// rate limiting stops a unit after 5 starts in 10 seconds; this
// deployment's unit has RestartSec=5 and no StartLimit, which is how it
// reached 52) and short enough that a count above 1 means something is
// wrong now rather than something that was wrong this morning.
const restartWindow = 30 * time.Minute

// restartWindowLineCap bounds the journal read that count comes out of.
// A crash-looping daemon writes a great deal into that window -- the
// usage dump this deployment saw was ~10,000 lines on its own -- and
// this runs at every start, including the ones inside the loop. The cap
// is on the *newest* entries (journalctl --lines), so a truncated read
// undercounts an extreme loop rather than missing a recent one, and 5000
// lines is far more than the two systemd writes per start.
const restartWindowLineCap = 5000

// unitStart matches systemd's own "Started <unit>" line, which it writes
// once per start of the unit. Anchored on the systemd[pid] prefix so
// that the daemon's own log lines -- which are in this same journal, and
// which quote whatever they are starting -- cannot be counted as starts
// of the service.
//
// The unit name is not matched: the read is already unit-scoped
// (journalctl --unit), and systemd's phrasing of the rest of the line
// has changed across versions ("Started grain-daemon.service - grain
// daemon..." on v250+, "Started grain daemon..." before it).
var unitStart = regexp.MustCompile(`systemd\[\d+\]: Started `)

// unitStopping matches systemd's "Stopping <unit>" line, which it writes
// when somebody asked for the unit to stop -- systemctl stop or restart,
// an installer re-run, an upgrade -- and not when the process exited on
// its own and Restart= brought it back (that path logs "Main process
// exited", "Scheduled restart job" and "Stopped", never "Stopping").
var unitStopping = regexp.MustCompile(`systemd\[\d+\]: Stopping `)

// countUnitStarts is how many starts of the unit those journal lines
// hold, less the ones that followed a stop somebody asked for: a restart
// on purpose is not the loop this count exists to show, and counting
// them warned "restarting rather than running" after a few deliberate
// restarts and installer re-runs (grain/task-4498). The first start the
// lines hold always counts, whatever came before it, so a journal that
// holds this start still never reads as 0.
func countUnitStarts(lines []string) int {
	n := 0
	asked := false
	for _, line := range lines {
		switch {
		case unitStopping.MatchString(line):
			asked = true
		case unitStart.MatchString(line):
			if n == 0 || !asked {
				n++
			}
			asked = false
		}
	}
	return n
}

// unitStartTimes is when each of those starts happened -- the same
// starts countUnitStarts counts, so a start after a "Stopping" line is
// left out here too and the count, the requested starts and whether it
// is restarting all describe one set of starts -- from the timestamp
// journalctl --output=short-iso puts at the head of every line. ok is
// false if any start's time could not be read, in which case the starts
// cannot be matched against the restarts this deployment asked for and
// are all treated as unasked -- the reading this file gave before
// restart requests existed.
func unitStartTimes(lines []string) (times []time.Time, ok bool) {
	ok = true
	asked := false
	for _, line := range lines {
		switch {
		case unitStopping.MatchString(line):
			asked = true
		case unitStart.MatchString(line):
			if len(times) == 0 || !asked {
				at, parsed := journalTime(line)
				if !parsed {
					ok = false
				}
				times = append(times, at)
			}
			asked = false
		}
	}
	return times, ok
}

// journalTime reads the short-iso timestamp a journal line starts with.
// systemd has printed its offset both as +0000 and as +00:00.
func journalTime(line string) (time.Time, bool) {
	stamp, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339Nano} {
		if at, err := time.Parse(layout, stamp); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

// restartRequestDedup merges requests closer together than this into
// one: the same restart is recorded both in the requests file and in the
// upgrade's status file, microseconds apart, and must explain one start
// rather than two.
const restartRequestDedup = 5 * time.Second

// classifyStarts sorts a window's starts into the ones this deployment
// asked for and the ones nobody did, and says whether the window holds a
// restart nobody asked for.
//
// A start was asked for if a restart request (restart_requests.go) falls
// after the start before it and no more than restartRequestReach ahead of
// it. Each request explains one start at most.
//
// The first start in the window is never a restart on its own account,
// asked for or not: it may be the boot of a daemon that then ran for
// hours, which is the one ambiguity this count has always had to live
// with (a single start reads as healthy). Every later start was a
// restart, and restarting is whether any of those was one nobody asked
// for -- a crash, a kill, a unit systemd brought back. Two upgrades in
// the window are therefore not a loop; an upgrade followed by a crash
// still is.
func classifyStarts(starts, requests []time.Time) (requested int, restarting bool) {
	starts = append([]time.Time(nil), starts...)
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	reqs := append([]time.Time(nil), requests...)
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].Before(reqs[j]) })
	var merged []time.Time
	for _, r := range reqs {
		if n := len(merged); n > 0 && r.Sub(merged[n-1]) < restartRequestDedup {
			continue
		}
		merged = append(merged, r)
	}
	used := make([]bool, len(merged))
	for i, start := range starts {
		lower := start.Add(-restartRequestReach)
		if i > 0 && starts[i-1].After(lower) {
			lower = starts[i-1]
		}
		upper := start.Add(restartRequestSlack)
		asked := false
		for j, r := range merged {
			if used[j] || !r.After(lower) || r.After(upper) {
				continue
			}
			used[j], asked = true, true
			break
		}
		if asked {
			requested++
		} else if i > 0 {
			restarting = true
		}
	}
	return requested, restarting
}

// startCount is what one reading of a deployment's starts comes to.
type startCount struct {
	// starts is how many times it started, including this one.
	starts int
	// requested is how many of those starts followed a restart this
	// deployment asked for itself.
	requested int
	// restarting is whether any start after the first was one nobody
	// asked for: the verdict the warning and the pane's alert are about.
	restarting bool
}

// restartRequestTimes is the slice of restartRequests daemonHealth
// needs, named so a test can hand it times without a data dir.
type restartRequestTimes interface {
	times(since time.Time) []time.Time
}

// journalReader is the slice of pkg/systemlog daemonHealth needs, named
// here so a test can supply journal lines without a systemd on the
// machine running it.
type journalReader interface {
	Read(ctx context.Context, q systemlog.Query) ([]string, error)
}

// podRestartReader is the slice of pkg/k8s.Client daemonHealth needs,
// named here for the same reason: a test supplies a restart count
// without a cluster behind it.
type podRestartReader interface {
	ContainerRestarts(ctx context.Context, pod, container string) (int, error)
}

// daemonShape is what a deployment's restart count is a count *of*, and
// what an operator does here about the two things this reading can
// report -- said in the words the UI puts on the screen, because the
// daemon is the only end that knows which shape it is (grain/task-367).
//
// There are two, and they are the two pkg/upgrade already distinguishes
// between imageFlagFix and KubeConfig.flagFix: a host running
// grain-daemon.service, where the command line is a unit file and the
// log is the journal, and a pod running one replica of a Deployment,
// where the command line is that Deployment's args and the log is
// `kubectl logs`.
type daemonShape struct {
	// startsScope is what Starts covers, as a phrase that follows a
	// count: "in the last 30 minutes", "since this pod was created".
	startsScope string
	// flagSource is what passed the flags this build is ignoring, as the
	// subject of a sentence about them.
	flagSource string
	// flagFix is the sentence telling an operator what to do about them.
	flagFix string
	// logCommand shows what a restarting daemon has been failing on --
	// which under Kubernetes is the *previous* container's log, since
	// the container answering this request is the one that came up.
	logCommand string
}

// systemdShape is a deployment scripts/setup.sh installed: a unit, a
// journal, and a script that rewrites the unit.
func systemdShape() daemonShape {
	return daemonShape{
		startsScope: fmt.Sprintf("in the last %d minutes", int(restartWindow/time.Minute)),
		flagSource:  "this host's grain-daemon.service",
		flagFix:     "Re-running scripts/setup.sh on the host rewrites the unit.",
		logCommand:  "journalctl -u grain-daemon.service",
	}
}

// kubeShape is a deployment deploy/kubernetes/ installed. It names the
// objects rather than the file, for the reason KubeConfig.flagFix gives:
// the args are on a Deployment somebody applied, and a cluster edited in
// place has drifted from the manifest in this repository.
func kubeShape(namespace, deployment, container string) daemonShape {
	return daemonShape{
		startsScope: "since this pod was created",
		flagSource:  fmt.Sprintf("container %s's command line in deployment/%s", container, deployment),
		flagFix: fmt.Sprintf("Edit container %s's args in deployment/%s (deploy/kubernetes/grain.yaml) and re-apply it.",
			container, deployment),
		logCommand: fmt.Sprintf("kubectl -n %s logs deploy/%s --previous", namespace, deployment),
	}
}

// daemonHealth is ui.Config.Daemon: the process's own start time, the
// start count taken once beside it, the flags this build was handed and
// does not define, and the shape this deployment is -- which is what
// decides what that count means and what an operator does about it.
type daemonHealth struct {
	startedAt    time.Time
	unknownFlags []string
	shape        daemonShape
	// window is how far back the count looked, where it looked back a
	// fixed distance at all. Zero on the Kubernetes reading, which
	// counts from the pod's creation and has no window to report.
	window time.Duration

	once     sync.Once
	count    func() (startCount, error)
	counted  startCount
	countErr error
}

// newDaemonHealth wires one against a unit's journal. journal may be nil
// -- a deployment with no journal to read, which reports the uptime and
// the flags and says why there is no count rather than claiming one.
//
// requests is where the restarts this deployment asked for itself are
// recorded (restart_requests.go); nil is a deployment that has none, and
// every start after the first then counts towards a loop.
func newDaemonHealth(startedAt time.Time, unknownFlags []string, journal journalReader, requests restartRequestTimes) *daemonHealth {
	h := &daemonHealth{startedAt: startedAt, unknownFlags: unknownFlags, shape: systemdShape(), window: restartWindow}
	h.count = func() (startCount, error) {
		if journal == nil {
			return startCount{}, fmt.Errorf("this deployment has no journal to count starts in")
		}
		// Bounded at both ends: the window, and the line cap above.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		lines, err := journal.Read(ctx, systemlog.Query{
			Since: startedAt.Add(-restartWindow),
			Lines: restartWindowLineCap,
		})
		if err != nil {
			return startCount{}, err
		}
		n := countUnitStarts(lines)
		// This process is in that window and is running, so a journal
		// that is readable at all holds at least this start. A 0 would
		// be a reading nobody should believe -- a container whose
		// journal mount holds another machine's entries, say -- and is
		// reported as no reading rather than as "it has never started".
		if n == 0 {
			return startCount{}, fmt.Errorf("the journal for this unit holds no start of it, including this one")
		}
		count := startCount{starts: n, restarting: n > 1}
		times, ok := unitStartTimes(lines)
		if ok && requests != nil {
			// Read back a little further than the window: a request
			// can precede the start it explains by up to
			// restartRequestReach.
			reqs := requests.times(startedAt.Add(-restartWindow - restartRequestReach))
			count.requested, count.restarting = classifyStarts(times, reqs)
		}
		return count, nil
	}
	return h
}

// newPodDaemonHealth wires one against the kubelet's own record instead,
// for the deployment shape that has no unit and no journal: grain runs
// as one replica of a Deployment (deploy/kubernetes/grain.yaml, one
// replica with strategy Recreate), so the loop this whole file exists to
// make visible is a CrashLoopBackOff of a single pod, and
// `.status.containerStatuses[].restartCount` is the kubelet's count of
// it.
//
// pod may be empty -- a deployment whose manifest does not tell it its
// own name -- which reports why there is no count rather than claiming
// one, exactly as a nil journal does above.
//
// Two things about that count are not true of the journal one, and both
// are why ui.DaemonHealth carries StartsScope:
//
//   - It has no window. It counts from the pod's creation, so a pod that
//     crash-looped this morning and has been up since still reports
//     those restarts.
//   - It goes back to zero when the pod is replaced, which every rollout
//     does. A crash-looping *rollout* is still visible -- the new pod's
//     own count climbs as the kubelet restarts it -- but it is visible
//     as the new pod's restarts and not as the deployment's history.
//
// The count is converted to starts (restarts + 1) so that Starts means
// the same thing on both shapes, including its floor: a container that
// has never restarted has started once, which is what a healthy journal
// reading also says.
func newPodDaemonHealth(startedAt time.Time, unknownFlags []string, pods podRestartReader, pod, container string, shape daemonShape) *daemonHealth {
	h := &daemonHealth{startedAt: startedAt, unknownFlags: unknownFlags, shape: shape}
	h.count = func() (startCount, error) {
		if pods == nil || pod == "" {
			return startCount{}, fmt.Errorf("this pod does not know its own name, so the kubelet's restart count for it " +
				"cannot be read: deploy/kubernetes/grain.yaml fills $" + PodNameEnv + " from the downward API (metadata.name)")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		restarts, err := pods.ContainerRestarts(ctx, pod, container)
		if err != nil {
			return startCount{}, err
		}
		// No restart requests on this shape: an upgrade here is a
		// rollout, which replaces the pod and starts this count again
		// from one, so a restart the deployment asked for never shows
		// up in it.
		return startCount{starts: restarts + 1, restarting: restarts > 0}, nil
	}
	return h
}

// Health is what the UI calls. The count is taken on the first call and
// kept; the uptime is live, since that is the half that moves.
func (h *daemonHealth) Health() ui.DaemonHealth {
	h.once.Do(func() { h.counted, h.countErr = h.count() })
	out := ui.DaemonHealth{
		StartedAt:       h.startedAt,
		Starts:          h.counted.starts,
		RequestedStarts: h.counted.requested,
		Restarting:      h.counted.restarting,
		UnknownFlags:    h.unknownFlags,
		FlagSource:      h.shape.flagSource,
		FlagFix:         h.shape.flagFix,
		LogCommand:      h.shape.logCommand,
	}
	if h.counted.starts > 0 {
		out.StartsScope = h.shape.startsScope
		out.WindowMinutes = int(h.window / time.Minute)
	}
	if h.countErr != nil {
		out.Error = h.countErr.Error()
	}
	return out
}

// logRestartLoop says it in the journal too, at the one moment the
// journal is guaranteed to be where somebody is looking: a daemon that
// has restarted in the window without anybody asking it to says so on
// the start that discovered it.
//
// Quiet for the ordinary single start -- a line per boot saying "started
// once" is noise -- and quiet about a count it could not take, which is
// the UI's to report and not worth a warning on every start of every
// deployment without a journal.
//
// Restarts this deployment asked for itself -- an upgrade's, the restart
// button's -- are not a loop and get no warning (grain task 9: two
// upgrades a minute apart were reported as one). They are still said, on
// one plain line, so a reader of the journal sees why the count is
// above one.
func (h *daemonHealth) LogRestartLoop(logf func(string, ...any)) {
	health := h.Health()
	if health.Starts < 2 {
		return
	}
	if !health.Restarting {
		logf("grain daemon: this service has started %d times %s; %s",
			health.Starts, health.StartsScope, requestedPhrase(health.RequestedStarts, true))
		return
	}
	asked := ""
	if health.RequestedStarts > 0 {
		asked = " (" + requestedPhrase(health.RequestedStarts, false) + ")"
	}
	logf("grain daemon: WARNING: this service has started %d times %s%s -- "+
		"it is restarting rather than running. Whatever failed is in `%s`, and nothing is dispatched "+
		"between the restarts",
		health.Starts, health.StartsScope, asked, health.LogCommand)
}

// requestedPhrase says how many starts were restarts grain asked for.
// every is the case where that accounts for all the restarts there were.
func requestedPhrase(n int, every bool) string {
	if every {
		return "every restart among them was one grain asked for (an upgrade, or the restart or reboot button)"
	}
	if n == 1 {
		return "1 of them a restart grain asked for"
	}
	return fmt.Sprintf("%d of them restarts grain asked for", n)
}
