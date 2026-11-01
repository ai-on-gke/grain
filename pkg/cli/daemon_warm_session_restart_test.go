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

// docs/test-plans/2026-11-01-late.md §2, against a real daemon and a real
// docker run host (docs/test-runs/2026-11-01-warm-sandbox-adoption.md):
// an interactive session's warm sandbox across daemon restarts.
//
// Four daemon.Runs over one data directory, each stopped mid-turn (a
// cancelled context is what the daemon's SIGTERM handler does), with a
// stub claude CLI in the run host that holds each turn open until the
// test releases it:
//
//  1. Turn 1 is adopted across a restart, parks warm, and turn 2 reuses
//     the same sandbox without a re-clone.
//  2. Turn 2's task_run_sandbox_key row is deleted while the daemon is
//     down (a run an older daemon dispatched): the adopter fails closed,
//     releasing the sandbox, and turn 3 gets a fresh one.
//  3. Turn 3's task loses a read grant while the daemon is down: the
//     adopter releases the sandbox and revokes its git-proxy token, and
//     turn 4 gets a fresh one.
//  4. Turn 4's first-attempt-named sandbox parks warm without a restart,
//     is counted in the sandbox budget, and is released on idle TTL.
//
// It needs docker, so it runs only with GRAIN_RUNHOST_DOCKER=1.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	"github.com/ai-on-gke/grain/pkg/ui"
)

// warmSessionStubClaude writes a stub CLI into dir. Exec N (counted by
// its line in dir/invocations) copies its --mcp-config to dir/mcp-N.json,
// writes one line, waits for dir/release-N, and ends the turn.
func warmSessionStubClaude(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	script := `#!/bin/sh
echo "$*" >> ` + filepath.Join(dir, "invocations") + `
n=$(wc -l < ` + filepath.Join(dir, "invocations") + ` | tr -d ' ')
prev=
for a in "$@"; do
	if [ "$prev" = --mcp-config ]; then cp "$a" ` + dir + `/mcp-$n.json; fi
	prev=$a
done
echo '{"type":"system","subtype":"init","session_id":"stub-session"}'
echo '{"type":"assistant","session_id":"stub-session","message":{"content":[{"type":"text","text":"turn '"$n"' started"}]}}'
while [ ! -e ` + dir + `/release-$n ]; do sleep 0.2; done
echo '{"type":"result","subtype":"success","is_error":false,"result":"turn '"$n"' answered","session_id":"stub-session"}'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("sk-ant-oat01-stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestDaemonRestartKeepsAWarmSessionSandboxOnlyUnderItsRecordedKeys(t *testing.T) {
	if os.Getenv("GRAIN_RUNHOST_DOCKER") != "1" {
		t.Skip("set GRAIN_RUNHOST_DOCKER=1 to run against a real docker daemon")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	image := runHostTestImage(t)

	const owner, repoName = "acme", "widgets"
	const taskID = "warm-session"

	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"))
	docs := initBareRepo(t, filepath.Join(upstream, owner, "docs.git"))
	for _, b := range []string{bare, docs} {
		seedDir := filepath.Join(t.TempDir(), "seed")
		runLive(t, t.TempDir(), "git", "clone", b, seedDir)
		runLive(t, seedDir, "git", "config", "user.email", "seed@example.com")
		runLive(t, seedDir, "git", "config", "user.name", "seed")
		if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runLive(t, seedDir, "git", "add", "README.md")
		runLive(t, seedDir, "git", "commit", "-q", "-m", "initial commit")
		runLive(t, seedDir, "git", "push", "-q", "origin", "main")
	}
	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	credDir := t.TempDir()
	claudePath := warmSessionStubClaude(t, credDir)
	sandboxDir := t.TempDir()

	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("seeding the store: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	human := model.Principal{Kind: model.PrincipalHuman, ID: "tester"}
	task := model.Task{
		ID: taskID, Intent: model.IntentImplement, Interactive: true,
		Title: "warm session across restarts", Body: "hello session",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: human},
			Reason:      model.ReasonDirect,
		},
		Binding: model.BindingDirective,
		Target:  &model.RepoRef{Owner: owner, Name: repoName},
		Reads:   []model.RepoRef{{Owner: owner, Name: "docs"}},
	}
	task.Approval = &model.Attribution{Actor: human}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("seeding the task: %v", err)
	}
	deployment, err := store.InstanceID(ctx)
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

	uiAddr := freeLocalAddr(t)
	cfg := daemon.Config{
		DataDir: dataDir, SandboxDir: sandboxDir, MaxWorkers: 1, PollInterval: time.Second,
		ClaudePath:   claudePath,
		RunHostImage: image, RunHostCredentialDir: credDir,
		GithubHost: githubHost, GithubInsecureHTTP: true,
		UiAddr: uiAddr,
	}
	seedTestDaemonConnections(t, cfg, `git:
  - credential: anonymous
models:
  - id: claude
    framework: claude
    models: [claude-sonnet-5]
    credential_file: `+filepath.Join(credDir, "token")+`
`)
	const idleTTL = 40 * time.Second
	stored, err := store.GetConfig(ctx)
	if err != nil || stored == nil {
		t.Fatalf("reading the seeded config: %v", err)
	}
	stored.SessionSandboxIdleTTL = idleTTL
	if err := store.PutConfig(ctx, *stored); err != nil {
		t.Fatal(err)
	}

	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("tester"), Capabilities: ui.OfferedCapabilities()}, store)
	reply := func(text string) {
		t.Helper()
		if err := client.AddComment(ctx, taskID, text, nil); err != nil {
			t.Fatalf("replying to the session: %v", err)
		}
	}
	release := func(turn int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(credDir, fmt.Sprintf("release-%d", turn)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// liveRun waits for the turn's run to be in its run host, the stub
	// having started (its mcp config copied).
	liveRun := func(turn int) model.Run {
		t.Helper()
		var live model.Run
		if !pollUntil(ctx, 3*time.Minute, func() bool {
			if _, err := os.Stat(filepath.Join(credDir, fmt.Sprintf("mcp-%d.json", turn))); err != nil {
				return false
			}
			runs, _ := store.LiveRuns(ctx)
			for _, r := range runs {
				if r.TaskID == taskID && r.Sandbox != "" {
					live = r
					return true
				}
			}
			return false
		}) {
			st, _ := store.State(ctx, taskID)
			runs, _ := store.Runs(ctx, taskID)
			comments, _ := store.Comments(ctx, taskID)
			t.Fatalf("turn %d never had a live run in a run host: task state %q, runs %+v, comments %+v", turn, st, runs, comments)
		}
		return live
	}
	finished := func(runID string, stopped <-chan struct{}) model.Run {
		t.Helper()
		run, err := awaitFinishedRun(ctx, store, taskID, stopped)
		if err != nil {
			t.Fatal(err)
		}
		if run.ID != runID || run.Outcome != "succeeded" {
			t.Fatalf("finished run = %s %q (%s), want %s succeeded", run.ID, run.Outcome, run.Detail, runID)
		}
		return run
	}
	keyRow := func(runID string) (sandboxKey, grantsKey string, ok bool) {
		t.Helper()
		err := db.QueryRowContext(ctx, `SELECT sandbox_key, grants_key FROM task_run_sandbox_key WHERE run_id = ?`, runID).Scan(&sandboxKey, &grantsKey)
		if err == sql.ErrNoRows {
			return "", "", false
		}
		if err != nil {
			t.Fatalf("reading task_run_sandbox_key for %s: %v", runID, err)
		}
		return sandboxKey, grantsKey, true
	}
	sandboxExists := func(name string) bool {
		_, err := os.Stat(filepath.Join(sandboxDir, name))
		return err == nil
	}
	// proxyCredential is the git-proxy credential line runOne wrote into
	// the sandbox's .git-credentials: scheme://user:token@proxy-host.
	proxyCredential := func(name string) string {
		t.Helper()
		var line string
		_ = filepath.WalkDir(filepath.Join(sandboxDir, name), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != ".git-credentials" || line != "" {
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr == nil {
				line = strings.NewReplacer("%3a", ":", "%3A", ":").Replace(strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0]))
			}
			return nil
		})
		if line == "" {
			t.Fatalf("sandbox %s has no .git-credentials", name)
		}
		return line
	}
	// lsRemote asks the git proxy at proxyFrom's host for acme/widgets
	// with the token from tokenFrom, as the sandbox's git would.
	lsRemote := func(tokenFrom, proxyFrom string) error {
		t.Helper()
		tok, err := url.Parse(tokenFrom)
		if err != nil {
			t.Fatal(err)
		}
		proxy, err := url.Parse(proxyFrom)
		if err != nil {
			t.Fatal(err)
		}
		proxy.User = tok.User
		proxy.Path = "/" + owner + "/" + repoName + ".git"
		cmd := exec.CommandContext(ctx, "git", "ls-remote", proxy.String())
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.ReplaceAll(string(out), tok.User.String(), "<token>"))
		}
		return nil
	}
	start := func() (context.CancelFunc, <-chan error, <-chan struct{}) {
		dctx, stop := context.WithCancel(ctx)
		done, stopped := startLiveDaemon(dctx, cfg)
		return stop, done, stopped
	}

	// 1. Turn 1, restarted mid-turn, parks warm; turn 2 reuses it.
	stop, done, _ := start()
	run1 := liveRun(1)
	if run1.Sandbox != run1.ID {
		t.Fatalf("turn 1's sandbox = %q, want its run ID %q", run1.Sandbox, run1.ID)
	}
	if sk, gk, ok := keyRow(run1.ID); !ok {
		t.Fatalf("turn 1 (%s) recorded no task_run_sandbox_key row", run1.ID)
	} else {
		t.Logf("task_run_sandbox_key %s: sandbox_key=%q grants_key=%q", run1.ID, sk, gk)
	}
	stopLiveDaemon(t, stop, done)
	stop, done, stopped := start()
	release(1)
	finished(run1.ID, stopped)
	if _, _, ok := keyRow(run1.ID); ok {
		t.Errorf("finished run %s left its task_run_sandbox_key row behind", run1.ID)
	}
	if !sandboxExists(run1.Sandbox) {
		t.Fatalf("adopted turn 1's sandbox %s was released, want it parked warm", run1.Sandbox)
	}
	marker := filepath.Join(sandboxDir, run1.Sandbox, "warm-marker")
	if err := os.WriteFile(marker, []byte("left by turn 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reply("turn 2, please")
	run2 := liveRun(2)
	if run2.ID == run1.ID || run2.Sandbox != run1.Sandbox {
		t.Fatalf("turn 2 = run %s in sandbox %q, want a new run in turn 1's warm sandbox %q", run2.ID, run2.Sandbox, run1.Sandbox)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("turn 2's sandbox lost turn 1's marker (re-cloned?): %v", err)
	}

	// 2. Fail closed: turn 2's recorded keys vanish while the daemon is down.
	stopLiveDaemon(t, stop, done)
	if _, err := db.ExecContext(ctx, `DELETE FROM task_run_sandbox_key WHERE run_id = ?`, run2.ID); err != nil {
		t.Fatal(err)
	}
	stop, done, stopped = start()
	release(2)
	finished(run2.ID, stopped)
	if !pollUntil(ctx, 30*time.Second, func() bool { return !sandboxExists(run2.Sandbox) }) {
		t.Fatalf("turn 2's sandbox %s outlived an adoption with no recorded keys", run2.Sandbox)
	}
	reply("turn 3, please")
	run3 := liveRun(3)
	if run3.Sandbox != run3.ID {
		t.Fatalf("turn 3's sandbox = %q, want a fresh one named %q", run3.Sandbox, run3.ID)
	}

	// 3. Narrowed in the gap: the read grant is withdrawn while the daemon is down.
	cred3 := proxyCredential(run3.Sandbox)
	if err := lsRemote(cred3, cred3); err != nil {
		t.Fatalf("the git proxy refuses turn 3's own token while it runs: %v", err)
	}
	stopLiveDaemon(t, stop, done)
	if err := store.UpdateTask(ctx, taskID, func(tk *model.Task) error { tk.Reads = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	stop, done, stopped = start()
	defer func() { stopLiveDaemon(t, stop, done) }()
	release(3)
	finished(run3.ID, stopped)
	if !pollUntil(ctx, 30*time.Second, func() bool { return !sandboxExists(run3.Sandbox) }) {
		t.Fatalf("turn 3's sandbox %s was kept after its task was narrowed", run3.Sandbox)
	}
	reply("turn 4, please")
	run4 := liveRun(4)
	if run4.Sandbox != run4.ID {
		t.Fatalf("turn 4's sandbox = %q, want a fresh one named %q", run4.Sandbox, run4.ID)
	}
	// The proxy has moved with the restart (-sandbox-port-base=0), so
	// turn 3's token is tried where turn 4's sandbox reaches it.
	cred4 := proxyCredential(run4.Sandbox)
	if err := lsRemote(cred4, cred4); err != nil {
		t.Fatalf("the git proxy refuses turn 4's own token: %v", err)
	}
	if err := lsRemote(cred3, cred4); err == nil {
		t.Errorf("the git proxy still accepts turn 3's token after its sandbox was released")
	} else {
		t.Logf("turn 3's token after release: %v", err)
	}

	// 4. The ledger: a first-attempt warm sandbox is counted idle, then
	// released on TTL.
	release(4)
	parkedAt := time.Now()
	finished(run4.ID, stopped)
	if !sandboxExists(run4.Sandbox) {
		t.Fatalf("turn 4's sandbox %s was not parked warm", run4.Sandbox)
	}
	// GET /api/sandboxes: the warm sandbox is listed as an idle session
	// and, where this host's size could be read (budget.known), counted
	// in the agent pool of the sandbox budget.
	type sandboxesReport struct {
		Sandboxes []struct {
			Sandbox string `json:"sandbox"`
			Kind    string `json:"kind"`
		} `json:"sandboxes"`
		Budget struct {
			Known bool `json:"known"`
			Pools []struct {
				Pool  string `json:"pool"`
				Count int    `json:"count"`
			} `json:"pools"`
		} `json:"budget"`
	}
	var lastSandboxes string
	sandboxes := func() sandboxesReport {
		var rep sandboxesReport
		code, err := os.ReadFile(filepath.Join(dataDir, "local-api-token"))
		if err != nil {
			return rep
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+uiAddr+"/api/sandboxes", nil)
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(code)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return rep
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		lastSandboxes = string(raw)
		_ = json.Unmarshal(raw, &rep)
		return rep
	}
	listedIdle := func(rep sandboxesReport) bool {
		for _, sb := range rep.Sandboxes {
			if sb.Sandbox == run4.Sandbox && sb.Kind == "idle session" {
				return true
			}
		}
		return false
	}
	agentCount := func(rep sandboxesReport) int {
		for _, p := range rep.Budget.Pools {
			if p.Pool == model.PoolAgent {
				return p.Count
			}
		}
		return 0
	}
	if !pollUntil(ctx, 10*time.Second, func() bool {
		rep := sandboxes()
		return listedIdle(rep) && (!rep.Budget.Known || agentCount(rep) == 1)
	}) {
		t.Errorf("GET /api/sandboxes with turn 4's sandbox warm does not list it as an idle session counted in the agent pool: %s", lastSandboxes)
	} else if !sandboxes().Budget.Known {
		t.Logf("this host's size could not be read, so the sandbox budget is unknown and its agent-pool count was not checked")
	}
	if !pollUntil(ctx, idleTTL+45*time.Second, func() bool { return !sandboxExists(run4.Sandbox) }) {
		t.Fatalf("turn 4's warm sandbox %s outlived its %s idle TTL (parked %s ago)", run4.Sandbox, idleTTL, time.Since(parkedAt))
	}
	if !pollUntil(ctx, 10*time.Second, func() bool {
		rep := sandboxes()
		return !listedIdle(rep) && agentCount(rep) == 0
	}) {
		t.Errorf("GET /api/sandboxes after the TTL release still holds turn 4's sandbox: %s", lastSandboxes)
	}
	if infos, _ := driver.List(ctx, deployment); len(infos) != 0 {
		t.Errorf("run hosts left behind: %+v", infos)
	}
}
