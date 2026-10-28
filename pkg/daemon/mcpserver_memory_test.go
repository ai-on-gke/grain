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
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// Memory has no team layer (single-tenant reset, stage 4d): a run
// holding the memory grant writes the deployment's own memory and a
// repo's through the route the daemon builds for a real run, whatever
// its team, and a run without the grant is refused by the API.
func TestAMemoryRunWritesTheDeploymentAndRepoMemory(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	srv := httptest.NewServer(ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator"),
		Capabilities:  ui.OfferedCapabilities(),
		LocalAPIToken: testAccessCode,
	}, store))
	t.Cleanup(srv.Close)
	client := accessCodeClient(srv.URL)

	const memoryRun, otherRun = "3219", "3220"
	if err := store.PutTask(ctx, model.Task{
		ID: memoryRun, Intent: model.IntentImplement, Title: "write down what grain has learned",
		TemplateID: model.MemoryAgentTemplateID,
		Grants:     []model.Grant{{Capability: "memory", Via: model.GrantByGrain}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID: otherRun, Intent: model.IntentImplement, Title: "something else",
	}); err != nil {
		t.Fatal(err)
	}
	tools := mcp.NewMemoryTools(newDaemonMemory(client, memoryRun))
	for _, args := range []map[string]any{
		{"text": "deployment notes"},
		{"text": "repo notes", "repo": "bwsalmon/grain"},
	} {
		if res := tools[0].Handler(ctx, args); res.IsError {
			t.Fatalf("update_memory %v refused: %s", args, res.Text)
		}
	}
	check := func(repo *model.RepoRef, wantText string) {
		t.Helper()
		m, err := store.GetMemory(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		if m == nil || m.Text != wantText || m.UpdatedBy.ID != memoryRun {
			t.Errorf("memory for %v is %+v, want %q written by task %s", repo, m, wantText, memoryRun)
		}
	}
	check(nil, "deployment notes")
	check(&model.RepoRef{Owner: "bwsalmon", Name: "grain"}, "repo notes")

	if _, err := client.PutMemory(ctx, "", "overwritten",
		model.Principal{Kind: model.PrincipalAgent, ID: otherRun}); err == nil {
		t.Error("a run without the memory grant wrote the deployment memory; want it refused")
	}
	check(nil, "deployment notes")
}
