// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package systemlog implements pkg/ui.LogSource against the places
// grain v2 actually writes its own logs to: a systemd unit's journal
// (Journalctl), the kernel's own ring buffer under the daemon (Dmesg),
// a plain append-only file such as the git proxy's audit log (File),
// and one pod's container output through `kubectl logs` (Pod) --
// bwsalmon/agents#444's "core system logs" for the UI's System pane.
//
// Which of those a deployment has is a property of the deployment and
// not of this package: a daemon in a pod has no unit, no journal and no
// kernel log of its own, and cmd/grain wires only the sources the shape
// it is running as can actually answer (logSources).
package systemlog

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Journalctl tails a systemd unit's journal by shelling out to
// journalctl -- the same mechanism v1's `read_grain_logs` MCP tool used
// (grain/automation/mcp_server.py), and what scripts/setup.sh's own
// printed "Logs: journalctl -u grain-daemon.service -f" already tells an
// operator to reach for by hand. There is no journal client library
// vendored here; the project otherwise stays pure Go, and journalctl
// itself is already a hard dependency of the systemd unit setup.sh
// installs.
type Journalctl struct {
	// Unit is the systemd unit name to read, e.g. "grain-daemon.service".
	Unit string
}

// Tail returns the last lines entries of the unit's journal, oldest
// last. lines must be positive; see checkLines.
func (j Journalctl) Tail(ctx context.Context, lines int) ([]string, error) {
	if err := checkLines("journalctl -u "+j.Unit, lines); err != nil {
		return nil, err
	}
	return j.Read(ctx, Query{Lines: lines})
}

// Read is Tail with the rest of the bounds a caller may want: a time
// window and a minimum priority, as well as the line cap Tail is. It is
// what pkg/capability/grainlog serves an agent through read_grain_log,
// where "the last N entries" is the wrong question -- what that caller
// asks is "anything at all since this upgrade", which is a window with
// two ends and a severity floor, and reading the last N entries of the
// whole journal to filter them here would drop exactly the entries it
// came for.
//
// Every bound is a typed field rather than a string of journalctl
// arguments, and Query.args is the only thing that ever composes them,
// so no caller can reach journalctl's own argument surface -- which
// includes reading other units, other machines and other output formats
// -- by writing a clever value into one of these.
func (j Journalctl) Read(ctx context.Context, q Query) ([]string, error) {
	return journalctlQuery(ctx, "journalctl -u "+j.Unit, q, "--unit", j.Unit)
}

// Dmesg tails the kernel's own log for the machine the daemon runs on --
// the System pane's answer to the failures no userspace log can describe,
// because the process they happened to is the one that stopped writing:
// an OOM kill of an agent CLI mid-run, a disk erroring under a sandbox's
// checkout, a network interface dropping the git proxy's traffic. Sandbox
// health says the machine is unwell and Top says which process is wearing
// it out; this says what the kernel did about it.
//
// It reads that log through `journalctl --dmesg` rather than dmesg(1),
// which is the same thing by a route this deployment actually has.
// dmesg(1) reads the ring buffer through /dev/kmsg or the syslog(2)
// call, and the daemon's container is given neither -- it runs
// unprivileged as $GRAIN_USER, without CAP_SYSLOG and without that
// device (scripts/setup.sh's own docker_run_args) -- so dmesg in there
// fails on permissions, and would need util-linux added to the image to
// get that far. journald already collects the same kernel messages into
// the host journal that setup.sh bind-mounts in read-only for
// Journalctl above, group and all, so this needs nothing new installed
// and nothing new granted. Both spellings show the current boot only;
// --dmesg implies journalctl's own -b.
type Dmesg struct{}

// Tail returns the last lines entries of this boot's kernel log, oldest
// last. lines must be positive; see checkLines.
func (Dmesg) Tail(ctx context.Context, lines int) ([]string, error) {
	if err := checkLines("journalctl --dmesg", lines); err != nil {
		return nil, err
	}
	return Dmesg{}.Read(ctx, Query{Lines: lines})
}

// Read is Journalctl.Read's counterpart for the kernel's own log -- see
// that method. --dmesg implies journalctl's -b, so a window reaching
// back past this boot's start is answered by this boot alone; that is
// the kernel log this machine actually has, not a bound this narrows.
func (Dmesg) Read(ctx context.Context, q Query) ([]string, error) {
	return journalctlQuery(ctx, "journalctl --dmesg", q, "--dmesg")
}

// PodLogReader is the slice of pkg/k8s.Client that Pod below needs,
// named here rather than imported: this package is a set of small
// readers over things a deployment has, and a test of one of them should
// not need a cluster -- the same reason cmd/grain names podRestartReader
// for the restart count.
type PodLogReader interface {
	TailLogs(ctx context.Context, pod, container string, lines int) (string, error)
}

// Pod tails what one pod's container has printed, through `kubectl
// logs`. It is Journalctl's counterpart for the deployment shape that
// has no journal: grain running as a pod writes its own log to the
// container's stdout, the kubelet keeps it, and the API server serves it
// to a credential holding pods/log -- which deploy/kubernetes/grain.yaml
// already grants (grain/task-488).
//
// It reads the *running* container's log. The previous container's --
// what a crash-looping daemon failed on -- is `kubectl logs --previous`,
// exists only once there has been a restart, and is not a log this pane
// can usefully keep a tab open on; where an operator needs pointing at
// it, the daemon-health pane says so in words instead
// (ui.DaemonHealth.LogCommand, grain/task-367).
type Pod struct {
	// Pods runs the read.
	Pods PodLogReader
	// Name is the pod to read -- under Kubernetes grain reads its own,
	// which it learns from the downward API ($GRAIN_POD_NAME).
	Name string
	// Container is which container of it, or "" for a pod with one,
	// which is kubectl's own default.
	Container string
}

// Tail returns the last lines lines of that container's output, oldest
// last. lines must be positive; see checkLines.
//
// A Pod with no reader or no name is refused rather than read: a pod
// that was not told its own name would otherwise fall through to
// kubectl's own "pod name is required", and this says which reading was
// misconfigured. cmd/grain does not wire a source at all in that case,
// so this is the belt to that braces.
func (p Pod) Tail(ctx context.Context, lines int) ([]string, error) {
	desc := strings.TrimSpace("kubectl logs " + p.Name)
	if err := checkLines(desc, lines); err != nil {
		return nil, err
	}
	if p.Pods == nil || p.Name == "" {
		return nil, fmt.Errorf("%s: no pod to read the log of", desc)
	}
	out, err := p.Pods.TailLogs(ctx, p.Name, p.Container, lines)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// Query bounds one Read: which slice of a journal to return.
//
// The zero value is "the whole journal", which is not a useful read of
// anything and is why Lines has a caller-supplied floor rather than a
// default here -- every caller of Read has its own idea of how much is
// too much, and this package guesses for none of them.
type Query struct {
	// Since and Until bound the window, each optional: a zero Since is
	// the start of the journal and a zero Until is now. They are passed
	// to journalctl as "@<seconds since the epoch>", systemd.time(7)'s
	// own absolute form, so nothing here depends on which zone this
	// process, journald or the caller happens to be in.
	Since time.Time
	Until time.Time
	// Priority is the least severe journald priority to return, by name
	// -- one of Priorities, or "" for every priority. journalctl's
	// --priority is a *maximum numeric level*, which reads backwards; the
	// name here is the ordinary way an operator says it ("err and above").
	Priority string
	// Lines caps how many entries come back, most recent last, the same
	// way Tail's own argument does. Zero or negative asks journalctl for
	// no cap at all, which only a caller that has bounded the read some
	// other way should do.
	Lines int
}

// Priorities are the journald priority names Query.Priority may hold,
// most severe first. Anything else is refused by args below rather than
// passed through: this is the one field whose value reaches journalctl
// as text, and an allowlist is what keeps it from being an argument.
var Priorities = []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

// args is q as journalctl arguments, and the only place a Query becomes
// one. An unknown priority is an error rather than a silently dropped
// bound: a caller that asked for errors only and got everything would be
// handed far more than it meant to read.
func (q Query) args() ([]string, error) {
	var args []string
	if !q.Since.IsZero() {
		args = append(args, "--since", fmt.Sprintf("@%d", q.Since.Unix()))
	}
	if !q.Until.IsZero() {
		args = append(args, "--until", fmt.Sprintf("@%d", q.Until.Unix()))
	}
	if q.Priority != "" {
		if !slices.Contains(Priorities, q.Priority) {
			return nil, fmt.Errorf("systemlog: %q is not a journald priority (%s)",
				q.Priority, strings.Join(Priorities, ", "))
		}
		args = append(args, "--priority", q.Priority)
	}
	// Any non-zero value, negative included: zero is this package's own
	// "no cap", and a negative one is a caller's mistake that journalctl
	// itself refuses with a better message than anything invented here
	// (TestJournalctlErrorsNameTheReadThatFailed). No negative count
	// arrives here from Tail, which refuses one in front of every
	// implementation (checkLines); this is Read's own surface.
	if q.Lines != 0 {
		args = append(args, "--lines", strconv.Itoa(q.Lines))
	}
	return args, nil
}

// journalctlQuery runs one journalctl read bounded by q and splits its
// output. sel is what selects the journal to read -- the unit, or
// --dmesg -- and is this package's own, never a caller's. desc names the
// command in the error, since what an operator needs from a failure here
// is which read failed -- a unit that does not exist on this host, a
// journal directory the container was not given -- rather than "exit
// status 1".
func journalctlQuery(ctx context.Context, desc string, q Query, sel ...string) ([]string, error) {
	bounds, err := q.args()
	if err != nil {
		return nil, err
	}
	args := append(append(append([]string{}, sel...), bounds...),
		"--no-pager",
		"--output=short-iso")
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", desc, err, strings.TrimSpace(stderr.String()))
	}
	return withoutMarkers(splitLines(stdout.String())), nil
}

// withoutMarkers drops journalctl's own commentary from its output.
//
// journalctl writes prose into stdout beside the entries: "-- No entries
// --" for a read that matched nothing, and "-- Journal begins at ... --"
// or "-- Boot <id> --" as headers, depending on version and arguments.
// They are not journal entries and a caller counting entries must not
// count them -- a window that matched nothing would otherwise come back
// holding one "entry", which for pkg/capability/grainlog is precisely
// the difference between "nothing happened" and "something did".
//
// The test is the shape of the marker rather than its text, since the
// text is a systemd version's business: an entry in --output=short-iso
// begins with a timestamp, so nothing that begins and ends with "--" is
// one.
func withoutMarkers(lines []string) []string {
	out := lines[:0]
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "-- ") && strings.HasSuffix(trimmed, " --") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// File tails a plain append-only log file, such as the git proxy's own
// audit.log (pkg/gitproxy.FileAuditLog) -- one JSON line per request,
// with no rotation, so reading the whole thing before taking the last N
// lines needs no size guard beyond what an operator's own disk already
// enforces.
type File struct {
	// Path is the log file to read.
	Path string
}

// Tail returns the end of the file -- its last lines lines, oldest
// last -- and no lines at all (with no error) if the file does not exist -- a log that
// has not been written to yet is empty rather than broken. lines must be
// positive; see checkLines.
//
// Any *other* read failure is returned as an error rather than folded
// into that empty answer: an operator looking at an empty Logs tab needs
// to be able to tell a log nothing has been written to yet from one the
// daemon cannot read -- a path that is a directory, a file whose mode
// this process is not allowed -- and those two are the same pane
// otherwise.
//
// ctx is checked before the read but does not interrupt it: os.ReadFile
// takes no context, and this is one unrotated file read in a single
// call, so the window in which a cancellation could arrive and be
// noticed is the read itself. Checking it at the door is what the
// journalctl-backed sources give a cancelled caller
// (exec.CommandContext) as far as this one can follow them, and it means
// a browser that has closed the Logs pane does not start a read nobody
// is waiting for.
func (f File) Tail(ctx context.Context, lines int) ([]string, error) {
	if err := checkLines(f.Path, lines); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", f.Path, err)
	}
	data, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	all := splitLines(string(data))
	if len(all) <= lines {
		return all, nil
	}
	return all[len(all)-lines:], nil
}

// checkLines is what every Tail in this package does with its argument
// before reading anything: pkg/ui.LogSource's count is a number of lines
// to return, so it is positive, and a non-positive one is a caller's bug
// rather than a read.
//
// It exists because the three implementations used to answer that bug
// three different ways -- the journalctl-backed pair passed a negative
// count on to journalctl, which refused it, while File computed a slice
// bound from it and panicked on a negative one (all[len(all)-lines:]
// reaching past the end) or returned nothing at all for zero. Only
// handleGetLogLines' own validation kept that panic off a live request
// goroutine. Refusing it here, in front of every implementation, is one
// answer instead of three, and it is the answer two of them already
// gave.
//
// desc names the read, the same way journalctlQuery's errors do: what an
// operator needs from a failure in the System pane is which source
// failed, and this error is shown in the same place those are.
//
// Query.Lines is deliberately not held to this. Read's callers bound a
// read some other way -- pkg/capability/grainlog asks for a window with
// two ends -- and zero there is this package's documented "no cap".
func checkLines(desc string, lines int) error {
	if lines <= 0 {
		return fmt.Errorf("%s: lines must be positive, got %d", desc, lines)
	}
	return nil
}

// splitLines is what every source in this package turns its bytes into
// lines with, and the shape it gives them is the one the System pane
// renders against (ui/src/components/LogsPage.jsx shows "(no log lines)"
// for an empty result):
//
//   - empty input is no lines at all, not one empty line -- an empty
//     pane rather than a pane holding one blank row;
//   - a trailing newline is not a line, and neither are several, so a
//     file that ends the way a line-oriented log does reads back as its
//     entries alone;
//   - a blank line *between* entries is kept, since it is something the
//     source wrote;
//   - a last line with no newline after it is kept, which is how a log
//     an appending process was caught mid-write reads back.
func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
