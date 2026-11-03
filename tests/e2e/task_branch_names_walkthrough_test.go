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

// Task branch names on the wire: docs/test-plans/2026-11-01.md §4,
// "not proved" 2-4. pkg/model pins what FormatTaskBranchName returns and
// pkg/orchestrator what title a pull request is given; this is the same
// names going through a real daemon, a real `git push` through grain's
// git proxy into a bare repository, and a pull request opened on
// githubsim -- for a 300-character title, an all-punctuation one and a
// non-ASCII one, on a deployment with the longest id it may have, and
// then under a custom template so long that {slug} has to be trimmed.
//
// The agent is a custom backend script that commits a change whose
// summary line is 300 characters and pushes it, so every pull request is
// named after that line and has to be capped (capTitle) to open at all:
// GitHub refuses a title past 256 characters.

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
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestTaskBranchNamesPushAndOpenPullRequests(t *testing.T) {
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
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "-q", "origin", "main")

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
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{DirectConfigWrites: true}); err != nil {
		t.Fatal(err)
	}

	// The fake agent: one commit on the task's branch with a 300-character
	// summary line, pushed through the proxy.
	longSummary := strings.Repeat("x", 300)
	agentScript := filepath.Join(home, "fake-agent")
	mustWriteFile(t, agentScript, fmt.Sprintf(`#!/bin/sh
set -e
cat > %[1]s
dir="$(sed -n 's/.*"working_directory":"\([^"]*\)".*/\1/p' %[1]s)"
HOME="$dir"
export HOME
cd "$dir/work"
echo "agent was here $$" >> NOTES.md
git add NOTES.md
git -c user.name=agent -c user.email=agent@example.com commit -q -m "%[2]s" >&2
git push -q origin HEAD >&2
printf '%%s\n' '{"protocol_version":2,"result":{"final_text":"Pushed.","tool_calls":[{"name":"run_command","arguments":{"command":"git push origin HEAD"},"text":"exit=0"}],"transcript":"pushed"}}'
`, filepath.Join(home, "fake-agent-request"), longSummary))
	if err := os.Chmod(agentScript, 0o755); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
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

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
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

	// The longest deployment id a system admin may set, so {deployment}
	// takes as much of the name as it ever can.
	deployment := strings.Repeat("d", model.MaxDeploymentIDLen)
	backends := []model.AgentBackend{{
		ID: "fake", Name: "fake agent", Framework: model.AgentFrameworkCustom, Command: agentScript,
		Models: []string{"fake-model"},
	}}
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{AgentBackends: &backends, DeploymentID: &deployment}); err != nil {
		t.Fatalf("configuring the fake agent and deployment id: %v", err)
	}

	// file files a task and approves it; land waits for its pull request
	// and checks the branch it was pushed on.
	file := func(title string) ui.Task {
		t.Helper()
		task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: title, Description: "Touch NOTES.md.", Repo: owner + "/" + repoName,
			AgentFramework: model.AgentFrameworkCustom,
		})
		if err != nil {
			t.Fatalf("filing %.40q: %v", title, err)
		}
		if err := client.Approve(ctx, task.ID); err != nil {
			t.Fatalf("approving task %s: %v", task.ID, err)
		}
		return task
	}
	land := func(task ui.Task) githubsim.PullRequest {
		t.Helper()
		if len(task.Branch) > model.MaxBranchNameLen || !model.ValidBranchName(task.Branch) {
			t.Fatalf("task %s was given branch %q (%d bytes), want a valid name of at most %d", task.ID, task.Branch, len(task.Branch), model.MaxBranchNameLen)
		}
		if !strings.Contains(task.Branch, "/"+deployment+"/") {
			t.Fatalf("task %s branch %q does not carry the deployment id %q", task.ID, task.Branch, deployment)
		}
		var pr githubsim.PullRequest
		awaitWorkstation(t, ctx, "task "+task.ID+"'s pull request", exited, func() error {
			detail, err := client.GetTask(ctx, task.ID)
			if err != nil {
				return err
			}
			if n := len(detail.Attempts); n > 0 && detail.Attempts[n-1].FinishedAt != nil && detail.Attempts[n-1].Outcome != "succeeded" {
				return fmt.Errorf("attempt %d ended %q: %s", n, detail.Attempts[n-1].Outcome, detail.Attempts[n-1].Detail)
			}
			sim.mu.Lock()
			defer sim.mu.Unlock()
			for _, p := range sim.sim.PullRequests {
				if p.Head == task.Branch {
					pr = p
					return nil
				}
			}
			return fmt.Errorf("no pull request from %q yet; task is %s (%q)", task.Branch, detail.State, detail.Activity)
		})
		// git push took the name: the bare repository has the ref.
		refs := runOutput(t, upstream, "git", "ls-remote", bare, "refs/heads/"+task.Branch)
		if !strings.Contains(refs, "refs/heads/"+task.Branch) {
			t.Fatalf("git ls-remote has no refs/heads/%s:\n%s", task.Branch, refs)
		}
		if pr.Base != "main" {
			t.Fatalf("task %s pull request base = %q, want main", task.ID, pr.Base)
		}
		// GitHub refuses a title past 256 characters; grain caps it at
		// 200 bytes plus "...".
		if len(pr.Title) > 203 || !strings.HasSuffix(pr.Title, "...") {
			t.Fatalf("task %s pull request title is %d bytes: %q", task.ID, len(pr.Title), pr.Title)
		}
		t.Logf("task %s: branch %q (%d bytes), pull request #%d %q", task.ID, task.Branch, len(task.Branch), pr.Number, pr.Title)
		return pr
	}

	long := strings.TrimSpace(strings.Repeat("Make the very long task title survive branch naming ", 6))[:300]
	cases := []struct {
		title string
		slug  string
	}{
		{title: long, slug: "-make-the-very-long-task-title"},
		// No ASCII letter or digit: {slug} expands to nothing and the
		// hyphen before it goes with it.
		{title: "!!! ??? ... --- ***", slug: ""},
		{title: "修复 ünïcödé — 日本語のタイトル", slug: "-n-c-d"},
	}
	for _, c := range cases {
		task := file(c.title)
		land(task)
		if want := "grain/" + deployment + "/task-" + task.ID + c.slug; task.Branch != want {
			t.Errorf("title %.40q: branch %q, want %q", c.title, task.Branch, want)
		}
	}

	// A custom template as long as UpdateSettings accepts: the prefix
	// leaves room for a 19-digit task id and no slug under the longest
	// deployment id, so a real task's slug is trimmed to whatever is
	// left, and two tasks with the same title still get their own
	// branches because {id} tells them apart.
	prefix := "team/" + strings.Repeat("p", 181)
	nearLimit := prefix + "/{deployment}/task-{id}-{slug}"
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{TaskBranchTemplate: &nearLimit}); err != nil {
		t.Fatalf("saving the near-limit template %q: %v", nearLimit, err)
	}
	tooLong := "team/" + strings.Repeat("p", 182) + "/{deployment}/task-{id}-{slug}"
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{TaskBranchTemplate: &tooLong}); err == nil {
		t.Fatalf("a template one byte past the limit (%d bytes of prefix) was accepted", len(tooLong))
	}
	a, b := file(long), file(long)
	if a.Branch == b.Branch {
		t.Fatalf("two tasks with the same title share branch %q", a.Branch)
	}
	for _, task := range []ui.Task{a, b} {
		base := prefix + "/" + deployment + "/task-" + task.ID
		slug, ok := strings.CutPrefix(task.Branch, base)
		if !ok {
			t.Fatalf("branch %q does not start with %q", task.Branch, base)
		}
		// Trimmed at a word boundary to the room left under the cap.
		if slug != "-make-the-very" {
			t.Fatalf("branch %q: slug %q, want the title's slug trimmed to -make-the-very", task.Branch, slug)
		}
		land(task)
	}
}
