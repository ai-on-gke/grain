// bwsalmon/agents#249's own live test: the two flagship scenarios
// tests/e2e/e2e_test.go already proves against manual store.Observe
// calls standing in for "GitHub itself" (that file's own doc comments:
// "v2 has no completion detector of its own yet," "no code in v2 does
// this yet [merges/closes a PR]") -- driven here through RunCycle and a
// real github.Client against githubsim instead, the way
// gitproxy/live_test.go already holds the git-transport half to the
// same discipline.
package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// credentialingSandboxes gives every sandbox a dispatch builds a git
// identity, at the moment a real deployment does -- as part of preparing
// the run, not before the cycle. Without it `git commit` fails outright
// in a fresh sandbox (mcp.ConfigureGitCredentials' own doc comment on why
// a sandbox otherwise has no identity at all).
//
// This used to be done by hand, once per slot, before the cycle ran --
// which is where it belonged while a slot's directory outlived every run
// dispatched into it. A sandbox does not exist until the dispatch builds
// it now, so the only place left to configure one is around Acquire.
//
// It wraps Sandboxes rather than going through Deps.MintSandboxToken
// because that path derives its remote from Config.GitRemoteBase, and
// these tests deliberately leave that unset so a run's checkout clones
// straight off a bare repo path rather than through a gitproxy. The
// placeholder remote's scheme and host (never its path) are the only part
// of this that matters here.
type credentialingSandboxes struct {
	inner orchestrator.Sandboxes
	t     *testing.T
}

func (s credentialingSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	sb, err := s.inner.Acquire(ctx, name, shape)
	if err != nil {
		return nil, err
	}
	if err := sb.ConfigureGitCredentials(ctx, "http://placeholder.example/x/y.git", "unused", mcp.GitIdentity{}); err != nil {
		s.t.Fatalf("configuring git credentials on %s: %v", name, err)
	}
	return sb, nil
}

// --- scripting helpers for the gemini agent, duplicated from
// tests/e2e/harness_test.go the same deliberate way
// gitproxy/live_test.go's own comment explains: package-private test
// helpers are cheaper to duplicate than to share. -------------------

func finalText(text string) antigravity.Step { return antigravity.TextStep(text) }

func toolCall(name string, args map[string]any) antigravity.Step {
	return antigravity.ToolStep(name, args)
}

func preclonedPushScript(branch, taskID string) []antigravity.Step {
	cmd := "cd work && " +
		"echo 'change for " + taskID + "' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed " + branch),
	}
}

func pushScript(remote, branch, taskID string) []antigravity.Step {
	cmd := "git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " && " +
		"echo 'change for " + taskID + "' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed " + branch),
	}
}

func askScript(question string) []antigravity.Step {
	return []antigravity.Step{
		toolCall("ask_question", map[string]any{"question": question}),
		finalText("waiting on a reply"),
	}
}

func scriptedFramework(script []antigravity.Step) func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
	return func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
		return antigravity.NewForTest(antigravity.Steps(script...)), nil
	}
}

// fileTask creates a task exactly the way a person at the CLI or the UI
// does -- through pkg/ui.Client, against the same store RunCycle reads.
// That is the whole point of driving these two scenarios this way: the
// task's entire existence is one store write, where it used to be a
// labelled GitHub issue that a poll had to notice first.
func fileTask(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef,
	title, body string) (*ui.Client, ui.Task) {

	t.Helper()
	client := ui.NewClient(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return baseTime }

	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: title, Description: body, Approved: true,
	})
	if err != nil {
		t.Fatalf("filing a task: %v", err)
	}
	return client, task
}

func TestRunCycleCompletesEndToEnd(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, task := fileTask(t, ctx, store, repo, "add a NOTES file", "please add one")

	sandboxes := orchestrator.NewHostSandboxes(t.TempDir())

	clock := baseTime
	branch := model.BranchName(task.ID)

	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: credentialingSandboxes{inner: sandboxes, t: t}, MaxWorkers: 1,
		Framework: scriptedFramework(pushScript(sim.BareRepo, branch, task.ID)),
	}
	// One cycle, straight from the store write: no poll, and no tick spent
	// waiting for one to notice the task exists.
	if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state after the first cycle = %q, want awaiting_submit: the pull request is open and unsubmitted", st)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected a pull request to have been opened, got %+v", sim.PullRequests)
	}

	// GitHub itself merges the PR -- nothing in this package does that; a
	// live test plays GitHub's part here the same way e2e's own
	// mergeBranchIntoDefault does for the git side of a merge.
	for i := range sim.PullRequests {
		sim.PullRequests[i].State = "closed"
	}

	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (second, sync-only): %v", err)
	}

	st, err = store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state after the merge = %q, want closed", st)
	}
	// Closing out is one store write now. There is no issue to close
	// alongside it, and nothing filed one: the only thing this run put on
	// GitHub is the pull request.
	if len(sim.Issues) != 0 {
		t.Fatalf("expected no GitHub issues at all, got %+v", sim.Issues)
	}
}

// This is the complete successful task path over an explicit repository
// remote: the daemon pre-clones without its GitHub proxy, the agent works in
// that checkout and pushes back to the same remote, and the normal completion
// adapter opens and later closes the task's pull request.
func TestRunCycleCompletesEndToEndWithAConfiguredNonGitHubRemote(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CloneURL: sim.BareRepo}); err != nil {
		t.Fatal(err)
	}
	_, task := fileTask(t, ctx, store, repo, "add a NOTES file", "please add one")

	branch := model.BranchName(task.ID)
	deps := orchestrator.Deps{
		Store: store, Client: client,
		Sandboxes:  credentialingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir()), t: t},
		MaxWorkers: 1,
		Framework:  scriptedFramework(preclonedPushScript(branch, task.ID)),
	}
	clock := baseTime
	if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateAwaitingSubmit {
		t.Fatalf("state after push = %q, %v; want awaiting_submit", state, err)
	}
	if len(sim.PullRequests) != 1 || sim.PullRequests[0].Head != branch {
		t.Fatalf("pull requests after push = %+v, want one for %s", sim.PullRequests, branch)
	}

	for i := range sim.PullRequests {
		sim.PullRequests[i].State = "closed"
	}
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (close): %v", err)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateClosed {
		t.Fatalf("state after pull request close = %q, %v; want closed", state, err)
	}
}

func TestRunCycleParksOnAQuestionThenResumesAfterAReply(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	tasks, task := fileTask(t, ctx, store, repo, "ambiguous task", "do the thing")

	sandboxes := orchestrator.NewHostSandboxes(t.TempDir())

	clock := baseTime
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: credentialingSandboxes{inner: sandboxes, t: t}, MaxWorkers: 1,
		Framework: scriptedFramework(askScript("which file should this go in?")),
	}
	if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Fatalf("state after the question = %q, want awaiting_reply", st)
	}
	detail, err := tasks.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Comments) != 1 || detail.Comments[0].Body != "which file should this go in?" {
		t.Fatalf("conversation = %+v, want the relayed question", detail.Comments)
	}
	if detail.Comments[0].OnBehalfOf == "" {
		t.Fatal("the relayed question is not attributed on behalf of the agent")
	}

	// A human replies. That is the whole of it: replying is what resumes
	// the task. It used to take two acts -- comment, then re-apply the
	// trigger label so the next poll would notice -- and forgetting the
	// second left the task parked forever.
	if err := tasks.AddComment(ctx, task.ID, "put it in NOTES.md", nil); err != nil {
		t.Fatal(err)
	}

	branch := model.BranchName(task.ID)
	deps.Framework = scriptedFramework(pushScript(sim.BareRepo, branch, task.ID))
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (resume): %v", err)
	}

	st, err = store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state after resuming = %q, want awaiting_submit: the pull request is open and unsubmitted", st)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected a pull request to have been opened, got %+v", sim.PullRequests)
	}
}
