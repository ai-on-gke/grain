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

package deploy

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestClientScriptExistsAndIsExecutable(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(t), "scripts", "client.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat %s: %v", scriptPath, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("scripts/client.sh is not executable: mode %v", info.Mode())
	}

	cmd := exec.Command("bash", "-n", scriptPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n syntax check failed: %v\n%s", err, string(out))
	}
}

func TestClientScriptHelpAndMissingHost(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(t), "scripts", "client.sh")

	// --help
	cmd := exec.Command("bash", scriptPath, "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--help failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "Usage: client.sh") {
		t.Errorf("--help output missing Usage: %s", string(out))
	}

	// -h
	cmd = exec.Command("bash", scriptPath, "-h")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("-h failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "Usage: client.sh") {
		t.Errorf("-h output missing Usage: %s", string(out))
	}

	// Missing host
	cmd = exec.Command("bash", scriptPath)
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure on missing host, got success\n%s", string(out))
	}
	if !strings.Contains(string(out), "missing target host") {
		t.Errorf("output missing 'missing target host': %s", string(out))
	}

	// Unknown option
	cmd = exec.Command("bash", scriptPath, "--invalid-flag")
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure on invalid flag, got success\n%s", string(out))
	}
	if !strings.Contains(string(out), "unrecognized option") {
		t.Errorf("output missing 'unrecognized option': %s", string(out))
	}
}

func TestClientScriptSSHArgumentsAndFlags(t *testing.T) {
	dir := t.TempDir()
	sshLog := filepath.Join(dir, "ssh-calls.log")

	// Create a stub ssh that logs its arguments and exits
	stubSSH := filepath.Join(dir, "ssh")
	sshContent := fmt.Sprintf(`#!/usr/bin/env bash
echo "$@" >> %q
exit 0
`, sshLog)
	if err := os.WriteFile(stubSSH, []byte(sshContent), 0o755); err != nil {
		t.Fatalf("writing stub ssh: %v", err)
	}

	scriptPath := filepath.Join(repoRoot(t), "scripts", "client.sh")

	// Run with custom ports, extra ssh options, and --no-browser
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", scriptPath,
		"-p", "9090",
		"-r", "8420",
		"--no-browser",
		"-d", "1",
		"myuser@myhost",
		"-i", "/path/to/key",
		"-p", "2222",
	)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
	)

	_ = cmd.Run()

	logBytes, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatalf("reading ssh log: %v", err)
	}
	logStr := string(logBytes)

	// Verify required flags were passed to ssh
	if !strings.Contains(logStr, "-L 9090:localhost:8420") {
		t.Errorf("ssh arguments missing port forward '-L 9090:localhost:8420': got %q", logStr)
	}
	if !strings.Contains(logStr, "ExitOnForwardFailure=yes") {
		t.Errorf("ssh arguments missing ExitOnForwardFailure=yes: got %q", logStr)
	}
	if !strings.Contains(logStr, "myuser@myhost") {
		t.Errorf("ssh arguments missing target host: got %q", logStr)
	}
	if !strings.Contains(logStr, "-i /path/to/key") {
		t.Errorf("ssh arguments missing extra arg '-i /path/to/key': got %q", logStr)
	}
	if !strings.Contains(logStr, "-p 2222") {
		t.Errorf("ssh arguments missing extra arg '-p 2222': got %q", logStr)
	}
}

func TestClientScriptBrowserOpening(t *testing.T) {
	dir := t.TempDir()
	browserLog := filepath.Join(dir, "browser-calls.log")

	// Pick a free local port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	localPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Stub browser
	stubBrowser := filepath.Join(dir, "mybrowser")
	browserScript := fmt.Sprintf(`#!/usr/bin/env bash
echo "$@" >> %q
`, browserLog)
	if err := os.WriteFile(stubBrowser, []byte(browserScript), 0o755); err != nil {
		t.Fatalf("writing stub browser: %v", err)
	}

	// Stub ssh that starts a listener on localPort (to simulate port forwarding) and sleeps
	stubSSH := filepath.Join(dir, "ssh")
	sshScript := fmt.Sprintf(`#!/usr/bin/env bash
# Start a simple listener on localPort
python3 -c "import socket, time; s=socket.socket(); s.bind(('127.0.0.1', %d)); s.listen(1); time.sleep(2)" &
pid=$!
trap "kill $pid 2>/dev/null" EXIT INT TERM
wait $pid
`, localPort)
	if err := os.WriteFile(stubSSH, []byte(sshScript), 0o755); err != nil {
		t.Fatalf("writing stub ssh: %v", err)
	}

	scriptPath := filepath.Join(repoRoot(t), "scripts", "client.sh")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", scriptPath,
		"-p", fmt.Sprintf("%d", localPort),
		"-r", "80",
		"--path", "/dashboard",
		"remote-vm",
	)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"BROWSER="+stubBrowser,
	)

	_ = cmd.Run()

	bLogBytes, err := os.ReadFile(browserLog)
	if err != nil {
		t.Fatalf("reading browser log: %v", err)
	}
	bLog := string(bLogBytes)
	expectedURL := fmt.Sprintf("http://localhost:%d/dashboard", localPort)
	if !strings.Contains(bLog, expectedURL) {
		t.Errorf("browser called with %q, expected %q", bLog, expectedURL)
	}
}

func TestClientScriptReconnectionLoop(t *testing.T) {
	dir := t.TempDir()
	sshCountFile := filepath.Join(dir, "count.txt")

	// Stub ssh that exits non-zero to trigger reconnect
	stubSSH := filepath.Join(dir, "ssh")
	sshScript := fmt.Sprintf(`#!/usr/bin/env bash
echo "hit" >> %q
exit 1
`, sshCountFile)
	if err := os.WriteFile(stubSSH, []byte(sshScript), 0o755); err != nil {
		t.Fatalf("writing stub ssh: %v", err)
	}

	scriptPath := filepath.Join(repoRoot(t), "scripts", "client.sh")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", scriptPath,
		"-d", "0",
		"--no-browser",
		"remote-vm",
	)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
	)

	_ = cmd.Run()

	countBytes, err := os.ReadFile(sshCountFile)
	if err != nil {
		t.Fatalf("reading count file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(countBytes)), "\n")
	if len(lines) < 2 {
		t.Errorf("expected multiple SSH reconnection attempts, got %d", len(lines))
	}
}

// SIGINT while a tunnel is up runs client.sh's own trap: the ssh child
// is stopped and the script exits 130 saying so. The signal is sent only
// once the stub ssh has started, which is after the trap is installed, so
// a bash killed by the default SIGINT disposition (no exit code, no
// "Stopped.") fails here instead of passing for the wrong reason.
func TestClientScriptSignalHandling(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "ssh-started")
	stubSSH := filepath.Join(dir, "ssh")
	// exec, so the TERM client.sh's cleanup sends reaches the sleep
	// itself rather than leaving it orphaned for thirty seconds.
	sshScript := fmt.Sprintf(`#!/usr/bin/env bash
touch %q
exec sleep 30
`, started)
	if err := os.WriteFile(stubSSH, []byte(sshScript), 0o755); err != nil {
		t.Fatalf("writing stub ssh: %v", err)
	}

	scriptPath := filepath.Join(repoRoot(t), "scripts", "client.sh")

	var out strings.Builder
	cmd := exec.Command("bash", scriptPath, "--no-browser", "remote-vm")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting client.sh: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("client.sh never started its ssh tunnel; output:\n%s", out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("signaling client.sh: %v", err)
	}

	select {
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("client.sh did not terminate promptly after SIGINT")
	case <-done:
	}
	if code := cmd.ProcessState.ExitCode(); code != 130 {
		t.Errorf("client.sh exited %d after SIGINT, want 130 from its own trap; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "[grain-client] Stopped.") {
		t.Errorf("client.sh did not run its cleanup on SIGINT; output:\n%s", out.String())
	}
}
