package model_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// proposal files one task, proposed by parent, approved by whoever
// approver names -- nil for a task still waiting on somebody.
func proposal(t *testing.T, store *model.Store, ctx context.Context,
	id, parent string, approver *model.Principal) model.Task {

	t.Helper()
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: id,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: bot}, Reason: model.ReasonDirect,
		},
		Binding: model.BindingDirective,
	}
	if parent != "" {
		task.Links = []model.Link{{Kind: model.LinkProposedBy, Target: parent}}
	}
	if approver != nil {
		task.Approval = &model.Attribution{Actor: *approver}
		task.ApprovedAt = &now
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing %s: %v", id, err)
	}
	return task
}

// The depth bound is counted over the chain of tasks that were queued
// with nobody looking at them, and a human approval breaks that chain:
// the count starts again from whoever last decided something, so a tree
// grows as deep as anybody is willing to keep approving it and no
// further without them.
func TestExceededAutoSubmitLimitCountsFromTheLastHumanApproval(t *testing.T) {
	store, ctx := open(t)
	limits := model.AutoSubmitLimits{Depth: 2, FanOut: 100}

	// A task a person approved: its proposal would be the first
	// self-approved level, which is inside a limit of two.
	proposal(t, store, ctx, "root", "", &human)
	if limit, err := store.ExceededAutoSubmitLimit(ctx, "root", limits); err != nil {
		t.Fatal(err)
	} else if limit != model.AutoSubmitWithinLimits {
		t.Errorf("limit = %q under a human-approved task, want none", limit)
	}

	// One level of self-approval below it: the next proposal is level
	// two, which the limit still allows.
	proposal(t, store, ctx, "first", "root", &model.AutoSubmitPrincipal)
	if limit, err := store.ExceededAutoSubmitLimit(ctx, "first", limits); err != nil {
		t.Fatal(err)
	} else if limit != model.AutoSubmitWithinLimits {
		t.Errorf("limit = %q one level down, want none", limit)
	}

	// Two: the next proposal would be a child of a child of a child,
	// which is the shape a looping agent produces.
	proposal(t, store, ctx, "second", "first", &model.AutoSubmitPrincipal)
	if limit, err := store.ExceededAutoSubmitLimit(ctx, "second", limits); err != nil {
		t.Fatal(err)
	} else if limit != model.AutoSubmitDepthReached {
		t.Errorf("limit = %q two levels down, want %q", limit, model.AutoSubmitDepthReached)
	}

	// And a person approving that third one puts the count back where it
	// started: they are in the loop, which is the whole thing this
	// counts the distance from.
	if err := store.UpdateTask(ctx, "second", func(task *model.Task) error {
		task.Approval = &model.Attribution{Actor: human}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if limit, err := store.ExceededAutoSubmitLimit(ctx, "second", limits); err != nil {
		t.Fatal(err)
	} else if limit != model.AutoSubmitWithinLimits {
		t.Errorf("limit = %q below a task a human approved, want none", limit)
	}
}

// A standing session's own approval is self-approval too, for the tasks
// below it: the person chatting saw the message they sent, not what the
// work they asked for went on to queue for itself.
func TestExceededAutoSubmitLimitCountsAConsoleSessionAsALevel(t *testing.T) {
	store, ctx := open(t)
	limits := model.AutoSubmitLimits{Depth: 2, FanOut: 100}

	proposal(t, store, ctx, "session", "", &human)
	proposal(t, store, ctx, "asked-for", "session", &model.ConsoleSessionPrincipal)
	proposal(t, store, ctx, "its-own", "asked-for", &model.AutoSubmitPrincipal)
	if limit, err := store.ExceededAutoSubmitLimit(ctx, "its-own", limits); err != nil {
		t.Fatal(err)
	} else if limit != model.AutoSubmitDepthReached {
		t.Errorf("limit = %q below a session-queued task's own proposal, want %q",
			limit, model.AutoSubmitDepthReached)
	}
}

// The chain is only the tasks an agent's own proposals put in it. grain
// approves work nobody proposed -- a review task, filed against a pull
// request from a template a human configured once -- and spending a
// task's depth on tasks it never asked for would bound the machinery
// rather than the loop.
func TestExceededAutoSubmitLimitIgnoresGrainsOwnApprovals(t *testing.T) {
	store, ctx := open(t)
	limits := model.AutoSubmitLimits{Depth: 2, FanOut: 100}

	reviewer := model.Principal{Kind: model.PrincipalAutomation, ID: "reviewer"}
	proposal(t, store, ctx, "change", "", &human)
	proposal(t, store, ctx, "review", "change", &reviewer)
	proposal(t, store, ctx, "fix", "review", &model.AutoSubmitPrincipal)
	if limit, err := store.ExceededAutoSubmitLimit(ctx, "fix", limits); err != nil {
		t.Fatal(err)
	} else if limit != model.AutoSubmitWithinLimits {
		t.Errorf("limit = %q, want none -- a review task is not a level of self-approval", limit)
	}
}

// Fan-out counts what one task has already had queued unreviewed,
// whoever else it proposed for a human to decide on.
func TestExceededAutoSubmitLimitCountsOneTasksSelfApprovedProposals(t *testing.T) {
	store, ctx := open(t)
	limits := model.AutoSubmitLimits{Depth: 2, FanOut: 2}

	proposal(t, store, ctx, "busy", "", &human)
	proposal(t, store, ctx, "piece-1", "busy", &model.AutoSubmitPrincipal)
	// Waiting on a person, so it is not work this permission created.
	proposal(t, store, ctx, "waiting", "busy", nil)
	// Somebody else's child entirely.
	proposal(t, store, ctx, "elsewhere", "other", &model.AutoSubmitPrincipal)
	if limit, err := store.ExceededAutoSubmitLimit(ctx, "busy", limits); err != nil {
		t.Fatal(err)
	} else if limit != model.AutoSubmitWithinLimits {
		t.Errorf("limit = %q with one of two queued, want none", limit)
	}

	proposal(t, store, ctx, "piece-2", "busy", &model.AutoSubmitPrincipal)
	if limit, err := store.ExceededAutoSubmitLimit(ctx, "busy", limits); err != nil {
		t.Fatal(err)
	} else if limit != model.AutoSubmitFanOutReached {
		t.Errorf("limit = %q at the cap, want %q", limit, model.AutoSubmitFanOutReached)
	}
}

// Nothing here is measured against a bound of zero: an unset pair is a
// deployment that has never chosen, which gets grain's own.
func TestExceededAutoSubmitLimitResolvesAnUnsetPair(t *testing.T) {
	store, ctx := open(t)

	proposal(t, store, ctx, "root", "", &human)
	for i := range model.DefaultAutoSubmitFanOut {
		proposal(t, store, ctx, "piece-"+string(rune('a'+i)), "root", &model.AutoSubmitPrincipal)
	}
	if limit, err := store.ExceededAutoSubmitLimit(ctx, "root", model.AutoSubmitLimits{}); err != nil {
		t.Fatal(err)
	} else if limit != model.AutoSubmitFanOutReached {
		t.Errorf("limit = %q at the default fan-out, want %q -- an unset bound is not no bound",
			limit, model.AutoSubmitFanOutReached)
	}
}

// A deployment with no settings row at all, and one whose row predates
// these columns, run on grain's own bounds rather than on none: a zero
// read back out of the store is "nobody chose".
func TestAutoSubmitLimitsOfAnUnconfiguredDeployment(t *testing.T) {
	want := model.DefaultAutoSubmitLimits()
	if got := model.AutoSubmitLimitsOf(nil); got != want {
		t.Errorf("AutoSubmitLimitsOf(nil) = %+v, want %+v", got, want)
	}
	if got := model.AutoSubmitLimitsOf(&model.Config{}); got != want {
		t.Errorf("a stored zero pair resolved to %+v, want %+v", got, want)
	}
	chosen := model.Config{MaxAutoSubmitDepth: 1, MaxAutoSubmitFanOut: 9}
	if got := model.AutoSubmitLimitsOf(&chosen); got.Depth != 1 || got.FanOut != 9 {
		t.Errorf("a chosen pair resolved to %+v, want 1 and 9", got)
	}
}

// SelfApproved names the two approvals that put a *proposal* in the
// queue with nobody reading it, and nothing else -- not a person's own
// approval, not one relayed on their behalf, not grain's own machinery,
// and not a task nobody has approved at all.
func TestSelfApprovedNamesOnlyProposalsNobodyRead(t *testing.T) {
	relayed := model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}
	for _, tc := range []struct {
		name string
		task model.Task
		want bool
	}{
		{"auto-submitted", model.Task{
			Approval: &model.Attribution{Actor: model.AutoSubmitPrincipal}}, true},
		{"queued by a standing session", model.Task{
			Approval: &model.Attribution{Actor: model.ConsoleSessionPrincipal}}, true},
		{"approved by a person", model.Task{
			Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}}}, false},
		{"relayed on a person's behalf", model.Task{
			Approval: &model.Attribution{
				Actor:      model.AutoSubmitPrincipal,
				OnBehalfOf: &model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"},
			}}, false},
		{"grain's own machinery", model.Task{
			Approval: &model.Attribution{Actor: relayed}}, false},
		{"waiting on somebody", model.Task{}, false},
	} {
		if got := model.SelfApproved(tc.task); got != tc.want {
			t.Errorf("%s: SelfApproved = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAutoSubmitLimitNote(t *testing.T) {
	limits := model.AutoSubmitLimits{Depth: 3, FanOut: 5}

	depthNote := model.AutoSubmitLimitNote("task-123", model.AutoSubmitDepthReached, limits)
	if !strings.Contains(depthNote, "task-123") || !strings.Contains(depthNote, "4 levels of self-approved work") || !strings.Contains(depthNote, "3 is the limit") {
		t.Errorf("unexpected depthNote: %q", depthNote)
	}

	fanOutNote := model.AutoSubmitLimitNote("task-456", model.AutoSubmitFanOutReached, limits)
	if !strings.Contains(fanOutNote, "task-456") || !strings.Contains(fanOutNote, "already had 5 proposals queued") {
		t.Errorf("unexpected fanOutNote: %q", fanOutNote)
	}

	emptyNote := model.AutoSubmitLimitNote("task-789", model.AutoSubmitWithinLimits, limits)
	if emptyNote != "" {
		t.Errorf("expected empty note for AutoSubmitWithinLimits, got %q", emptyNote)
	}
}
