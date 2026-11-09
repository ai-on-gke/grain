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

package ui

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestCapabilitiesDetachScopeIsNotDangerousButModelOnly(t *testing.T) {
	info, ok := model.APIScopeInfoOf("capabilities:detach")
	if !ok {
		t.Fatal("capabilities:detach is not in the scope vocabulary")
	}
	if info.Dangerous || model.IsDangerousAPIScope("capabilities:detach") {
		t.Error("capabilities:detach is dangerous; it must be grantable without a person (director-capabilities.spec.md §3.1.2)")
	}
	if !IsSandboxDeniedScope("capabilities:detach") {
		t.Error("capabilities:detach is reachable on the sandbox-cli channel; it must be MCP-only")
	}
	if model.SandboxAPIGrantsOf([]string{"api:capabilities:detach"}).Has("capabilities:detach") {
		t.Error("the sandbox credential carries capabilities:detach")
	}
}

func TestCapabilitiesDetachRoutesAreCapabilitiesAdminRoutes(t *testing.T) {
	for pattern := range capabilitiesDetachRoutes {
		if routeScopes[pattern] != "capabilities:admin" {
			t.Errorf("detach route %q has scope %q; capabilities:detach only narrows capabilities:admin routes", pattern, routeScopes[pattern])
		}
	}
	set := APIGrantsOf([]string{"api:capabilities:detach"})
	if !capabilitiesDetachAllowsRoute(set, "POST /api/tasks/{id}/capabilities") {
		t.Error("capabilities:detach does not reach POST /api/tasks/{id}/capabilities")
	}
	if capabilitiesDetachAllowsRoute(set, "PUT /api/capability-policy") {
		t.Error("capabilities:detach reaches PUT /api/capability-policy")
	}
	if capabilitiesDetachAllowsRoute(APIGrantsOf([]string{"api:capabilities:read"}), "POST /api/tasks/{id}/capabilities") {
		t.Error("capabilities:read reaches a detach route")
	}
}

func TestCapabilitiesDetachBodyAllowed(t *testing.T) {
	stored := map[string][]string{
		"defaultcapabilities": {"github-sandbox", "gcp-key"},
		"cicapabilities":      {"github-sandbox", "gcp-key"},
	}
	const (
		task  = "POST /api/tasks/{id}/capabilities"
		repo  = "PUT /api/repos/{owner}/{name}/capabilities"
		ci    = "PUT /api/repos/{owner}/{name}/ci-capabilities"
		other = "PUT /api/capability-policy"
	)
	for _, tc := range []struct {
		pattern, body string
		ok            bool
	}{
		{task, `{"grant":"self-debug","attach":false}`, true},
		{task, `{"id":"self-debug","attach":false}`, true},
		{task, `{"capability":"https://example.com/caps/x#y","with":{"a":"b"},"attach":false}`, true},
		{task, `{"grant":"self-debug","attach":true}`, false},
		{task, `{"grant":"self-debug"}`, false},
		{task, `{"grant":"self-debug","attach":false,"replace":"other"}`, false},
		{task, `{"grant":"self-debug","attach":false,"REPLACE":" "}`, false},
		{task, `{"attach":false}`, false},
		{task, `not json`, false},
		{task, ``, false},
		{repo, `{"defaultCapabilities":["github-sandbox"]}`, true},
		{repo, `{"defaultcapabilities":["gcp-key","github-sandbox"]}`, true},
		{repo, `{"defaultCapabilities":[]}`, true},
		{repo, `{"defaultCapabilities":["github-sandbox","self-debug"]}`, false},
		{repo, `{"defaultCapabilities":null}`, false},
		{repo, `{"defaultCapabilities":"github-sandbox"}`, false},
		{repo, `{"defaultCapabilities":["github-sandbox"],"other":1}`, false},
		{repo, `{"ciCapabilities":["github-sandbox"]}`, false},
		{repo, `{}`, false},
		{ci, `{"ciCapabilities":["gcp-key"]}`, true},
		{ci, `{"ciCapabilities":["gcp-key","package-publish"]}`, false},
		{ci, `{"defaultCapabilities":["gcp-key"]}`, false},
		{other, `{}`, false},
	} {
		err := capabilitiesDetachBodyAllowed(tc.pattern, []byte(tc.body), stored, nil)
		if (err == nil) != tc.ok {
			t.Errorf("capabilitiesDetachBodyAllowed(%s, %s) = %v, want ok=%v", tc.pattern, tc.body, err, tc.ok)
		}
	}
	// Nothing stored: only an empty list is a subset.
	if err := capabilitiesDetachBodyAllowed(repo, []byte(`{"defaultCapabilities":["github-sandbox"]}`), nil, nil); err == nil {
		t.Error("narrowing to a list the row does not hold was allowed with nothing stored")
	}
	if err := capabilitiesDetachBodyAllowed(repo, []byte(`{"defaultCapabilities":[]}`), nil, nil); err != nil {
		t.Errorf("an empty list with nothing stored was refused: %v", err)
	}
}
