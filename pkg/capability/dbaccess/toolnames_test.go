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

package dbaccess_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/dbaccess"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Each tool grain#sql puts on a run's roster is one mcp.CapabilityForTool
// knows, under the grant that puts it there, so a run without the grant
// that calls it is told which grant it lacks rather than that grain has
// no such tool. execute_grain_db was keyed as exec_grain_db there, and a
// run holding only grain#sql={"write":false} that called it was answered
// "unknown tool execute_grain_db" (docs/test-runs/2026-11-02-grain-capability-server-upgrade.md).
func TestEveryDBToolNamesTheGrantThatServesIt(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"query_grain_db":   model.GrainSQLRef,
		"execute_grain_db": model.GrainSQLWriteGrant,
	}
	tools := dbaccess.Tools(dbaccess.Config{AllowWrite: true})
	if len(tools) != len(want) {
		t.Fatalf("dbaccess.Tools = %d tools, want %d", len(tools), len(want))
	}
	for _, tool := range tools {
		got, ok := mcp.CapabilityForTool(tool.Name)
		if !ok || got != want[tool.Name] {
			t.Errorf("mcp.CapabilityForTool(%q) = %q, %v; want %q", tool.Name, got, ok, want[tool.Name])
		}
	}

	// What a read-only run is told when it calls the write tool anyway.
	registry := mcp.NewRegistry()
	registry.Register(dbaccess.Tools(dbaccess.Config{})...)
	client := mcp.NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })
	_, err := client.CallTool(context.Background(), "execute_grain_db", map[string]any{"statement": "DELETE FROM task"})
	if err == nil || !strings.Contains(err.Error(), model.GrainSQLWriteGrant) {
		t.Errorf("calling execute_grain_db without the write grant: %v, want an error naming %s", err, model.GrainSQLWriteGrant)
	}
}
