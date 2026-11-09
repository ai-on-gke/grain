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

package capability_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/capabilitytest"
	"github.com/ai-on-gke/grain/pkg/model"
)

// zzDefaultOn is the recorded grant of the made-up default-on method below.
const zzDefaultOn = capabilitytest.MethodServer + "#zz-default-on={}"

// A DefaultOn method is what a brand-new deployment of the build
// starts with in its DefaultCapabilities, and nothing else is.
func TestDefaultOnSeedsANewDeploymentConfig(t *testing.T) {
	if got := capability.NewDeploymentConfig().DefaultCapabilities; len(got) != 0 {
		t.Fatalf("NewDeploymentConfig().DefaultCapabilities = %v before anything registers a default", got)
	}
	if got := model.DefaultConsoleSessions()[0].Capabilities; slices.Contains(got, zzDefaultOn) {
		t.Fatalf("DefaultConsoleSessions()[0].Capabilities = %v before anything registers a default", got)
	}
	unregister := capability.Register(capability.Method{
		Server:    capabilitytest.MethodServer,
		Spec:      capabilitytest.MethodSpec("zz-default-on"),
		DefaultOn: true,
	})
	t.Cleanup(unregister)
	t.Cleanup(capability.Register(capability.Method{
		Server: capabilitytest.MethodServer,
		Spec:   capabilitytest.MethodSpec("zz-not-default"),
	}))

	if got := capability.DefaultOn(); !slices.Equal(got, []string{zzDefaultOn}) {
		t.Errorf("DefaultOn() = %v, want only the registration with DefaultOn set", got)
	}
	cfg := capability.NewDeploymentConfig()
	if !slices.Equal(cfg.DefaultCapabilities, []string{zzDefaultOn}) {
		t.Errorf("NewDeploymentConfig().DefaultCapabilities = %v, want [zz-default-on]", cfg.DefaultCapabilities)
	}
	if cfg.TaskBranchTemplate != model.DefaultTaskBranchTemplate {
		t.Errorf("NewDeploymentConfig() lost model.NewDeploymentConfig's own settings: %+v", cfg)
	}
	wantDirector := []string{
		"api:self-debug",
		model.GrainSourceGrant,
		model.GrainJournalGrant,
		"api:deployment:read",
		model.GrainHealthGrant,
		model.BootstrapPlaybooksGrant,
		"api:task-admin",
		"api:capabilities:read",
		"api:capabilities:detach",
		"api:settings:read",
		"api:repos:read",
		"api:releases:read",
		"api:schedules:read",
		"api:templates:read",
		"api:suites:read",
		"api:dispatch:read",
		zzDefaultOn,
	}
	if got := model.DefaultConsoleSessions()[0].Capabilities; !slices.Equal(got, wantDirector) {
		t.Errorf("DefaultConsoleSessions()[0].Capabilities = %v, want %v", got, wantDirector)
	}
	unregister()
	if got := model.DefaultConsoleSessions()[0].Capabilities; slices.Contains(got, zzDefaultOn) {
		t.Errorf("DefaultConsoleSessions()[0].Capabilities = %v after unregistering", got)
	}
}
