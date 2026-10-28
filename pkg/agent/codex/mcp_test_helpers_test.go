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
