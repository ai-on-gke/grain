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
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A deployment_id edited in Settings reaches a Kubernetes backend's
// instance label on the next tick, not on the next restart.
func TestRefreshHandsTheSandboxBackendAnEditedDeploymentID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	cfg := Config{
		PollInterval:     30 * time.Second,
		MaxWorkers:       1,
		K8sSandboxes:     true,
		CpuOvercommit:    1,
		MemoryOvercommit: 1,
	}
	stored := cfg.ToModelConfig()
	if err := store.PutConfig(ctx, stored); err != nil {
		t.Fatalf("store.PutConfig: %v", err)
	}
	initial := k8sInstanceLabel(ctx, store)
	if initial == "" {
		t.Fatal("k8sInstanceLabel = \"\", want the default derived from the instance id")
	}
	sb := orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{
		Image: "example/sandbox", Kubectl: "/nonexistent/kubectl", Instance: initial,
	})
	live := NewLiveConfig(store, sb, cfg, nil)
	deps := orchestrator.Deps{Budget: cfg.Budget()}

	live.Refresh(ctx, &deps)
	if got := sb.CurrentInstance(); got != initial {
		t.Fatalf("CurrentInstance = %q after an unchanged tick, want %q", got, initial)
	}

	stored.DeploymentID = "renamed"
	if err := store.PutConfig(ctx, stored); err != nil {
		t.Fatalf("store.PutConfig: %v", err)
	}
	live.Refresh(ctx, &deps)
	if got := sb.CurrentInstance(); got != "renamed" {
		t.Fatalf("CurrentInstance = %q after deployment_id was edited, want renamed", got)
	}
}
