package agent_test

// The half of the turn-budget contract neither package can hold alone:
// pkg/agent writes the sentence and pkg/model reads it, and nothing else
// fails when the two drift -- the cost lands on a metric that quietly
// counts zero runs ended by MaxAgentTurns forever.

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
)

func TestMaxTurnsErrorsReadBackAsTheTurnBudgetEnding(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"a cap grain configured", agent.MaxTurnsExceeded("claude", 100)},
		{"the CLI's own limit", agent.MaxTurnsExceededByCLI("claude")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The shape orchestrator.RunDispatch records: the
			// framework's own text, plus what the run got done first.
			detail := tc.err.Error() + "; the run made 42 tool call(s) [run_command x42] first"
			if got := model.EndingOf("failed", detail); got != model.EndingTurnsExhausted {
				t.Errorf("EndingOf(%q) = %q, want %q", detail, got, model.EndingTurnsExhausted)
			}
		})
	}
}

// The number is what an operator raises, so it has to stay in the text.
func TestMaxTurnsExceededNamesTheCapAndTheFramework(t *testing.T) {
	got := agent.MaxTurnsExceeded("codex", 7).Error()
	if want := "codex: exceeded max turns (7) without a final answer"; got != want {
		t.Errorf("MaxTurnsExceeded() = %q, want %q", got, want)
	}
}
