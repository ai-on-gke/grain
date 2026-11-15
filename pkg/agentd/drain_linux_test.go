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

package agentd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

// TestDrainFlagEndsAHostAgent is the 2026-11-13 walkthrough's F1:
// `grain-agentd -drain` was an unknown flag, so a grain-agentd had no
// drain of its own. Against a real host agent serving a socket with
// nothing running, -drain must exit 0 and the host agent must exit (a
// cutover with nothing running ends at once), for systemd to restart it.
func TestDrainFlagEndsAHostAgent(t *testing.T) {
	notRoot(t)
	dir, err := os.MkdirTemp("", "agentd-drain")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "agentd.yaml")
	writeFile(t, allow, "clis:\n  agy:\n    path: /bin/true\n", 0o600)
	sock := filepath.Join(dir, "a.sock")
	srv := &hostd.Server{
		Socket:          sock,
		Allowlist:       allow,
		AllowlistOwners: owners(),
		StateDir:        filepath.Join(dir, "state"),
		AgentsOnly:      true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx) }()
	c := hostd.Client{Socket: sock, Build: "test"}
	if _, err := c.Hello(ctx); err != nil {
		t.Fatalf("hello before the drain: %v", err)
	}

	var out, errOut bytes.Buffer
	if code := run([]string{"-drain", "-socket", sock}, &out, &errOut); code != 0 {
		t.Fatalf("-drain = %d: %s", code, errOut.String())
	}
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve after -drain = %v, want a clean exit", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the host agent was still serving 20s after -drain")
	}
}

func TestDrainFlagSaysWhenNothingAnswers(t *testing.T) {
	notRoot(t)
	sock := filepath.Join(t.TempDir(), "none.sock")
	var out, errOut bytes.Buffer
	if code := run([]string{"-drain", "-socket", sock}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "not reachable") {
		t.Errorf("-drain with no host agent = %d, %q; want 1 and why", code, errOut.String())
	}
}

type countDrains struct{ n atomic.Int32 }

func (c *countDrains) Drain() { c.n.Add(1) }

// TestSIGUSR1Drains: grain-agentd ignored SIGUSR1, which `grain host`
// documents as its drain (F1). drainOnSignal is what run wires to it.
func TestSIGUSR1Drains(t *testing.T) {
	sig := make(chan os.Signal, 1)
	var c countDrains
	done := make(chan struct{})
	go func() { drainOnSignal(sig, &c); close(done) }()
	sig <- syscall.SIGUSR1
	close(sig)
	<-done
	if got := c.n.Load(); got != 1 {
		t.Errorf("drains after one SIGUSR1 = %d, want 1", got)
	}
}

// TestDefaultSocketFallsBackToTheUsersRunDir: with XDG_RUNTIME_DIR
// dropped (sudo -u, su), the default is still the socket
// grain-agentd.service listens on, in the user's own /run/user/<uid>
// (F1); a run dir missing or owned by somebody else falls back to /tmp.
func TestDefaultSocketFallsBackToTheUsersRunDir(t *testing.T) {
	uid := os.Getuid()
	base := t.TempDir()
	if got, want := defaultSocketFor("/xdg", uid, base), filepath.Join("/xdg", Program, "agentd.sock"); got != want {
		t.Errorf("with XDG_RUNTIME_DIR: %q, want %q", got, want)
	}
	tmp := filepath.Join(os.TempDir(), Program+"-"+strconv.Itoa(uid), "agentd.sock")
	if got := defaultSocketFor("", uid, base); got != tmp {
		t.Errorf("with no run dir: %q, want %q", got, tmp)
	}
	run := filepath.Join(base, strconv.Itoa(uid))
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, want := defaultSocketFor("", uid, base), filepath.Join(run, Program, "agentd.sock"); got != want {
		t.Errorf("with the user's run dir: %q, want %q", got, want)
	}
	if got := defaultSocketFor("", uid+1, base); got != filepath.Join(os.TempDir(), Program+"-"+strconv.Itoa(uid+1), "agentd.sock") {
		t.Errorf("another uid's run dir that is not there: %q", got)
	}
}
