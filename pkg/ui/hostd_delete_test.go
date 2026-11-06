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

package ui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// DELETE /api/hostds/{name}: a live hostd, local and an unknown name are
// refused; a revoked hostd goes with its tokens, recorded in the config
// audit; and its name can be registered again as a new hostd.
func TestDeleteHostdRoute(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	if err := client.Store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	rec := do(t, srv, http.MethodPost, "/api/hostds", `{"name":"ws-1","owner":"alice@example.com","labels":{"os":"linux"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/hostds = %d %s", rec.Code, rec.Body.String())
	}
	var first ui.IssuedHostdToken
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}

	rec = do(t, srv, http.MethodDelete, "/api/hostds/ws-1", "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "revoke it first") {
		t.Fatalf("deleting a live hostd = %d %s, want 400 saying revoke it first", rec.Code, rec.Body.String())
	}
	if h, _ := client.Store.GetHostd(ctx, "ws-1"); h == nil {
		t.Fatal("a refused delete removed ws-1")
	}
	if rec := do(t, srv, http.MethodDelete, "/api/hostds/local", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("deleting local = %d, want 400", rec.Code)
	}
	if rec := do(t, srv, http.MethodDelete, "/api/hostds/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("deleting an unknown hostd = %d, want 404", rec.Code)
	}

	if rec := do(t, srv, http.MethodPost, "/api/hostds/ws-1/revoke", ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, srv, http.MethodDelete, "/api/hostds/ws-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deleting a revoked hostd = %d %s", rec.Code, rec.Body.String())
	}
	if h, _ := client.Store.GetHostd(ctx, "ws-1"); h != nil {
		t.Fatalf("ws-1 is still in the registry: %+v", h)
	}
	audit, err := client.Store.ConfigAudit(ctx, 1)
	if err != nil || len(audit) == 0 || audit[0].Surface != "hostd:ws-1" || !strings.Contains(audit[0].Summary, "deleted") {
		t.Errorf("newest config audit = %+v, %v; want ws-1's delete", audit, err)
	}
	var listed []ui.HostdView
	if err := json.Unmarshal(do(t, srv, http.MethodGet, "/api/hostds", "").Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	for _, h := range listed {
		if h.Name == "ws-1" {
			t.Errorf("GET /api/hostds still lists ws-1: %+v", h)
		}
	}

	rec = do(t, srv, http.MethodPost, "/api/hostds", `{"name":"ws-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("registering ws-1 again = %d %s", rec.Code, rec.Body.String())
	}
	var again ui.IssuedHostdToken
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.Token == first.Token || again.Hostd.Owner != "" || len(again.Hostd.Labels) != 0 || again.Hostd.Revoked() {
		t.Errorf("the new ws-1 = %+v, want a fresh hostd with a new token", again)
	}
	if h, _ := client.Store.HostdForToken(ctx, first.Token); h != nil {
		t.Error("the deleted ws-1's token names the new one")
	}
}

// A revoked hostd that a model connection or a capability server still
// names by name is not deleted: the refusal lists each one. A label
// selector names no one hostd and does not hold it.
func TestDeleteHostdIsRefusedWhileConfigurationNamesIt(t *testing.T) {
	c, store, ctx := testClient(t)
	registerHostds(t, store, model.Hostd{Name: "ws-old", Labels: map[string]string{"pool": "a"}}, model.Hostd{Name: "laptop"})
	if _, err := store.RevokeHostd(ctx, "ws-old"); err != nil {
		t.Fatal(err)
	}
	set := func(backends []model.AgentBackend, servers map[string]protocol.ServerConfig) {
		t.Helper()
		if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
			cfg.AgentBackends = backends
			cfg.MCPServers = servers
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name     string
		backends []model.AgentBackend
		servers  map[string]protocol.ServerConfig
		want     string
	}{
		{"connection hostd", []model.AgentBackend{{ID: "agy-ws", Framework: "antigravity", Hostd: "ws-old"}}, nil,
			`model connection "agy-ws" (hostd)`},
		{"connection fallback", []model.AgentBackend{{ID: "agy-ws", Framework: "antigravity", Hostd: "laptop", HostdFallback: []string{"pool=b", "ws-old"}}}, nil,
			`model connection "agy-ws" (hostdFallback)`},
		{"capability server", nil, map[string]protocol.ServerConfig{"build-box": {Command: "build", Run: protocol.RunHostd, Hostd: "ws-old"}},
			`capability server "build-box" (hostd)`},
	} {
		set(tc.backends, tc.servers)
		_, err := c.DeleteHostd(ctx, "ws-old")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: DeleteHostd err = %v, want it to name %s", tc.name, err, tc.want)
		}
		if h, _ := store.GetHostd(ctx, "ws-old"); h == nil {
			t.Fatalf("%s: a refused delete removed ws-old", tc.name)
		}
	}

	// Through the bulk action, it is skipped and listed, and an
	// unreferenced revoked hostd ("gone", registerHostds') is deleted.
	set([]model.AgentBackend{{ID: "agy-ws", Framework: "antigravity", Hostd: "ws-old"}}, nil)
	res, err := c.DeleteRevokedHostds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0].Name != "gone" {
		t.Errorf("bulk deleted = %+v, want gone alone", res.Deleted)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Name != "ws-old" || len(res.Skipped[0].References) != 1 {
		t.Errorf("bulk skipped = %+v, want ws-old with its reference", res.Skipped)
	}
	if h, _ := store.GetHostd(ctx, "laptop"); h == nil {
		t.Error("the bulk action deleted a live hostd")
	}

	// A label selector matching it is no reference.
	set([]model.AgentBackend{{ID: "agy-pool", Framework: "antigravity", Hostd: "pool=a"}},
		map[string]protocol.ServerConfig{"build-box": {Command: "build", Run: protocol.RunHostd, Hostd: "pool=a"}})
	if _, err := c.DeleteHostd(ctx, "ws-old"); err != nil {
		t.Fatalf("deleting ws-old named only by a label selector: %v", err)
	}
}

// Usage-limit pauses keyed to the hostd (owner@hostd/<name>) -- in force
// in the gate, or only stored -- are cleared with it and reported;
// pauses on other hostds and on the bare credential stay.
func TestDeleteHostdClearsAndReportsItsUsageLimitPauses(t *testing.T) {
	c, store, ctx := testClient(t)
	registerHostds(t, store, model.Hostd{Name: "ws-old"}, model.Hostd{Name: "ws-new"})
	if _, err := store.RevokeHostd(ctx, "ws-old"); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour)
	gate := &fakePause{pauses: []model.UsageLimitPause{
		{Connection: "agy@hostd/ws-old", Model: "gemini-x", Team: model.DefaultTeamID, Until: until},
		{Connection: "agy@hostd/ws-new", Team: model.DefaultTeamID, Until: until},
		{Connection: "agy", Team: model.DefaultTeamID, Until: until},
	}}
	c.Config.AgentPause = gate
	if err := store.PutUsageLimitPause(ctx, "stored-only", model.UsageLimitPause{Connection: "codex@hostd/ws-old", Team: model.DefaultTeamID, Until: until}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutUsageLimitPause(ctx, "other", model.UsageLimitPause{Connection: "codex@hostd/ws-new", Team: model.DefaultTeamID, Until: until}); err != nil {
		t.Fatal(err)
	}

	d, err := c.DeleteHostd(ctx, "ws-old")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agy@hostd/ws-old (gemini-x)", "codex@hostd/ws-old"}
	if strings.Join(d.ClearedPauses, "|") != strings.Join(want, "|") {
		t.Errorf("cleared pauses = %q, want %q", d.ClearedPauses, want)
	}
	if len(gate.pauses) != 2 {
		t.Errorf("the gate holds %+v, want the ws-new and bare pauses kept", gate.pauses)
	}
	rows, err := store.ListUsageLimitPauses(ctx)
	if err != nil || len(rows) != 1 || rows[0].Key != "other" {
		t.Errorf("stored pauses = %+v, %v; want only ws-new's", rows, err)
	}
	audit, err := store.ConfigAudit(ctx, 1)
	if err != nil || len(audit) == 0 || !strings.Contains(audit[0].Summary, "codex@hostd/ws-old") {
		t.Errorf("newest config audit = %+v, %v; want the cleared pauses named", audit, err)
	}
}

// A run on a hostd since deleted still shows the name it recorded,
// marked deleted -- also once the name is registered again, since that
// is another hostd.
func TestPastRunsKeepADeletedHostdsName(t *testing.T) {
	c, store, ctx := testClient(t)
	registerHostds(t, store, model.Hostd{Name: "laptop"}, model.Hostd{Name: "desk"})
	started := c.Now().UTC().Add(-time.Minute)
	taskOn := map[string]string{}
	for _, h := range []string{"laptop", "desk", model.LocalHostd} {
		task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "train it on " + h, Description: "on a GPU", Approved: true})
		if err != nil {
			t.Fatal(err)
		}
		taskOn[h] = task.ID
		runID := task.ID + "-1"
		if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Sandbox: "s-" + h, Attempt: 1, StartedAt: started}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordTaskRunHostd(ctx, model.TaskRunHostd{RunID: runID, TaskID: task.ID, Hostd: h, At: started.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"laptop", "desk"} {
		if _, err := store.RevokeHostd(ctx, name); err != nil {
			t.Fatal(err)
		}
		if _, err := c.DeleteHostd(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.CreateHostd(ctx, model.Hostd{Name: "desk"}); err != nil {
		t.Fatal(err)
	}

	for h, wantDeleted := range map[string]bool{"laptop": true, "desk": true, model.LocalHostd: false} {
		detail, err := c.GetTask(ctx, taskOn[h])
		if err != nil || len(detail.Attempts) != 1 {
			t.Fatalf("%s: attempts = %+v, %v", h, detail.Attempts, err)
		}
		got := detail.Attempts[0].Hostd
		if got == nil || got.Hostd != h || got.Deleted != wantDeleted {
			t.Errorf("the run on %s shows hostd %+v, want %s with deleted=%v", h, got, h, wantDeleted)
		}
	}
}
