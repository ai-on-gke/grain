package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeReporter struct {
	notes  []string
	report StatusReport
	err    error
}

func (f *fakeReporter) ReportStatus(_ context.Context, note string) (StatusReport, error) {
	f.notes = append(f.notes, note)
	return f.report, f.err
}

// updateStatus calls the tool the way a client would: by name, off the
// constructor, with whatever arguments the model sent.
func updateStatus(t *testing.T, reporter StatusReporter, args map[string]any) Result {
	t.Helper()
	tools := NewStatusTools(reporter)
	if len(tools) != 1 || tools[0].Name != "update_status" {
		t.Fatalf("NewStatusTools returned %+v, want one update_status tool", tools)
	}
	return tools[0].Handler(context.Background(), args)
}

func TestUpdateStatusSendsTheNoteAndConfirmsIt(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}

	res := updateStatus(t, reporter, map[string]any{"status": "waiting for CI on the second push"})
	if res.IsError {
		t.Fatalf("update_status reported an error: %s", res.Text)
	}
	if len(reporter.notes) != 1 || reporter.notes[0] != "waiting for CI on the second push" {
		t.Fatalf("reporter got %q, want the note as written", reporter.notes)
	}
	// The confirmation quotes it back, since what an agent most needs to
	// know is which of several phrases it is now showing.
	if !strings.Contains(res.Text, "waiting for CI on the second push") {
		t.Errorf("text = %q, want the note quoted back", res.Text)
	}
}

// A status is one line on a task row, so a note that arrives as several
// is flattened rather than refused: the agent said something usable, and
// the alternative is a turn spent on punctuation.
func TestUpdateStatusFlattensWhitespace(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}

	if res := updateStatus(t, reporter, map[string]any{
		"status": "  running the test suite\nfor pkg/orchestrator  ",
	}); res.IsError {
		t.Fatalf("update_status reported an error: %s", res.Text)
	}
	if got := reporter.notes[0]; got != "running the test suite for pkg/orchestrator" {
		t.Errorf("reporter got %q, want it flattened onto one line", got)
	}
}

// Both refusals happen before the reporter is reached: an empty note has
// nothing to show, and one too long for the row is worth shortening in
// the same turn rather than after a round trip. Each says what to send
// instead, since the reader is deciding its next call from the answer.
func TestUpdateStatusRefusesNotesItCannotShow(t *testing.T) {
	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing", nil, "required"},
		{"empty", map[string]any{"status": "   "}, "required"},
		{"too long", map[string]any{"status": strings.Repeat("x", MaxStatusLength+1)}, "characters"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reporter := &fakeReporter{report: StatusReport{Live: true}}
			res := updateStatus(t, reporter, tt.args)
			if !res.IsError {
				t.Fatalf("update_status accepted %v: %s", tt.args, res.Text)
			}
			if !strings.Contains(res.Text, tt.want) {
				t.Errorf("text = %q, want it to mention %q", res.Text, tt.want)
			}
			if len(reporter.notes) != 0 {
				t.Errorf("reporter got %q, want nothing sent", reporter.notes)
			}
		})
	}
}

// A note of exactly the limit is a note, not an overrun -- the boundary
// is worth pinning, since the daemon enforces the same number on the
// other side of the hop (ui.MaxActivityLength) and the two disagreeing
// would refuse a call the agent had already been told was fine.
func TestUpdateStatusAcceptsTheLimitExactly(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	if res := updateStatus(t, reporter, map[string]any{
		"status": strings.Repeat("x", MaxStatusLength),
	}); res.IsError {
		t.Fatalf("a note of exactly %d characters was refused: %s", MaxStatusLength, res.Text)
	}
}

// A run whose task is no longer live gets a plain answer rather than an
// error: nothing about its work is wrong, there is just nobody watching
// the row any more.
func TestUpdateStatusSaysWhenNothingIsListening(t *testing.T) {
	res := updateStatus(t, &fakeReporter{report: StatusReport{Live: false}},
		map[string]any{"status": "still going"})
	if res.IsError {
		t.Fatalf("a run grain has already finished is not a failed call: %s", res.Text)
	}
	if !strings.Contains(res.Text, "no longer") {
		t.Errorf("text = %q, want it to say the run is no longer in flight", res.Text)
	}
}

func TestUpdateStatusReportsTheDaemonsOwnError(t *testing.T) {
	res := updateStatus(t, &fakeReporter{err: errors.New("connection refused")},
		map[string]any{"status": "waiting for CI"})
	if !res.IsError || !strings.Contains(res.Text, "connection refused") {
		t.Fatalf("result = %+v, want the underlying error reported", res)
	}
}

// A nil reporter is what each agent framework's allowedTools holds when
// it only wants the tool's name, so the tool has to exist and refuse
// rather than panic -- and the refusal has to tell a run that nothing
// about its task depends on this, or it will spend turns retrying.
func TestUpdateStatusWithoutARouteBackRefusesPlainly(t *testing.T) {
	res := updateStatus(t, nil, map[string]any{"status": "waiting for CI"})
	if !res.IsError {
		t.Fatalf("a status with nowhere to go should be an error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Carry on") {
		t.Errorf("text = %q, want it to say the run can carry on regardless", res.Text)
	}
}
