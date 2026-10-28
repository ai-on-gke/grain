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

package codex

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

func agentTurn(id, text string) string {
	return `{"type":"item.completed","item":{"id":"` + id + `","item_type":"agent_message","text":"` + text + `"}}`
}

// TestTurnCapCountsCompletedTurnsAcrossChunkBoundaries is the property
// this writer exists for: exec.Cmd hands it whatever it read from the
// pipe, which splits lines anywhere, and a cap that miscounted a split
// line would either fire early or never.
func TestTurnCapCountsCompletedTurnsAcrossChunkBoundaries(t *testing.T) {
	var cancelled bool
	c := &turnCap{max: 2, cancel: func() { cancelled = true }}

	full := agentTurn("i1", "one") + "\n" + agentTurn("i2", "two") + "\n"
	// One byte at a time: the most hostile chunking there is.
	for i := 0; i < len(full); i++ {
		if _, err := c.Write([]byte(full[i : i+1])); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if !c.tripped() {
		t.Error("cap did not trip after two completed turns")
	}
	if !cancelled {
		t.Error("cap tripped without cancelling the run's context")
	}
}

// The cap counts the same thing a reader of the finished capture counts
// -- an assistant message, completed -- and nothing else: a tool call,
// codex's own shell, a reasoning item and an unparseable line are not
// turns spent.
func TestTurnCapIgnoresEverythingThatIsNotACompletedTurn(t *testing.T) {
	c := &turnCap{max: 1, cancel: func() {}}
	noise := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"item.started","item":{"id":"i1","item_type":"agent_message","text":"partial"}}`,
		`{"type":"item.completed","item":{"id":"i2","item_type":"reasoning","text":"thinking"}}`,
		`{"type":"item.completed","item":{"id":"i3","item_type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"run_command","status":"completed","result":"ok"}}`,
		`{"type":"item.completed","item":{"id":"i4","item_type":"command_execution","command":"ls"}}`,
		"not json at all",
		"",
	}, "\n")
	if _, err := c.Write([]byte(noise)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if c.tripped() {
		t.Error("cap tripped on events that are not completed turns")
	}
}

// The older vocabulary's own completed turn counts too: a deployment
// running a codex that speaks it must get the cap it configured, not no
// cap at all.
func TestTurnCapCountsTheOlderVocabularysTurns(t *testing.T) {
	c := &turnCap{max: 1, cancel: func() {}}
	if _, err := c.Write([]byte(`{"id":"0","msg":{"type":"agent_message","message":"one"}}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !c.tripped() {
		t.Error("cap did not trip on a completed turn in the older vocabulary")
	}
}

// A zero cap is no cap at all -- defaultMaxTurns' own reading, and the
// default every deployment runs with.
func TestTurnCapWithNoMaxNeverTrips(t *testing.T) {
	c := &turnCap{max: 0, cancel: func() { t.Error("a capless run was cancelled") }}
	for i := 0; i < 100; i++ {
		if _, err := c.Write([]byte(agentTurn("i", "turn") + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if c.tripped() {
		t.Error("cap tripped with max = 0")
	}
}

// cancelWatchingRunner is a fake codex that streams turns until the
// context it was given is cancelled -- what a real codex killed by
// procgroup does, modeled here so Run's own cap can be exercised end to
// end.
type cancelWatchingRunner struct {
	turnsProduced int
}

func (r *cancelWatchingRunner) Run(ctx context.Context, _ []string, _ string, _ []string, _ string, tee io.Writer) (string, error) {
	var out strings.Builder
	for i := 0; i < 50; i++ {
		if err := ctx.Err(); err != nil {
			r.turnsProduced = i
			return out.String(), err
		}
		line := agentTurn("i", "turn") + "\n"
		out.WriteString(line)
		if tee != nil {
			io.WriteString(tee, line)
		}
	}
	r.turnsProduced = 50
	return out.String(), nil
}

// TestRunStopsTheSubprocessOnceTheTurnCapIsReached is the whole point of
// enforcing the cap on the live stream rather than on the finished
// capture: codex has no --max-turns, so a runaway run has to actually be
// stopped, not merely reported afterwards.
func TestRunStopsTheSubprocessOnceTheTurnCapIsReached(t *testing.T) {
	r := &cancelWatchingRunner{}
	f := newTestFramework(r, "/usr/local/bin/grain")

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
	if r.turnsProduced >= 50 {
		t.Errorf("subprocess produced %d turns; the cap did not stop it", r.turnsProduced)
	}
}
