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

package model_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

func TestNormalizeAndDeriveRegistryScopes(t *testing.T) {
	t.Parallel()

	cfg, err := model.NormalizeRegistryConfig(model.RegistryConfig{
		URL:         "https://grain.example.com/",
		Entry:       "  Team-Autoscaler ",
		AuthMode:    "oidc",
		DisplayName: "Autoscaler Grain",
		Purpose:     "Autoscaler control-loop heuristics and scale-down simulations",
		HostKind:    api.HostKindKubernetes,
		Listed:      true,
		Tags:        []string{"Autoscaler", "K8S", "autoscaler"},
		Scopes: []model.RegistryScopeRule{
			{Repo: "Acme/Monorepo", PathPrefix: "/pkg/autoscaler/"},
			{Repo: "acme/companion", PathPrefix: "", Role: api.RoleSecondary},
		},
	}, false)
	if err != nil {
		t.Fatalf("NormalizeRegistryConfig: %v", err)
	}
	if cfg.URL != "https://grain.example.com" || cfg.Entry != "team-autoscaler" {
		t.Errorf("normalized URL/Entry = (%q, %q)", cfg.URL, cfg.Entry)
	}
	if len(cfg.Tags) != 2 || cfg.Tags[0] != "autoscaler" || cfg.Tags[1] != "k8s" {
		t.Errorf("normalized Tags = %v", cfg.Tags)
	}

	// grain/config must be rejected as an explicit scope override.
	if _, err := model.NormalizeRegistryConfig(model.RegistryConfig{
		Scopes: []model.RegistryScopeRule{{Repo: "grain/config"}},
	}, false); err == nil {
		t.Errorf("expected grain/config scope rule to be rejected")
	}

	// Derive scopes from TargetRepos + WorkOwnership + settings overrides,
	// ensuring grain/config and stateRepo are excluded.
	stateRepo := model.RepoRef{Owner: "acme", Name: "grain-state"}
	repoConfigs := []model.RepoConfig{
		{
			Repo: model.RepoRef{Owner: "acme", Name: "primary-repo"},
			Work: model.WorkOwnership{Default: model.OwnershipUnset},
		},
		{
			Repo: model.RepoRef{Owner: "acme", Name: "secondary-repo"},
			Work: model.WorkOwnership{Default: model.OwnershipOff},
		},
		{
			Repo: model.RepoRef{Owner: "acme", Name: "monorepo"},
			Work: model.WorkOwnership{Default: model.OwnershipOff},
		},
	}

	advertised := model.DeriveRegistryScopes(
		[]string{"grain/config", "acme/grain-state", "Acme/Primary-Repo", "acme/secondary-repo", "acme/monorepo", "acme/excluded"},
		[]string{"acme/local-only"},
		stateRepo,
		repoConfigs,
		[]model.RegistryScopeRule{
			// Inherits secondary role from acme/monorepo's Work.Default = off!
			{Repo: "acme/monorepo", PathPrefix: "pkg/autoscaler"},
			{Repo: "acme/excluded", Exclude: true},
			{Repo: "acme/extra", PathPrefix: "services/api", Role: api.RoleSecondary},
		},
	)

	want := []model.RegistryAdvertisedScope{
		{Repo: "acme/extra", PathPrefix: "services/api", Role: api.RoleSecondary, Origin: model.RegistryScopeAdded},
		{Repo: "acme/local-only", PathPrefix: "", Role: api.RolePrimary, Origin: model.RegistryScopeFromRepoConfig},
		{Repo: "acme/monorepo", PathPrefix: "pkg/autoscaler", Role: api.RoleSecondary, Origin: model.RegistryScopeOverridden},
		{Repo: "acme/primary-repo", PathPrefix: "", Role: api.RolePrimary, Origin: model.RegistryScopeFromRepoConfig},
		{Repo: "acme/secondary-repo", PathPrefix: "", Role: api.RoleSecondary, Origin: model.RegistryScopeFromRepoConfig},
	}
	if len(advertised) != len(want) {
		t.Fatalf("DeriveRegistryScopes = %+v, want %+v", advertised, want)
	}
	for i := range want {
		if advertised[i] != want[i] {
			t.Errorf("advertised[%d] = %+v, want %+v", i, advertised[i], want[i])
		}
	}
}

func TestStoreRegistryConfigAndTaskCounts(t *testing.T) {
	t.Parallel()
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()

	regCfg := model.RegistryConfig{
		URL:         "https://grain.example.com",
		Entry:       "workstation-dev",
		DisplayName: "Dev Workstation",
		Purpose:     "Local controller testing",
		HostKind:    api.HostKindWorkstation,
		Listed:      true,
		Scopes: []model.RegistryScopeRule{
			{Repo: "acme/widgets", PathPrefix: "cmd/server", Role: api.RolePrimary},
		},
	}
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.Registry = regCfg
		return nil
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil || got == nil {
		t.Fatalf("GetConfig: %v, %v", got, err)
	}
	if got.Registry.URL != regCfg.URL || got.Registry.Entry != regCfg.Entry || !got.Registry.Listed || len(got.Registry.Scopes) != 1 {
		t.Errorf("round-tripped Registry = %+v, want %+v", got.Registry, regCfg)
	}

	now := time.Now().UTC().Truncate(time.Second)

	// Task 1: currently running.
	if err := store.PutTask(ctx, model.Task{ID: "t-running", Title: "Run", CreatedAt: &now, ApprovedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "run-1", TaskID: "t-running", StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	// Task 2: awaiting human reply / acknowledgement (completed with no pull request).
	if err := store.PutTask(ctx, model.Task{ID: "t-await", Title: "Ask", CreatedAt: &now, ApprovedAt: &now}); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(time.Minute)
	if err := store.ObserveField(ctx, "t-await", finished, func(o *model.Observation) {
		o.CompletedAt = &finished
	}); err != nil {
		t.Fatal(err)
	}

	// Task 3: queued (should count in neither active nor awaiting_reply).
	if err := store.PutTask(ctx, model.Task{ID: "t-queued", Title: "Queued", CreatedAt: &now, ApprovedAt: &now}); err != nil {
		t.Fatal(err)
	}

	active, awaiting, err := store.RegistryTaskCounts(ctx)
	if err != nil {
		t.Fatalf("RegistryTaskCounts: %v", err)
	}
	if active != 1 || awaiting != 1 {
		t.Errorf("RegistryTaskCounts = (active=%d, awaiting=%d), want (1, 1)", active, awaiting)
	}
}
