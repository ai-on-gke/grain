package e2e

// A run that fails on the *deployment's* credential rather than on the
// task's scope -- the failure this package could not arrange until
// githubsim.GitUpstream could sit in front of its git http-backend
// (harness_test.go's withGitHubAuth).
//
// Every git failure reachable here before was the proxy's own refusal: a
// repo the task never named, which the proxy answers before anything
// leaves the machine. The commonest real failure is the other kind --
// the repo is in scope, the proxy forwards, and GitHub refuses the
// credential this deployment chose: a fine-grained token without
// Contents: write, an App installation granted read only, a token that
// expired since the last run. Nothing about the model or the sandbox is
// wrong in that case, and the operator has to be sent to credentials.json
// instead.
//
// pkg/gitproxy/live_refusals_test.go proves the sentence reaches a
// terminal. What this file adds is who is holding that terminal in a real
// dispatch: the agent, through run_command's own output, with the run
// then failing and the task going back to the queue on its own.

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
)

// toolOutput is everything the agent read back from its tool calls, which
// for a scripted run_command is the git output the sandbox's shell
// produced -- the only place a dispatched agent can learn why its push
// did not land.
func toolOutput(result *agent.Result) string {
	var b strings.Builder
	for _, c := range result.ToolCalls {
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// TestARunGitHubRefusesTheDeploymentsCredentialOnFailsAndTellsTheAgentWhy
// takes a task that is entirely in order -- the repo is its target, the
// sandbox is scoped to it, the clone works -- and gives the deployment a
// credential GitHub will not let write. The push is refused upstream, and
// three things have to follow: the agent is told which credential and
// where to look, nothing lands in the upstream repo, and the run fails
// and requeues the task rather than reading as a success with no push.
func TestARunGitHubRefusesTheDeploymentsCredentialOnFailsAndTellsTheAgentWhy(t *testing.T) {
	t.Parallel()
	w := newWorld(t, withGitHubAuth(map[string]githubsim.GitAccess{
		// Read but not write: the read-only fine-grained token, or an App
		// installation granted Contents: read.
		e2eBotToken: {Read: []string{"acme/widgets"}},
	}))
	w.newRepo("acme", "widgets")

	clock := baseTime
	fileIssue(w, "iss-cred", human("dana"), model.RepoRef{Owner: "acme", Name: "widgets"})
	branch := model.BranchName("iss-cred")

	first, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, clock)
	if err != nil || len(first) != 1 {
		t.Fatalf("first Cycle: %v, %+v", err, first)
	}

	clock = clock.Add(time.Minute)
	refused := w.runDispatch(first[0], pushScript(w.remote("acme", "widgets"), branch, "iss-cred"), clock)
	if pushedOK(refused) {
		t.Fatalf("expected the push to be refused upstream, got: %+v", refused.ToolCalls)
	}

	// What the agent actually read. The credential's name and the git
	// connection that chose it are the two things nothing else in the sandbox
	// could have told it -- the sandbox never sees the credential the
	// proxy forwards under.
	out := toolOutput(refused)
	for _, want := range []string{
		"acme/widgets",
		`the "bot" credential`,
		"refused it write access (403)",
		"git connection",
		// GitHub's own sentence, quoted back through the proxy.
		"Write access to repository not granted",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the agent read:\n%s\nwant it to mention %q", out, want)
		}
	}

	if w.branchExists("acme", "widgets", branch) {
		t.Error("a push GitHub refused must land nothing upstream")
	}
	assertState(w, "iss-cred", model.StateQueued, false)

	// And the other half: the same task, the same repo, once the
	// credential is granted what it was missing. Nothing about the task
	// or the sandbox changed, which is what makes the failure above a
	// statement about the credential alone.
	w.grantGitHubWrite(e2eBotToken, "acme/widgets")

	clock = clock.Add(time.Minute)
	second, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, clock)
	if err != nil || len(second) != 1 || second[0].Attempt != 2 {
		t.Fatalf("retry Cycle: %v, %+v, want attempt 2", err, second)
	}
	clock = clock.Add(time.Minute)
	granted := w.runDispatch(second[0], pushScript(w.remote("acme", "widgets"), branch, "iss-cred"), clock)
	if !pushedOK(granted) {
		t.Fatalf("the retry with a writable credential still failed: %s", toolOutput(granted))
	}
	if got := w.log1("acme", "widgets", branch, "%s"); got != "agent commit for iss-cred" {
		t.Fatalf("pushed branch tip = %q, want the agent's commit", got)
	}
}

// TestARunWhoseCredentialGitHubNoLongerKnowsFailsWithoutCostingTheSandboxItsOwn
// is the expired-token shape: GitHub refuses the deployment's credential
// itself, before it considers the repository, with a 401. The sandbox's
// own credential is the per-run proxy token and was accepted, so it must
// survive -- git erases a credential it is answered 401 for
// (pkg/gitproxy/live_refusals_test.go proves that against a real git),
// and a sandbox that lost its proxy token cannot run another git command
// for the rest of the run, including the retry that would have worked.
func TestARunWhoseCredentialGitHubNoLongerKnowsFailsWithoutCostingTheSandboxItsOwn(t *testing.T) {
	t.Parallel()
	w := newWorld(t, withGitHubAuth(map[string]githubsim.GitAccess{
		"some-other-token": {Read: []string{"acme/widgets"}, Write: []string{"acme/widgets"}},
	}))
	w.newRepo("acme", "widgets")

	clock := baseTime
	fileIssue(w, "iss-expired", human("dana"), model.RepoRef{Owner: "acme", Name: "widgets"})
	branch := model.BranchName("iss-expired")

	first, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, clock)
	if err != nil || len(first) != 1 {
		t.Fatalf("first Cycle: %v, %+v", err, first)
	}
	clock = clock.Add(time.Minute)
	refused := w.runDispatch(first[0], pushScript(w.remote("acme", "widgets"), branch, "iss-expired"), clock)
	if pushedOK(refused) {
		t.Fatalf("expected the clone to be refused upstream, got: %+v", refused.ToolCalls)
	}

	out := toolOutput(refused)
	for _, want := range []string{
		"refused the credential itself (401)",
		`the "bot" credential`,
		"Git connections",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the agent read:\n%s\nwant it to mention %q", out, want)
		}
	}
	// The sandbox still holds the proxy token it was dispatched with --
	// the credential it has was never the refused one, and the next git
	// command of this run would still reach the proxy.
	token, err := w.tokens.EnsureToken(first[0].RunID)
	if err != nil {
		t.Fatal(err)
	}
	if creds := w.sandboxCredentials(first[0].RunID); !strings.Contains(creds, token) {
		t.Errorf("the sandbox's ~/.git-credentials = %q, want the proxy token it started with", creds)
	}
}

// TestARunWrongHostCredentialFailsAndTellsAgentAndTaskNotes arranges a wrong-host
// credential state: the credential is for a different host than the request target.
func TestARunWrongHostCredentialFailsAndTellsAgentAndTaskNotes(t *testing.T) {
	t.Parallel()
	w := newWorld(t, withGitHubAuth(map[string]githubsim.GitAccess{
		e2eBotToken: {Read: []string{"acme/widgets"}, Write: []string{"acme/widgets"}},
	}))
	w.newRepo("acme", "widgets")

	diffHost := "ghe-internal.example.com"
	if err := w.credentials.SetHost("bot", gitproxy.CredentialHost{
		Host: diffHost,
		Kind: model.GitHostGitHub,
	}); err != nil {
		t.Fatalf("SetHost: %v", err)
	}

	clock := baseTime
	taskID := "iss-wrong-host"
	fileIssue(w, taskID, human("dana"), model.RepoRef{Owner: "acme", Name: "widgets"})
	branch := model.BranchName(taskID)

	first, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, clock)
	if err != nil || len(first) != 1 {
		t.Fatalf("first Cycle: %v, %+v", err, first)
	}

	clock = clock.Add(time.Minute)
	refused := w.runDispatch(first[0], pushScript(w.remote("acme", "widgets"), branch, taskID), clock)
	if pushedOK(refused) {
		t.Fatalf("expected the push to be refused with wrong-host, got: %+v", refused.ToolCalls)
	}

	out := toolOutput(refused)
	targetHost := w.credentials.DefaultHost()
	wantSentence := `credential "bot" is for git host ` + diffHost + `, and this request goes to ` + targetHost + ` -- grain never sends one host's credential to another; add a git connection for ` + targetHost

	if !strings.Contains(out, wantSentence) {
		t.Errorf("the agent read:\n%s\nwant it to contain exact sentence:\n%q", out, wantSentence)
	}

	if err := w.store.FinishRun(w.ctx, first[0].RunID, clock, "failed", out); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	streak, err := w.store.FailureStreak(w.ctx, taskID)
	if err != nil {
		t.Fatalf("FailureStreak: %v", err)
	}
	if streak == nil || streak.Count != 1 {
		t.Fatalf("streak = %+v, want count 1", streak)
	}
	if !strings.Contains(streak.LastDetail, wantSentence) {
		t.Errorf("task's recorded failure = %q, want it to contain:\n%q", streak.LastDetail, wantSentence)
	}

	if w.branchExists("acme", "widgets", branch) {
		t.Error("a push refused for wrong host must land nothing upstream")
	}
}
