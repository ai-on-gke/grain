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
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestRealDaemonDispatchesByPriorityThenFIFO is docs/test-plans/2026-11-01.md
// §1 step 4 on a real `grain daemon`: one worker, host sandboxes and a
// custom agent that only records which task it was started for. Four tasks
// are queued under a pipeline pause, at priorities 5, 1, 9 and 5 in that
// filing order; once the pause lifts they must start 9, then the two 5s in
// the order they were filed, then 1.
func TestRealDaemonDispatchesByPriorityThenFIFO(t *testing.T) {
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

	// The agent appends the branch its sandbox checked out, which names
	// the task, and ends its turn without changing anything.
	order := filepath.Join(home, "dispatch-order")
	agentScript := filepath.Join(home, "fake-agent")
	mustWriteFile(t, agentScript, fmt.Sprintf(`#!/bin/sh
req="$(cat)"
dir="$(printf '%%s' "$req" | sed -n 's/.*"working_directory":"\([^"]*\)".*/\1/p')"
git -C "$dir/work" rev-parse --abbrev-ref HEAD >> %[1]s
sleep 1
printf '%%s\n' '{"protocol_version":2,"result":{"final_text":"Nothing to change.","transcript":"recorded the dispatch"}}'
`, order))
	if err := os.Chmod(agentScript, 0o755); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	daemonCmd := exec.Command(filepath.Join(dataDir, "daemon.sh"), "-github-host", githubHost, "-github-insecure-http",
		"-sandbox-port-base=0", "-poll-interval", "1s")
	daemonCmd.Env = env
	daemonCmd.Stdout = &logs
	daemonCmd.Stderr = &logs
	daemonCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := daemonCmd.Start(); err != nil {
		t.Fatalf("starting the daemon: %v", err)
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

	if _, _, err := client.PausePipeline(ctx, "queue the priority walkthrough's tasks together"); err != nil {
		t.Fatalf("pausing dispatch: %v", err)
	}
	one := 1
	backends := []model.AgentBackend{{
		ID: "fake", Name: "fake agent", Framework: model.AgentFrameworkCustom, Command: agentScript,
		Models: []string{"fake-model"},
	}}
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{AgentBackends: &backends, MaxWorkers: &one}); err != nil {
		t.Fatalf("configuring the fake agent and one worker: %v", err)
	}

	type filed struct {
		name     string
		priority int
		id       string
	}
	tasks := []*filed{{name: "first five", priority: 5}, {name: "one", priority: 1}, {name: "nine", priority: 9}, {name: "second five", priority: 5}}
	for _, f := range tasks {
		p := f.priority
		task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "Priority " + f.name, Description: "Record the dispatch order.",
			Repo: owner + "/" + repoName, AgentFramework: model.AgentFrameworkCustom, Priority: &p,
		})
		if err != nil {
			t.Fatalf("filing %q: %v", f.name, err)
		}
		if task.Priority != f.priority {
			t.Fatalf("task %s filed at priority %d, want %d", task.ID, task.Priority, f.priority)
		}
		if err := client.Approve(ctx, task.ID); err != nil {
			t.Fatalf("approving task %s: %v", task.ID, err)
		}
		f.id = task.ID
		// Filing order is creation time; keep the two 5s apart in it.
		time.Sleep(20 * time.Millisecond)
	}
	if _, _, err := client.ResumePipeline(ctx); err != nil {
		t.Fatalf("lifting the pause: %v", err)
	}

	branchOf := map[string]string{}
	var started []string
	awaitWorkstation(t, ctx, "every task to have been dispatched once", exited, func() error {
		for _, f := range tasks {
			if branchOf[f.id] != "" {
				continue
			}
			detail, err := client.GetTask(ctx, f.id)
			if err != nil {
				return err
			}
			if detail.Branch != "" {
				branchOf[f.id] = detail.Branch
			}
		}
		b, _ := os.ReadFile(order)
		started = started[:0]
		for _, branch := range strings.Fields(string(b)) {
			for _, f := range tasks {
				if branchOf[f.id] == branch && !slices.Contains(started, f.name) {
					started = append(started, f.name)
				}
			}
		}
		if len(started) < len(tasks) {
			return fmt.Errorf("started %v so far (agent saw %q)", started, strings.Fields(string(b)))
		}
		return nil
	})
	want := []string{"nine", "first five", "second five", "one"}
	if !slices.Equal(started, want) {
		t.Fatalf("dispatch order = %v, want %v", started, want)
	}
	t.Logf("dispatch order: %v", started)
}
