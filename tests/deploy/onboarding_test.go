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

// Onboarding tests verify that first-time users can install, configure,
// connect to, and use Grain smoothly without cryptic errors or missing guidance.
package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupScriptRootPreflightCheck(t *testing.T) {
	// As root the preflight passes and setup.sh goes on to install a
	// whole deployment onto this machine -- system users, systemd units,
	// docker -- which on a CI sandbox took the VM down with it.
	if os.Geteuid() == 0 {
		t.Skip("running as root: setup.sh would really install, not refuse")
	}
	script := filepath.Join(repoRoot(t), "scripts", "setup.sh")
	cmd := exec.Command("bash", script)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected setup.sh to fail when executed as non-root")
	}
	want := "setup.sh: must run as root (it creates a system user, systemd units, and /usr/local/bin/grain) -- try sudo"
	if !strings.Contains(string(out), want) {
		t.Fatalf("setup.sh non-root failure did not contain expected guidance: got %q, want substring %q", string(out), want)
	}
}

func TestSetupScriptUsageAndHelpFlags(t *testing.T) {
	script := filepath.Join(repoRoot(t), "scripts", "setup.sh")
	for _, flag := range []string{"-h", "--help"} {
		t.Run(flag, func(t *testing.T) {
			cmd := exec.Command("bash", script, flag)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("setup.sh %s failed: %v\n%s", flag, err, out)
			}
			helpText := string(out)
			for _, requiredSection := range []string{
				"Usage: sudo ./setup.sh",
				"GRAIN_GITHUB_TOKEN",
				"GRAIN_GEMINI_API_KEY",
				"GRAIN_CLAUDE_CODE_OAUTH_TOKEN",
				"GRAIN_OPENAI_API_KEY",
				"GRAIN_TARGET_REPO",
				"GRAIN_UI_ADDR",
			} {
				if !strings.Contains(helpText, requiredSection) {
					t.Errorf("setup.sh %s missing required documentation for %q", flag, requiredSection)
				}
			}
		})
	}
}

func TestSetupScriptReadinessDiagnosticProse(t *testing.T) {
	code := setupText(t)
	readiness := body(t, code, "report_readiness() {")

	// Missing GitHub credentials must warn about inability to clone
	contains(t, readiness, "With no GitHub credential the git proxy cannot clone")

	// Missing agent credentials must direct users to Settings or specific environment variables
	contains(t, readiness, "With no agent credential set at all")
	contains(t, readiness, "Settings ->")
	contains(t, readiness, "GRAIN_GEMINI_API_KEY")

	// Missing agent CLIs must identify which framework is disabled
	contains(t, readiness, "no claude CLI")
	contains(t, readiness, "no agy CLI")
	contains(t, readiness, "no codex CLI")

	// Missing secrets key should note backup and recovery guidance
	contains(t, readiness, "no secrets key on this host yet: grain-daemon.service mints one")
	contains(t, readiness, "GRAIN_SECRETS_KEY")
}

func TestREADMEGettingStartedConsistency(t *testing.T) {
	readme := read(t, "README.md")

	// Ensure Getting Started documentation has accurate command examples and ports
	contains(t, readme, "./scripts/setup.sh")
	contains(t, readme, "./scripts/client.sh <grain-host>")
	contains(t, readme, "ssh -L 4242:localhost:4242 <grain-host>")
	contains(t, readme, "http://<grain-host>:4242/")
	// Not 8080 at both ends: the README said 8080:localhost:8080 while the
	// installer bound 127.0.0.1:80 (grain/task-3688).
	absent(t, readme, "ssh -L 8080:localhost:8080")
	// There is no system admin to make: whoever holds the access code is
	// the owner (task 4880).
	contains(t, readme, "### First sign-in\n")
	absent(t, readme, "system admin")
	contains(t, readme, "grain create")
	contains(t, readme, "grain list")
	absent(t, readme, "google/scripts")
}

func TestClientScriptUsageAndHelpFlags(t *testing.T) {
	script := filepath.Join(repoRoot(t), "scripts", "client.sh")
	for _, flag := range []string{"-h", "--help"} {
		t.Run(flag, func(t *testing.T) {
			cmd := exec.Command("bash", script, flag)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("client.sh %s failed: %v\n%s", flag, err, out)
			}
			helpText := string(out)
			for _, requiredSection := range []string{
				"Usage: client.sh",
				"-p, --local-port",
				"-r, --remote-port",
				"--no-browser",
				"Examples:",
			} {
				if !strings.Contains(helpText, requiredSection) {
					t.Errorf("client.sh %s missing documentation for %q", flag, requiredSection)
				}
			}
		})
	}
}
