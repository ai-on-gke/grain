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

//go:build linux

package agentdcompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agentd/release"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// TestReleasedAgentdsUpdateToHead installs each released grain-agentd
// the way `grain-agentd install` leaves it, starts a detached run on it,
// and has head's `grain-agentd update` take head from the controller's
// signed release: the old agent cuts over once the run ends, the agent
// restarted through ~/.local/bin/grain-agentd (what systemd --user's
// Restart=always does) is head, it adopts the old build's spool, and the
// controller's hello confirms the update.
func TestReleasedAgentdsUpdateToHead(t *testing.T) {
	bins := binaries(t)
	head, ok := bins["head"]
	if !ok {
		t.Skipf("$%s names no head build to update to", BinariesEnv)
	}
	for label, bin := range bins {
		if label == "head" {
			continue
		}
		t.Run(label, func(t *testing.T) { updateToHead(t, bin, head) })
	}
}

func updateToHead(t *testing.T, oldBin, headBin string) {
	root, err := os.MkdirTemp("", "agu")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "h")
	stage := filepath.Join(home, ".grain", "hostd-stage")
	state := filepath.Join(root, "state")
	for _, d := range []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".config", "grain"), state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cli := filepath.Join(root, "fake-cli")
	writeOrFatal(t, cli, []byte(fakeCLI), 0o755)
	allowlist := filepath.Join(root, "agentd.yaml")
	writeOrFatal(t, allowlist, []byte(fmt.Sprintf("clis:\n  fake:\n    path: %s\n    paths_under: [%s]\n    cwd_under: [%s]\n", cli, stage, stage)), 0o644)
	const token = "compat-test-token"
	tokenFile := filepath.Join(root, "token")
	writeOrFatal(t, tokenFile, []byte(token+"\n"), 0o600)

	// The release key pinned at install, and head signed by it.
	pub, priv, err := release.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pem, _ := release.FormatPublicKeyPEM(pub)
	writeOrFatal(t, filepath.Join(home, ".config", "grain", release.DefaultConfigFileName), pem, 0o644)
	headBytes, err := os.ReadFile(headBin)
	if err != nil {
		t.Fatal(err)
	}
	headVersion := buildOf(t, headBin)
	platform := runtime.GOOS + "/" + runtime.GOARCH
	m, err := release.NewManifestForBinary(headVersion, platform, headBytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m, err = release.SignManifest(m, priv, nil); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(m)
	rel := http.NewServeMux()
	rel.HandleFunc("GET /api/hostd/agentd/"+platform+"/manifest.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(manifest) })
	rel.HandleFunc("GET /api/hostd/agentd/"+platform, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(headBytes) })
	releases := httptest.NewServer(rel)
	t.Cleanup(releases.Close)

	// The old build, installed where install puts it.
	installed := filepath.Join(home, ".local", "bin", "grain-agentd")
	oldBytes, err := os.ReadFile(oldBin)
	if err != nil {
		t.Fatal(err)
	}
	writeOrFatal(t, installed, oldBytes, 0o755)

	ctl := startController(t, token)
	socket := filepath.Join(root, "s.sock")
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "USER=" + os.Getenv("USER")}
	start := func() *agent {
		a := &agent{exited: make(chan error, 1), log: &bytes.Buffer{}}
		a.cmd = exec.Command(installed, "-server", ctl.http.URL, "-token-file", tokenFile,
			"-key-file", filepath.Join(root, "no-key"), "-allowlist", allowlist, "-socket", socket, "-state-dir", state)
		a.cmd.Env = env
		a.cmd.Stdout, a.cmd.Stderr = lockedWriter(a.log), lockedWriter(a.log)
		if err := a.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { a.exited <- a.cmd.Wait() }()
		t.Cleanup(func() {
			_ = a.cmd.Process.Kill()
			err := <-a.exited
			a.exited <- err
			if t.Failed() {
				t.Logf("%s's log:\n%s", a.cmd.Path, logText(a.log))
			}
		})
		return a
	}
	old := start()
	ctl.waitConnected(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := ctl.client()
	peer, err := c.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stageRoot := peer.StageRoot

	// A detached run, held open by a gate, going through the cutover.
	gate := filepath.Join(stageRoot, "update-gate")
	if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageMkdirAll, Path: gate, Perm: 0o700}); err != nil {
		t.Fatal(err)
	}
	started := make(chan hostd.Started, 1)
	dc := c
	dc.OnStarted = func(s hostd.Started) { started <- s }
	dc.Detach = func(context.Context) bool { return true }
	runCtx, stopRun := context.WithCancel(ctx)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = dc.Run(runCtx, hostd.Request{Kind: hostd.KindCLI, Name: "fake", Args: []string{"wait", gate},
			Dir: stageRoot, Detach: true, Run: "run-u", Task: "task-u"}, nil, io.Discard, io.Discard)
	}()
	var st hostd.Started
	select {
	case st = <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the detached start never started")
	}
	stopRun()
	<-runDone

	// The owner runs head's update.
	up := exec.Command(headBin, "update", "-version", headVersion, "-server", releases.URL, "-allowlist", allowlist, "-socket", socket)
	up.Env = env
	out, err := up.CombinedOutput()
	if err != nil {
		t.Fatalf("grain-agentd update: %v\n%s", err, out)
	}
	t.Logf("grain-agentd update:\n%s", out)
	if got := buildOf(t, installed); got != headVersion {
		t.Fatalf("%s is %s after the update, want %s", installed, got, headVersion)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "lib", "grain-agentd", "versions", buildOf(t, oldBin), "grain-agentd")); err != nil {
		t.Fatalf("the previous build is not kept: %v", err)
	}

	// The old agent is cutting over: it still runs while the run does,
	// and exits once it ends.
	select {
	case err := <-old.exited:
		t.Fatalf("the old agent exited with a run going: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	writeOrFatal(t, filepath.Join(gate, "go"), []byte("1"), 0o600)
	select {
	case err := <-old.exited:
		old.exited <- err
		if err != nil {
			t.Fatalf("the old agent's cutover: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the old agent did not cut over once the run ended")
	}

	// systemd starts ~/.local/bin/grain-agentd again: head.
	start()
	ctl.restart(t)
	ctl.waitConnected(t)
	c = ctl.client()
	h, err := c.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.Build != headVersion || !h.Has(hostd.FeatureSelfUpdate) || h.Platform != platform || h.UpdateMode != hostd.UpdateOffer {
		t.Fatalf("hello after the update = %+v: want head (%s) offering self_update on %s", h, headVersion, platform)
	}
	var out2 bytes.Buffer
	exit, _, err := c.Attach(ctx, st.ID, hostd.Cursor{}, &out2, io.Discard)
	if err != nil {
		t.Fatalf("attach to the old build's run on head: %v", err)
	}
	if exit.Code != 3 || !strings.Contains(out2.String(), "released") {
		t.Fatalf("the old build's run, read on head: exit %+v, stdout %q", exit, out2.String())
	}
	pending := filepath.Join(home, ".local", "lib", "grain-agentd", "pending")
	waitFor(t, "the controller's hello to confirm the update", func() bool {
		_, err := os.Stat(pending)
		return os.IsNotExist(err)
	})
}

func buildOf(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "-build").Output()
	if err != nil {
		t.Fatalf("%s -build: %v", bin, err)
	}
	return strings.TrimSpace(string(out))
}

func writeOrFatal(t *testing.T, path string, data []byte, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatal(err)
	}
}
