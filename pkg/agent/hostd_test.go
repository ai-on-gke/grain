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

package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func startHostd(t *testing.T) (hostd.Client, string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)
	dir, err := os.MkdirTemp("", "hostd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "hostd.yaml")
	body := fmt.Sprintf("clis:\n  fakecli:\n    path: %s\n    paths_under: [%[2]s]\n    cwd_under: [%[2]s]\n    env: [HOME]\n", sh, dir)
	if err := os.WriteFile(allow, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &hostd.Server{
		Socket:          filepath.Join(dir, "hostd.sock"),
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		Logf:            func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; ; i++ {
		if _, err := os.Stat(srv.Socket); err == nil {
			break
		}
		if i > 500 {
			t.Fatal("hostd never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return hostd.Client{Socket: srv.Socket}, dir
}

func TestHostdHostRunsTheCLIWithLocalHostsContract(t *testing.T) {
	client, dir := startHostd(t)
	var logged []string
	h := HostdHost{LocalHost: LocalHost{Logf: func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}}, Client: client, Entry: "fakecli"}

	var tee strings.Builder
	out, err := h.Run(context.Background(), Command{Path: "/ignored/by/hostd", Args: []string{"-c", `cat; echo "home=$HOME"`},
		Stdin: "prompt\n", Env: []string{"HOME=" + dir}, Dir: dir}, &tee)
	if err != nil {
		t.Fatal(err)
	}
	if want := "prompt\nhome=" + dir + "\n"; out != want || tee.String() != want {
		t.Errorf("stdout = %q, tee = %q, want %q", out, tee.String(), want)
	}
	if len(logged) == 0 || strings.Contains(logged[0], "/ignored/by/hostd") || !strings.Contains(logged[0], `(entry "fakecli"): fakecli -c`) {
		t.Errorf("log = %q, want entry name and not Command.Path", logged)
	}

	logged = nil
	h.Path = "/host/bin/fakecli"
	if _, err := h.Run(context.Background(), Command{Path: "/usr/local/bin/fakecli", Args: []string{"-c", "true"}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(logged) == 0 || strings.Contains(logged[0], "/usr/local/bin/fakecli") || !strings.Contains(logged[0], `(entry "fakecli"): /host/bin/fakecli -c`) {
		t.Errorf("log = %q, want hostd-reported path and not container Command.Path", logged)
	}

	out, err = h.Run(context.Background(), Command{Args: []string{"-c", "echo partial; echo boom >&2; exit 2"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") || out != "partial\n" {
		t.Errorf("a failing CLI: out %q, err %v; want its stdout and an error carrying stderr", out, err)
	}

	_, err = h.Run(context.Background(), Command{Args: []string{"-c", "true"}, Env: []string{"GITHUB_TOKEN=x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("an env var outside the entry: err = %v, want a refusal", err)
	}
}

// The same cases as host_waitdelay_test.go, for a CLI run through hostd.
func TestHostdHostRunReturnsOnceTheCLIIsGoneEvenIfAChildHoldsItsOutput(t *testing.T) {
	client, _ := startHostd(t)
	h := HostdHost{LocalHost: LocalHost{Logf: func(string, ...any) {}}, Client: client, Entry: "fakecli"}
	for _, tc := range []struct {
		name    string
		script  string
		wantErr bool
	}{
		{name: "killed", script: "echo started; sleep 60 & kill -9 $$", wantErr: true},
		{name: "exited", script: "echo started; sleep 60 & exit 0", wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			out, err := h.Run(context.Background(), Command{Args: []string{"-c", tc.script}}, io.Discard)
			if elapsed := time.Since(start); elapsed > 30*time.Second {
				t.Fatalf("Run took %s, want it bounded by hostd.WaitDelay", elapsed)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("Run err = %v, want error: %v", err, tc.wantErr)
			}
			if !strings.Contains(out, "started") {
				t.Errorf("Run output = %q", out)
			}
		})
	}
}

func TestHostdHostCancelEndsTheRun(t *testing.T) {
	client, _ := startHostd(t)
	h := HostdHost{LocalHost: LocalHost{Logf: func(string, ...any) {}}, Client: client, Entry: "fakecli"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := h.Run(ctx, Command{Args: []string{"-c", "sleep 60"}}, nil)
	if err == nil || time.Since(start) > 20*time.Second {
		t.Errorf("Run = %v after %s, want a prompt failure", err, time.Since(start))
	}
}
