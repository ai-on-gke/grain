package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/safepath"
)

// A CI artifact reference arrives in a URL and becomes a file name on the
// daemon's disk; anything that could name a different directory is
// refused before a store sees it.
func TestCIArtifactRefsCannotLeaveTheStore(t *testing.T) {
	if err := (ci.ArtifactRef{Run: "abc123", Job: "go", Name: "report"}).Validate(); err != nil {
		t.Fatalf("an ordinary reference was refused: %v", err)
	}
	hostile := []string{"..", "../..", "../../etc/passwd", "a/b", `a\b`, "/etc", "a.b", "", "a\x00b", "%2e%2e"}
	for _, v := range hostile {
		for _, ref := range []ci.ArtifactRef{
			{Run: v, Job: "go", Name: "report"},
			{Run: "abc123", Job: v, Name: "report"},
			{Run: "abc123", Job: "go", Name: v},
		} {
			if err := ref.Validate(); err == nil {
				t.Errorf("artifact reference %+v accepted", ref)
			}
		}
	}
}

// A sandbox's file tools act on its workspace and nothing else: not by
// climbing out with .., not by naming an absolute path elsewhere, and
// not through a symlink the agent itself planted.
func TestSandboxFileToolsStayInTheWorkspace(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{workspace, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(workspace, "secret-link")); err != nil {
		t.Fatal(err)
	}

	tools := map[string]mcp.Tool{}
	for _, tool := range mcp.NewSandboxTools(workspace) {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"read_file", "write_file", "edit_file"} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("sandbox tools have no %s", name)
		}
	}
	ctx := context.Background()

	escapes := []string{
		"../outside/secret.txt",
		"sub/../../outside/secret.txt",
		secret,
		"out/secret.txt",
		"secret-link",
	}
	for _, p := range escapes {
		if res := tools["read_file"].Handler(ctx, map[string]any{"file_path": p}); !res.IsError || strings.Contains(res.Text, "host secret") {
			t.Errorf("read_file(%q) was allowed: %s", p, res.Text)
		}
		if res := tools["edit_file"].Handler(ctx, map[string]any{
			"file_path": p, "old_string": "host secret", "new_string": "tampered",
		}); !res.IsError {
			t.Errorf("edit_file(%q) was allowed: %s", p, res.Text)
		}
		if res := tools["write_file"].Handler(ctx, map[string]any{"file_path": p, "content": "tampered"}); !res.IsError {
			t.Errorf("write_file(%q) was allowed: %s", p, res.Text)
		}
	}
	for _, p := range []string{"../escaped.txt", "out/escaped.txt"} {
		tools["write_file"].Handler(ctx, map[string]any{"file_path": p, "content": "x"})
	}

	if got, err := os.ReadFile(secret); err != nil || string(got) != "host secret" {
		t.Fatalf("the file outside the workspace changed: %q, %v", got, err)
	}
	for _, p := range []string{filepath.Join(base, "escaped.txt"), filepath.Join(outside, "escaped.txt")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("a write escaped the workspace and created %s", p)
		}
	}

	// And the tools still work where they are meant to.
	if res := tools["write_file"].Handler(ctx, map[string]any{"file_path": "ok.txt", "content": "fine"}); res.IsError {
		t.Fatalf("write_file inside the workspace failed: %s", res.Text)
	}
	if res := tools["read_file"].Handler(ctx, map[string]any{"file_path": "ok.txt"}); res.IsError || !strings.Contains(res.Text, "fine") {
		t.Fatalf("read_file inside the workspace failed: %s", res.Text)
	}
}

// safepath.Within is the containment check the path-containment ratchet
// sends the rest of the tree to, so it has to refuse every escape the
// sandbox tools refuse: .., an absolute path elsewhere, a symlink out of
// the root, and a sibling whose name merely begins with the root's.
func TestSafepathWithinRefusesWhatTheWorkspaceRefuses(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{workspace, outside, base + "/workspace-other"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(workspace, "secret-link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"../outside/secret.txt",
		"sub/../../outside/secret.txt",
		secret,
		"out/secret.txt",
		"out/new.txt",
		"secret-link",
		"../workspace-other/x",
	} {
		if got, err := safepath.Within(workspace, p); err == nil {
			t.Errorf("safepath.Within(workspace, %q) = %q, want a refusal", p, got)
		}
	}
	if _, err := safepath.Within(workspace, "sub/new.txt"); err != nil {
		t.Errorf("a new file inside the workspace was refused: %v", err)
	}
}

// run_command's children see the environment a sandbox should have, not
// the daemon's: a credential in the process that serves the tools is not
// one the agent's commands inherit.
func TestRunCommandDoesNotInheritDaemonCredentials(t *testing.T) {
	sensitive := map[string]string{
		"GRAIN_TOKEN":             "grain-token-value",
		"GRAIN_DB_URL":            "postgres://user:pw@db/grain",
		"GCP_SERVICE_ACCOUNT_KEY": "gcp-key-value",
		"AWS_SECRET_ACCESS_KEY":   "aws-secret-value",
		"GITHUB_TOKEN":            "ghp_securitysuitevalue",
		"ANTHROPIC_API_KEY":       "sk-ant-securitysuite",
		"GEMINI_API_KEY":          "gemini-securitysuite",
	}
	for k, v := range sensitive {
		t.Setenv(k, v)
	}
	workspace := t.TempDir()
	var run mcp.Tool
	for _, tool := range mcp.NewSandboxTools(workspace) {
		if tool.Name == "run_command" {
			run = tool
		}
	}
	if run.Handler == nil {
		t.Fatal("sandbox tools have no run_command")
	}
	res := run.Handler(context.Background(), map[string]any{"command": "env"})
	if res.IsError {
		t.Fatalf("run_command env failed: %s", res.Text)
	}
	for k, v := range sensitive {
		if strings.Contains(res.Text, v) {
			t.Errorf("run_command's environment carries %s from the daemon", k)
		}
	}
}
