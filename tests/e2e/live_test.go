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

package e2e

// TestLiveIssueCompletesEndToEnd is agent/antigravity's own live gating,
// one layer up: it exercises the real agy binary against the same real
// store/gitproxy/git rig e2e_test.go's scripted tests do, so it skips
// harmlessly wherever neither GEMINI_API_KEY nor agy is present (the
// pull-request suite, a laptop), but proves -- wherever both are -- that
// an unscripted model, left to decide its own tool calls, actually
// completes an issue the way the scripted tests assume a model would:
//
//	GRAIN_LIVE_AGENT_TEST=1 GEMINI_API_KEY=... \
//	  go test ./tests/e2e/... -run TestLiveIssueCompletesEndToEnd -v
//
// .grain/workflows/live-agent.yml runs exactly that, nightly, with a
// credential of its own -- so this is a check something remembers rather
// than only a maintainer. It is scheduled rather than pushed for reasons
// worth reading before moving it: see that file's header and README's
// "Proving a live run actually gets the tools".
//
// # Why this test is the only one that can see the config file's name
//
// agy has no --mcp-config: it reads its MCP servers out of the HOME it
// was started with, and agent/antigravity writes each run's own server
// into ~/.gemini/config/mcp_config.json there. That path is a fact about
// agy's on-disk layout, not about grain, and nothing else in this
// repository can check it -- every other test that covers the wiring
// drives a scripted runner or a stub CLI, which reads whatever file the
// test itself was told to read. That is exactly how the wrong name
// (~/.gemini/settings.json, where Gemini CLI kept the same map) survived:
// the whole suite stayed green while a real agy loaded no MCP servers at
// all. See README's "Proving a live run actually gets the tools".
//
// So the roster is asserted here, up front and on its own, rather than
// left to be inferred from the run's outcome. An agy that loaded no tools
// still starts, still answers, and still fails this test -- but it would
// fail it at "the branch does not exist", which reads like a model that
// declined to push rather than like a run that was never given a way to.
//
// # GRAIN_LIVE_AGENT_TEST
//
// The two gates below skip, so this file costs the pull-request suite
// nothing (no credential is available there, deliberately -- see
// .github/workflows/tests.yml). A skip is also indistinguishable from a
// pass in `go test`'s summary line, which is a bad property for the one
// test standing between this package and a config file agy never opens.
// Setting GRAIN_LIVE_AGENT_TEST turns both gates into failures, so a run
// that could not do the one thing this test exists for says so -- which
// is why the nightly job sets it, and why a maintainer running this by
// hand should too.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestLiveIssueCompletesEndToEnd(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		skipUnlessLiveRequired(t, "GEMINI_API_KEY is not set")
	}
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		skipUnlessLiveRequired(t, "no agy binary on $PATH")
	}

	w := newWorld(t)
	w.newRepo("acme", "live")

	clock := baseTime
	fileIssue(w, "iss-live", human("tester"), model.RepoRef{Owner: "acme", Name: "live"})

	dispatches, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, clock)
	if err != nil || len(dispatches) != 1 {
		t.Fatalf("Cycle: %v, %+v", err, dispatches)
	}
	d := dispatches[0]
	branch := model.BranchName("iss-live")
	remote := w.remote("acme", "live")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	// agy reaches the sandbox by forking this repo's own "mcpserver"
	// subcommand, so a live run needs a built grain binary as well as a
	// credential -- see agent/antigravity's own doc comment.
	fw := antigravity.New(agyPath, buildGrainBinary(t), antigravity.WithAPIKey(apiKey))

	prompt := "Your sandbox workspace has git already configured with credentials for one remote. " +
		"Using your run_command tool, do exactly the following as ordinary shell/git commands:\n" +
		"1. Clone " + remote + " into a directory named work.\n" +
		"2. Inside work, create a new branch named exactly " + branch + " (git checkout -b).\n" +
		"3. Append the exact line 'the agent was here' to a file named NOTES.md in that directory " +
		"(creating it if it does not exist).\n" +
		"4. Commit that change with any commit message.\n" +
		"5. Push the " + branch + " branch to the origin remote (not main).\n" +
		"Reply with a short confirmation once the push has succeeded."

	// The raw stream-json is kept because agy's opening "init" event --
	// the tool roster it actually loaded -- appears nowhere in the
	// agent.Result the framework hands back. Framework.Run mirrors the
	// stream here line by line as it arrives, so this file is readable
	// even for a run that was cancelled or failed part-way.
	transcriptPath := filepath.Join(t.TempDir(), "agy-stream.jsonl")

	result, runErr := fw.Run(ctx, agent.RunConfig{
		Prompt:         prompt,
		SandboxRoot:    w.prepareSandbox(dispatches[0]),
		MaxTurns:       15,
		TranscriptPath: transcriptPath,
	})
	// Asserted ahead of runErr on purpose: a run given no tools fails
	// somewhere further down every time, and every one of those failures
	// describes a model that would not do as it was asked rather than the
	// config file it was never handed.
	assertGrainToolsAdvertised(t, transcriptPath)
	if runErr != nil {
		t.Fatalf("agent run failed: %v", runErr)
	}
	for _, c := range result.ToolCalls {
		t.Logf("tool call: %s(%v) -> error=%v text=%q", c.Name, c.Arguments, c.IsError, c.Text)
	}
	// The roster proves agy was told about grain's tools; this proves it
	// reached the sandbox through one. They are separate failures because
	// they have separate causes: a published roster nothing calls is a
	// model that ignored its tools, and no roster at all is this
	// package's own wiring.
	assertSandboxToolRan(t, result)

	if err := w.store.FinishRun(w.ctx, d.RunID, clock.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}

	if !w.branchExists("acme", "live", branch) {
		t.Fatalf("expected branch %s to exist in acme/live after a live agent run; final answer: %q", branch, result.FinalText)
	}
	bare := filepath.Join(w.upstreamDir, "acme", "live.git")
	notes := runOutput(t, w.upstreamDir, "git", "--git-dir", bare, "show", branch+":NOTES.md")
	if !strings.Contains(notes, "the agent was here") {
		t.Fatalf("NOTES.md on %s = %q, want it to contain the requested line", branch, notes)
	}

	completedAt := clock.Add(2 * time.Minute)
	observeCompletion(w, "iss-live", completedAt)
	assertState(w, "iss-live", model.StateCompleted, false)
}

// liveAgentTestEnv is the opt-in that makes this file's gates loud. Set
// it when running the live test by hand: an unset GEMINI_API_KEY or a
// missing agy then fails the test instead of skipping it, so a `go test`
// that could not do the one thing it exists to do says so rather than
// printing "ok".
const liveAgentTestEnv = "GRAIN_LIVE_AGENT_TEST"

func skipUnlessLiveRequired(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv(liveAgentTestEnv) != "" {
		t.Fatalf("%s is set, but %s: this run proved nothing about a live agy", liveAgentTestEnv, reason)
	}
	t.Skipf("%s; skipping the live agent integration test (set %s=1 to make this a failure)", reason, liveAgentTestEnv)
}

// assertGrainToolsAdvertised reads agy's own opening "init" event out of
// a raw stream-json capture and fails unless the roster it reports leaves
// the run some route to grain's tools.
//
// It used to demand grain's tools by name in that roster, on the theory
// that the roster is the run's whole tool vocabulary. Run against the
// real binary that turned out to be false: agy's init event lists agy's
// own native tools and never an MCP tool under any spelling, eagerly
// registered or not (agy 1.1.25 advertises 57 of them and no grain tool).
// The old assertion would therefore have failed every live run, and the
// reason would have read as "grain's MCP config is written where agy
// cannot find it" -- which was true once (task 146) and is exactly the
// kind of wrong diagnosis a test should not hand anybody.
//
// What the roster does say is whether there is a bridge at all:
// call_mcp_tool, agy's dispatcher for lazily loaded MCP tools, or an
// eagerly registered tool under mcp.AgyQualifiedToolName's spelling. A
// roster with neither is a run that cannot reach its sandbox. The
// stronger claim -- that a tool call really landed -- is
// assertSandboxToolRan's, below, and it is the one that matters.
func assertGrainToolsAdvertised(t *testing.T, transcriptPath string) {
	t.Helper()

	capture, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("reading agy's stream-json capture: %v", err)
	}
	tools, sawInit := advertisedTools(capture)
	if !sawInit {
		t.Fatalf("agy's stream carried no init event, so nothing here can say which tools the run was given; capture:\n%s", capture)
	}
	t.Logf("agy's init event advertised %d tool(s): %v", len(tools), tools)

	eagerPrefix := mcp.AgyQualifiedToolName("")
	for _, name := range tools {
		if name == "call_mcp_tool" || strings.HasPrefix(name, eagerPrefix) {
			return
		}
	}
	t.Fatalf("agy's init event advertised neither call_mcp_tool nor any %s* tool (roster: %v).\n"+
		"This run had no way to reach grain's MCP server at all: check that agent/antigravity's "+
		"mcpConfigRelPath is still where this agy build keeps its servers (`agy mcp add` writes "+
		"it, `agy mcp list` reads it back).", eagerPrefix, tools)
}

// advertisedTools pulls the tool roster off the first "init" event in a
// stream-json capture. Only the two fields this assertion reads are
// modeled, and an unparseable line is skipped rather than treated as
// fatal, for the same reason agent/antigravity's own parser tolerates one
// -- including the truncated last line a capture read while agy is still
// writing can end on.
func advertisedTools(capture []byte) (tools []string, sawInit bool) {
	for _, line := range strings.Split(string(capture), "\n") {
		var ev struct {
			Event string `json:"event"`
			Init  *struct {
				Tools []string `json:"tools"`
			} `json:"init"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &ev); err != nil {
			continue
		}
		if ev.Event == "init" && ev.Init != nil {
			return ev.Init.Tools, true
		}
	}
	return nil, false
}

// The roster assertion above only ever runs on a live agy, so nothing
// would otherwise notice a typo in the one thing it reads -- and a
// broken reader fails the live run for a reason that has nothing to do
// with what the live run was there to check. This covers the reader
// itself against agy's event shape, with the same fixtures
// agent/antigravity's own parser tests use.
func TestAdvertisedToolsReadsAgysInitEvent(t *testing.T) {
	t.Parallel()
	// agy's own roster shape: its native tools, with call_mcp_tool among
	// them as the only thing in it that leads to grain.
	const initLine = `{"event":"init","init":{"cwd":"/w","tools":["run_command","view_file","call_mcp_tool"],"permission_mode":"bypass"}}`
	const resultLine = `{"event":"result","result":{"status":"SUCCESS","response":"done"}}`

	tools, sawInit := advertisedTools([]byte(initLine + "\n" + resultLine + "\n"))
	if !sawInit {
		t.Fatal("advertisedTools found no init event in a capture that opens with one")
	}
	if !slices.Contains(tools, "call_mcp_tool") {
		t.Errorf("advertisedTools = %v, want agy's MCP dispatcher among them", tools)
	}

	// A run agy never loaded the MCP config for: the event is there, the
	// roster is empty. This is the shape the live assertion has to catch,
	// and "no init event" is not it.
	tools, sawInit = advertisedTools([]byte(`{"event":"init","init":{"tools":[]}}` + "\n"))
	if !sawInit || len(tools) != 0 {
		t.Errorf("advertisedTools of an empty roster = %v, %v; want none, true", tools, sawInit)
	}

	// A capture read while agy is still writing can end mid-line, and
	// one that never started can hold nothing at all.
	if _, sawInit := advertisedTools([]byte(`{"event":"ini`)); sawInit {
		t.Error("advertisedTools read an init event out of a truncated line")
	}
	if _, sawInit := advertisedTools(nil); sawInit {
		t.Error("advertisedTools read an init event out of an empty capture")
	}
}

// assertSandboxToolRan fails unless the run actually called one of
// grain's tools and got an answer back -- the other half of what a live
// run proves and the roster alone does not, since a roster is a promise
// about tools that exist rather than evidence that calling one reaches
// this run's sandbox.
//
// Names are compared bare ("run_command", not the mcp__-prefixed form
// agy reports): every Framework strips the prefix before recording a
// call, which is what the rest of grain matches on (mcp.BareToolName).
func assertSandboxToolRan(t *testing.T, result *agent.Result) {
	t.Helper()
	for _, c := range result.ToolCalls {
		if c.Name == "run_command" && !c.IsError {
			return
		}
	}
	t.Fatalf("no successful run_command call among the run's %d tool call(s): agy was handed grain's roster and never reached the sandbox through it",
		len(result.ToolCalls))
}
