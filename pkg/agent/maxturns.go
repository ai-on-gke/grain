package agent

// The one sentence a Framework uses when a run ran out of turns.
//
// Every Framework here already ended that way in words of its own, and
// all three happened to agree; this is that agreement written down, for
// the reason mcp.BareToolName exists. What reads the sentence afterwards
// is not a human: orchestrator records it in task_run.detail, and
// model.EndingOf reads it back to tell a run that exhausted MaxAgentTurns
// -- which a deployment fixes by raising the cap -- from every other way
// a run can fail, which it fixes some other way entirely. A framework
// that worded its own version differently would count as an ordinary
// failure forever, silently, and nothing else in the repo would notice.
//
// So the phrase lives here, in one place, and maxturns_test.go asserts
// that what these build really does classify as
// model.EndingTurnsExhausted.

import "fmt"

// MaxTurnsExceeded is what a Framework returns when a run used every turn
// RunConfig.MaxTurns allowed it without producing a final answer.
//
// The number is named because the operator reading the failed run needs
// to know which one to raise, and framework is named because "which CLI
// stopped this" is the next question after that.
func MaxTurnsExceeded(framework string, maxTurns int) error {
	return fmt.Errorf("%s: exceeded max turns (%d) without a final answer", framework, maxTurns)
}

// MaxTurnsExceededByCLI is the same ending with no number to name: grain
// configured no cap at all, and the CLI stopped at one of its own. Saying
// so is what stops an operator going to look for a grain setting to raise
// that is already unlimited.
func MaxTurnsExceededByCLI(framework string) error {
	return fmt.Errorf("%s: exceeded max turns: the CLI stopped the run at its own turn limit, "+
		"which grain never set, without a final answer", framework)
}
