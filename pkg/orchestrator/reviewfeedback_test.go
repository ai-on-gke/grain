package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// reviewFinding is one add_review_comment call as a dispatched run makes
// it -- built here rather than through pkg/mcp's own handler, since what
// ProcessResult sees is agent.Result.ToolCalls and the point of these
// tests is what it does with them. (That the two halves agree about the
// argument names is escape_hatch_arguments_test.go's own subject.)
func reviewFinding(body, path string, line int) agent.ToolCall {
	args := map[string]any{"body": body}
	if path != "" {
		args["path"] = path
		// float64, the shape a JSON number arrives in from every CLI
		// that speaks MCP over stdio.
		args["line"] = float64(line)
	}
	return agent.ToolCall{Name: "add_review_comment", Arguments: args}
}

// reviewedAndReviewing is the pair every test here starts from: a
// finished task whose change is proposed in a pull request, and the
// review grain filed for it.
func reviewedAndReviewing(t *testing.T) (store *model.Store, ctx context.Context,
	sim *githubsim.Sim, client *github.RESTClient, reviewed, review model.Task) {

	t.Helper()
	store, ctx = openStore(t)
	sim, client = newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	tmpl := reviewTemplate(t, ctx, store)
	reviewed = completedTask(t, ctx, store, sim, client, "t1", repo, true, tmpl.ID)
	if err := orchestrator.SyncReviews(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncReviews: %v", err)
	}
	return store, ctx, sim, client, reviewed, reviewTaskOf(t, ctx, store, reviewed.ID)
}

// A review task is the one dispatch that has a pull request to attach
// feedback to: it carries LinkProposedBy back to the task it reviews, and
// that task carries LinkFixes to the pull request itself. So its
// add_review_comment calls become a real draft review there, on the lines
// they name.
func TestAReviewsFindingsBecomeADraftReviewOnTheChangeItReviews(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, reviewed, review := reviewedAndReviewing(t)

	result := toolResult(
		reviewFinding("this loop is quadratic", "pkg/thing/thing.go", 42),
		reviewFinding("the whole approach here needs a decision from whoever asked", "", 0),
	)
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.Reviews) != 1 {
		t.Fatalf("reviews on GitHub = %+v, want exactly one", sim.Reviews)
	}
	posted := sim.Reviews[0]
	if posted.Number != sim.PullRequests[0].Number {
		t.Errorf("review posted on #%d, want the reviewed change's own #%d",
			posted.Number, sim.PullRequests[0].Number)
	}
	if len(posted.Comments) != 1 {
		t.Fatalf("inline comments = %+v, want the one finding that named a line", posted.Comments)
	}
	if posted.Comments[0].Path != "pkg/thing/thing.go" || posted.Comments[0].Line != 42 {
		t.Errorf("inline comment = %+v, want it anchored where the run put it", posted.Comments[0])
	}
	if !strings.Contains(posted.Comments[0].Body, "quadratic") {
		t.Errorf("inline comment body = %q, want the run's own words", posted.Comments[0].Body)
	}
	// A finding that named no line cannot be an inline comment -- GitHub
	// rejects a comments[] entry with no path -- so it is in the review's
	// own body instead, where it is still read.
	if !strings.Contains(posted.Body, "needs a decision") {
		t.Errorf("review body = %q, want the finding that named no line in it", posted.Body)
	}
	if !strings.Contains(posted.Body, review.ID) {
		t.Errorf("review body = %q, want it to say which grain task wrote it", posted.Body)
	}

	// A draft is visible only to the credential that created it until a
	// human submits it, so the same findings have to reach the person the
	// change belongs to somewhere they will actually see them.
	bodies := commentBodies(t, ctx, store, reviewed.ID)
	last := bodies[len(bodies)-1]
	for _, want := range []string{"quadratic", "needs a decision", review.ID} {
		if !strings.Contains(last, want) {
			t.Errorf("comment on the reviewed task = %q, want %q in it", last, want)
		}
	}
	// And the review's own thread says where its work went, rather than
	// reading as a run that produced nothing.
	if own := commentBodies(t, ctx, store, review.ID); len(own) != 1 || !strings.Contains(own[0], "quadratic") {
		t.Errorf("the review's own conversation = %q, want it to record what it posted", own)
	}
}

// The decision this change makes: findings are the ones the review chose
// not to fix, so the change stops merging by itself and waits for the
// human whose judgement they were left for. Submitting is one click, and
// that is deliberately the whole of the hold.
func TestAReviewsFindingsTakeTheChangeItReviewsOffAutomaticMerge(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, reviewed, review := reviewedAndReviewing(t)
	markMergeable(sim)

	result := toolResult(reviewFinding("this retry loop never terminates", "run.go", 7))
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	got, err := store.GetTask(ctx, reviewed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoMerge {
		t.Fatal("a change whose review left findings must stop merging automatically")
	}
	state, err := store.State(ctx, reviewed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want %q -- the change is waiting on a person now",
			state, model.StateAwaitingSubmit)
	}

	// Long past the deadline a review that never finished would be
	// escalated on: this one finished, so nothing is announced and
	// nothing merges either.
	late := baseTime.Add(7 * time.Hour)
	if err := orchestrator.SyncPullRequests(ctx, store, client, late); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if sim.PullRequests[0].Merged {
		t.Fatal("a change held for a human's Submit must not merge on its own")
	}
	obs, err := store.GetObservation(ctx, reviewed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.MergeQueueBlockedAt != nil {
		t.Fatal("a review that finished and reported must not be escalated as one that never finished")
	}

	// The click. ui.Client.Submit is exactly this, and the change lands
	// the moment it reads clean afterwards.
	if err := store.UpdateTask(ctx, reviewed.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, late.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !sim.PullRequests[0].Merged {
		t.Fatal("a submitted change merges once its review's findings have been read")
	}
}

// The other half of the same decision: posting findings does not stop a
// review landing the fixes it did make. Its own pull request still merges
// straight back into the branch under review, which is what it was
// dispatched to do -- what waits is the change, and it waits on a person
// rather than on the review.
func TestAReviewThatPostedFindingsStillMergesItsOwnFixesBack(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, reviewed, review := reviewedAndReviewing(t)

	// It fixed what it could and wrote down what it would not fix.
	pushBranch(t, sim.BareRepo, model.BranchName(review.ID))
	result := toolResult(
		agent.ToolCall{Name: "run_command", Text: "pushed the fixes"},
		reviewFinding("this one is a call for whoever asked for the change", "shape.go", 11),
	)
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	markMergeable(sim)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	merged := map[string]bool{}
	for _, pr := range sim.PullRequests {
		merged[pr.Head] = pr.Merged
	}
	if !merged[model.BranchName(review.ID)] {
		t.Error("a review's own fixes still merge back into the branch it reviewed")
	}
	if merged[model.BranchName(reviewed.ID)] {
		t.Error("the change itself waits: its review left findings for a human to read")
	}
}

// A review that pushes nothing has no pull request of its own, so nothing
// will ever close it -- and the change it reviewed used to wait on that
// for six hours before the queue announced that the review had never
// finished. A finished review with nothing to merge back is finished.
func TestAReviewThatPushedNothingStopsHoldingTheChangeItReviewed(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, reviewed, review := reviewedAndReviewing(t)
	markMergeable(sim)

	// It read the change, found nothing worth changing, and said so.
	result := toolResult(agent.ToolCall{
		Name:      "comment_on_issue",
		Arguments: map[string]any{"comment": "read the diff; nothing to fix"},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !sim.PullRequests[0].Merged {
		t.Fatal("a change whose review finished with nothing to merge back must not keep waiting")
	}
	if bodies := commentBodies(t, ctx, store, reviewed.ID); len(bodies) != 1 {
		t.Fatalf("comments = %q, want only the one announcing the review -- nothing was escalated", bodies)
	}
	state, err := store.State(ctx, review.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateCompleted {
		t.Fatalf("the review's own state = %q, want %q", state, model.StateCompleted)
	}
}

// A change that landed (or was abandoned) while its review was still
// running has nothing left to hold, so the findings are still posted and
// the task itself is left exactly as it is -- a note about withdrawing a
// merge that already happened would only mislead whoever reads it.
func TestFindingsOnAChangeThatHasAlreadyClosedLeaveTheTaskAlone(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, reviewed, review := reviewedAndReviewing(t)

	closed := baseTime.Add(time.Minute)
	if err := store.Observe(ctx, model.Observation{
		TaskID: reviewed.ID, CompletedAt: &baseTime, ClosedAt: &closed,
	}); err != nil {
		t.Fatal(err)
	}

	result := toolResult(reviewFinding("this needs a second look", "late.go", 9))
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, review.ID+"-1",
		baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.Reviews) != 1 {
		t.Fatalf("reviews on GitHub = %+v, want the findings posted anyway", sim.Reviews)
	}
	got, err := store.GetTask(ctx, reviewed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoMerge {
		t.Error("a change that has already closed must not be taken off automatic merge after the fact")
	}
	if bodies := commentBodies(t, ctx, store, reviewed.ID); len(bodies) != 1 {
		t.Fatalf("comments = %q, want only the one announcing the review", bodies)
	}
}

// The fallback, and the reason nothing here is conditional on being a
// review: a run that reaches for this tool with no pull request behind it
// has still said something, and agent.Result is not persisted anywhere
// else.
func TestReviewFeedbackFromARunReviewingNothingLandsInItsOwnConversation(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})

	result := toolResult(reviewFinding("this comparison is inverted", "cmp.go", 3))
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.Reviews) != 0 {
		t.Fatalf("reviews on GitHub = %+v, want none -- this run reviewed nothing", sim.Reviews)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "inverted") {
		t.Fatalf("conversation = %q, want the findings relayed into it", bodies)
	}
	if !strings.Contains(bodies[0], "cmp.go:3") {
		t.Errorf("relayed findings = %q, want the line each one named", bodies[0])
	}
}

type failingCreateReviewClient struct {
	github.Client
}

func (c failingCreateReviewClient) CreateReview(owner, repo string, number int, body string, comments []github.NewReviewComment) (int, error) {
	return 0, errors.New("simulated 500 error")
}

func TestRelayReviewFeedbackFallsBackToTaskConversationWhenCreateReviewFails(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, _, review := reviewedAndReviewing(t)

	failingClient := failingCreateReviewClient{client}
	result := toolResult(
		reviewFinding("this needs refactoring", "foo.go", 10),
		reviewFinding("consider alternate architecture", "", 0),
	)
	if err := orchestrator.ProcessResult(ctx, store, failingClient, review, result, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.Reviews) != 0 {
		t.Fatalf("reviews on GitHub = %+v, want none since CreateReview failed", sim.Reviews)
	}
	// Fallback puts findings in the review task's own conversation with the error explanation
	bodies := commentBodies(t, ctx, store, review.ID)
	if len(bodies) != 1 {
		t.Fatalf("comments on review task = %q, want 1 fallback comment", bodies)
	}
	if !strings.Contains(bodies[0], "Posting these findings as a review on") || !strings.Contains(bodies[0], "failed (simulated 500 error)") {
		t.Errorf("comment preamble = %q, want error explanation", bodies[0])
	}
	if !strings.Contains(bodies[0], "this needs refactoring") || !strings.Contains(bodies[0], "foo.go:10") {
		t.Errorf("comment body = %q, want findings listed", bodies[0])
	}
	if !strings.Contains(bodies[0], "consider alternate architecture") {
		t.Errorf("comment body = %q, want unlocated finding listed", bodies[0])
	}
}

func TestReviewFindingsArgumentTypesAndInvalidBodies(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, _, review := reviewedAndReviewing(t)

	result := &agent.Result{
		ToolCalls: []agent.ToolCall{
			// float64
			{Name: "add_review_comment", Arguments: map[string]any{"body": "from float", "path": "a.go", "line": float64(10)}},
			// int
			{Name: "add_review_comment", Arguments: map[string]any{"body": "from int", "path": "b.go", "line": int(20)}},
			// int64
			{Name: "add_review_comment", Arguments: map[string]any{"body": "from int64", "path": "c.go", "line": int64(30)}},
			// json.Number
			{Name: "add_review_comment", Arguments: map[string]any{"body": "from json.Number", "path": "d.go", "line": json.Number("40")}},
			// whitespace path trimmed -> unlocated
			{Name: "add_review_comment", Arguments: map[string]any{"body": "unlocated due to empty path", "path": "  ", "line": float64(50)}},
			// invalid line <= 0 -> unlocated
			{Name: "add_review_comment", Arguments: map[string]any{"body": "unlocated due to non-positive line", "path": "e.go", "line": float64(0)}},
			// empty body filtered out
			{Name: "add_review_comment", Arguments: map[string]any{"body": "   ", "path": "f.go", "line": float64(10)}},
			// error tool call filtered out
			{Name: "add_review_comment", Arguments: map[string]any{"body": "errored tool call", "path": "g.go", "line": float64(10)}, IsError: true},
			// unrelated tool call ignored
			{Name: "run_command", Arguments: map[string]any{"command": "ls"}},
		},
	}

	if err := orchestrator.ProcessResult(ctx, store, client, review, result, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.Reviews) != 1 {
		t.Fatalf("reviews on GitHub = %+v, want 1", sim.Reviews)
	}
	posted := sim.Reviews[0]
	if len(posted.Comments) != 4 {
		t.Fatalf("inline comments count = %d, want 4 (float, int, int64, json.Number)", len(posted.Comments))
	}
	if posted.Comments[0].Path != "a.go" || posted.Comments[0].Line != 10 || posted.Comments[0].Body != "from float" {
		t.Errorf("comment 0 = %+v", posted.Comments[0])
	}
	if posted.Comments[1].Path != "b.go" || posted.Comments[1].Line != 20 || posted.Comments[1].Body != "from int" {
		t.Errorf("comment 1 = %+v", posted.Comments[1])
	}
	if posted.Comments[2].Path != "c.go" || posted.Comments[2].Line != 30 || posted.Comments[2].Body != "from int64" {
		t.Errorf("comment 2 = %+v", posted.Comments[2])
	}
	if posted.Comments[3].Path != "d.go" || posted.Comments[3].Line != 40 || posted.Comments[3].Body != "from json.Number" {
		t.Errorf("comment 3 = %+v", posted.Comments[3])
	}
	if !strings.Contains(posted.Body, "unlocated due to empty path") || !strings.Contains(posted.Body, "unlocated due to non-positive line") {
		t.Errorf("draft review body = %q, want unlocated comments", posted.Body)
	}
	if strings.Contains(posted.Body, "errored tool call") {
		t.Errorf("draft review body = %q, errored tool call should have been excluded", posted.Body)
	}
}
