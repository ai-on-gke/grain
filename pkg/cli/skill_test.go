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

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func setupSkillTestEnv(t *testing.T) (homeDir, projectDir, rawToken string, store *model.Store) {
	t.Helper()
	origLogOut := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(origLogOut) })

	ts, st := loginServer(t)

	homeDir = t.TempDir()
	projectDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)
	t.Chdir(projectDir)

	origVer := skillVersionFunc
	origExe := skillExecutable
	skillVersionFunc = func() string { return "v0.42.0-test" }
	skillExecutable = func() (string, error) { return "/usr/local/bin/grain", nil }
	t.Cleanup(func() {
		skillVersionFunc = origVer
		skillExecutable = origExe
	})

	ctx := context.Background()
	cfg, err := st.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.EnvironmentName = "Acme Engineering"
	if err := st.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTargetRepos(ctx, []string{"acme/widgets", "acme/gadgets"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutCustomField(ctx, model.CustomField{
		Name: "component", Type: model.CustomFieldEnum, Options: []string{"api", "cli", "ui"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutCustomField(ctx, model.CustomField{
		Name: "customer_ticket", Type: model.CustomFieldText,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTemplate(ctx, model.Template{
		ID:     "design-impl",
		Name:   "Design Implementation",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
		Title:  "Implement spec",
		Body:   "Implement committed spec",
	}); err != nil {
		t.Fatal(err)
	}

	_ = captureStdout(t, func() {
		if err := runCLI([]string{
			"login", ts.URL,
			"--name", "team",
			"--auth", "test-sso",
			"--issue-token",
			"--expires", "7d",
		}); err != nil {
			t.Fatalf("grain login --issue-token: %v", err)
		}
	})

	rawToken, err = newCredentialStore().load("file:team")
	if err != nil {
		t.Fatalf("loading credential file:team: %v", err)
	}
	rawToken = strings.TrimSpace(rawToken)
	if !strings.HasPrefix(rawToken, "gma_") {
		t.Fatalf("expected minted gma_ token in credential store, got %q", rawToken[:min(8, len(rawToken))])
	}
	return homeDir, projectDir, rawToken, st
}

// TestGrainSkillInstallTargetPathsIdempotencyAndContainsNoSecrets verifies
// external-cli.spec.md#XC-30.1, #XC-30.2, #XC-30.3, #XC-30.5, and #XC-31.3:
// target skill paths across Jetski, Claude Code, Codex CLI, Gemini CLI, and
// --dir (both user-level and --project), MCP config registration via --mcp,
// byte-for-byte idempotency, live deployment metadata population, and strict
// absence of any bearer token, access code, or secret.
func TestGrainSkillInstallTargetPathsIdempotencyAndContainsNoSecrets(t *testing.T) {
	homeDir, projectDir, rawToken, _ := setupSkillTestEnv(t)

	customDir := filepath.Join(t.TempDir(), "custom-skill")
	// Pre-populate an existing MCP server in Jetski's user config and Codex's
	// TOML config to verify --mcp preserves unrelated servers.
	jetskiMCPPath := filepath.Join(homeDir, ".gemini", "config", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(jetskiMCPPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jetskiMCPPath, []byte(`{"mcpServers":{"other":{"command":"other-mcp","args":["serve"]}}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	codexMCPPath := filepath.Join(homeDir, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(codexMCPPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexMCPPath, []byte("model = \"gpt-5\"\n\n[mcp_servers.other]\ncommand = \"other\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		flags     []string
		wantSkill string
		wantMCP   string
	}{
		{
			name:      "jetski-user",
			flags:     []string{"--jetski", "--mcp"},
			wantSkill: filepath.Join(homeDir, ".gemini", "jetski", "skills", "grain", "SKILL.md"),
			wantMCP:   jetskiMCPPath,
		},
		{
			name:      "jetski-project",
			flags:     []string{"--jetski", "--project", "--mcp"},
			wantSkill: filepath.Join(projectDir, ".agents", "skills", "grain", "SKILL.md"),
			wantMCP:   filepath.Join(projectDir, ".gemini", "config", "mcp_config.json"),
		},
		{
			name:      "claude-user",
			flags:     []string{"--claude", "--mcp"},
			wantSkill: filepath.Join(homeDir, ".claude", "skills", "grain", "SKILL.md"),
			wantMCP:   filepath.Join(homeDir, ".claude.json"),
		},
		{
			name:      "claude-project",
			flags:     []string{"--claude", "--project", "--mcp"},
			wantSkill: filepath.Join(projectDir, ".claude", "skills", "grain", "SKILL.md"),
			wantMCP:   filepath.Join(projectDir, ".mcp.json"),
		},
		{
			name:      "codex-user",
			flags:     []string{"--codex", "--mcp"},
			wantSkill: filepath.Join(homeDir, ".codex", "skills", "grain", "SKILL.md"),
			wantMCP:   codexMCPPath,
		},
		{
			name:      "codex-project",
			flags:     []string{"--codex", "--project", "--mcp"},
			wantSkill: filepath.Join(projectDir, ".agents", "skills", "grain", "SKILL.md"),
			wantMCP:   filepath.Join(projectDir, ".codex", "config.toml"),
		},
		{
			name:      "gemini-user",
			flags:     []string{"--gemini", "--mcp"},
			wantSkill: filepath.Join(homeDir, ".gemini", "skills", "grain", "SKILL.md"),
			wantMCP:   filepath.Join(homeDir, ".gemini", "settings.json"),
		},
		{
			name:      "gemini-project",
			flags:     []string{"--gemini", "--project", "--mcp"},
			wantSkill: filepath.Join(projectDir, ".gemini", "skills", "grain", "SKILL.md"),
			wantMCP:   filepath.Join(projectDir, ".gemini", "settings.json"),
		},
		{
			name:      "custom-dir",
			flags:     []string{"--dir", customDir, "--mcp"},
			wantSkill: filepath.Join(customDir, "SKILL.md"),
			wantMCP:   filepath.Join(customDir, "mcp_config.json"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"skill", "install", "--json"}, tc.flags...)
			out1 := captureStdout(t, func() {
				if err := runCLI(args); err != nil {
					t.Fatalf("first grain skill install (%v): %v", tc.flags, err)
				}
			})
			var res struct {
				Context          string        `json:"context"`
				Version          string        `json:"version"`
				DeploymentDigest string        `json:"deploymentDigest"`
				Targets          []skillTarget `json:"targets"`
			}
			if err := json.Unmarshal([]byte(out1), &res); err != nil {
				t.Fatalf("unmarshal install --json (%q): %v", out1, err)
			}
			if res.Context != "team" || res.Version != "v0.42.0-test" || len(res.DeploymentDigest) != 64 {
				t.Fatalf("unexpected install result metadata: %+v", res)
			}
			if len(res.Targets) != 1 || res.Targets[0].SkillPath != tc.wantSkill || res.Targets[0].MCPConfigPath != tc.wantMCP {
				t.Fatalf("targets = %+v, want skill=%s mcp=%s", res.Targets, tc.wantSkill, tc.wantMCP)
			}

			skillBytes1, err := os.ReadFile(tc.wantSkill)
			if err != nil {
				t.Fatalf("reading installed SKILL.md: %v", err)
			}
			mcpBytes1, err := os.ReadFile(tc.wantMCP)
			if err != nil {
				t.Fatalf("reading MCP config %s: %v", tc.wantMCP, err)
			}

			// Second run must be byte-for-byte idempotent.
			_ = captureStdout(t, func() {
				if err := runCLI(args); err != nil {
					t.Fatalf("second grain skill install (%v): %v", tc.flags, err)
				}
			})
			skillBytes2, err := os.ReadFile(tc.wantSkill)
			if err != nil {
				t.Fatal(err)
			}
			mcpBytes2, err := os.ReadFile(tc.wantMCP)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(skillBytes1, skillBytes2) {
				t.Errorf("SKILL.md changed across idempotent re-run")
			}
			if !bytes.Equal(mcpBytes1, mcpBytes2) {
				t.Errorf("MCP config %s changed across idempotent re-run:\n--- first ---\n%s\n--- second ---\n%s", tc.wantMCP, mcpBytes1, mcpBytes2)
			}

			content := string(skillBytes1)
			// Static guidance & frontmatter checks (#XC-30.2).
			for _, want := range []string{
				"---\nname: grain\ndescription: ",
				"export GRAIN_CLIENT=",
				"export GRAIN_CLIENT_SESSION=",
				"| `0` |",
				"| `5` |",
				"grain reply",
				"grain comment",
				"Never call `grain force-merge`",
				"[Task #<id>](/tasks/<id>)",
				"Acme Engineering",
				"`acme/widgets`",
				"`acme/gadgets`",
				"`design-impl` (Design Implementation)",
				"`component` (`enum`): `api`, `cli`, `ui`",
				"`customer_ticket` (`text`)",
				"`grain/external-dev`",
				"`tasks:write`",
				"`design/` workflow:** active",
			} {
				if !strings.Contains(content, want) {
					t.Errorf("SKILL.md missing expected substring %q\n--- SKILL.md ---\n%s", want, content)
				}
			}

			// Secret exclusion invariant (#XC-30.5).
			for _, secret := range []string{rawToken, "gma_", "sso:jane@example.com", "Authorization: Bearer"} {
				if strings.Contains(content, secret) {
					t.Fatalf("SKILL.md leaked secret marker %q", secret)
				}
			}

			// Verify MCP config registration (#XC-31.3).
			if strings.HasSuffix(tc.wantMCP, ".toml") {
				tomlText := string(mcpBytes1)
				if !strings.Contains(tomlText, "[mcp_servers.grain]") ||
					!strings.Contains(tomlText, `command = "/usr/local/bin/grain"`) ||
					!strings.Contains(tomlText, `args = ["mcp", "--context", "team"]`) {
					t.Errorf("Codex TOML MCP config missing grain server entry:\n%s", tomlText)
				}
				if tc.wantMCP == codexMCPPath && !strings.Contains(tomlText, "[mcp_servers.other]") {
					t.Errorf("Codex TOML MCP config overwrote existing server:\n%s", tomlText)
				}
			} else {
				var parsed map[string]any
				if err := json.Unmarshal(mcpBytes1, &parsed); err != nil {
					t.Fatalf("invalid JSON MCP config %s: %v", tc.wantMCP, err)
				}
				servers, _ := parsed["mcpServers"].(map[string]any)
				grainEntry, ok := servers["grain"].(map[string]any)
				if !ok {
					t.Fatalf("MCP config %s missing mcpServers.grain: %+v", tc.wantMCP, parsed)
				}
				if grainEntry["command"] != "/usr/local/bin/grain" {
					t.Errorf("command = %v, want /usr/local/bin/grain", grainEntry["command"])
				}
				gotArgs, _ := grainEntry["args"].([]any)
				if len(gotArgs) != 3 || gotArgs[0] != "mcp" || gotArgs[1] != "--context" || gotArgs[2] != "team" {
					t.Errorf("args = %v, want [mcp --context team]", gotArgs)
				}
				if tc.wantMCP == jetskiMCPPath && servers["other"] == nil {
					t.Errorf("Jetski JSON MCP config dropped pre-existing server 'other': %+v", servers)
				}
			}
		})
	}
}

// TestGrainSkillStatusDetectsVersionAndDeploymentDigestStaleness verifies
// external-cli.spec.md#XC-30.3 and #XC-30.4: `grain skill status` reports
// missing before installation, installed when current, stale with
// version_mismatch when the binary version advances, and stale with
// deployment_digest_mismatch when deployment metadata changes.
func TestGrainSkillStatusDetectsVersionAndDeploymentDigestStaleness(t *testing.T) {
	_, _, _, store := setupSkillTestEnv(t)

	readStatus := func() skillStatusReport {
		t.Helper()
		raw := captureStdout(t, func() {
			if err := runCLI([]string{"skill", "status", "--json"}); err != nil {
				t.Fatalf("grain skill status --json: %v", err)
			}
		})
		var rep skillStatusReport
		if err := json.Unmarshal([]byte(raw), &rep); err != nil {
			t.Fatalf("unmarshal status (%q): %v", raw, err)
		}
		return rep
	}

	// 1. Before any installation, status is "missing".
	rep := readStatus()
	if rep.Status != "missing" {
		t.Fatalf("before install, status = %q, want missing (%+v)", rep.Status, rep)
	}

	// 2. Install for Jetski; status becomes "installed".
	_ = captureStdout(t, func() {
		if err := runCLI([]string{"skill", "install", "--jetski", "--json"}); err != nil {
			t.Fatalf("grain skill install: %v", err)
		}
	})
	rep = readStatus()
	if rep.Status != "installed" || len(rep.Files) != 1 || rep.Files[0].Status != "installed" {
		t.Fatalf("after install, status = %+v, want installed", rep)
	}

	// 3. Advance CLI version; status becomes "stale" with version_mismatch.
	skillVersionFunc = func() string { return "v0.43.0-upgraded" }
	rep = readStatus()
	if rep.Status != "stale" || !slices.Contains(rep.Reasons, "version_mismatch") {
		t.Fatalf("after version bump, status = %+v, want stale (version_mismatch)", rep)
	}

	// 4. Re-install refreshes SKILL.md to v0.43.0-upgraded; status returns to "installed".
	_ = captureStdout(t, func() {
		if err := runCLI([]string{"skill", "install", "--json"}); err != nil {
			t.Fatalf("re-install: %v", err)
		}
	})
	rep = readStatus()
	if rep.Status != "installed" {
		t.Fatalf("after re-install, status = %+v, want installed", rep)
	}

	// 5. Mutate deployment metadata (declare a new custom field); status becomes
	// "stale" with deployment_digest_mismatch.
	ctx := context.Background()
	if err := store.PutCustomField(ctx, model.CustomField{
		Name: "release_train",
		Type: model.CustomFieldText,
	}); err != nil {
		t.Fatal(err)
	}

	rep = readStatus()
	if rep.Status != "stale" || !slices.Contains(rep.Reasons, "deployment_digest_mismatch") {
		t.Fatalf("after deployment config change, status = %+v, want stale (deployment_digest_mismatch)", rep)
	}
}

// TestStaleSkillStderrNoticeNeverCorruptsJSONStdout verifies
// external-cli.spec.md#XC-30.3 and #XC-20: when an installed skill file is
// stale (by version or cached deployment digest), ordinary CLI commands emit
// at most a single-line notice on stderr and NEVER write non-JSON bytes to
// stdout.
func TestStaleSkillStderrNoticeNeverCorruptsJSONStdout(t *testing.T) {
	_, _, _, store := setupSkillTestEnv(t)

	_ = captureStdout(t, func() {
		if err := runCLI([]string{"skill", "install", "--jetski", "--json"}); err != nil {
			t.Fatalf("grain skill install: %v", err)
		}
	})

	runTaskCLIWithCapture := func(args []string) (code int, stdout, stderr string) {
		t.Helper()
		var errBuf bytes.Buffer
		out := captureStdout(t, func() {
			code = runTaskCLI(args, &errBuf)
		})
		return code, out, errBuf.String()
	}

	// Fresh skill: zero staleness notice on stderr, clean JSON on stdout.
	code, stdout, stderr := runTaskCLIWithCapture([]string{"whoami", "--json"})
	if code != 0 {
		t.Fatalf("whoami --json exited %d: stderr=%s", code, stderr)
	}
	if strings.Contains(stderr, "stale") {
		t.Fatalf("unexpected staleness notice when skill is fresh: %q", stderr)
	}
	var who map[string]any
	if err := json.Unmarshal([]byte(stdout), &who); err != nil {
		t.Fatalf("whoami --json stdout invalid JSON (%q): %v", stdout, err)
	}

	// Case A: CLI binary version advances -> single-line notice on stderr,
	// valid JSON on stdout.
	skillVersionFunc = func() string { return "v0.99.0-next" }
	for _, argv := range [][]string{
		{"whoami", "--json"},
		{"list", "--json"},
	} {
		code, stdout, stderr = runTaskCLIWithCapture(argv)
		if code != 0 {
			t.Fatalf("%v exited %d: stderr=%s", argv, code, stderr)
		}
		var parsed any
		if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
			t.Fatalf("%v corrupted JSON stdout (%q): %v", argv, stdout, err)
		}
		if strings.Contains(stdout, "stale") || strings.Contains(stdout, "skill install") {
			t.Fatalf("%v leaked staleness text onto stdout: %q", argv, stdout)
		}
		wantNotice := "note: grain skill for context team is stale; run 'grain skill install'\n"
		if stderr != wantNotice {
			t.Errorf("%v stderr = %q, want exact single-line %q", argv, stderr, wantNotice)
		}
	}

	// Case B: Re-install so version matches, then update deployment config and
	// refresh cached digest via `grain skill status`; subsequent `grain list --json`
	// emits the single-line notice on stderr while keeping stdout valid JSON.
	_ = captureStdout(t, func() {
		if err := runCLI([]string{"skill", "install", "--json"}); err != nil {
			t.Fatal(err)
		}
	})
	ctx := context.Background()
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.EnvironmentName = "Acme Engineering Renamed"
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	_ = captureStdout(t, func() {
		if err := runCLI([]string{"skill", "status", "--json"}); err != nil {
			t.Fatal(err)
		}
	})

	code, stdout, stderr = runTaskCLIWithCapture([]string{"list", "--json"})
	if code != 0 {
		t.Fatalf("list --json exited %d: stderr=%s", code, stderr)
	}
	var listOut []any
	if err := json.Unmarshal([]byte(stdout), &listOut); err != nil {
		t.Fatalf("list --json stdout invalid JSON (%q): %v", stdout, err)
	}
	wantNotice := "note: grain skill for context team is stale; run 'grain skill install'\n"
	if stderr != wantNotice {
		t.Errorf("digest-stale list --json stderr = %q, want %q", stderr, wantNotice)
	}
}
