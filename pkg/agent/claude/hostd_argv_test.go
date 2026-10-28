package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// The argv and environment claude is started with, exactly as Run builds
// them, pass grain hostd's checks for a claude entry whose paths_under is
// the data dir its staging goes under: the --mcp-config file and a staged
// Vertex credential are paths under it, and nothing else in argv is a
// path, a ".." or a "~" for hostd to refuse.
func TestTheArgvAndEnvRunBuildsPassHostdsChecks(t *testing.T) {
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", data)
	sandbox := filepath.Join(data, "sandboxes", "task-1")
	if err := os.MkdirAll(sandbox, 0o755); err != nil {
		t.Fatal(err)
	}
	allow, err := hostd.ParseAllowlist([]byte(strings.ReplaceAll(`
clis:
  claude:
    path: /usr/local/bin/claude
    paths_under: [DATA]
    cwd_under: [DATA]
    env: [CLAUDE_CODE_*, MCP_TOOL_TIMEOUT, GOOGLE_APPLICATION_CREDENTIALS, ANTHROPIC_VERTEX_PROJECT_ID]
`, "DATA", data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{"oauth", []Option{WithModel("claude-opus-4-5"), WithMaxTurns(40), WithOAuthToken("sk-ant-oat-test")}},
		{"vertex", []Option{WithModel("claude-opus-4-5@20251101"), WithVertex(true),
			WithOAuthToken(`{"type":"service_account","project_id":"p"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "done"})}
			// hostd refuses a path that does not exist, so the check runs
			// as claude starts, while what Run staged is there.
			var checkErr error
			fake.onRun = func(args, env []string) {
				req := &hostd.Request{Kind: hostd.KindCLI, Name: "claude", Args: args, Env: env, Dir: sandbox}
				_, checkErr = allow.Check(req, []string{"PATH=/usr/bin", "HOME=/home/me"})
			}
			f := newFramework(fake, "/usr/local/bin/grain", tc.opts...)
			if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "fix /etc/hosts handling, see ../README", SandboxRoot: sandbox}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(fake.gotArgs) == 0 {
				t.Fatal("claude was never started")
			}
			if cfg := argValue(fake.gotArgs, "--mcp-config"); !strings.HasPrefix(cfg, data+"/") {
				t.Fatalf("--mcp-config = %q, want a file staged under %s", cfg, data)
			}
			if checkErr != nil {
				t.Errorf("hostd refused claude as Run starts it: %v\nargs %q\nenv %q", checkErr, fake.gotArgs, fake.gotEnv)
			}
		})
	}
}
