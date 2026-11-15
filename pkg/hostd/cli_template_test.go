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

package hostd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAllowlistTemplateValidateAndLookup(t *testing.T) {
	valid := AllowlistTemplate{
		Version: AllowlistTemplateVersion,
		Server:  "https://grain.example.com",
		CLIs: []CLITemplateEntry{
			{
				Name:       "agy",
				Aliases:    []string{"antigravity"},
				Candidates: []string{"/opt/agy/bin/agy", "agy", "~/.local/bin/agy"},
				Env:        []string{"HOME", "AGY_CLI_DISABLE_AUTO_UPDATE", "GEMINI_API_KEY"},
				PassEnv:    ProxyVars,
			},
			{
				Name:       "claude",
				Candidates: []string{"claude", "~/.local/bin/claude"},
				Env:        []string{"CLAUDE_CODE_OAUTH_TOKEN"},
				PassEnv:    ProxyVars,
			},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid template failed Validate: %v", err)
	}
	if got, ok := valid.Lookup("antigravity"); !ok || got.Name != "agy" {
		t.Fatalf("Lookup(antigravity) = %+v, %v; want agy", got, ok)
	}
	if got, ok := valid.Lookup("CLAUDE"); !ok || got.Name != "claude" {
		t.Fatalf("Lookup(CLAUDE) = %+v, %v; want claude", got, ok)
	}
	if _, ok := valid.Lookup("unknown"); ok {
		t.Fatal("Lookup(unknown) succeeded")
	}

	invalid := []struct {
		name string
		tmpl AllowlistTemplate
	}{
		{"empty CLIs", AllowlistTemplate{Version: 1}},
		{"bad name", AllowlistTemplate{CLIs: []CLITemplateEntry{{Name: "a/b", Candidates: []string{"agy"}}}}},
		{"duplicate name", AllowlistTemplate{CLIs: []CLITemplateEntry{
			{Name: "agy", Candidates: []string{"agy"}},
			{Name: "AGY", Candidates: []string{"agy"}},
		}}},
		{"traversal candidate", AllowlistTemplate{CLIs: []CLITemplateEntry{{Name: "agy", Candidates: []string{"/opt/../bin/agy"}}}}},
		{"relative slash candidate", AllowlistTemplate{CLIs: []CLITemplateEntry{{Name: "agy", Candidates: []string{"bin/agy"}}}}},
		{"bad tilde candidate", AllowlistTemplate{CLIs: []CLITemplateEntry{{Name: "agy", Candidates: []string{"~other/bin/agy"}}}}},
		{"bad env", AllowlistTemplate{CLIs: []CLITemplateEntry{{Name: "agy", Candidates: []string{"agy"}, Env: []string{"BAD=1"}}}}},
		{"bad pass_env", AllowlistTemplate{CLIs: []CLITemplateEntry{{Name: "agy", Candidates: []string{"agy"}, PassEnv: []string{"*"}}}}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.tmpl.Validate(); err == nil {
				t.Errorf("Validate(%+v) = nil, want error", tc.tmpl)
			}
		})
	}
}

func TestResolveAllFindsCandidatesInOrder(t *testing.T) {
	home := t.TempDir()
	pathDir := t.TempDir()
	optBin := filepath.Join(t.TempDir(), "agy")
	pathClaude := filepath.Join(pathDir, "claude")
	localCodex := filepath.Join(home, ".local", "bin", "codex")

	for _, f := range []string{optBin, pathClaude, localCodex} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tmpl := AllowlistTemplate{
		Version: 1,
		CLIs: []CLITemplateEntry{
			{Name: "agy", Candidates: []string{optBin, "agy", "~/.local/bin/agy"}},
			{Name: "claude", Candidates: []string{"claude", "~/.local/bin/claude"}},
			{Name: "codex", Candidates: []string{"codex", "~/.local/bin/codex"}},
			{Name: "missing", Candidates: []string{"missing-cli", "~/.local/bin/missing-cli"}},
		},
	}
	lookPath := func(name string) (string, error) {
		p := filepath.Join(pathDir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
		return "", os.ErrNotExist
	}
	isExec := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
	}

	got := tmpl.ResolveAll(home, lookPath, isExec)
	if len(got) != 3 {
		t.Fatalf("ResolveAll = %+v, want 3 resolved CLIs", got)
	}
	wantPaths := map[string]string{
		"agy":    optBin,
		"claude": pathClaude,
		"codex":  localCodex,
	}
	for _, r := range got {
		if r.Path != wantPaths[r.Entry.Name] {
			t.Errorf("%s resolved to %q, want %q", r.Entry.Name, r.Path, wantPaths[r.Entry.Name])
		}
	}
}

func TestRenderAndUpsertRespectTargetEnvOpenFeature(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stage := filepath.Join(home, ".grain", "hostd-stage")
	agyBin := filepath.Join(home, ".local", "bin", "agy")
	claudeBin := filepath.Join(home, ".local", "bin", "claude")

	agyEntry := CLITemplateEntry{
		Name:       "agy",
		Candidates: []string{"agy", "~/.local/bin/agy"},
		Env:        []string{"HOME", "AGY_CLI_DISABLE_AUTO_UPDATE", "GEMINI_API_KEY"},
		PassEnv:    []string{"HTTPS_PROXY", "NO_PROXY"},
	}
	claudeEntry := CLITemplateEntry{
		Name:       "claude",
		Candidates: []string{"claude", "~/.local/bin/claude"},
		Env:        []string{"CLAUDE_CODE_OAUTH_TOKEN", "MCP_TOOL_TIMEOUT"},
		PassEnv:    []string{"HTTPS_PROXY", "NO_PROXY"},
	}

	modern := ThisHello("v3.5.0")
	modernYAML, err := RenderAgentdAllowlist("grain-agentd", "https://grain.example.com", home, stage, []ResolvedCLI{{Entry: agyEntry, Path: agyBin}}, modern)
	if err != nil {
		t.Fatalf("RenderAgentdAllowlist(modern): %v", err)
	}
	if !strings.Contains(modernYAML, "path: ~/.local/bin/agy\n") ||
		!strings.Contains(modernYAML, "paths_under: [~/.grain/hostd-stage]\n") ||
		!strings.Contains(modernYAML, `env: [HOME, "*"]`+"\n") {
		t.Fatalf("modern allowlist missing ~/ or open env:\n%s", modernYAML)
	}

	// Upsert claude into modernYAML while preserving header comments and agy.
	updated, snippet, unchanged, err := UpsertAgentdAllowlistCLI([]byte(modernYAML), "grain-agentd", "https://grain.example.com", home, stage, ResolvedCLI{Entry: claudeEntry, Path: claudeBin}, modern)
	if err != nil {
		t.Fatalf("UpsertAgentdAllowlistCLI(modern): %v", err)
	}
	if unchanged {
		t.Fatal("expected unchanged=false when adding claude")
	}
	if !strings.Contains(snippet, "  claude:\n") || !strings.Contains(snippet, `env: ["*"]`) {
		t.Errorf("unexpected snippet:\n%s", snippet)
	}
	if !strings.Contains(string(updated), "# grain-agentd's allowlist:") ||
		!strings.Contains(string(updated), "  agy:\n") ||
		!strings.Contains(string(updated), "  claude:\n") {
		t.Fatalf("updated YAML lost header or entries:\n%s", updated)
	}
	// Repeating the same upsert reports unchanged=true.
	_, _, unchanged, err = UpsertAgentdAllowlistCLI(updated, "grain-agentd", "https://grain.example.com", home, stage, ResolvedCLI{Entry: claudeEntry, Path: claudeBin}, modern)
	if err != nil || !unchanged {
		t.Fatalf("second UpsertAgentdAllowlistCLI = unchanged %v, err %v; want true, nil", unchanged, err)
	}

	// Legacy target without FeatureEnvOpen gets closed env lists and absolute paths only.
	legacy := Hello{Protocol: 4}
	legacyYAML, err := RenderAgentdAllowlist("grain-agentd", "https://grain.example.com", home, stage, []ResolvedCLI{
		{Entry: agyEntry, Path: agyBin},
		{Entry: claudeEntry, Path: claudeBin},
	}, legacy)
	if err != nil {
		t.Fatalf("RenderAgentdAllowlist(legacy): %v", err)
	}
	if strings.Contains(legacyYAML, `"*"`) || strings.Contains(legacyYAML, "~/") {
		t.Fatalf("legacy allowlist emitted FeatureEnvOpen syntax:\n%s", legacyYAML)
	}
	if !strings.Contains(legacyYAML, "path: "+agyBin+"\n") ||
		!strings.Contains(legacyYAML, "env: [HOME, AGY_CLI_DISABLE_AUTO_UPDATE, GEMINI_API_KEY]\n") ||
		!strings.Contains(legacyYAML, "env: [CLAUDE_CODE_OAUTH_TOKEN, MCP_TOOL_TIMEOUT]\n") {
		t.Fatalf("legacy allowlist missing closed env or absolute path:\n%s", legacyYAML)
	}
}

func TestFetchAllowlistTemplateAndRegistries(t *testing.T) {
	t.Cleanup(RegisterCLICandidates("agy", "/opt/custom/bin/agy"))
	if got := RegisteredCLICandidates("agy"); !reflect.DeepEqual(got, []string{"/opt/custom/bin/agy"}) {
		t.Fatalf("RegisteredCLICandidates = %v", got)
	}
	custom := CLITemplateEntry{
		Name:       "custom-cli",
		Candidates: []string{"custom-cli", "~/.local/bin/custom-cli"},
		Env:        []string{"CUSTOM_API_KEY"},
	}
	t.Cleanup(RegisterCLITemplate(custom))
	if got := RegisteredCLITemplates(); len(got) != 1 || got[0].Name != "custom-cli" {
		t.Fatalf("RegisteredCLITemplates = %+v", got)
	}

	want := AllowlistTemplate{
		Version: AllowlistTemplateVersion,
		CLIs:    []CLITemplateEntry{custom},
	}
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != InstallTemplateEndpoint || r.URL.Query().Get(InstallTemplateQuery) != InstallTemplateAgentd {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer ts.Close()

	got, err := FetchAllowlistTemplate(context.Background(), FetchTemplateOptions{
		ServerURL: ts.URL,
		IDToken:   func(context.Context) (string, error) { return "id-tok-123", nil },
	})
	if err != nil {
		t.Fatalf("FetchAllowlistTemplate: %v", err)
	}
	if gotAuth != "Bearer id-tok-123" {
		t.Errorf("Authorization = %q, want Bearer id-tok-123", gotAuth)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
