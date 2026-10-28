package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// deadlineClient is a one-tool server whose clock the test holds: the
// tool answers a fixed sentence, and everything else these tests read is
// what the registry added to it.
func deadlineClient(t *testing.T, deadline time.Time, now *time.Time, answer Result) *Client {
	t.Helper()
	registry := NewRegistry()
	registry.AnnounceDeadline(deadline, func() time.Time { return *now })
	registry.Register(Tool{
		Name:        "say",
		Description: "answers the same thing every time",
		InputSchema: map[string]any{"type": "object"},
		Handler:     func(context.Context, map[string]any) Result { return answer },
	})
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })
	return client
}

func callSay(t *testing.T, client *Client) *CallResult {
	t.Helper()
	res, err := client.CallTool(context.Background(), "say", nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A run with most of its budget left is told nothing: the notice is for
// the turns where the deadline changes what the run should do, and one
// on every result from turn 1 would be noise a model learns to skip
// exactly when it starts to matter.
func TestToolResultsCarryNoDeadlineNoticeEarlyInARun(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	client := deadlineClient(t, now.Add(90*time.Minute), &now, Result{Text: "the answer"})

	if got := callSay(t, client).Text(); got != "the answer" {
		t.Errorf("text = %q, want the tool's own answer untouched", got)
	}
}

// Inside the window, every result says how much is left and what to do
// with it -- the number alone is a fact a run can note and ignore.
func TestToolResultsCarryTheTimeLeftOnceTheBudgetRunsLow(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	client := deadlineClient(t, now.Add(14*time.Minute+30*time.Second), &now, Result{Text: "the answer"})

	got := callSay(t, client).Text()
	if !strings.HasPrefix(got, "the answer\n\n") {
		t.Fatalf("text = %q, want the tool's own answer first, then the notice", got)
	}
	// Rounded down: a run told "15m" with fourteen and a half minutes
	// left has been handed thirty seconds it does not have.
	if !strings.Contains(got, "14m left") {
		t.Errorf("text = %q, want the time remaining rounded down to 14m", got)
	}
	for _, want := range []string{"cancels this run", "push"} {
		if !strings.Contains(got, want) {
			t.Errorf("text = %q, want it to mention %q", got, want)
		}
	}
}

// The last few minutes get different advice, because what fits in them
// is different: not "finish this piece and push it" but "push now".
func TestTheDeadlineNoticeEscalatesInTheLastMinutes(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	client := deadlineClient(t, now.Add(90*time.Second), &now, Result{Text: "the answer"})

	got := callSay(t, client).Text()
	if !strings.Contains(got, "1m30s left") {
		t.Errorf("text = %q, want seconds once there are barely any minutes to spend", got)
	}
	if !strings.Contains(got, "no time for another edit-and-test cycle") {
		t.Errorf("text = %q, want the last-minutes wording", got)
	}
}

// Past the deadline the run is being cancelled as it reads this, so the
// only instruction left is the one that saves work already committed.
func TestTheDeadlineNoticeSaysSoOnceTheBudgetIsGone(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	client := deadlineClient(t, now.Add(-time.Minute), &now, Result{Text: "the answer"})

	got := callSay(t, client).Text()
	if !strings.Contains(got, "past its wall-clock deadline") {
		t.Errorf("text = %q, want it to say the deadline has passed", got)
	}
}

// A failed call is just as close to the wall as a successful one, and is
// the likelier moment for a run to start a long repair it cannot push.
// The notice rides along without turning the failure into a success.
func TestAFailedToolResultCarriesTheDeadlineNoticeToo(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	client := deadlineClient(t, now.Add(3*time.Minute), &now,
		Result{Text: "exit=1\nboom", IsError: true})

	res := callSay(t, client)
	if !res.IsError {
		t.Errorf("IsError = false, want the handler's own error flag kept")
	}
	if !strings.Contains(res.Text(), "[grain]") {
		t.Errorf("text = %q, want the notice on a failed result as well", res.Text())
	}
}

// The clock moves, and so does the notice: the same tool called twice
// reports the time left at each call rather than at server start.
func TestTheDeadlineNoticeIsComputedPerCall(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	deadline := now.Add(30 * time.Minute)
	client := deadlineClient(t, deadline, &now, Result{Text: "the answer"})

	if got := callSay(t, client).Text(); got != "the answer" {
		t.Fatalf("text = %q, want nothing added with 30m left", got)
	}
	now = deadline.Add(-12 * time.Minute)
	if got := callSay(t, client).Text(); !strings.Contains(got, "12m left") {
		t.Errorf("text = %q, want the notice once the same run has 12m left", got)
	}
}

// Every caller with no deadline to give -- pkg/mcp's own tests,
// tests/e2e, a `grain mcpserver` run by hand -- gets exactly the results
// it always did.
func TestAServerWithNoDeadlineLeavesToolResultsAlone(t *testing.T) {
	registry := NewRegistry()
	registry.AnnounceDeadline(time.Time{}, nil)
	registry.Register(Tool{
		Name:        "say",
		InputSchema: map[string]any{"type": "object"},
		Handler:     func(context.Context, map[string]any) Result { return Result{Text: "the answer"} },
	})
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	if got := callSay(t, client).Text(); got != "the answer" {
		t.Errorf("text = %q, want the tool's own answer untouched", got)
	}
}

// The window is stated in one place, and the notice starts exactly at
// it: this is the boundary the two halves of the wording are chosen
// around, so a change to one without the other should fail here.
func TestTheDeadlineNoticeStartsAtTheWindow(t *testing.T) {
	if got := runDeadlineNotice(RunDeadlineNoticeWindow + time.Second); got != "" {
		t.Errorf("notice just outside the window = %q, want none", got)
	}
	if got := runDeadlineNotice(RunDeadlineNoticeWindow); got == "" {
		t.Error("notice at exactly the window = \"\", want the run told")
	}
	if got := runDeadlineNotice(runDeadlineFinalWindow); !strings.Contains(got, "no time for another") {
		t.Errorf("notice at exactly the final window = %q, want the last-minutes wording", got)
	}
}

// The deadline reaches the handler itself, not just the line appended
// after it. A tool that blocks -- wait_for_checks -- has to know how much
// of the run is left *before* it decides how long to block, and this is
// the wire it reads it off.
func TestAToolCallRunsUnderTheAnnouncedDeadline(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	registry := NewRegistry()
	registry.AnnounceDeadline(now.Add(45*time.Minute), func() time.Time { return now })
	registry.Register(Tool{
		Name:        "say",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, _ map[string]any) Result {
			remaining, ok := runDeadlineRemaining(ctx)
			return Result{Text: fmt.Sprintf("%v %v", remaining, ok)}
		},
	})
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	if got := callSay(t, client).Text(); got != "45m0s true" {
		t.Errorf("the handler saw %q, want the announced deadline's 45m0s", got)
	}
}

// And a server nobody gave a deadline hands its handlers no deadline
// either -- "nobody said" has to stay distinguishable from "no time
// left", or every unconfigured run would clamp its waits to nothing.
func TestAToolCallWithNoAnnouncedDeadlineSeesNone(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Tool{
		Name:        "say",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, _ map[string]any) Result {
			_, ok := runDeadlineRemaining(ctx)
			return Result{Text: fmt.Sprintf("%v", ok)}
		},
	})
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	if got := callSay(t, client).Text(); got != "false" {
		t.Errorf("the handler saw a deadline (%q) on a server that was told none", got)
	}
}
