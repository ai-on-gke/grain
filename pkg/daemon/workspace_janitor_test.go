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

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/servers"
)

type janitorRegistry struct {
	statuses []servers.Status
	calls    []string
	cleanup  map[string]any
}

func (r *janitorRegistry) Statuses() []servers.Status { return r.statuses }

func (r *janitorRegistry) CallTool(_ context.Context, id, tool string, args map[string]any, _ time.Duration) ([]byte, error) {
	r.calls = append(r.calls, id+" "+tool)
	switch tool {
	case taskWorkspacesTool:
		return []byte(`{"workspaces":[{"workspace":"grain_d_task_1","task":"1"},{"workspace":"grain_d_task_2","task":"2"}]}`), nil
	case cleanupWorkspacesTool:
		r.cleanup, _ = args["closed"].(map[string]any)
		return json.Marshal(map[string]any{"deleted": []string{"grain_d_task_1"}})
	}
	return nil, fmt.Errorf("unexpected %s", tool)
}

// withJanitorTools registers a made-up workspace server's janitor tools
// for the length of t.
func withJanitorTools(t *testing.T) {
	t.Helper()
	list, cleanup := taskWorkspacesTool, cleanupWorkspacesTool
	RegisterWorkspaceJanitorTools("ws_task_workspaces", "ws_cleanup_workspaces")
	t.Cleanup(func() { RegisterWorkspaceJanitorTools(list, cleanup) })
}

// The janitor hands a server with the workspace tools only the closed tasks
// among its workspaces, and leaves servers without them alone.
func TestWorkspaceJanitorPassesOnlyClosedTasks(t *testing.T) {
	withJanitorTools(t)
	closedAt := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	reg := &janitorRegistry{statuses: []servers.Status{
		{ID: "workspace", State: servers.Attached, Tools: []string{taskWorkspacesTool, cleanupWorkspacesTool}},
		{ID: "other", State: servers.Attached, Tools: []string{"test_script"}},
		{ID: "down", State: servers.Failed, Tools: []string{taskWorkspacesTool, cleanupWorkspacesTool}},
	}}
	closed := func(_ context.Context, id string) (time.Time, bool) { return closedAt, id == "1" }
	var logs []string
	sweepTaskWorkspaces(context.Background(), reg, closed, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	if len(reg.calls) != 2 || reg.calls[0] != "workspace "+taskWorkspacesTool || reg.calls[1] != "workspace "+cleanupWorkspacesTool {
		t.Fatalf("calls %v", reg.calls)
	}
	if len(reg.cleanup) != 1 || reg.cleanup["1"] != closedAt.Format(time.RFC3339) {
		t.Errorf("cleanup was told %v, want only task 1", reg.cleanup)
	}
	if len(logs) != 1 {
		t.Errorf("logs %v", logs)
	}
}
