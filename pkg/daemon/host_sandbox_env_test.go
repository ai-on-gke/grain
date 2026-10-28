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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestResolveHostSandboxEnv(t *testing.T) {
	t.Parallel()
	lookup := func(name string) (string, bool) {
		v, ok := map[string]string{"SSH_AUTH_SOCK": "/run/agent.sock", "HTTPS_PROXY": "http://proxy:3128"}[name]
		return v, ok
	}
	env, unset, err := ResolveHostSandboxEnv(
		[]string{"SSH_AUTH_SOCK", "HTTPS_PROXY", "CLOUDSDK_CONFIG=/home/me/.config/gcloud", "NO_PROXY"}, lookup)
	if err != nil {
		t.Fatalf("ResolveHostSandboxEnv: %v", err)
	}
	wantEnv := []string{"SSH_AUTH_SOCK=/run/agent.sock", "HTTPS_PROXY=http://proxy:3128", "CLOUDSDK_CONFIG=/home/me/.config/gcloud"}
	if !slices.Equal(env, wantEnv) {
		t.Errorf("env = %q, want %q", env, wantEnv)
	}
	if !slices.Equal(unset, []string{"NO_PROXY"}) {
		t.Errorf("unset = %q, want [NO_PROXY]", unset)
	}

	for _, bad := range [][]string{
		{"HOME"},
		{"HOME=/home/me"},
		{"NOT-A-NAME"},
		{"SSH_AUTH_SOCK", "SSH_AUTH_SOCK=/elsewhere"},
		// A password in a literal entry would sit on the daemon's own
		// argv (GSA1-12); passed by name it does not.
		{"HTTPS_PROXY=http://alice:s3cret@proxy:3128"},
		{"ALL_PROXY=socks5://alice:@proxy:1080"},
	} {
		if _, _, err := ResolveHostSandboxEnv(bad, lookup); err == nil {
			t.Errorf("ResolveHostSandboxEnv(%q) = nil error, want a refusal", bad)
		}
	}
}

// The same proxy URL with a password in it is fine passed by name: the
// value then comes from the daemon's environment, which only its own user
// can read, and never from its command line.
func TestResolveHostSandboxEnvTakesAPasswordByName(t *testing.T) {
	t.Parallel()
	lookup := func(name string) (string, bool) { return "http://alice:s3cret@proxy:3128", name == "HTTPS_PROXY" }
	env, _, err := ResolveHostSandboxEnv([]string{"HTTPS_PROXY", "NO_PROXY=http://proxy:3128,localhost"}, lookup)
	if err != nil {
		t.Fatalf("ResolveHostSandboxEnv: %v", err)
	}
	want := []string{"HTTPS_PROXY=http://alice:s3cret@proxy:3128", "NO_PROXY=http://proxy:3128,localhost"}
	if !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
	_, _, err = ResolveHostSandboxEnv([]string{"HTTPS_PROXY=http://alice:s3cret@proxy:3128"}, lookup)
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("literal entry with a password = %v, want a refusal that does not repeat the password", err)
	}
}

func TestCheckHostSandboxEnvNeedsHostSandboxes(t *testing.T) {
	t.Parallel()
	cfg := workstation()
	cfg.HostSandboxEnv = []string{"SSH_AUTH_SOCK"}
	if err := CheckSandboxBackend(cfg, false); err != nil {
		t.Errorf("-host-sandboxes -host-sandbox-env SSH_AUTH_SOCK = %v, want it to start", err)
	}
	cfg.HostSandboxEnv = []string{"HOME=/home/me"}
	if err := CheckSandboxBackend(cfg, false); err == nil || !strings.Contains(err.Error(), "HOME") {
		t.Errorf("-host-sandbox-env HOME=... = %v, want a refusal naming HOME", err)
	}
	cfg = Config{ContainerSandboxes: true, HostSandboxEnv: []string{"SSH_AUTH_SOCK"}}
	if err := CheckSandboxBackend(cfg, false); err == nil || !strings.Contains(err.Error(), "only for -host-sandboxes") {
		t.Errorf("-host-sandbox-env without -host-sandboxes = %v, want a refusal", err)
	}
}

func TestMCPServerSandboxEnvFlags(t *testing.T) {
	root := t.TempDir()
	envFile := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(envFile, []byte("A=b\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	homeFile := filepath.Join(t.TempDir(), "home-env")
	if err := os.WriteFile(homeFile, []byte("HOME=/home/me\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"not a sandbox root", []string{"-container", "c", "-sandbox-env", "A=b"}, "only for -sandbox-root"},
		{"HOME", []string{"-sandbox-root", root, "-sandbox-env", "HOME=/home/me"}, "cannot set HOME"},
		{"bare name", []string{"-sandbox-root", root, "-sandbox-env", "SSH_AUTH_SOCK"}, "not NAME=VALUE"},
		{"file without a sandbox root", []string{"-container", "c", "-sandbox-env-file", envFile}, "only for -sandbox-root"},
		{"missing file", []string{"-sandbox-root", root, "-sandbox-env-file", filepath.Join(root, "gone")}, "-sandbox-env-file"},
		{"HOME in the file", []string{"-sandbox-root", root, "-sandbox-env-file", homeFile}, "cannot set HOME"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := ServeMCPServerPinned(strings.NewReader(""), &out, tc.args, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("mcpserver %q = %v, want an error containing %q", tc.args, err, tc.want)
			}
		})
	}
}

// A host sandbox built WithEnv hands those entries to the commands this
// process runs in it (checkout, setup) through Tools -- but only for a
// run acquired as the operator's own (Shape.OperatorEnv). Any other run
// gets none of the operator's ambient credentials (GSA1-05).
func TestHostSandboxesWithEnvReachesRunCommand(t *testing.T) {
	ctx := context.Background()
	sandboxes := orchestrator.NewHostSandboxes(t.TempDir()).WithEnv([]string{"SSH_AUTH_SOCK=/run/agent.sock"})
	for name, tc := range map[string]struct {
		shape orchestrator.Shape
		want  string
	}{
		// Set, and redacted from what run_command returns (GSA1-12).
		"the operator's own": {orchestrator.Shape{OperatorEnv: true}, "sock=[redacted: sandbox env SSH_AUTH_SOCK]"},
		"anybody else's":     {orchestrator.Shape{}, "sock=\n"},
	} {
		sb, err := sandboxes.Acquire(ctx, "run-"+strings.ReplaceAll(name, " ", "-"), tc.shape)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		t.Cleanup(func() { _ = sb.Release(ctx) })
		if env := sb.(interface{ SandboxEnv() []string }).SandboxEnv(); tc.shape.OperatorEnv != (len(env) > 0) {
			t.Errorf("%s run: SandboxEnv = %q", name, env)
		}
		tools, err := sb.Tools(ctx)
		if err != nil {
			t.Fatalf("Tools: %v", err)
		}
		found := false
		for _, tool := range tools {
			if tool.Name != "run_command" {
				continue
			}
			found = true
			res := tool.Handler(ctx, map[string]any{"command": `echo "sock=$SSH_AUTH_SOCK"`})
			if !strings.Contains(res.Text, tc.want) {
				t.Errorf("run_command in %s WithEnv host sandbox = %q, want it to contain %q", name, res.Text, tc.want)
			}
		}
		if !found {
			t.Fatal("host sandbox has no run_command tool")
		}
	}
}

// A host sandbox hands its operator's entries to the agent's mcpserver in
// a 0600 file outside the run's own directory, never on that process's
// argv (GSA1-12), and removes the file with the run. A run that is not
// the operator's own gets no file at all.
func TestHostSandboxEnvFileIsPrivateAndGoesWithTheRun(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	entries := []string{"HTTPS_PROXY=http://alice:s3cret@proxy:3128", "MULTI=line one\nline two"}
	sandboxes := orchestrator.NewHostSandboxes(base).WithEnv(entries)
	type envFiler interface{ SandboxEnvFile() (string, error) }

	other, err := sandboxes.Acquire(ctx, "run-other", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = other.Release(ctx) })
	if path, err := other.(envFiler).SandboxEnvFile(); err != nil || path != "" {
		t.Errorf("SandboxEnvFile for a run that is not the operator's = %q, %v; want none", path, err)
	}

	sb, err := sandboxes.Acquire(ctx, "run-own", orchestrator.Shape{OperatorEnv: true})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	path, err := sb.(envFiler).SandboxEnvFile()
	if err != nil || path == "" {
		t.Fatalf("SandboxEnvFile = %q, %v", path, err)
	}
	if again, _ := sb.(envFiler).SandboxEnvFile(); again != path {
		t.Errorf("SandboxEnvFile a second time = %q, want the same file %q", again, path)
	}
	if strings.HasPrefix(path, base) {
		t.Errorf("env file %q is under the sandbox dir %q, where the run or ReapOrphans reaches it", path, base)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat env file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("env file mode = %o, want 600", mode)
	}

	// The forked mcpserver reads it back whole, newline and all.
	got, err := mcp.ReadSandboxEnvFile(path)
	if err != nil || !slices.Equal(got, entries) {
		t.Errorf("ReadSandboxEnvFile = %q, %v; want %q", got, err, entries)
	}

	if err := sb.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("env file still there after Release: %v", err)
	}
}
