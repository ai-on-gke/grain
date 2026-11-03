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

// sandbox_cli_edges_walkthrough_test.go is docs/test-plans/2026-11-03.md
// §10 (docs/test-runs/2026-11-03-sandbox-cli-edges.md): the sandbox grain
// run inside a real Docker sandbox container against a real daemon --
// grain grep over src://, grain log -n, every missing argument -- and the
// task priority spellings on the daemon's API and the host CLI.
//
// It needs docker and a daemon source tree where the image carries one
// (/usr/local/share/grain/src, scripts/export-source.sh), so it runs only
// with GRAIN_WALK_SANDBOX_CLI_EDGES=1. GRAIN_WALK_OUT names a directory
// for the transcripts.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const edgesDir = "../../docs/test-runs/2026-11-03-sandbox-cli-edges"

// edgesSandboxImage is debian:bookworm-slim with git, an unprivileged
// user whose uid is this process's (so docker exec writes nothing the
// test cannot clean up) and the sandbox contract label.
func edgesSandboxImage(t *testing.T, grain string) string {
	t.Helper()
	ctxDir := t.TempDir()
	raw, err := os.ReadFile(grain)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "grain"), raw, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(ctxDir, "Dockerfile"), fmt.Sprintf(`FROM debian:bookworm-slim
RUN apt-get update -qq && apt-get install -y -qq git ca-certificates > /dev/null && rm -rf /var/lib/apt/lists/*
RUN useradd -m -d /home/agent -u %d agent
COPY grain /usr/local/bin/grain
LABEL dev.grain.sandbox.contract=2
USER agent
WORKDIR /home/agent
`, os.Getuid()))
	image := fmt.Sprintf("grain-walk-edges:%d", time.Now().UnixNano())
	if out, err := exec.Command("docker", "build", "-q", "-t", image, ctxDir).CombinedOutput(); err != nil {
		t.Fatalf("building the sandbox image: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })
	return image
}

// edgesAgent is a stub claude that finds its run's sandbox container and
// runs sweep.sh in it with docker exec, as the agent's own run_command
// would, then holds the run open until the test is done with it.
func edgesAgent(t *testing.T, sweep, symbols string) stubAgent {
	t.Helper()
	agent := newStubAgent(t, 900)
	d := agent.dir
	script := `#!/bin/bash
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"run_command","input":{"command":"sweep.sh"}}]}}'
c=""
for i in $(seq 60); do
  c=$(docker ps -q --filter label=grain.sandbox=true | head -1)
  [ -n "$c" ] && docker exec "$c" test -s /home/agent/.git-credentials 2>/dev/null && break
  sleep 1
done
echo "$c" > ` + filepath.Join(d, "container") + `
docker cp ` + symbols + ` "$c":/tmp/symbols.txt
docker exec -i "$c" bash -s /tmp/symbols.txt < ` + sweep + ` > ` + filepath.Join(d, "sweep.txt") + ` 2>&1
echo "exit=$?" >> ` + filepath.Join(d, "sweep.txt") + `
touch ` + filepath.Join(d, "sweep.txt.done") + `
echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}}'
while [ ! -e ` + filepath.Join(d, "release") + ` ]; do sleep 1; done
echo '{"type":"result","result":"done"}'
`
	mustWriteFile(t, filepath.Join(d, "claude"), script)
	return agent
}

func TestSandboxCLIEdgesWalkthrough(t *testing.T) {
	if os.Getenv("GRAIN_WALK_SANDBOX_CLI_EDGES") != "1" {
		t.Skip("set GRAIN_WALK_SANDBOX_CLI_EDGES=1 to walk docs/test-plans/2026-11-03.md §10 against docker")
	}
	out := os.Getenv("GRAIN_WALK_OUT")
	if out == "" {
		out = t.TempDir()
	}
	grain := buildGrainCLI(t)
	image := edgesSandboxImage(t, grain)
	sweep, _ := filepath.Abs(filepath.Join(edgesDir, "sweep.sh"))
	symbols, _ := filepath.Abs(filepath.Join(edgesDir, "symbols.txt"))
	agent := edgesAgent(t, sweep, symbols)
	t.Cleanup(func() { mustWriteFile(t, filepath.Join(agent.dir, "release"), "") })

	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, "acme", "widgets.git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seed := filepath.Join(t.TempDir(), "seed")
	run(t, upstream, "git", "clone", "-q", bare, seed)
	mustWriteFile(t, filepath.Join(seed, "README.md"), "widgets\n")
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "-c", "user.email=s@e", "-c", "user.name=s", "commit", "-q", "-m", "seed")
	run(t, seed, "git", "push", "-q", "origin", "HEAD:main")
	githost := httptest.NewServer(gitHTTPBackend(upstream))
	t.Cleanup(githost.Close)

	cfg := daemon.Config{
		DataDir: t.TempDir(), SandboxDir: t.TempDir(),
		UiAddr:     "127.0.0.1:" + strconv.Itoa(freeLoopbackPort(t)),
		ClaudePath: filepath.Join(agent.dir, "claude"),
		MaxWorkers: 2, PollInterval: time.Second,
		GithubHost: strings.TrimPrefix(githost.URL, "http://"), GithubInsecureHTTP: true,
		// The host network, so the container reaches the git proxy on
		// this host's loopback as a host sandbox would.
		ContainerSandboxes: true, ContainerImage: image,
		ContainerGitProxyHost: "127.0.0.1", ContainerNetwork: "host",
	}
	seedConnections(t, cfg, nil,
		[]model.AgentBackend{{ID: "claude", Framework: model.AgentFrameworkClaude, Models: []string{"claude-sonnet-5"}}},
		map[string]string{"claude": "sk-ant-oat01-stub"})
	setup := startDaemon(t, cfg)
	accessDo(t, http.MethodPost, setup.base+"/api/repos", `{"repo":"acme/widgets"}`)
	accessDo(t, http.MethodPut, setup.base+"/api/github-tokens/default", `{"value":"ghp_stub"}`)
	setup.stop(t)

	d := startDaemon(t, cfg)
	c := ui.NewHTTPClient(d.base)
	ctx := context.Background()
	caps := []string{"self-debug", "https://grain.dev/capabilities/grain#journal"}
	walker, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "sandbox CLI edges", Description: "the stub agent runs sweep.sh in its sandbox",
		Approved: true, Repo: "acme/widgets", Capabilities: &caps,
	})
	if err != nil {
		t.Fatalf("filing the walking task: %v", err)
	}
	t.Logf("walking task %s, capabilities %v", walker.ID, walker.Capabilities)

	// §10 item 3 while the sweep runs: every spelling on POST, on PATCH
	// and through the host CLI, and what the board's own list answers.
	var table strings.Builder
	fmt.Fprintf(&table, "spelling\tPOST /api/tasks\tstored\tPATCH /api/tasks/<id>\tstored\tgrain create -priority\tstored\tgrain update -priority\tstored\n")
	base, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "priority edit target", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{`1e3`, `1000.0`, `1.5e2`, `-1`, `1e10`, `"1000"`} {
		post := apiPriority(t, d.base, http.MethodPost, "/api/tasks",
			`{"title":"priority `+strings.ReplaceAll(spelling, `"`, `'`)+`","repo":"acme/widgets","priority":`+spelling+`}`)
		patch := apiPriority(t, d.base, http.MethodPatch, "/api/tasks/"+base.ID, `{"priority":`+spelling+`}`)
		flagValue := strings.Trim(spelling, `"`)
		create := cliPriority(t, grain, d.base, "-json", "create", "-title", "cli priority "+flagValue, "-repo", "acme/widgets", "-priority", flagValue)
		edit := cliPriority(t, grain, d.base, "-json", "update", base.ID, "-priority", flagValue)
		fmt.Fprintf(&table, "%s\t%s\t%s\t%s\n", spelling, post, patch, create+"\t"+edit)
	}
	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&table, "\nGET /api/tasks (the board's list):\n")
	for _, tk := range tasks {
		if strings.Contains(tk.Title, "priority") {
			fmt.Fprintf(&table, "%s\t%q\tpriority=%d\n", tk.ID, tk.Title, tk.Priority)
		}
	}
	writeWalk(t, out, "priority.txt", table.String())
	t.Logf("priority:\n%s", table.String())

	waitUntil(t, "the sweep to finish in the sandbox", 10*time.Minute, func() bool { return agent.done("sweep.txt") })
	transcript := agent.output(t, "sweep.txt")
	writeWalk(t, out, "transcript-sweep.txt", transcript)
	t.Logf("sweep:\n%s", transcript)
	if !strings.Contains(transcript, "$ grain grep func RotateLocalAPIToken src://pkg\n  exit=0") {
		t.Errorf("grain grep over src://pkg did not find pkg/ui's RotateLocalAPIToken")
	}
}

// apiPriority sends body and answers "<status> <error or ok>\t<stored priority>".
func apiPriority(t *testing.T, base, method, path, body string) string {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	presentAccessCode(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "error " + err.Error() + "\t-"
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var task ui.Task
	if resp.StatusCode < 300 && json.Unmarshal(raw, &task) == nil {
		return fmt.Sprintf("%d accepted\t%d", resp.StatusCode, task.Priority)
	}
	return fmt.Sprintf("%d %s\t-", resp.StatusCode, strings.TrimSpace(string(raw)))
}

// cliPriority runs the host grain against base and answers
// "exit=<n> <first line>\t<stored priority>".
func cliPriority(t *testing.T, grain, base string, args ...string) string {
	t.Helper()
	cmd := exec.Command(grain, args...)
	cmd.Env = append(os.Environ(), "GRAIN_SERVER="+base, "HOME="+t.TempDir())
	raw, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	var task ui.Task
	if code == 0 && json.Unmarshal(raw, &task) == nil {
		return fmt.Sprintf("exit=0 accepted\t%d", task.Priority)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	return fmt.Sprintf("exit=%d %s\t-", code, first)
}

func writeWalk(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Errorf("writing %s: %v", name, err)
	}
}
