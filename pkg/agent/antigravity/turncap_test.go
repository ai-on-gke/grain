package antigravity

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
)

func agentTurn(idx int, text string) string {
	return `{"event":"step_update","step_update":{"step_index":` + itoa(idx) +
		`,"state":"DONE","step_type":"agent_response","text_delta":"` + text + `"}}`
}

// TestTurnCapCountsCompletedAgentTurnsAcrossChunkBoundaries is the
// property this writer exists for: exec.Cmd hands it whatever it read
// from the pipe, which splits lines anywhere, and a cap that miscounted
// a split line would either fire early or never.
func TestTurnCapCountsCompletedAgentTurnsAcrossChunkBoundaries(t *testing.T) {
	var cancelled bool
	c := &turnCap{max: 2, cancel: func() { cancelled = true }}

	full := stream(agentTurn(0, "one"), agentTurn(1, "two"))
	// One byte at a time: the most hostile chunking there is.
	for i := 0; i < len(full); i++ {
		if _, err := c.Write([]byte(full[i : i+1])); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if !c.tripped() {
		t.Error("cap did not trip after two completed agent turns")
	}
	if !cancelled {
		t.Error("cap tripped without cancelling the run's context")
	}
}

// TestTurnCapIgnoresEverythingThatIsNotACompletedAgentTurn keeps the cap
// measuring the same thing a reader of the finished capture would: tool
// steps and still-running turns are not turns spent.
func TestTurnCapIgnoresEverythingThatIsNotACompletedAgentTurn(t *testing.T) {
	c := &turnCap{max: 1, cancel: func() {}}
	noise := stream(
		initLine,
		toolActive(0, "run_command", `{"command":"ls"}`),
		toolDone(0, "run_command", "a.txt"),
		`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"thinking"}}`,
		`{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"checkpoint"}}`,
		"not json at all",
	)
	if _, err := c.Write([]byte(noise)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if c.tripped() {
		t.Error("cap tripped on events that are not completed agent turns")
	}
}

// TestTurnCapIsInertWithoutAMax covers RunConfig.MaxTurns being left at
// the framework default of "no cap asked for" at this layer.
func TestTurnCapIsInertWithoutAMax(t *testing.T) {
	c := &turnCap{max: 0, cancel: func() { t.Error("cancelled with no cap set") }}
	if _, err := c.Write([]byte(stream(agentTurn(0, "one"), agentTurn(1, "two")))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if c.tripped() {
		t.Error("cap tripped with max 0")
	}
}

// cancelWatchingRunner is a fake agy that streams turns until the context
// it was given is cancelled -- what a real agy killed by procgroup does,
// modeled here so Run's own cap can be exercised end to end.
type cancelWatchingRunner struct {
	transcriptSoFar string
}

func (r *cancelWatchingRunner) Run(ctx context.Context, _ []string, _ string, _ []string, _ string, tee io.Writer) (string, error) {
	var out strings.Builder
	for i := 0; i < 50; i++ {
		if err := ctx.Err(); err != nil {
			r.transcriptSoFar = out.String()
			return out.String(), err
		}
		line := agentTurn(0, "turn") + "\n"
		out.WriteString(line)
		if tee != nil {
			io.WriteString(tee, line)
		}
	}
	r.transcriptSoFar = out.String()
	return out.String(), nil
}

// TestRunStopsTheSubprocessOnceTheTurnCapIsReached is the whole point of
// enforcing the cap on the live stream rather than on the finished
// capture: agy has no --max-turns, so a runaway run has to actually be
// stopped, not merely reported afterwards.
func TestRunStopsTheSubprocessOnceTheTurnCapIsReached(t *testing.T) {
	r := &cancelWatchingRunner{}
	f := newFramework(r, "/usr/local/bin/grain")

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), MaxTurns: 3,
	})
	if err == nil {
		t.Fatal("Run err = nil, want the cap reported as an error")
	}
	if !strings.Contains(err.Error(), "exceeded max turns (3)") {
		t.Errorf("Run err = %v, want it to name the cap it hit", err)
	}
	if result == nil {
		t.Fatal("Run result = nil; a capped run's own work must still come back")
	}
	// The run was stopped, not merely counted: far fewer than the 50
	// turns the fake would otherwise have produced reached the stream.
	if turns := strings.Count(r.transcriptSoFar, "agent_response"); turns >= 50 {
		t.Errorf("subprocess produced %d turns; the cap did not stop it", turns)
	}
}

// TestRunReturnsNilForARunThatNeverStarted is agent.Framework's contract
// at its sharpest, and the behavior e2e's close-while-live test depends
// on: a run cancelled before agy said anything must not come back as an
// empty-but-non-nil result, or a caller reads it as an agent that
// reached a sandbox.
func TestRunReturnsNilForARunThatNeverStarted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := newFramework(&cancelWatchingRunner{}, "/usr/local/bin/grain")
	result, err := f.Run(ctx, agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil for an already-cancelled context, want an error")
	}
	if result != nil {
		t.Fatalf("Run result = %+v, want nil for a run that never started", result)
	}
}

// TestRunReturnsWhatARunDidBeforeItFailed is the same contract's other
// side, and the failure agent.Framework's own doc comment records: a run
// that pushed a branch and then died must not be reported as nothing.
func TestRunReturnsWhatARunDidBeforeItFailed(t *testing.T) {
	// A capture with real work in it but no terminal result event --
	// what a killed agy leaves behind.
	r := &recordingRunner{stdout: stream(
		initLine,
		toolActive(0, "run_command", `{"command":"git push"}`),
		toolDone(0, "run_command", "branch pushed"),
	)}
	f := newFramework(r, "/usr/local/bin/grain")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil for a capture with no result event, want an error")
	}
	if result == nil {
		t.Fatal("Run result = nil; the push this run already made would be stranded")
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Text != "branch pushed" {
		t.Errorf("ToolCalls = %+v, want the push it managed before dying", result.ToolCalls)
	}
}

// TestRunLeavesTheTranscriptFileForTheCallerToClean documents the
// ownership Run's own doc comment claims: the mirror outlives the run.
func TestRunLeavesTheTranscriptFileForTheCallerToClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-1")
	f := newFramework(&recordingRunner{stdout: okStream()}, "/usr/local/bin/grain")
	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), TranscriptPath: path,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("transcript file gone after Run returned: %v", err)
	}
}

// The default is no cap, so this same fake runs all 50 of its turns
// through untouched -- where the former default of 20 would have
// cancelled it a third of the way in. What bounds a runaway run is
// orchestrator's Config.MaxRunRuntime, not a turn count guessed here.
func TestRunDoesNotCapTurnsByDefault(t *testing.T) {
	r := &cancelWatchingRunner{}
	f := newFramework(r, "/usr/local/bin/grain")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err != nil && strings.Contains(err.Error(), "exceeded max turns") {
		t.Fatalf("Run err = %v, want no turn cap with none configured", err)
	}
	if turns := strings.Count(r.transcriptSoFar, "agent_response"); turns != 50 {
		t.Errorf("subprocess produced %d turns, want all 50 -- something capped it", turns)
	}
}
