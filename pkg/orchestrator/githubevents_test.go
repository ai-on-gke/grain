package orchestrator_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/ghwebhook"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

type fakeEventSource struct {
	events []ghwebhook.Event
	acked  uint64
}

func (f *fakeEventSource) Pending() ([]ghwebhook.Event, error) {
	var out []ghwebhook.Event
	for _, ev := range f.events {
		if ev.Seq > f.acked {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (f *fakeEventSource) Ack(seq uint64) error {
	if seq > f.acked {
		f.acked = seq
	}
	return nil
}

func runGitHubEventsReconciler(t *testing.T, ctx context.Context, deps orchestrator.Deps, now time.Time) {
	t.Helper()
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "github-events" {
			if err := r.Reconcile(ctx, deps, now); err != nil {
				t.Fatalf("github-events reconciler: %v", err)
			}
			return
		}
	}
	t.Fatal("github-events reconciler is not registered in orchestrator.Reconcilers()")
}

func TestGitHubEventsTrustedMentionQueuesTaskReactsEyesAndDeduplicates(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	sim.CollaboratorPermissions = map[string]string{"alice": "write"}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	source := &fakeEventSource{
		events: []ghwebhook.Event{
			{
				Seq:         1,
				DeliveryID:  "deliv-1",
				Event:       "issue_comment",
				Action:      "created",
				Repo:        "acme/widgets",
				SenderLogin: "alice",
				SenderType:  "User",
				Number:      12,
				Title:       "Broken pagination",
				CommentID:   501,
				CommentBody: "@grain-bot please fix the off-by-one bug in page cursor",
			},
			// Duplicate redelivery of the exact same comment under a second seq.
			{
				Seq:         2,
				DeliveryID:  "deliv-2",
				Event:       "issue_comment",
				Action:      "created",
				Repo:        "acme/widgets",
				SenderLogin: "alice",
				SenderType:  "User",
				Number:      12,
				Title:       "Broken pagination",
				CommentID:   501,
				CommentBody: "@grain-bot please fix the off-by-one bug in page cursor",
			},
		},
	}

	deps := orchestrator.Deps{
		Store:        store,
		Client:       client,
		GitHubEvents: source,
		AppBotIdentity: func(_ context.Context, _ model.RepoRef) (string, bool) {
			return "grain-bot[bot]", true
		},
	}

	runGitHubEventsReconciler(t, ctx, deps, baseTime)
	if source.acked != 2 {
		t.Fatalf("acked = %d, want 2", source.acked)
	}

	tasks, err := store.TasksWithOrigin(ctx, model.GitHubOrigin(repo, 12))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks filed = %d, want 1", len(tasks))
	}
	filed := tasks[0]
	st, err := store.State(ctx, filed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state = %q, want %q for trusted collaborator", st, model.StateQueued)
	}
	if filed.Origin.Attribution.Actor != (model.Principal{Kind: model.PrincipalHuman, ID: "alice"}) {
		t.Errorf("origin actor = %+v, want alice human", filed.Origin.Attribution.Actor)
	}
	if !slices.Contains(sim.CommentReactions, githubsim.CommentReaction{CommentID: 501, Content: "eyes"}) {
		t.Errorf("reactions = %+v, want eyes on comment 501", sim.CommentReactions)
	}
}

func TestGitHubEventsUntrustedMentionLandsInStateProposed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	sim.CollaboratorPermissions = map[string]string{"stranger": "read"}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	source := &fakeEventSource{
		events: []ghwebhook.Event{
			{
				Seq:         1,
				DeliveryID:  "deliv-untrusted",
				Event:       "issue_comment",
				Action:      "created",
				Repo:        "acme/widgets",
				SenderLogin: "stranger",
				SenderType:  "User",
				Number:      19,
				Title:       "Feature idea",
				CommentID:   777,
				CommentBody: "@grain-bot implement dark mode",
			},
		},
	}
	deps := orchestrator.Deps{
		Store:        store,
		Client:       client,
		GitHubEvents: source,
		AppBotIdentity: func(_ context.Context, _ model.RepoRef) (string, bool) {
			return "grain-bot[bot]", true
		},
	}

	runGitHubEventsReconciler(t, ctx, deps, baseTime)
	tasks, err := store.TasksWithOrigin(ctx, model.GitHubOrigin(repo, 19))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks filed = %d, want 1", len(tasks))
	}
	st, err := store.State(ctx, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateProposed {
		t.Fatalf("untrusted user task state = %q, want %q", st, model.StateProposed)
	}
	if tasks[0].Approval != nil {
		t.Errorf("untrusted user task must not have Approval set: %+v", tasks[0].Approval)
	}
}

func TestGitHubEventsFiltersSelfLoopsAndGrainMarkers(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	sim.CollaboratorPermissions = map[string]string{"grain-bot[bot]": "write", "alice": "write"}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	source := &fakeEventSource{
		events: []ghwebhook.Event{
			{
				Seq:         1,
				Event:       "issue_comment",
				Action:      "created",
				Repo:        "acme/widgets",
				SenderLogin: "grain-bot[bot]",
				SenderType:  "Bot",
				Number:      21,
				CommentID:   801,
				CommentBody: "@grain-bot self echo",
			},
			{
				Seq:         2,
				Event:       "issue_comment",
				Action:      "created",
				Repo:        "acme/widgets",
				SenderLogin: "alice",
				SenderType:  "User",
				Number:      21,
				CommentID:   802,
				CommentBody: "<!-- grain:work-tree -->\n@grain-bot status update",
			},
			{
				Seq:         3,
				Event:       "issue_comment",
				Action:      "created",
				Repo:        "acme/widgets",
				SenderLogin: "alice",
				SenderType:  "User",
				Number:      21,
				CommentID:   803,
				CommentBody: "Use `@grain-bot review` in docs to trigger a review.",
			},
		},
	}
	deps := orchestrator.Deps{
		Store:        store,
		Client:       client,
		GitHubEvents: source,
		AppBotIdentity: func(_ context.Context, _ model.RepoRef) (string, bool) {
			return "grain-bot[bot]", true
		},
	}

	runGitHubEventsReconciler(t, ctx, deps, baseTime)
	tasks, err := store.TasksWithOrigin(ctx, model.GitHubOrigin(repo, 21))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks for self-loops and code-span mentions, got %+v", tasks)
	}
}

func TestExternalPRReviewSubmitsBotReviewAndUpdatesCheckRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	sim.CollaboratorPermissions = map[string]string{"alice": "maintain"}

	pushBranch(t, sim.BareRepo, "feat/human-pr")
	pr, err := client.CreatePullRequest("acme", "widgets", "feat/human-pr", "main", "Human PR", "Adds handler")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	source := &fakeEventSource{
		events: []ghwebhook.Event{
			{
				Seq:                    1,
				DeliveryID:             "deliv-rr",
				Event:                  "pull_request",
				Action:                 "review_requested",
				Repo:                   "acme/widgets",
				SenderLogin:            "alice",
				Number:                 pr.Number,
				IsPullRequest:          true,
				Title:                  "Human PR",
				HeadRef:                "feat/human-pr",
				HeadSHA:                detail.HeadSHA,
				BaseRef:                "main",
				RequestedReviewerLogin: "grain-bot[bot]",
			},
		},
	}
	deps := orchestrator.Deps{
		Store:        store,
		Client:       client,
		GitHubEvents: source,
		AppBotIdentity: func(_ context.Context, _ model.RepoRef) (string, bool) {
			return "grain-bot[bot]", true
		},
	}

	runGitHubEventsReconciler(t, ctx, deps, baseTime)
	tasks, err := store.TasksWithOrigin(ctx, model.GitHubOrigin(repo, pr.Number))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("review tasks filed = %d, want 1", len(tasks))
	}
	reviewTask := tasks[0]
	if reviewTask.Intent != model.IntentReview || reviewTask.Origin.Reason != model.ReasonReview {
		t.Fatalf("unexpected task shape: intent=%q reason=%q", reviewTask.Intent, reviewTask.Origin.Reason)
	}
	if reviewTask.Base != "feat/human-pr" {
		t.Errorf("review base = %q, want feat/human-pr", reviewTask.Base)
	}

	// Simulate the review run finishing with an inline finding.
	result := toolResult(
		reviewFinding("missing context cancellation check", "handler.go", 17),
		agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"body": "Reviewed handler.go."}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, reviewTask, result, reviewTask.ID+"-1", baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.Reviews) != 1 {
		t.Fatalf("posted reviews = %+v, want 1 submitted review", sim.Reviews)
	}
	if sim.Reviews[0].Event != "COMMENT" {
		t.Errorf("review Event = %q, want COMMENT (submitted, not invisible PENDING draft)", sim.Reviews[0].Event)
	}
	// Inline comments on a submitted review are visible in ListReviewComments!
	visibleComments, err := client.ListReviewComments("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(visibleComments) != 1 || !strings.Contains(visibleComments[0].Body, "cancellation") {
		t.Errorf("visible inline comments = %+v, want 1 published comment", visibleComments)
	}

	// And the Check Run created at filing should now be completed as neutral.
	checks, err := client.ListCheckRuns("acme", "widgets", detail.HeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	foundCheck := false
	for _, c := range checks {
		if c.Name == "grain/review" {
			foundCheck = true
			if c.Status != "completed" || c.Conclusion == nil || *c.Conclusion != "neutral" {
				t.Errorf("grain/review check = %+v, want completed/neutral", c)
			}
		}
	}
	if !foundCheck {
		t.Errorf("expected grain/review check run on %s, got %+v", detail.HeadSHA, checks)
	}
}

func TestChangesRequestedReviewWithNoInlineCommentsRequeuesGrainTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	sim.CollaboratorPermissions = map[string]string{"reviewer-bob": "write"}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	owned := completedTask(t, ctx, store, sim, client, "t-owned", repo, true, "")
	prNum := sim.PullRequests[0].Number

	source := &fakeEventSource{
		events: []ghwebhook.Event{
			{
				Seq:           1,
				DeliveryID:    "deliv-cr",
				Event:         "pull_request_review",
				Action:        "submitted",
				Repo:          "acme/widgets",
				SenderLogin:   "reviewer-bob",
				Number:        prNum,
				IsPullRequest: true,
				ReviewID:      9001,
				ReviewState:   "changes_requested",
				ReviewBody:    "Please split the migration into a backwards-compatible step first.",
			},
		},
	}
	deps := orchestrator.Deps{
		Store:        store,
		Client:       client,
		GitHubEvents: source,
		AppBotIdentity: func(_ context.Context, _ model.RepoRef) (string, bool) {
			return "grain-bot[bot]", true
		},
	}

	runGitHubEventsReconciler(t, ctx, deps, baseTime.Add(5*time.Minute))

	st, err := store.State(ctx, owned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("task state after CHANGES_REQUESTED review = %q, want %q", st, model.StateQueued)
	}
	comments, err := store.Comments(ctx, owned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "backwards-compatible") {
		t.Fatalf("expected reviewer body in task conversation, got %+v", comments)
	}
}
