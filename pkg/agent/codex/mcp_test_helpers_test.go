package codex

import (
	"context"
	"encoding/json"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

func newTestFramework(run runner, grain string, opts ...Option) *Framework {
	f := newFramework(run, grain, opts...)
	f.startMCP = func(_ context.Context, _ string, args []string) (*mcp.ManagedHTTP, error) {
		if recorder, ok := run.(*recordingRunner); ok {
			data, _ := json.Marshal(args)
			recorder.mcpArgs = string(data)
		}
		return &mcp.ManagedHTTP{URL: "http://127.0.0.1:12345/mcp", Token: "test-only-token", Tools: []mcp.ToolInfo{{Name: "write_file"}, {Name: "run_command"}}}, nil
	}
	return f
}

func newTerminalTestFramework(binary, grain string, opts ...Option) *Framework {
	f := newTestFramework(nil, grain, opts...)
	f.binPath = binary
	return f
}
