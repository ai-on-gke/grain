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

package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// agentFunc adapts a plain function to agent.Framework, so each test can
// script exactly the run it wants without a scripted Gemini transcript --
// ported from pkg/orchestrate's own test helper (bwsalmon/agents#254) when
// that package's capability-handling tests moved here (bwsalmon/agents#263).
type agentFunc func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error)

func (f agentFunc) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return f(ctx, cfg)
}

// openingFramework is an agentFunc that also implements
// agent.PullRequestFramework, standing in for a real Framework built
// WithGrainServer -- the only kind whose runs are offered
// open_pull_request at all.
type openingFramework struct{ agentFunc }

func (openingFramework) CanOpenPullRequest() bool { return true }

func pushed() *agent.Result {
	return &agent.Result{
		FinalText: "pushed the change",
		ToolCalls: []agent.ToolCall{{Name: "run_command", Text: "ok"}},
	}
}

// fakeCapability is a model.CapabilityProvider a test configures to
// refuse, or to mint a lease and a placement, recording every Revoke call
// it gets. Ported from pkg/orchestrate's own test helper (bwsalmon/agents#254).
type fakeCapability struct {
	name     string
	refuse   string // non-empty means Resolve refuses with this reason
	path     string // placement path (absolute, like a real provider's)
	content  string
	limited  int // number of temporary resource refusals before minting
	attempts int
	// minted, when set, is called after each lease is minted, so a test
	// can break something between admission and the run that uses it.
	minted func()

	mu      sync.Mutex
	revoked []model.Lease
}

type scarceFakeCapability struct{ *fakeCapability }

func (*scarceFakeCapability) ScarceResourceProvider() {}

func (c *fakeCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{Name: c.name, Provision: model.ProvisionMint}
}

func (c *fakeCapability) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	if c.refuse != "" {
		return model.RefusedBecause(c.refuse), nil
	}
	return model.Honoured(), nil
}

func (c *fakeCapability) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	c.attempts++
	if c.limited > 0 {
		c.limited--
		return model.Materialization{}, &model.ResourceExhaustedError{
			Resource: "test worker keys", RetryAfter: time.Millisecond, Err: errors.New("none free"),
		}
	}
	materialized := model.Materialization{
		Lease: &model.Lease{Capability: c.name, Resource: "res-1", MintedBy: model.CredentialRef{Name: "test"}, IssuedAt: cc.Now},
	}
	if c.path != "" {
		materialized.Placements = []model.Placement{{Side: model.SideSandbox, Path: c.path, Content: c.content}}
	}
	if c.minted != nil {
		c.minted()
	}
	return materialized, nil
}

func (c *fakeCapability) PromptSection(ctx context.Context, cc model.CapabilityContext, placements []model.Placement) (string, error) {
	return "capability " + c.name + " is ready", nil
}

func (c *fakeCapability) Revoke(ctx context.Context, cc model.CapabilityContext, lease model.Lease) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revoked = append(c.revoked, lease)
	return nil
}

// dispatchTask puts an approved (human-filed) task directly, standing in
// for what dispatch.Cycle would already have found ready by the time
// RunDispatch runs -- these tests are about what RunDispatch does with a
// task's own Grants, not about approval or scheduling.
func dispatchTask(t *testing.T, ctx context.Context, store *model.Store, id string, grants ...model.Grant) model.Task {
	t.Helper()
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "Do the thing", Body: "details",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Target:   &model.RepoRef{Owner: "acme", Name: "widgets"},
		Binding:  model.BindingDirective,
		Grants:   grants,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing task %s: %v", id, err)
	}
	return task
}

// startRun records the task_run row dispatch.Cycle would already have written
// by the time RunDispatch ever sees a Dispatch -- RunDispatch only ever
// UPDATEs it (via store.FinishRun), the same "the run is already durable"
// assumption pkg/orchestrate's own runDispatch documented.
func startRun(t *testing.T, ctx context.Context, store *model.Store, d dispatch.Dispatch, at time.Time) {
	t.Helper()
	if err := store.StartRun(ctx, model.Run{
		ID: d.RunID, TaskID: d.TaskID, Sandbox: d.RunID, Attempt: d.Attempt, StartedAt: at,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting run %s: %v", d.RunID, err)
	}
}

// BuildPrompt tells the agent about a read-only repo, but the wording
// makes clear it grants nothing beyond a fetch -- gitproxy/authorize.go
// is what actually refuses a push to one; this only informs.
func TestBuildPromptMentionsReadOnlyRepos(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
		Reads: []model.RepoRef{
			{Owner: "acme", Name: "shared-lib"},
			{Owner: "acme", Name: "schema"},
		},
	}
	prompt := orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if !strings.Contains(prompt, "acme/shared-lib") || !strings.Contains(prompt, "acme/schema") {
		t.Fatalf("prompt does not mention both read-only repos: %q", prompt)
	}
	if !strings.Contains(prompt, "never push") {
		t.Fatalf("prompt does not warn against pushing to a read-only repo: %q", prompt)
	}
}

// With a checkout prepared for it (RunDispatch's own prepareCheckout),
// the prompt says so -- where the clone is, which branch is checked out,
// and that pushing is all the git the agent has left to do. Without one
// the wording is unchanged, which is what every deployment running no
// proxy, and every other test here, still gets.
func TestBuildPromptNamesAPreparedCheckout(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{"./" + orchestrator.CheckoutDir, model.BranchName("t1"), "rather than cloning"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not mention %q: %q", want, prompt)
		}
	}
	// Against the checkout sentence's own phrasing rather than against
	// CheckoutDir alone: that constant is "work", an ordinary English word
	// the rest of the prompt is entitled to use (proposalSection does), so
	// a bare substring search for it fails on prose that mentions no
	// checkout at all. "./work" and "rather than cloning" are what only
	// that sentence says, which is what this is checking is absent.
	bare := orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, unwanted := range []string{"./" + orchestrator.CheckoutDir, "rather than cloning"} {
		if strings.Contains(bare, unwanted) {
			t.Fatalf("prompt mentions %q, a checkout that was never prepared: %q", unwanted, bare)
		}
	}
}

// The checkout is named as an absolute path too, and the environment the
// run's own framework describes to it is disowned in the same breath:
// that block is about the controller the framework runs on, and a run
// that reconciles it with "./work" for itself invents /work/... and
// spends a turn finding out it does not exist (sandboxDirsSection).
func TestBuildPromptNamesTheSandboxsOwnDirectories(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "/home/debian", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{
		// Where the tools land, and the absolute form of the checkout
		// the sentence above it gives relatively.
		"/home/debian",
		"/home/debian/" + orchestrator.CheckoutDir,
		// The guess itself, named so the run does not have to make it.
		"/" + orchestrator.CheckoutDir + "/... is not a path",
		// And what the framework's own preamble is about.
		"not your sandbox",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not say %q: %q", want, prompt)
		}
	}
	// A task with no repo has no checkout to place, but its tools land
	// in the same sandbox and its framework describes the same wrong
	// machine, so the first half is still said.
	noRepo := orchestrator.BuildPrompt(model.Task{ID: "t2", Title: "Think", Body: "details"},
		"", "/home/debian", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if !strings.Contains(noRepo, "run_command starts in /home/debian") {
		t.Errorf("prompt does not say where a repo-less run's tools land: %q", noRepo)
	}
	if strings.Contains(noRepo, "The checkout is") {
		t.Errorf("prompt places a checkout that was never prepared: %q", noRepo)
	}
	// And a sandbox that could not be asked gets no invented path: the
	// prompt reads exactly as it did before any of this.
	unknown := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, unwanted := range []string{"not your sandbox", "run_command starts in"} {
		if strings.Contains(unknown, unwanted) {
			t.Errorf("prompt says %q about a sandbox whose directory is unknown: %q", unwanted, unknown)
		}
	}
}

// A repo with a setup command gets it reported in the same breath as the
// checkout it ran in, before anything about pushing or CI -- the failure
// case being the one that matters, since a run not told is a run
// debugging the repo's toolchain from a build it has no reason to
// distrust.
func TestBuildPromptReportsAFailedSetupBeforeTheMechanics(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{},
		&orchestrator.SetupResult{Command: "make deps", ExitCode: 2, Output: "no such package: widgetlib"})

	setup := strings.Index(prompt, "make deps")
	if setup < 0 || !strings.Contains(prompt, "no such package: widgetlib") {
		t.Fatalf("prompt says nothing about the setup command that failed: %q", prompt)
	}
	// Ahead of the push/check/repair paragraphs: a run that reads it
	// after those has already spent turns on the failure it explains.
	if push := strings.Index(prompt, "Push as often as you like"); push >= 0 && setup > push {
		t.Errorf("the setup report comes after the CI loop paragraphs: %q", prompt)
	}
	// And a repo that configures none says nothing at all -- most repos.
	none := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(none, "setup command") {
		t.Errorf("prompt mentions a setup command for a repo that has none: %q", none)
	}
}

// The push/check/repair loop pull_request_status exists for is only
// usable if the prompt says it is there: nothing about the tool's own
// description tells a run that it may push more than once, and the
// sentences before it read like one final act.
func TestBuildPromptDescribesThePushAndCheckCILoop(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{"pull_request_status", "Push as often as you like", "check fails"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, prompt)
		}
	}

	// A task with no repo has no branch to push and no CI to watch, so
	// the paragraph must not appear at all -- the same reason the
	// pushing/branching sentences do not.
	bare := orchestrator.BuildPrompt(model.Task{ID: "t2", Title: "Think", Body: "details"}, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(bare, "pull_request_status") {
		t.Errorf("prompt offers a CI tool to a task with no repo: %q", bare)
	}
}

// A repair run is told the opposite of what every other run is told
// about breakage it did not cause. The queue sent it because this branch
// is not merging; a repair that reads base_branch_status, finds the same
// failure on the base and reports "not mine" hands back the very pull
// request the queue could not merge, and the queue then gives up on it.
func TestBuildPromptTellsARepairToFixBreakageItDidNotCause(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	repair := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{Repair: true}, nil)
	for _, want := range []string{
		"merge queue's repair", "whether or not your own commits caused the failure",
		// The two things the ordinary paragraph is right about, which
		// this one keeps: the tool is still worth calling, and a test is
		// still not to be weakened.
		"base_branch_status", "rather than weakening the test",
	} {
		if !strings.Contains(repair, want) {
			t.Errorf("a repair's prompt does not mention %q: %q", want, repair)
		}
	}
	if strings.Contains(repair, "not yours to fix") {
		t.Errorf("a repair is still told to leave inherited breakage alone: %q", repair)
	}

	// And an ordinary dispatch keeps the advice that is right for it:
	// chasing a failure it did not cause is how a run spends its turns
	// rewriting working code.
	ordinary := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if !strings.Contains(ordinary, "not yours to fix") {
		t.Errorf("an ordinary run is no longer told inherited breakage is not its own: %q", ordinary)
	}
	if strings.Contains(ordinary, "merge queue's repair") {
		t.Errorf("an ordinary run is told it is a repair: %q", ordinary)
	}
}

// Where a pull request's description comes from is a fact about grain
// that nothing in the sandbox reveals: an agent told nothing writes
// "wip" and "fix tests", which are fine git log entries and a
// description-free pull request. So the prompt says what those messages
// become.
func TestBuildPromptSaysCommitMessagesBecomeTheDescription(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{"human reviewer", "pull request description", "commit messages"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, prompt)
		}
	}

	// A task with no repo pushes no branch and earns no pull request, so
	// there is no description for its commit messages to become.
	bare := orchestrator.BuildPrompt(model.Task{ID: "t2", Title: "Think", Body: "details"}, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(bare, "pull request description") {
		t.Errorf("prompt promises a description to a task with no repo: %q", bare)
	}
}

// The other half of that loop: when it is over. Every sentence before it
// is about how to push and how to look, and a run that pushed once, read
// one status and stopped has obeyed all of them -- so the prompt says
// outright that unfinished checks are not passes and that a conflict
// with the base is the run's own to resolve while it still has the
// checkout.
func TestBuildPromptSaysGreenChecksAndACleanMergeAreTheFinishLine(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{"not done", "merges cleanly", "carries no verdict", "conflicts with", "git fetch origin"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, prompt)
		}
	}
	// With no /base directive grain does not know the repo's default
	// branch, so the sentence describes the base rather than naming one:
	// a guessed branch name is one an agent would try to merge and fail.
	if strings.Contains(prompt, "conflicts with `") {
		t.Errorf("prompt names a base branch this task never set: %q", prompt)
	}

	task.Base = "release-2"
	based := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if !strings.Contains(based, "`release-2`") {
		t.Errorf("prompt does not name the base this task is built on: %q", based)
	}

	// A task with no repo has no branch, no base and no CI, the same
	// reason the paragraph above it is absent.
	bare := orchestrator.BuildPrompt(model.Task{ID: "t2", Title: "Think", Body: "details"}, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(bare, "merges cleanly") {
		t.Errorf("prompt talks about merging to a task with no repo: %q", bare)
	}
}

// open_pull_request is only on a run's roster when the Framework driving
// it was given a daemon to ask (agent.PullRequestFramework), so the
// prompt names it on exactly that condition: a run that has it and is
// never told finishes without ever seeing its own CI, which is the whole
// thing the tool exists to fix, and a run told to call one it does not
// have burns turns on an error it cannot do anything about.
func TestBuildPromptOffersOpenPullRequestOnlyToARunThatHasIt(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", true, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{"open_pull_request", "push again", "never opens a second one"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, prompt)
		}
	}

	without := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(without, "open_pull_request") {
		t.Errorf("prompt names a tool this run's mcpserver never registered: %q", without)
	}

	// A task with no repo has no branch, no pull request and no CI, so
	// the sentence has nothing to attach itself to even where the tool is
	// registered -- registration turns on -server/-task, neither of which
	// knows whether the task has a target.
	bare := orchestrator.BuildPrompt(model.Task{ID: "t2", Title: "Think", Body: "details"}, "", "", true, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(bare, "open_pull_request") {
		t.Errorf("prompt offers to open a pull request for a task with no repo: %q", bare)
	}
}

// update_status is registered by the same -server/-task pair
// open_pull_request is, so the prompt names it on exactly the same
// condition -- and it needs naming for a reason of its own: it is the one
// tool here whose whole value is to somebody outside the run, which makes
// it the one a run working on its own task would never go looking for.
// Unlike open_pull_request it has nothing to do with a branch, so a task
// with no repo at all gets the paragraph too: a run that spends an hour
// answering a question can say what it is doing just as usefully as one
// that pushes.
//
// The same paragraph carries the free half of that row: grain narrates
// every tool call onto it, run_command from its own `description`
// (mcp.Registry.AnnounceActivity), so the paragraph asks for a
// description on every command and leaves update_status what a tool name
// cannot say for itself. Both halves are asserted here because dropping
// either turns the row back into "running" -- one for the commands, one
// for everything between them.
func TestBuildPromptAsksForAStatusOnlyFromARunThatCanSendOne(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", true,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{
		"update_status", "task's row", "`run_command`", "`description`",
		"every tool call you make",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, prompt)
		}
	}

	without := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(without, "update_status") {
		t.Errorf("prompt names a tool this run's mcpserver never registered: %q", without)
	}

	bare := orchestrator.BuildPrompt(model.Task{ID: "t2", Title: "Think", Body: "details"}, "", "", true,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if !strings.Contains(bare, "update_status") {
		t.Errorf("prompt withholds the status tool from a task with no repo: %q", bare)
	}
}

// The prompt's half of the fact only the Framework holds: RunDispatch
// asks the one it is about to run, so a deployment whose daemon serves a
// UI/API tells its runs about open_pull_request and one without says
// nothing. openingFramework is the "yes" answer; a bare agentFunc, which
// implements no such method, is every other test here saying "no".
func TestRunDispatchTellsARunItCanOpenItsOwnPullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var gotPrompt string
	fw := openingFramework{agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		gotPrompt = cfg.Prompt
		return pushed(), nil
	})}
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if !strings.Contains(gotPrompt, "open_pull_request") {
		t.Errorf("prompt = %q, want it to name open_pull_request -- this framework's runs have it", gotPrompt)
	}
}

// The observation is the only record that a dispatch is the merge
// queue's repair rather than ordinary work on the task (requeueForRepair
// clears CompletedAt and sets MergeQueueRepairAt), so RunDispatch has to
// read it: the prompt paragraph and base_branch_status's own answer both
// hang off it, and neither is reachable from anything the task itself
// says.
func TestRunDispatchTellsARepairRunThatItIsOne(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	repairedAt := baseTime.Add(-time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: "t1", MergeQueueRepairAt: &repairedAt}); err != nil {
		t.Fatalf("recording the repair: %v", err)
	}
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 2}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var got agent.RunConfig
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		got = cfg
		return pushed(), nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{},
		*task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if !got.Repair {
		t.Errorf("RunConfig.Repair = false for a task the merge queue sent back to be repaired")
	}
	if !strings.Contains(got.Prompt, "merge queue's repair") {
		t.Errorf("prompt = %q, want the repair's own CI paragraph", got.Prompt)
	}

	// The same task once that repair has finished is ordinary work
	// again, and gets the ordinary advice back: one repair is the whole
	// policy, and a later attempt is not a second one.
	if err := store.Observe(ctx, model.Observation{
		TaskID: "t1", MergeQueueRepairAt: &repairedAt, CompletedAt: &baseTime,
	}); err != nil {
		t.Fatalf("recording the finished repair: %v", err)
	}
	d2 := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 3}
	startRun(t, ctx, store, d2, baseTime)
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{},
		*task, d2, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if got.Repair {
		t.Errorf("RunConfig.Repair = true after the repair finished")
	}
	if !strings.Contains(got.Prompt, "not yours to fix") {
		t.Errorf("prompt = %q, want the ordinary inherited-breakage paragraph", got.Prompt)
	}
}

// A task filed with CreateTaskRequest.NoRepo (bwsalmon/agents#614) has a
// nil Target -- prepareCheckout already skips cloning outright for one
// (checkout.go), so this is the other half: the prompt has to say so in
// words, rather than formatting a nil *model.RepoRef with %s and reading
// like a clone that silently failed.
func TestBuildPromptExplainsThereIsNoRepo(t *testing.T) {
	t.Parallel()
	task := model.Task{ID: "t1", Title: "Do the thing", Body: "details"}
	prompt := orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if !strings.Contains(prompt, "no repo") {
		t.Fatalf("prompt does not say there is no repo: %q", prompt)
	}
	if strings.Contains(prompt, "Push your change") || strings.Contains(prompt, "<nil>") {
		t.Fatalf("prompt still talks about pushing/branching, or leaked a nil format: %q", prompt)
	}
}

// An agent can only follow propose_task's own etiquette if it knows two
// things about itself grain never otherwise tells it: which task it is
// (what a proposal's depends_on names) and whether that task auto-merges
// (what caps a proposal's auto_merge -- orchestrator.proposedAutoMerge).
func TestBuildPromptTellsAnAgentWhatItsProposalsCanSay(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{"task t1", "depends_on"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, prompt)
		}
	}
	// Nothing to say about auto_merge to a task that cannot pass it on.
	if strings.Contains(prompt, "auto_merge") {
		t.Errorf("prompt offers auto_merge to a task that is not an auto-merge job: %q", prompt)
	}

	task.AutoMerge = true
	prompt = orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if !strings.Contains(prompt, "auto_merge") {
		t.Errorf("prompt does not tell an auto-merge job it can pass that on: %q", prompt)
	}

	// An auto-merge job on a base branch of its own passes nothing on:
	// its proposals are filed against the default branch, which is not
	// the branch a human gave it (model.SameBranch). Told only the first
	// half, a run would split work out expecting it to land the same way.
	task.Base = "release/2.0"
	prompt = orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if !strings.Contains(prompt, "Nothing you propose for this repository inherits") {
		t.Errorf("prompt lets an auto-merge job on its own base branch think it can pass that on: %q", prompt)
	}
}

func TestBuildPromptInstructsRelativeUILinks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		task model.Task
	}{
		{
			name: "ordinary task",
			task: model.Task{
				ID: "t1", Title: "Do the thing", Body: "details",
				Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
			},
		},
		{
			name: "console session",
			task: model.Task{
				ID: "450", Title: "Director", Body: "chat",
				Interactive: true,
				Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := orchestrator.BuildPrompt(tc.task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
			for _, want := range []string{
				"/tasks/<id>",
				"[Task #31](/tasks/31)",
				"[Settings → CI](/settings/ci)",
				"http://127.0.0.1:...",
				"http://localhost:...",
				"UberProxy",
			} {
				if !strings.Contains(prompt, want) {
					t.Errorf("prompt does not mention %q: %q", want, prompt)
				}
			}
		})
	}
}

// "Name task N itself" is the right advice for a run that lands a change
// and ends, and the wrong advice for the one kind of task that never
// ends. A standing chat session is never completed, a blocking link is
// released only by its target closing, and the director following this
// sentence made eight of its own tasks undispatchable in a day
// (grain/task-487). Such a run is told the other half of the paragraph
// -- an earlier proposal in the same run, which is the half that applies
// to it -- and told outright not to name itself.
func TestBuildPromptDoesNotAskAStandingSessionToDependOnItself(t *testing.T) {
	t.Parallel()
	session := model.Task{
		ID: "450", Title: "Director", Body: "chat",
		Interactive: true,
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
	}
	prompt := orchestrator.BuildPrompt(session, "", "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(prompt, "task 450 itself, when") {
		t.Errorf("prompt asks a session that never completes to depend on itself: %q", prompt)
	}
	if !strings.Contains(prompt, "Never name task 450 itself") {
		t.Errorf("prompt does not tell the session not to name itself: %q", prompt)
	}
	for _, want := range []string{"task 450", "depends_on", "an earlier proposal in this same run"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, prompt)
		}
	}

	// An ordinary task still gets the whole of the advice: it is the one
	// this was always right for.
	ordinary := model.Task{ID: "t1", Title: "Do the thing", Body: "details"}
	prompt = orchestrator.BuildPrompt(ordinary, "", "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if !strings.Contains(prompt, "task t1 itself, when") {
		t.Errorf("prompt no longer tells an ordinary task it may depend on itself: %q", prompt)
	}
	if strings.Contains(prompt, "Never name task") {
		t.Errorf("prompt warns an ordinary task off a link it may perfectly well make: %q", prompt)
	}
}

// Where a proposal lands by default is a fact about grain, not about the
// work, so a run has to be told it or it never reaches for propose_task's
// `repo` at all -- which is how eight proposals about grain's own code
// came to be filed against the state repository a run happened to be
// working in (grain/task-144).
func TestBuildPromptSaysWhichRepoAProposalIsFiledAgainst(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{"filed against acme/widgets", "`repo`"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not say where a proposal lands (%q): %q", want, prompt)
		}
	}

	// A task with no repo at all has no default to state: there is
	// nothing for a proposal to be filed against and nothing to name
	// instead.
	bare := orchestrator.BuildPrompt(model.Task{ID: "t2", Title: "Think", Body: "details"}, "", "",
		false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(bare, "the repository you are working in") {
		t.Errorf("prompt tells a task with no repo where its proposals land: %q", bare)
	}
}

// A task with more than one writeable repo has to be told the whole of
// how work reaches the others: which they are, which branch each takes,
// that this sandbox pushes to none of them, and that a proposal naming
// one is what gets a change made there (model.WriteTarget). A run told
// only "this task covers both repos" either burns its turns finding out
// that the second half is impossible or does the work somewhere it
// cannot push and loses it with the sandbox.
func TestBuildPromptNamesTheTasksOtherWriteableRepos(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
		Writes: []model.WriteTarget{
			{Repo: model.RepoRef{Owner: "acme", Name: "widgets-state"}, Base: "deploy"},
			{Repo: model.RepoRef{Owner: "acme", Name: "docs"}},
		},
	}
	prompt := orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{
		"acme/widgets-state (on branch `deploy`)",
		"acme/docs (on its default branch)",
		"propose_task",
		"repo",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, prompt)
		}
	}
	// Not offered as somewhere to push: the proxy refuses it, and a run
	// that tries has spent a turn finding that out.
	if !strings.Contains(prompt, "You cannot push to those from this sandbox") {
		t.Errorf("prompt does not say the other repos are not pushable: %q", prompt)
	}
	// Nothing about inheriting auto-merge for a task that has none to
	// pass on -- the same silence proposalSection keeps.
	if strings.Contains(prompt, "inherits this task's auto-merge") {
		t.Errorf("prompt offers auto-merge inheritance to a task that is not an auto-merge job: %q", prompt)
	}

	// An auto-merge task with a base of its own is told the exception
	// that goes with these repos: a proposal for one of them lands on a
	// branch a human did give it, so it does inherit
	// (model.LandsWhereGranted) -- which is the opposite of what the
	// paragraph just above it says about its own repository.
	task.AutoMerge, task.Base = true, "release/2.0"
	prompt = orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if !strings.Contains(prompt, "inherits this task's auto-merge") {
		t.Errorf("prompt does not tell an auto-merge job its cross-repo proposals inherit that: %q", prompt)
	}

	// And a single-repo task reads exactly as it always did.
	plain := model.Task{ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}
	if p := orchestrator.BuildPrompt(plain, "", "", false, orchestrator.DefaultMaxRunRuntime,
		orchestrator.History{}, nil); strings.Contains(p, "may land work in more than one repository") {
		t.Errorf("prompt talks about other writeable repos to a task that has none: %q", p)
	}
}

// The wall-clock budget is grain's own fact and unreachable from inside
// the sandbox: a run never told one has no reason to push before
// reaching for one more refactor, and salvagePushedBranch rescues only
// what was pushed. So the prompt states the number and what to do with
// it -- the second half being the part that changes what a run does.
func TestBuildPromptStatesTheRunsWallClockBudget(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, 90*time.Minute, orchestrator.History{}, nil)
	for _, want := range []string{"1h30m", "cancels this run", "push each piece"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, prompt)
		}
	}

	// The real default, rendered for a person rather than as Duration's
	// own "2h0m0s".
	if dflt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil); !strings.Contains(dflt, "2h ") {
		t.Errorf("prompt does not state the default budget as 2h: %q", dflt)
	}

	// A task with no repo is on the same clock and has no branch to push
	// -- its work product is the closing note comment_on_issue relays, so
	// that is what the paragraph tells it to write early.
	bare := orchestrator.BuildPrompt(model.Task{ID: "t2", Title: "Think", Body: "details"},
		"", "", false, 90*time.Minute, orchestrator.History{}, nil)
	if !strings.Contains(bare, "1h30m") || !strings.Contains(bare, "comment_on_issue") {
		t.Errorf("prompt does not give a task with no repo its budget and what to do with it: %q", bare)
	}
	if strings.Contains(bare, "push each piece") {
		t.Errorf("prompt tells a task with no repo to push: %q", bare)
	}

	// Zero is a caller with no budget to state, not a run that has
	// forever: no dispatch passes it (Config.maxRunRuntime substitutes
	// the default), and a paragraph promising unbounded time would be
	// worse than none.
	if none := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, 0, orchestrator.History{}, nil); strings.Contains(none, "cancels this run") {
		t.Errorf("prompt talks about a budget it was given none of: %q", none)
	}
}

// The budget paragraph states the rule the run is actually under, wait
// credit included. A run told "2h" that silently gets more still rations
// itself as if it had 2h -- and what it rations away is the wait on the
// push it has just made, which is the one thing the credit exists to pay
// for (orchestrator.RunWaitCreditCap).
func TestBuildPromptStatesTheWaitCreditAndTheCeiling(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{"wait_for_checks", "45m", "2h45m"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, prompt)
		}
	}

	// A caller with no budget to state gets no paragraph at all, and so
	// no offer of credit against a deadline nobody named.
	if none := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, 0,
		orchestrator.History{}, nil); strings.Contains(none, "wait_for_checks is not") {
		t.Errorf("prompt offers credit against a budget it was given none of: %q", none)
	}
}

// The dispatch half of the same fact: the number in the prompt is the
// one this run will actually be cancelled at, not the default written
// out somewhere it can go stale (cfg.maxRunRuntime, the very duration
// RunDispatch builds the agent's ctx with).
func TestRunDispatchTellsTheRunItsOwnConfiguredBudget(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var gotPrompt string
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		gotPrompt = cfg.Prompt
		return pushed(), nil
	})
	cfg := orchestrator.Config{MaxRunRuntime: 45 * time.Minute}
	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if !strings.Contains(gotPrompt, "45m") {
		t.Errorf("prompt = %q, want it to state this deployment's own 45m budget", gotPrompt)
	}
}

// Matched against the Reads section's own opening words rather than the
// bare substring "read", which this used to look for: every other
// sentence BuildPrompt writes is free to use the word for something
// else, and the first one that did (the pull_request_status paragraph:
// "see what GitHub's checks made of it") would have failed this test
// while the Reads section was correctly absent.
func TestBuildPromptOmitsReadsSectionWhenThereAreNone(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(prompt, "You may also read") {
		t.Fatalf("prompt mentions reading a repo with no Reads set: %q", prompt)
	}
}

// The repo a task works in is not one of the repos it may only read,
// however it got into that set -- a template's or a schedule's own
// Reads, a backport carrying the original's, or somebody typing it into
// the read-only repos box, none of which anything refuses. Listed
// there, the prompt told the agent to push to acme/widgets and, a few
// paragraphs later, never to push to acme/widgets.
func TestBuildPromptDoesNotOfferTheTargetAsAReadOnlyRepo(t *testing.T) {
	t.Parallel()
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &target,
		Reads: []model.RepoRef{
			// The same repository, spelled the way somebody typed it.
			{Owner: "Acme", Name: "Widgets"},
			{Owner: "acme", Name: "shared-lib"},
		},
	}
	prompt := orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	want := "You may also read acme/shared-lib for reference"
	if !strings.Contains(prompt, want) {
		t.Fatalf("prompt = %q, want it to carry %q", prompt, want)
	}
	if strings.Contains(prompt, "read Acme/Widgets") || strings.Contains(prompt, "read acme/widgets") {
		t.Fatalf("prompt offers the repo it works in as read-only: %q", prompt)
	}
}

// And with nothing but the target in Reads there is no sentence at all,
// rather than one naming an empty list.
func TestBuildPromptOmitsReadsSectionWhenOnlyTheTargetIsRead(t *testing.T) {
	t.Parallel()
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &target,
		Reads:  []model.RepoRef{target},
	}
	prompt := orchestrator.BuildPrompt(task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(prompt, "You may also read") {
		t.Fatalf("prompt mentions reading a repo when the only one is its own target: %q", prompt)
	}
}

func TestBuildPromptIncludesAssociatedIssue(t *testing.T) {
	t.Parallel()
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	taskWithIssue := model.Task{
		ID:     "t1",
		Title:  "Fix bug",
		Body:   "details",
		Target: &target,
		Issue:  42,
	}
	prompt := orchestrator.BuildPrompt(taskWithIssue, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if !strings.Contains(prompt, "This task is associated with GitHub issue #42.") {
		t.Fatalf("prompt does not mention associated issue: %q", prompt)
	}

	taskWithoutIssue := model.Task{
		ID:     "t2",
		Title:  "Fix bug",
		Body:   "details",
		Target: &target,
		Issue:  0,
	}
	promptWithout := orchestrator.BuildPrompt(taskWithoutIssue, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	if strings.Contains(promptWithout, "associated with GitHub issue") {
		t.Fatalf("prompt mentions associated issue when Issue == 0: %q", promptWithout)
	}
}

func TestRunDispatchMaterializesAppliesPromptsAndRevokesACapability(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1", model.Grant{Capability: "keyed", Via: model.GrantByLabel})
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	root := t.TempDir()
	var gotPrompt string
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		gotPrompt = cfg.Prompt
		return pushed(), nil
	})
	cap := &fakeCapability{name: "keyed", path: "/home/debian/.secret", content: "sh-sh-sh"}
	cfg := orchestrator.Config{Capabilities: model.NewCapabilityRegistry(cap)}

	result, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, root, "", nil, baseTime)
	if err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if result == nil {
		t.Fatal("expected a result")
	}

	if want := "capability keyed is ready"; !strings.Contains(gotPrompt, want) {
		t.Errorf("prompt %q does not mention %q", gotPrompt, want)
	}
	placed := filepath.Join(root, "home/debian/.secret")
	data, err := os.ReadFile(placed)
	if err != nil {
		t.Fatalf("placement was not written to %s: %v", placed, err)
	}
	if string(data) != "sh-sh-sh" {
		t.Errorf("placement content = %q", data)
	}

	if len(cap.revoked) != 1 || cap.revoked[0].Resource != "res-1" {
		t.Fatalf("revoked = %+v, want exactly one lease for res-1", cap.revoked)
	}
	live, err := store.LiveLeases(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Errorf("live leases after revoke = %+v, want none", live)
	}
}

func TestRunDispatchUsesAResourceReservedBeforeTheSandboxWithoutMintingItTwice(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1", model.Grant{Capability: "keyed", Via: model.GrantByLabel})
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	ran := false
	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		ran = true
		return pushed(), nil
	})
	cap := &scarceFakeCapability{&fakeCapability{
		name: "keyed", path: "/home/debian/.secret", content: "key",
	}}
	reg := model.NewCapabilityRegistry(cap)
	cc := model.CapabilityContext{Task: *task, Run: model.Run{ID: d.RunID, TaskID: d.TaskID}, Now: baseTime}
	resolved, err := model.ResolveGrants(ctx, reg, cc)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := model.MaterializeScarceGrants(ctx, reg, cc, resolved)
	if err != nil {
		t.Fatal(err)
	}
	cfg := orchestrator.Config{Capabilities: reg, PreparedCapabilities: reserved}

	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("worker failed with its reserved grant: %v", err)
	}
	if !ran || cap.attempts != 1 {
		t.Fatalf("agent ran = %v, capability attempts = %d; want one pre-sandbox mint", ran, cap.attempts)
	}
	if len(cap.revoked) != 1 {
		t.Fatalf("revoked leases = %+v, want the eventual grant released", cap.revoked)
	}
}

// TestRunDispatchPlacesAttachmentsAndMentionsThemInThePrompt covers
// bwsalmon/agents#522: a file the task carries (or one carried by a
// comment in its conversation) lands in the sandbox and is named in the
// prompt, even for a task with no capability Grants at all -- unlike a
// capability's own SideSandbox placement, an attachment is written via
// the slot's own write_file tool (orchestrator.placeAttachments), not
// applyPlacements, which is why this test passes real tools rather than
// the nil every other RunDispatch test in this file gets away with.
func TestRunDispatchPlacesAttachmentsAndMentionsThemInThePrompt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	if _, err := store.AddAttachment(ctx, model.Attachment{
		TaskID: "t1", Filename: "repro.zip", ContentType: "application/zip",
		Content: []byte("PK\x03\x04fake"), Size: 9, CreatedAt: baseTime,
	}); err != nil {
		t.Fatalf("adding attachment: %v", err)
	}
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	root := t.TempDir()
	var gotPrompt string
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		gotPrompt = cfg.Prompt
		return pushed(), nil
	})

	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, d, mcp.NewSandboxTools(root), root, "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	wantPath := orchestrator.AttachmentsDir + "/1-repro.zip"
	if !strings.Contains(gotPrompt, wantPath) {
		t.Errorf("prompt %q does not mention %q", gotPrompt, wantPath)
	}
	got, err := os.ReadFile(filepath.Join(root, wantPath))
	if err != nil {
		t.Fatalf("attachment was not written into the sandbox: %v", err)
	}
	if string(got) != "PK\x03\x04fake" {
		t.Errorf("attachment content = %q", got)
	}
}

// TestRunDispatchRecordsTheAgentsTranscript covers bwsalmon/agents#446:
// a framework's own agent.Result.Transcript should end up readable back
// off the store, against the task and attempt number RunDispatch was
// given -- the one thing FinishRun's own outcome/detail never carried.
func TestRunDispatchRecordsTheAgentsTranscript(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{
			FinalText:  "pushed the change",
			ToolCalls:  []agent.ToolCall{{Name: "run_command", Text: "ok"}},
			Transcript: "> run_command(...)\nok\n\npushed the change",
		}, nil
	})
	cfg := orchestrator.Config{}

	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	transcript, found, err := store.RunTranscript(ctx, "t1", 1)
	if err != nil || !found {
		t.Fatalf("RunTranscript: (%q, %v, %v)", transcript, found, err)
	}
	if !strings.Contains(transcript, "pushed the change") {
		t.Errorf("transcript = %q, want it to contain the agent's own transcript text", transcript)
	}
}

// TestRunDispatchRecordsThePromptItGaveTheAgent covers grain/task-91's
// half of "show the full prompt": whatever RunDispatch actually handed
// framework.Run has to be readable back off the store afterwards, since
// the prompt is assembled once from a task and a conversation that both
// move on afterwards, and nothing else records it.
//
// It is recorded before the agent's first turn, not after it returns, so
// a run still in flight can be asked what it was told -- which is what
// the agent's own view of the store proves here: by the time
// framework.Run is called, the row already carries it.
func TestRunDispatchRecordsThePromptItGaveTheAgent(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var gotPrompt, recordedDuringRun string
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		gotPrompt = cfg.Prompt
		recordedDuringRun, _, _ = store.RunPrompt(ctx, "t1", 1)
		return pushed(), nil
	})

	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	prompt, found, err := store.RunPrompt(ctx, "t1", 1)
	if err != nil || !found {
		t.Fatalf("RunPrompt: (%q, %v, %v)", prompt, found, err)
	}
	if prompt != gotPrompt {
		t.Errorf("recorded prompt = %q, want exactly what the agent was given: %q", prompt, gotPrompt)
	}
	if recordedDuringRun != gotPrompt {
		t.Errorf("prompt readable mid-run = %q, want it already recorded before the agent's first turn", recordedDuringRun)
	}
}

func TestRunDispatchFinishesTheRunAsFailedWhenACapabilityIsRefused(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1", model.Grant{Capability: "locked", Via: model.GrantByLabel})
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	ran := false
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		ran = true
		return pushed(), nil
	})
	cap := &fakeCapability{name: "locked", refuse: "not for you"}
	cfg := orchestrator.Config{Capabilities: model.NewCapabilityRegistry(cap)}

	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); err == nil {
		t.Fatal("expected RunDispatch to report the refusal")
	}
	if ran {
		t.Fatal("agent must not run when a capability was refused")
	}

	// A failed run still gets finished -- an unfinished one would hold
	// its slot forever.
	occupied, err := store.LiveRunCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if occupied != 0 {
		t.Errorf("occupied slots after a refused capability = %v, want none", occupied)
	}
	// The task does not go back to queued for a retry, though, which is
	// what it used to do: a refusal is a resolution rather than an
	// error, so the next attempt is refused identically and buys
	// nothing. It is parked on grain's own explanation instead
	// (orchestrator.noteRefusedGrant, grain/task-382), which any reply
	// lifts.
	state, err := store.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateAwaitingReply {
		t.Errorf("state = %q, want %q", state, model.StateAwaitingReply)
	}
}

// The other half of that ending, and the reason it exists: whoever is
// sitting in the task's chat has to be told. grain/task-382's operator
// ticked package-publish on an interactive task and watched two runs
// fail with a one-line error, each message they sent starting another
// run that died before an agent existed to answer them.
func TestRunDispatchExplainsARefusedCapabilityInTheTasksOwnConversation(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1", model.Grant{Capability: "locked", Via: model.GrantByLabel})
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	reason := "package-publish is a CI capability, not a task grant"
	cfg := orchestrator.Config{
		Capabilities: model.NewCapabilityRegistry(&fakeCapability{name: "locked", refuse: reason}),
	}
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return pushed(), nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); err == nil {
		t.Fatal("RunDispatch succeeded, want the refused capability to fail the run")
	}

	comments, err := store.Comments(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("task has %d comments, want grain's own explanation", len(comments))
	}
	body := comments[len(comments)-1].Body
	for _, want := range []string{"locked", reason} {
		if !strings.Contains(body, want) {
			t.Errorf("the task was told %q, which does not mention %q", body, want)
		}
	}
	// And parked on it, so the next thing that happens is a person
	// reading it rather than four more runs failing the same way.
	if pendingQuestion(t, ctx, store, "t1") == nil {
		t.Error("the task is not parked on that comment, so dispatch offers it again unchanged")
	}
	// The run's own row still carries the failure, unchanged: the
	// comment is where the reason is explained, not where it moved to.
	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != "failed" || !strings.Contains(runs[0].Detail, reason) {
		t.Fatalf("run row = %+v, want one failed run whose detail repeats the refusal", runs)
	}
}

// The line pkg/metrics splits a run's own duration at: everything before
// the agent's first turn is setup this deployment could speed up (a
// sandbox, a clone, a capability mint), and everything after it is the
// agent framework's own. Recorded by RunDispatch, because it is the only
// thing that knows the moment it hands the run over.
func TestRunDispatchRecordsWhenItsAgentStarted(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	// The run is dispatched at baseTime and its agent only gets going
	// four minutes later, which is exactly the gap worth measuring.
	agentStarted := baseTime.Add(4 * time.Minute)
	cfg := orchestrator.Config{Now: func() time.Time { return agentStarted }}
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return pushed(), nil
	})

	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	runs, err := store.RunTimings(ctx)
	if err != nil {
		t.Fatalf("RunTimings: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("read %d run timings, want 1", len(runs))
	}
	if runs[0].AgentStartedAt == nil || !runs[0].AgentStartedAt.Equal(agentStarted) {
		t.Errorf("AgentStartedAt = %v, want %v", runs[0].AgentStartedAt, agentStarted)
	}
}

// Which model a run is on is only settled once its framework is built --
// a task left to the deployment's default list or the rotation can be
// given a different one each attempt -- so RunDispatch records the one it
// was handed on the run's own row, where the attempts timeline reads it.
func TestRunDispatchRecordsTheModelItRanOn(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := modelAgent{agentFunc: func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return pushed(), nil
	}, model: "gemini-3.1-flash", framework: "antigravity"}
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v (%v), want one", runs, err)
	}
	if runs[0].Model != "gemini-3.1-flash" || runs[0].AgentFramework != "antigravity" {
		t.Errorf("run ran on %q/%q, want gemini-3.1-flash/antigravity", runs[0].Model, runs[0].AgentFramework)
	}
}

// modelAgent is an agentFunc on a connection that names its model and
// framework, as daemon.BackendFramework does.
type modelAgent struct {
	agentFunc
	model, framework string
}

func (m modelAgent) CredentialModel() string     { return m.model }
func (m modelAgent) CredentialFramework() string { return m.framework }

// A run whose capability is refused never reaches an agent at all, so
// there is no agent start to record -- and recording one anyway would
// report setup latency for a run that never finished setting up.
func TestRunDispatchRecordsNoAgentStartWhenTheAgentNeverRan(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1", model.Grant{Capability: "locked", Via: model.GrantByLabel})
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return pushed(), nil
	})
	cfg := orchestrator.Config{
		Capabilities: model.NewCapabilityRegistry(&fakeCapability{name: "locked", refuse: "not for you"}),
	}

	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); err == nil {
		t.Fatal("RunDispatch succeeded, want the refused capability to fail the run")
	}

	runs, err := store.RunTimings(ctx)
	if err != nil {
		t.Fatalf("RunTimings: %v", err)
	}
	if len(runs) != 1 || runs[0].AgentStartedAt != nil {
		t.Fatalf("runs = %+v, want one attempt with no agent start", runs)
	}
}

func TestRunDispatchFailsARunThatMadeNoToolCall(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{FinalText: "nothing to do here"}, nil
	})

	result, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, d, nil, t.TempDir(), "", nil, baseTime)
	if err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if result == nil || len(result.ToolCalls) != 0 {
		t.Fatalf("result = %+v, want the agent's own (tool-call-less) result back", result)
	}

	occupied, err := store.LiveRunCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if occupied != 0 {
		t.Errorf("occupied slots after a tool-call-less run = %v, want none", occupied)
	}
	state, err := store.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Errorf("state = %q, want %q (a failed run with nothing observed is eligible for retry)", state, model.StateQueued)
	}
}

// TestRunDispatchIncludesTheCommentThreadOnARedispatch is
// bwsalmon/agents#402's own scenario: a run parks on ask_question,
// ProcessResult relays that question into the store the same way a real
// dispatch would, a human answers it exactly as `grain comment` or the UI
// would, and the task's second dispatch must see both -- otherwise the
// redispatched agent has no way to know its question was already
// answered and asks it again, forever. This drives two full RunDispatch
// calls (with a real ProcessResult and a real store-backed comment
// between them) rather than calling BuildPrompt directly, since the bug
// this reproduces was in what RunDispatch's caller never did, not in
// BuildPrompt itself.
func TestRunDispatchIncludesTheCommentThreadOnARedispatch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	_ = sim
	task := dispatchTask(t, ctx, store, "t1")
	task.Target = &model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("re-filing task with a real target: %v", err)
	}

	d1 := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d1, baseTime)

	const question = "should the new field be snake_case or camelCase?"
	fw1 := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{ToolCalls: []agent.ToolCall{
			{Name: "ask_question", Arguments: map[string]any{"question": question}},
		}}, nil
	})
	got, err := store.GetTask(ctx, "t1")
	if err != nil || got == nil {
		t.Fatalf("reading task: %v", err)
	}
	result1, err := orchestrator.RunDispatch(ctx, store, fw1, orchestrator.Config{}, *got, d1, nil, t.TempDir(), "", nil, baseTime)
	if err != nil {
		t.Fatalf("first RunDispatch: %v", err)
	}
	if err := orchestrator.ProcessResult(ctx, store, client, *got, result1, d1.RunID, baseTime); err != nil {
		t.Fatalf("ProcessResult after the question: %v", err)
	}

	const answer = "snake_case, to match the rest of the schema"
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    "t1",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body:      answer,
		CreatedAt: baseTime.Add(time.Minute),
	}); err != nil {
		t.Fatalf("answering the question: %v", err)
	}

	d2 := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 2}
	startRun(t, ctx, store, d2, baseTime.Add(2*time.Minute))
	got2, err := store.GetTask(ctx, "t1")
	if err != nil || got2 == nil {
		t.Fatalf("reading task: %v", err)
	}

	var gotPrompt string
	fw2 := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		gotPrompt = cfg.Prompt
		return pushed(), nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw2, orchestrator.Config{}, *got2, d2, nil, t.TempDir(), "", nil, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("second RunDispatch: %v", err)
	}

	if !strings.Contains(gotPrompt, question) {
		t.Errorf("second prompt does not contain the question it asked itself: %q", gotPrompt)
	}
	if !strings.Contains(gotPrompt, answer) {
		t.Errorf("second prompt does not contain the human's answer: %q", gotPrompt)
	}
}

// TestRunDispatchOmitsTheCommentThreadOnAFirstDispatch checks that a
// task's first dispatch, with no conversation yet, gets exactly
// BuildPrompt's own prompt back -- no empty "conversation so far" section
// tacked onto every prompt whether or not there is anything to say.
func TestRunDispatchOmitsTheCommentThreadOnAFirstDispatch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var gotPrompt string
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		gotPrompt = cfg.Prompt
		return pushed(), nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if withoutClock(gotPrompt) != withoutClock(orchestrator.BuildPrompt(*task, "", "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)) {
		t.Errorf("prompt = %q, want exactly BuildPrompt's own prompt with no conversation yet", gotPrompt)
	}
}

// A redispatch used to be handed the task, the conversation, its
// attachments and a checkout continuing the previous attempt's branch,
// and told nothing whatever about the attempt that made those commits or
// how it ended -- docs/agent-ergonomics.md's finding 8. The store has
// had it all along: every task_run row carries outcome and detail, and
// since outcomeOf that detail describes a run that succeeded as well as
// one that failed.
func TestRunDispatchTellsARedispatchHowTheEarlierAttemptsEnded(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")

	first := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, first, baseTime)
	const detail = "grain cancelled this run after 2h0m of wall-clock time"
	if err := store.FinishRun(ctx, "r1", baseTime.Add(2*time.Hour), "cancelled", detail); err != nil {
		t.Fatalf("finishing the first attempt: %v", err)
	}

	second := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 2}
	startRun(t, ctx, store, second, baseTime.Add(3*time.Hour))
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var gotPrompt string
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		gotPrompt = cfg.Prompt
		return pushed(), nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, second, nil,
		t.TempDir(), "", nil, baseTime.Add(3*time.Hour)); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	for _, want := range []string{"attempted before", "you are attempt 2", "attempt 1", "cancelled", detail} {
		if !strings.Contains(gotPrompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, gotPrompt)
		}
	}
	// This run's own task_run row is already in the store when
	// RunDispatch reads the history (dispatch.Cycle writes it first), and
	// a prompt that listed it would be telling a run about itself -- with
	// an empty outcome, because it has not happened yet.
	if strings.Contains(gotPrompt, "attempt 2 ended") {
		t.Errorf("prompt describes the run reading it as one of its own predecessors: %q", gotPrompt)
	}
}

func TestBuildPromptKeepsAutomatedTaskUpdatesConcise(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("automated detail ", 40)
	human := strings.Repeat("human detail ", 40)
	relayed := strings.Repeat("agent detail ", 40)
	agent := model.Principal{Kind: model.PrincipalAgent, ID: "t1"}
	comments := []model.Comment{
		{Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}, Body: long},
		{Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}, Body: human},
		{Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}, OnBehalfOf: &agent}, Body: relayed},
	}
	prompt := orchestrator.BuildPrompt(model.Task{ID: "t1", Title: "test"}, "", "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{Comments: comments}, nil)

	if strings.Contains(prompt, long) || !strings.Contains(prompt, "automated detail automated detail") ||
		!strings.Contains(prompt, "...") {
		t.Errorf("automated update was not summarized: %q", prompt)
	}
	if !strings.Contains(prompt, human) {
		t.Errorf("human comment was shortened: %q", prompt)
	}
	if !strings.Contains(prompt, relayed) {
		t.Errorf("agent comment relayed by automation was shortened: %q", prompt)
	}
}

// The ordering the finding asks for: the attempt history and the
// conversation are the same kind of fact -- what has already happened to
// this task -- and both are read before the mechanics of pushing and
// watching CI. A section a run reaches after two paragraphs of push
// loop is one it reaches after deciding what to do.
func TestRunDispatchPutsWhatHappenedBeforeAheadOfTheMechanics(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    "t1",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body:      "the flake is in the fixture, not the test",
		CreatedAt: baseTime,
	}); err != nil {
		t.Fatalf("adding a comment: %v", err)
	}

	first := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, first, baseTime)
	if err := store.FinishRun(ctx, "r1", baseTime.Add(time.Hour), "failed", "the agent made no tool calls at all"); err != nil {
		t.Fatalf("finishing the first attempt: %v", err)
	}
	second := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 2}
	startRun(t, ctx, store, second, baseTime.Add(2*time.Hour))
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var gotPrompt string
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		gotPrompt = cfg.Prompt
		return pushed(), nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, second, nil,
		t.TempDir(), "", nil, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	attempts := strings.Index(gotPrompt, "attempted before")
	thread := strings.Index(gotPrompt, "Conversation on this task so far")
	mechanics := strings.Index(gotPrompt, "Push as often as you like")
	if attempts < 0 || thread < 0 || mechanics < 0 {
		t.Fatalf("prompt is missing one of the three sections (%d, %d, %d): %q",
			attempts, thread, mechanics, gotPrompt)
	}
	if !(attempts < thread && thread < mechanics) {
		t.Errorf("sections are ordered attempts=%d thread=%d mechanics=%d, want the history first: %q",
			attempts, thread, mechanics, gotPrompt)
	}
}

// TestRunDispatchLetsAFrameworkPollForACommentAddedMidRun is bwsalmon/
// agents#523's own scenario: a comment posted while a run is still live
// has to reach a Framework whose own loop polls for one
// (agent.RunConfig.Addenda), not just the task's next dispatch. fw here
// plays that Framework itself, polling before and after adding a comment
// mid-run to prove the second poll sees what the first could not have.
func TestRunDispatchLetsAFrameworkPollForACommentAddedMidRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	const addendum = "actually, use snake_case for the new field"
	var gotBefore, gotAfter []string
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		var err error
		if gotBefore, err = cfg.Addenda(ctx); err != nil {
			t.Fatalf("polling before the comment landed: %v", err)
		}
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID:    "t1",
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			Body:      addendum,
			CreatedAt: baseTime.Add(time.Minute),
		}); err != nil {
			t.Fatalf("adding a comment mid-run: %v", err)
		}
		if gotAfter, err = cfg.Addenda(ctx); err != nil {
			t.Fatalf("polling after the comment landed: %v", err)
		}
		return pushed(), nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if len(gotBefore) != 0 {
		t.Errorf("polled before any comment existed, got %v, want none", gotBefore)
	}
	if len(gotAfter) != 1 || !strings.Contains(gotAfter[0], addendum) {
		t.Errorf("polled after a comment landed mid-run, got %v, want one containing %q", gotAfter, addendum)
	}
}

// TestRunDispatchSeedsTheAddendaCursorPastCommentsAlreadyInThePrompt
// checks the other half of the same rule: on a redispatch, a comment
// already folded into the prompt by commentThreadSection must not also
// come back out of the first Addenda poll -- a Framework that acted on
// both would fold the same comment into its conversation twice.
func TestRunDispatchSeedsTheAddendaCursorPastCommentsAlreadyInThePrompt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    "t1",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body:      "already folded into the prompt",
		CreatedAt: baseTime,
	}); err != nil {
		t.Fatalf("adding a comment before dispatch: %v", err)
	}

	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var got []string
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		var err error
		if got, err = cfg.Addenda(ctx); err != nil {
			t.Fatalf("polling: %v", err)
		}
		return pushed(), nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Addenda returned a comment already folded into the prompt: %v", got)
	}
}

// closeTask marks id closed by writing task_observation directly, the
// same effect ui.Client.Close has -- restated here rather than importing
// pkg/ui, matching orchestrator_test.go's own "duplicated per file" style
// note on newSim.
func closeTask(t *testing.T, ctx context.Context, store *model.Store, id string, at time.Time) {
	t.Helper()
	if err := store.ObserveField(ctx, id, at, func(o *model.Observation) { o.ClosedAt = &at }); err != nil {
		t.Fatalf("closing task %s: %v", id, err)
	}
}

// TestRunDispatchCancelsTheAgentWhenItsTaskIsClosedMidFlight is
// bwsalmon/agents#346's own scenario: a task closed while its run is
// still live must actually stop that run's agent, not just prevent
// dispatch.Cycle from starting another one and ProcessResult from opening
// a pull request for it (tests/e2e/close_while_live_test.go already
// covered those two). fw here blocks on the very ctx RunDispatch hands
// framework.Run until it is cancelled, so this test only passes if
// closing the task mid-run actually reaches that ctx -- proving
// watchForTaskClosed's store-polled cancellation signal works end to
// end, deterministically and fast (CancelPollInterval is set to a few
// milliseconds), rather than relying on a real subprocess's own timing.
func TestRunDispatchCancelsTheAgentWhenItsTaskIsClosedMidFlight(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	started := make(chan struct{})
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		close(started)
		<-runCtx.Done()
		return nil, runCtx.Err()
	})
	cfg := orchestrator.Config{CancelPollInterval: 5 * time.Millisecond}

	type runOutcome struct {
		result *agent.Result
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)
		done <- runOutcome{result, err}
	}()

	<-started
	closeTask(t, ctx, store, "t1", baseTime)

	select {
	case out := <-done:
		if out.result != nil {
			t.Errorf("result = %+v, want nil for a run cancelled mid-flight", out.result)
		}
		if out.err == nil || !strings.Contains(out.err.Error(), "closed") {
			t.Errorf("RunDispatch err = %v, want an error naming the task's closure", out.err)
		}
	case <-time.After(runWaitLimit):
		t.Fatal("RunDispatch did not return after its task was closed mid-flight -- the agent's ctx was never cancelled")
	}

	occupied, err := store.LiveRunCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if occupied != 0 {
		t.Errorf("occupied slots after a cancelled run = %v, want none: FinishRun still frees the slot", occupied)
	}
}

// TestRunDispatchCancelsAnAgentThatOutlivesMaxRunRuntime is bwsalmon/
// agents#575's own regression test for the run-level wall-clock cap:
// v1 had AutomationConfig.max_runtime_minutes plus a sweeper for a run
// that is alive but stuck making no progress (e.g. a run_command with
// no timeout of its own, hung forever); v2 had nothing playing that
// role until Config.MaxRunRuntime. fw here blocks on the very ctx
// RunDispatch hands framework.Run until it is cancelled, exactly like
// TestRunDispatchCancelsTheAgentWhenItsTaskIsClosedMidFlight, except
// nothing ever closes the task here -- only MaxRunRuntime elapsing
// should end it.
func TestRunDispatchCancelsAnAgentThatOutlivesMaxRunRuntime(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	started := make(chan struct{})
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		close(started)
		<-runCtx.Done()
		return nil, runCtx.Err()
	})
	cfg := orchestrator.Config{MaxRunRuntime: 20 * time.Millisecond}

	type runOutcome struct {
		result *agent.Result
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)
		done <- runOutcome{result, err}
	}()

	<-started

	select {
	case out := <-done:
		if out.result != nil {
			t.Errorf("result = %+v, want nil for a run cancelled by MaxRunRuntime", out.result)
		}
		if out.err == nil || !strings.Contains(out.err.Error(), "wall-clock") {
			t.Errorf("RunDispatch err = %v, want an error naming the wall-clock limit", out.err)
		}
	case <-time.After(runWaitLimit):
		t.Fatal("RunDispatch did not return after MaxRunRuntime elapsed -- the agent's ctx was never cancelled")
	}

	occupied, err := store.LiveRunCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if occupied != 0 {
		t.Errorf("occupied slots after a MaxRunRuntime-cancelled run = %v, want none: FinishRun still frees the slot", occupied)
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want exactly one", runs)
	}
	if runs[0].Outcome != "cancelled" {
		t.Errorf("run outcome = %q, want \"cancelled\"", runs[0].Outcome)
	}
	if !strings.Contains(runs[0].Detail, "wall-clock") {
		t.Errorf("run detail = %q, want it to name the wall-clock limit", runs[0].Detail)
	}
}

// TestRunDispatchNeverLetsAnAlreadyClosedTaskReachARealToolCall is the
// race tests/e2e/close_while_live_test.go itself exercises:
// dispatch.Cycle claims a slot while a task is still running, the task
// is closed before RunDispatch ever gets called for that
// already-claimed run, and only then does RunDispatch actually run.
// Leaving this to watchForTaskClosed's own polling ticker would make
// whether the agent's first tool call ever reaches a real sandbox a
// race against CancelPollInterval; RunDispatch instead checks
// synchronously, before framework.Run is ever invoked, which this
// proves by checking that framework.Run's own ctx already reads
// cancelled the instant it starts, under a CancelPollInterval long
// enough that the watcher's first tick cannot be what cancelled it.
//
// An hour, rather than the default two seconds and a stopwatch: this
// test used to assert RunDispatch returned within 500ms, which measures
// how loaded the machine is rather than which code path cancelled the
// run, and it failed the `go` job on a busy CI host at 727ms
// (grain/task-426). An interval no test run can outlast rules the
// watcher out outright instead of racing it.
func TestRunDispatchNeverLetsAnAlreadyClosedTaskReachARealToolCall(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}
	closeTask(t, ctx, store, "t1", baseTime)

	sawCancelledCtx := false
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		sawCancelledCtx = runCtx.Err() != nil
		return nil, runCtx.Err()
	})

	// The watcher goroutine gets a tick an hour away, so nothing it does
	// can be mistaken for the synchronous check below.
	cfg := orchestrator.Config{CancelPollInterval: time.Hour}
	result, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)

	if result != nil {
		t.Errorf("result = %+v, want nil for a task closed before its run ever started", result)
	}
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("err = %v, want an error naming the task's closure", err)
	}
	if !sawCancelledCtx {
		t.Error("framework.Run's own ctx was not already cancelled when it started -- an already-closed task's first tool call could still reach a real sandbox")
	}
}

func TestRunDispatchCancelsLiveRunWhenTaskStopped(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	started := make(chan struct{})
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		close(started)
		<-runCtx.Done()
		return nil, runCtx.Err()
	})

	cancellers := orchestrator.NewTaskCancellers()
	cfg := orchestrator.Config{
		TaskCancellers: cancellers,
	}

	type outcome struct {
		result *agent.Result
		err    error
	}
	outc := make(chan outcome, 1)
	go func() {
		res, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)
		outc <- outcome{result: res, err: err}
	}()

	<-started
	if !cancellers.Cancel("t1", orchestrator.ErrTurnStopped) {
		t.Fatal("cancellers.Cancel returned false for live run")
	}

	select {
	case out := <-outc:
		if out.err == nil || !strings.Contains(out.err.Error(), "turn stopped by user") {
			t.Errorf("RunDispatch err = %v, want error naming turn stopped by user", out.err)
		}
		runs, err := store.Runs(ctx, "t1")
		if err != nil || len(runs) != 1 {
			t.Fatalf("reading runs: %v, len = %d", err, len(runs))
		}
		if runs[0].Outcome != "cancelled" {
			t.Errorf("run outcome = %q, want cancelled", runs[0].Outcome)
		}
		if runs[0].Detail != model.TurnStoppedDetail {
			t.Errorf("run detail = %q, want %q", runs[0].Detail, model.TurnStoppedDetail)
		}
	case <-time.After(runWaitLimit):
		t.Fatal("RunDispatch did not return after task was stopped")
	}
}

// TestRunDispatchGivesTheFrameworkATranscriptPathAndCleansItUp proves the
// two halves of bwsalmon/agents#467's wiring: RunDispatch tells the
// framework where to mirror a still-running run's own transcript (a file
// named after the run ID, under Config.TranscriptDir, matching what
// claude.LiveTranscriptDir would look for), and removes that file once
// the run is over -- its own store row already carries the same story by
// then (SetRunTranscript, just above in RunDispatch), so nothing is ever
// left behind for a finished run to read back out of the file.
func TestRunDispatchGivesTheFrameworkATranscriptPathAndCleansItUp(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	transcriptDir := t.TempDir()
	var gotPath string
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		gotPath = cfg.TranscriptPath
		if err := os.WriteFile(gotPath, []byte("still going"), 0o644); err != nil {
			t.Fatalf("writing transcript file: %v", err)
		}
		return pushed(), nil
	})
	cfg := orchestrator.Config{TranscriptDir: transcriptDir}

	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	if want := filepath.Join(transcriptDir, "r1"); gotPath != want {
		t.Errorf("TranscriptPath = %q, want %q", gotPath, want)
	}
	if _, err := os.Stat(gotPath); !os.IsNotExist(err) {
		t.Errorf("transcript file %s still exists after RunDispatch returned, want it removed", gotPath)
	}
}

// TestRunDispatchLeavesTranscriptPathEmptyWithoutATranscriptDir proves
// the opt-in half of the same wiring: a deployment that never sets
// Config.TranscriptDir gets the pre-bwsalmon/agents#467 behaviour back,
// exactly -- no file, no path, nothing for a framework that doesn't look
// at RunConfig.TranscriptPath to trip over.
func TestRunDispatchLeavesTranscriptPathEmptyWithoutATranscriptDir(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	gotPath := "unset"
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		gotPath = cfg.TranscriptPath
		return pushed(), nil
	})

	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if gotPath != "" {
		t.Errorf("TranscriptPath = %q, want empty with no Config.TranscriptDir set", gotPath)
	}
}

// finished_at used to be stamped with the same `at` RunCycle passed in as
// the run's StartedAt, so every run ever recorded read back as having
// taken zero seconds -- the UI's attempt timeline showed a run that had
// worked for an hour and one that died on its first turn identically,
// which is the first thing anyone wants to know about a failed run.
func TestRunDispatchRecordsWhenTheRunActuallyFinished(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{ToolCalls: []agent.ToolCall{{Name: "run_command", Text: "ok"}}}, nil
	})

	// baseTime is the fixed clock this package's tests dispatch against,
	// far from wall-clock now -- which is exactly what makes it able to
	// tell the two timestamps apart.
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs = (%+v, %v)", runs, err)
	}
	if runs[0].FinishedAt == nil {
		t.Fatal("FinishedAt = nil, want the run to be finished")
	}
	if !runs[0].FinishedAt.After(runs[0].StartedAt) {
		t.Errorf("FinishedAt (%s) is not after StartedAt (%s); the run's own duration is unreadable",
			runs[0].FinishedAt, runs[0].StartedAt)
	}
}

// A task filed as a live chat is a different job from every other
// dispatch, and the prompt has to say so: the run has somebody to ask,
// and the instinct every other prompt trains -- read the task, guess at
// the ambiguity, deliver -- is the wrong one here.
//
// It is gated on the same -server/-task pair open_pull_request is, since
// that is what registers the tool (pkg/cli/mcpserver.go): a paragraph
// naming a tool the roster does not have costs a run turns it cannot get
// back.
func TestBuildPromptTellsAnInteractiveRunToChat(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "pair on the flake", Body: "details", Interactive: true,
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", true,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
	for _, want := range []string{"`chat`", "live chat", "blocks until they reply"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not mention %q: %q", want, prompt)
		}
	}

	// Not for an ordinary task, which has nobody on the other end...
	background := task
	background.Interactive = false
	if got := orchestrator.BuildPrompt(background, orchestrator.CheckoutDir, "", true,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil); strings.Contains(got, "`chat`") {
		t.Errorf("prompt offers chat to a background run: %q", got)
	}
	// ...and not for a run whose framework has no daemon to reach, which
	// is a run with no chat tool registered.
	if got := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil); strings.Contains(got, "`chat`") {
		t.Errorf("prompt offers chat to a run that cannot reach the daemon: %q", got)
	}
}

// A repository that pairs branches tells every run not to merge one into
// the other (grain/task-1891). grain/task-1869 merged origin/candidate into
// a branch cut from main, which carried candidate's ports back onto main as
// duplicates.
func TestBuildPromptForbidsMergingOnePairedBranchIntoTheOther(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}
	prompt := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, orchestrator.DefaultMaxRunRuntime,
		orchestrator.History{PairedBranches: []string{"main", "candidate"}}, nil)
	for _, want := range []string{"`main` and `candidate` are paired", "do not merge one into the other", "ask_question", "propose_task a port"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not say %q: %q", want, prompt)
		}
	}

	three := orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, orchestrator.DefaultMaxRunRuntime,
		orchestrator.History{PairedBranches: []string{"main", "candidate", "release"}}, nil)
	if !strings.Contains(three, "`main`, `candidate` and `release` are paired") {
		t.Errorf("prompt does not name all three paired branches: %q", three)
	}

	// A repository pairing nothing, or only one branch, has nothing to
	// merge across, and a task with no repo has no branch at all.
	for name, p := range map[string]string{
		"unpaired": orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, orchestrator.DefaultMaxRunRuntime,
			orchestrator.History{}, nil),
		"one branch": orchestrator.BuildPrompt(task, orchestrator.CheckoutDir, "", false, orchestrator.DefaultMaxRunRuntime,
			orchestrator.History{PairedBranches: []string{"main"}}, nil),
		"no repo": orchestrator.BuildPrompt(model.Task{ID: "t2", Title: "Think", Body: "details"}, "", "", false,
			orchestrator.DefaultMaxRunRuntime, orchestrator.History{PairedBranches: []string{"main", "candidate"}}, nil),
	} {
		if strings.Contains(p, "are paired") {
			t.Errorf("%s: prompt talks about paired branches: %q", name, p)
		}
	}
}

// A run stopped by its daemon shutting down -- RunDispatch's own ctx
// cancelled, the way SIGTERM cancels the daemon's -- is recorded as
// model.InterruptedOutcome, charges the task nothing, and keeps the record
// of what it did: its transcript and its tool census are written on a
// context detached from the shutdown, like the outcome itself
// (docs/test-runs/2026-10-12-upgrade-drain-interrupted.md, finding 2).
func TestRunDispatchInterruptedByShutdownKeepsItsRecord(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	daemonCtx, shutdown := context.WithCancel(ctx)
	started := make(chan struct{})
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		close(started)
		<-runCtx.Done()
		return &agent.Result{
			ToolCalls:  []agent.ToolCall{{Name: "run_command", Text: "ok"}},
			Transcript: "> run_command(...)\nok\n",
		}, runCtx.Err()
	})
	done := make(chan error, 1)
	go func() {
		_, err := orchestrator.RunDispatch(daemonCtx, store, fw, orchestrator.Config{}, *task, d, nil, t.TempDir(), "", nil, baseTime)
		done <- err
	}()
	<-started
	shutdown()

	select {
	case err = <-done:
	case <-time.After(runWaitLimit):
		t.Fatal("RunDispatch did not return after its daemon's ctx was cancelled")
	}
	if err == nil || !strings.Contains(err.Error(), "the daemon driving this run was stopped") {
		t.Errorf("RunDispatch err = %v, want it to name the shutdown", err)
	}
	if err != nil && strings.Contains(err.Error(), "finishing run") {
		t.Errorf("RunDispatch err = %v, want no failure to finish the run's own row", err)
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs = %+v, %v; want the one run", runs, err)
	}
	if runs[0].Outcome != model.InterruptedOutcome || runs[0].FinishedAt == nil {
		t.Errorf("run = outcome %q finished %v, want a finished %q run", runs[0].Outcome, runs[0].FinishedAt, model.InterruptedOutcome)
	}
	streak, err := store.FailureStreak(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if streak != nil && (streak.Count != 0 || streak.BackoffCount != 0) {
		t.Errorf("failure streak = %+v, want an interrupted run to charge nothing", streak)
	}
	transcript, found, err := store.RunTranscript(ctx, "t1", 1)
	if err != nil || !found || !strings.Contains(transcript, "run_command") {
		t.Errorf("RunTranscript = (%q, %v, %v), want the interrupted run's transcript kept", transcript, found, err)
	}
	uses, err := store.RunToolUses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var census int
	for _, u := range uses {
		if u.RunID == "r1" {
			census++
		}
	}
	if census == 0 {
		t.Errorf("tool census for r1 = %+v, want the interrupted run's one tool call recorded", uses)
	}
}
