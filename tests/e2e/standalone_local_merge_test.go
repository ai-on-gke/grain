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

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestStandaloneTaskImplementsTestsAndLandsAChange is Track 1.3's exit
// criterion run by a machine: one standalone Grain node, set up by
// `grain init workstation` and started from the daemon.sh it wrote --
// single-tenant, the local policy as its PEP, host sandboxes -- is given
// a task against an attached repository and carries it all the way to
// the repository's base branch with nobody touching it.
//
// What is real: the daemon process and everything in it (dispatch, the
// policy check, the host sandbox and its checkout, the git proxy the
// agent pushes through, grain CI planning and running the repository's
// own .grain/workflows on a host runner, the merge queue's verdict and
// its merge). What is not: the agent, which is a custom backend script
// that edits a file, commits and pushes from the checkout it is handed
// rather than calling a model; and the forge, which is githubsim over a
// bare repository on this machine's disk, served on loopback -- so no
// network leaves the machine, and "landed" is a fact about that bare
// repository's main, read with git.
//
// The one thing the test still does by hand is the forge's own
// asynchronous mergeability field, which githubsim leaves unknown
// (githubsim.PullRequest.Mergeable): a loop answers it from
// `git merge-tree` against the bare repository, which is what GitHub
// computes it from.
func TestStandaloneTaskImplementsTestsAndLandsAChange(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real daemon")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)
	home := t.TempDir()
	addr := freeLoopbackAddr(t)
	env := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "GRAIN_DATA_DIR=")

	// The attached repository: a bare repo on disk whose main carries a
	// workflow that fails until the change the task asks for is there.
	const owner, repoName = "acme", "widgets"
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	mustWriteFile(t, filepath.Join(seed, "NOTES.md"), "seed\n")
	mustWriteFile(t, filepath.Join(seed, ".grain", "workflows", "tests.yml"), `name: tests
on:
  push:
    branches: ["**"]
  pull_request:
jobs:
  notes:
    name: notes
    steps:
      - name: NOTES.md carries the agent's line
        run: grep -q "standalone agent was here" NOTES.md
`)
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "-q", "origin", "main")
	baseBefore := strings.TrimSpace(runOutput(t, upstream, "git", "--git-dir", bare, "rev-parse", "main"))

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	tokenFile := filepath.Join(home, "github-token")
	mustWriteFile(t, tokenFile, "ghp-not-a-real-token-for-a-sim\n")

	initCmd := exec.Command(bin, "init", "workstation", "-non-interactive",
		"-repo", owner+"/"+repoName, "-sandbox-backend", "host", "-model-backend", "claude",
		"-github-token-file", tokenFile,
		"-owner", "e2e@example.com", "-ui-addr", addr, "-start-service=false")
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("grain init workstation: %v\n%s", err, out)
	}
	dataDir := filepath.Join(home, ".grain")

	// The fake agent: it reads its request, edits NOTES.md in the
	// checkout grain cloned under the sandbox it was given
	// (orchestrator.CheckoutDir), commits on the task's branch and pushes it
	// through the proxy -- the work an agent CLI does in a sandbox -- and
	// reports the run_command call a real agent would have made it with.
	ran := filepath.Join(home, "fake-agent-ran")
	agentScript := filepath.Join(home, "fake-agent")
	mustWriteFile(t, agentScript, fmt.Sprintf(`#!/bin/sh
set -e
cat > %[1]s
dir="$(sed -n 's/.*"working_directory":"\([^"]*\)".*/\1/p' %[1]s)"
# A host sandbox's commands run with HOME pinned to the sandbox, which
# is where grain put the git proxy's credentials.
HOME="$dir"
export HOME
cd "$dir/work"
echo "standalone agent was here" >> NOTES.md
git add NOTES.md
git -c user.name=agent -c user.email=agent@example.com commit -q -m "add the agent's NOTES line" >&2
git push -q origin HEAD >&2
printf '%%s\n' '{"protocol_version":2,"result":{"final_text":"Pushed the NOTES change.","tool_calls":[{"name":"run_command","arguments":{"command":"git push origin HEAD"},"text":"exit=0"}],"transcript":"edited, committed, pushed"}}'
`, ran))
	if err := os.Chmod(agentScript, 0o755); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	// -sandbox-port-base=0: the default block starts at 9728, which the
	// machine running this test may already hold -- a grain sandbox's own
	// git proxy and caches are forwarded to exactly those loopback ports
	// -- and a daemon that cannot bind its git proxy serves its API but
	// dispatches nothing, leaving the task queued until the test gives up.
	daemonCmd := exec.Command(filepath.Join(dataDir, "daemon.sh"), "-github-host", githubHost, "-github-insecure-http", "-sandbox-port-base=0")
	daemonCmd.Env = env
	daemonCmd.Stdout, daemonCmd.Stderr = &logs, &logs
	daemonCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := daemonCmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var exitErr error
	go func() { exitErr = daemonCmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-daemonCmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			_ = syscall.Kill(-daemonCmd.Process.Pid, syscall.SIGKILL)
			<-exited
		}
		if t.Failed() {
			t.Logf("daemon exit: %v\ndaemon output:\n%s", exitErr, logs.String())
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	client := ui.NewHTTPClient("http://" + addr)
	awaitWorkstation(t, ctx, "the daemon to write its local API token", exited, func() error {
		b, err := os.ReadFile(ui.LocalAPITokenPath(dataDir))
		if err != nil {
			return err
		}
		if client.Token = strings.TrimSpace(string(b)); client.Token == "" {
			return errors.New("the token file is empty")
		}
		return nil
	})
	awaitWorkstation(t, ctx, "the daemon to serve its API", exited, func() error {
		_, err := client.GetSettings(ctx)
		return err
	})

	backends := []model.AgentBackend{{
		ID: "fake", Name: "fake agent", Framework: model.AgentFrameworkCustom, Command: agentScript,
		// A custom command with an empty list has no models, and its
		// tasks wait for one to be set.
		Models: []string{"fake-model"},
	}}
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{AgentBackends: &backends}); err != nil {
		t.Fatalf("configuring the fake agent: %v", err)
	}
	if _, err := client.SetRepoCIMode(ctx, owner+"/"+repoName, string(model.CIModeGrain)); err != nil {
		t.Fatalf("putting %s/%s on grain CI: %v", owner, repoName, err)
	}

	// Stand in for the forge's mergeability computation for as long as
	// the test runs.
	mergeCtx, stopMergeability := context.WithCancel(ctx)
	defer stopMergeability()
	go answerMergeability(mergeCtx, sim, bare)

	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Add the agent's NOTES line", Description: "Append a line to NOTES.md so the notes workflow passes.",
		Repo: owner + "/" + repoName, AutoMerge: true, AgentFramework: model.AgentFrameworkCustom,
	})
	if err != nil {
		t.Fatalf("filing the task: %v", err)
	}
	if err := client.Approve(ctx, task.ID); err != nil {
		t.Fatalf("approving the task: %v", err)
	}

	var last ui.TaskDetail
	awaitWorkstation(t, ctx, "the change to land on main", exited, func() error {
		detail, err := client.GetTask(ctx, task.ID)
		if err != nil {
			return err
		}
		last = detail
		if n := len(detail.Attempts); n > 0 && detail.Attempts[n-1].FinishedAt != nil && detail.Attempts[n-1].Outcome != "succeeded" {
			return fmt.Errorf("attempt %d ended %q: %s", n, detail.Attempts[n-1].Outcome, detail.Attempts[n-1].Detail)
		}
		head := strings.TrimSpace(runOutput(t, upstream, "git", "--git-dir", bare, "rev-parse", "main"))
		if head == baseBefore {
			return fmt.Errorf("main has not moved; task is %s (%q)", detail.State, detail.Activity)
		}
		if detail.State != model.StateClosed {
			return fmt.Errorf("main moved but the task is %s (%q)", detail.State, detail.Activity)
		}
		return nil
	})
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("the task landed without running the fake agent: %+v", last.Attempts)
	}

	// The agent's commit is on main, and main's own tree passes the
	// workflow it carries.
	mainLog := runOutput(t, upstream, "git", "--git-dir", bare, "log", "--format=%s", "main")
	if !strings.Contains(mainLog, "add the agent's NOTES line") {
		t.Fatalf("main's log = %q, want the agent's commit on it", mainLog)
	}
	notes := runOutput(t, upstream, "git", "--git-dir", bare, "show", "main:NOTES.md")
	if !strings.Contains(notes, "standalone agent was here") {
		t.Fatalf("main:NOTES.md = %q, want the agent's line", notes)
	}

	// Grain CI judged the change: a successful run of the repository's
	// own workflow is on record against the repository.
	runs, enabled, err := client.ListCIRuns(ctx, owner+"/"+repoName)
	if err != nil {
		t.Fatalf("listing CI runs: %v", err)
	}
	if !enabled {
		t.Fatal("grain CI reads disabled for the repository")
	}
	passed := false
	for _, r := range runs {
		passed = passed || r.Conclusion == "success"
	}
	if !passed {
		t.Fatalf("no successful grain CI run is on record for %s/%s: %+v", owner, repoName, runs)
	}
}

// answerMergeability plays GitHub's asynchronous mergeability check for
// every open pull request on sim until ctx ends: true when `git
// merge-tree` merges its head into its base cleanly, false when it does
// not. A pull request already answered is left alone unless its head or
// base has moved since.
func answerMergeability(ctx context.Context, sim *syncedSim, bare string) {
	answered := map[int]string{}
	for {
		sim.mu.Lock()
		type open struct {
			number     int
			head, base string
		}
		var prs []open
		for _, pr := range sim.sim.PullRequests {
			if pr.State == "open" {
				prs = append(prs, open{pr.Number, pr.Head, pr.Base})
			}
		}
		sim.mu.Unlock()
		for _, pr := range prs {
			heads, err := exec.CommandContext(ctx, "git", "--git-dir", bare, "rev-parse", pr.head, pr.base).Output()
			if err != nil {
				continue
			}
			key := strings.Join(strings.Fields(string(heads)), " ")
			if answered[pr.number] == key {
				continue
			}
			clean := exec.CommandContext(ctx, "git", "--git-dir", bare, "merge-tree", "--write-tree", pr.base, pr.head).Run() == nil
			sim.mu.Lock()
			for i := range sim.sim.PullRequests {
				if sim.sim.PullRequests[i].Number == pr.number {
					sim.sim.PullRequests[i].Mergeable = &clean
				}
			}
			sim.mu.Unlock()
			answered[pr.number] = key
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}
