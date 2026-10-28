// This file is docs/test-plans/2026-10-04.md §7 steps 1 and 2 driven
// through real cycles: dispatch.Cycle deciding what runs, RunDispatch
// recording each ending, the shared orchestrator.Pause deciding what may
// run next, against a real store and a real gitproxy-fronted world. The
// only stand-in is the model: a scripted framework that fails the way a
// provider refusal does (step 1), or the real claude runner exec'ing a
// fake `claude` that replays a captured stream-json transcript (step 2).
package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// burstClock is the one clock a walkthrough's cycles, RunDispatch's own
// timestamps and the pause all read, so "each run failed 10s after its
// agent started" and "15 minutes later" are both exact.
type burstClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *burstClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *burstClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

// refusingFramework is the scripted model: every run takes ranFor on
// the shared clock and then fails with the next of texts (the last one
// repeating). With toolCall set, the run reports one tool call first --
// an agent that did some work before failing.
type refusingFramework struct {
	clock    *burstClock
	ranFor   time.Duration
	toolCall bool

	mu    sync.Mutex
	texts []string
	runs  int
}

func (f *refusingFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.mu.Lock()
	text := f.texts[min(f.runs, len(f.texts)-1)]
	f.runs++
	f.mu.Unlock()
	f.clock.Advance(f.ranFor)
	result := &agent.Result{}
	if f.toolCall {
		result.ToolCalls = []agent.ToolCall{{Name: "run_command", Arguments: map[string]any{"command": "ls"}}}
	}
	return result, errors.New(text)
}

func (f *refusingFramework) Runs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

// captureLog sends the standard logger to a buffer for the rest of the
// test. Only for a test that is not t.Parallel: the logger is global.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// burstWorld is a world, a repo, a pause and a clock, with tasks filed
// on it and the deps that run them.
func burstWorld(t *testing.T, fw agent.Framework, clock *burstClock, workers int) (*world, orchestrator.Deps, model.RepoRef) {
	t.Helper()
	w := newWorld(t)
	const owner, repoName = "acme", "refusals"
	w.newRepo(owner, repoName)
	deps := orchestrator.Deps{
		Store: w.store, Client: simClient(w, owner, repoName), Sandboxes: worldSandboxes{w}, MaxWorkers: workers,
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) { return fw, nil },
		Config:    orchestrator.Config{Pause: &orchestrator.Pause{}, Now: clock.Now},
	}
	return w, deps, model.RepoRef{Owner: owner, Name: repoName}
}

// simClient is githubsim in front of the world's bare repo: GitHub,
// the one other external boundary these runs reach (a failed run asks
// it whether a branch was pushed before the failure).
func simClient(w *world, owner, name string) github.Client {
	bare := filepath.Join(w.upstreamDir, owner, name+".git")
	return github.NewClient(githubsim.New(owner, name, bare, "main"), nil)
}

// cycle runs one tick. A run that failed is reported as RunCycle's
// error, which is every run in these walkthroughs, so it is logged
// rather than fatal; what each test asserts is what the store and the
// pause say afterwards.
func cycle(t *testing.T, w *world, deps orchestrator.Deps, now time.Time) {
	t.Helper()
	if err := orchestrator.RunCycle(w.ctx, deps, now); err != nil {
		t.Logf("RunCycle at %s: %v", now.Format(time.TimeOnly), err)
	}
}

// runsOf is every run the store holds for the given tasks, by task.
func runsOf(t *testing.T, w *world, ids ...string) map[string][]model.Run {
	t.Helper()
	out := map[string][]model.Run{}
	for _, id := range ids {
		runs, err := w.store.Runs(w.ctx, id)
		if err != nil {
			t.Fatalf("Runs(%s): %v", id, err)
		}
		out[id] = runs
	}
	return out
}

// Step 1: three runs on one framework, each failing 10s after it starts
// with no tool call and the words "Refused: policy X", inside five
// minutes. The third pauses the framework for 15 minutes with the
// condition=unrecognised_refusal_burst line; all three are recorded as
// paused, so no task is charged an attempt; the task still queued stays
// queued while the pause lasts; and dispatch resumes once it has passed.
func TestWalkthroughRefusalBurstPausesTheFrameworkAndQueuedTasksWait(t *testing.T) {
	logs := captureLog(t)
	defer func() {
		t.Logf("burst log:\n%s", grepLines(logs.String(), "unrecognised_refusal_burst", "dispatch paused"))
	}()
	clock := &burstClock{now: baseTime}
	fw := &refusingFramework{clock: clock, ranFor: 10 * time.Second, texts: []string{"Refused: policy X"}}
	// Three workers and four tasks: one cycle runs three of them at
	// once, the way the 2026-10-02 outage met five, and leaves the
	// fourth queued behind them.
	w, deps, repo := burstWorld(t, fw, clock, 3)
	pause := deps.Config.Pause

	ids := []string{"rb-1", "rb-2", "rb-3", "rb-4"}
	for _, id := range ids {
		fileIssue(w, id, human("erin"), repo)
	}
	cycle(t, w, deps, clock.Now())
	if got := fw.Runs(); got != 3 {
		t.Fatalf("the first cycle ran the framework %d times, want 3", got)
	}
	// Each run advanced the shared clock by its 10s; the pause began
	// when the last of them finished.
	pausedAt := clock.Now()

	paused := pause.Pauses(clock.Now())
	if len(paused) != 1 {
		t.Fatalf("paused %v, want the one credential the burst came from", paused)
	}
	until, _ := pause.Until()
	if want := pausedAt.Add(15 * time.Minute); !until.Equal(want) {
		t.Errorf("paused until %s, want %s (15 minutes after the third failure)", until, want)
	}
	if !strings.Contains(logs.String(), "level=error condition=unrecognised_refusal_burst") ||
		!strings.Contains(logs.String(), `message="Refused: policy X"`) {
		t.Errorf("no unrecognised_refusal_burst line naming the words; log:\n%s", logs.String())
	}

	// Every run of the burst is paused rather than failed, and every
	// task in it is back in the queue with no failure on its streak.
	ran := map[string]bool{}
	for id, runs := range runsOf(t, w, ids...) {
		for _, r := range runs {
			ran[id] = true
			if r.Outcome != model.PausedOutcome {
				t.Errorf("%s run %s outcome = %q, want %q", id, r.ID, r.Outcome, model.PausedOutcome)
			}
		}
		streak, err := w.store.FailureStreak(w.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if streak != nil && streak.Count != 0 {
			t.Errorf("%s failure streak = %d, want 0: a burst charges no task", id, streak.Count)
		}
	}
	if len(ran) != 3 || ran["rb-4"] {
		t.Fatalf("tasks that ran: %v, want three, and not rb-4", ran)
	}

	// While the pause lasts, cycles dispatch nothing: every task --
	// the one that never ran and the three the burst sent back -- waits.
	for _, at := range []time.Duration{time.Minute, 7 * time.Minute, 13 * time.Minute} {
		cycle(t, w, deps, pausedAt.Add(at))
		if got := fw.Runs(); got != 3 {
			t.Fatalf("%s into the pause the framework has run %d times, want still 3", at, got)
		}
	}
	for _, id := range ids {
		assertState(w, id, model.StateQueued, false)
	}

	// Fifteen minutes on, the framework is free again and the next
	// cycle dispatches.
	clock.Advance(15*time.Minute - clock.Now().Sub(pausedAt))
	if paused := pause.Pauses(clock.Now()); len(paused) != 0 {
		t.Fatalf("still paused %v at %s, past the 15-minute window", paused, clock.Now())
	}
	cycle(t, w, deps, clock.Now())
	if got := fw.Runs(); got <= 3 {
		t.Fatalf("after the pause the framework has run %d times, want dispatch to have resumed", got)
	}
}

// Step 1, the two near-misses: three fast failures with three different
// texts, and three identical fast failures from runs that each made a
// tool call. Neither is a burst, so nothing pauses and the runs are
// ordinary failures.
func TestWalkthroughDifferentTextsOrAToolCallDoNotPause(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		fw   func(*burstClock) *refusingFramework
	}{
		{"three different texts", func(c *burstClock) *refusingFramework {
			return &refusingFramework{clock: c, ranFor: 10 * time.Second,
				texts: []string{"Refused: policy X", "Refused: policy Y", "Refused: policy Z"}}
		}},
		{"identical texts after a tool call", func(c *burstClock) *refusingFramework {
			return &refusingFramework{clock: c, ranFor: 10 * time.Second, toolCall: true,
				texts: []string{"Refused: policy X"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := &burstClock{now: baseTime}
			fw := tc.fw(clock)
			w, deps, repo := burstWorld(t, fw, clock, 1)
			ids := []string{"nb-1", "nb-2", "nb-3"}
			for _, id := range ids {
				fileIssue(w, id, human("erin"), repo)
			}
			for i := 0; i < 3; i++ {
				cycle(t, w, deps, clock.Now())
				clock.Advance(time.Minute)
			}
			if got := fw.Runs(); got != 3 {
				t.Fatalf("framework ran %d times, want 3", got)
			}
			if paused := deps.Config.Pause.Pauses(clock.Now()); len(paused) != 0 {
				t.Fatalf("paused %v, want nothing", paused)
			}
			// One worker retries the first task: three runs, one task.
			total := 0
			for id, runs := range runsOf(t, w, ids...) {
				for _, r := range runs {
					total++
					if r.Outcome != "failed" {
						t.Errorf("%s run %s outcome = %q, want failed", id, r.ID, r.Outcome)
					}
				}
			}
			if total != 3 {
				t.Errorf("%d runs recorded, want 3", total)
			}
		})
	}
}

// sessionLimitTranscript is a claude -p --output-format stream-json
// stream as the CLI wrote it during the 2026-10-02 outage: the init
// event, then a terminal result that is an error with subtype
// "success" and the worded refusal, then exit 1.
const sessionLimitTranscript = `{"type":"system","subtype":"init","cwd":"/home/debian","session_id":"5f0c8a2e-1b7d-4c55-9a43-0d2f6e9b1c01","tools":[],"mcp_servers":[{"name":"grain-sandbox","status":"connected"}],"model":"claude-opus-5-5","permissionMode":"default","apiKeySource":"none"}
{"type":"result","subtype":"success","is_error":true,"duration_ms":412,"duration_api_ms":0,"num_turns":1,"result":"You've hit your session limit · resets 5:30pm (UTC)","session_id":"5f0c8a2e-1b7d-4c55-9a43-0d2f6e9b1c01","total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0}}
`

// Step 2: the real claude runner, exec'ing a fake `claude` that
// replays sessionLimitTranscript, inside a real cycle. The run ends in
// an agent.UsageLimitError whose reset is the next 17:30 UTC, the claude
// framework is paused until then, and the run is recorded as paused --
// not a failure on the task's streak.
func TestWalkthroughReplayedClaudeSessionLimitPausesUntilItsReset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	transcript := filepath.Join(dir, "transcript.jsonl")
	mustWriteFile(t, transcript, sessionLimitTranscript)
	fakeClaude := filepath.Join(dir, "claude")
	mustWriteFile(t, fakeClaude, fmt.Sprintf("#!/bin/sh\ncat %q\nexit 1\n", transcript))
	if err := os.Chmod(fakeClaude, 0o755); err != nil {
		t.Fatal(err)
	}
	// grainBinaryPath only names the mcpserver the --mcp-config file
	// points at; the fake claude never starts it.
	fw := claude.New(fakeClaude, "/bin/true")

	// The reset the parser reads is measured from the wall clock
	// (usageLimitNow), so this test is too.
	start := time.Now().UTC()
	limit := runClaude(t, fw)
	end := time.Now().UTC()
	if limit == nil {
		t.Fatal("the replayed session-limit result was not an agent.UsageLimitError")
	}
	if limit.Framework != "claude" || !strings.Contains(limit.Message, "hit your session limit") {
		t.Errorf("limit = %+v, want claude's, carrying the CLI's words", limit)
	}
	if want, alt := next1730(start), next1730(end); !limit.ResetAt.Equal(want) && !limit.ResetAt.Equal(alt) {
		t.Errorf("ResetAt = %s, want the next 17:30 UTC (%s)", limit.ResetAt, want)
	}

	// The same transcript through a real cycle.
	w := newWorld(t)
	w.newRepo("acme", "limits")
	repo := model.RepoRef{Owner: "acme", Name: "limits"}
	pause := &orchestrator.Pause{}
	now := time.Now().UTC()
	deps := orchestrator.Deps{
		Store: w.store, Client: simClient(w, "acme", "limits"), Sandboxes: worldSandboxes{w}, MaxWorkers: 1,
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) { return fw, nil },
		Config:    orchestrator.Config{Pause: pause, Now: func() time.Time { return now }, AgentFramework: "claude"},
	}
	fileIssue(w, "sl-1", human("erin"), repo)
	cycle(t, w, deps, now)

	paused := pause.Pauses(now)
	until, _ := pause.Until()
	if len(paused) != 1 || paused[0].Framework != "claude" {
		t.Fatalf("paused %v, want claude alone", paused)
	}
	// Pause.Begin bounds a named reset to at most six hours away.
	want := limit.ResetAt
	if ceiling := now.Add(6 * time.Hour); want.After(ceiling) {
		want = ceiling
	}
	if !until.Equal(want) {
		t.Errorf("claude paused until %s, want %s", until, want)
	}
	runs := runsOf(t, w, "sl-1")["sl-1"]
	if len(runs) != 1 || runs[0].Outcome != model.PausedOutcome {
		t.Fatalf("sl-1 runs = %+v, want one paused run", runs)
	}
	if streak, err := w.store.FailureStreak(w.ctx, "sl-1"); err != nil || (streak != nil && streak.Count != 0) {
		t.Errorf("sl-1 failure streak = %+v, %v; want 0", streak, err)
	}
	assertState(w, "sl-1", model.StateQueued, false)
}

func runClaude(t *testing.T, fw agent.Framework) *agent.UsageLimitError {
	t.Helper()
	_, err := fw.Run(context.Background(), agent.RunConfig{Prompt: "work the task", SandboxRoot: t.TempDir()})
	var limit *agent.UsageLimitError
	if !errors.As(err, &limit) {
		t.Logf("claude run error: %v", err)
		return nil
	}
	return limit
}

func next1730(now time.Time) time.Time {
	at := time.Date(now.Year(), now.Month(), now.Day(), 17, 30, 0, 0, time.UTC)
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// grepLines is the lines of text carrying any of words, for a test's
// own record of what it logged.
func grepLines(text string, words ...string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		for _, w := range words {
			if strings.Contains(line, w) {
				out = append(out, line)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}
