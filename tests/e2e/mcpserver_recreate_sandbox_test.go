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

// This file drives recreate_sandbox the way a dispatched run really
// does: a real `grain mcpserver -server <url> -task <id>` subprocess,
// asked over real stdio MCP to throw its sandbox away, against a real
// ui.Server whose Config.SandboxRecreate is a real
// orchestrator.SandboxRecreations -- with a real run registered in it by
// a real RunCycle, holding a real HostSandboxes directory with a real
// clone of a real repo, fetched through this world's real git proxy. So
// what comes back is a sandbox that has actually been destroyed and
// rebuilt, rather than a fake's canned account of one.
//
// Every layer of that chain is already covered on its own:
// pkg/mcp/recreate_sandbox_tool_test.go renders a report from a fake
// recreator, pkg/ui/sandbox_recreate_test.go carries a fake's answer over
// HTTP, and pkg/orchestrator/recreate_internal_test.go runs the restore
// steps against a real host sandbox and a real git remote. What none of
// them touch is the join, and the join is where the untested couplings
// are: the report's JSON round trip (Restored and Warnings are omitempty
// slices, so "grain put nothing back" and "grain put three things back"
// are the same shape on the wire until something decodes one), the
// shape a real HTTP client sees for a refusal as against a 404, and
// pkg/cli/mcpserver.go's daemonSandbox converting ui.SandboxRecreation
// into mcp.SandboxRecreationReport field for field with no compiler
// anywhere near the two ends.
//
// It also asserts the one claim the whole feature rests on and which
// only a real subprocess can show: that the tools a run was already
// holding still reach its sandbox afterwards. The mcpserver here is
// started before the rebuild and used after it -- to push from the
// restored clone with the re-minted credentials, through the same proxy
// -- because a rebuild that leaves the run unable to work in what came
// back would be no better than the wedged sandbox it replaced.
// pkg/orchestrator/granule_docker_real_test.go makes the same argument for
// the other backend, where the address is a VM's container name rather
// than a directory path.
//
// ui.Config.SandboxRecreate is wired to a recreator of this file's own
// rather than to cmd/grain's sandboxRecreateAdapter, which is package
// main and not importable. That conversion is covered where it lives, in
// cmd/grain; what this file is for is everything outside that process.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// registryBackedRecreator is ui.Config.SandboxRecreate over the real
// registry, standing in for the daemon's own sandboxRecreateAdapter
// (pkg/cli/daemon.go, package main and so out of reach from here). It
// converts the same way that one does, for the same reason: pkg/ui does
// not import pkg/orchestrator, so somebody has to.
type registryBackedRecreator struct {
	recreations *orchestrator.SandboxRecreations
}

func (r registryBackedRecreator) RecreateForTask(ctx context.Context, taskID string) (ui.SandboxRecreation, error) {
	recreation, err := r.recreations.Recreate(ctx, taskID)
	if err != nil {
		return ui.SandboxRecreation{}, err
	}
	return ui.SandboxRecreation{
		Sandbox:     recreation.Sandbox,
		CheckoutDir: recreation.CheckoutDir,
		Restored:    recreation.Restored,
		Warnings:    recreation.Warnings,
	}, nil
}

// pausedFramework is the agent turn this test needs: one that hands its
// RunConfig to the test and then holds the run open until the test lets
// go.
//
// A sandbox exists only while its run does, so every assertion here has
// to happen mid-run; and the dispatch goroutine is not the test's own, so
// a scripted agent doing this work would be making its assertions
// somewhere t.Fatalf does not end the test (it calls runtime.Goexit on
// whichever goroutine it is on). Pausing hands both problems back to the
// test goroutine, where the whole live phase reads as ordinary
// straight-line code.
type pausedFramework struct {
	live    chan agent.RunConfig
	release chan struct{}
}

func (f pausedFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	select {
	case f.live <- cfg:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &agent.Result{FinalText: "rebuilt my sandbox and pushed from the clone grain put back"}, nil
}

// callRecreateSandbox makes the call and fails the test if it did not
// round-trip at all. Whether the *answer* is an error is left to the
// caller: two of the checks below want an error answer and two want a
// real report, and all four are the tool answering rather than the
// process dying.
func callRecreateSandbox(t *testing.T, p *mcpProcess) *mcp.CallResult {
	t.Helper()
	res, err := p.CallTool(context.Background(), "recreate_sandbox", map[string]any{})
	if err != nil {
		t.Fatalf("tools/call recreate_sandbox: %v\nstderr:\n%s", err, p.stderr.String())
	}
	return res
}

func TestMCPServerRecreatesARealSandboxOverStdio(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	bin := buildGrainBinary(t)
	w := newWorld(t) // skips if git is missing

	const owner, repoName = "acme", "widgets"
	const taskID = "t-recreate"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}
	branch := model.BranchName(taskID)
	sandboxName := dispatch.RunID(taskID, 1)

	fileIssue(w, taskID, human("dana"), repo)

	// An attachment, so the report has more than one thing in Restored:
	// an omitempty slice with one element and one with three are the same
	// shape on the wire, and only a decode at the other end tells them
	// apart. It is also the one restored piece whose *contents* can be
	// checked byte for byte on the far side of the rebuild.
	const attached = "the spec this task was filed with\n"
	attachmentID, err := w.store.AddAttachment(w.ctx, model.Attachment{
		TaskID: taskID, Filename: "spec.txt", ContentType: "text/plain",
		Size: int64(len(attached)), Content: []byte(attached), CreatedAt: baseTime,
	})
	if err != nil {
		t.Fatalf("attaching a file to %s: %v", taskID, err)
	}
	attachmentPath := filepath.Join(
		orchestrator.AttachmentsDir, fmt.Sprintf("%d-spec.txt", attachmentID))

	// The daemon the subprocess is pointed at: a real ui.Server with a
	// real registry behind Config.SandboxRecreate, which is exactly the
	// arrangement `grain daemon` builds (sandboxRecreateAdapter over the
	// orchestrator.SandboxRecreations it also hands the cycle).
	recreations := orchestrator.NewSandboxRecreations()
	daemonURL := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor:           ui.DefaultActor("operator"),
		Capabilities:    ui.OfferedCapabilities(),
		SandboxRecreate: registryBackedRecreator{recreations},
	}), w.store))

	sim := githubsim.New(owner, repoName, filepath.Join(w.upstreamDir, owner, repoName+".git"), "main")

	// Sandboxes is HostSandboxes itself, not this package's
	// credentialedSandboxes wrapper: a rebuild is an optional interface
	// (orchestrator.SandboxRebuilder) that runOne's registration carries
	// through untouched, and a wrapper that does not forward it would
	// leave this test asserting against a sandbox that cannot rebuild
	// itself. The git identity and proxy credential come from
	// MintSandboxToken instead, which is where a deployment's come from.
	fw := pausedFramework{live: make(chan agent.RunConfig), release: make(chan struct{})}
	deps := orchestrator.Deps{
		Store:      w.store,
		Client:     github.NewClient(sim, nil),
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		MaxWorkers: 1,
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return fw, nil
		},
		Config: orchestrator.Config{
			GitRemoteBase:      w.proxyURL,
			SandboxRecreations: recreations,
		},
		MintSandboxToken: w.tokens.EnsureToken,
	}

	// Runs is nil, so RunCycle waits for the dispatch it starts -- which
	// is why it needs a goroutine here: the run has to still be in flight
	// while the assertions below run against its sandbox.
	var cycleErr error
	cycleDone := make(chan struct{})
	go func() {
		defer close(cycleDone)
		cycleErr = orchestrator.RunCycle(w.ctx, deps, baseTime)
	}()

	var releaseOnce sync.Once
	finishRun := func() {
		releaseOnce.Do(func() { close(fw.release) })
		select {
		case <-cycleDone:
		case <-time.After(2 * time.Minute):
			t.Error("the dispatch did not finish within 2 minutes of the agent's turn being released")
		}
	}
	// Registered as a cleanup as well as called on the happy path below:
	// an assertion that fails with t.Fatalf would otherwise leave the
	// dispatch goroutine parked on fw.release forever, holding a sandbox
	// under a TempDir this test is on its way out of.
	t.Cleanup(finishRun)

	var cfg agent.RunConfig
	select {
	case cfg = <-fw.live:
	case <-cycleDone:
		t.Fatalf("the cycle ended without ever dispatching %s: %v", taskID, cycleErr)
	case <-time.After(2 * time.Minute):
		t.Fatal("no run became live within 2 minutes")
	}

	root := cfg.SandboxRoot
	if root == "" {
		t.Fatal("the run was given no sandbox directory, so there is nothing to rebuild")
	}
	work := filepath.Join(root, orchestrator.CheckoutDir)
	if _, err := os.Stat(filepath.Join(work, ".git")); err != nil {
		t.Fatalf("the run started without a checkout, so a restored one would prove nothing: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, attachmentPath)); err != nil || string(got) != attached {
		t.Fatalf("the run started without its attachment at ./%s (%q, err %v)", attachmentPath, got, err)
	}

	// What the agent broke: something of its own beside the checkout, and
	// something inside it. Neither may survive. The attachment is removed
	// too, so "it is there afterwards" means grain put it back rather
	// than that it was never touched.
	junk := filepath.Join(root, "wedged.lock")
	if err := os.WriteFile(junk, []byte("a lock nothing in here can clear"), 0o644); err != nil {
		t.Fatal(err)
	}
	halfWritten := filepath.Join(work, "half-written.txt")
	if err := os.WriteFile(halfWritten, []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, attachmentPath)); err != nil {
		t.Fatal(err)
	}

	// The subprocess is started *before* the rebuild and used after it,
	// which is the point: a run's tools address its sandbox by name, so
	// the ones it already holds -- in a separate process nothing here
	// could reach to replace -- must reach whatever comes back.
	p := startMCPServer(t, bin, "-sandbox-root", root, "-server", daemonURL, "-task", taskID)

	res := callRecreateSandbox(t, p)
	if res.IsError {
		t.Fatalf("recreate_sandbox answered with an error: %q\nstderr:\n%s", res.Text(), p.stderr.String())
	}
	report := res.Text()
	for _, want := range []string{
		// The sandbox's own name, unchanged by the rebuild -- the whole
		// reason the tools above still work.
		"Sandbox " + sandboxName + " has been destroyed and rebuilt",
		"its git credentials for grain's git proxy",
		"this task's 1 attachment(s), under ./" + orchestrator.AttachmentsDir,
		"a fresh clone of " + owner + "/" + repoName + " at ./" + orchestrator.CheckoutDir,
		"with " + branch + " checked out",
		"Start again from ./" + orchestrator.CheckoutDir,
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report is missing %q; full report:\n%s", want, report)
		}
	}
	// Nothing failed, so the report must not carry a warnings section --
	// an empty Warnings slice that survived the wire as a one-element one
	// would read to the agent as a sandbox it cannot trust.
	if strings.Contains(report, "could not put back") {
		t.Errorf("the report has a warnings section though nothing failed; full report:\n%s", report)
	}

	// The sandbox itself, not the sentence about it.
	for _, path := range []string{junk, halfWritten} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived the rebuild (err = %v), want it destroyed", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(work, ".git")); err != nil {
		t.Fatalf("the repo was not cloned again: %v", err)
	}
	if got := strings.TrimSpace(runOutput(t, work, "git", "rev-parse", "--abbrev-ref", "HEAD")); got != branch {
		t.Errorf("the fresh clone is on %q, want the task's own branch %q", got, branch)
	}
	if got, err := os.ReadFile(filepath.Join(root, attachmentPath)); err != nil || string(got) != attached {
		t.Errorf("the attachment was not put back at ./%s (%q, err %v)", attachmentPath, got, err)
	}

	// The credentials are only restored if the run can actually push with
	// them, so this pushes -- through the tools the run was already
	// holding, from the clone grain put back, over this world's real git
	// proxy, which authorizes by the token that was minted again for this
	// same sandbox name.
	pushed, err := p.CallTool(context.Background(), "run_command", map[string]any{
		"command": "cd " + orchestrator.CheckoutDir + " && " +
			"echo 'change for " + taskID + "' > NOTES-" + taskID + ".md && " +
			"git add NOTES-" + taskID + ".md && " +
			"git commit -q -m 'from the rebuilt sandbox' && " +
			"git push -q origin " + branch,
	})
	if err != nil {
		t.Fatalf("tools/call run_command after the rebuild: %v\nstderr:\n%s", err, p.stderr.String())
	}
	if pushed.IsError {
		t.Fatalf("committing and pushing from the rebuilt sandbox failed: %s", pushed.Text())
	}
	if !w.branchExists(owner, repoName, branch) {
		t.Fatalf("%s never reached the remote, so the git credentials grain reported putting back do not work", branch)
	}

	// And what "including anything you had already pushed" means: a
	// second rebuild, immediately after that push, comes back with the
	// commit on it. This is the difference between a rebuild costing a
	// run its unpushed work and costing it everything.
	if again := callRecreateSandbox(t, p); again.IsError {
		t.Fatalf("the second recreate_sandbox answered with an error: %q", again.Text())
	}
	pushedBack := filepath.Join(work, "NOTES-"+taskID+".md")
	if got, err := os.ReadFile(pushedBack); err != nil || !strings.Contains(string(got), "change for "+taskID) {
		t.Errorf("the re-clone does not carry the commit this run had already pushed (%q, err %v)", got, err)
	}

	// Let the agent's turn end, and let grain finish the run the ordinary
	// way: the branch is on the remote, so this is a completion with a
	// pull request, from work done entirely inside a sandbox that was
	// destroyed mid-run.
	finishRun()
	if cycleErr != nil {
		t.Fatalf("RunCycle: %v", cycleErr)
	}
	assertState(w, taskID, model.StateAwaitingSubmit, false)
	if len(sim.PullRequests) != 1 {
		t.Errorf("expected one pull request for the work pushed from the rebuilt sandbox, got %+v", sim.PullRequests)
	}

	// The run is over, so its sandbox is gone -- and the tool says so in
	// the registry's own words rather than failing as a transport error.
	// This is the answer a run gets after it has been cancelled, and the
	// one shape of refusal an agent can actually act on.
	ended := callRecreateSandbox(t, p)
	if !ended.IsError {
		t.Fatalf("recreate_sandbox succeeded for a task with no live run: %q", ended.Text())
	}
	if !strings.Contains(ended.Text(), "no sandbox to rebuild") {
		t.Errorf("text = %q, want the registry's own message about there being no sandbox to rebuild", ended.Text())
	}

	// A UI/API server not colocated with a daemon that owns sandboxes
	// answers 404 -- "this deployment does not offer that" -- and a 404
	// is the one status ui.HTTPClient turns into a typed error rather
	// than passing the message through unchanged, so it is worth seeing
	// what an agent is left holding.
	t.Run("a daemon with no sandbox registry says so", func(t *testing.T) {
		unwired := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
			Actor:        ui.DefaultActor("operator"),
			Capabilities: ui.OfferedCapabilities(),
		}), w.store))
		q := startMCPServer(t, bin, "-sandbox-root", t.TempDir(), "-server", unwired, "-task", taskID)

		res := callRecreateSandbox(t, q)
		if !res.IsError {
			t.Fatalf("recreate_sandbox succeeded against a daemon that has no sandboxes: %q", res.Text())
		}
		if !strings.Contains(res.Text(), "not available") {
			t.Errorf("text = %q, want the daemon's own reason rather than a bare 404", res.Text())
		}
	})
}
