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

package orchestrator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A host sandbox's root is the HOME its commands run with, so a relative
// -sandbox-dir used to give every command a relative HOME: git, run from
// the checkout, looked for ~/.git-credentials under it and every clone
// failed with "could not read Username". Found running a daemon with
// `-sandbox-dir d/sb` (docs/test-runs/2026-10-15-corp-research-capability.md).
func TestHostSandboxesRootIsAbsoluteForARelativeBaseDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("sb", 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sb, err := orchestrator.NewHostSandboxes("sb").Acquire(ctx, "1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sb.Release(ctx) })
	rooted, ok := sb.(interface{ Root() (string, error) })
	if !ok {
		t.Fatalf("%T has no Root", sb)
	}
	root, err := rooted.Root()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("Root() = %q, want an absolute path", root)
	}
	if err := sb.ConfigureGitCredentials(ctx, "http://127.0.0.1:9/placeholder/placeholder.git", "tok", mcp.GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	tools, err := sb.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var run func(context.Context, map[string]any) mcp.Result
	for _, tool := range tools {
		if tool.Name == "run_command" {
			run = tool.Handler
		}
	}
	if run == nil {
		t.Fatal("no run_command tool")
	}
	// From inside a subdirectory, as the clone script runs git: the
	// credential has to come from the sandbox's own ~/.git-credentials.
	res := run(ctx, map[string]any{"command": "cd work && printf 'protocol=http\\nhost=127.0.0.1:9\\n\\n' | GIT_TERMINAL_PROMPT=0 git credential fill"})
	if res.IsError || !strings.Contains(res.Text, "password=tok") {
		t.Fatalf("git credential fill from the checkout = %q (error %v), want the sandbox's own credential", res.Text, res.IsError)
	}
}
