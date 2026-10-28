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

package systemlog_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/systemlog"
)

// The real journalctl against this machine's real kernel journal, not a
// fake standing in for it -- the whole of Dmesg is the flags it passes,
// and a stub would assert only that the stub was written to match (the
// same call pkg/hosttop's suite makes about `top`). A machine with no
// journalctl, or one whose journal this test cannot read, skips: both
// are properties of the runner rather than of the code under test.
func requireKernelJournal(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("journalctl"); err != nil {
		t.Skip("journalctl is not installed (systemd)")
	}
	out, err := exec.Command("journalctl", "--dmesg", "--lines", "1", "--no-pager").Output()
	if err != nil {
		t.Skipf("this machine's kernel journal is not readable: %v", err)
	}
	// Readable and empty is its own case, and journalctl exits 0 for it:
	// an unprivileged process on a machine whose kernel messages are
	// restricted to the systemd-journal group gets "-- No entries --"
	// and success. The tests below are about what a journal with entries
	// in it reads back as, so that machine skips them rather than
	// failing them.
	if body := strings.TrimSpace(string(out)); body == "" || strings.HasPrefix(body, "--") {
		t.Skip("this machine's kernel journal is readable but holds nothing this process may see")
	}
}

// requireReadableJournal is the weaker guard: journalctl runs and this
// process may read the kernel journal, whether or not there is anything
// in it. It is what the tests about an *empty* read want -- those have
// nothing to say about a machine with entries, and skipping them on one
// without any would skip exactly the case they are about.
func requireReadableJournal(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("journalctl"); err != nil {
		t.Skip("journalctl is not installed (systemd)")
	}
	if err := exec.Command("journalctl", "--dmesg", "--lines", "1", "--no-pager").Run(); err != nil {
		t.Skipf("this machine's kernel journal is not readable: %v", err)
	}
}

func TestDmesgTailReadsTheKernelJournal(t *testing.T) {
	requireKernelJournal(t)

	lines, err := (systemlog.Dmesg{}).Tail(context.Background(), 5)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("Tail returned no lines, but the journal has kernel entries")
	}
	// journalctl's --lines is a cap on entries, and short-iso prints one
	// line per entry -- so asking for 5 is asking for at most 5, which is
	// what keeps GET /api/logs/dmesg?lines= a bound rather than a hint.
	if len(lines) > 5 {
		t.Errorf("Tail(5) returned %d lines: %v", len(lines), lines)
	}
}

// A caller that gives up -- the browser closing the Logs pane's own
// request -- takes journalctl down with it rather than leaving it
// reading.
func TestDmesgTailFailsOnACancelledContext(t *testing.T) {
	requireKernelJournal(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := (systemlog.Dmesg{}).Tail(ctx, 5); err == nil {
		t.Fatal("Tail on a cancelled context returned no error")
	}
}

// writeFakeJournalctl installs a shell script named "journalctl" at the
// front of PATH, which appends the argv it was called with to argvLog
// (one argument per line, so nothing here has to unquote anything) and
// prints a journal in place of reading one: two of the markers journalctl
// writes beside its entries, and three entries oldest-first, which is the
// order --output=short-iso hands them over in.
//
// The header at the top of this file argues against a fake, and it is
// right about the fake it is arguing against: for Dmesg, whose whole
// substance is the flags it passes to a journal every machine has, a stub
// would assert only that the stub was written to match. This is the read
// that cannot be made real on a runner -- Journalctl is wired in
// pkg/cli/daemon.go to grain-daemon.service and grain-config-sync.service,
// which exist on the deployment and on no CI machine -- and what is
// substituted here is a different thing from an interface fake. The code
// under test is handed no seam and takes no new argument: it composes its
// own argv and shells out through exec.CommandContext exactly as it does
// in production, and what is replaced is the program on the far side of
// that exec, which writes down what it was actually asked for. So the
// assertion is about grain's argv, not about a stub's manners.
//
// What it cannot check is that journalctl means by those arguments what
// this package believes it does -- that --unit really narrows the read,
// that --lines really caps it, that entries really arrive oldest-first.
// TestJournalctlTailReadsARealUnitsJournalOldestFirst is that half,
// against whatever unit the runner does happen to have.
func writeFakeJournalctl(t *testing.T, argvLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake journalctl is a POSIX shell script")
	}
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" >> %s
cat <<'ENTRIES'
-- Journal begins at Sun 2026-09-06 00:00:00 UTC, ends at Mon 2026-09-07 12:00:00 UTC. --
%s
%s
%s
-- No more entries --
ENTRIES
`, argvLog, fakeJournalEntries[0], fakeJournalEntries[1], fakeJournalEntries[2])
	if err := os.WriteFile(filepath.Join(dir, "journalctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeJournalEntries is what that script prints between its markers,
// oldest first.
var fakeJournalEntries = []string{
	"2026-09-07T11:59:58+0000 host grain-daemon[1]: oldest",
	"2026-09-07T11:59:59+0000 host grain-daemon[1]: middle",
	"2026-09-07T12:00:00+0000 host grain-daemon[1]: newest",
}

// The unit a Journalctl is given is the unit it reads, and the only
// journal it reads. Nothing else in this suite pins that: every other
// Journalctl test is a failure, so a dropped or mistyped --unit would
// leave the daemon pane quietly showing the whole journal, or another
// unit's, with the suite still green.
//
// The argv is asserted whole rather than searched for a substring. It is
// short, this package composes all of it (Query.args and journalctlQuery
// are the only things that do, deliberately, so that no caller can reach
// journalctl's own argument surface), and a flag appearing here that this
// file does not know about is exactly the change worth stopping on.
func TestJournalctlTailReadsTheUnitItWasGiven(t *testing.T) {
	// Both units the daemon actually wires, so that a Unit ignored in
	// favour of a hard-coded name cannot pass.
	for _, unit := range []string{"grain-daemon.service", "grain-config-sync.service"} {
		t.Run(unit, func(t *testing.T) {
			argvLog := filepath.Join(t.TempDir(), "argv")
			writeFakeJournalctl(t, argvLog)

			lines, err := (systemlog.Journalctl{Unit: unit}).Tail(context.Background(), 3)
			if err != nil {
				t.Fatalf("Tail: %v", err)
			}
			// Oldest first, in the order journalctl printed them, with
			// journalctl's own commentary dropped -- what pkg/ui.LogSource
			// promises the pane it hands lines to.
			if !slices.Equal(lines, fakeJournalEntries) {
				t.Errorf("lines = %#v, want %#v", lines, fakeJournalEntries)
			}
			assertOldestFirst(t, lines)

			argv := readLines(t, argvLog)
			// --lines is journalctl's own cap and is passed as one; that it
			// is honoured is journalctl's business, and the real-journal
			// test below is what holds it to it.
			want := []string{"--unit", unit, "--lines", "3", "--no-pager", "--output=short-iso"}
			if !slices.Equal(argv, want) {
				t.Errorf("journalctl called with %v, want %v", argv, want)
			}
		})
	}
}

// The other half: with the real journalctl, against a unit this machine
// really has, those arguments mean what the test above assumes. A runner
// has no grain-daemon.service, so this reads whichever unit does have a
// journal here -- which says nothing about grain's own units and
// everything about the flags, since they are the same flags.
func TestJournalctlTailReadsARealUnitsJournalOldestFirst(t *testing.T) {
	unit := unitWithAReadableJournal(t)

	lines, err := (systemlog.Journalctl{Unit: unit}).Tail(context.Background(), 5)
	if err != nil {
		t.Fatalf("Tail(%s): %v", unit, err)
	}
	if len(lines) == 0 {
		t.Fatalf("Tail returned no lines, but %s has journal entries", unit)
	}
	// The same bound TestDmesgTailReadsTheKernelJournal holds --dmesg to:
	// GET /api/logs/<source>?lines= is a bound rather than a hint.
	if len(lines) > 5 {
		t.Errorf("Tail(5) returned %d lines: %v", len(lines), lines)
	}

	assertOldestFirst(t, lines)
}

// assertOldestFirst holds a read to the order pkg/ui.LogSource's doc
// comment promises the pane it feeds: oldest first, newest last, which is
// the order --output=short-iso prints and the order the Logs tab renders
// straight down the page.
func assertOldestFirst(t *testing.T, lines []string) {
	t.Helper()
	var stamps []time.Time
	for i, line := range lines {
		stamp, ok := shortISOStamp(line)
		if !ok {
			// An entry whose message holds a newline prints across several
			// lines, and only the first of them carries a timestamp -- so a
			// later line without one is a continuation rather than a fault.
			// The first line is some entry's own first line, and nothing
			// but a timestamp belongs at its head.
			if i == 0 {
				t.Fatalf("first line does not begin with a short-iso timestamp: %q", line)
			}
			continue
		}
		stamps = append(stamps, stamp)
	}
	if !slices.IsSortedFunc(stamps, func(a, b time.Time) int { return a.Compare(b) }) {
		t.Errorf("entries are not oldest-first: %v", lines)
	}
}

// shortISOStamp is the timestamp --output=short-iso puts at the head of
// each entry: systemd formats it with strftime's %z, so the zone offset
// has no colon in it, but a version that grew one should not read as a
// line with no timestamp at all.
func shortISOStamp(line string) (time.Time, bool) {
	head, _, ok := strings.Cut(line, " ")
	if !ok {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339} {
		if stamp, err := time.Parse(layout, head); err == nil {
			return stamp, true
		}
	}
	return time.Time{}, false
}

// unitWithAReadableJournal names a systemd unit on this machine whose
// journal this process may read and which has something in it, or skips
// -- both being properties of the runner rather than of the code under
// test, the same call requireKernelJournal makes for the kernel journal.
func unitWithAReadableJournal(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("journalctl"); err != nil {
		t.Skip("journalctl is not installed (systemd)")
	}
	out, err := exec.Command("systemctl", "list-units", "--type=service", "--all",
		"--no-legend", "--no-pager", "--plain").Output()
	if err != nil {
		t.Skipf("this machine's units cannot be listed: %v", err)
	}
	// Bounded: this is a search for one usable unit, not a survey, and on
	// a machine where none of the first several can be read the rest are
	// very unlikely to differ.
	const tried = 40
	var units []string
	for _, line := range strings.Split(string(out), "\n") {
		// The unit name is the first field, except that systemctl puts a
		// bullet in front of a failed one, so this takes the first field
		// that looks like a unit rather than the first field.
		for _, field := range strings.Fields(line) {
			if strings.HasSuffix(field, ".service") {
				units = append(units, field)
				break
			}
		}
	}
	for _, unit := range units[:min(len(units), tried)] {
		body, err := exec.Command("journalctl", "--unit", unit, "--lines", "2",
			"--no-pager", "--output=short-iso").Output()
		if err != nil {
			continue
		}
		// Readable and empty is journalctl exiting 0 with a marker and
		// nothing else -- which is what an unprivileged process gets for a
		// unit whose entries it may not see, as well as what a unit that
		// has never logged anything gets. Neither is a journal to read.
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
				return unit
			}
		}
	}
	t.Skip("no unit on this machine has a journal this process may read")
	return ""
}

// readLines reads a file the fake journalctl wrote, as lines.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// The error an unreadable source produces names the read that failed --
// "journalctl -u ..." or "journalctl --dmesg" -- rather than a bare exit
// status, since that name is the whole of what the pane can show an
// operator about it.
func TestJournalctlErrorsNameTheReadThatFailed(t *testing.T) {
	if _, err := exec.LookPath("journalctl"); err != nil {
		t.Skip("journalctl is not installed (systemd)")
	}

	// A negative Query.Lines fails on any host, whatever its journal
	// holds: journalctl reads the "-1" as a flag of its own and refuses
	// it. That is Read's own surface -- Tail refuses a non-positive count
	// before any process starts (TestTailRefusesANonPositiveCount) -- and
	// this is about what a failed read reports, not about the count.
	_, err := (systemlog.Journalctl{Unit: "grain-daemon.service"}).Read(
		context.Background(), systemlog.Query{Lines: -1})
	if err == nil {
		t.Fatal("Journalctl.Read with Lines: -1 returned no error")
	}
	if !strings.Contains(err.Error(), "journalctl -u grain-daemon.service") {
		t.Errorf("error = %q, want it to name the unit read", err)
	}
	_, err = (systemlog.Dmesg{}).Read(context.Background(), systemlog.Query{Lines: -1})
	if err == nil {
		t.Fatal("Dmesg.Read with Lines: -1 returned no error")
	}
	if !strings.Contains(err.Error(), "journalctl --dmesg") {
		t.Errorf("error = %q, want it to name the kernel read", err)
	}
}

// Every implementation of pkg/ui.LogSource in this package answers a
// non-positive count the same way: an error naming the source, before it
// reads anything. They used not to -- File turned -1 into a slice bound
// past the end of the file and panicked on it, and 0 into an empty
// answer, while the journalctl-backed pair errored on both -- so this
// pins the one contract rather than leaving it to arithmetic. No
// production caller sends a non-positive count (handleGetLogLines
// rejects a non-positive ?lines= with a 400), which is what made the
// panic latent rather than live; this is the guard that means it does
// not depend on that handler.
//
// It needs no journalctl and no readable journal: the point is that the
// count is refused before either matters.
func TestTailRefusesANonPositiveCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Spelled out here rather than imported: pkg/ui.LogSource is a
	// structural interface this package satisfies without either package
	// depending on the other (pkg/agent/codex says the same of its own).
	type logSource interface {
		Tail(ctx context.Context, lines int) ([]string, error)
	}
	sources := map[string]struct {
		source logSource
		names  string
	}{
		"Journalctl": {systemlog.Journalctl{Unit: "grain-daemon.service"}, "journalctl -u grain-daemon.service"},
		"Dmesg":      {systemlog.Dmesg{}, "journalctl --dmesg"},
		"File":       {systemlog.File{Path: path}, path},
		"Pod":        {systemlog.Pod{Pods: &fakePodLogs{}, Name: "grain-7d9f-abcde"}, "kubectl logs grain-7d9f-abcde"},
	}
	for name, tc := range sources {
		for _, lines := range []int{-1, 0} {
			got, err := tc.source.Tail(context.Background(), lines)
			if err == nil {
				t.Errorf("%s.Tail(%d) returned no error (lines = %v)", name, lines, got)
				continue
			}
			if got != nil {
				t.Errorf("%s.Tail(%d) returned %v beside its error, want no lines", name, lines, got)
			}
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("%s.Tail(%d) error = %q, want it to name the source (%s)", name, lines, err, tc.names)
			}
		}
	}
}

// The file behind a refused count is not read at all -- a count is
// checked before the source is touched, so a File over a path that
// cannot be read reports the count rather than the path's own trouble.
func TestFileTailRefusesTheCountBeforeReadingTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.log")
	_, err := (systemlog.File{Path: path}).Tail(context.Background(), 0)
	if err == nil {
		t.Fatal("File.Tail(0) over a missing file returned no error")
	}
	if !strings.Contains(err.Error(), "lines must be positive") {
		t.Errorf("error = %q, want it to say the count was wrong", err)
	}
}

func TestFileTailReturnsWholeFileWhenShorterThanRequested(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := (systemlog.File{Path: path}).Tail(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"one", "two", "three"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("lines = %v, want %v", lines, want)
		}
	}
}

func TestFileTailReturnsOnlyTheLastNLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := (systemlog.File{Path: path}).Tail(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"three", "four"}
	if len(lines) != len(want) || lines[0] != want[0] || lines[1] != want[1] {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
}

func TestFileTailOfMissingFileIsEmptyNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.log")
	lines, err := (systemlog.File{Path: path}).Tail(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatalf("lines = %v, want empty", lines)
	}
}

// The other half of that decision: a log that cannot be read is an
// error, not the empty answer a log that does not exist yet gets. It is
// the half an operator needs more, because both halves are the same
// empty Logs tab otherwise -- "the git proxy has not served a request
// yet" and "the daemon cannot read its audit log" are one pane, and only
// the error tells them apart.
//
// A directory is the case that fails for every runner, root included,
// which is why it carries the assertion; the unreadable-mode case below
// is the one this actually happens as in production and skips for uid 0.
func TestFileTailReportsAReadItCannotDoRatherThanAnEmptyLog(t *testing.T) {
	dir := t.TempDir()
	lines, err := (systemlog.File{Path: dir}).Tail(context.Background(), 10)
	if err == nil {
		t.Fatalf("Tail over a directory returned no error (lines = %v)", lines)
	}
	if lines != nil {
		t.Errorf("Tail returned %v beside its error, want no lines", lines)
	}
	// The path is the whole of what the pane can show about the failure,
	// so it has to survive into the message -- os.ReadFile's *PathError
	// carries it, and nothing here may replace that with a bare "is a
	// directory".
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error = %q, want it to name the path read (%s)", err, dir)
	}
}

func TestFileTailReportsAFileItMayNotOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which may open a 0o000 file regardless of its mode")
	}
	path := filepath.Join(t.TempDir(), "no-perm.log")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	lines, err := (systemlog.File{Path: path}).Tail(context.Background(), 10)
	if err == nil {
		t.Fatalf("Tail over an unreadable file returned no error (lines = %v)", lines)
	}
	if lines != nil {
		t.Errorf("Tail returned %v beside its error, want no lines", lines)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error = %q, want a permission error", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %q, want it to name the path read (%s)", err, path)
	}
}

// An empty log is no lines, not one empty line. The System pane shows
// "(no log lines)" for the first and a pane containing one blank row for
// the second (ui/src/components/LogsPage.jsx renders
// `lines.length > 0 ? lines.join("\n") : "(no log lines)"`), so the
// difference is visible to an operator -- and an audit log the git proxy
// has created but not yet written a request to is exactly this file.
func TestFileTailOfAnEmptyFileIsNoLinesNotOneBlankLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := (systemlog.File{Path: path}).Tail(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if lines != nil {
		t.Fatalf("lines = %#v, want none at all", lines)
	}
}

// The real audit log is appended to by a live process, so a reader can
// catch it between a line's bytes and its newline. That last line is
// kept rather than dropped: it is the most recent request the proxy
// served, which is the one an operator opening the pane came to see.
func TestFileTailKeepsALastLineWithNoNewlineAfterIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, []byte("one\ntwo"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := (systemlog.File{Path: path}).Tail(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"one", "two"}; !slices.Equal(lines, want) {
		t.Fatalf("lines = %#v, want %#v", lines, want)
	}
}

// A blank line the source wrote is a line; the newline that ends the
// last one, and any after it, are not. Trailing blanks are what a log
// flushed with a spare newline or two leaves behind, and counting them
// as entries would pad the pane with rows nothing wrote.
func TestFileTailKeepsInteriorBlankLinesAndTrimsTrailingOnes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, []byte("one\n\nthree\n\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := (systemlog.File{Path: path}).Tail(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"one", "", "three"}; !slices.Equal(lines, want) {
		t.Fatalf("lines = %#v, want %#v", lines, want)
	}
}

// A caller that has given up gets an error rather than a read, the same
// as it does from the journalctl-backed sources
// (TestDmesgTailFailsOnACancelledContext) -- one interface, one answer
// to a cancelled context, over a file this process could otherwise read
// perfectly well.
func TestFileTailFailsOnACancelledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lines, err := (systemlog.File{Path: path}).Tail(ctx, 10)
	if err == nil {
		t.Fatalf("Tail on a cancelled context returned no error (lines = %v)", lines)
	}
	if lines != nil {
		t.Errorf("Tail returned %v beside its error, want no lines", lines)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %q, want it to wrap context.Canceled", err)
	}
	// Named the same way every other failure in this package is: the
	// System pane shows one error line per source, and which source it
	// came from is the whole of what an operator can act on.
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %q, want it to name the source (%s)", err, path)
	}
}

// A Query's bounds reach journalctl as bounds. A window entirely in the
// future is the one assertion that needs no assumption about what this
// machine's journal holds: whatever is in it, none of it is from
// tomorrow, so an empty answer is the window having been applied.
func TestReadAppliesItsWindow(t *testing.T) {
	requireReadableJournal(t)

	tomorrow := time.Now().Add(24 * time.Hour)
	lines, err := (systemlog.Dmesg{}).Read(context.Background(), systemlog.Query{
		Since: tomorrow, Until: tomorrow.Add(time.Hour), Lines: 10,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("a window starting tomorrow returned %d entries: %v", len(lines), lines)
	}
}

// A priority is a name off Priorities and nothing else. This is the one
// field of a Query that reaches journalctl as text, and the allowlist is
// what keeps it from being an argument -- so it is refused here, before
// any process is started, whatever the caller had in mind.
func TestReadRefusesAPriorityThatIsNotOne(t *testing.T) {
	_, err := (systemlog.Dmesg{}).Read(context.Background(), systemlog.Query{Priority: "--output=json"})
	if err == nil {
		t.Fatal("Read accepted a priority that is not one")
	}
	if !strings.Contains(err.Error(), "not a journald priority") {
		t.Errorf("error = %q, want it to say what was wrong with the priority", err)
	}
}

// Every name in Priorities is one journalctl actually takes -- the list
// is an allowlist, and an allowlist that names something the tool
// refuses would turn a legitimate read into a failure a caller cannot
// explain.
func TestEveryPriorityNameIsOneJournalctlAccepts(t *testing.T) {
	requireReadableJournal(t)

	for _, priority := range systemlog.Priorities {
		if _, err := (systemlog.Dmesg{}).Read(context.Background(), systemlog.Query{
			Priority: priority, Lines: 1,
		}); err != nil {
			t.Errorf("Read at priority %q: %v", priority, err)
		}
	}
}

// journalctl's own commentary is not a journal entry. "-- No entries --"
// is what it prints for a read that matched nothing, and a caller
// counting entries that counted it would read an empty window as a full
// one -- which is exactly the distinction read_grain_log is built to
// keep (pkg/capability/grainlog).
func TestReadReturnsNoEntriesRatherThanJournalctlsNoteAboutThem(t *testing.T) {
	requireReadableJournal(t)

	tomorrow := time.Now().Add(24 * time.Hour)
	lines, err := (systemlog.Dmesg{}).Read(context.Background(), systemlog.Query{
		Since: tomorrow, Until: tomorrow.Add(time.Hour), Lines: 10,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			t.Errorf("Read returned journalctl's own commentary as an entry: %q", line)
		}
	}
}

// fakePodLogs stands in for pkg/k8s.Client, which is what Pod reads
// through in a real deployment -- the interface is named in this package
// (systemlog.PodLogReader) precisely so a test of it needs no cluster,
// and so the arguments Pod passes on can be asserted rather than
// inferred from a kubectl command line.
type fakePodLogs struct {
	out string
	err error

	pod       string
	container string
	lines     int
	calls     int
}

func (f *fakePodLogs) TailLogs(ctx context.Context, pod, container string, lines int) (string, error) {
	f.calls++
	f.pod, f.container, f.lines = pod, container, lines
	return f.out, f.err
}

// Pod is the Logs tab's "daemon" source on a Kubernetes deployment: it
// reads this pod's own container through `kubectl logs`, bounded to the
// lines the pane asked for, and splits them the way every other source
// here does (grain/task-488).
func TestPodTailReadsTheNamedContainerBoundedToTheLinesAskedFor(t *testing.T) {
	t.Parallel()
	pods := &fakePodLogs{out: "one\ntwo\nthree\n"}
	source := systemlog.Pod{Pods: pods, Name: "grain-7d9f-abcde", Container: "grain"}

	lines, err := source.Tail(context.Background(), 500)
	if err != nil {
		t.Fatalf("Pod.Tail: %v", err)
	}
	if !slices.Equal(lines, []string{"one", "two", "three"}) {
		t.Errorf("Pod.Tail = %v, want the container's lines with the trailing newline dropped", lines)
	}
	if pods.pod != "grain-7d9f-abcde" || pods.container != "grain" {
		t.Errorf("Pod.Tail read %q/%q, want the pod and container it was given", pods.pod, pods.container)
	}
	// The bound matters more here than for a file: the whole log is
	// however much this daemon has printed since it started, and the
	// pane asks for a few hundred lines of it.
	if pods.lines != 500 {
		t.Errorf("Pod.Tail asked for %d lines, want the 500 its caller did", pods.lines)
	}
}

// The same "empty is empty, not one blank line" splitLines gives every
// other source: a container that has printed nothing reads back as an
// empty pane rather than a pane holding one blank row.
func TestPodTailOfAContainerThatHasPrintedNothingIsNoLines(t *testing.T) {
	t.Parallel()
	source := systemlog.Pod{Pods: &fakePodLogs{}, Name: "grain-7d9f-abcde"}

	lines, err := source.Tail(context.Background(), 10)
	if err != nil {
		t.Fatalf("Pod.Tail: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("Pod.Tail = %v, want no lines at all", lines)
	}
}

// A Pod with no pod to read is a misconfigured source rather than a
// read: cmd/grain wires none at all for a pod that was not told its own
// name ($GRAIN_POD_NAME), and if one reached here anyway it must not
// fall through to kubectl's own "pod name is required".
func TestPodTailWithNoPodToReadIsRefusedRatherThanRun(t *testing.T) {
	t.Parallel()
	pods := &fakePodLogs{out: "somebody else's log\n"}
	if _, err := (systemlog.Pod{Pods: pods}).Tail(context.Background(), 10); err == nil {
		t.Error("Pod.Tail with no pod name returned no error")
	}
	if pods.calls != 0 {
		t.Errorf("Pod.Tail with no pod name ran %d reads, want none", pods.calls)
	}
	if _, err := (systemlog.Pod{Name: "grain-7d9f-abcde"}).Tail(context.Background(), 10); err == nil {
		t.Error("Pod.Tail with no reader returned no error")
	}
}

// And a failed read is the failure itself, not an empty log: an operator
// looking at the Logs tab needs to tell "this container has printed
// nothing" from "this deployment may not read its own log", which is the
// same distinction File.Tail draws for a path it cannot open.
func TestPodTailReportsAReadItCannotDo(t *testing.T) {
	t.Parallel()
	pods := &fakePodLogs{err: errors.New(`k8s: reading pod "grain-7d9f-abcde"'s logs: exit status 1`)}
	lines, err := (systemlog.Pod{Pods: pods, Name: "grain-7d9f-abcde"}).Tail(context.Background(), 10)
	if err == nil {
		t.Fatalf("Pod.Tail returned no error (lines = %v)", lines)
	}
	if lines != nil {
		t.Errorf("Pod.Tail returned %v beside its error, want no lines", lines)
	}
	if !strings.Contains(err.Error(), "grain-7d9f-abcde") {
		t.Errorf("Pod.Tail error = %q, want it to name the pod the read failed for", err)
	}
}
