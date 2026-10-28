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
	"log"
	"slices"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A built-in capability server may let any agent create a task workspace
// of its own, so grain has them deleted once their tasks are done with:
// every workspaceJanitorInterval it asks each attached server advertising
// the two tools RegisterWorkspaceJanitorTools named which task workspaces
// exist, and hands back the ones whose tasks have closed, with when. The
// server keeps every workspace still inside its configured retention,
// held by an attempt, or holding pending work; grain only says which
// tasks are closed.
const (
	workspaceJanitorInterval = time.Hour
	workspaceJanitorTimeout  = 10 * time.Minute
)

// taskWorkspacesTool lists a server's task workspaces and
// cleanupWorkspacesTool deletes the closed ones; both "" (no janitor)
// until a server registers them.
var taskWorkspacesTool, cleanupWorkspacesTool string

// RegisterWorkspaceJanitorTools names the pair of tools a capability
// server serving task workspaces advertises for the janitor: list, which
// answers {"workspaces":[{"task":...}]}, and cleanup, which takes
// {"closed":{task: RFC 3339 time}}. Call it from init().
func RegisterWorkspaceJanitorTools(list, cleanup string) {
	taskWorkspacesTool, cleanupWorkspacesTool = list, cleanup
}

// workspaceJanitorRegistry is the part of servers.Registry the janitor
// uses.
type workspaceJanitorRegistry interface {
	Statuses() []servers.Status
	CallTool(ctx context.Context, capabilityID, tool string, arguments map[string]any, timeout time.Duration) ([]byte, error)
}

// closedAtFunc is when task id closed, or false while it is open or
// unknown -- either way its workspace is not grain's to have deleted.
type closedAtFunc func(ctx context.Context, id string) (time.Time, bool)

func storeClosedAt(store *model.Store) closedAtFunc {
	return func(ctx context.Context, id string) (time.Time, bool) {
		obs, err := store.GetObservation(ctx, id)
		if err != nil || obs == nil || obs.ClosedAt == nil {
			return time.Time{}, false
		}
		return *obs.ClosedAt, true
	}
}

// runWorkspaceJanitor sweeps every interval until ctx ends.
func runWorkspaceJanitor(ctx context.Context, reg workspaceJanitorRegistry, closedAt closedAtFunc, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		sweepTaskWorkspaces(ctx, reg, closedAt, log.Printf)
	}
}

// sweepTaskWorkspaces is one pass over every server with the janitor's
// tools.
func sweepTaskWorkspaces(ctx context.Context, reg workspaceJanitorRegistry, closedAt closedAtFunc, logf func(string, ...any)) {
	if taskWorkspacesTool == "" || cleanupWorkspacesTool == "" {
		return
	}
	for _, st := range reg.Statuses() {
		if st.State != servers.Attached || !slices.Contains(st.Tools, taskWorkspacesTool) || !slices.Contains(st.Tools, cleanupWorkspacesTool) {
			continue
		}
		raw, err := reg.CallTool(ctx, st.ID, taskWorkspacesTool, map[string]any{}, workspaceJanitorTimeout)
		if err != nil {
			logf("grain daemon: workspace janitor: listing %s's task workspaces: %v", st.ID, err)
			continue
		}
		var listed struct {
			Workspaces []struct {
				Task string `json:"task"`
			} `json:"workspaces"`
		}
		if err := json.Unmarshal(raw, &listed); err != nil {
			logf("grain daemon: workspace janitor: %s answered %s unreadably: %v", st.ID, taskWorkspacesTool, err)
			continue
		}
		closed := map[string]any{}
		for _, w := range listed.Workspaces {
			if at, ok := closedAt(ctx, w.Task); ok {
				closed[w.Task] = at.UTC().Format(time.RFC3339)
			}
		}
		if len(closed) == 0 {
			continue
		}
		raw, err = reg.CallTool(ctx, st.ID, cleanupWorkspacesTool, map[string]any{"closed": closed}, workspaceJanitorTimeout)
		if err != nil {
			logf("grain daemon: workspace janitor: cleaning up %s's task workspaces: %v", st.ID, err)
			continue
		}
		var res struct {
			Deleted []string `json:"deleted"`
		}
		if json.Unmarshal(raw, &res) == nil && len(res.Deleted) > 0 {
			logf("grain daemon: workspace janitor: %s deleted %d closed task workspace(s): %v", st.ID, len(res.Deleted), res.Deleted)
		}
	}
}
