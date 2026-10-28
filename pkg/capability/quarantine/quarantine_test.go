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

package quarantine

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestQuarantineProviderSpec(t *testing.T) {
	p := New()
	spec := p.Spec()
	if spec.Name != CapabilityName {
		t.Fatalf("spec.Name = %q, want %q", spec.Name, CapabilityName)
	}
	if spec.Source != model.GrantByLabel {
		t.Fatalf("spec.Source = %v, want %v", spec.Source, model.GrantByLabel)
	}
	if spec.Provision != model.ProvisionGrant {
		t.Fatalf("spec.Provision = %v, want %v", spec.Provision, model.ProvisionGrant)
	}
	if spec.Description == "" {
		t.Fatalf("spec.Description should not be empty")
	}
}
