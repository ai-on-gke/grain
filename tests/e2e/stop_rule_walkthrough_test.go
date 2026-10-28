// The stop rule (agent.DecideStop) through a real cycle: a background
// task whose agent ends its turn on "back in a minute" without closing
// is resumed once in the same session, within the same run, and the run
// is recorded with the work the continuation did. The model is the one
// stand-in: a scripted framework with no Stop hook of its own, the
// position antigravity, codex and custom backends are in.
package e2e

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// promisingFramework ends its first turn on a promise and, resumed with
// the nudge, closes with a comment.
type promisingFramework struct {
	mu      sync.Mutex
	configs []agent.RunConfig
}

func (f *promisingFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.mu.Lock()
	f.configs = append(f.configs, cfg)
	f.mu.Unlock()
	if cfg.ContinuePrompt == "" {
		return &agent.Result{
			FinalText:        "Checking the CI log now -- back in a minute.",
			ToolCalls:        []agent.ToolCall{{Name: "run_command", Arguments: map[string]any{"command": "ls"}}},
			Transcript:       "looked around",
			ResumableSession: "sess-promise",
		}, nil
	}
	return &agent.Result{
		FinalText: "Reported.",
		ToolCalls: []agent.ToolCall{{Name: "comment_on_issue",
			Arguments: map[string]any{"comment": "The CI log shows the flake; nothing to change."}}},
		Transcript:       "commented",
		ResumableSession: "sess-promise",
	}, nil
}

func TestWalkthroughARunThatEndsOnAPromiseIsResumedOnce(t *testing.T) {
	t.Parallel()
	clock := &burstClock{now: baseTime}
	fw := &promisingFramework{}
	w, deps, repo := burstWorld(t, fw, clock, 1)
	fileIssue(w, "sr-1", human("erin"), repo)

	cycle(t, w, deps, clock.Now())

	fw.mu.Lock()
	configs := append([]agent.RunConfig(nil), fw.configs...)
	fw.mu.Unlock()
	if len(configs) != 2 {
		t.Fatalf("framework ran %d time(s) in the cycle, want 2: the run and its one continuation", len(configs))
	}
	if configs[1].ResumeSession != "sess-promise" || configs[1].ResumeMessage() != agent.BackgroundContinueReason {
		t.Errorf("continuation resumed %q with %q, want sess-promise and the background nudge alone",
			configs[1].ResumeSession, configs[1].ResumeMessage())
	}

	runs := runsOf(t, w, "sr-1")["sr-1"]
	if len(runs) != 1 {
		t.Fatalf("sr-1 has %d run(s), want one: the continuation is part of the same run", len(runs))
	}
	transcript, found, err := w.store.RunTranscript(w.ctx, "sr-1", runs[0].Attempt)
	if err != nil || !found {
		t.Fatalf("reading the transcript: found=%v err=%v", found, err)
	}
	if !strings.Contains(transcript, "grain resumed the session once") || !strings.Contains(transcript, "commented") {
		t.Errorf("transcript does not show the resumed turn:\n%s", transcript)
	}
	// The continuation's comment is what the run closed on, so the task
	// is not left waiting for a person to ask again.
	comments, err := w.store.Comments(w.ctx, "sr-1")
	if err != nil {
		t.Fatal(err)
	}
	var said bool
	for _, c := range comments {
		if strings.Contains(c.Body, "nothing to change") {
			said = true
		}
	}
	if !said {
		t.Errorf("the continuation's closing comment never reached the task: %+v", comments)
	}
}
