package grainlog_test

// The tool a grain-log grant hands a run: what it will read, what it
// refuses to read, and -- the part that matters most -- what it says
// when it could not show a run everything that matched.
//
// The journal itself is stood in for. What this package owns is the
// bounding, the redaction and the wording of the answer; that journalctl
// runs at all is pkg/systemlog's, and its own suite reads a real journal
// for it.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/grainlog"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/systemlog"
)

// fakeJournal is one log's worth of lines, and a record of what it was
// asked for -- which is how the bounds a run wrote are checked to have
// actually reached the read.
type fakeJournal struct {
	lines []string
	asked systemlog.Query
	err   error
}

func (f *fakeJournal) read(_ context.Context, q systemlog.Query) ([]string, error) {
	f.asked = q
	if f.err != nil {
		return nil, f.err
	}
	lines := f.lines
	if q.Lines > 0 && len(lines) > q.Lines {
		// journalctl's --lines returns the *last* N of the window, which
		// is the behaviour the tool's own windowCap depends on.
		lines = lines[len(lines)-q.Lines:]
	}
	return lines, nil
}

// tool builds read_grain_log over one fake journal named "daemon", with
// a redactor holding one known value, and returns the tool plus the
// fake and the audit lines written.
func tool(t *testing.T, journal *fakeJournal) (mcp.Tool, *[]string) {
	t.Helper()
	var audit []string
	tools := grainlog.Tools(grainlog.Config{
		TaskID:   "226",
		Redactor: grainlog.NewRedactor([]grainlog.KnownValue{{Label: "secret one", Value: "s3cret-value-here"}}),
		Sources: []grainlog.Source{
			{Name: "daemon", What: "a stand-in journal", Read: journal.read},
		},
		Log: func(format string, args ...any) {
			audit = append(audit, fmt.Sprintf(format, args...))
		},
		Now: func() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) },
	})
	if len(tools) != 1 || tools[0].Name != "read_grain_log" {
		t.Fatalf("Tools = %v, want just read_grain_log", tools)
	}
	return tools[0], &audit
}

func call(t *testing.T, tl mcp.Tool, args map[string]any) mcp.Result {
	t.Helper()
	return tl.Handler(context.Background(), args)
}

func TestReadGrainLogReturnsTheLinesItRead(t *testing.T) {
	journal := &fakeJournal{lines: []string{"first entry", "second entry"}}
	tl, _ := tool(t, journal)

	got := call(t, tl, map[string]any{"source": "daemon"})
	if got.IsError {
		t.Fatalf("read_grain_log errored: %s", got.Text)
	}
	for _, want := range []string{"first entry", "second entry"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("the answer does not carry %q:\n%s", want, got.Text)
		}
	}
}

// The typed bounds reach the read as bounds, rather than being applied
// to whatever the last N entries of the whole journal happened to be.
func TestReadGrainLogPassesItsWindowToTheJournal(t *testing.T) {
	journal := &fakeJournal{lines: []string{"an entry"}}
	tl, _ := tool(t, journal)

	since := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	until := time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC)
	got := call(t, tl, map[string]any{
		"source": "daemon", "min_priority": "err",
		"since": since.Format(time.RFC3339), "until": until.Format(time.RFC3339),
	})
	if got.IsError {
		t.Fatalf("read_grain_log errored: %s", got.Text)
	}
	if !journal.asked.Since.Equal(since) || !journal.asked.Until.Equal(until) {
		t.Errorf("the journal was asked for %v..%v, want %v..%v",
			journal.asked.Since, journal.asked.Until, since, until)
	}
	if journal.asked.Priority != "err" {
		t.Errorf("the journal was asked for priority %q, want err", journal.asked.Priority)
	}
	// And the header says what was read, so the run can tell which
	// window an answer is an answer about.
	if !strings.Contains(got.Text, since.Format(time.RFC3339)) {
		t.Errorf("the answer does not say which window it read:\n%s", got.Text)
	}
}

// The line cap a run writes bounds what comes back, but never what is
// read: journalctl's --lines returns the *last* N of a window, so asking
// it for max_lines and filtering here would silently drop every older
// match and report the window clean.
func TestReadGrainLogReadsTheWholeWindowWhateverMaxLinesSays(t *testing.T) {
	journal := &fakeJournal{lines: []string{"old panic", "a", "b", "c"}}
	tl, _ := tool(t, journal)

	got := call(t, tl, map[string]any{"source": "daemon", "contains": "panic", "max_lines": 2})
	if got.IsError {
		t.Fatalf("read_grain_log errored: %s", got.Text)
	}
	if journal.asked.Lines <= 2 {
		t.Errorf("the journal was asked for %d lines, want the whole window regardless of max_lines",
			journal.asked.Lines)
	}
	if !strings.Contains(got.Text, "old panic") {
		t.Errorf("the match older than max_lines was dropped:\n%s", got.Text)
	}
}

// The failure this capability exists to fix, in miniature: an answer
// that showed a run part of what matched has to say so, loudly enough
// that a run cannot read it as the whole of it.
func TestReadGrainLogSaysWhenItShowedLessThanMatched(t *testing.T) {
	var lines []string
	for i := range 50 {
		lines = append(lines, fmt.Sprintf("entry %d", i))
	}
	journal := &fakeJournal{lines: lines}
	tl, _ := tool(t, journal)

	got := call(t, tl, map[string]any{"source": "daemon", "max_lines": 10})
	if got.IsError {
		t.Fatalf("read_grain_log errored: %s", got.Text)
	}
	if !strings.Contains(got.Text, "TRUNCATED") {
		t.Fatalf("40 of 50 entries were dropped without saying so:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "50") || !strings.Contains(got.Text, "40") {
		t.Errorf("the truncation notice does not say how much was dropped:\n%s", got.Text)
	}
	// The notice is above the lines, not after them: a run that stops
	// reading at the last entry has still read it.
	if strings.Index(got.Text, "TRUNCATED") > strings.Index(got.Text, "entry 49") {
		t.Errorf("the truncation notice is below the entries:\n%s", got.Text)
	}
}

// The other truncation, and the one that would be invisible without
// this: a window holding more entries than one call reads at all. "I saw
// nothing" and "I was cut off before I got there" are different answers
// and the run has to be able to tell them apart.
func TestReadGrainLogSaysWhenTheWindowItselfWasCutOff(t *testing.T) {
	var lines []string
	for i := range 6000 {
		lines = append(lines, fmt.Sprintf("entry %d", i))
	}
	journal := &fakeJournal{lines: lines}
	tl, _ := tool(t, journal)

	got := call(t, tl, map[string]any{"source": "daemon", "contains": "nothing matches this"})
	if got.IsError {
		t.Fatalf("read_grain_log errored: %s", got.Text)
	}
	if !strings.Contains(got.Text, "TRUNCATED") {
		t.Fatalf("a window read to its bound reported no truncation:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "NOT examined") {
		t.Errorf("the notice does not say the rest of the window went unread:\n%s", got.Text)
	}
	// And an empty result inside a truncated window does not read as a
	// clean one.
	if !strings.Contains(got.Text, "No entries matched") {
		t.Errorf("the answer does not say that nothing matched:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "per the truncation above") {
		t.Errorf("an empty answer inside a cut-off window reads as a clean window:\n%s", got.Text)
	}
}

// And when nothing matched in a window that *was* read whole, the
// answer says outright that this is an answer. A run that reads silence
// as a failure goes looking somewhere worse.
func TestReadGrainLogSaysAnEmptyWindowIsAnAnswer(t *testing.T) {
	journal := &fakeJournal{lines: []string{"nothing of interest"}}
	tl, _ := tool(t, journal)

	got := call(t, tl, map[string]any{"source": "daemon", "contains": "panic"})
	if got.IsError {
		t.Fatalf("read_grain_log errored: %s", got.Text)
	}
	if !strings.Contains(got.Text, "This is an answer, not a failure") {
		t.Errorf("an empty window does not say it is an answer:\n%s", got.Text)
	}
	if strings.Contains(got.Text, "TRUNCATED") {
		t.Errorf("a window read whole reported truncation:\n%s", got.Text)
	}
}

// Redaction happens between the read and the answer, here on the host.
func TestReadGrainLogRedactsBeforeAnswering(t *testing.T) {
	journal := &fakeJournal{lines: []string{
		"grain[1]: authenticating with s3cret-value-here",
		"grain[1]: and with ghp_AbCdEfGhIjKlMnOpQrStUvWxYz01",
	}}
	tl, audit := tool(t, journal)

	got := call(t, tl, map[string]any{"source": "daemon"})
	if strings.Contains(got.Text, "s3cret-value-here") {
		t.Fatalf("a known value left the host:\n%s", got.Text)
	}
	if strings.Contains(got.Text, "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz01") {
		t.Fatalf("a token shape left the host:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "redacted on the host") {
		t.Errorf("the answer does not say that anything was redacted:\n%s", got.Text)
	}
	if len(*audit) != 1 || !strings.Contains((*audit)[0], "2 redacted") {
		t.Errorf("the audit line does not record the redaction count: %v", *audit)
	}
}

// No redactor is not "nothing to redact": it is a process that could not
// read what it would have removed, and it refuses rather than serving
// the journal it could not clean.
func TestReadGrainLogRefusesWithoutARedactor(t *testing.T) {
	journal := &fakeJournal{lines: []string{"grain[1]: authenticating with s3cret-value-here"}}
	var audit []string
	tools := grainlog.Tools(grainlog.Config{
		TaskID:  "226",
		Sources: []grainlog.Source{{Name: "daemon", What: "a stand-in", Read: journal.read}},
		Log:     func(f string, a ...any) { audit = append(audit, fmt.Sprintf(f, a...)) },
	})

	got := call(t, tools[0], map[string]any{"source": "daemon"})
	if !got.IsError {
		t.Fatalf("a server with no redactor served the journal anyway:\n%s", got.Text)
	}
	if strings.Contains(got.Text, "s3cret-value-here") {
		t.Fatalf("the refusal carried a journal line with it:\n%s", got.Text)
	}
	// The tool is still on the roster, saying what is wrong, rather than
	// absent and read as a capability never granted.
	if len(tools) != 1 {
		t.Errorf("Tools = %v, want the tool present and refusing", tools)
	}
	// A refused read is as much a thing an operator wants in the trail
	// as one that happened.
	if len(audit) != 1 || !strings.Contains(audit[0], "refused") {
		t.Errorf("the refusal was not audited: %v", audit)
	}
}

// The allowlist is the whole of what is reachable: there is no parameter
// through which a run can name a unit that is not in it.
func TestReadGrainLogRefusesALogItWasNotGiven(t *testing.T) {
	journal := &fakeJournal{lines: []string{"an entry"}}
	tl, _ := tool(t, journal)

	for _, source := range []string{"grain-config-sync.service", "sshd", "", "../daemon"} {
		got := call(t, tl, map[string]any{"source": source})
		if !got.IsError {
			t.Errorf("source %q was served:\n%s", source, got.Text)
		}
		if !strings.Contains(got.Text, "daemon") {
			t.Errorf("the refusal does not say what can be read instead:\n%s", got.Text)
		}
	}
}

// Times are RFC3339 and nothing else. journalctl's own expressions
// ("-1h", "yesterday") are a second argument language, and this tool
// deliberately speaks none of it.
func TestReadGrainLogTakesRFC3339TimesOnly(t *testing.T) {
	journal := &fakeJournal{lines: []string{"an entry"}}
	tl, _ := tool(t, journal)

	for _, since := range []string{"-1h", "yesterday", "2026-09-07", "--unit=sshd"} {
		got := call(t, tl, map[string]any{"source": "daemon", "since": since})
		if !got.IsError {
			t.Errorf("since %q was accepted:\n%s", since, got.Text)
		}
	}
}

func TestReadGrainLogRefusesAPriorityThatIsNotOne(t *testing.T) {
	journal := &fakeJournal{lines: []string{"an entry"}}
	tl, _ := tool(t, journal)

	got := call(t, tl, map[string]any{"source": "daemon", "min_priority": "--output=json"})
	if !got.IsError {
		t.Fatalf("an arbitrary priority was accepted:\n%s", got.Text)
	}
	if journal.asked.Priority != "" {
		t.Errorf("it reached the journal anyway: %q", journal.asked.Priority)
	}
}

// A window wider than one call may ask for is refused with the reason,
// rather than quietly narrowed -- a narrowed window would answer a
// different question than the one asked and say nothing about it.
func TestReadGrainLogRefusesAWindowWiderThanItWillRead(t *testing.T) {
	journal := &fakeJournal{lines: []string{"an entry"}}
	tl, _ := tool(t, journal)

	got := call(t, tl, map[string]any{
		"source": "daemon",
		"since":  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})
	if !got.IsError {
		t.Fatalf("a year-wide window was accepted:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "narrowing since/until") {
		t.Errorf("the refusal does not say how to ask a narrower question:\n%s", got.Text)
	}
}

// max_lines is capped rather than refused: a run asking for more than
// one call can carry gets the maximum and the truncation notice, which
// is a useful answer, where a refusal would just cost it a turn.
func TestReadGrainLogCapsMaxLines(t *testing.T) {
	var lines []string
	for i := range 3000 {
		lines = append(lines, fmt.Sprintf("entry %d", i))
	}
	journal := &fakeJournal{lines: lines}
	tl, _ := tool(t, journal)

	got := call(t, tl, map[string]any{"source": "daemon", "max_lines": 99999})
	if got.IsError {
		t.Fatalf("read_grain_log errored: %s", got.Text)
	}
	if strings.Count(got.Text, "entry ") > 1100 {
		t.Errorf("the answer carried more than the cap: %d entries", strings.Count(got.Text, "entry "))
	}
	if !strings.Contains(got.Text, "TRUNCATED") {
		t.Errorf("a capped answer did not say it was capped:\n%s", got.Text)
	}
}

// Every call is logged, whatever it asked for, so what a run read is
// recoverable afterwards.
func TestReadGrainLogAuditsEveryCall(t *testing.T) {
	journal := &fakeJournal{lines: []string{"an entry"}}
	tl, audit := tool(t, journal)

	call(t, tl, map[string]any{"source": "daemon", "contains": "panic", "min_priority": "err"})
	if len(*audit) != 1 {
		t.Fatalf("audit = %v, want one line", *audit)
	}
	for _, want := range []string{"226", "daemon", "panic", "err"} {
		if !strings.Contains((*audit)[0], want) {
			t.Errorf("the audit line does not record %q: %s", want, (*audit)[0])
		}
	}
}

// A journal that cannot be read is a failure the run is told about
// plainly -- and it is not an empty window, which is the one thing it
// must never be mistaken for.
func TestReadGrainLogReportsAFailedRead(t *testing.T) {
	journal := &fakeJournal{err: fmt.Errorf("journalctl -u grain-daemon.service: exit status 1")}
	tl, audit := tool(t, journal)

	got := call(t, tl, map[string]any{"source": "daemon"})
	if !got.IsError {
		t.Fatalf("a failed read was answered as a result:\n%s", got.Text)
	}
	if strings.Contains(got.Text, "No entries matched") {
		t.Fatalf("a failed read reads as an empty window:\n%s", got.Text)
	}
	if len(*audit) != 1 || !strings.Contains((*audit)[0], "failed") {
		t.Errorf("the failure was not audited: %v", *audit)
	}
}

// The tool is always on the roster, and the schema says which logs there
// are: a run should be able to see what it may ask for without guessing.
func TestReadGrainLogSchemaNamesTheLogsItWillServe(t *testing.T) {
	tools := grainlog.Tools(grainlog.Config{Redactor: grainlog.NewRedactor(nil)})
	if len(tools) != 1 {
		t.Fatalf("Tools = %v, want one", tools)
	}
	props, _ := tools[0].InputSchema["properties"].(map[string]any)
	source, _ := props["source"].(map[string]any)
	enum, _ := source["enum"].([]string)
	want := map[string]bool{"daemon": true, "dmesg": true}
	if len(enum) != len(want) {
		t.Fatalf("source enum = %v, want exactly %v", enum, want)
	}
	for _, name := range enum {
		if !want[name] {
			t.Errorf("source enum offers %q, which is not on the default allowlist", name)
		}
	}
}
