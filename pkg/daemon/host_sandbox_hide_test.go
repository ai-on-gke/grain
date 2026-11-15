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

package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const hideTestAccessCode = "WXYZ-2345-ABCD-EFGH-IJKL-MNOP-QRST-UV67"

// stubHostConfinement makes checkHostConfinement answer err for the
// rest of t.
func stubHostConfinement(t *testing.T, err error) {
	t.Helper()
	prev := checkHostConfinement
	checkHostConfinement = func(context.Context) error { return err }
	t.Cleanup(func() { checkHostConfinement = prev })
}

// A daemon's host sandboxes hide its data directory, which holds the
// access code, and a host that cannot hide it is refused at start-up
// rather than left with the code in every agent's reach
// (grain/task-4366).
func TestHostSandboxHiddenPaths(t *testing.T) {
	dataDir := t.TempDir()
	noBwrap := errors.New("bwrap: not installed")
	cfg := Config{DataDir: dataDir}
	// The API socket admits the daemon's own account, which every host
	// run executes as, as -deployment-owner: moved out of the data
	// directory it is hidden on its own.
	socket := filepath.Join(t.TempDir(), "grain.sock")
	for name, tc := range map[string]struct {
		cfg     Config
		check   error
		want    []string
		wantErr string
	}{
		"confinable":                            {cfg, nil, []string{dataDir}, ""},
		"not confinable":                        {cfg, noBwrap, nil, "-host-sandboxes needs"},
		"no data directory":                     {Config{}, noBwrap, nil, ""},
		"api socket outside the data directory": {Config{DataDir: dataDir, APISocket: socket}, nil, []string{dataDir, socket}, ""},
		"api socket inside the data directory": {
			Config{DataDir: dataDir, APISocket: filepath.Join(dataDir, "api", "grain.sock")}, nil, []string{dataDir}, "",
		},
		"api socket off":                {Config{DataDir: dataDir, APISocket: "none"}, nil, []string{dataDir}, ""},
		"api socket, no data directory": {Config{APISocket: socket}, noBwrap, nil, "-host-sandboxes needs"},
	} {
		t.Run(name, func(t *testing.T) {
			stubHostConfinement(t, tc.check)
			got, err := hostSandboxHiddenPaths(context.Background(), tc.cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "bubblewrap") {
					t.Fatalf("hostSandboxHiddenPaths = %q, %v; want an error containing %q naming bubblewrap", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("hostSandboxHiddenPaths = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestMCPServerSandboxHideFlags(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"not a sandbox root", []string{"-container", "c", "-sandbox-hide", "/var/lib/grain/data"}, "only for -sandbox-root"},
		{"relative", []string{"-sandbox-root", root, "-sandbox-hide", "var/lib/grain"}, "not an absolute path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := ServeMCPServer(strings.NewReader(""), &out, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("mcpserver %q = %v, want an error containing %q", tc.args, err, tc.want)
			}
		})
	}
}

// The two ways a host sandbox's commands run -- the daemon's own
// in-process Tools (checkout, the setup script) and the agent's forked
// `grain mcpserver -sandbox-root ... -sandbox-hide ...` -- both keep a
// data directory WithHidden names out of a run_command's reach.
func TestHostSandboxRunCommandCannotReadTheDataDirectory(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("bwrap not installed: %v", err)
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	token := filepath.Join(dataDir, "local-api-token")
	if err := os.WriteFile(token, []byte(hideTestAccessCode+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(dataDir, "sandbox", "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	sb, err := orchestrator.NewHostSandboxes(runs).WithHidden([]string{dataDir}).Acquire(ctx, "run-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = sb.Release(ctx) })
	if got := sb.(interface{ HiddenPaths() []string }).HiddenPaths(); !slices.Equal(got, []string{dataDir}) {
		t.Errorf("HiddenPaths = %q, want %q", got, []string{dataDir})
	}
	root, err := sb.(interface{ Root() (string, error) }).Root()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("in-process", func(t *testing.T) {
		tools, err := sb.Tools(ctx)
		if err != nil {
			t.Fatalf("Tools: %v", err)
		}
		for _, tool := range tools {
			if tool.Name != "run_command" {
				continue
			}
			res := tool.Handler(ctx, map[string]any{"command": "cat " + token + "; echo done"})
			if strings.Contains(res.Text, hideTestAccessCode) || !strings.Contains(res.Text, "done") {
				t.Fatalf("in-process run_command = %q, want it to run without reading the code", res.Text)
			}
			return
		}
		t.Fatal("host sandbox has no run_command tool")
	})

	t.Run("mcpserver", func(t *testing.T) {
		in := strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_command","arguments":{"command":"cat ` +
				token + `; echo done"}}}` + "\n")
		var out bytes.Buffer
		// io.EOF is the server reaching the end of its input.
		if err := ServeMCPServer(in, &out, []string{"-sandbox-root", root, "-sandbox-hide", dataDir}); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("mcpserver: %v", err)
		}
		if strings.Contains(out.String(), hideTestAccessCode) || !strings.Contains(out.String(), "done") {
			t.Fatalf("mcpserver run_command answered %s, want it to run without reading the code", out.String())
		}
	})
}

// An API socket outside the data directory (-api-socket) is hidden from
// a host sandbox's commands as well: it admits the daemon's own account,
// which they run as, as -deployment-owner.
func TestHostSandboxRunCommandCannotReachAnAPISocketOutsideTheDataDirectory(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("bwrap not installed: %v", err)
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	stubHostConfinement(t, nil)
	sock := filepath.Join(t.TempDir(), "api", "grain.sock")
	ln, err := ui.ListenAPISocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	hide, err := hostSandboxHiddenPaths(ctx, Config{DataDir: dataDir, APISocket: sock})
	if err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(dataDir, "sandbox", "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	sb, err := orchestrator.NewHostSandboxes(runs).WithHidden(hide).Acquire(ctx, "run-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = sb.Release(ctx) })
	tools, err := sb.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	for _, tool := range tools {
		if tool.Name != "run_command" {
			continue
		}
		res := tool.Handler(ctx, map[string]any{"command": "if [ -S " + sock + " ]; then echo REACHABLE; fi; echo done"})
		if strings.Contains(res.Text, "REACHABLE") || !strings.Contains(res.Text, "done") {
			t.Fatalf("run_command = %q, want it to run with the API socket hidden", res.Text)
		}
		return
	}
	t.Fatal("host sandbox has no run_command tool")
}
