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
	"github.com/ai-on-gke/grain/pkg/capability/admincaps"
	"github.com/ai-on-gke/grain/pkg/capability/capabilitytest"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRegisterProvider(t *testing.T) {
	if _, ok := capability.Lookup(capabilitytest.ID); ok {
		t.Fatalf("%s is registered before the test registers it", capabilitytest.ID)
	}
	unregister := capability.RegisterProvider(capability.Registration{
		ID: "zz-made-up", Name: "ZZ", New: func() model.CapabilityProvider { return &capabilitytest.Provider{} },
	})
	capabilitytest.Register(t)

	var ids []string
	for _, r := range capability.Registered() {
		ids = append(ids, r.ID)
	}
	if want := []string{capabilitytest.ID, "zz-made-up"}; !slices.Equal(ids, want) {
		t.Errorf("Registered() = %v, want %v, ordered by id", ids, want)
	}
	if got := capability.RunGrants(); !slices.Equal(got, []string{capabilitytest.ID}) {
		t.Errorf("RunGrants() = %v, want only the registration with RunGrant set", got)
	}
	if r, ok := capability.Lookup(capabilitytest.ID); !ok || r.Name != capabilitytest.Name {
		t.Errorf("Lookup(%q) = %+v, %v", capabilitytest.ID, r, ok)
	}

	unregister()
	if _, ok := capability.Lookup("zz-made-up"); ok {
		t.Error("unregistering left the registration in place")
	}
}

func TestRegisterProviderRefusesADuplicateOrAnEmptyRegistration(t *testing.T) {
	capabilitytest.Register(t)
	for name, r := range map[string]capability.Registration{
		"duplicate": {ID: capabilitytest.ID, New: func() model.CapabilityProvider { return nil }},
		"no id":     {New: func() model.CapabilityProvider { return nil }},
		"no New":    {ID: "zz-no-new"},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("RegisterProvider did not panic")
				}
			}()
			capability.RegisterProvider(r)
		})
	}
}

// A registered capability is listed after admincaps' own.
func TestAdmincapsIncludesRegisteredProviders(t *testing.T) {
	capabilitytest.Register(t)
	providers := admincaps.Providers()
	if got := providers[len(providers)-1].Spec().Name; got != capabilitytest.ID {
		t.Errorf("admincaps.Providers() ends with %s, want %s last", got, capabilitytest.ID)
	}
}

// A DefaultOn registration is what a brand-new deployment of the build
// starts with in its DefaultCapabilities, and nothing else is.
func TestDefaultOnSeedsANewDeploymentConfig(t *testing.T) {
	if got := capability.NewDeploymentConfig().DefaultCapabilities; len(got) != 0 {
		t.Fatalf("NewDeploymentConfig().DefaultCapabilities = %v before anything registers a default", got)
	}
	if got := model.DefaultConsoleSessions()[0].Capabilities; slices.Contains(got, "zz-default-on") {
		t.Fatalf("DefaultConsoleSessions()[0].Capabilities = %v before anything registers a default", got)
	}
	unregister := capability.RegisterProvider(capability.Registration{
		ID: "zz-default-on", Name: "ZZ", DefaultOn: true,
		New: func() model.CapabilityProvider { return &capabilitytest.Provider{} },
	})
	t.Cleanup(unregister)
	capabilitytest.Register(t)

	if got := capability.DefaultOn(); !slices.Equal(got, []string{"zz-default-on"}) {
		t.Errorf("DefaultOn() = %v, want only the registration with DefaultOn set", got)
	}
	cfg := capability.NewDeploymentConfig()
	if !slices.Equal(cfg.DefaultCapabilities, []string{"zz-default-on"}) {
		t.Errorf("NewDeploymentConfig().DefaultCapabilities = %v, want [zz-default-on]", cfg.DefaultCapabilities)
	}
	if cfg.TaskBranchTemplate != model.DefaultTaskBranchTemplate {
		t.Errorf("NewDeploymentConfig() lost model.NewDeploymentConfig's own settings: %+v", cfg)
	}
	wantDirector := []string{"self-debug", model.GrainJournalGrant, "api:deployment:read", model.GrainHealthGrant, "bootstrap-playbooks", "zz-default-on"}
	if got := model.DefaultConsoleSessions()[0].Capabilities; !slices.Equal(got, wantDirector) {
		t.Errorf("DefaultConsoleSessions()[0].Capabilities = %v, want %v", got, wantDirector)
	}
	unregister()
	if got := model.DefaultConsoleSessions()[0].Capabilities; slices.Contains(got, "zz-default-on") {
		t.Errorf("DefaultConsoleSessions()[0].Capabilities = %v after unregistering", got)
	}
}
