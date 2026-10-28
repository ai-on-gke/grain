// TestCLIAttachedCapabilityIsMaterializedAppliedAndRevokedThroughRunCycle is
// bwsalmon/agents#329's own scenario: prepareCapabilities -> applyPlacements
// -> agent run -> revokeAll, exercised through a real dispatch -> agent ->
// gitproxy-free push pipeline instead of pkg/orchestrator/run_test.go's own
// TestRunDispatchMaterializesAppliesPromptsAndRevokesACapability, which
// proves the same four moments without ever going through a real
// orchestrator.RunCycle or a real grain CLI subprocess.
//
// The capability itself is a small stand-in registered by this test, not
// the real gcpkey/geminikey providers -- those need real GCP/Gemini
// credentials this sandbox should not assume it has. It is attached to the
// task through the real CLI's `capability <id> <cap> attach`, under the one
// capability ID (self-debug) cmd/grain/main.go's ui.OfferedCapabilities
// allow-list accepts that needs no such credential, exactly the way an
// operator would toggle it on -- the orchestrator.Config.Capabilities
// registry that actually resolves and materializes it, by contrast, is
// this test's own, so nothing here ever calls out to GCP or Gemini.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// testCapabilityProvider is a model.CapabilityProvider standing in for
// gcpkey/geminikey, mirroring the stand-ins pkg/model/capability_test.go
// already builds: Materialize mints one SideSandbox placement and a Lease,
// and Revoke records that it was actually called.
type testCapabilityProvider struct {
	name, path, content string

	mu      sync.Mutex
	revoked bool
}

func (p *testCapabilityProvider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{Name: p.name, Provision: model.ProvisionMint}
}

func (p *testCapabilityProvider) Resolve(context.Context, model.CapabilityContext) (model.Resolution, error) {
	return model.Honoured(), nil
}

func (p *testCapabilityProvider) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	return model.Materialization{
		Lease: &model.Lease{
			Capability: p.name, Resource: "test-resource",
			MintedBy: model.CredentialRef{Name: "test"}, IssuedAt: cc.Now,
		},
		Placements: []model.Placement{
			{Side: model.SideSandbox, Path: p.path, Content: p.content},
		},
	}, nil
}

func (p *testCapabilityProvider) PromptSection(context.Context, model.CapabilityContext, []model.Placement) (string, error) {
	return "capability " + p.name + " is ready", nil
}

func (p *testCapabilityProvider) Revoke(context.Context, model.CapabilityContext, model.Lease) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revoked = true
	return nil
}

func (p *testCapabilityProvider) wasRevoked() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.revoked
}

// capabilityPushScript is pushScript (harness_test.go) with one extra tool
// call spliced in front of it: a run_command that reads placementPath back
// and checks its content, proving applyPlacements really wrote the
// capability's material into the sandbox root before the agent ever
// touches git, not merely that the run went on to succeed regardless.
// placementPath is always absolute, as a real provider's Path always is
// (see applyPlacements' own doc comment); the command reads it relative to
// the sandbox root instead, since run_command's cmd.Dir is root and an
// absolute path in the command string would instead name a real path on
// this test process's own host.
func capabilityPushScript(remote, branch, taskID, placementPath, wantContent string) []antigravity.Step {
	relPath := strings.TrimPrefix(placementPath, "/")
	readBack := fmt.Sprintf("test \"$(cat %s)\" = %q", relPath, wantContent)
	push := "git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " && " +
		"echo 'change for " + taskID + "' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": readBack}),
		toolCall("run_command", map[string]any{"command": push}),
		finalText("read the capability's placement and pushed " + branch),
	}
}

func TestCLIAttachedCapabilityIsMaterializedAppliedAndRevokedThroughRunCycle(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)

	const owner, repoName = "acme", "widgets"
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "NOTES.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "NOTES.md")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)

	// Step 1: file the task, then attach a capability to it, both through
	// the real CLI binary -- not a seeded store row and not a hand-built
	// Grant -- the way an operator actually would. self-debug is the
	// capability ID because it is on cmd/grain/main.go's own
	// ui.OfferedCapabilities allow-list and needs no real credential; the
	// registry that actually resolves and materializes it below is this
	// test's own, never the real one a deployment would wire up for it.
	storeDir := t.TempDir()
	created := runCLIStore(t, bin, storeDir,
		"-json",
		"create",
		"-title", "add a NOTES entry",
		"-body", "please add a line to NOTES.md",
		"-repo", owner+"/"+repoName,
		"-approve",
	)
	var task ui.Task
	if err := json.Unmarshal([]byte(created), &task); err != nil {
		t.Fatalf("parsing grain create -json output: %v\n%s", err, created)
	}
	if task.ID == "" {
		t.Fatalf("grain create did not return a task id: %s", created)
	}

	const capabilityID = "self-debug"
	attached := runCLIStore(t, bin, storeDir, "-json", "capability", task.ID, capabilityID, "attach")
	var afterAttach ui.Task
	if err := json.Unmarshal([]byte(attached), &afterAttach); err != nil {
		t.Fatalf("parsing grain capability attach -json output: %v\n%s", err, attached)
	}
	attachedOK := false
	for _, c := range afterAttach.Capabilities {
		if c == capabilityID {
			attachedOK = true
		}
	}
	if !attachedOK {
		t.Fatalf("capabilities after attach = %v, want %q among them", afterAttach.Capabilities, capabilityID)
	}

	// Step 2: dispatch it through a real RunCycle. reconcileDispatch reads
	// the Grant the CLI just wrote, RunDispatch resolves and materializes
	// it against this test's own provider, applyPlacements writes its
	// lease into the sandbox root before the agent's first turn, the
	// scripted agent reads that placement back and then pushes as normal,
	// and revokeAll tears the lease down again once the run finishes.
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	sandboxes := credentialed(t, remote)

	const placementPath = "/secrets/self-debug-token"
	const placementContent = "sh-sh-sh-e2e"
	provider := &testCapabilityProvider{name: capabilityID, path: placementPath, content: placementContent}

	branch := model.BranchName(task.ID)
	client := github.NewClient(sim, nil)
	deps := orchestrator.Deps{
		Client: client, Sandboxes: sandboxes, MaxWorkers: 1,
		Framework: scriptedFramework(capabilityPushScript(remote, branch, task.ID, placementPath, placementContent)),
		Config:    orchestrator.Config{Capabilities: model.NewCapabilityRegistry(provider)},
	}

	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
			t.Fatalf("RunCycle (dispatch): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateAwaitingSubmit {
			t.Fatalf("state after the agent's push = %q, want awaiting_submit: the pull request is open and unsubmitted", st)
		}

		live, err := store.LiveLeases(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(live) != 0 {
			t.Errorf("live leases after the run = %+v, want none -- store.DropLease should have cleared it", live)
		}
	})

	if !provider.wasRevoked() {
		t.Error("provider.Revoke was never called -- revokeAll should have called it once the run finished")
	}

	// The placement really did land under the sandbox root, not just get
	// read back successfully by the scripted agent's own bash -- confirmed
	// independently of the agent's tool calls, the same way harness_test.go's
	// own assertions read back the upstream repo rather than trusting the
	// agent's report of what it did.
	placed := filepath.Join(sandboxes.rootOf(task.ID+"-1"), strings.TrimPrefix(placementPath, "/"))
	data, err := os.ReadFile(placed)
	if err != nil {
		t.Fatalf("placement was not written to %s: %v", placed, err)
	}
	if string(data) != placementContent {
		t.Errorf("placement content = %q, want %q", data, placementContent)
	}

	if sim.pullRequestCount() != 1 {
		t.Fatalf("expected grain to have opened one pull request, got %d", sim.pullRequestCount())
	}
}

// TestRefusedCapabilityGrantFailsTheRunBeforeTheAgentStartsAndParksTheTask
// is bwsalmon/agents#330, reached from a real dispatch.Cycle decision
// against this package's own store/git rig: a refused model.Grant fails
// the run before the agent exists, and pushes no branch.
// pkg/orchestrator/run_test.go's
// TestRunDispatchFinishesTheRunAsFailedWhenACapabilityIsRefused proves
// the refusal itself, directly against orchestrator.RunDispatch.
//
// It used to end where TestFailedRunReturnsTaskToQueueForRetry does, in
// a requeue -- which is right for a run that failed at something a
// second attempt might do differently, and wrong for this: a refusal is
// the provider's settled answer about this task, so the retries only
// spent attempts on it and told nobody anything. As of grain/task-382
// the task is parked on grain's own explanation instead, which any
// reply lifts.

// refusingCapability is a model.CapabilityProvider whose Resolve always
// refuses, mirroring pkg/model/capability_test.go's own
// mintCapability{refuse: ...} stand-in.
type refusingCapability struct {
	model.BaseCapability
	name   string
	reason string
}

func (c refusingCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{Name: c.name, Label: "grain-" + c.name, Source: model.GrantByLabel, Provision: model.ProvisionGrant}
}

func (c refusingCapability) Resolve(context.Context, model.CapabilityContext) (model.Resolution, error) {
	return model.RefusedBecause(c.reason), nil
}

// panicIfRun is an agent.Framework that panics the moment Run is called --
// prepareCapabilities' own doc comment says a refused grant "must not run
// [the agent] at all," so this framework makes the test fail loudly,
// rather than quietly pass, if that short-circuit ever slips.
type panicIfRun struct{}

func (panicIfRun) Run(context.Context, agent.RunConfig) (*agent.Result, error) {
	panic("agent framework Run must not be called when a capability grant was refused")
}

func TestRefusedCapabilityGrantFailsTheRunBeforeTheAgentStartsAndParksTheTask(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.newRepo("acme", "widgets")

	clock := baseTime
	task := fileIssue(w, "iss-cap", human("dave"), model.RepoRef{Owner: "acme", Name: "widgets"})
	task.Grants = []model.Grant{{Capability: "locked", Via: model.GrantByLabel}}
	if err := w.store.PutTask(w.ctx, task); err != nil {
		t.Fatalf("attaching a grant to iss-cap: %v", err)
	}
	assertState(w, "iss-cap", model.StateQueued, false)

	dispatches, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, clock)
	if err != nil || len(dispatches) != 1 || dispatches[0].TaskID != "iss-cap" {
		t.Fatalf("Cycle: %v, %+v", err, dispatches)
	}
	d := dispatches[0]
	assertState(w, "iss-cap", model.StateRunning, true)

	dispatched, err := w.store.GetTask(w.ctx, "iss-cap")
	if err != nil || dispatched == nil {
		t.Fatalf("reading dispatched task: %v (nil=%v)", err, dispatched == nil)
	}

	cap := refusingCapability{name: "locked", reason: "not for you"}
	// Now: this test's own clock, not the wall clock -- dispatch's retry
	// backoff compares a run's finished_at against the clock the next
	// Cycle is given, and the two have to be on the same timeline (see
	// orchestrator.Config.Now).
	cfg := orchestrator.Config{
		Capabilities: model.NewCapabilityRegistry(cap),
		Now:          func() time.Time { return clock },
	}

	clock = clock.Add(time.Minute)
	if _, err := orchestrator.RunDispatch(w.ctx, w.store, panicIfRun{}, cfg, *dispatched, d, nil, w.prepareSandbox(d), "", nil, clock); err == nil {
		t.Fatal("expected RunDispatch to report the refusal")
	}

	branch := model.BranchName("iss-cap")
	if w.branchExists("acme", "widgets", branch) {
		t.Fatal("a refused capability grant must never push a branch")
	}

	occupied, err := w.store.LiveRunCount(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if occupied != 0 {
		t.Errorf("occupied slots after a refused capability = %v, want none", occupied)
	}

	// Parked on grain's own account of the refusal, rather than offered
	// again straight away (grain/task-382). The refusal is a resolution:
	// the provider looked at this task and said no, and it says the same
	// no to every later attempt, so the retries this used to take bought
	// nothing and -- on an interactive task -- answered the person in the
	// chat with a one-line failure and nothing else.
	obs, err := w.store.GetObservation(w.ctx, "iss-cap")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID == nil {
		t.Fatalf("observation after a refused capability = %+v, want the task parked on grain's explanation", obs)
	}
	comments, err := w.store.Comments(w.ctx, "iss-cap")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) == 0 {
		t.Fatal("a refused capability left no comment on the task, so nobody is told why its runs fail")
	}
	body := comments[len(comments)-1].Body
	for _, want := range []string{"locked", "not for you"} {
		if !strings.Contains(body, want) {
			t.Errorf("the task was told %q, which does not name %q", body, want)
		}
	}
	assertState(w, "iss-cap", model.StateAwaitingReply, false)

	clock = clock.Add(time.Minute)
	if parked, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, clock); err != nil || len(parked) != 0 {
		t.Fatalf("parked Cycle: %v, %+v, want nothing dispatched while the refusal stands", err, parked)
	}

	// And a reply lifts it, exactly as it lifts every other park -- the
	// one gesture a human makes for a task waiting on them. The grant is
	// still refused, so the next attempt fails in the same place; what
	// matters here is that the task is dispatchable again at all, since
	// a task nobody could restart would be a worse ending than the
	// retries this replaced.
	if err := w.store.ObserveField(w.ctx, "iss-cap", clock, func(o *model.Observation) {
		o.PendingQuestionCommentID = nil
	}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	second, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, clock)
	if err != nil || len(second) != 1 || second[0].Attempt != 2 {
		t.Fatalf("retry Cycle: %v, %+v, want attempt 2", err, second)
	}
}
