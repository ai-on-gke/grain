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

package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

const testRunCredential = "grr_dGFzaw.cnVu.c2VjcmV0LW1hYw"

// A sandbox that hides nothing gets the credential in the mcpserver's
// environment, as before: no file is written and the hide list is left
// alone.
func TestHideRunCredentialLeavesAnUnconfinedSandboxAlone(t *testing.T) {
	file, hide, cleanup, err := hideRunCredential(testRunCredential, nil)
	defer cleanup()
	if err != nil || file != "" || len(hide) != 0 {
		t.Fatalf("hideRunCredential with nothing to hide = %q, %q, %v; want no file", file, hide, err)
	}
}

func runCommandOf(t *testing.T, tools []mcp.Tool) mcp.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == "run_command" {
			return tool
		}
	}
	t.Fatal("no run_command tool")
	return mcp.Tool{}
}

// On a confined host sandbox the credential is in a file inside a
// directory run_command hides, the mcpserver is told only its path, and
// a command run in the sandbox cannot read it -- by its path, or by
// searching the temp directory the claude MCP config and the sandbox env
// file are written to (task 4048's finding).
func TestARunCredentialFileIsHiddenFromAConfinedRunCommand(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("bwrap not installed: %v", err)
	}
	if err := mcp.CheckHostConfinement(context.Background()); err != nil {
		t.Fatalf("CheckHostConfinement: %v", err)
	}
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "sandboxes", "run-1")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	file, hide, cleanup, err := hideRunCredential(testRunCredential, []string{dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if file == "" || !slices.Contains(hide, filepath.Dir(file)) || !slices.Contains(hide, dataDir) {
		t.Fatalf("hideRunCredential = %q, %q; want a file in a directory added to the hide list", file, hide)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("credential file: %v %v, want mode 0600", fi, err)
	}

	// The mcpserver is told the path, never the credential.
	args := agent.SandboxEnvArgs(agent.RunConfig{SandboxRoot: root, SandboxHide: hide, RunCredentialFile: file})
	if !slices.Contains(args, "-run-credential-file") || !slices.Contains(args, file) {
		t.Fatalf("mcpserver args %q do not name the credential file", args)
	}
	if strings.Contains(strings.Join(args, " "), testRunCredential) {
		t.Fatalf("mcpserver args carry the credential: %q", args)
	}

	run := runCommandOf(t, mcp.NewHostSandboxTools(root, hide))
	for name, command := range map[string]string{
		"by path":   "cat " + file + " 2>&1; true",
		"by search": "grep -rs " + testRunCredential[:12] + " " + os.TempDir() + " 2>/dev/null; true",
	} {
		if res := run.Handler(context.Background(), map[string]any{"command": command}); strings.Contains(res.Text, testRunCredential) {
			t.Errorf("%s: a confined run_command read the run credential: %s", name, res.Text)
		}
	}

	// The control: with only the data directory hidden, the same command
	// reads it -- the leak the hide list closes.
	unhidden := runCommandOf(t, mcp.NewHostSandboxTools(root, []string{dataDir}))
	if res := unhidden.Handler(context.Background(), map[string]any{"command": "cat " + file}); !strings.Contains(res.Text, testRunCredential) {
		t.Fatalf("control: an unhidden credential file was not readable either (%s), so this test proves nothing", res.Text)
	}

	cleanup()
	if _, err := os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
		t.Errorf("the credential's directory outlived the run: %v", err)
	}
}
