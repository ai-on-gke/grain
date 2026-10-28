package cli

// What `grain failures` actually reads like at a terminal. The rows and
// the window are pkg/model's and pkg/ui's; what is only decidable here
// is whether an operator scanning a week of them can see the thing the
// whole instrument exists for -- several failures saying the same
// unfamiliar thing, which is a refusal grain has no marker for.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ui"
)

func sampleFailures() ui.UnmatchedFailureReport {
	at := time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC)
	return ui.UnmatchedFailureReport{
		Since: at.Add(-7 * 24 * time.Hour), Until: at, WindowSeconds: 7 * 24 * 60 * 60,
		Total: 3, Suspected: 2,
		Infrastructure: ui.InfrastructureFailures{ExecTunnelSetups: 5, ExecTunnelToolRuns: 2, ExecTunnelToolCalls: 7},
		Failures: []ui.UnmatchedFailure{
			{RunID: "r3", TaskID: "t3", Framework: "claude", At: at.Add(-time.Hour),
				Text: "the model is presently at capacity; retry in a moment", Suspect: true},
			{RunID: "r2", TaskID: "t2", Framework: "claude", At: at.Add(-2 * time.Hour),
				Text: "claude: exceeded max turns (100) without a final answer"},
			{RunID: "r1", TaskID: "t1", Framework: "claude", At: at.Add(-3 * time.Hour),
				Text: "the model is presently at capacity; retry in a moment", Suspect: true},
		},
	}
}

func TestPrintFailures(t *testing.T) {
	out := captureStdout(t, func() {
		(&printer{}).failures(sampleFailures(), false)
	})
	for _, want := range []string{
		// The counts first: one is a Tuesday, a week of them is a
		// marker that has stopped matching.
		"3", "2",
		// The provider's own words, which are the entire content of the
		// report -- an operator cannot recognise a rewording they have
		// not been shown.
		"presently at capacity",
		// And the run and task, so the failure can be looked at whole.
		"r1", "t1", "claude",
		// The pointer at what to do about it.
		"pkg/agent/capacity.go",
		// The cluster's own failures, which are not in the list.
		"infrastructure: 5 runs never reached their agent because the cluster's exec tunnel failed; " +
			"2 more had 7 tool calls fail on it mid-run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
}

// -suspect is the narrow read: only the ones that look like a provider
// refusing a run, for the operator who already suspects a rewording.
func TestPrintFailuresSuspectOnly(t *testing.T) {
	out := captureStdout(t, func() {
		(&printer{}).failures(sampleFailures(), true)
	})
	if strings.Contains(out, "exceeded max turns") {
		t.Errorf("-suspect printed an ordinary failure:\n%s", out)
	}
	if !strings.Contains(out, "presently at capacity") {
		t.Errorf("-suspect dropped the refusal-shaped failures:\n%s", out)
	}
}

// A window with nothing in it says which of the two silences it is: no
// failures, or none that grain failed to recognise. They read very
// differently to somebody checking whether the instrument works.
func TestPrintFailuresSaysNothingIsNotBroken(t *testing.T) {
	out := captureStdout(t, func() {
		(&printer{}).failures(ui.UnmatchedFailureReport{
			WindowSeconds: 7 * 24 * 60 * 60, Failures: []ui.UnmatchedFailure{},
		}, false)
	})
	if !strings.Contains(out, "usage limit or a capacity refusal") {
		t.Errorf("an empty report does not say what empty means:\n%s", out)
	}
}

// -json prints the route's own body, so a reading piped into something
// else and one fetched with curl are the same object.
func TestPrintFailuresJSON(t *testing.T) {
	out := captureStdout(t, func() {
		(&printer{json: true}).failures(sampleFailures(), false)
	})
	var got ui.UnmatchedFailureReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Suspected != 2 || len(got.Failures) != 3 {
		t.Fatalf("got %+v, want three failures of which two are suspect", got)
	}
}
