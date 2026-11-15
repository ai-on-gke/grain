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

package agentd

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/ai-on-gke/grain/"

// forbiddenDeps are the grain packages grain-agentd must never link: the
// daemon's UI and server, the data model, the stores, and the frameworks
// and MCP server whose hooks it answers through leaf packages instead
// (pkg/agent/antigravity/hook, pkg/agent/claude/stophook). Each is a
// prefix: a package under one is forbidden too.
var forbiddenDeps = []string{
	"pkg/ui",
	"pkg/daemon",
	"pkg/model",
	"pkg/staterepo",
	"pkg/secretsrepo",
	"pkg/mcp",
	"pkg/orchestrator",
	"pkg/capability",
}

// forbiddenExact are packages forbidden themselves, while leaf packages
// under them are allowed: the frameworks.
var forbiddenExact = []string{
	"pkg/agent",
	"pkg/agent/antigravity",
	"pkg/agent/claude",
	"pkg/agent/codex",
}

// TestDependencyFence fails when grain-agentd starts to link anything of
// the daemon's. It is what keeps the binary small, and keeps a build that
// vendors pkg/agentd from vendoring half of grain with it. A new
// dependency that trips it belongs behind a seam or in a leaf package.
func TestDependencyFence(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on $PATH")
	}
	for _, pkg := range []string{module + "cmd/grain-agentd", module + "pkg/agentd"} {
		cmd := exec.Command("go", "list", "-deps", "-test=false", pkg)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v\n%s", pkg, err, stderr.String())
		}
		for _, dep := range strings.Fields(string(out)) {
			rel, ok := strings.CutPrefix(dep, module)
			if !ok {
				continue
			}
			if why := forbidden(rel); why != "" {
				t.Errorf("%s depends on %s (%s)", pkg, dep, why)
			}
		}
	}
}

func forbidden(rel string) string {
	for _, f := range forbiddenExact {
		if rel == f {
			return "a framework package; import its leaf hook package instead"
		}
	}
	for _, f := range forbiddenDeps {
		if rel == f || strings.HasPrefix(rel, f+"/") {
			return "under " + f
		}
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.Contains(part, "store") {
			return "a store package"
		}
	}
	return ""
}

func TestForbidden(t *testing.T) {
	for rel, want := range map[string]bool{
		"pkg/ui":                         true,
		"pkg/ui/static":                  true,
		"pkg/daemon":                     true,
		"pkg/model":                      true,
		"pkg/agent/sessionstore":         true,
		"pkg/agent":                      true,
		"pkg/agent/antigravity":          true,
		"pkg/agent/antigravity/hook":     false,
		"pkg/agent/claude/stophook":      false,
		"pkg/agent/hookpolicy":           false,
		"pkg/agent/stoprule":             false,
		"pkg/hostd":                      false,
		"pkg/uiutil-not-under-ui-really": false,
	} {
		if got := forbidden(rel) != ""; got != want {
			t.Errorf("forbidden(%q) = %v, want %v", rel, got, want)
		}
	}
}
