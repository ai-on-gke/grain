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

// ci_wait_two_pushes_walkthrough_test.go is docs/test-plans/2026-11-02.md
// §7 gap 4 (docs/test-runs/2026-11-02-sandbox-cli-strict-flags.md): a run
// pushes, starts `grain ci wait`, and pushes again before CI answers --
// the real in-sandbox grain against a real daemon's embedded CI.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// twoPushAgent pushes, starts `grain ci wait` in the background, pushes a
// second commit while it waits, then starts a second `grain ci wait`.
func twoPushAgent(t *testing.T, grain, sandboxDir string) stubAgent {
	t.Helper()
	agent := newStubAgent(t, 600)
	d := agent.dir
	bg := func(name, args string) string {
		out := filepath.Join(d, name)
		return fmt.Sprintf("setsid sh -c '%s %s > %s 2>&1; echo \"exit=$?\" >> %s; touch %s.done' < /dev/null > /dev/null 2>&1 &\n", grain, args, out, out, out)
	}
	script := `#!/bin/bash
creds=$(ls -t $(find ` + sandboxDir + ` -maxdepth 4 -name .git-credentials) 2>/dev/null | head -1)
[ -n "$creds" ] || { echo "no sandbox credential under ` + sandboxDir + `" >&2; exit 1; }
export HOME=$(dirname "$creds")
unset GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN GRAIN_SERVER GRAIN_LOCAL_API_TOKEN_FILE
cd "$HOME/work" || { echo "no checkout at $HOME/work" >&2; exit 1; }
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"run_command","input":{"command":"grain ci wait"}}]}}'
git -c user.email=a@b -c user.name=a commit -q --allow-empty -m one
git push -q origin HEAD
git rev-parse HEAD > ` + filepath.Join(d, "commit.1") + `
` + bg("wait.1", "ci wait") + `sleep 3
git -c user.email=a@b -c user.name=a commit -q --allow-empty -m two
git push -q origin HEAD
git rev-parse HEAD > ` + filepath.Join(d, "commit.2") + `
sleep 1
` + bg("wait.2", "ci wait") + `echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}}'
sleep 600
echo '{"type":"result","result":"done"}'
`
	mustWriteFile(t, filepath.Join(d, "claude"), script)
	return agent
}

func TestCIWaitAcrossTwoQuickPushes(t *testing.T) {
	if testing.Short() {
		t.Skip("walks two CI runs of a 10s job")
	}
	grain := buildGrainCLI(t)
	dataDir, sandboxDir := t.TempDir(), t.TempDir()
	agent := twoPushAgent(t, grain, sandboxDir)

	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, "acme", "widgets.git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seed := filepath.Join(t.TempDir(), "seed")
	run(t, upstream, "git", "clone", "-q", bare, seed)
	mustWriteFile(t, filepath.Join(seed, ".grain", "workflows", "ci.yml"), `name: ci
on: [push]
jobs:
  a:
    capabilities: []
    steps:
      - run: sleep 10
`)
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "-c", "user.email=s@e", "-c", "user.name=s", "commit", "-q", "-m", "workflow")
	run(t, seed, "git", "push", "-q", "origin", "HEAD:main")
	githost := httptest.NewServer(gitHTTPBackend(upstream))
	t.Cleanup(githost.Close)

	cfg := daemon.Config{
		DataDir: dataDir, SandboxDir: sandboxDir,
		UiAddr:     "127.0.0.1:" + strconv.Itoa(freeLoopbackPort(t)),
		ClaudePath: filepath.Join(agent.dir, "claude"),
		MaxWorkers: 2, PollInterval: time.Second,
		GithubHost: strings.TrimPrefix(githost.URL, "http://"), GithubInsecureHTTP: true,
		Ci: true, HostSandboxes: true,
	}
	seedConnections(t, cfg, nil,
		[]model.AgentBackend{{ID: "claude", Framework: model.AgentFrameworkClaude, Models: []string{"claude-sonnet-5"}}},
		map[string]string{"claude": "sk-ant-oat01-stub"})
	setup := startDaemon(t, cfg)
	accessDo(t, http.MethodPost, setup.base+"/api/repos", `{"repo":"acme/widgets"}`)
	accessDo(t, http.MethodPut, setup.base+"/api/repos/acme/widgets/ci-mode", `{"ciMode":"grain"}`)
	accessDo(t, http.MethodPut, setup.base+"/api/github-tokens/default", `{"value":"ghp_stub"}`)
	setup.stop(t)

	d := startDaemon(t, cfg)
	c := ui.NewHTTPClient(d.base)
	if _, err := c.CreateTask(context.Background(), ui.CreateTaskRequest{
		Title: "wait across two pushes", Description: "the stub agent pushes twice around a CI wait",
		Approved: true, Repo: "acme/widgets",
	}); err != nil {
		t.Fatalf("filing the task: %v", err)
	}
	waitUntil(t, "both waits to answer", 3*time.Minute, func() bool {
		return agent.done("wait.1") && agent.done("wait.2")
	})
	commit1, commit2 := strings.TrimSpace(agent.output(t, "commit.1")), strings.TrimSpace(agent.output(t, "commit.2"))
	wait1, wait2 := agent.output(t, "wait.1"), agent.output(t, "wait.2")
	t.Logf("commit 1 %s, commit 2 %s", commit1, commit2)
	t.Logf("wait 1 (started between the pushes):\n%s", wait1)
	t.Logf("wait 2 (started after the second push):\n%s", wait2)
	runs, _, err := c.ListCIRuns(context.Background(), "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		t.Logf("run %s ref %s commit %.12s conclusion %q cancelled by %q (%q)", r.ID, r.Ref, r.Commit, r.Conclusion, r.CancelledBy, r.CancelReason)
	}
	if out := os.Getenv("GRAIN_WALK_OUT"); out != "" {
		_ = os.WriteFile(out, []byte(fmt.Sprintf("commit.1 %s\ncommit.2 %s\n--- wait.1\n%s--- wait.2\n%s", commit1, commit2, wait1, wait2)), 0o644)
	}
	// The wait started after the second push answers for that commit.
	if !strings.Contains(wait2, "passed on "+commit2[:12]) {
		t.Errorf("the wait started after the second push answered %q, want commit 2's pass", wait2)
	}
	// The one started between them is on commit 1's run, which the second
	// wait's run superseded. It is not followed onto another commit, but
	// it says why it was cancelled and which commit's verdict to wait for,
	// rather than a bare "failed ... cancelled".
	if !strings.Contains(wait1, "on "+commit1[:12]+" (run ") || !strings.Contains(wait1, "Grain cancelled this run itself: superseded by run ") ||
		!strings.Contains(wait1, "That run is on "+commit2[:12]+", a newer commit on this branch") {
		t.Errorf("the wait started between the pushes answered %q, want commit 1's run superseded by commit 2's", wait1)
	}
}
