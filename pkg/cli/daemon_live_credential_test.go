// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

// The daemon's own handling of a run that fails on *the deployment's*
// GitHub credential -- not on the task's scope, not on the sandbox, and
// not on anything the model did wrong.
//
// Every git failure this package could arrange before was one grain
// itself produced: a repo the task never named, which the git proxy
// refuses before anything leaves the machine, or a proxy token the
// sandbox never got. The commonest real failure is the other kind. The
// repo is the task's target, the proxy forwards the push, and GitHub
// refuses the credential the ladder chose for it: a fine-grained token
// without Contents: write, an App installation granted read only, a token
// that expired since the last run. Nothing about the run is wrong in that
// case and there is nothing for a redispatch to do differently -- an
// operator has to go and look at credentials.json.
//
// pkg/gitproxy/live_refusals_test.go proves refusals.go's sentence
// reaches a real terminal, and tests/e2e/credential_refusal_test.go
// proves a dispatched agent reads it and the task goes back to the queue,
// both against scripted agents. What is left, and what this file adds, is
// the same failure through `grain daemon` itself with a live model
// driving it: run() dispatches, grain's own checkout clones (the
// credential may read), the agent's push is refused upstream, and the
// daemon has to end that run without opening a pull request for work that
// never landed.
//
// Gated on GEMINI_API_KEY like the other live daemon tests here, so it
// runs nowhere without a live key (including CI) and costs nothing when
// skipped:
//
//	GEMINI_API_KEY=... go test ./cmd/grain/... -run TestRunLiveFailsWhenGitHubRefusesTheDeploymentsCredential -v -timeout 20m

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// liveBotToken is this deployment's own GitHub credential: what
// credentials.json's "bot" entry resolves to, and what
// githubsim.GitUpstream sees arrive on the far side of the proxy. It is
// nothing like the per-run token the sandbox's own git holds, and that
// distinction is the whole subject here -- the credential GitHub refuses
// below is one the sandbox never saw and cannot name.
const liveBotToken = "graind-live-bot-token"

// TestRunLiveFailsWhenGitHubRefusesTheDeploymentsCredential dispatches a
// task that is entirely in order -- its target is the repo, the sandbox
// is scoped to it, grain's own checkout clones it -- through a deployment
// whose credential GitHub will not let write.
//
// Four things have to follow, and each is a fact about the daemon rather
// than about the model:
//
//   - the push lands nothing upstream, since GitHub refused it;
//   - the agent was told which credential was refused and where to look,
//     which is the one fact the sandbox cannot discover for itself (the
//     proxy is the only party that knows what it forwarded under) and is
//     read back here off the run's own stored transcript;
//   - no pull request is opened, because there is no branch to open one
//     for -- the failure this file guards is a daemon that reports a run
//     as done when nothing was pushed;
//   - the run does not read "succeeded" once ProcessResult has looked at
//     it, so the task is retried rather than counted as finished work.
//
// The prompt is more prescriptive than a real task's would be, and
// deliberately so. What a live model does with a refused push is its own
// business -- retry it, ask a question, leave a comment -- and each of
// those is a different ending in orchestrator.ProcessResult, which would
// make this test a measurement of the model's temperament rather than of
// grain's handling. So the task says what to do when the push is refused
// and what not to do, the same way TestRunLiveDispatchesAndOpensAPullRequest's
// own prompt scripts the happy path. A run that asks or comments anyway
// fails here, and says so in those words rather than as a bare
// mismatched-outcome error.
func TestRunLiveFailsWhenGitHubRefusesTheDeploymentsCredential(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set; skipping live grain daemon integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	const owner, repoName = "acme", "widgets"
	taskID := "graind-live-credential"
	branch := model.BranchName(taskID)

	// A real bare repo standing in for the GitHub-hosted one, seeded with
	// one commit on main -- githubsim.Sim.BranchExists shells out to git
	// against this same path, and so does branchPushed below.
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"))
	runLive(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedDir := filepath.Join(t.TempDir(), "seed")
	runLive(t, t.TempDir(), "git", "clone", bare, seedDir)
	runLive(t, seedDir, "git", "config", "user.email", "seed@example.com")
	runLive(t, seedDir, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seedDir, "git", "add", "README.md")
	runLive(t, seedDir, "git", "commit", "-q", "-m", "initial commit")
	runLive(t, seedDir, "git", "push", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	// Read but not write: the read-only fine-grained token, or an App
	// installation granted Contents: read. Read matters as much as the
	// missing write does -- grain clones the target into the sandbox
	// itself before the agent's first turn (orchestrator.prepareCheckout),
	// so a credential that could not read would fail the dispatch during
	// setup and this test would never reach an agent at all.
	githubHost := githubHostServerAuthed(t, sim, upstream, map[string]githubsim.GitAccess{
		liveBotToken: {Read: []string{owner + "/" + repoName}},
	})

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The credential ladder, pointed at a real token rather than at the
	// "anonymous" rung the other live tests here run on: this is the
	// deployment's own choice of credential, and it is the thing under
	// test.
	seedTestConnections(t, dataDir, "git:\n  - credential: bot\n")
	if err := os.WriteFile(filepath.Join(dataDir, "secrets", "github", "bot.token"),
		[]byte(liveBotToken), 0o600); err != nil {
		t.Fatal(err)
	}

	// Seeded before the daemon starts, for the reason
	// TestRunLiveDispatchesAndOpensAPullRequest's own seeding gives: run()
	// opens its store and dispatches on its first tick, with no hook for a
	// test to insert a task in between.
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("seeding the store: %v", err)
	}
	task := model.Task{
		ID:     taskID,
		Intent: model.IntentImplement,
		Title:  "graind live credential test",
		// No cloning to arrange here, unlike the sibling live test: grain
		// clones the target into ./work and checks the branch out before
		// the agent's first turn, so the task is only the commit and the
		// push that will be refused.
		Body: "Your sandbox already has a clone of " + owner + "/" + repoName + " in ./work, with " +
			"the branch " + branch + " checked out. Call your run_command tool exactly once, with " +
			"the single shell command below, and then stop.\n\n" +
			"cd work && echo 'graind live credential test' >> NOTES.md && git add NOTES.md && " +
			"git commit -q -m 'graind live credential test' && git push origin " + branch + "\n\n" +
			"If the push succeeds, reply saying so. If it is refused, reply with the whole of what " +
			"the remote printed and nothing else -- that output is the entire point of this task. " +
			"Do not run the command a second time, do not look for another way to land the commit, " +
			"and do not call ask_question or comment_on_issue: your reply is the report here.",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Binding: model.BindingDirective,
		Target:  &model.RepoRef{Owner: owner, Name: repoName},
	}
	task.Approval = &model.Attribution{Actor: task.Origin.Attribution.Actor}
	if err := store.PutTask(context.Background(), task); err != nil {
		db.Close()
		t.Fatalf("seeding the task: %v", err)
	}
	// Held open past the seeding: this connection is also how the test
	// reads the run's own row back while the daemon writes to it.
	defer db.Close()

	// No seedPassingCheck here, unlike the sibling test: nothing is
	// pushed, so there is no branch for the prompt's CI paragraph to send
	// a wait_for_checks call after, and seeding a *passing* check for a
	// branch that was never pushed would tell the agent the opposite of
	// what happened.
	ctx, runCtx, cancel := liveDaemonContext(t, liveFinishWindow, liveShutdownReserve)
	defer cancel()
	liveKeyFile := writeKeyFile(t, apiKey)
	liveCfg := daemon.Config{
		DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: 5 * time.Second,
		GeminiAPIKeyFile: liveKeyFile,
		GeminiEffort:     antigravity.DefaultEffort, MaxAgentTurns: 15,
		GithubHost: githubHost, GithubInsecureHTTP: true,
	}
	seedTestDaemonConnections(t, liveCfg, "models:\n  - id: gemini\n    framework: antigravity\n    models: ["+liveGeminiModel+"]\n"+
		"    credential_file: "+liveKeyFile+"\n")
	done, stopped := startLiveDaemon(ctx, liveCfg)
	defer stopLiveDaemon(t, cancel, done)

	finished, err := awaitFinishedRun(runCtx, store, taskID, stopped)
	if err != nil {
		t.Fatal(err)
	}

	// A push GitHub refused lands nothing, whatever the agent went on to
	// do about it. This failing means the arrangement itself is wrong --
	// the auth layer let the push through -- rather than the daemon being.
	if branchPushed(bare, branch) {
		t.Fatalf("branch %s is on %s, but the deployment's credential was granted read and no write: "+
			"GitHub's own auth layer (githubsim.GitUpstream) was supposed to refuse this push", branch, bare)
	}

	// What the agent actually read. The credential's name and
	// credentials.json are the two facts nothing in the sandbox could have
	// supplied -- the sandbox never sees the credential the proxy forwards
	// under -- and GitHub's own sentence is the half the proxy quotes back
	// rather than invents. The run's transcript is where a tool's output
	// survives the run (Store.SetRunTranscript); agent.Result itself is
	// discarded, so this is also the only record an operator would have.
	transcript := awaitRunTranscript(ctx, store, taskID, finished.Attempt)
	if transcript == "" {
		t.Fatalf("run %s recorded no transcript, so there is no evidence of what the agent read "+
			"when its push was refused; the run ended %q (%s)",
			finished.ID, finished.Outcome, finished.Detail)
	}
	for _, want := range []string{
		owner + "/" + repoName,
		`the "bot" credential`,
		"refused it write access (403)",
		"git connection",
		// GitHub's own sentence, quoted back through the proxy.
		"Write access to repository not granted",
	} {
		if !strings.Contains(transcript, want) {
			t.Errorf("the run's transcript does not mention %q. What the agent read back was:\n%s",
				want, transcript)
		}
	}

	// The daemon's own verdict, which is written twice: RunDispatch stamps
	// this run "succeeded" the moment the agent makes any tool call at all
	// (orchestrator.outcomeOf cannot check GitHub), and ProcessResult
	// overwrites it with "no_action" once a pushed branch, a question and
	// a closing comment have all been ruled out. So the assertion is that
	// the second write happened, and waiting for it is waiting on grain's
	// own couple of steps rather than on the model -- the same window the
	// sibling test waits for its pull request in.
	processed, ok := awaitProcessedRun(ctx, store, taskID, finished.ID)
	if !ok {
		t.Fatalf("run %s of task %s still reads outcome %q (%s) %s after it finished. A run whose "+
			"push GitHub refused pushed no branch, so ProcessResult should have corrected it to "+
			"\"no_action\" -- an outcome still reading \"succeeded\" means the run ended some other "+
			"way instead, most likely the agent asking a question or leaving a comment despite the "+
			"task telling it not to",
			finished.ID, taskID, processed.Outcome, processed.Detail, liveFinishWindow)
	}

	// And no pull request. Checked after the outcome above rather than on
	// a window of its own: ProcessResult opens a pull request for a pushed
	// branch (salvagePushedBranch) *before* it records this outcome, so by
	// the time the outcome has been corrected the decision not to open one
	// has already been taken.
	if n := sim.pullRequestCount(); n != 0 {
		t.Errorf("%d pull request(s) were opened for a run that pushed nothing -- the branch GitHub "+
			"refused is not on the remote, and nothing else was in flight", n)
	}

	// The task is not done. Queued is the ordinary reading; running is the
	// same answer a beat later, since a task whose run ended without
	// pushing is eligible for another attempt and this daemon is still
	// ticking (dispatch's own backoff decides when).
	state, err := store.State(ctx, taskID)
	if err != nil {
		t.Fatalf("reading task %s's state back: %v", taskID, err)
	}
	if state != model.StateQueued && state != model.StateRunning {
		t.Errorf("task %s reads %q after a run GitHub refused the credential for, want it back in "+
			"the queue: a credential failure is nothing the task has been finished by", taskID, state)
	}
}

// awaitRunTranscript is the transcript recorded for taskID's numbered
// attempt, waited for rather than read once.
//
// RunDispatch writes it immediately after the finished_at stamp
// awaitFinishedRun returns on (two writes, for the reason
// Store.SetRunTranscript's own doc comment gives), so a test that reads it
// the instant a run finishes is racing that second write by a hair. ""
// once the window is up, which the caller reports as the absence of
// evidence it is.
func awaitRunTranscript(ctx context.Context, store *model.Store, taskID string, attempt int) string {
	var transcript string
	pollUntil(ctx, liveFinishWindow, func() bool {
		text, found, err := store.RunTranscript(ctx, taskID, attempt)
		if err != nil || !found {
			return false
		}
		transcript = text
		return transcript != ""
	})
	return transcript
}

// awaitProcessedRun waits for runID to carry an outcome ProcessResult has
// had its say on -- anything but the "succeeded" RunDispatch guesses for
// every run that called a tool -- and hands the row back either way, so a
// caller that ran out of window can say what the run does read.
//
// It looks runID up by id rather than taking the task's latest run: the
// daemon is still ticking while this waits, and a task whose run ended
// without pushing is eligible for a second attempt (dispatch's own
// baseRetryBackoff is 30s, well inside this window), which would otherwise
// be the row read back.
func awaitProcessedRun(ctx context.Context, store *model.Store, taskID, runID string) (model.Run, bool) {
	var run model.Run
	ok := pollUntil(ctx, liveFinishWindow, func() bool {
		runs, err := store.Runs(ctx, taskID)
		if err != nil {
			return false
		}
		for _, r := range runs {
			if r.ID != runID {
				continue
			}
			run = r
			return r.Outcome != "" && r.Outcome != "succeeded"
		}
		return false
	})
	return run, ok
}

// TestPushThroughTheDaemonsProxyIsRefusedWhenTheCredentialMayNotWrite is
// the keyless half of the test above: the same rig -- this package's
// stand-in GitHub with githubsim.GitUpstream in front of it, this
// package's own startGitProxy, a bot credential granted read and no
// write -- driven by the clone/commit/push a dispatched agent would
// perform, done by hand rather than by a model.
//
// It exists because everything above it is gated on GEMINI_API_KEY and so
// runs nowhere in CI, which would leave githubHostServerAuthed's wiring
// -- that the token GitUpstream sees really is the one credentials.json
// named, that its paths match what the proxy forwards, that the refusal
// reaches the sandbox as text a terminal prints -- proved by nothing that
// anybody runs. What it cannot cover is the daemon's own handling of the
// failure, which needs a real agent to produce one: that is the live test
// above, and this is what keeps its rig honest in between.
func TestPushThroughTheDaemonsProxyIsRefusedWhenTheCredentialMayNotWrite(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	const owner, repoName = "acme", "widgets"

	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"))
	runLive(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedDir := filepath.Join(t.TempDir(), "seed")
	runLive(t, t.TempDir(), "git", "clone", bare, seedDir)
	runLive(t, seedDir, "git", "config", "user.email", "seed@example.com")
	runLive(t, seedDir, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seedDir, "git", "add", "README.md")
	runLive(t, seedDir, "git", "commit", "-q", "-m", "initial commit")
	runLive(t, seedDir, "git", "push", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServerAuthed(t, sim, upstream, map[string]githubsim.GitAccess{
		liveBotToken: {Read: []string{owner + "/" + repoName}},
	})

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: bot\n")
	if err := os.WriteFile(filepath.Join(dataDir, "secrets", "github", "bot.token"),
		[]byte(liveBotToken), 0o600); err != nil {
		t.Fatal(err)
	}

	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	giveDefaultTeamCredential(t, store, "bot")

	task := model.Task{
		ID:     "credential-refusal-check",
		Intent: model.IntentImplement,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Binding: model.BindingDirective,
		Target:  &model.RepoRef{Owner: owner, Name: repoName},
	}
	task.Approval = &model.Attribution{Actor: task.Origin.Attribution.Actor}
	if err := store.PutTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	// The proxy authorizes a sandbox by the live run it belongs to, so
	// this needs a real dispatch on record naming its sandbox -- what
	// orchestrator.runOne records once it has acquired one. Without it
	// every request through the proxy is refused for being out of scope,
	// which is the failure this test is precisely not about.
	dispatches, err := dispatch.Cycle(context.Background(), store, model.Limits{Workers: 1}, time.Now().UTC())
	if err != nil || len(dispatches) != 1 {
		t.Fatalf("expected exactly one dispatch, got %v (err=%v)", dispatches, err)
	}
	sandbox := dispatches[0].RunID
	if err := store.SetRunSandbox(context.Background(), dispatches[0].RunID, sandbox, task.Target); err != nil {
		t.Fatal(err)
	}

	proxyURL, _, stop, err := daemon.StartGitProxy(dataDir, store, githubHost, true, daemon.NewDirect(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())

	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json"))
	token, err := tokens.EnsureToken(sandbox)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := mcp.ConfigureGitCredentials(root, proxyURL+"/placeholder/placeholder.git", token, mcp.GitIdentity{}); err != nil {
		t.Fatal(err)
	}

	branch := model.BranchName(task.ID)
	script := "HOST=$(sed -n 's#.*@\\([^/]*\\).*#\\1#p' ~/.git-credentials) && " +
		"git clone http://$HOST/" + owner + "/" + repoName + ".git work && " +
		"cd work && git checkout -b " + branch + " && " +
		"echo 'credential refusal check' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'credential refusal check' && " +
		"git push origin " + branch

	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = root
	// GIT_TERMINAL_PROMPT=0 so that a git which decides it wants a
	// username fails and says so rather than hanging this test on a
	// terminal that is not there.
	cmd.Env = append(os.Environ(), "HOME="+root, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the push was allowed through with a credential granted read and no write:\n%s", out)
	}

	// The clone in front of it is half the assertion: this is a working
	// remote reached with a credential GitHub accepts, so the refusal
	// below is about the write permission and nothing else.
	for _, want := range []string{
		"Cloning into 'work'",
		owner + "/" + repoName,
		`the "bot" credential`,
		"refused it write access (403)",
		"git connection",
		"Write access to repository not granted",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("git printed:\n%s\nwant it to carry %q", out, want)
		}
	}
	if branchPushed(bare, branch) {
		t.Errorf("branch %s reached %s despite the refusal", branch, bare)
	}
	// The sandbox's own credential was never the refused one, and is
	// still there for the next git command of the run -- what
	// refusals.go answers 403 rather than 401 to preserve.
	creds, err := os.ReadFile(filepath.Join(root, ".git-credentials"))
	if err != nil {
		t.Fatalf("reading the sandbox's git credentials back: %v", err)
	}
	if !strings.Contains(string(creds), token) {
		t.Errorf("the sandbox's ~/.git-credentials = %q, want the proxy token it started with", creds)
	}
}
