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

import (
	"context"
	"encoding/base64"
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

// toolCallStubClaude writes the stub CLI TestRunHostAgentCannotReachAHostDirectorySandbox
// runs into dir, which the daemon mounts into every run host
// (-run-host-credential-dir). It does what a real claude does with the
// --mcp-config it is handed: starts the grain-sandbox server that file
// names, with that file's argv, and calls a sandbox tool on it -- a
// run_command reading the task's staged attachment. The server's argv
// goes to dir/mcp-args and its answer to dir/mcp-out; then it waits for
// dir/release and ends its turn.
func toolCallStubClaude(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	script := `#!/bin/sh
d=` + dir + `
prev=
for a in "$@"; do
	if [ "$prev" = --mcp-config ]; then cp "$a" $d/mcp.json; fi
	prev=$a
done
args=$(sed -n 's/.*"args": *\[\([^]]*\)\].*/\1/p' $d/mcp.json | tr ',' ' ' | tr -d '"')
echo "$args" > $d/mcp-args
{ echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"stub","version":"0"}}}'
  echo '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  echo '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_command","arguments":{"command":"cat attachments/*","description":"reading the attachment"}}}'
  sleep 5; } | /usr/local/bin/grain $args > $d/mcp-out.partial 2>&1
mv $d/mcp-out.partial $d/mcp-out
echo '{"type":"system","subtype":"init","session_id":"stub-session"}'
echo '{"type":"assistant","session_id":"stub-session","message":{"content":[{"type":"tool_use","id":"call-1","name":"mcp__grain-sandbox__run_command","input":{"command":"cat attachments/*"}}]}}'
echo '{"type":"user","session_id":"stub-session","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"read"}]}}'
while [ ! -e $d/release ]; do sleep 0.2; done
echo '{"type":"result","subtype":"success","is_error":false,"result":"read the attachment","session_id":"stub-session"}'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("sk-ant-oat01-stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunHostAgentCannotReachAHostDirectorySandbox is the end-to-end
// test of the soak's finding F3 (docs/test-runs/2026-11-02-session-lifecycle-soak.md):
// a real daemon, a real docker run host, and a directory sandbox -- the
// test build's stand-in for -host-sandboxes, a plain directory on the
// daemon's host (daemon.LocalSandboxes). The agent in the run host starts
// `grain mcpserver` from its --mcp-config and calls run_command to read
// the attachment staged in its sandbox.
//
// From the daemon's own machine the same argv reads the attachment. From
// the run host it cannot: no run host is given a host sandbox, so its
// mcpserver finds no -sandbox-root and never starts. That is why the
// daemon refuses -run-host-image with -host-sandboxes
// (daemon.CheckRunHostBackend), which this says with a real call rather
// than a reading of the code. If a run host is ever given its run's
// sandbox, this fails, and the refusal -- and what such a run host's
// run_command would reach beside the sandbox -- is what to revisit with
// it.
//
// It needs docker, so it runs only with GRAIN_RUNHOST_DOCKER=1 (the "run
// host" job in .github/workflows/tests.yml).
func TestRunHostAgentCannotReachAHostDirectorySandbox(t *testing.T) {
	if os.Getenv("GRAIN_RUNHOST_DOCKER") != "1" {
		t.Skip("set GRAIN_RUNHOST_DOCKER=1 to run against a real docker daemon")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	image := runHostTestImage(t)
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "grain"), "github.com/ai-on-gke/grain/cmd/grain")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building grain: %v\n%s", err, out)
	}

	const owner, repoName = "acme", "widgets"
	const taskID = "runhost-host-sandbox"
	const attachment = "the attachment staged in the run's own sandbox"
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"))
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
	githubHost := githubHostServer(t, sim, upstream)

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	credDir := t.TempDir()
	claudePath := toolCallStubClaude(t, credDir)

	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("seeding the store: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	human := model.Principal{Kind: model.PrincipalHuman, ID: "tester"}
	task := model.Task{
		ID: taskID, Intent: model.IntentImplement,
		Title: "read the attachment", Body: "the stub agent reads its attachment through its sandbox tools",
		Origin:  model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding: model.BindingDirective,
		Target:  &model.RepoRef{Owner: owner, Name: repoName},
	}
	task.Approval = &model.Attribution{Actor: human}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("seeding the task: %v", err)
	}
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("tester"), Capabilities: ui.OfferedCapabilities()}, store)
	if err := client.AddComment(ctx, taskID, "here is the file", []ui.AttachmentUpload{{
		Filename: "note.txt", ContentType: "text/plain",
		Content: base64.StdEncoding.EncodeToString([]byte(attachment + "\n")),
	}}); err != nil {
		t.Fatalf("attaching the file: %v", err)
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

	cfg := daemon.Config{
		DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Second,
		ClaudePath:   claudePath,
		RunHostImage: image, RunHostCredentialDir: credDir,
		GithubHost: githubHost, GithubInsecureHTTP: true,
	}
	seedTestDaemonConnections(t, cfg, `git:
  - credential: anonymous
models:
  - id: claude
    framework: claude
    models: [claude-sonnet-5]
    credential_file: `+filepath.Join(credDir, "token")+`
`)
	daemonCtx, stopDaemon := context.WithCancel(ctx)
	done, stopped := startLiveDaemon(daemonCtx, cfg)
	defer stopLiveDaemon(t, stopDaemon, done)

	outPath := filepath.Join(credDir, "mcp-out")
	if !pollUntil(ctx, 4*time.Minute, func() bool {
		_, err := os.Stat(outPath)
		return err == nil
	}) {
		invocations, _ := os.ReadFile(filepath.Join(credDir, "mcp-args"))
		t.Fatalf("the agent in the run host never finished its sandbox tool call (mcpserver argv %q)", invocations)
	}
	remote, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	rawArgs, err := os.ReadFile(filepath.Join(credDir, "mcp-args"))
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Fields(string(rawArgs))
	if len(args) == 0 || args[0] != "mcpserver" || !strings.Contains(string(rawArgs), "-sandbox-root") {
		t.Fatalf("the agent's --mcp-config started %q, want grain mcpserver -sandbox-root", rawArgs)
	}

	// From the daemon's machine, while the run still holds its sandbox:
	// the same argv, the same requests.
	stdin := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"stub","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_command","arguments":{"command":"cat attachments/*","description":"reading the attachment"}}}`,
	}, "\n") + "\n"
	local := exec.CommandContext(ctx, filepath.Join(bin, "grain"), args...)
	local.Stdin = strings.NewReader(stdin)
	localOut, err := local.CombinedOutput()
	if err != nil || !strings.Contains(string(localOut), attachment) {
		t.Fatalf("mcpserver on the daemon's machine did not read the attachment (%v):\n%s", err, localOut)
	}

	// From the run host.
	if strings.Contains(string(remote), attachment) {
		t.Fatalf("the agent in a run host read its host-directory sandbox, which daemon.CheckRunHostBackend "+
			"refuses -run-host-image with -host-sandboxes on the grounds that it cannot -- revisit that refusal:\n%s", remote)
	}
	if !strings.Contains(string(remote), "-sandbox-root") || !strings.Contains(string(remote), "is not a directory") {
		t.Fatalf("mcpserver in the run host failed some other way than finding no sandbox:\n%s", remote)
	}
	t.Logf("mcpserver in a run host, as the refusal expects: %s", remote)

	if err := os.WriteFile(filepath.Join(credDir, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := awaitFinishedRun(ctx, store, taskID, stopped); err != nil {
		t.Fatal(err)
	}
}
