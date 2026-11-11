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

package reconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hub"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
)

// The grain-config a hosted VM boots from names the instance: the
// entry's display name, else its entry name, cut to what the daemon's
// Settings accept and stripped of what a daemon.env line cannot carry
// the same way to systemd and docker.
func TestInstanceGrainConfigCarriesTheEntrysEnvironmentName(t *testing.T) {
	for _, tc := range []struct {
		name, display, want string
	}{
		{name: "alpha", want: "alpha"},
		{name: "alpha", display: "Alpha team", want: "Alpha team"},
		{name: "alpha", display: "  Alpha\nteam ", want: "Alpha team"},
		{name: "alpha", display: `Bob's "prod" $HOME \ ` + "`x`", want: "Bobs prod HOME x"},
		{name: "alpha", display: strings.Repeat("long ", 20), want: "long long long long long long lo"},
		{name: "alpha", display: `"$"`, want: "alpha"},
	} {
		env := InstanceGrainConfig(hub.Config{}, &store.EntryRow{Name: tc.name, DisplayName: tc.display}, nil, "")
		if got := env["environment_name"]; got != tc.want {
			t.Errorf("display name %q: environment_name = %q, want %q", tc.display, got, tc.want)
		}
		if n := len([]rune(env["environment_name"])); n > api.MaxEnvironmentNameRunes {
			t.Errorf("display name %q: environment_name is %d runes, over %d", tc.display, n, api.MaxEnvironmentNameRunes)
		}
	}
}

// What the reconciler hands the compute provider for a new hosted entry
// -- the StartupEnv the GCP provider writes as grain-config -- names the
// instance after the entry's display name.
func TestReconcilerStartupEnvNamesTheInstance(t *testing.T) {
	ctx := context.Background()
	rec, st, comp := newPasswordTestReconciler(t, nil)
	entry, err := st.GetEntryByName(ctx, "alice-vm")
	if err != nil {
		t.Fatalf("GetEntryByName: %v", err)
	}
	display := "Alice's sandbox"
	if _, err := st.UpdateEntryCatalog(ctx, entry.ID, entry.Version, store.UpdateEntryCatalogInput{DisplayName: &display}); err != nil {
		t.Fatalf("UpdateEntryCatalog: %v", err)
	}
	_ = rec.RunOnce(ctx)

	comp.mu.Lock()
	spec, ok := comp.specs["grain-vm-alice-vm"]
	comp.mu.Unlock()
	if !ok {
		t.Fatal("expected the reconciler to create grain-vm-alice-vm")
	}
	if got, want := spec.StartupEnv["environment_name"], "Alices sandbox"; got != want {
		t.Errorf("StartupEnv environment_name = %q, want %q", got, want)
	}
}
