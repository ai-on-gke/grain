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

package ui_test

import (
	"net/http"
	"testing"
)

// The Settings UI greys out exactly the fields of its bootstrap and
// operations sections that the API refuses while settings are locked
// (settings-mutability.spec.md D-1, D-2; ui/src/components/settings
// EngineSection, CachesSection, SandboxesSection, CredentialsSection).
// This pins the server half of that agreement field by field: if a field
// moves into or out of clearBootstrap, the UI's gating has to move with
// it.
func TestSettingsLockedRefusesExactlyTheFieldsTheUIGreysOut(t *testing.T) {
	t.Parallel()
	srv := stateRepoServer(t, &fakeStateRepo{})
	if rec := do(t, srv, http.MethodPut, "/api/settings", `{"settingsLocked":true}`); rec.Code >= 300 {
		t.Fatalf("locking settings: status %d: %s", rec.Code, rec.Body)
	}
	// Drawn editable while locked: Models, Sandboxes sizes and budget,
	// Engine's max workers.
	for _, body := range []string{
		`{"defaultModel":"claude-opus-5-5"}`,
		`{"maxWorkers":3}`,
		`{"sandboxCpus":3}`,
		`{"sandboxMemoryMb":6144}`,
		`{"sandboxDiskGb":30}`,
		`{"networkAccess":"none"}`,
		`{"sandboxCpuOvercommit":3}`,
		`{"sandboxDiskFloorShare":0.2}`,
		`{"sandboxImage":"example.com/img:v1"}`,
	} {
		if rec := do(t, srv, http.MethodPut, "/api/settings", body); rec.Code >= 300 {
			t.Errorf("bootstrap %s while locked: status %d: %s", body, rec.Code, rec.Body)
		}
	}
	// Drawn read-only while locked.
	for _, body := range []string{
		`{"pollInterval":"25s"}`,
		`{"maxMergesPerRepo":2}`,
		`{"mergeBatchSize":3}`,
		`{"mergeGatherWindow":"90s"}`,
		`{"packageCacheEnabled":true}`,
		`{"packageCacheSizeGb":12}`,
		`{"actionsCacheEnabled":true}`,
		`{"actionsCacheSizeGb":9}`,
		`{"sessionSandboxIdleTtl":"45m"}`,
		`{"githubHost":"github.example.com"}`,
		`{"gcpProject":"my-project-123"}`,
		`{"gcpServiceAccountEmail":"sa@proj.iam.gserviceaccount.com"}`,
	} {
		if rec := do(t, srv, http.MethodPut, "/api/settings", body); rec.Code != http.StatusConflict {
			t.Errorf("config-managed %s while locked: status %d, want 409: %s", body, rec.Code, rec.Body)
		}
	}
	if rec := do(t, srv, http.MethodPut, "/api/settings", `{"settingsLocked":false}`); rec.Code >= 300 {
		t.Fatalf("unlocking settings: status %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, srv, http.MethodPut, "/api/settings", `{"pollInterval":"25s"}`); rec.Code >= 300 {
		t.Fatalf("pollInterval after unlocking: status %d: %s", rec.Code, rec.Body)
	}
}
