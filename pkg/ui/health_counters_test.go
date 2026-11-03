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
	"sync/atomic"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func healthCounterSection(t *testing.T, sections []ui.HealthCounters, name string) (ui.HealthCounters, bool) {
	t.Helper()
	for _, s := range sections {
		if s.Name == name {
			return s, true
		}
	}
	return ui.HealthCounters{}, false
}

// A layer's counters ride on GET /api/sandboxes, which is what System →
// Health, `grain health` and read_deployment_health all read, and are
// read from the layer at the time of each request rather than once.
func TestRegisteredHealthCountersShowInSandboxHealthUntilUnregistered(t *testing.T) {
	t.Parallel()
	const name = "test sign-in verification"
	var admitted atomic.Int64
	admitted.Store(3)
	unregister := ui.RegisterHealthCounters(name, func() map[string]int64 {
		return map[string]int64{"verifier:admitted": admitted.Load(), "verifier:refused": 1}
	})
	t.Cleanup(unregister)

	// Both the unconfigured response and the configured one carry them:
	// counters are about the layer, not about the sandbox pool.
	for _, configured := range []bool{false, true} {
		srv, client := testServer(t)
		if configured {
			client.Config.Sandboxes = fakeSandboxHealth{}
		}
		rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("configured=%v: status = %d, want 200: %s", configured, rec.Code, rec.Body)
		}
		got := decode[ui.SandboxHealthResponse](t, rec)
		section, ok := healthCounterSection(t, got.Counters, name)
		if !ok {
			t.Fatalf("configured=%v: counters = %+v, want a %q section", configured, got.Counters, name)
		}
		if section.Counters["verifier:admitted"] != 3 || section.Counters["verifier:refused"] != 1 {
			t.Errorf("configured=%v: %q = %v, want admitted 3, refused 1", configured, name, section.Counters)
		}
	}

	admitted.Add(1)
	srv, _ := testServer(t)
	got := decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
	if section, _ := healthCounterSection(t, got.Counters, name); section.Counters["verifier:admitted"] != 4 {
		t.Errorf("after one more admission: %q = %v, want admitted 4", name, section.Counters)
	}

	// The section System → Health polls is the same counters, without
	// the sandbox sweep the rest of the response costs.
	sectionOnly := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/sandboxes?section=counters", ""))
	if _, ok := sectionOnly["enabled"]; ok {
		t.Errorf("?section=counters = %v, want only the counters", sectionOnly)
	}
	got = decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes?section=counters", ""))
	if section, _ := healthCounterSection(t, got.Counters, name); section.Counters["verifier:admitted"] != 4 {
		t.Errorf("?section=counters: %q = %v, want admitted 4", name, section.Counters)
	}

	unregister()
	unregister() // twice is harmless
	got = decode[ui.SandboxHealthResponse](t, do(t, srv, http.MethodGet, "/api/sandboxes", ""))
	if _, ok := healthCounterSection(t, got.Counters, name); ok {
		t.Errorf("after unregister: counters = %+v, want no %q section", got.Counters, name)
	}
}

// A source that panics costs the health read its own section, not the
// whole response.
func TestAPanickingHealthCounterSourceIsLeftOut(t *testing.T) {
	t.Parallel()
	t.Cleanup(ui.RegisterHealthCounters("test panicking counters", func() map[string]int64 { panic("boom") }))
	t.Cleanup(ui.RegisterHealthCounters("test quiet counters", func() map[string]int64 { return nil }))

	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.SandboxHealthResponse](t, rec)
	if _, ok := healthCounterSection(t, got.Counters, "test panicking counters"); ok {
		t.Errorf("counters = %+v, want the panicking source left out", got.Counters)
	}
	if quiet, ok := healthCounterSection(t, got.Counters, "test quiet counters"); !ok || quiet.Counters == nil {
		t.Errorf("counters = %+v, want the quiet source as an empty section", got.Counters)
	}
}
