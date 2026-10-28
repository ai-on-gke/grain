package orchestrator_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// filedTask puts a queued task in the store, the way the CLI or the UI
// would -- these tests are about what ProcessResult does with a run's own
// tool calls. There is no issue to seed alongside it any more: a task is
// a row, and githubsim.Sim answers only for the target repo's pull
// requests.
func filedTask(t *testing.T, ctx context.Context, store *model.Store, id string, repo model.RepoRef) model.Task {
	t.Helper()
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "fix the thing", Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Target:   &repo,
		Binding:  model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing task: %v", err)
	}
	return task
}

// commentBodies is what a task's conversation now says, in order -- where
// these tests used to read client.ListComments off a GitHub issue.
func commentBodies(t *testing.T, ctx context.Context, store *model.Store, taskID string) []string {
	t.Helper()
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(comments))
	for _, c := range comments {
		out = append(out, c.Body)
	}
	return out
}

func toolResult(calls ...agent.ToolCall) *agent.Result {
	return &agent.Result{ToolCalls: calls}
}

func TestProcessResultOpensAPullRequestForAPushedBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request, got %+v", sim.PullRequests)
	}
	if sim.PullRequests[0].Head != model.BranchName(task.ID) || sim.PullRequests[0].Base != "main" {
		t.Fatalf("got %+v", sim.PullRequests[0])
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A pull request opened and nobody has submitted it -- the finish path
	// sets CompletedAt, and model.StateOf reads that plus the fixes-link
	// below as "waiting on a human's Submit click" rather than as done.
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit", st)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantLink := model.PullRequestRef{Repo: *task.Target, Number: sim.PullRequests[0].Number}.String()
	found := false
	for _, l := range got.Links {
		if l.Kind == model.LinkFixes && l.Target == wantLink {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a LinkFixes to %s, got %+v", wantLink, got.Links)
	}
}

func TestProcessResultOpensAPullRequestWithFixesClauseForTaskWithIssue(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.Issue = 42
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request, got %+v", sim.PullRequests)
	}
	body := sim.PullRequests[0].Body
	if !strings.Contains(body, "Fixes #42") {
		t.Fatalf("body = %q, want Fixes #42", body)
	}
}

func TestProcessResultReusesAnAlreadyOpenPullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	existing, err := client.CreatePullRequest("acme", "widgets", model.BranchName(task.ID), "main", "existing", "")
	if err != nil {
		t.Fatal(err)
	}

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected no second PR to be opened, got %+v", sim.PullRequests)
	}
	if sim.PullRequests[0].Number != existing.Number {
		t.Fatalf("got %+v, want the existing PR reused", sim.PullRequests[0])
	}
}

func TestProcessResultRelaysAQuestionAndParksTheTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "ask_question", Arguments: map[string]any{"question": "which config file?"},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}

	if got := commentBodies(t, ctx, store, task.ID); len(got) != 1 || got[0] != "which config file?" {
		t.Fatalf("conversation = %q, want the relayed question", got)
	}

	// The question grain is waiting on is named by id, and the relay is
	// attributed as grain speaking for the agent rather than as its own.
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != comments[0].ID {
		t.Fatalf("pending question = %+v, want the stored comment's id %d", obs.PendingQuestionCommentID, comments[0].ID)
	}
	if comments[0].Author.Actor.Kind != model.PrincipalAutomation || comments[0].Author.OnBehalfOf == nil {
		t.Fatalf("relayed question attribution = %+v, want automation on behalf of the agent", comments[0].Author)
	}
}

// A question carries the one-line summary its run gave (Comment.Summary),
// which is what the inbox and the task list show of it; a run that gave
// none -- one dispatched before the parameter existed -- gets one derived
// from the question's first sentence rather than none at all.
func TestProcessResultStoresTheQuestionsSummary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"given", map[string]any{
			"question": "Which config file? There are two.",
			"summary":  "Pick the config file: prod.yml or dev.yml",
		}, "Pick the config file: prod.yml or dev.yml"},
		{"given across lines, collapsed", map[string]any{
			"question": "Which config file?",
			"summary":  "Pick the\nconfig file",
		}, "Pick the config file"},
		{"missing, derived from the question", map[string]any{
			"question": "Which config file? There are two.",
		}, "Which config file?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := openStore(t)
			_, client := newSim(t, "acme", "widgets", "main")
			task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})
			result := toolResult(agent.ToolCall{Name: "ask_question", Arguments: tc.args})
			if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
				t.Fatalf("ProcessResult: %v", err)
			}
			comments, err := store.Comments(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(comments) != 1 || comments[0].Summary != tc.want {
				t.Fatalf("comments = %+v, want one question summarized %q", comments, tc.want)
			}
			if comments[0].Body != tc.args["question"] {
				t.Errorf("body = %q, want the whole question", comments[0].Body)
			}
		})
	}
}

func TestProcessResultStoresASecretRequestsSummary(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})
	result := toolResult(agent.ToolCall{Name: "request_secret", Arguments: map[string]any{
		"secret": "stripe-api-key", "reason": "The deploy script authenticates with it.",
		"summary": "Set stripe-api-key so the deploy can authenticate",
	}})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Summary != "Set stripe-api-key so the deploy can authenticate" {
		t.Fatalf("comments = %+v, want the secret request summarized as the run said", comments)
	}
}

// TestProcessResultRelaysASecretRequestAndParksTheTask is
// request_secret's half of the parking contract (grain/task-230): the
// run asked for a credential it must never be handed, so the task parks
// exactly as it does on a question -- awaiting_reply, out of task_ready
// -- and records *which* secret, so the UI can offer a write-only box
// for it (ui.TaskDetail.PendingSecret) instead of only a reply box.
//
// The agent's own reason is relayed verbatim, since it is all the human
// deciding what to paste has to go on; grain's own sentence about where
// the value goes follows it, because that is the part a human has to be
// able to trust and an agent cannot promise.
func TestProcessResultRelaysASecretRequestAndParksTheTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "request_secret",
		Arguments: map[string]any{
			"secret": "stripe-api-key",
			"reason": "the deploy script authenticates to Stripe with it",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}

	got := commentBodies(t, ctx, store, task.ID)
	if len(got) != 1 {
		t.Fatalf("conversation = %q, want one relayed request", got)
	}
	if !strings.Contains(got[0], "the deploy script authenticates to Stripe with it") {
		t.Errorf("relayed request = %q, want the agent's own reason in it", got[0])
	}
	if !strings.Contains(got[0], "stripe-api-key") {
		t.Errorf("relayed request = %q, want it to name the secret", got[0])
	}

	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != comments[0].ID {
		t.Fatalf("pending question = %+v, want the request's own comment id %d",
			obs.PendingQuestionCommentID, comments[0].ID)
	}
	if obs.PendingSecret != "stripe-api-key" {
		t.Fatalf("pending secret = %q, want the name the run asked for", obs.PendingSecret)
	}
}

// TestProcessResultRelaysBothASecretRequestAndAQuestion is the same
// "neither call is dropped in silence" the comment/question pair below
// pins, for the two hatches that both park: a run that asked for a
// credential and also asked a question gets both relayed, and parks on
// the question -- the later of the two, and the one that ended its turn
// -- while the secret it asked for stays recorded and still offered.
func TestProcessResultRelaysBothASecretRequestAndAQuestion(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(
		agent.ToolCall{
			Name:      "request_secret",
			Arguments: map[string]any{"secret": "stripe-api-key", "reason": "the deploy script needs it"},
		},
		agent.ToolCall{
			Name: "ask_question", Arguments: map[string]any{"question": "live or test mode?"},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("conversation = %q, want the request and the question", commentBodies(t, ctx, store, task.ID))
	}
	if comments[1].Body != "live or test mode?" {
		t.Fatalf("second comment = %q, want the question last", comments[1].Body)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != comments[1].ID {
		t.Fatalf("pending question = %+v, want the question's id %d",
			obs.PendingQuestionCommentID, comments[1].ID)
	}
	if obs.PendingSecret != "stripe-api-key" {
		t.Fatalf("pending secret = %q, want the request to survive the question", obs.PendingSecret)
	}
}

// TestProcessResultRelaysBothACommentAndAQuestion covers a run that
// called comment_on_issue and then ask_question. ProcessResult used to
// return on the question before it ever looked for a comment, so the
// comment was dropped with no trace anywhere: agent.Result is not
// persisted, and this path never reaches the "nothing to act on" logging
// that would at least have named the call. Both are the run's own words
// and both belong in the conversation -- the comment first, and the
// question is still the one the task parks on.
func TestProcessResultRelaysBothACommentAndAQuestion(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(
		agent.ToolCall{
			Name: "comment_on_issue", Arguments: map[string]any{"comment": "both tools record fine"},
		},
		agent.ToolCall{
			Name: "ask_question", Arguments: map[string]any{"question": "which config file?"},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	got := commentBodies(t, ctx, store, task.ID)
	want := []string{"both tools record fine", "which config file?"}
	if len(got) != len(want) {
		t.Fatalf("conversation = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("conversation = %q, want %q", got, want)
		}
	}

	// The question is what the task waits on, not the comment that
	// preceded it, and asking parks the task rather than completing it.
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != comments[1].ID {
		t.Fatalf("pending question = %+v, want the question's id %d", obs.PendingQuestionCommentID, comments[1].ID)
	}
}

// TestProcessResultRelaysACommentAlongsideAPushedBranch is the same drop
// on the other ending: a pushed branch returned as soon as its pull
// request was opened, past the comment the same run had left. That
// combination is one comment_on_issue's own description invites -- "if
// you do push commits, a pull request is opened for them regardless of
// whether you also call this" -- so the remark explaining the push has to
// survive it.
func TestProcessResultRelaysACommentAlongsideAPushedBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	result := toolResult(
		agent.ToolCall{Name: "run_command", Text: "pushed"},
		agent.ToolCall{
			Name: "comment_on_issue", Arguments: map[string]any{"comment": "fixed it; here is why"},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request, got %+v", sim.PullRequests)
	}
	if got := commentBodies(t, ctx, store, task.ID); len(got) != 1 || got[0] != "fixed it; here is why" {
		t.Fatalf("conversation = %q, want the relayed comment", got)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit", st)
	}
}

// An attachment that will not decode is dropped rather than failing the
// finish (grain/task-1064): the run's pull request is still opened and
// linked, the comment is still relayed with every attachment that did
// decode, and the comment says which one was dropped and why.
func TestProcessResultDropsAnAttachmentThatIsNotBase64(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	result := toolResult(
		agent.ToolCall{Name: "run_command", Text: "pushed"},
		agent.ToolCall{
			Name: "comment_on_issue", Arguments: map[string]any{
				"comment": "fixed it",
				"attachments": []any{
					map[string]any{"filename": "after.png", "content_type": "image/png", "content": "cG5n"},
					map[string]any{"filename": "before-contact-sheet.png", "content_type": "image/png", "content": "cG5n!!"},
				},
			},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request, got %+v", sim.PullRequests)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantLink := model.PullRequestRef{Repo: *task.Target, Number: sim.PullRequests[0].Number}.String()
	if !slices.ContainsFunc(got.Links, func(l model.Link) bool {
		return l.Kind == model.LinkFixes && l.Target == wantLink
	}) {
		t.Fatalf("expected a LinkFixes to %s, got %+v", wantLink, got.Links)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit", st)
	}

	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 || !strings.HasPrefix(bodies[0], "fixed it\n") ||
		!strings.Contains(bodies[0], "attachment `before-contact-sheet.png` was dropped: not valid base64") {
		t.Fatalf("conversation = %q, want the comment with a note naming the dropped attachment", bodies)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := store.AttachmentMetas(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].Filename != "after.png" ||
		attachments[0].CommentID == nil || *attachments[0].CommentID != comments[0].ID {
		t.Fatalf("attachments = %+v, want after.png alone, on the relayed comment", attachments)
	}
}

// A question's attachments get the same tolerance: the question is still
// relayed and the task still parks on it.
func TestProcessResultRelaysAQuestionWhoseAttachmentIsNotBase64(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "ask_question", Arguments: map[string]any{
			"question":    "which one?",
			"attachments": []any{map[string]any{"filename": "options.png", "content": "%%%"}},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 || !strings.HasPrefix(bodies[0], "which one?") ||
		!strings.Contains(bodies[0], "attachment `options.png` was dropped") {
		t.Fatalf("conversation = %q, want the question with a drop note", bodies)
	}
}

// The comment-only ending, by contrast, stays 'completed': a task that
// pushed nothing has no pull request to submit, so there is nothing for
// a human to do next and no wait to name.
func TestProcessResultRelaysAClosingCommentWithNoPush(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "comment_on_issue", Arguments: map[string]any{
			"comment": "the answer is 4",
			"attachments": []any{map[string]any{
				"filename": "answer.png", "content_type": "image/png", "content": "cG5n",
			}},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The closing comment completes the task and opens nothing, which is
	// exactly model.StateAwaitingAck: finished, with an answer on it
	// nobody has read yet.
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}
	if got := commentBodies(t, ctx, store, task.ID); len(got) != 1 || got[0] != "the answer is 4" {
		t.Fatalf("conversation = %q, want the relayed closing comment", got)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := store.AttachmentMetas(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].Filename != "answer.png" ||
		attachments[0].ContentType != "image/png" || attachments[0].CommentID == nil ||
		*attachments[0].CommentID != comments[0].ID {
		t.Fatalf("attachments = %+v, want answer.png linked to the closing comment", attachments)
	}
}

// TestProcessResultRelaysFullAssistantResponseWhenCommentIsTruncatedWithEllipsis
// tests that when an agent provides an abbreviated/truncated comment in comment_on_issue
// ending with an ellipsis (…), ProcessResult prefers the full conversational assistant
// response from FinalText.
func TestProcessResultRelaysFullAssistantResponseWhenCommentIsTruncatedWithEllipsis(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	fullReport := "Investigation summary: Found that the database connection pool was exhausted due to unclosed rows."
	result := &agent.Result{
		FinalText: fullReport,
		ToolCalls: []agent.ToolCall{
			{
				Name: "comment_on_issue",
				Arguments: map[string]any{
					"comment": "Investigation summary: Found that…",
				},
			},
		},
	}
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}
	got := commentBodies(t, ctx, store, task.ID)
	if len(got) != 1 || got[0] != fullReport {
		t.Fatalf("conversation = %q, want full report %q", got, fullReport)
	}
}

// TestProcessResultRelaysFullAssistantResponseWhenCommentEndsWithDots tests
// that comment_on_issue ending in ASCII dots "..." also triggers the fallback to FinalText.
func TestProcessResultRelaysFullAssistantResponseWhenCommentEndsWithDots(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	fullReport := "Summary of findings: All unit tests are passing and the migration completed successfully."
	result := &agent.Result{
		FinalText: fullReport,
		ToolCalls: []agent.ToolCall{
			{
				Name: "comment_on_issue",
				Arguments: map[string]any{
					"comment": "Summary of findings...",
				},
			},
		},
	}
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	got := commentBodies(t, ctx, store, task.ID)
	if len(got) != 1 || got[0] != fullReport {
		t.Fatalf("conversation = %q, want full report %q", got, fullReport)
	}
}

// TestProcessResultPrefersFullAssistantResponseWhenMoreComplete tests that
// even without an ellipsis, a significantly more detailed FinalText is preferred.
func TestProcessResultPrefersFullAssistantResponseWhenMoreComplete(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	fullReport := "Completed analysis.\n\nKey takeaways:\n1. Memory usage stabilized.\n2. Latency improved by 40%."
	result := &agent.Result{
		FinalText: fullReport,
		ToolCalls: []agent.ToolCall{
			{
				Name: "comment_on_issue",
				Arguments: map[string]any{
					"comment": "Completed analysis.",
				},
			},
		},
	}
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	got := commentBodies(t, ctx, store, task.ID)
	if len(got) != 1 || got[0] != fullReport {
		t.Fatalf("conversation = %q, want full report %q", got, fullReport)
	}
}

// TestProcessResultRelaysFinalAssistantTextWhenCommentOnIssueAbsent tests
// that when comment_on_issue is absent, the final assistant text is used as the closing note.
func TestProcessResultRelaysFinalAssistantTextWhenCommentOnIssueAbsent(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	finalReport := "Here is the complete analysis report with all details."
	result := &agent.Result{
		FinalText: finalReport,
		ToolCalls: []agent.ToolCall{
			{Name: "read_file", Text: "contents"},
		},
	}
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}
	got := commentBodies(t, ctx, store, task.ID)
	if len(got) != 1 || got[0] != finalReport {
		t.Fatalf("conversation = %q, want %q", got, finalReport)
	}
}

// TestProcessResultPreservesAttachmentsWhenUsingFinalAssistantText tests that
// attachments specified in comment_on_issue are preserved when FinalText is preferred.
func TestProcessResultPreservesAttachmentsWhenUsingFinalAssistantText(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	fullReport := "See chart for full throughput metrics over the 24 hour period."
	result := &agent.Result{
		FinalText: fullReport,
		ToolCalls: []agent.ToolCall{
			{
				Name: "comment_on_issue",
				Arguments: map[string]any{
					"comment": "See chart…",
					"attachments": []any{map[string]any{
						"filename": "chart.png", "content_type": "image/png", "content": "cG5n",
					}},
				},
			},
		},
	}
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Body != fullReport {
		t.Fatalf("comments = %+v, want body %q", comments, fullReport)
	}
	attachments, err := store.AttachmentMetas(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].Filename != "chart.png" ||
		attachments[0].CommentID == nil || *attachments[0].CommentID != comments[0].ID {
		t.Fatalf("attachments = %+v, want chart.png attached to comment %d", attachments, comments[0].ID)
	}
}

// TestProcessResultCorrectsARunThatProducedNothingToActOn is
// bwsalmon/agents#403: RunDispatch's own outcomeOf calls a run
// "succeeded" the moment the agent makes one harmless tool call, with no
// way to know yet whether that call amounted to a push, a question, or a
// closing comment. A run_command call that touches neither git nor either
// escape hatch is exactly that gap -- ProcessResult must correct
// task_run's own outcome once it has actually ruled all three out, or a
// run that never did anything useful would keep reading "succeeded"
// forever and never count toward FailureStreak's own cap.
func TestProcessResultCorrectsARunThatProducedNothingToActOn(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	runID := "t1-1"
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := store.FinishRun(ctx, runID, baseTime, "succeeded", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "ran something, pushed nothing"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	streak, err := store.FailureStreak(ctx, task.ID)
	if err != nil {
		t.Fatalf("FailureStreak: %v", err)
	}
	if streak == nil || streak.Count != 1 {
		t.Fatalf("failure streak = %+v, want a single counted failure", streak)
	}
	if streak.LastOutcome != "no_action" {
		t.Fatalf("last outcome = %q, want no_action", streak.LastOutcome)
	}
	if streak.LastDetail == "" {
		t.Fatal("last detail is empty, want an explanation a human could read")
	}

	// Still queued, not completed and not failed outright -- one
	// unproductive run is not the cap.
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state = %q, want queued", st)
	}
}

// A handover is relayed as a comment marked as one, and changes nothing
// else about how the run ended. The mark is the whole mechanism: it is
// what lets the next dispatch's prompt carry this comment outside the
// budget it cuts the rest of a long thread to
// (orchestrator.commentThreadSection).
//
// Pinned alongside a run that did nothing else, because that is the
// combination the wording has to survive: a handover is not a closing
// note, so a run that wrote one and pushed nothing has still produced
// nothing anybody asked for and must still reach the ending that says so.
// Completing the task on a handover would be the worse failure of the two
// -- a completed task is not dispatched again, so the summary written for
// the next run would be the last thing ever read on it.
func TestProcessResultRelaysAHandoverWithoutCompletingTheTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name:      "write_handover",
		Arguments: map[string]any{"handover": "  the merge lane may only ever hold one branch  "},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("conversation = %+v, want the one relayed handover", comments)
	}
	if got := comments[0].Body; got != "the merge lane may only ever hold one branch" {
		t.Errorf("handover body = %q, want it relayed trimmed and whole", got)
	}
	if !comments[0].Handover {
		t.Error("the relayed comment is not marked as a handover, so no later prompt can find it")
	}
	// Relayed on the agent's behalf, like every other word a run says:
	// a human reading the thread sees who wrote it.
	if comments[0].Author.OnBehalfOf == nil ||
		comments[0].Author.OnBehalfOf.Kind != model.PrincipalAgent {
		t.Errorf("author = %+v, want grain on the agent's behalf", comments[0].Author)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state = %q, want queued: a handover is not a closing note", st)
	}
}

// The last call wins, which is write_handover's own documented rule and
// the opposite of every other escape hatch here. A handover is a document
// a run revises as it works out what the thread was actually about, and
// the draft it threw away is not the one every later prompt should carry.
func TestProcessResultRelaysTheLastHandoverARunWrote(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(
		agent.ToolCall{Name: "write_handover", Arguments: map[string]any{"handover": "first draft"}},
		agent.ToolCall{
			Name: "write_handover", IsError: true,
			Arguments: map[string]any{"handover": "a call that failed"},
		},
		agent.ToolCall{Name: "write_handover", Arguments: map[string]any{"handover": "what it settled on"}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got := commentBodies(t, ctx, store, task.ID); len(got) != 1 || got[0] != "what it settled on" {
		t.Fatalf("conversation = %q, want only the last handover the run wrote", got)
	}
}

func TestProcessResultSkipsDuplicateHandoverComment(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	first := toolResult(agent.ToolCall{
		Name:      "write_handover",
		Arguments: map[string]any{"handover": "standing summary"},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, first, "t1-1", baseTime); err != nil {
		t.Fatalf("first ProcessResult: %v", err)
	}
	second := toolResult(agent.ToolCall{
		Name:      "write_handover",
		Arguments: map[string]any{"handover": "  standing summary \n"},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, second, "t1-2", baseTime); err != nil {
		t.Fatalf("second ProcessResult: %v", err)
	}
	if got := commentBodies(t, ctx, store, task.ID); len(got) != 1 {
		t.Fatalf("conversation = %q (len %d), want 1 handover comment without duplicate spam", got, len(got))
	}
}

// A handover accompanies whatever else the run did rather than replacing
// it: the endings it matters most on are the ones that leave the task
// open, and a question is the commonest of them. Both are relayed, and
// the task is still parked on the question.
func TestProcessResultRelaysAHandoverAlongsideAQuestion(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(
		agent.ToolCall{Name: "write_handover", Arguments: map[string]any{"handover": "what was settled"}},
		agent.ToolCall{Name: "ask_question", Arguments: map[string]any{"question": "which config file?"}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || !comments[0].Handover || comments[1].Handover {
		t.Fatalf("conversation = %+v, want the handover and then the question", comments)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply: the run still parked on its question", st)
	}
	// And the park points at the question rather than at the handover,
	// which is what a human's reply un-parks (ui.Client.AddComment).
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != comments[1].ID {
		t.Errorf("parked on comment %v, want the question at %d", obs.PendingQuestionCommentID, comments[1].ID)
	}
}

func TestProcessResultFilesAProposedTaskIntoTheStore(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "follow-up work", "body": "do more of this",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var proposal *model.Task
	for i := range tasks {
		if tasks[i].ID != task.ID {
			proposal = &tasks[i]
		}
	}
	if proposal == nil {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.Title != "follow-up work" {
		t.Fatalf("title = %q, want the proposed one", proposal.Title)
	}

	// Unapproved is the whole contract: proposeTaskTool's own doc comment
	// says a human must accept it before the agent set ever attempts it,
	// which the state machine enforces now rather than a withheld label.
	state, err := store.State(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateProposed {
		t.Fatalf("state = %q, want proposed", state)
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ready {
		if id == proposal.ID {
			t.Fatal("a proposed task is dispatchable")
		}
	}
	// And it records which task proposed it, which the issue version had
	// no way to say.
	if len(proposal.Links) != 1 || proposal.Links[0].Kind != model.LinkProposedBy ||
		proposal.Links[0].Target != task.ID {
		t.Fatalf("links = %+v, want proposed-by the task that asked", proposal.Links)
	}
}

// A proposal joins the backlog at the end of it, the same place a task a
// human files joins by default -- not at OrderKey's zero value, which is
// a position too (ahead of everything filed since the backlog started)
// rather than the absence of one, and which used to put an agent's
// afterthought in front of work already waiting.
func TestProcessResultFilesProposedTasksAtTheEndOfTheBacklog(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := placeInBacklog(t, ctx, store, filedTask(t, ctx, store, "t1", repo), 0)
	waiting := placeInBacklog(t, ctx, store, filedTask(t, ctx, store, "t2", repo), 100)

	result := toolResult(
		agent.ToolCall{
			Name:      "propose_task",
			Arguments: map[string]any{"title": "first", "body": "the spec"},
		},
		agent.ToolCall{
			Name:      "propose_task",
			Arguments: map[string]any{"title": "second", "body": "then the thing"},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposals := proposalsByTitle(t, ctx, store, task.ID)
	first, ok := proposals["first"]
	if !ok {
		t.Fatalf("the first proposal was never filed: %v", proposals)
	}
	second, ok := proposals["second"]
	if !ok {
		t.Fatalf("the second proposal was never filed: %v", proposals)
	}
	if first.OrderKey <= waiting.OrderKey {
		t.Errorf("first proposal's order key = %v, want past the task already queued (%v)",
			first.OrderKey, waiting.OrderKey)
	}
	// Behind each other in call order, not sharing one key with the id
	// left to break the tie.
	if second.OrderKey <= first.OrderKey {
		t.Errorf("second proposal's order key = %v, want past the first (%v)",
			second.OrderKey, first.OrderKey)
	}
	want := []string{task.ID, waiting.ID, first.ID, second.ID}
	if got := backlogIDs(t, ctx, store); !slices.Equal(got, want) {
		t.Errorf("backlog = %v, want the proposals at the end of it: %v", got, want)
	}
}

// NewestFirst is where new work joins the backlog, not where a human's
// own new work joins: a deployment that has asked for new tasks at the
// front gets its proposals there too, the same as ui.CreateTask.
func TestProcessResultFilesProposedTasksAtTheFrontUnderNewestFirst(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := placeInBacklog(t, ctx, store, filedTask(t, ctx, store, "t1", repo), 0)
	if err := store.PutConfig(ctx, model.Config{NewestFirst: true}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name:      "propose_task",
		Arguments: map[string]any{"title": "follow-up work", "body": "do more of this"},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.OrderKey >= task.OrderKey {
		t.Errorf("proposal's order key = %v, want ahead of the task that proposed it (%v)",
			proposal.OrderKey, task.OrderKey)
	}
}

// TestProcessResultProposedTaskInheritsAutoMergeFromItsParent covers
// bwsalmon/agents#345: a task proposed by an auto-merge job should
// itself be an auto-merge job, since propose_task's own input schema has
// no way to request a capability the parent lacked -- see
// model.GrantsSubsetOf.
func TestProcessResultProposedTaskInheritsAutoMergeFromItsParent(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 auto-merge: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "follow-up work", "body": "do more of this",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var proposal *model.Task
	for i := range tasks {
		if tasks[i].ID != task.ID {
			proposal = &tasks[i]
		}
	}
	if proposal == nil {
		t.Fatal("the proposed task was never filed")
	}
	if !proposal.AutoMerge {
		t.Error("AutoMerge = false, want true -- an auto-merge job's own proposal should inherit it")
	}

	// And it inherits the approval with it (model.AutoSubmits): a task
	// whose own commits merge unread is trusted to queue work of the
	// same shape on the same branch, so a click nobody would withhold is
	// not asked for.
	state, err := store.State(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Fatalf("state = %q, want queued -- an auto-merge job's proposal is filed approved", state)
	}
	// Approved by policy rather than by anybody: an automation actor
	// with no OnBehalfOf, which is what keeps "who let this run?"
	// answerable off the task alone.
	if proposal.Approval == nil || proposal.Approval.Actor != model.AutoSubmitPrincipal ||
		proposal.Approval.OnBehalfOf != nil {
		t.Errorf("approval = %+v, want %+v acting for nobody", proposal.Approval, model.AutoSubmitPrincipal)
	}
	if proposal.ApprovedAt == nil {
		t.Error("ApprovedAt is unset on an approved proposal")
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ready, proposal.ID) {
		t.Errorf("ready = %v, want the proposal among it -- nothing else is holding it back", ready)
	}
}

// The gate the test above passes is the parent's own permission, so a
// proposal that steps outside it is filed proposed like any other, even
// though the task making it auto-merges: landing on a branch nobody
// granted (model.LandsWhereGranted) is work a human has to look at
// before it starts, not only work whose pull request they read.
func TestProcessResultWithholdsApprovalFromAProposalOnAnotherBranch(t *testing.T) {
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	task.Base = "release/2.0"
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 an auto-merge job on a release branch: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name:      "propose_task",
		Arguments: map[string]any{"title": "follow-up work", "body": "do more of this"},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	state, err := store.State(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateProposed {
		t.Fatalf("state = %q, want proposed -- this proposal lands where nobody granted anything", state)
	}
}

// Opting a proposal out of the merge queue is not opting it out of the
// queue: auto_merge: false says a human should read that proposal's own
// pull request, which is a different question from who queues the work
// (model.AutoSubmits' own doc comment). A run that had to choose between
// the two would be paying for a review with a round trip through a
// person who has nothing to decide yet.
func TestProcessResultQueuesAProposalThatDeclinedAutoMerge(t *testing.T) {
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 auto-merge: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "something else entirely", "body": "separate work", "auto_merge": false,
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["something else entirely"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.AutoMerge {
		t.Error("AutoMerge = true on a proposal that asked not to be one")
	}
	state, err := store.State(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Fatalf("state = %q, want queued -- declining auto-merge says nothing about approval", state)
	}
}

func TestProcessResultQueuesProposalFromAutoApproveProposalsTaskWithoutAutoMerge(t *testing.T) {
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = false
	task.AutoApproveProposals = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 auto-approve-proposals: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "audit follow-up", "body": "fix found issue",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["audit follow-up"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.AutoMerge {
		t.Error("AutoMerge = true, want false -- parent task does not auto-merge")
	}
	state, err := store.State(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Fatalf("state = %q, want queued -- parent has auto-approve-proposals enabled", state)
	}
	if proposal.Approval == nil || proposal.Approval.Actor != model.AutoSubmitPrincipal {
		t.Errorf("approval = %+v, want AutoSubmitPrincipal", proposal.Approval)
	}
}

// The permission is not the whole of it: a proposal inside it still has
// to be inside the bounds on how far it may propagate
// (model.AutoSubmitLimits). Two levels of self-approved work below the
// last person who approved anything is the default limit, so the third
// is filed for a human -- and says so, since a proposal that would have
// been queued and was not leaves whoever reads it needing to know they
// are now the decision.
func TestProcessResultWithholdsApprovalPastTheAutoSubmitDepth(t *testing.T) {
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// A chain a human started and nobody has looked at since: the task
	// making this proposal is the second self-approved level below them.
	root := filedTask(t, ctx, store, "t1", repo)
	chain := root.ID
	for _, id := range []string{"t2", "t3"} {
		task := filedTask(t, ctx, store, id, repo)
		task.AutoMerge = true
		task.Approval = &model.Attribution{Actor: model.AutoSubmitPrincipal}
		task.Links = []model.Link{{Kind: model.LinkProposedBy, Target: chain}}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("filing %s: %v", id, err)
		}
		chain = id
	}
	deepest, err := store.GetTask(ctx, chain)
	if err != nil || deepest == nil {
		t.Fatalf("reading the deepest task back: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name:      "propose_task",
		Arguments: map[string]any{"title": "still more work", "body": "and more after that"},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, *deepest, result, chain+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, chain)["still more work"]
	if !ok {
		t.Fatal("the proposed task was never filed -- a limit costs a click, not the work")
	}
	state, err := store.State(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateProposed {
		t.Fatalf("state = %q, want proposed -- this is a third level of self-approved work", state)
	}
	if !strings.Contains(proposal.Body, "levels of self-approved work") {
		t.Errorf("the proposal's body does not say why it is waiting:\n%s", proposal.Body)
	}
	// The other half of the inheritance is untouched: whether this
	// proposal's own pull request is read is a different question from
	// who queues the work, and the human now being asked to queue it can
	// see the answer on the proposal in front of them.
	if !proposal.AutoMerge {
		t.Error("AutoMerge = false -- the bound is on the approval, not on the merge queue")
	}
}

// The second bound, on the same terms: one task may have so many
// proposals queued unreviewed, and the next is filed for a person.
func TestProcessResultWithholdsApprovalPastTheAutoSubmitFanOut(t *testing.T) {
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 auto-merge: %v", err)
	}

	// One more call than the deployment's fan-out allows, all in one
	// run: the cap counts the siblings this loop has already filed, so
	// the last one is the one that waits.
	var calls []agent.ToolCall
	titles := make([]string, 0, model.DefaultAutoSubmitFanOut+1)
	for i := range model.DefaultAutoSubmitFanOut + 1 {
		title := "piece " + string(rune('a'+i))
		titles = append(titles, title)
		calls = append(calls, agent.ToolCall{
			Name:      "propose_task",
			Arguments: map[string]any{"title": title, "body": "a piece of the same job"},
		})
	}
	if err := orchestrator.ProcessResult(ctx, store, client, task, toolResult(calls...),
		"t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposals := proposalsByTitle(t, ctx, store, task.ID)
	for i, title := range titles {
		proposal, ok := proposals[title]
		if !ok {
			t.Fatalf("%q was never filed", title)
		}
		state, err := store.State(ctx, proposal.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := model.StateQueued
		if i == len(titles)-1 {
			want = model.StateProposed
		}
		if state != want {
			t.Errorf("%q: state = %q, want %q", title, state, want)
		}
	}
	if body := proposals[titles[len(titles)-1]].Body; !strings.Contains(body, "which is the limit") {
		t.Errorf("the proposal over the cap does not say why it is waiting:\n%s", body)
	}
}

// TestProcessResultProposedTaskDoesNotInheritAutoMergeFromANonAutoMergeParent
// is TestProcessResultFilesAProposedTaskIntoTheStore's own default case,
// made explicit: a task proposed by a job that was not itself an
// auto-merge job stays out of the merge queue.
func TestProcessResultProposedTaskDoesNotInheritAutoMergeFromANonAutoMergeParent(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "follow-up work", "body": "do more of this",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var proposal *model.Task
	for i := range tasks {
		if tasks[i].ID != task.ID {
			proposal = &tasks[i]
		}
	}
	if proposal == nil {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.AutoMerge {
		t.Error("AutoMerge = true, want false -- nothing here opted this proposal into the merge queue")
	}
}

// An auto-merge job that works against a base branch of its own passes
// that on to nothing: a proposal is filed against the repo's default
// branch, and the human who let one branch take unreviewed commits said
// nothing about the trunk (model.SameBranch). The proposal says so in
// its body rather than arriving as ordinary work with a decision quietly
// made about it.
// A propose_task call that tacks a write target onto an ordinary
// same-branch follow-up -- here the configuration repo -- is the first
// hop of #2378's chain: the proposal is filed with what it asked for,
// but neither queued nor auto-merged on its parent's permission, and
// its body says why.
func TestProcessResultWithholdsApprovalFromAProposalWithUngrantedWrites(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 auto-merge: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "follow-up work", "body": "do more of this", "auto_merge": true,
			"writes": []any{map[string]any{"repo": "grain/config", "base": "main"}},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.AutoMerge {
		t.Error("AutoMerge = true on a proposal declaring a write target its parent was never given")
	}
	state, err := store.State(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateProposed {
		t.Errorf("state = %q, want proposed -- a person has to approve the extra write target", state)
	}
	if !strings.Contains(proposal.Body, "grain/config") || strings.Contains(proposal.Body, "lands on") {
		t.Errorf("proposal body does not say which write target held it back: %q", proposal.Body)
	}
}

// A propose_task call naming a read-only repository its parent could not
// fetch is filed with it, but waits for a person rather than being
// queued on the auto-merge parent's permission -- the git proxy would
// otherwise let the child clone it with nobody having agreed to that.
// Its own auto-merge is untouched: reads are not where it lands.
func TestProcessResultWithholdsApprovalFromAProposalWithUngrantedReads(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	task.Reads = []model.RepoRef{{Owner: "acme", Name: "docs"}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 auto-merge: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "follow-up work", "body": "do more of this", "auto_merge": true,
			"reads": []any{"acme/docs", "acme/private"},
		},
	}, agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "granted follow-up", "body": "and this", "reads": []any{"acme/docs"},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposals := proposalsByTitle(t, ctx, store, task.ID)
	proposal, ok := proposals["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	state, err := store.State(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateProposed {
		t.Errorf("state = %q, want proposed -- a person has to approve the extra read access", state)
	}
	if len(proposal.Reads) != 2 {
		t.Errorf("Reads = %v, want both repos the run asked for, filed as asked", proposal.Reads)
	}
	if !strings.Contains(proposal.Body, "acme/private") || strings.Contains(proposal.Body, "`acme/docs`") {
		t.Errorf("proposal body does not name exactly the read that held it back: %q", proposal.Body)
	}
	if !proposal.AutoMerge {
		t.Error("AutoMerge = false; an ungranted read should not change where the proposal lands")
	}

	granted, ok := proposals["granted follow-up"]
	if !ok {
		t.Fatal("the granted proposal was never filed")
	}
	if state, err := store.State(ctx, granted.ID); err != nil {
		t.Fatal(err)
	} else if state == model.StateProposed {
		t.Error("a proposal reading only what its parent reads was not queued")
	}
}

func TestProcessResultWithholdsAutoMergeFromAProposalOnAnotherBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	task.Base = "release/2.0"
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 an auto-merge job on a release branch: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name:      "propose_task",
		Arguments: map[string]any{"title": "follow-up work", "body": "do more of this"},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.AutoMerge {
		t.Error("AutoMerge = true, want false -- this proposal lands on a branch its parent was told nothing about")
	}
	if !strings.Contains(proposal.Body, "release/2.0") {
		t.Errorf("proposal body does not say why auto-merge was withheld: %q", proposal.Body)
	}
	// The agent's own words are still the top of the body.
	if !strings.HasPrefix(proposal.Body, "do more of this") {
		t.Errorf("proposal body no longer starts with what the run wrote: %q", proposal.Body)
	}
}

// A task with more than one writeable repo reaches the others by
// proposing work for them (model.WriteTarget): propose_task's `repo`
// names one, and grain files the proposal against it, on the branch the
// human declared with it -- not against the repo the run was working in
// and not on that repo's default branch.
func TestProcessResultFilesAProposalAgainstADeclaredWriteRepo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "acme", Name: "widgets-state"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	task.Writes = []model.WriteTarget{{Repo: other, Base: "deploy"}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("giving t1 a second write target: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "update the deployment", "body": "bump the image tag",
			"repo": "acme/widgets-state",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["update the deployment"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.Target == nil || !proposal.Target.Same(other) {
		t.Fatalf("target = %v, want the write target the run named", proposal.Target)
	}
	if proposal.Base != "deploy" {
		t.Errorf("base = %q, want the branch declared with that repo", proposal.Base)
	}
	// The whole point of declaring the repo: work grain was told may land
	// there unreviewed still may, or a two-repo auto-merge deployment is
	// no better off than one filing these by hand
	// (model.LandsWhereGranted).
	if !proposal.AutoMerge {
		t.Error("AutoMerge = false, want true -- this proposal lands exactly where its parent was allowed to")
	}
	if strings.Contains(proposal.Body, "grain:") {
		t.Errorf("proposal body explains a decision nobody made: %q", proposal.Body)
	}
}

// A task with multiple branches declared on its own repo can propose tasks
// for any of those branches and inherit auto-merge.
func TestProcessResultFilesAProposalAgainstADeclaredBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.Base = "main"
	task.AutoMerge = true
	task.Writes = []model.WriteTarget{{Repo: repo, Base: "release/1.0"}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("giving t1 a second branch: %v", err)
	}

	result := toolResult(
		agent.ToolCall{
			Name: "propose_task",
			Arguments: map[string]any{
				"title": "backport to release/1.0", "body": "fix bug on release branch",
				"branch": "release/1.0",
			},
		},
		agent.ToolCall{
			Name: "propose_task",
			Arguments: map[string]any{
				"title": "undeclared branch work", "body": "fix bug on release 2.0",
				"branch": "release/2.0",
			},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposals := proposalsByTitle(t, ctx, store, task.ID)
	declared := proposals["backport to release/1.0"]
	if declared.Target == nil || !declared.Target.Same(repo) {
		t.Fatalf("declared target = %v, want %v", declared.Target, repo)
	}
	if declared.Base != "release/1.0" {
		t.Fatalf("declared base = %q, want release/1.0", declared.Base)
	}
	if !declared.AutoMerge {
		t.Errorf("declared proposal AutoMerge = false, want true")
	}
	if strings.Contains(declared.Body, "grain:") {
		t.Errorf("declared proposal body has note: %q", declared.Body)
	}

	undeclared := proposals["undeclared branch work"]
	if undeclared.Target == nil || !undeclared.Target.Same(repo) {
		t.Fatalf("undeclared target = %v, want %v", undeclared.Target, repo)
	}
	if undeclared.Base != "release/2.0" {
		t.Fatalf("undeclared base = %q, want release/2.0", undeclared.Base)
	}
	if undeclared.AutoMerge {
		t.Errorf("undeclared proposal AutoMerge = true, want false")
	}
	if !strings.Contains(undeclared.Body, "auto-merge is a permission for one branch") {
		t.Errorf("undeclared proposal missing branch note: %q", undeclared.Body)
	}
}

// A repository nobody declared on this task, but one this deployment
// plainly works in -- another task is filed against it. That is the case
// the `repo` argument was widened for (grain/task-144): a run reading
// this deployment's own history in its state repository and proposing
// changes to grain's code has no write target to name, and filing those
// against the state repository files work that cannot land. It goes to
// the repository the run named, on that repository's default branch.
func TestProcessResultFilesAProposalAgainstAnotherRepoTheDeploymentWorksIn(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "acme", Name: "gadgets"}
	// The only thing that makes acme/gadgets a repository this
	// deployment works in: a task already filed against it.
	filedTask(t, ctx, store, "t0", other)
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 auto-merge: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "fix the gadget", "body": "the change belongs over there",
			"repo": "acme/gadgets",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["fix the gadget"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.Target == nil || !proposal.Target.Same(other) {
		t.Fatalf("target = %v, want the repo the run named", proposal.Target)
	}
	if proposal.Base != "" {
		t.Errorf("base = %q, want that repo's own default branch", proposal.Base)
	}
	if !strings.Contains(proposal.Body, "acme/gadgets") {
		t.Errorf("proposal body does not say where it landed: %q", proposal.Body)
	}
	// Nobody granted this task anything about acme/gadgets, so the
	// auto-merge stops at the repository boundary and the proposal says
	// so (model.LandsWhereGranted, otherBranchAutoMergeNote).
	if proposal.AutoMerge {
		t.Error("AutoMerge = true, want false -- nothing was granted about the repo it landed in")
	}
}

// The same call under a deployment that restricts its repos: the
// allowlist is the answer, so a repository off it is not one a proposal
// may name however many tasks mention it -- a task filed there would be
// parked before it ever dispatched (ui.(*Client).parkOffAllowlist).
func TestProcessResultRefusesAProposalRepoOffTheTargetRepoList(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "acme", Name: "gadgets"}
	filedTask(t, ctx, store, "t0", other)
	if err := store.SetTargetRepos(ctx, []string{"acme/widgets"}); err != nil {
		t.Fatalf("restricting the deployment to one repo: %v", err)
	}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "fix the gadget", "body": "the change belongs over there",
			"repo": "acme/gadgets",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["fix the gadget"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.Target == nil || !proposal.Target.Same(repo) {
		t.Fatalf("target = %v, want the parent's own repo", proposal.Target)
	}
	if !strings.Contains(proposal.Body, "acme/gadgets") {
		t.Errorf("proposal body does not say which repo the run asked for: %q", proposal.Body)
	}
}

// The same call naming a repo nothing here has ever worked in: filed
// anyway, so the work is not lost, but against the repo the run was
// actually in and with a note saying what happened --
// unresolvedDependencyNote's own bargain, and no auto-merge, since
// nothing about that repo was granted.
func TestProcessResultNotesAProposalForARepoTheTaskWasNeverGiven(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 auto-merge: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "somewhere else", "body": "do a thing over there",
			"repo": "acme/elsewhere",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["somewhere else"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.Target == nil || !proposal.Target.Same(repo) {
		t.Fatalf("target = %v, want the parent's own repo", proposal.Target)
	}
	if !strings.Contains(proposal.Body, "acme/elsewhere") {
		t.Errorf("proposal body does not say which repo the run asked for: %q", proposal.Body)
	}
	if !strings.HasPrefix(proposal.Body, "do a thing over there") {
		t.Errorf("proposal body no longer starts with what the run wrote: %q", proposal.Body)
	}
	// It still auto-merges: it landed where an ordinary proposal from
	// this task lands, which is what that decision has always been about.
	if !proposal.AutoMerge {
		t.Error("AutoMerge = false -- the proposal was filed where an unnamed one would have been")
	}
}

// The note is only for a proposal that would otherwise have auto-merged:
// a run that opted its proposal out itself has had no decision made for
// it, and nothing to explain.
func TestProcessResultDoesNotExplainAutoMergeAProposalOptedOutOf(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	task.Base = "release/2.0"
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 an auto-merge job on a release branch: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "separate work", "body": "unrelated, review it", "auto_merge": false,
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["separate work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.AutoMerge {
		t.Error("AutoMerge = true, want false -- the run asked for false")
	}
	if proposal.Body != "unrelated, review it" {
		t.Errorf("proposal body = %q, want the run's own words alone", proposal.Body)
	}
}

func TestProcessResultMainConfigRepoProposals(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// 1. Task running under team "alpha" trying to propose to grain/config
	alphaTask := filedTask(t, ctx, store, "t-alpha", repo)
	if err := store.PutTask(ctx, alphaTask); err != nil {
		t.Fatal(err)
	}

	resultAlpha := toolResult(
		agent.ToolCall{
			Name: "propose_task",
			Arguments: map[string]any{
				"title": "main config update from alpha",
				"body":  "update main config",
				"repo":  model.ConfigRepo().String(),
			},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, alphaTask, resultAlpha, "t-alpha-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposalsAlpha := proposalsByTitle(t, ctx, store, alphaTask.ID)
	propFromAlpha, ok := proposalsAlpha["main config update from alpha"]
	if !ok {
		t.Fatal("proposal missing")
	}
	// Filed where it asked, into the default team: the proposer's team
	// decides nothing since the single-tenant reset (stage 4c).
	if propFromAlpha.Target == nil || !propFromAlpha.Target.Same(model.ConfigRepo()) {
		t.Fatalf("propFromAlpha target = %v, want %v", propFromAlpha.Target, model.ConfigRepo())
	}

	// 2. Task running under admin team ("default") proposing to grain/config
	adminTask := filedTask(t, ctx, store, "t-admin", repo)
	if err := store.PutTask(ctx, adminTask); err != nil {
		t.Fatal(err)
	}

	resultAdmin := toolResult(
		agent.ToolCall{
			Name: "propose_task",
			Arguments: map[string]any{
				"title": "main config update from admin",
				"body":  "update main config",
				"repo":  model.ConfigRepo().String(),
			},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, adminTask, resultAdmin, "t-admin-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposalsAdmin := proposalsByTitle(t, ctx, store, adminTask.ID)
	propFromAdmin, ok := proposalsAdmin["main config update from admin"]
	if !ok {
		t.Fatal("proposal missing")
	}
	if propFromAdmin.Target == nil || !propFromAdmin.Target.Same(model.ConfigRepo()) {
		t.Fatalf("propFromAdmin target = %v, want %v", propFromAdmin.Target, model.ConfigRepo())
	}
}

// proposalsByTitle collects every task in the store except the one that
// proposed them, keyed by title: ListTasks' own order is the queue's, not
// the order a run made its propose_task calls in, and a title is the only
// thing about a proposal a test knows before it is filed.
func proposalsByTitle(t *testing.T, ctx context.Context, store *model.Store, proposer string) map[string]model.Task {
	t.Helper()
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]model.Task{}
	for _, task := range tasks {
		if task.ID != proposer {
			out[task.Title] = task
		}
	}
	return out
}

// dependsOn is every task id a proposal is blocked on, in link order.
func dependsOn(task model.Task) []string {
	var out []string
	for _, l := range task.Links {
		if l.Kind == model.LinkDependsOn {
			out = append(out, l.Target)
		}
	}
	return out
}

// TestProcessResultResolvesProposedTaskDependencies covers the etiquette
// propose_task's own description asks an agent for: a proposal that has
// to follow other work says so in depends_on, and lands blocked on it
// rather than dispatchable the moment a human approves it.
//
// Both spellings a run has resolve here -- an existing task id (its own,
// the usual case for a piece split out of the work in hand) and the local
// `id` of an earlier propose_task call in the same run -- and a duplicate
// collapses rather than filing the same link twice.
func TestProcessResultResolvesProposedTaskDependencies(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(
		agent.ToolCall{
			Name: "propose_task",
			Arguments: map[string]any{
				"id":         "spec",
				"title":      "write the spec",
				"body":       "the shape first",
				"depends_on": []any{"t1"},
			},
		},
		// "#t1", "grain/task-t1", URL, and "t1" are all the same dependency,
		// and naming it repeatedly is one dependency.
		agent.ToolCall{
			Name: "propose_task",
			Arguments: map[string]any{
				"title":      "build it",
				"body":       "then the thing itself",
				"depends_on": []any{"spec", "#t1", "grain/task-t1", "http://localhost:8080/tasks/t1", "t1"},
			},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposals := proposalsByTitle(t, ctx, store, task.ID)
	spec, ok := proposals["write the spec"]
	if !ok {
		t.Fatalf("the first proposal was never filed: %v", proposals)
	}
	build, ok := proposals["build it"]
	if !ok {
		t.Fatalf("the second proposal was never filed: %v", proposals)
	}

	if got := dependsOn(spec); len(got) != 1 || got[0] != task.ID {
		t.Errorf("first proposal depends on %v, want just the task that proposed it (%s)", got, task.ID)
	}
	// Two, not three: "#t1" and "t1" are the same dependency. Links come
	// back ordered by target rather than in the order the run named them
	// (Store.hydrate), so this checks membership.
	got := dependsOn(build)
	if len(got) != 2 || !slices.Contains(got, spec.ID) || !slices.Contains(got, task.ID) {
		t.Errorf("second proposal depends on %v, want the first proposal (%s) and %s, once each",
			got, spec.ID, task.ID)
	}

	// A resolved dependency is a real block, not a note: neither
	// proposal is dispatchable while the task that proposed them is
	// still open, approval or no approval.
	if !model.IsBlocked(build, map[string]bool{}) {
		t.Error("the second proposal is not blocked by anything")
	}
}

// A GitHub pull request or issue URL is not a task reference, however its
// number reads: model.NormalizeTaskID once took the last segment of any
// URL, so a run naming https://github.com/acme/widgets/pull/12 blocked its
// proposal on task 12 -- and superseding by one closed task 12 outright.
func TestProcessResultDoesNotReadAGitHubURLAsATaskID(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	bystander := filedTask(t, ctx, store, "12", repo)

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title":      "follow the pull request",
			"body":       "after #12 on GitHub merges",
			"depends_on": []any{"https://github.com/acme/widgets/pull/12"},
			"supersedes": "https://github.com/acme/widgets/issues/12",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow the pull request"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	for _, l := range proposal.Links {
		if l.Target == bystander.ID {
			t.Errorf("proposal has a %s link to task %s, which only shares a number with the "+
				"pull request it named", l.Kind, bystander.ID)
		}
	}
	if !strings.Contains(proposal.Body, "https://github.com/acme/widgets/pull/12") {
		t.Errorf("body = %q, want the unresolved pull request URL named in it", proposal.Body)
	}
	if state, err := store.State(ctx, bystander.ID); err != nil || state == model.StateClosed {
		t.Errorf("task %s is %q (%v) after a supersede naming a GitHub issue URL, want it left open",
			bystander.ID, state, err)
	}
}

// An entry that names nothing grain can find is kept where a human will
// see it rather than dropped: a proposal filed silently unblocked is one
// an approver has no reason to look twice at.
func TestProcessResultKeepsAnUnresolvableDependencyInTheProposalBody(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title":      "follow-up work",
			"body":       "do more of this",
			"depends_on": []any{"no-such-task"},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if deps := dependsOn(proposal); len(deps) != 0 {
		t.Errorf("depends on %v, want nothing -- a link to a task that does not exist never unblocks", deps)
	}
	if !strings.Contains(proposal.Body, "no-such-task") {
		t.Errorf("body = %q, want the unresolved dependency named in it", proposal.Body)
	}
}

// A standing chat session's own task is never finished, so a depends-on
// link naming it is a link nothing can ever release: grain/task-487,
// where the director filed eight tasks depending on its own session and
// every one of them sat queued and undispatchable. The link is refused
// the way a link to a task that was never real is refused, and for the
// same stated reason, with the ordering the run meant written into the
// body where the approver reads it.
func TestProcessResultRefusesADependencyOnATaskThatNeverCompletes(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	session := filedTask(t, ctx, store, "director-task", repo)
	session.Tags = []string{model.ConsoleSessionTag(model.DirectorSessionID)}
	if err := store.PutTask(ctx, session); err != nil {
		t.Fatalf("filing the session's task: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title":      "follow-up work",
			"body":       "do more of this",
			"depends_on": []any{session.ID, task.ID},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	// The other entry is a real task that really does finish, and is
	// untouched: one refused entry does not cost the proposal the
	// ordering it got right.
	if deps := dependsOn(proposal); len(deps) != 1 || deps[0] != task.ID {
		t.Errorf("depends on %v, want just %s -- a session's task never closes, so a link to it never unblocks",
			deps, task.ID)
	}
	if !strings.Contains(proposal.Body, session.ID) {
		t.Errorf("body = %q, want the refused dependency named in it", proposal.Body)
	}
}

// The same session's task once it has been closed and replaced by a
// fresh conversation is an ordinary dependency: it completed, once, and
// a blocking link is released by exactly that. The refusal is about a
// task nothing will ever finish, not about the kind of task it is.
func TestProcessResultAllowsADependencyOnAClosedSessionTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	session := filedTask(t, ctx, store, "old-director-task", repo)
	session.Tags = []string{model.ConsoleSessionTag(model.DirectorSessionID)}
	if err := store.PutTask(ctx, session); err != nil {
		t.Fatalf("filing the session's task: %v", err)
	}
	closedAt := baseTime
	if err := store.Observe(ctx, model.Observation{TaskID: session.ID, ClosedAt: &closedAt}); err != nil {
		t.Fatalf("closing the session's task: %v", err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title":      "follow-up work",
			"body":       "do more of this",
			"depends_on": []any{session.ID},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if deps := dependsOn(proposal); len(deps) != 1 || deps[0] != session.ID {
		t.Errorf("depends on %v, want %s -- a closed task is a dependency already met, not one that never is",
			deps, session.ID)
	}
}

// grain/task-105: a run can say what its proposal is for on this
// deployment's own declared fields, so a follow-up split out of work for
// one customer is filed as being for that customer rather than blank in
// the column the humans here sort by. The values go through the gate a
// human's own filing goes through, which is what canonicalizes "  acme "
// and " YES".
func TestProcessResultFilesAProposedTaskWithItsCustomFieldValues(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	for _, f := range []model.CustomField{
		{Name: "customer"},
		{Name: "escalated", Type: model.CustomFieldBool},
		{Name: "components", Multiple: true},
	} {
		if err := store.PutCustomField(ctx, f); err != nil {
			t.Fatal(err)
		}
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "follow-up work",
			"body":  "do more of this",
			"custom_fields": map[string]any{
				"customer":  "  acme ",
				"escalated": " YES",
				// A field declared to hold a set takes an array here,
				// which is the shape the task API takes from a human
				// too (grain/task-106, ui.CustomFieldValue) -- the
				// prompt's own field list says so, since it reads such
				// a field out as one comma-joined line.
				"components": []any{"store", " prompt "},
			},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if !slices.Equal(proposal.CustomFields["customer"], []string{"acme"}) ||
		!slices.Equal(proposal.CustomFields["escalated"], []string{"true"}) {
		t.Errorf("custom fields = %v, want them canonicalized by their declarations", proposal.CustomFields)
	}
	if !slices.Equal(proposal.CustomFields["components"], []string{"prompt", "store"}) {
		t.Errorf("custom fields = %v, want the whole set the run asked for", proposal.CustomFields)
	}
}

// A field nobody declares, or a value its declaration does not allow, is
// dropped and named in the body rather than failing the relay -- the run
// is over, there is nobody to hand an error to, and a proposal lost over
// one argument is worse than one filed with a note (the same call
// unresolvedDependencyNote makes).
func TestProcessResultNotesACustomFieldValueItCouldNotSet(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	if err := store.PutCustomField(ctx, model.CustomField{
		Name: "priority", Type: model.CustomFieldEnum, Options: []string{"low", "high"},
	}); err != nil {
		t.Fatal(err)
	}

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "follow-up work",
			"body":  "do more of this",
			"custom_fields": map[string]any{
				"priority": "urgent",
				"customer": "acme",
			},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if len(proposal.CustomFields) != 0 {
		t.Errorf("custom fields = %v, want neither of them recorded", proposal.CustomFields)
	}
	if !strings.Contains(proposal.Body, "priority") || !strings.Contains(proposal.Body, "customer") {
		t.Errorf("body = %q, want both fields it could not set named in it", proposal.Body)
	}
}

// TestProcessResultDropsANetworkAccessGrainCannotBuild: a proposal
// relayed with a network_access outside model.NetworkAccesses must not
// be filed carrying it, since every sandbox backend would dispatch it
// with an open network. It is filed with no override and a note saying
// what was asked for.
func TestProcessResultDropsANetworkAccessGrainCannotBuild(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(
		agent.ToolCall{
			Name: "propose_task",
			Arguments: map[string]any{
				"title": "restricted work", "body": "do more of this", "network_access": "restricted",
			},
		},
		agent.ToolCall{
			Name: "propose_task",
			Arguments: map[string]any{
				"title": "offline work", "body": "do more of this", "network_access": "none",
			},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposals := proposalsByTitle(t, ctx, store, task.ID)
	restricted, ok := proposals["restricted work"]
	if !ok {
		t.Fatal("the proposal asking for restricted was never filed")
	}
	if restricted.NetworkAccess != "" {
		t.Errorf("network access = %q, want no override recorded", restricted.NetworkAccess)
	}
	if !strings.Contains(restricted.Body, `"restricted"`) {
		t.Errorf("body = %q, want the access it could not set named in it", restricted.Body)
	}
	offline, ok := proposals["offline work"]
	if !ok {
		t.Fatal("the proposal asking for none was never filed")
	}
	if offline.NetworkAccess != model.NetworkNone {
		t.Errorf("network access = %q, want %q kept", offline.NetworkAccess, model.NetworkNone)
	}
}

// TestProcessResultProposedTaskCanDeclineInheritedAutoMerge is the other
// half of bwsalmon/agents#345's inheritance: a run that judges a proposal
// to be separate work rather than a piece of its own task says so with
// auto_merge, and that proposal gets a human's review even though the job
// that proposed it did not need one.
func TestProcessResultProposedTaskCanDeclineInheritedAutoMerge(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("marking t1 auto-merge: %v", err)
	}

	result := toolResult(
		agent.ToolCall{
			Name: "propose_task",
			Arguments: map[string]any{
				"title":      "the rest of this task",
				"body":       "a piece of the same work",
				"auto_merge": true,
			},
		},
		agent.ToolCall{
			Name: "propose_task",
			Arguments: map[string]any{
				"title":      "something else entirely",
				"body":       "separate work",
				"auto_merge": false,
			},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposals := proposalsByTitle(t, ctx, store, task.ID)
	piece, ok := proposals["the rest of this task"]
	if !ok {
		t.Fatalf("the first proposal was never filed: %v", proposals)
	}
	separate, ok := proposals["something else entirely"]
	if !ok {
		t.Fatalf("the second proposal was never filed: %v", proposals)
	}
	if !piece.AutoMerge {
		t.Error("AutoMerge = false on a piece of an auto-merge task that asked for it")
	}
	if separate.AutoMerge {
		t.Error("AutoMerge = true on a proposal that asked not to be one")
	}
}

// auto_merge cannot raise a proposal above the job that made it: a run
// whose own task a human left reviewable cannot hand its successor the
// unreviewed merge it was denied.
func TestProcessResultProposedTaskCannotGrantItselfAutoMerge(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "follow-up work", "body": "do more of this", "auto_merge": true,
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["follow-up work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.AutoMerge {
		t.Error("AutoMerge = true, want false -- a run cannot grant its proposal what its own task lacked")
	}
}

func TestProposedTaskReceivesSandboxShapeAndNetworkAccess(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title":             "resource intensive work",
			"body":              "run large benchmarks",
			"sandbox_cpus":      float64(8),
			"sandbox_memory_mb": float64(16384),
			"sandbox_disk_gb":   float64(60),
			"network_access":    "none",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["resource intensive work"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	if proposal.SandboxCPUs != 8 || proposal.SandboxMemoryMB != 16384 || proposal.SandboxDiskGB != 60 {
		t.Errorf("sandbox shape = (%d, %d, %d), want (8, 16384, 60)",
			proposal.SandboxCPUs, proposal.SandboxMemoryMB, proposal.SandboxDiskGB)
	}
	if proposal.NetworkAccess != model.NetworkNone {
		t.Errorf("networkAccess = %q, want %q", proposal.NetworkAccess, model.NetworkNone)
	}
}

// A run that pushed a branch and then failed must still get its pull
// request opened. This is the failure that produced grain/task-1 on
// bwsalmon/grain: the agent edited, committed and pushed, then spent the
// rest of its turn budget and the framework returned an error. RunCycle
// treated the error as "nothing happened", skipped ProcessResult
// entirely, recreated the sandbox, and left a real branch on GitHub with
// no pull request, no comment, and nothing pointing at it -- while the
// task went back to the queue to do the same work again.
func TestRunCycleOpensAPullRequestForABranchAFailedRunAlreadyPushed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	// The shape of a real max-turns ending: the work is on GitHub, and
	// the framework reports the failure with what it managed to do first
	// (see agent.Framework's own contract).
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))
	ranOutOfTurns := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{ToolCalls: []agent.ToolCall{
			{Name: "run_command", Text: "pushed"},
			{Name: "run_command", Text: "ok"},
		}}, errors.New("gemini: exceeded max turns (2) without a final answer")
	})

	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(ranOutOfTurns),
		MaxWorkers: 1,
	}

	err := orchestrator.RunCycle(ctx, deps, baseTime)
	if err == nil {
		t.Fatal("expected RunCycle to report the framework's failure")
	}
	if !strings.Contains(err.Error(), "exceeded max turns") {
		t.Errorf("error = %v, want the framework's own diagnosis preserved", err)
	}

	if len(sim.PullRequests) != 1 {
		t.Fatalf("pull requests = %+v, want one for the branch the run pushed before it failed", sim.PullRequests)
	}
	if sim.PullRequests[0].Head != model.BranchName(task.ID) {
		t.Errorf("pull request = %+v, want head %s", sim.PullRequests[0], model.BranchName(task.ID))
	}

	// The run itself stays failed with the real reason: salvaging the
	// branch does not turn a run that ran out of turns into a success,
	// and the detail now names what it spent them on.
	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	if runs[0].Outcome != "failed" {
		t.Errorf("outcome = %q, want failed", runs[0].Outcome)
	}
	if !strings.Contains(runs[0].Detail, "exceeded max turns") ||
		!strings.Contains(runs[0].Detail, "2 tool call(s)") {
		t.Errorf("detail = %q, want both the failure and what the run did first", runs[0].Detail)
	}
}

// A chat run parks its own task while it is still running: the chat tool
// posts a message and points the observation at it as it goes
// (ui.Client.PostAgentMessage), so a session whose last word went
// unanswered is already parked by the time ProcessResult looks.
//
// Two endings would otherwise be wrong about it, and both are asserted
// here. CompletedAt outranks a pending question in model.StateOf, so a
// run that also left a closing note would mark a task complete while it
// waits for an answer -- and a completed task is not dispatched by a
// reply, so the conversation would die there. And a session that talked
// and pushed nothing is not "a run that produced nothing to act on".
func TestProcessResultLeavesAChatParkedTaskWaiting(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	// What the chat tool leaves behind: the run's own message, and the
	// task pointed at it.
	agentPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: task.ID}
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &agentPrincipal,
		},
		Body: "which did you mean?", CreatedAt: baseTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
	}); err != nil {
		t.Fatal(err)
	}

	result := toolResult(
		agent.ToolCall{Name: "chat", Text: "which did you mean?"},
		agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "here is where I got to"}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Errorf("state = %v, want the task still waiting on the person it asked", st)
	}
	// The closing note is still relayed -- the words are the run's own and
	// belong in the conversation -- it just does not complete the task.
	if bodies := commentBodies(t, ctx, store, task.ID); !slices.Contains(bodies, "here is where I got to") {
		t.Errorf("comments = %q, want the closing note relayed anyway", bodies)
	}
}

func TestProcessResultFilesProposedTaskWithDeclarativeParametersAndTemplates(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	tmpl := model.Template{
		ID:            "audit-template",
		Name:          "Security Audit",
		Title:         "Audit {{service}}",
		Body:          "Perform full audit for {{service}} on {{tier}} tier",
		NetworkAccess: model.NetworkNone,
		Fields: []model.TemplateField{
			{Name: "service", Required: true},
			{Name: "tier", Default: "standard"},
		},
	}
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatal(err)
	}

	result := toolResult(
		agent.ToolCall{
			Name: "propose_task",
			Arguments: map[string]any{
				"template_id":       "audit-template",
				"template_fields":   map[string]any{"service": "auth"},
				"at_front":          true,
				"no_repo":           true,
				"sandbox_cpus":      4,
				"sandbox_memory_mb": 8192,
				"sandbox_disk_gb":   40,
				"model":             "claude-sonnet-5",
				"agent_framework":   "claude",
				"reads":             []any{"acme/docs"},
				"writes":            []any{map[string]any{"repo": "acme/backend", "base": "staging"}},
				"capabilities":      []any{"grain-ci"},
			},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var proposal *model.Task
	for i := range tasks {
		if tasks[i].ID != task.ID {
			proposal = &tasks[i]
		}
	}
	if proposal == nil {
		t.Fatal("proposal was not filed")
	}

	if proposal.Title != "Audit auth" {
		t.Errorf("title = %q, want 'Audit auth'", proposal.Title)
	}
	if !strings.Contains(proposal.Body, "Perform full audit for auth on standard tier") {
		t.Errorf("body = %q, want interpolated body", proposal.Body)
	}
	if proposal.Target != nil {
		t.Errorf("target = %v, want nil for no_repo: true", proposal.Target)
	}
	if proposal.SandboxCPUs != 4 || proposal.SandboxMemoryMB != 8192 || proposal.SandboxDiskGB != 40 {
		t.Errorf("sizing = cpus:%d, mem:%d, disk:%d; want 4, 8192, 40",
			proposal.SandboxCPUs, proposal.SandboxMemoryMB, proposal.SandboxDiskGB)
	}
	if proposal.NetworkAccess != model.NetworkNone {
		t.Errorf("networkAccess = %q, want %q", proposal.NetworkAccess, model.NetworkNone)
	}
	if proposal.Model != "claude-sonnet-5" || proposal.AgentBackendID != "" || proposal.AgentFramework != "claude" {
		t.Errorf("agent = model:%q, backend:%q, framework:%q; want claude-sonnet-5, no pin, claude",
			proposal.Model, proposal.AgentBackendID, proposal.AgentFramework)
	}
	if len(proposal.Reads) != 1 || proposal.Reads[0].String() != "acme/docs" {
		t.Errorf("reads = %+v, want acme/docs", proposal.Reads)
	}
	if len(proposal.Writes) != 1 || proposal.Writes[0].Repo.String() != "acme/backend" || proposal.Writes[0].Base != "staging" {
		t.Errorf("writes = %+v, want acme/backend@staging", proposal.Writes)
	}
	// grain-ci is the target repository's CI Mode to grant, and a
	// no_repo proposal has none: naming it is passed over.
	for _, g := range proposal.Grants {
		if g.Capability == "grain-ci" {
			t.Errorf("grants = %+v, want no grain-ci grant on a no_repo proposal", proposal.Grants)
		}
	}
}

// A proposal that names `not_before` is filed deferred with that wake
// time (grain/task-1405), so reconcileWake -- not a date in its body that
// nothing reads -- is what decides when it may start. One whose time is
// already past is filed as though it had named none.
func TestProcessResultFilesAProposalScheduledForLaterAsDeferred(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	wake := baseTime.Add(48 * time.Hour)
	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "read the first week", "body": "once it has run",
			"not_before": wake.Format(time.RFC3339),
		},
	}, agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "already due", "body": "any time", "not_before": "2000-01-01",
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]model.Task{}
	for _, tk := range tasks {
		byTitle[tk.Title] = tk
	}
	later, ok := byTitle["read the first week"]
	if !ok {
		t.Fatal("the scheduled proposal was never filed")
	}
	if later.DeferredAt == nil || later.DeferredUntil == nil || !later.DeferredUntil.Equal(wake) {
		t.Fatalf("deferred at %v until %v, want deferred until %v", later.DeferredAt, later.DeferredUntil, wake)
	}
	if state, err := store.State(ctx, later.ID); err != nil || state != model.StateDeferred {
		t.Fatalf("state = %q (%v), want deferred", state, err)
	}
	due, err := store.DueDeferrals(ctx, wake)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0] != later.ID {
		t.Fatalf("due at the wake time = %v, want [%s]", due, later.ID)
	}
	now, ok := byTitle["already due"]
	if !ok {
		t.Fatal("the proposal with a past not_before was never filed")
	}
	if now.DeferredAt != nil || now.DeferredUntil != nil {
		t.Fatalf("a past not_before deferred the task (at %v until %v)", now.DeferredAt, now.DeferredUntil)
	}
}

// TestProcessResultReadsAProposalModelNamingAConnectionAsItsPin: the
// propose_task schema offers `model` as "the model or agent backend", and a
// proposal has no other field to pin a connection with, so a `model` that
// names a configured connection (and is not a model that connection serves)
// is filed as that connection's pin, the way ui.CreateTask reads a request.
func TestProcessResultReadsAProposalModelNamingAConnectionAsItsPin(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	if err := store.PutConfig(ctx, model.Config{AgentBackends: []model.AgentBackend{
		{ID: "conn-claude", Framework: "claude", Models: []string{"claude-sonnet-5"}},
		{ID: "elsewhere-conn", Framework: "claude", Models: []string{"claude-sonnet-5"}, Team: "elsewhere"},
	}}); err != nil {
		t.Fatal(err)
	}

	result := toolResult(
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{"title": "pinned", "body": "b", "model": "conn-claude"}},
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{"title": "chosen", "body": "b", "model": "claude-sonnet-5"}},
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{"title": "other team's", "body": "b", "model": "elsewhere-conn"}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]model.Task{}
	for _, tk := range tasks {
		got[tk.Title] = tk
	}
	if p := got["pinned"]; p.AgentBackendID != "conn-claude" || p.Model != "" {
		t.Errorf("proposal naming a connection: backend %q, model %q; want pinned to conn-claude with no model", p.AgentBackendID, p.Model)
	}
	if p := got["chosen"]; p.AgentBackendID != "" || p.Model != "claude-sonnet-5" {
		t.Errorf("proposal naming a model: backend %q, model %q; want model claude-sonnet-5 with no pin", p.AgentBackendID, p.Model)
	}
	// A connection another team saved is still one of the deployment's,
	// so it pins like any other (docs/single-tenant-reset.md, stage 1).
	if p := got["other team's"]; p.AgentBackendID != "elsewhere-conn" || p.Model != "" {
		t.Errorf("proposal naming another team's connection: backend %q, model %q; want pinned to elsewhere-conn", p.AgentBackendID, p.Model)
	}
}

// An ordinary interactive task, not only a standing session, keeps
// itself open for a message its run was never shown: a closing answer
// written while the person was typing must not complete the task under
// what they said (grain/task-1774). Once the run has been handed the
// message, the same ending completes it as before.
func TestProcessResultLeavesAnInteractiveTaskReadyForAMessageItsRunNeverSaw(t *testing.T) {
	t.Parallel()
	for _, handed := range []bool{false, true} {
		store, ctx := openStore(t)
		_, client := newSim(t, "acme", "widgets", "main")
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		task := filedTask(t, ctx, store, "t1", repo)
		task.Interactive = true
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{ID: "t1-1", TaskID: task.ID, Attempt: 1, StartedAt: baseTime},
			model.Limits{}); err != nil {
			t.Fatal(err)
		}
		asked, err := store.AddComment(ctx, model.Comment{
			TaskID: task.ID,
			Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			Body:   "put it in a new pane", CreatedAt: baseTime.Add(-time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AdvanceDelivered(ctx, task.ID, asked, baseTime); err != nil {
			t.Fatal(err)
		}
		late, err := store.AddComment(ctx, model.Comment{
			TaskID: task.ID,
			Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			Body:   "oh, and rename it too", CreatedAt: baseTime.Add(time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if handed {
			if err := store.AdvanceDelivered(ctx, task.ID, late, baseTime.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}

		if err := store.FinishRun(ctx, "t1-1", baseTime.Add(2*time.Minute), "succeeded",
			"the agent answered in text without calling a tool"); err != nil {
			t.Fatal(err)
		}
		result := &agent.Result{FinalText: "done -- it is in the new pane"}
		if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1",
			baseTime.Add(2*time.Minute)); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := model.StateQueued
		if handed {
			want = model.StateAwaitingReply
		}
		if st != want {
			t.Errorf("handed=%v: state = %v, want %v", handed, st, want)
		}
		if bodies := commentBodies(t, ctx, store, task.ID); !slices.Contains(bodies, "done -- it is in the new pane") {
			t.Errorf("handed=%v: comments = %q, want the run's answer relayed either way", handed, bodies)
		}
	}
}

// A propose_task call's review_template_id is held to the run's own
// task: the review it names is filed approved and auto-merge on the
// branch the run wrote, so a template carrying a grant the task lacks is
// refused for the repository's default review, with a note saying why,
// and one within the task's grants is kept.
func TestProcessResultRefusesAReviewTemplateBeyondTheProposersGrants(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.Grants = []model.Grant{{Capability: "gcp-key", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for _, tmpl := range []model.Template{
		{ID: "ops", Name: "ops", Title: "ops", Grants: []model.Grant{{Capability: "api:deployment-ops", Via: model.GrantByLabel}}},
		{ID: "keyed", Name: "keyed", Title: "keyed", Grants: []model.Grant{{Capability: "gcp-key", Via: model.GrantByLabel}}},
		{ID: model.DefaultReviewTemplateID(repo), Name: "default", Title: "review"},
	} {
		if err := store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatal(err)
		}
	}

	result := toolResult(agent.ToolCall{
		Name:      "propose_task",
		Arguments: map[string]any{"title": "escalating", "body": "do it", "review_template_id": "ops"},
	}, agent.ToolCall{
		Name:      "propose_task",
		Arguments: map[string]any{"title": "within", "body": "do it", "review_template_id": "keyed"},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	proposals := proposalsByTitle(t, ctx, store, task.ID)
	refused, ok := proposals["escalating"]
	if !ok {
		t.Fatal("the proposal naming a refused review template was never filed")
	}
	if refused.ReviewTemplateID != model.DefaultReviewTemplateID(repo) {
		t.Errorf("ReviewTemplateID = %q, want the repository's default review", refused.ReviewTemplateID)
	}
	if !strings.Contains(refused.Body, "`ops`") || !strings.Contains(refused.Body, "api:deployment-ops") {
		t.Errorf("proposal body does not say why its review template was refused: %q", refused.Body)
	}
	if kept := proposals["within"]; kept.ReviewTemplateID != "keyed" {
		t.Errorf("ReviewTemplateID = %q, want the template within the run's own grants kept", kept.ReviewTemplateID)
	}
}

// An attachment the run gave by path was read out of its sandbox when it
// made the call and staged against the run (model.Store.StageAttachment):
// the finish path, which gets back only the call's arguments, relays the
// staged bytes on the comment -- and one whose path nothing was staged
// under is dropped with a note rather than failing the finish.
func TestProcessResultRelaysAnAttachmentStagedByPath(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	if err := store.StartRun(ctx, model.Run{ID: "t1-1", TaskID: task.ID, Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	sheet := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 150<<10)...)
	if live, err := store.StageAttachment(ctx, task.ID, model.StagedAttachment{
		Path: "screenshots/after/contact-sheet.png", Filename: "contact-sheet.png",
		ContentType: "image/png", Content: sheet, CreatedAt: baseTime,
	}); err != nil || !live {
		t.Fatalf("StageAttachment = %v, %v", live, err)
	}

	result := toolResult(
		agent.ToolCall{Name: "run_command", Text: "pushed"},
		agent.ToolCall{
			Name: "comment_on_issue", Arguments: map[string]any{
				"comment": "fixed it",
				"attachments": []any{
					map[string]any{"path": "screenshots/after/contact-sheet.png"},
					map[string]any{"path": "screenshots/before/contact-sheet.png", "filename": "before.png"},
				},
			},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 || !strings.HasPrefix(bodies[0], "fixed it\n") ||
		!strings.Contains(bodies[0], "attachment `before.png` was dropped: was not read from screenshots/before/contact-sheet.png") {
		t.Fatalf("conversation = %q, want the comment with a note naming the one never staged", bodies)
	}
	attachments, err := store.Attachments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].Filename != "contact-sheet.png" ||
		attachments[0].ContentType != "image/png" || !bytes.Equal(attachments[0].Content, sheet) {
		t.Fatalf("attachments = %d, want the staged contact sheet alone, byte for byte", len(attachments))
	}
}
