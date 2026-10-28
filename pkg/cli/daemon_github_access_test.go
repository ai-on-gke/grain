package cli

// The joint between the daemon's framework construction and the two
// agent packages' own MCP wiring: a run driven by either framework has
// to fork an mcpserver that was told which repo and branch it may read
// CI for, or pull_request_status answers every run with "this run has no
// GitHub repository configured for it" -- a sentence that reads like a
// task with no target rather than like a deployment that lost the
// wiring, which is exactly what makes losing it invisible.
//
// Both sides of the seam are covered elsewhere: pkg/agent/claude and
// pkg/agent/antigravity each prove WithGitHubAccess plus a RunConfig
// carrying a repo and branch produce this argv, and mcpserver_test.go
// proves that argv produces a correctly scoped reader. What is asserted
// here is only the joint -- that buildClaudeFramework and
// buildAntigravityFramework pass the option at all.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// agyMCPConfigRelPath is where agent/antigravity writes the MCP config
// file inside the private HOME it hands one run (that package's own
// mcpConfigRelPath, which is unexported). Duplicated rather than shared
// because it is agy's on-disk layout, not grain's API: if a future agy
// moves the file, the stub below stops finding it and this test fails
// loudly rather than quietly asserting nothing.
const agyMCPConfigRelPath = ".gemini/config/mcp_config.json"

// stubAgentCLI writes an executable shell script at path that saves the
// MCP configuration its invocation was handed into the file named by
// $GRAIN_TEST_MCP_CONFIG, prints a minimal successful transcript, and
// exits 0. Saving it is the whole trick: both frameworks delete that
// configuration as Run returns (a temp file for claude, a whole private
// HOME for agy), so a test that looked afterwards would find nothing.
//
// capture is the framework-specific half -- where that one CLI is told
// about its MCP server -- and transcript the stream-json each package
// parses back into an agent.Result.
func stubAgentCLI(t *testing.T, path, capture, transcript string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		// The prompt arrives on stdin and nothing here reads it; drain
		// it rather than exiting in the writer's face.
		"cat >/dev/null\n" +
		capture + "\n" +
		"cat <<'EOF'\n" + transcript + "\nEOF\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub CLI: %v", err)
	}
}

// forkedMCPServerArgs reads back what one of the stubs above captured:
// the argv the agent CLI would have forked "grain mcpserver" with.
func forkedMCPServerArgs(t *testing.T, capturePath string) []string {
	t.Helper()
	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("the stub CLI captured no MCP configuration: %v", err)
	}
	var parsed struct {
		MCPServers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("the MCP configuration was not JSON: %v (%s)", err, data)
	}
	server, ok := parsed.MCPServers[mcp.ToolNamespace]
	if !ok {
		t.Fatalf("MCP configuration named no %s server: %s", mcp.ToolNamespace, data)
	}
	return server.Args
}

// A run dispatched by this daemon must be able to see its own CI. That
// is one option per framework in daemon.go (WithGitHubAccess), and
// deleting either line breaks nothing else: the tool stays registered
// and keeps answering, just with nothing to answer about.
//
// The framework is built the way a dispatch builds it -- through
// agentFrameworks, off a config -- and then actually Run, with a stub
// binary standing in for the agent CLI, so what is asserted is the
// configuration a real claude/agy subprocess would have been handed
// rather than any internal this test could reach into.
func TestAgentFrameworksTellTheForkedMCPServerAboutGitHub(t *testing.T) {
	// claude is pointed at its MCP servers by --mcp-config <file>, which
	// agent/claude removes as Run returns.
	const claudeCapture = `while [ "$#" -gt 0 ]; do
	if [ "$1" = "--mcp-config" ]; then cp "$2" "$GRAIN_TEST_MCP_CONFIG"; fi
	shift
done`
	// agy has no --mcp-config: it reads an mcp_config.json out of its
	// HOME, and agent/antigravity gives each run a private one that it
	// deletes as Run returns.
	const agyCapture = `cp "$HOME/` + agyMCPConfigRelPath + `" "$GRAIN_TEST_MCP_CONFIG"`

	const claudeTranscript = `{"type":"result","result":"ok"}`
	const agyTranscript = `{"event":"init","init":{"cwd":"/w","tools":[],"permission_mode":"bypass"}}` + "\n" +
		`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`

	for _, tc := range []struct {
		framework  string
		binary     string
		credential string
		capture    string
		transcript string
	}{
		{
			framework:  model.AgentFrameworkClaude,
			binary:     "claude",
			credential: "sk-ant-oat01-fake",
			capture:    claudeCapture,
			transcript: claudeTranscript,
		},
		{
			framework:  model.AgentFrameworkAntigravity,
			binary:     "agy",
			credential: "AIza-fake",
			capture:    agyCapture,
			transcript: agyTranscript,
		},
	} {
		t.Run(tc.framework, func(t *testing.T) {
			binPath := filepath.Join(t.TempDir(), tc.binary)
			stubAgentCLI(t, binPath, tc.capture, tc.transcript)
			capturePath := filepath.Join(t.TempDir(), "mcp-config.json")
			t.Setenv("GRAIN_TEST_MCP_CONFIG", capturePath)

			secretStore := testSecrets(t)
			setDefaultTeamBackendKey(t, secretStore, tc.framework, tc.credential)
			// -claude-path/-agy-path, so this needs neither CLI on
			// $PATH; the other three fields are what a deployment's own
			// flags set, and are exactly what WithGitHubAccess carries.
			dataDir := t.TempDir()
			cfg := daemon.Config{
				ClaudePath:         binPath,
				AgyPath:            binPath,
				DataDir:            dataDir,
				GithubHost:         "github.example",
				GithubInsecureHTTP: true,
			}

			framework, err := daemon.AgentFrameworks(withDefaultTeamBackends(cfg), testStore(t), secretStore)(context.Background(), orchestrator.AgentChoice{Framework: tc.framework})
			if err != nil {
				t.Fatalf("building the %s framework: %v", tc.framework, err)
			}
			// Repo and Branch are the per-run half, set by
			// orchestrator.RunDispatch off the task; both halves have to
			// arrive for either to be passed, so a Framework missing its
			// half shows up as no flags at all.
			if _, err := framework.Run(context.Background(), agent.RunConfig{
				Prompt: "go", SandboxRoot: t.TempDir(),
				Repo: "acme/widgets", Branch: "grain/task-9",
			}); err != nil {
				t.Fatalf("running the %s framework against a stub CLI: %v", tc.framework, err)
			}

			args := forkedMCPServerArgs(t, capturePath)
			for _, want := range [][2]string{
				{"-data-dir", dataDir},
				{"-pr-repo", "acme/widgets"},
				{"-pr-branch", "grain/task-9"},
				// The rest of the option, in the order daemon.go passes
				// it: a host and a data directory transposed onto each
				// other would be a credential ladder rooted at
				// "github.example".
				{"-github-host", "github.example"},
			} {
				if !mcpArgsHave(args, want[0], want[1]) {
					t.Errorf("forked mcpserver args = %v, want %s %s", args, want[0], want[1])
				}
			}
			if !slices.Contains(args, "-github-insecure-http") {
				t.Errorf("forked mcpserver args = %v, want -github-insecure-http", args)
			}
		})
	}
}

// mcpArgsHave reports whether args carries name followed by value.
func mcpArgsHave(args []string, name, value string) bool {
	for i, a := range args {
		if a == name && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}
