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

package granule_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TestModelBuiltinSandboxShapeMirrorsGranuleDefaults keeps the default
// shape model measures a proposal's sandbox against
// (model.EffectiveSandboxShape) the one a VM is actually built at.
func TestModelBuiltinSandboxShapeMirrorsGranuleDefaults(t *testing.T) {
	if model.BuiltinSandboxCPUs != granule.DefaultCPUs ||
		model.BuiltinSandboxMemoryMB != granule.DefaultMemoryMB ||
		model.BuiltinSandboxDiskGB != granule.DefaultDiskGB {
		t.Fatalf("model.Builtin* sandbox shape (%d CPUs, %d MiB, %d GiB) differs from pkg/granule's defaults (%d, %d, %d)",
			model.BuiltinSandboxCPUs, model.BuiltinSandboxMemoryMB, model.BuiltinSandboxDiskGB,
			granule.DefaultCPUs, granule.DefaultMemoryMB, granule.DefaultDiskGB)
	}
}
