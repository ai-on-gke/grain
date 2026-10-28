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

package console_test

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/console"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestMaterializeMovesNothingIntoASandbox(t *testing.T) {
	p := console.New()
	ctx := context.Background()
	res, err := p.Resolve(ctx, model.CapabilityContext{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Refused {
		t.Errorf("resolve refused: %s", res.Reason)
	}
	m, err := p.Materialize(ctx, model.CapabilityContext{})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(m.Placements) != 0 || m.Lease != nil || m.CredentialOverride != nil {
		t.Errorf("console must place nothing and mint nothing: %+v", m)
	}
}

func TestSpecIsAnInteractiveConsoleGrant(t *testing.T) {
	spec := console.New().Spec()

	if spec.Name != console.CapabilityName {
		t.Errorf("Name = %q, want %q", spec.Name, console.CapabilityName)
	}
	if spec.Label == "" {
		t.Error("Label is empty, want a readable capability picker label")
	}
	if spec.Description == "" {
		t.Error("Description is empty, want a readable capability picker description")
	}
	if spec.Source != model.GrantByLabel {
		t.Errorf("Source = %q, want %q", spec.Source, model.GrantByLabel)
	}
	if spec.Provision != model.ProvisionGrant {
		t.Errorf("Provision = %q, want %q", spec.Provision, model.ProvisionGrant)
	}
	if spec.MaxLease != 0 {
		t.Errorf("MaxLease = %s, want none because the capability mints nothing", spec.MaxLease)
	}
	if len(spec.Requires) != 0 {
		t.Errorf("Requires = %v, want none because the capability resolves no credential", spec.Requires)
	}
}

func TestGrantedReadsATasksOwnGrants(t *testing.T) {
	tests := []struct {
		name   string
		grants []model.Grant
		want   bool
	}{
		{name: "no grants"},
		{
			name:   "unrelated grant",
			grants: []model.Grant{{Capability: "grain-ci"}},
		},
		{
			name: "console grant among other grants",
			grants: []model.Grant{
				{Capability: "grain-ci"},
				{Capability: console.CapabilityName},
				{Capability: "self-debug"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := console.Granted(tt.grants); got != tt.want {
				t.Errorf("Granted(%v) = %v, want %v", tt.grants, got, tt.want)
			}
		})
	}
}
