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

package staterepo_test

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// An antigravity connection's model-turn limits (model.AgentBackend's
// ModelTurnTimeout and ModelRetryCutoff) go out to the state repository
// with the rest of the config row and come back through the settings
// import intact -- the path a pull request against grain/config takes.
func TestAgentBackendTurnLimitsRoundTripThroughTheStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	want := model.AgentBackend{
		ID: "corp-agy", Name: "Corp agy", Framework: model.AgentFrameworkAntigravity,
		ModelTurnTimeout: "40m", ModelRetryCutoff: "2m",
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: []model.AgentBackend{want}}); err != nil {
		t.Fatalf("putting the config: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}

	other, otherDB := openDB(t)
	if err := staterepo.ImportTables(ctx, otherDB, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("importing the settings tables: %v", err)
	}
	cfg, err := other.GetConfig(ctx)
	if err != nil || cfg == nil || len(cfg.AgentBackends) != 1 {
		t.Fatalf("reading the imported config: %+v, %v", cfg, err)
	}
	got := cfg.AgentBackends[0]
	if got.ModelTurnTimeout != want.ModelTurnTimeout || got.ModelRetryCutoff != want.ModelRetryCutoff {
		t.Errorf("turn limits after the round trip = %q/%q, want %q/%q",
			got.ModelTurnTimeout, got.ModelRetryCutoff, want.ModelTurnTimeout, want.ModelRetryCutoff)
	}
}

func TestKubernetesSandboxSettingsRoundTripThroughTheStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	in := model.Config{
		SandboxBackend:    model.SandboxBackendKubernetesGranule,
		K8sNamespace:      "grain-k8s",
		K8sKubectl:        "/usr/local/bin/kubectl",
		K8sSandboxImage:   "example/k8s-sandbox:v1",
		K8sGranuleImage:   "example/k8s-granule:v1",
		K8sWorkspace:      "/home/debian",
		K8sGitProxyHost:   "10.0.0.12",
		K8sSandboxService: "grain-sandbox",
		K8sServiceAccount: "sandbox-sa",
		K8sNodeSelector:   []string{"pool=kvm", "zone=us-central1-a"},
		K8sPrivileged:     true,
		K8sNetworkPolicy:  true,
		K8sDeployment:     "grain-deploy",
		K8sContainer:      "grain-container",
	}
	if err := store.PutConfig(ctx, in); err != nil {
		t.Fatalf("putting the config: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}

	other, otherDB := openDB(t)
	if err := staterepo.ImportTables(ctx, otherDB, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("importing the settings tables: %v", err)
	}
	cfg, err := other.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("reading the imported config: %+v, %v", cfg, err)
	}
	if cfg.SandboxBackend != in.SandboxBackend ||
		cfg.K8sNamespace != in.K8sNamespace ||
		cfg.K8sKubectl != in.K8sKubectl ||
		cfg.K8sSandboxImage != in.K8sSandboxImage ||
		cfg.K8sGranuleImage != in.K8sGranuleImage ||
		cfg.K8sWorkspace != in.K8sWorkspace ||
		cfg.K8sGitProxyHost != in.K8sGitProxyHost ||
		cfg.K8sSandboxService != in.K8sSandboxService ||
		cfg.K8sServiceAccount != in.K8sServiceAccount ||
		len(cfg.K8sNodeSelector) != 2 ||
		cfg.K8sNodeSelector[0] != "pool=kvm" ||
		cfg.K8sNodeSelector[1] != "zone=us-central1-a" ||
		!cfg.K8sPrivileged ||
		!cfg.K8sNetworkPolicy ||
		cfg.K8sDeployment != in.K8sDeployment ||
		cfg.K8sContainer != in.K8sContainer {
		t.Fatalf("imported K8s config = %+v, want %+v", *cfg, in)
	}
}
