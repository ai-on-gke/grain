package agent

import (
	"errors"
	"strings"
	"testing"
)

// The case the whole function exists for: a CLI that said why it failed
// and then exited non-zero. Neither half may be dropped -- the sentence
// is what an operator acts on, and the exit status is what tells them
// the process ended rather than the run.
func TestRunFailureNamesBothHalves(t *testing.T) {
	reported := errors.New("antigravity: run ended in status ERROR: There was a network issue connecting to the server, please try again.")
	exitErr := errors.New("exit status 1 (stderr: )")

	err := RunFailure("antigravity", "agy", reported, exitErr)
	for _, want := range []string{"There was a network issue", "agy also exited non-zero", "exit status 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("RunFailure = %q, want it to contain %q", err, want)
		}
	}
	// The stream's account leads: it is the half that says why, and a
	// log line is read from the left.
	if !strings.HasPrefix(err.Error(), "antigravity: run ended in status ERROR:") {
		t.Errorf("RunFailure = %q, want the stream's own account first", err)
	}
	// Both stay reachable by identity, so that a wrapped
	// context.Canceled or exec.ExitError still answers errors.Is.
	if !errors.Is(err, reported) || !errors.Is(err, exitErr) {
		t.Errorf("RunFailure = %q, want errors.Is to answer for both halves", err)
	}
}

// A subprocess that died before saying anything: a missing binary, a
// signal, a cancelled context. There is no sentence to lead with, so the
// caller's own wrapping is all there is.
func TestRunFailureWithNothingReported(t *testing.T) {
	exitErr := errors.New("no such file or directory")
	err := RunFailure("antigravity", "agy", nil, exitErr)
	if got, want := err.Error(), "antigravity: running agy: no such file or directory"; got != want {
		t.Errorf("RunFailure = %q, want %q", got, want)
	}
	if !errors.Is(err, exitErr) {
		t.Errorf("RunFailure = %q, want the subprocess's own error wrapped in it", err)
	}
}

// The mirror image: a CLI that reported a failure and then exited 0
// anyway. Its own words are the whole story, unadorned.
func TestRunFailureWithNoExitStatus(t *testing.T) {
	reported := errors.New("codex: run ended in error: stream disconnected before completion")
	if err := RunFailure("codex", "codex", reported, nil); err != reported {
		t.Errorf("RunFailure = %v, want the reported failure unchanged", err)
	}
}

// Neither half is not a failure at all, and must not be reported as one:
// a caller that always calls this would otherwise turn every successful
// run into an error with nothing in it.
func TestRunFailureWithNeitherHalfIsNil(t *testing.T) {
	if err := RunFailure("claude", "claude", nil, nil); err != nil {
		t.Errorf("RunFailure = %v, want nil", err)
	}
}
