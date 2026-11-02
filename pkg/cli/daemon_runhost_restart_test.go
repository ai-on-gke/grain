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

// The acceptance test of design/design/non-disruptive-upgrade.design.md, against a real
// daemon and a real docker run host:
//
//	With an agent run in flight, restart the daemon. The same run
//	finishes and opens its pull request; its transcript has no hole; its
//	deadline is not reset.
//
// Two daemon.Runs over one data directory, the second started after the
// first has stopped, which is what a restart is to the data directory.
// The agent is a stub claude CLI that writes one line of its stream,
// waits for the test to say the restart is over, and writes the rest; it
// runs in a sibling container built from debian:bookworm-slim and this
// checkout's own grain (`grain runhost exec`), the run host the real
// daemon creates for it under -run-host-image. The pull request is
// opened against a githubsim, as TestRunLiveDispatchesAndOpensAPullRequest's
// is, for a branch the test pushes to the bare repo standing in for
// GitHub while the run is held open -- the stub has no sandbox tools to
// push with, and the push is not what is under test.
//
// It needs docker, so it runs only with GRAIN_RUNHOST_DOCKER=1 (the "run
// host" job in .grain/workflows/tests.yml).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// runHostTestImage builds the run-host image: debian:bookworm-slim with
// this checkout's grain at the path NewRunHost runs it from.
func runHostTestImage(t *testing.T) string {
	t.Helper()
	base := os.Getenv("GRAIN_RUNHOST_IMAGE")
	if base == "" {
		base = "debian:bookworm-slim"
	}
	ctxDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(ctxDir, "grain"), "github.com/ai-on-gke/grain/cmd/grain")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building grain: %v\n%s", err, out)
	}
	dockerfile := "FROM " + base + "\nCOPY grain /usr/local/bin/grain\n"
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	image := fmt.Sprintf("grain-runhost-acceptance:%d", time.Now().UnixNano())
	if out, err := exec.Command("docker", "build", "-q", "-t", image, ctxDir).CombinedOutput(); err != nil {
		t.Fatalf("building the run-host image: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })
	return image
}

// restartStubClaude writes the stub CLI into dir, which the daemon mounts
// into every run host (-run-host-credential-dir), so the run host finds
// it at the same path. It writes "before the restart", waits for
// dir/release, writes "after the restart", and succeeds.
//
// Before any of that it refuses to start, as the real CLI does, when the
// --mcp-config file it is handed is not readable in its run host or its
// HOME is not writable: the daemon walkthrough
// (docs/test-runs/2026-10-14-run-hosts-daemon.md) found a real claude
// failing on both while this stub, which ignored its argv, passed.
//
// Each exec's arguments are appended to dir/invocations. An exec asked
// to --resume a session (a durable run resumed after a restart it was
// not adopted across, TestDaemonRollResumesADurableRunItDidNotAdopt)
// rejoins it only if the session the first exec left in
// $HOME/.claude/stub-sessions is still there, as the real CLI does, and
// then writes "after the restart" at once and succeeds.
func restartStubClaude(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	script := `#!/bin/sh
echo "$*" >> ` + filepath.Join(dir, "invocations") + `
prev=
resume=
for a in "$@"; do
	if [ "$prev" = --mcp-config ] && [ ! -r "$a" ]; then echo "MCP config file not found: $a" >&2; exit 3; fi
	if [ "$prev" = --resume ]; then resume=$a; fi
	prev=$a
done
if ! touch "$HOME/.stub-claude-home-probe" 2>/dev/null; then echo "HOME $HOME is not writable" >&2; exit 4; fi
if [ -n "$resume" ]; then
	if [ ! -e "$HOME/.claude/stub-sessions/$resume" ]; then echo "No conversation found with session ID: $resume" >&2; exit 1; fi
	echo '{"type":"system","subtype":"init","session_id":"'"$resume"'"}'
	echo '{"type":"assistant","session_id":"'"$resume"'","message":{"content":[{"type":"text","text":"line written after the restart"}]}}'
	echo '{"type":"assistant","session_id":"'"$resume"'","message":{"content":[{"type":"tool_use","id":"call-1","name":"mcp__grain-sandbox__run_command","input":{"command":"git push"}}]}}'
	echo '{"type":"user","session_id":"'"$resume"'","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"pushed"}]}}'
	echo '{"type":"result","subtype":"success","is_error":false,"result":"pushed the branch","session_id":"'"$resume"'"}'
	exit 0
fi
mkdir -p "$HOME/.claude/stub-sessions" && touch "$HOME/.claude/stub-sessions/stub-session"
echo '{"type":"system","subtype":"init","session_id":"stub-session"}'
echo '{"type":"assistant","session_id":"stub-session","message":{"content":[{"type":"text","text":"line written before the restart"}]}}'
while [ ! -e ` + filepath.Join(dir, "release") + ` ]; do sleep 0.2; done
echo '{"type":"assistant","session_id":"stub-session","message":{"content":[{"type":"text","text":"line written after the restart"}]}}'
echo '{"type":"assistant","session_id":"stub-session","message":{"content":[{"type":"tool_use","id":"call-1","name":"mcp__grain-sandbox__run_command","input":{"command":"git push"}}]}}'
echo '{"type":"user","session_id":"stub-session","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"pushed"}]}}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"pushed the branch","session_id":"stub-session"}'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("sk-ant-oat01-stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDaemonRestartAdoptsTheRunAndItOpensItsPullRequest(t *testing.T) {
	if os.Getenv("GRAIN_RUNHOST_DOCKER") != "1" {
		t.Skip("set GRAIN_RUNHOST_DOCKER=1 to run against a real docker daemon")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	image := runHostTestImage(t)

	const owner, repoName = "acme", "widgets"
	taskID := "runhost-restart"
	branch := model.BranchName(taskID)

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
	runLive(t, seedDir, "git", "push", "-q", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	sim.seedPassingCheck(branch)
	githubHost := githubHostServer(t, sim, upstream)

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	credDir := t.TempDir()
	claudePath := restartStubClaude(t, credDir)

	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("seeding the store: %v", err)
	}
	defer db.Close()
	task := model.Task{
		ID: taskID, Intent: model.IntentImplement,
		Title: "run across a daemon restart", Body: "the stub agent waits for the restart",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Binding: model.BindingDirective,
		Target:  &model.RepoRef{Owner: owner, Name: repoName},
	}
	task.Approval = &model.Attribution{Actor: task.Origin.Attribution.Actor}
	if err := store.PutTask(context.Background(), task); err != nil {
		t.Fatalf("seeding the task: %v", err)
	}
	deployment, err := store.InstanceID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	driver := runhost.Docker{}
	t.Cleanup(func() {
		infos, _ := driver.List(context.Background(), deployment)
		for _, i := range infos {
			_ = driver.Destroy(context.Background(), i.Name)
		}
	})

	cfg := daemon.Config{
		DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Second,
		ClaudePath:   claudePath,
		RunHostImage: image, RunHostCredentialDir: credDir,
		GithubHost: githubHost, GithubInsecureHTTP: true,
	}
	// Named explicitly: grain has no default model of its own, and a
	// connection with none to run waits rather than guessing one.
	seedTestDaemonConnections(t, cfg, `git:
  - credential: anonymous
models:
  - id: claude
    framework: claude
    models: [claude-sonnet-5]
    credential_file: `+filepath.Join(credDir, "token")+`
`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// The first daemon dispatches the run into a run host.
	firstCtx, stopFirst := context.WithCancel(ctx)
	firstDone, _ := startLiveDaemon(firstCtx, cfg)
	var live model.Run
	var host runhost.Info
	if !pollUntil(ctx, 3*time.Minute, func() bool {
		runs, _ := store.LiveRuns(ctx)
		infos, _ := driver.List(ctx, deployment)
		for _, r := range runs {
			if r.TaskID == taskID && len(infos) == 1 {
				live, host = r, infos[0]
				return true
			}
		}
		return false
	}) {
		stopLiveDaemon(t, stopFirst, firstDone)
		t.Fatal("the first daemon never had the run in a run host")
	}
	// Long enough for the first daemon's tail to read the stub's first
	// line, so the transcript is split across the two daemons.
	time.Sleep(3 * time.Second)
	startedAt, deadline := live.StartedAt, host.Labels.Deadline
	if deadline.IsZero() {
		t.Fatalf("run host %s carries no deadline label", host.Name)
	}
	// What the adopting daemon carries the run's deadline from: where the
	// first daemon started the run's clock, not the row's start, which
	// would charge the run its setup a second time.
	if live.AgentStartedAt == nil || live.AgentStartedAt.Before(startedAt) {
		t.Fatalf("live run %s reads back agent start %v (started %s), so an adopter would carry its deadline from the wrong moment",
			live.ID, live.AgentStartedAt, startedAt)
	}

	// The agent's push, while the run is in flight.
	runLive(t, seedDir, "git", "checkout", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(seedDir, "NOTES.md"), []byte("across a restart\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seedDir, "git", "add", "NOTES.md")
	runLive(t, seedDir, "git", "commit", "-q", "-m", "work done across a restart")
	runLive(t, seedDir, "git", "push", "-q", "origin", branch)

	// The restart: the first daemon stops, and the run host does not.
	stopLiveDaemon(t, stopFirst, firstDone)
	infos, err := driver.List(ctx, deployment)
	if err != nil || len(infos) != 1 || infos[0].Name != host.Name {
		t.Fatalf("after the first daemon stopped, run hosts = %+v (%v); want %s still running", infos, err, host.Name)
	}
	if runs, _ := store.LiveRuns(ctx); len(runs) != 1 || runs[0].ID != live.ID {
		t.Fatalf("after the first daemon stopped, live runs = %+v; want %s still live", runs, live.ID)
	}
	if n := sim.pullRequestCount(); n != 0 {
		t.Fatalf("%d pull request(s) opened before the run finished", n)
	}

	// The second daemon adopts it; the agent then finishes.
	secondCtx, stopSecond := context.WithCancel(ctx)
	secondDone, stopped := startLiveDaemon(secondCtx, cfg)
	defer stopLiveDaemon(t, stopSecond, secondDone)
	if err := os.WriteFile(filepath.Join(credDir, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	finished, err := awaitFinishedRun(ctx, store, taskID, stopped)
	if err != nil {
		t.Fatal(err)
	}
	if finished.ID != live.ID {
		t.Fatalf("the run that finished is %s, not the one in flight across the restart (%s)", finished.ID, live.ID)
	}
	if finished.Outcome != "succeeded" {
		t.Fatalf("the adopted run ended %q (%s), want succeeded", finished.Outcome, finished.Detail)
	}
	if !finished.StartedAt.Equal(startedAt) {
		t.Fatalf("the adopted run's start moved from %s to %s, which resets its deadline", startedAt, finished.StartedAt)
	}
	if !pollUntil(ctx, liveFinishWindow, func() bool { return sim.pullRequestCount() > 0 }) {
		t.Fatalf("the adopted run ended %q (%s), but the second daemon opened no pull request for %s",
			finished.Outcome, finished.Detail, branch)
	}
	pr := sim.onlyPullRequest(t)
	if pr.Head != branch {
		t.Fatalf("pull request head = %q, want %q", pr.Head, branch)
	}

	transcript, found, err := store.RunTranscript(ctx, taskID, 1)
	if err != nil || !found {
		t.Fatalf("reading the run's transcript: found=%v err=%v", found, err)
	}
	for _, line := range []string{"line written before the restart", "line written after the restart"} {
		if n := strings.Count(transcript, line); n != 1 {
			t.Errorf("transcript has %q %d times, want once (a hole or a duplicate across the restart):\n%s", line, n, transcript)
		}
	}
	if before, after := strings.Index(transcript, "before the restart"), strings.Index(transcript, "after the restart"); before > after {
		t.Errorf("transcript is out of order across the restart:\n%s", transcript)
	}
	if infos, _ := driver.List(ctx, deployment); len(infos) != 0 {
		t.Errorf("run hosts left behind after the adopted run finished: %+v", infos)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("ran out of time")
	}
}
