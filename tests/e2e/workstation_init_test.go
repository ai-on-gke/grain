package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// TestWorkstationInitToAFirstTask is docs/quickstart.md run by a
// machine: `grain init workstation -non-interactive` in a fresh home
// directory, the daemon started the way grain.service starts it (the
// daemon.sh init wrote), and a trivial task carried through to its end
// by a fake agent -- a custom backend that answers with a closing
// comment and calls no model. Host sandboxes, because a clean CI
// container has no docker of its own to give a run.
func TestWorkstationInitToAFirstTask(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real daemon")
	}
	bin := buildGrainCLI(t)
	home := t.TempDir()
	addr := freeLoopbackAddr(t)
	env := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "GRAIN_DATA_DIR=")

	initCmd := exec.Command(bin, "init", "workstation", "-non-interactive",
		"-repo", "acme/widgets", "-sandbox-backend", "host", "-model-backend", "claude",
		"-owner", "e2e@example.com", "-ui-addr", addr, "-start-service=false")
	initCmd.Env = env
	out, err := initCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("grain init workstation: %v\n%s", err, out)
	}
	dataDir := filepath.Join(home, ".grain")
	for _, f := range []string{
		filepath.Join(dataDir, "grain_policy.yml"),
		filepath.Join(dataDir, "daemon.sh"),
		filepath.Join(home, ".config", "systemd", "user", "grain.service"),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("init wrote no %s: %v\n%s", f, err, out)
		}
	}

	// The fake agent: one JSON request in, one closing comment out.
	ran := filepath.Join(home, "fake-agent-ran")
	agent := filepath.Join(home, "fake-agent")
	script := fmt.Sprintf(`#!/bin/sh
cat > %[1]s
printf '%%s\n' '{"protocol_version":2,"result":{"final_text":"Hello from the fake agent.","tool_calls":[{"name":"comment_on_issue","arguments":{"comment":"Hello from the fake agent."},"text":"ok","is_error":false}],"transcript":"said hello"}}'
`, ran)
	if err := os.WriteFile(agent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	// -sandbox-port-base=0 for the reason the standalone merge test
	// gives: the default block at 9728 may already be held on the
	// machine running this, a grain sandbox included.
	daemonCmd := exec.Command(filepath.Join(dataDir, "daemon.sh"), "-sandbox-port-base=0")
	daemonCmd.Env = env
	daemonCmd.Stdout, daemonCmd.Stderr = &logs, &logs
	daemonCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := daemonCmd.Start(); err != nil {
		t.Fatal(err)
	}
	// exited is closed once the daemon has, with its error in exitErr.
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
			t.Logf("daemon exit: %v", exitErr)
		}
		if t.Failed() {
			t.Logf("daemon output:\n%s", logs.String())
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// Every caller presents the access code, so the API wants the local API token the daemon writes into its
	// data directory -- the one the CLI reads through $GRAIN_DATA_DIR.
	// It is handed to the client outright, so no GRAIN_* variable of
	// the machine running this test can stand in for it.
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
		ID: "fake", Name: "fake agent", Framework: model.AgentFrameworkCustom, Command: agent,
		// A custom command with an empty list has no models, and its
		// tasks wait for one to be set.
		Models: []string{"fake-model"},
	}}
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{AgentBackends: &backends}); err != nil {
		t.Fatalf("configuring the fake agent: %v", err)
	}
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Say hello", Description: "Leave a closing comment.", NoRepo: true,
		AgentFramework: model.AgentFrameworkCustom,
	})
	if err != nil {
		t.Fatalf("filing the task: %v", err)
	}
	if err := client.Approve(ctx, task.ID); err != nil {
		t.Fatalf("approving the task: %v", err)
	}

	var attempt ui.Attempt
	awaitWorkstation(t, ctx, "the task's attempt to finish", exited, func() error {
		detail, err := client.GetTask(ctx, task.ID)
		if err != nil {
			return err
		}
		if len(detail.Attempts) == 0 || detail.Attempts[len(detail.Attempts)-1].FinishedAt == nil {
			return fmt.Errorf("task is %s (%q), with %d attempts", detail.State, detail.Activity, len(detail.Attempts))
		}
		attempt = detail.Attempts[len(detail.Attempts)-1]
		return nil
	})
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("the attempt finished (%s: %s) without running the fake agent", attempt.Outcome, attempt.Detail)
	}
	if attempt.Outcome != "succeeded" {
		t.Fatalf("attempt ended %q (%s), want succeeded", attempt.Outcome, attempt.Detail)
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range detail.Comments {
		found = found || strings.Contains(c.Body, "Hello from the fake agent.")
	}
	if !found {
		t.Fatalf("the agent's closing comment is not on task %s (state %s): %+v", task.ID, detail.State, detail.Comments)
	}
}

// TestWorkstationInitContainerDaemonStarts is docs/quickstart.md's
// default path on a machine with docker: init chooses container
// sandboxes, and the daemon.sh it writes has to start and serve its API.
// It used to exit at once with "-container-git-proxy-host is required
// with -container-sandboxes", which grain.service retried five times and
// gave up on.
func TestWorkstationInitContainerDaemonStarts(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real daemon")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("container sandboxes need a working docker: %v", err)
	}
	bin := buildGrainCLI(t)
	home := t.TempDir()
	addr := freeLoopbackAddr(t)
	env := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "GRAIN_DATA_DIR=")

	initCmd := exec.Command(bin, "init", "workstation", "-non-interactive",
		"-repo", "acme/widgets", "-sandbox-backend", "container", "-model-backend", "claude",
		"-owner", "e2e@example.com", "-ui-addr", addr, "-install-service=false", "-start-service=false")
	initCmd.Env = env
	out, err := initCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("grain init workstation: %v\n%s", err, out)
	}
	dataDir := filepath.Join(home, ".grain")

	var logs bytes.Buffer
	// -sandbox-port-base=0 for the reason the standalone merge test
	// gives: the default block at 9728 may already be held on the
	// machine running this, a grain sandbox included.
	daemonCmd := exec.Command(filepath.Join(dataDir, "daemon.sh"), "-sandbox-port-base=0")
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
			launcher, _ := os.ReadFile(filepath.Join(dataDir, "daemon.sh"))
			t.Logf("daemon exit: %v\ninit output:\n%s\ndaemon.sh:\n%s\ndaemon output:\n%s", exitErr, out, launcher, logs.String())
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var token string
	awaitWorkstation(t, ctx, "the daemon to write its local API token", exited, func() error {
		b, err := os.ReadFile(ui.LocalAPITokenPath(dataDir))
		if err != nil {
			return err
		}
		if token = strings.TrimSpace(string(b)); token == "" {
			return errors.New("the token file is empty")
		}
		return nil
	})
	awaitWorkstation(t, ctx, "the daemon to answer GET /api/sandboxes", exited, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/sandboxes", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET /api/sandboxes: %s", resp.Status)
		}
		return nil
	})
}

func awaitWorkstation(t *testing.T, ctx context.Context, what string, exited <-chan struct{}, cond func() error) {
	t.Helper()
	var last error
	for {
		if last = cond(); last == nil {
			return
		}
		select {
		case <-exited:
			t.Fatalf("waiting for %s, the daemon exited", what)
		case <-ctx.Done():
			t.Fatalf("gave up waiting for %s: %v", what, errors.Join(ctx.Err(), last))
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// freeLoopbackAddr is a loopback address nothing listens on yet.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}
