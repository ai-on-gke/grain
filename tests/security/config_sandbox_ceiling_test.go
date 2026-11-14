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

package security

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestConfigReportsTheCallersSandboxShapeCeiling holds /api/config's
// sandboxShapeCeiling, which the new-task form shows beside its sandbox
// fields, to the bound a filing is actually refused above: the
// deployment's maximum for an unlimited role, and a limited role's own
// ceiling beneath it, with the maximum standing in for what the role's
// entry leaves at zero.
func TestConfigReportsTheCallersSandboxShapeCeiling(t *testing.T) {
	rig := newEscalationRig(t)
	ctx := context.Background()
	cfg, err := rig.store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SandboxShapeLimits = model.SandboxShapeLimits{
		Max:   model.SandboxShape{CPUs: 16, MemoryMB: 65536, DiskGB: 200},
		Roles: map[string]model.SandboxShape{"grain/contributor": {CPUs: 4, MemoryMB: 16384}},
	}
	if err := rig.store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	ceiling := func(who string) model.SandboxShape {
		t.Helper()
		code, raw := rig.callAs(who, http.MethodGet, "/api/config", "")
		if code != http.StatusOK {
			t.Fatalf("GET /api/config as %s = %d %s", who, code, raw)
		}
		var resp struct {
			SandboxShapeCeiling *model.SandboxShape `json:"sandboxShapeCeiling"`
		}
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.SandboxShapeCeiling == nil {
			t.Fatalf("GET /api/config as %s carries no sandboxShapeCeiling: %s", who, raw)
		}
		return *resp.SandboxShapeCeiling
	}
	if got, want := ceiling("ops@example.com"), (model.SandboxShape{CPUs: 16, MemoryMB: 65536, DiskGB: 200}); got != want {
		t.Errorf("operator's ceiling = %+v, want the deployment maximum %+v", got, want)
	}
	if got, want := ceiling("contrib@example.com"), (model.SandboxShape{CPUs: 4, MemoryMB: 16384, DiskGB: 200}); got != want {
		t.Errorf("contributor's ceiling = %+v, want its role ceiling %+v", got, want)
	}
}
