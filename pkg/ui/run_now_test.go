package ui_test

// The person's way of saying "work on this one next": Client.RunNow,
// which the UI's "Run now" button and `grain run-now` both come through.
// It is the only action here that overrides the queue's own order and
// the readiness rules rather than feeding them, so what it writes -- and
// what it refuses -- is the whole of how far that override goes.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// TestRunNowAsksForARunAndSaysSo: the request itself, and the note that
// is its only record -- the stamp is cleared by the run it starts, so a
// person reading the task afterwards would otherwise see a run nobody
// appears to have asked for.
func TestRunNowAsksForARunAndSaysSo(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := c.RunNow(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.RunNowAt == nil {
		t.Fatal("RunNowAt = nil, want the request dispatch.Cycle reads")
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.RunNowAt == nil {
		t.Fatal("RunNowAt = nil on the wire, want the pane to be able to say the click took")
	}
	if len(detail.Comments) != 1 {
		t.Fatalf("comments = %d, want the request to say so in the task's own thread", len(detail.Comments))
	}
	if detail.Comments[0].Author != "alice" {
		t.Fatalf("comment author = %q, want the person who asked for it", detail.Comments[0].Author)
	}
}

// The list and a task's own row carry the request too, not only the
// pane: under a pause (manual mode) a request can wait many cycles for
// capacity, and the list's "paused — waits for Run now" chip has to be
// able to tell a task already asked for from one still waiting for it
// (grain/task-927). A task nobody asked for carries none.
func TestRunNowIsOnTheListShape(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	asked := create(t, c, ctx)
	other := create(t, c, ctx)

	if err := c.RunNow(ctx, asked.ID); err != nil {
		t.Fatal(err)
	}

	list, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]ui.Task{}
	for _, row := range list {
		seen[row.ID] = row
	}
	if seen[asked.ID].RunNowAt == nil {
		t.Fatalf("list row for %s: RunNowAt = nil, want the outstanding request", asked.ID)
	}
	if seen[other.ID].RunNowAt != nil {
		t.Fatalf("list row for %s: RunNowAt = %v, want none: nobody asked", other.ID, seen[other.ID].RunNowAt)
	}
	row, err := c.Task(ctx, asked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.RunNowAt == nil {
		t.Fatal("Task(): RunNowAt = nil, want the request on the row POST /run-now answers with")
	}
}

// The second case this exists for: a task whose work is over, its pull
// request sitting on the merge queue, handed back to an agent. The
// completion has to go, or grain goes on treating the task as finished
// -- and the note has to name the branch, because it is what the run's
// own agent is told.
func TestRunNowSendsAMergingTaskBackToAnAgent(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	ref := "acme/widgets#42"
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: ref})
		tk.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completedAt := baseTime.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &completedAt}); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateCompleted {
		t.Fatalf("state = %q, err = %v; want completed before the request", state, err)
	}

	if err := c.RunNow(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.CompletedAt != nil {
		t.Fatalf("CompletedAt = %v, want it cleared: the task is being worked on again", obs.CompletedAt)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateQueued {
		t.Fatalf("state = %q, err = %v; want queued again", state, err)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Comments) != 1 || !strings.Contains(detail.Comments[0].Body, ref) {
		t.Fatalf("comments = %+v, want one naming %s so the run knows which branch to push to", detail.Comments, ref)
	}
}

// A task that has exhausted its retries is one of the states the request
// overrides, and the streak is what would otherwise stop the run: asking
// for a run is asking for the attempt.
func TestRunNowClearsAFailureStreak(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	for i := 0; i < model.MaxConsecutiveFailures; i++ {
		runID := "run-" + strings.Repeat("x", i+1)
		if err := store.StartRun(ctx, model.Run{
			ID: runID, TaskID: task.ID, Attempt: i + 1, StartedAt: baseTime,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, runID, baseTime.Add(time.Minute), "failed", "nope"); err != nil {
			t.Fatal(err)
		}
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateFailed {
		t.Fatalf("state = %q, err = %v; want failed before the request", state, err)
	}

	if err := c.RunNow(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateQueued {
		t.Fatalf("state = %q, err = %v; want the streak cleared with the request", state, err)
	}
}

// An unapproved task is run, not left to fall back to being a proposal
// the moment its run ends -- so the approval is granted rather than
// bypassed. A deferred one is picked back up for the same reason.
func TestRunNowApprovesAndUndefersWhatItStarts(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "later", Description: "not yet"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Defer(ctx, task.ID, ui.DeferOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := c.RunNow(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Approval == nil {
		t.Fatal("approval = nil, want asking for a run to be an approval of it")
	}
	if stored.DeferredAt != nil {
		t.Fatalf("DeferredAt = %v, want the task picked back up", stored.DeferredAt)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateQueued {
		t.Fatalf("state = %q, err = %v; want queued", state, err)
	}
}

// A task parked on a question is started without one: it cannot both be
// dispatched and be waiting for an answer, and the run is the answer
// somebody chose to give.
func TestRunNowUnparksATaskWaitingOnAQuestion(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Body: "which one?", CreatedAt: baseTime,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID: task.ID, PendingQuestionCommentID: &commentID, PendingSecret: "stripe-api-key",
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.RunNow(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID != nil || obs.PendingSecret != "" {
		t.Fatalf("question = %v, secret = %q; want both cleared with the request",
			obs.PendingQuestionCommentID, obs.PendingSecret)
	}
}

// Nor a proposal filed against a repository the agent's own task cannot
// read: propose_task may name any repository, and approving it would be
// the agent reaching one nobody gave it. A follow-up in its own
// repository is approved as usual.
func TestRunNowByAnAgentDoesNotApproveWorkInAnotherRepository(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	requester, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "manual mode", Description: "hand-pick", Repo: "acme/widgets", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: requester.ID})

	elsewhere, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "elsewhere", Description: "for a person", Repo: "acme/secrets",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.RunNow(agentCtx, elsewhere.ID)
	var forbidden *ui.ForbiddenError
	if !errors.As(err, &forbidden) || !strings.Contains(err.Error(), "acme/secrets") {
		t.Fatalf("RunNow by an agent on a proposal in acme/secrets: err = %v, want a refusal naming it", err)
	}
	if stored, err := store.GetTask(ctx, elsewhere.ID); err != nil || stored.Approval != nil {
		t.Fatalf("approval = %+v, err = %v; want the proposal left for a person", stored.Approval, err)
	}

	sameRepo, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "follow-up", Description: "same repo", Repo: "acme/widgets",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RunNow(agentCtx, sameRepo.ID); err != nil {
		t.Fatalf("RunNow by an agent on a proposal in its own repository: %v", err)
	}
}

// Nor an auto-merge task, for an agent whose own task is not one:
// approving it would land its change with nobody reading it, which is
// more than a proposal from that agent could ever have kept (CreateTask
// drops AutoMerge from a proposer that is not an auto-merge job).
func TestRunNowByAnAgentDoesNotApproveAutoMergeItLacks(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	requester, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "manual mode", Description: "hand-pick", Repo: "acme/widgets", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: requester.ID})

	unattended, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "lands itself", Description: "for a person", Repo: "acme/widgets", AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.RunNow(agentCtx, unattended.ID)
	var forbidden *ui.ForbiddenError
	if !errors.As(err, &forbidden) || !strings.Contains(err.Error(), "auto-merge") {
		t.Fatalf("RunNow by a reviewed agent on an auto-merge proposal: err = %v, want a refusal naming auto-merge", err)
	}
	if stored, err := store.GetTask(ctx, unattended.ID); err != nil || stored.Approval != nil {
		t.Fatalf("approval = %+v, err = %v; want the proposal left for a person", stored.Approval, err)
	}

	// An auto-merge job landing on the same branch could have proposed
	// it as auto-merge itself, so it may approve it.
	autoRequester, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "manual mode", Description: "hand-pick", Repo: "acme/widgets", Approved: true, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	autoCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: autoRequester.ID})
	if err := c.RunNow(autoCtx, unattended.ID); err != nil {
		t.Fatalf("RunNow by an auto-merge agent on an auto-merge proposal on its branch: %v", err)
	}
}

// An agent's run_task_now does not un-park a task: the question (or the
// request for a secret) was put to a person, and starting the task anyway
// is answering it in their place.
func TestRunNowByAnAgentLeavesAParkedTaskForAPerson(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	requester := create(t, c, ctx)
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: requester.ID})
	task := create(t, c, ctx)
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Body: "which key?", CreatedAt: baseTime,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID: task.ID, PendingQuestionCommentID: &commentID, PendingSecret: "stripe-api-key",
	}); err != nil {
		t.Fatal(err)
	}

	err = c.RunNow(agentCtx, task.ID)
	var forbidden *ui.ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Fatalf("RunNow by an agent on a parked task: err = %v, want a refusal", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID == nil || obs.PendingSecret == "" || obs.RunNowAt != nil {
		t.Fatalf("question = %v, secret = %q, runNowAt = %v; want the task still parked and no run asked for",
			obs.PendingQuestionCommentID, obs.PendingSecret, obs.RunNowAt)
	}

	if err := c.RunNow(ctx, task.ID); err != nil {
		t.Fatalf("RunNow by a person on the parked task: %v", err)
	}
}

// The two refusals, and they are the two cases where a request would
// mean nothing: a closed task's run is cancelled the moment it starts,
// and a running task has an agent on it already.
func TestRunNowRefusesAClosedTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}

	err := c.RunNow(ctx, task.ID)
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError for a closed task", err)
	}
	if !strings.Contains(err.Error(), "reopen") {
		t.Fatalf("err = %q, want it to say what to do about it", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.RunNowAt != nil {
		t.Fatal("a refused request wrote its stamp anyway")
	}
}

func TestRunNowRefusesARunningTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.StartRun(ctx, model.Run{
		ID: "run-1", TaskID: task.ID, Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	err := c.RunNow(ctx, task.ID)
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError for a task already running", err)
	}
}

func TestRunNowOnAnUnknownTaskIsNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	err := c.RunNow(ctx, "nope")
	var nf *ui.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want a NotFoundError", err)
	}
}

// grain/task-399: an agent's run_task_now approves what it starts, as the
// button does, but never a task asking for more than the agent's own task
// holds. Those are the proposals that wait for a person
// (model.EscalatedCapabilities); approving one through Run now would let a
// run propose itself any grant and then start it.
func TestRunNowByAnAgentDoesNotApproveAnEscalation(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	requester := create(t, c, ctx)
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: requester.ID})

	caps := []string{"self-debug"}
	escalated, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "needs a grant", Description: "for a person", Capabilities: &caps,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.RunNow(agentCtx, escalated.ID)
	var forbidden *ui.ForbiddenError
	if !errors.As(err, &forbidden) || !strings.Contains(err.Error(), "self-debug") {
		t.Fatalf("RunNow by an agent on a proposal asking for self-debug: err = %v, want a refusal naming it", err)
	}
	stored, err := store.GetTask(ctx, escalated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Approval != nil {
		t.Fatalf("approval = %+v, want the escalated proposal left for a person", stored.Approval)
	}

	ordinary, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "plain", Description: "nothing extra"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RunNow(agentCtx, ordinary.ID); err != nil {
		t.Fatalf("RunNow by an agent on a proposal asking for nothing it lacks: %v", err)
	}
	if stored, err := store.GetTask(ctx, ordinary.ID); err != nil || stored.Approval == nil ||
		stored.Approval.Actor.Kind != model.PrincipalAgent {
		t.Fatalf("approval = %+v, err = %v; want it approved as the agent", stored.Approval, err)
	}

	if err := c.RunNow(ctx, escalated.ID); err != nil {
		t.Fatalf("RunNow by a person on the escalated proposal: %v, want the person's approval to stand", err)
	}
}

// An agent's Run now is not limited to its own proposals, so the grants
// it may not approve are not only the ones a proposal names: a directive,
// a folder, a .grain declaration or grain itself can put a capability on
// a task that is still waiting for a person to approve it.
func TestRunNowByAnAgentDoesNotApproveAGrantItDidNotName(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	requester := create(t, c, ctx)
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: requester.ID})

	for _, via := range []model.GrantSource{model.GrantByDirective, model.GrantByFolder, model.GrantByGrain,
		model.GrantSource(model.DeclarationDir + "/templates/x.yml")} {
		task := create(t, c, ctx)
		if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
			t.Grants = append(t.Grants, model.Grant{Capability: "self-debug", Via: via})
			t.Approval = nil
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		err := c.RunNow(agentCtx, task.ID)
		var forbidden *ui.ForbiddenError
		if !errors.As(err, &forbidden) || !strings.Contains(err.Error(), "self-debug") {
			t.Fatalf("RunNow by an agent on a task holding self-debug via %s: err = %v, want a refusal naming it", via, err)
		}
		if stored, err := store.GetTask(ctx, task.ID); err != nil || stored.Approval != nil {
			t.Fatalf("via %s: approval = %+v, err = %v; want it left for a person", via, stored.Approval, err)
		}
	}
}

// priority-dispatch is a person's to give even to an agent whose own task
// holds it (model.IsPersonOnlyGrant): the pipeline steward holds it and
// run_task_now both, and must not be able to propose a task naming it and
// then approve that task through Run now.
func TestRunNowByAnAgentDoesNotApprovePriorityDispatchEvenHoldingIt(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	requester := create(t, c, ctx)
	if err := store.UpdateTask(ctx, requester.ID, func(t *model.Task) error {
		t.Grants = append(t.Grants, model.Grant{Capability: model.PriorityDispatchCapability, Via: model.GrantByGrain})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: requester.ID})

	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Grants = append(t.Grants, model.Grant{Capability: model.PriorityDispatchCapability, Via: model.GrantByLabel})
		t.Approval = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := c.RunNow(agentCtx, task.ID)
	var forbidden *ui.ForbiddenError
	if !errors.As(err, &forbidden) || !strings.Contains(err.Error(), model.PriorityDispatchCapability) {
		t.Fatalf("RunNow by an agent holding priority-dispatch on a proposal naming it: err = %v, want a refusal naming it", err)
	}
	if stored, err := store.GetTask(ctx, task.ID); err != nil || stored.Approval != nil {
		t.Fatalf("approval = %+v, err = %v; want it left for a person", stored.Approval, err)
	}
}

// A task filed from a repository-declared template with a push set lets its
// sandbox push refs beyond its own branch without a pull request
// (Store.GitDeclaredPush). An agent cannot approve an unapproved task carrying
// one through Run now, and an agent filing from that template over the agent
// API does not attach its push set.
func TestRunNowByAnAgentDoesNotApproveADeclaredPushSet(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(c)
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	templateID := model.RepoDeclaredID(model.DeclaredTemplate, target, "release")
	if _, err := store.SyncRepoDeclarations(ctx, target, "abc123", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path:     model.TemplateDeclarationDir + "/release.yml",
			Template: model.Template{ID: templateID, Name: "release", Title: "Cut a release", Body: "Cut it.", Target: &target},
			Push:     model.RefPushSet{{Pattern: "refs/heads/main"}, {Pattern: "refs/tags/v*"}},
		}},
	}, time.Now().UTC(), time.UTC); err != nil {
		t.Fatalf("syncing declarations: %v", err)
	}

	requester, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "steward", Description: "holds dispatch-control",
		Repo: target.String(), Capabilities: &[]string{"api:dispatch-control"}, Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: requester.ID})

	unapprovedRelease, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		TemplateID: templateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.RunNow(agentCtx, unapprovedRelease.ID)
	var forbidden *ui.ForbiddenError
	if !errors.As(err, &forbidden) || !strings.Contains(err.Error(), ".grain/templates/release.yml") {
		t.Fatalf("RunNow by an agent on a task with a declared push set: err = %v, want a refusal naming .grain/templates/release.yml", err)
	}
	if stored, err := store.GetTask(ctx, unapprovedRelease.ID); err != nil || stored.Approval != nil {
		t.Fatalf("approval = %+v, err = %v; want it left for a person", stored.Approval, err)
	}

	code, body := asAgent(t, srv, requester.ID, http.MethodPost, "/api/tasks",
		`{"templateId":"`+templateID+`","creator":"agent"}`, "")
	if code >= 300 {
		t.Fatalf("POST /api/tasks from template as agent: %d %s", code, body)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var agentFiled *model.Task
	for i := range tasks {
		if tasks[i].ID != requester.ID && tasks[i].ID != unapprovedRelease.ID {
			agentFiled = &tasks[i]
		}
	}
	if agentFiled == nil || agentFiled.TemplateID != "" {
		t.Fatalf("agent-filed task = %+v, want TemplateID empty so it carries no declared push set", agentFiled)
	}
}

// Over the agent API, an agent cannot POST /api/pipeline-pause to overwrite a
// pause someone else took (and then DELETE it), nor DELETE its own pause while
// an upgrade is scheduled under it.
func TestAgentAPIPipelinePauseDoesNotOverwriteOrLiftAnotherPauseOrUpgrade(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	seedAPITask(t, client, "1", "acme/widgets", "api:dispatch-control")

	if err := client.Store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: time.Now(), Reason: "operator pause",
		By: model.Principal{Kind: model.PrincipalHuman, ID: "alice"},
	}); err != nil {
		t.Fatal(err)
	}
	if code, body := asAgent(t, srv, "1", http.MethodPost, "/api/pipeline-pause", `{"reason":"take over"}`, ""); code != http.StatusForbidden {
		t.Fatalf("POST /api/pipeline-pause over an operator's pause: %d %s, want 403", code, body)
	}
	if pause, err := client.Store.GetPipelinePause(ctx); err != nil || pause == nil || pause.By.ID != "alice" {
		t.Fatalf("pause after refused POST = %+v, err = %v; want alice's pause intact", pause, err)
	}

	if err := client.Store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: time.Now(), Reason: "manual mode",
		By: model.Principal{Kind: model.PrincipalAgent, ID: "1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.SetScheduledUpgrade(ctx, model.ScheduledUpgrade{
		Branch: "main", RequestedAt: time.Now(), By: model.ScheduledUpgradePrincipal,
	}); err != nil {
		t.Fatal(err)
	}
	if code, body := asAgent(t, srv, "1", http.MethodDelete, "/api/pipeline-pause", ``, "done"); code != http.StatusForbidden {
		t.Fatalf("DELETE /api/pipeline-pause while an upgrade is scheduled: %d %s, want 403", code, body)
	}
}

// An operator's per-task pause (model.TaskPause) holds that task where it is
// until the operator lifts it: an agent's run_task_now or agent-API call may
// neither start nor resume it, nor overwrite who paused it.
func TestRunNowByAnAgentRefusesAnOperatorPausedTask(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	seedAPITask(t, client, "99", "acme/widgets", "api:dispatch-control")

	unapproved, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "paused work", Description: "held by operator", Repo: "acme/widgets",
	})
	if err != nil {
		t.Fatal(err)
	}
	operatorCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "alice"})
	if err := client.Pause(operatorCtx, unapproved.ID, ui.PauseOptions{}); err != nil {
		t.Fatalf("pausing as operator: %v", err)
	}

	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "99"})
	err = client.RunNow(agentCtx, unapproved.ID)
	var forbidden *ui.ForbiddenError
	if !errors.As(err, &forbidden) || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("RunNow by an agent on an operator-paused task: err = %v, want ForbiddenError naming alice", err)
	}
	if stored, err := client.Store.GetTask(ctx, unapproved.ID); err != nil || stored.Approval != nil {
		t.Fatalf("approval after refused RunNow = %+v, err = %v; want paused task untouched", stored.Approval, err)
	}
	if obs, err := client.Store.GetObservation(ctx, unapproved.ID); err != nil || (obs != nil && obs.RunNowAt != nil) {
		t.Fatalf("obs after refused RunNow = %+v, err = %v; want nil RunNowAt", obs, err)
	}

	if code, body := asAgent(t, srv, "99", http.MethodPost, "/api/tasks/"+unapproved.ID+"/run-now", `{}`, ""); code != http.StatusForbidden || !strings.Contains(body, "paused by human alice") {
		t.Fatalf("POST /api/tasks/%s/run-now over agent API while paused: %d %s, want 403 naming alice", unapproved.ID, code, body)
	}
	if code, body := asAgent(t, srv, "99", http.MethodPost, "/api/tasks/"+unapproved.ID+"/resume", `{}`, ""); code != http.StatusForbidden || !strings.Contains(body, "paused by human alice") {
		t.Fatalf("POST /api/tasks/%s/resume over agent API while paused by alice: %d %s, want 403 naming alice", unapproved.ID, code, body)
	}
	if code, body := asAgent(t, srv, "99", http.MethodPost, "/api/tasks/"+unapproved.ID+"/pause", `{"immediate":true}`, ""); code != http.StatusForbidden || !strings.Contains(body, "paused by human alice") {
		t.Fatalf("POST /api/tasks/%s/pause over agent API while paused by alice: %d %s, want 403 naming alice", unapproved.ID, code, body)
	}
	if pause, err := client.Store.GetPause(ctx, unapproved.ID); err != nil || pause == nil || pause.PausedBy.ID != "alice" || pause.Immediate {
		t.Fatalf("pause after refused agent calls = %+v, err = %v; want alice's pause intact", pause, err)
	}
}

// A dangerous api scope is a person's to grant, never an agent's
// (model.EscalatedCapabilities, docs/agent-api.md): an agent holding one
// still cannot approve an unapproved task asking for it through Run now.
func TestRunNowByAnAgentDoesNotApproveADangerousAPIGrantItHolds(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	caps := []string{"api:secrets:write"}
	requester, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "holds a dangerous scope", Description: "granted by a person",
		Capabilities: &caps, Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: requester.ID})

	proposal, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "asks for that dangerous scope", Description: "waits for a person",
		Capabilities: &caps,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.RunNow(agentCtx, proposal.ID)
	var forbidden *ui.ForbiddenError
	if !errors.As(err, &forbidden) || !strings.Contains(err.Error(), "api:secrets:write") {
		t.Fatalf("RunNow by an agent on a task asking for api:secrets:write: err = %v, want a refusal naming it", err)
	}
	if stored, err := store.GetTask(ctx, proposal.ID); err != nil || stored.Approval != nil {
		t.Fatalf("approval = %+v, err = %v; want it left for a person", stored.Approval, err)
	}
}

// Over the agent API (X-Grain-Acting-Task), the acting task is already
// in the request's context: naming a different task in requestedBy to
// borrow its grants on Run now or attribute a pause to it is refused.
func TestRunNowAndPauseOverAgentAPIRefuseAnotherAgentRequester(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:dispatch-control")
	seedAPITask(t, client, "2", "acme/widgets", "self-debug")
	seedAPITask(t, client, "3", "acme/widgets", "self-debug")

	code, body := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/3/run-now",
		`{"requestedBy":"2","requestedByKind":"agent"}`, "")
	if code != http.StatusForbidden {
		t.Fatalf("POST /api/tasks/3/run-now naming task 2 as requester: %d %s, want 403", code, body)
	}
	if stored, err := client.Store.GetTask(context.Background(), "3"); err != nil || stored.Approval != nil {
		t.Fatalf("task 3 approval = %+v, err = %v; want it still unapproved", stored.Approval, err)
	}

	code, body = asAgent(t, srv, "1", http.MethodPost, "/api/pipeline-pause",
		`{"reason":"spoofed","requestedBy":"2","requestedByKind":"agent"}`, "")
	if code != http.StatusForbidden {
		t.Fatalf("POST /api/pipeline-pause naming task 2 as requester: %d %s, want 403", code, body)
	}
}
