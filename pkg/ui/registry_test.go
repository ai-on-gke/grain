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
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestRegistrySettingsAndEndpoints(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	secDir := t.TempDir()
	secStore := secrets.Open(secrets.Config{
		File:    filepath.Join(secDir, "secrets.age"),
		KeyFile: filepath.Join(secDir, "secrets.key"),
	})
	c.Config.Secrets = secStore

	var syncCalls atomic.Int32
	c.Config.TriggerRegistrySync = func() { syncCalls.Add(1) }

	srv := ui.NewServerWithClient(c)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	// Unconfigured daemon: GET /api/registry reports disabled, and
	// GET /api/sandboxes omits registry health.
	rec := do(t, srv, http.MethodGet, "/api/registry", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/registry = %d: %s", rec.Code, rec.Body.String())
	}
	snap := decode[model.RegistrySnapshot](t, rec)
	if snap.Enabled || snap.State != model.RegistryStateDisabled {
		t.Fatalf("initial registry snapshot = %+v, want disabled", snap)
	}
	healthRec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if healthRec.Code != http.StatusOK {
		t.Fatalf("GET /api/sandboxes = %d: %s", healthRec.Code, healthRec.Body.String())
	}
	if h := decode[ui.SandboxHealthResponse](t, healthRec); h.Registry != nil {
		t.Fatalf("unconfigured deployment reported non-nil Registry health: %+v", h.Registry)
	}

	// Invalid registry URL (plain HTTP to non-loopback) is rejected by UpdateSettings.
	badReg := model.RegistryConfig{URL: "http://registry.example.com", Entry: "grain-dev"}
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{Registry: &badReg}); err == nil {
		t.Fatal("expected plain HTTP non-loopback registry URL to fail validation")
	}

	// Add two repositories and configure work ownership on one as "off" -> secondary.
	if _, err := c.AddTargetRepo(ctx, "acme/widgets"); err != nil {
		t.Fatalf("AddTargetRepo(acme/widgets): %v", err)
	}
	if _, err := c.AddTargetRepo(ctx, "acme/docs"); err != nil {
		t.Fatalf("AddTargetRepo(acme/docs): %v", err)
	}
	if _, err := c.SetRepoWorkDefault(ctx, "acme/docs", "off"); err != nil {
		t.Fatalf("SetRepoWorkDefault(acme/docs, off): %v", err)
	}

	// Set registration token secret via PUT /api/secrets/registry-registration-token/value
	// and verify TriggerRegistrySync fires.
	beforeSync := syncCalls.Load()
	secRec := do(t, srv, http.MethodPut,
		"/api/secrets/"+secrets.RegistryRegistrationTokenSecret+"/"+secrets.AgentCredentialKey,
		`{"value":"grc_supersecrettokenvalue"}`)
	if secRec.Code != http.StatusOK {
		t.Fatalf("PUT registry secret = %d: %s", secRec.Code, secRec.Body.String())
	}
	if syncCalls.Load() <= beforeSync {
		t.Fatal("setting registry-registration-token secret did not trigger registry sync")
	}

	// Configure registry settings with a path-prefix scope override on acme/widgets.
	regCfg := model.RegistryConfig{
		URL:         "https://registry.example.com/",
		Entry:       "grain-dev",
		DisplayName: "Grain Dev",
		Purpose:     "Widgets team controller",
		HostKind:    api.HostKindWorkstation,
		Listed:      true,
		Scopes: []model.RegistryScopeRule{
			{Repo: "acme/widgets", PathPrefix: "/pkg/autoscaler/"},
		},
	}
	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{Registry: &regCfg})
	if err != nil {
		t.Fatalf("UpdateSettings(Registry): %v", err)
	}
	if saved.Registry.URL != "https://registry.example.com" {
		t.Errorf("saved Registry.URL = %q, want trailing slash trimmed", saved.Registry.URL)
	}
	if !saved.RegistryTokenSet {
		t.Error("saved.RegistryTokenSet = false, want true")
	}

	// Seed one running task so GET /api/registry and GET /api/registry/status
	// report live queue counts.
	now := time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, model.Task{
		ID:         "101",
		Title:      "Fix autoscaler",
		ApprovedAt: &now,
		CreatedAt:  &now,
	}); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID:        "run-101",
		TaskID:    "101",
		Attempt:   1,
		StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// GET /api/registry/status returns wire api.InstanceStatus with derived scopes.
	statusRec := do(t, srv, http.MethodGet, "/api/registry/status", "")
	if statusRec.Code != http.StatusOK {
		t.Fatalf("GET /api/registry/status = %d: %s", statusRec.Code, statusRec.Body.String())
	}
	inst := decode[api.InstanceStatus](t, statusRec)
	if inst.ActiveTasks != 1 {
		t.Errorf("InstanceStatus.ActiveTasks = %d, want 1", inst.ActiveTasks)
	}
	if len(inst.Scopes) != 2 {
		t.Fatalf("InstanceStatus.Scopes = %+v, want 2 scopes", inst.Scopes)
	}
	if inst.Scopes[0].Repo != "acme/docs" || inst.Scopes[0].Role != api.ScopeRoleSecondary {
		t.Errorf("Scopes[0] = %+v, want acme/docs secondary", inst.Scopes[0])
	}
	if inst.Scopes[1].Repo != "acme/widgets" || inst.Scopes[1].PathPrefix != "pkg/autoscaler" || inst.Scopes[1].Role != api.ScopeRolePrimary {
		t.Errorf("Scopes[1] = %+v, want acme/widgets pkg/autoscaler primary", inst.Scopes[1])
	}

	// Verify LogRedactor redacts secret material in LastError on GET /api/registry
	// and GET /api/sandboxes, and POST /api/registry/heartbeat invokes callback.
	c.Config.LogRedactor = func() (*grainlog.Redactor, error) {
		return grainlog.NewRedactor([]grainlog.KnownValue{
			{Label: "secret registry-registration-token", Value: "grc_supersecrettokenvalue"},
		}), nil
	}
	var hbTriggered atomic.Bool
	c.Config.TriggerRegistryHeartbeat = func(_ context.Context) (model.RegistrySnapshot, error) {
		hbTriggered.Store(true)
		return model.RegistrySnapshot{
			Enabled:   true,
			State:     model.RegistryStateError,
			Config:    saved.Registry,
			LastError: "auth failed with bearer grc_supersecrettokenvalue",
		}, nil
	}
	c.Config.RegistryStatus = c.Config.TriggerRegistryHeartbeat

	hbRec := do(t, srv, http.MethodPost, "/api/registry/heartbeat", "")
	if hbRec.Code != http.StatusOK {
		t.Fatalf("POST /api/registry/heartbeat = %d: %s", hbRec.Code, hbRec.Body.String())
	}
	if !hbTriggered.Load() {
		t.Error("POST /api/registry/heartbeat did not invoke TriggerRegistryHeartbeat")
	}
	hbSnap := decode[model.RegistrySnapshot](t, hbRec)
	if strings.Contains(hbSnap.LastError, "grc_supersecrettokenvalue") {
		t.Errorf("LastError leaked secret token: %q", hbSnap.LastError)
	}
}
