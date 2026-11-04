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

package cli

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// `grain health` against a real server: a layer's registered counters
// are its text and its -json, and are gone once it unregisters them.
func TestGrainHealthShowsRegisteredCountersUntilUnregistered(t *testing.T) {
	const code = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
	store, _ := sqlitetest.Open(t)
	srv := ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator@example.com"),
		LocalAPIToken: code,
	}, store)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	const name = "cli test verification"
	unregister := ui.RegisterHealthCounters(name, func() map[string]int64 {
		return map[string]int64{"verifier:admitted": 12, "verifier:host_mismatch": 2}
	})
	t.Cleanup(unregister)

	out, err := runAccessCLI(t, ts.URL, code, "health")
	if err != nil {
		t.Fatalf("grain health: %v", err)
	}
	for _, want := range []string{"counters since the daemon started", name, "verifier:admitted", "12", "verifier:host_mismatch"} {
		if !strings.Contains(out, want) {
			t.Errorf("grain health does not mention %q:\n%s", want, out)
		}
	}
	// Sorted by key, so the same counter is always on the same line.
	if strings.Index(out, "verifier:admitted") > strings.Index(out, "verifier:host_mismatch") {
		t.Errorf("counters are not sorted by key:\n%s", out)
	}

	out, err = runAccessCLI(t, ts.URL, code, "-json", "health")
	if err != nil {
		t.Fatalf("grain -json health: %v", err)
	}
	var got ui.SandboxHealthResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("grain -json health is not JSON: %v\n%s", err, out)
	}
	var found bool
	for _, s := range got.Counters {
		if s.Name == name {
			found = s.Counters["verifier:admitted"] == 12 && s.Counters["verifier:host_mismatch"] == 2
		}
	}
	if !found {
		t.Errorf("grain -json health counters = %+v, want %q with admitted 12, host_mismatch 2", got.Counters, name)
	}

	unregister()
	out, err = runAccessCLI(t, ts.URL, code, "health")
	if err != nil {
		t.Fatalf("grain health after unregister: %v", err)
	}
	if strings.Contains(out, name) {
		t.Errorf("grain health still shows %q after unregister:\n%s", name, out)
	}
}
