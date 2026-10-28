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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/modelexec"
)

func TestRemoteExecutorAntigravityEndToEndDispatch(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	token := "antigravity-e2e-token"
	srv, err := modelexec.NewServer(modelexec.Config{
		Token: token,
		Wait:  5 * time.Second,
		ServeMCP: func(rwc io.ReadWriteCloser, args []string) error {
			defer rwc.Close()
			dec := json.NewDecoder(rwc)
			enc := json.NewEncoder(rwc)
			for {
				var req map[string]any
				if err := dec.Decode(&req); err != nil {
					return nil
				}
				// Mock MCP response
				resp := map[string]any{
					"jsonrpc": "2.0",
					"id":      req["id"],
					"result": map[string]any{
						"content": []map[string]any{
							{"type": "text", "text": "sandbox command output: OK"},
						},
					},
				}
				if err := enc.Encode(resp); err != nil {
					return err
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	go func() { _ = srv.Serve(ctx, ln) }()

	// Connect Daemon
	d := &modelexec.Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "remote-node-1",

		Binaries: map[string]string{"agy": "/bin/sh", "grain": "/bin/sh"},
	}
	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil || host == nil {
		t.Fatalf("srv.Host: %v, host=%v", err, host)
	}

	// 1. Verify UnconfinedHost & SandboxPlacer capabilities
	if !host.Unconfined() {
		t.Fatal("expected host to be unconfined")
	}

	remoteDir, err := host.MkdirTemp(ctx, "agy-remote-sandbox-*")
	if err != nil {
		t.Fatalf("MkdirTemp on remote host: %v", err)
	}
	defer func() { _ = host.RemoveAll(ctx, remoteDir) }()

	// Test credential placement on remote host
	credFile := filepath.Join(remoteDir, ".gcp-service-account.json")
	if err := host.PlaceFile(ctx, credFile, `{"project_id": "test-project"}`, "600"); err != nil {
		t.Fatalf("PlaceFile on remote host: %v", err)
	}

	readCred, _, err := host.ReadTail(ctx, credFile, 1024)
	if err != nil || !strings.Contains(string(readCred), "test-project") {
		t.Fatalf("ReadTail on remote credential = %q, want test-project", readCred)
	}

	// 2. Test Agent Framework running on RemoteHost
	// Create a script simulating Antigravity CLI executing on the remote host and tunneling MCP
	agentScript := `
python3 -c '
import os, sys, socket, json

sock_path = os.environ.get("GRAIN_EXECUTOR_SOCK")
if not sock_path:
    sys.exit("missing GRAIN_EXECUTOR_SOCK")

# Connect to MCP socket
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sock_path)
s.sendall(json.dumps(["mcpserver"]).encode("utf-8") + b"\n")

# Call run_command tool via MCP
req = {
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {"name": "run_command", "arguments": {"command": "git status"}}
}
s.sendall(json.dumps(req).encode("utf-8") + b"\n")

data = b""
while b"\n" not in data:
    chunk = s.recv(1024)
    if not chunk:
        break
    data += chunk
s.close()
print("AGENT_DISPATCH_COMPLETE:" + data.decode("utf-8").strip())
'
`
	// The tunnel socket is only in the environment of what is started
	// through the executor's grain binary, as `grain mcpserver` is.
	grain, err := host.BinaryPath("grain")
	if err != nil {
		t.Fatalf("BinaryPath(grain): %v", err)
	}
	// The server only serves an MCP tunnel whose argv grain issued to this
	// executor (mcpgrants.go). Without this grant the stream is refused and
	// closed, and the script's second write races that close (BrokenPipe).
	host.AuthorizeMCPArgs([]string{"mcpserver"})
	out, err := host.Run(ctx, agent.Command{
		Path: grain,
		Args: []string{"-c", agentScript},
	}, nil)
	if err != nil {
		t.Fatalf("agent execution on remote host failed: %v, out: %s", err, out)
	}
	if !strings.Contains(out, "AGENT_DISPATCH_COMPLETE") {
		t.Fatalf("agent output missing completion marker: %s", out)
	}
	if !strings.Contains(out, "sandbox command output: OK") {
		t.Fatalf("agent output missing the MCP tool response: %s", out)
	}
}

func TestRemoteExecutorClaudeAndCodexIntegration(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	token := "claude-codex-e2e-token"
	srv, err := modelexec.NewServer(modelexec.Config{
		Token: token,
		Wait:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	go func() { _ = srv.Serve(ctx, ln) }()

	// Connect Daemon offering claude and codex
	codexAuthFile := "/home/debian/.codex/auth.json"
	d := &modelexec.Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "claude-codex-node",

		Binaries:      map[string]string{"claude": "/bin/sh", "codex": "/bin/sh"},
		CodexAuthFile: codexAuthFile,
	}
	go func() { _ = d.Run(ctx) }()

	// 1. Claude Host
	claudeHost, err := srv.Host(ctx, "claude")
	if err != nil || claudeHost == nil {
		t.Fatalf("srv.Host(ai-team, claude): %v", err)
	}
	if !claudeHost.Unconfined() {
		t.Fatal("expected Claude remote host to be unconfined")
	}

	// 2. Codex Host & Auth file
	codexHost, err := srv.Host(ctx, "codex")
	if err != nil || codexHost == nil {
		t.Fatalf("srv.Host(ai-team, codex): %v", err)
	}
	if codexHost.CodexAuthFile() != codexAuthFile {
		t.Fatalf("CodexAuthFile() = %q, want %q", codexHost.CodexAuthFile(), codexAuthFile)
	}

	// Test execution on Codex host
	out, err := codexHost.Run(ctx, agent.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo CODEX_HOST_VERIFIED"},
	}, nil)
	if err != nil || strings.TrimSpace(out) != "CODEX_HOST_VERIFIED" {
		t.Fatalf("codexHost.Run = %q, %v; want CODEX_HOST_VERIFIED", out, err)
	}
}

func TestRemoteExecutorConcurrentAgentsOnSameNode(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	token := "concurrent-same-node-token"
	srv, err := modelexec.NewServer(modelexec.Config{
		Token: token,
		Wait:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	go func() { _ = srv.Serve(ctx, ln) }()

	// Connect a single daemon node with concurrency 3
	d := &modelexec.Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "multi-slot-node",

		Binaries:    map[string]string{"agy": "/bin/sh"},
		Concurrency: 3,
	}
	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil || host == nil {
		t.Fatalf("srv.Host: %v, host=%v", err, host)
	}

	if host.Concurrency() != 3 {
		t.Fatalf("host.Concurrency() = %d, want 3", host.Concurrency())
	}

	// Launch 3 concurrent agent runs, each writing to a local file in its working directory
	type agentResult struct {
		workDir string
		fileVal string
		err     error
	}
	resCh := make(chan agentResult, 3)

	for i := 1; i <= 3; i++ {
		go func(agentNum int) {
			script := fmt.Sprintf("echo 'agent-%d' > my_agent.txt && pwd && cat my_agent.txt", agentNum)
			out, runErr := host.Run(ctx, agent.Command{
				Path: "/bin/sh",
				Args: []string{"-c", script},
			}, nil)
			if runErr != nil {
				resCh <- agentResult{err: runErr}
				return
			}
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) < 2 {
				resCh <- agentResult{err: fmt.Errorf("unexpected output lines: %q", out)}
				return
			}
			resCh <- agentResult{
				workDir: strings.TrimSpace(lines[0]),
				fileVal: strings.TrimSpace(lines[1]),
			}
		}(i)
	}

	seenDirs := make(map[string]bool)
	seenVals := make(map[string]bool)
	for i := 0; i < 3; i++ {
		select {
		case r := <-resCh:
			if r.err != nil {
				t.Fatalf("concurrent agent %d returned error: %v", i, r.err)
			}
			if seenDirs[r.workDir] {
				t.Fatalf("working directory %q was shared between concurrent agents!", r.workDir)
			}
			seenDirs[r.workDir] = true
			seenVals[r.fileVal] = true
		case <-time.After(10 * time.Second):
			t.Fatalf("timeout waiting for agent %d", i)
		}
	}

	if len(seenDirs) != 3 {
		t.Fatalf("expected 3 distinct working directories, got %d", len(seenDirs))
	}
	if len(seenVals) != 3 {
		t.Fatalf("expected 3 distinct agent values, got %d", len(seenVals))
	}
}

func TestRemoteExecutorGitProxyTunnelingEndToEnd(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Setup simulated upstream Git repository
	upstreamDir := t.TempDir()
	repoPath := filepath.Join(upstreamDir, "repo.git")
	runCmd := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed in %s: %v\noutput: %s", args, dir, err, out)
		}
	}

	runCmd(upstreamDir, "git", "init", "--bare", "-b", "main", repoPath)
	runCmd(repoPath, "git", "config", "http.receivepack", "true")

	// Seed with an initial commit
	seedDir := t.TempDir()
	runCmd(seedDir, "git", "init", "-b", "main")
	runCmd(seedDir, "git", "config", "user.name", "Repo Owner")
	runCmd(seedDir, "git", "config", "user.email", "owner@example.com")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("# Grain Remote Test\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	runCmd(seedDir, "git", "add", "README.md")
	runCmd(seedDir, "git", "commit", "-m", "initial commit")
	runCmd(seedDir, "git", "remote", "add", "origin", repoPath)
	runCmd(seedDir, "git", "push", "origin", "main")

	// 2. Start Grain server with GitProxy CGI backend
	gitExecPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatalf("git --exec-path: %v", err)
	}
	gitHttpBackend := filepath.Join(strings.TrimSpace(string(gitExecPath)), "git-http-backend")
	gitProxyHandler := &cgi.Handler{
		Path: gitHttpBackend,
		Env: []string{
			"GIT_PROJECT_ROOT=" + upstreamDir,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

	token := "gitproxy-e2e-token"
	srv, err := modelexec.NewServer(modelexec.Config{
		Token:    token,
		Wait:     5 * time.Second,
		GitProxy: gitProxyHandler,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	go func() { _ = srv.Serve(ctx, ln) }()

	// 3. Connect remote executor Daemon
	d := &modelexec.Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "gitproxy-worker",

		Binaries: map[string]string{"agy": "/bin/sh"},
	}
	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil || host == nil {
		t.Fatalf("srv.Host(core, agy): %v", err)
	}

	proxyURL := host.GitProxyURL()
	if proxyURL == "" || !strings.HasPrefix(proxyURL, "http://127.0.0.1:") {
		t.Fatalf("host.GitProxyURL() = %q, want http://127.0.0.1:<port>", proxyURL)
	}

	// 4. Test ConfigureGitCredentials on RemoteHost
	remoteSandbox, err := host.MkdirTemp(ctx, "gitproxy-sandbox-*")
	if err != nil {
		t.Fatalf("MkdirTemp on host: %v", err)
	}
	defer func() { _ = host.RemoveAll(ctx, remoteSandbox) }()

	cloneURL := fmt.Sprintf("%s/repo.git", proxyURL)
	identity := mcp.GitIdentity{
		Name:  "Grain Bot",
		Email: "bot@grain.local",
	}
	if err := host.ConfigureGitCredentials(ctx, remoteSandbox, cloneURL, "fake-proxy-token", identity); err != nil {
		t.Fatalf("ConfigureGitCredentials on host: %v", err)
	}

	// 5. Execute git clone, commit, push on the RemoteHost using host.Run
	agentGitScript := fmt.Sprintf(`
set -e
cd %s
export HOME=%s
git clone %s work
cd work
git config user.name "Grain Bot"
git config user.email "bot@grain.local"
echo "change from remote executor via tunneled proxy" >> README.md
git add README.md
git commit -m "feat: change from remote executor"
git push origin main
echo "REMOTE_GIT_WORKFLOW_SUCCESS"
`, remoteSandbox, remoteSandbox, cloneURL)

	out, err := host.Run(ctx, agent.Command{
		Path: "/bin/sh",
		Args: []string{"-c", agentGitScript},
	}, nil)
	if err != nil {
		t.Fatalf("host.Run git workflow failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "REMOTE_GIT_WORKFLOW_SUCCESS") {
		t.Fatalf("output missing REMOTE_GIT_WORKFLOW_SUCCESS marker: %s", out)
	}

	// 6. Verify upstream repo has the new commit
	verifyDir := t.TempDir()
	runCmd("", "git", "clone", repoPath, verifyDir)
	readmeBytes, err := os.ReadFile(filepath.Join(verifyDir, "README.md"))
	if err != nil || !strings.Contains(string(readmeBytes), "change from remote executor via tunneled proxy") {
		t.Fatalf("verified README.md content = %q, missing remote change", string(readmeBytes))
	}
}
