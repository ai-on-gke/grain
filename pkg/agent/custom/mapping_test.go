package custom

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
)

// writeMapping writes a fake CLI and a mapping naming it into a state
// repository and returns the repository's directory.
func writeMapping(t *testing.T, cli, mapping string) string {
	t.Helper()
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "my-agent"), []byte(cli), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "my-agent.yaml"), []byte(mapping), 0o644); err != nil {
		t.Fatal(err)
	}
	return state
}

func mappedFramework(t *testing.T, state, model, effort string) (*Framework, error) {
	return New(Config{
		Command: "my-agent.yaml", StateRepoDir: state, Model: model, Effort: effort,
		ModelProxy: func(context.Context, agent.RunConfig) (string, string, func(), error) {
			return "http://proxy.local", "scoped-token", nil, nil
		},
		MCPServer: func(context.Context, agent.RunConfig) (Command, error) {
			return Command{Command: "/opt/grain", Args: []string{"mcpserver", "-task", "7"}}, nil
		},
	})
}

const streamJSONCLI = `#!/bin/sh
# Records what it was given, then answers as claude's stream-json does.
out="$PWD/record"
printf '%s\n' "$@" > "$out.argv"
cat "$MY_AGENT_MCP" > "$out.mcp"
for a in "$@"; do case "$a" in *prompt.md) cat "$a" > "$out.prompt";; esac; done
printf '%s|%s\n' "$PROXY_URL" "$PROXY_TOKEN" > "$out.env"
cat <<'EOF'
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"mcp__grain-sandbox__comment_on_issue","input":{"comment":"hi"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}
{"type":"result","subtype":"success","result":"all done"}
EOF
`

const fullMapping = `binary: my-agent
argv:
  headless: ["--prompt-file", "{prompt}", "--jsonl"]
mcp_config:
  format: claude-json
  env: MY_AGENT_MCP
permissions:
  none: ["--no-builtin-tools"]
events: claude-stream-json
model: ["--model", "{model}"]
effort: ["--effort={effort}"]
max_turns: ["--max-turns", "{max_turns}"]
model_proxy:
  url_env: PROXY_URL
  token_env: PROXY_TOKEN
`

func TestMappingRunsTheDeclaredCLI(t *testing.T) {
	state := writeMapping(t, streamJSONCLI, fullMapping)
	framework, err := mappedFramework(t, state, "m1", "high")
	if err != nil {
		t.Fatal(err)
	}
	sandbox := t.TempDir()
	result, err := framework.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: sandbox, MaxTurns: 9})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "all done" || len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "comment_on_issue" || result.ToolCalls[0].Text != "ok" {
		t.Fatalf("result = %#v", result)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(sandbox, "record."+name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	argv := strings.Split(strings.TrimSpace(read("argv")), "\n")
	want := []string{"--prompt-file", "", "--jsonl", "--model", "m1", "--effort=high", "--max-turns", "9", "--no-builtin-tools"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %q, want the shape %q", argv, want)
	}
	for i, w := range want {
		if w != "" && argv[i] != w {
			t.Fatalf("argv[%d] = %q, want %q (argv %q)", i, argv[i], w, argv)
		}
	}
	if !strings.HasSuffix(argv[1], "/prompt.md") || strings.HasPrefix(argv[1], sandbox) {
		t.Fatalf("prompt file %q should be staged outside the sandbox", argv[1])
	}
	if got := read("prompt"); got != "do the thing" {
		t.Fatalf("prompt = %q", got)
	}
	if got := read("mcp"); !strings.Contains(got, `"mcpServers":{"grain-sandbox":{"args":["mcpserver","-task","7"],"command":"/opt/grain"`) {
		t.Fatalf("mcp config = %s", got)
	}
	if got := read("env"); got != "http://proxy.local|scoped-token\n" {
		t.Fatalf("model proxy env = %q", got)
	}
}

func TestMappingCodexTOML(t *testing.T) {
	m := &Mapping{Binary: "/bin/x", MCPConfig: MappingMCPConfig{Format: "codex-toml", Arg: []string{"-c", "{file}"}}, Permissions: MappingPermissions{None: &[]string{}}}
	run, err := m.plan("/stage", Request{Prompt: "p", MCPServer: Command{Command: "/opt/grain", Args: []string{"mcpserver", `a"b`}, Env: []string{"HOME=/h", "DOCKER_HOST=unix:///d"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `[mcp_servers."grain-sandbox"]
command = "/opt/grain"
args = ["mcpserver", "a\"b"]
required = true
env = { "DOCKER_HOST" = "unix:///d", "HOME" = "/h" }
`
	if got := string(run.files["mcp.toml"]); got != want {
		t.Fatalf("mcp.toml =\n%s\nwant\n%s", got, want)
	}
	if strings.Join(run.argv, " ") != "/bin/x -c /stage/mcp.toml" {
		t.Fatalf("argv = %q", run.argv)
	}
}

func TestMappingEventsNone(t *testing.T) {
	cli := "#!/bin/sh\necho plain answer\n"
	mapping := `binary: my-agent
argv: {headless: ["{prompt}"]}
mcp_config: {format: claude-json, arg: ["--mcp", "{file}"]}
permissions: {none: []}
events: none
`
	state := writeMapping(t, cli, mapping)
	framework, err := mappedFramework(t, state, "", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := framework.Run(context.Background(), agent.RunConfig{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "plain answer" {
		t.Fatalf("result = %#v", result)
	}
}

func TestMappingRefusesUndeclaredSettings(t *testing.T) {
	base := map[string]string{
		"binary":      "binary: my-agent",
		"argv":        `argv: {headless: ["{prompt}"]}`,
		"mcp_config":  `mcp_config: {format: claude-json, arg: ["{file}"]}`,
		"permissions": `permissions: {none: []}`,
		"events":      "events: none",
	}
	build := func(override map[string]string) string {
		var lines []string
		for _, k := range []string{"binary", "argv", "mcp_config", "permissions", "events"} {
			v, ok := override[k]
			if !ok {
				v = base[k]
			}
			if v != "" {
				lines = append(lines, v)
			}
		}
		return strings.Join(lines, "\n") + "\n" + override["extra"]
	}
	for _, tc := range []struct {
		name          string
		override      map[string]string
		model, effort string
		want          string
	}{
		{name: "no prompt", override: map[string]string{"argv": `argv: {headless: ["--go"]}`}, want: "{prompt}"},
		{name: "unknown field", override: map[string]string{"extra": `argv2: x`}, want: "argv2"},
		{name: "interactive argv", override: map[string]string{"argv": `argv: {headless: ["{prompt}"], interactive: ["--tui"]}`}, want: "argv.interactive is refused"},
		{name: "no mcp format", override: map[string]string{"mcp_config": `mcp_config: {arg: ["{file}"]}`}, want: "mcp_config.format is required"},
		{name: "stdio bridge", override: map[string]string{"mcp_config": `mcp_config: {format: stdio-bridge, arg: ["{file}"]}`}, want: "not supported"},
		{name: "mcp never passed", override: map[string]string{"mcp_config": `mcp_config: {format: claude-json}`}, want: "must pass the config"},
		{name: "no permissions", override: map[string]string{"permissions": ""}, want: "permissions.none is required"},
		{name: "permissions all", override: map[string]string{"permissions": `permissions: {none: [], all: []}`}, want: "permissions.all is refused"},
		{name: "no events", override: map[string]string{"events": ""}, want: "events is required"},
		{name: "undeclared model", model: "m1", want: "declares no model argument"},
		{name: "undeclared effort", effort: "high", want: "declares no effort argument"},
		{name: "half a proxy", override: map[string]string{"extra": "model_proxy: {url_env: U}\n"}, want: "both url_env and token_env"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := writeMapping(t, "#!/bin/sh\n", build(tc.override))
			_, err := mappedFramework(t, state, tc.model, tc.effort)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestMappingRefusesANonExecutableBinary(t *testing.T) {
	state := writeMapping(t, "#!/bin/sh\n", `binary: my-agent
argv: {headless: ["{prompt}"]}
mcp_config: {format: claude-json, arg: ["{file}"]}
permissions: {none: []}
events: none
`)
	if err := os.Chmod(filepath.Join(state, "my-agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mappedFramework(t, state, "", ""); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("err = %v", err)
	}
}
