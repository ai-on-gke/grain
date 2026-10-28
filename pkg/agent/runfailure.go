package agent

// The one sentence a Framework uses when the CLI it drove failed.
//
// A real CLI failure usually arrives twice: the run's own stream says
// what went wrong ("There was a network issue connecting to the server,
// please try again."), and then the process exits non-zero with nothing
// on stderr. Each Framework here parsed both halves and then reported
// only one of them -- whichever its own switch happened to reach first --
// so the half an operator could act on was dropped exactly when both
// were present, which is the ordinary shape of a failed run. What was
// left in the daemon's log was "running agy: exit status 1 (stderr: )",
// and since the stream-json capture is removed when the run finishes
// (orchestrator's own cleanup), nothing else kept the sentence either:
// naming the cause cost a live reproduction every time.
//
// So the joining lives here, in one place, for the reason
// MaxTurnsExceeded does: all three CLIs fail in the same two halves, and
// what reads the result afterwards is not only a human -- orchestrator
// records it in task_run.detail and model.EndingOf reads it back.

import "fmt"

// RunFailure is what a Framework returns for a run its CLI could not
// finish: the account the run's own output gave of the failure, the exit
// status the subprocess left behind, or -- the usual case -- both.
//
// reported is the failure the stream described, already worded by the
// package that parsed it ("antigravity: run ended in status ERROR: ..."),
// and is nil for a subprocess that died before saying anything: a missing
// binary, a signal, a cancelled context. exitErr is what the subprocess
// itself returned, and is nil for a CLI that reported a failure and then
// exited 0 anyway.
//
// The stream's account leads because it is the half that says why, and
// the exit status follows in parentheses rather than being dropped: it is
// how an operator tells a run the CLI ended deliberately from one that
// died under it, and it keeps errors.Is answering for a wrapped
// context.Canceled or exec.ExitError. framework and binary name the
// caller and the command it ran ("antigravity" and "agy"; "claude" and
// "claude"), and are used only in the sentence.
func RunFailure(framework, binary string, reported, exitErr error) error {
	switch {
	case reported == nil && exitErr == nil:
		return nil
	case reported == nil:
		return fmt.Errorf("%s: running %s: %w", framework, binary, exitErr)
	case exitErr == nil:
		return reported
	}
	return fmt.Errorf("%w (%s also exited non-zero: %w)", reported, binary, exitErr)
}
