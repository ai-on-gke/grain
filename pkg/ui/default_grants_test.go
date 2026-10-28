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

// Capability server method grants and agent API grants as defaults: the
// deployment's own default capabilities, a repository's task and CI
// defaults, and a console session's set are all edited with the UI's
// GrantsPicker, so each of their writes has to take a method grant (with
// its with: arguments) and an api:* grant, and record a method grant in
// the normalized form a task's own grant is recorded in.

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	defaultGrantIn       = grantTestCap + `#key={"account":"staging"}`
	defaultGrantRecorded = grantTestCap + `#key={"account":"staging","scopes":["all"]}`
	defaultGrantInvalid  = grantTestCap + `#key={"account":"qa"}`
)

func TestDeploymentDefaultCapabilitiesTakeMethodAndAPIGrants(t *testing.T) {
	ts, _ := grantTestServer(t)
	var out map[string]any
	code := postJSON(t, ts, http.MethodPut, "/api/settings", map[string]any{
		"githubHost":          "github.com",
		"defaultCapabilities": []string{defaultGrantIn, "api:tasks:read", defaultGrantRecorded},
	}, &out)
	if code != http.StatusOK {
		t.Fatalf("PUT /api/settings: %d %v", code, out)
	}
	got := stringsOf(out["defaultCapabilities"])
	if !slices.Equal(got, []string{defaultGrantRecorded, "api:tasks:read"}) {
		t.Fatalf("defaultCapabilities = %v", got)
	}
	if code := postJSON(t, ts, http.MethodPut, "/api/settings", map[string]any{
		"defaultCapabilities": []string{defaultGrantInvalid},
	}, nil); code != http.StatusBadRequest {
		t.Fatalf("a method grant failing its schema was saved: %d", code)
	}

	// GET /api/config hands them to the new-task form, which seeds its
	// own GrantsPicker from them (defaultCapabilitiesFor).
	var cfg map[string]any
	if code := postJSON(t, ts, http.MethodGet, "/api/config", nil, &cfg); code != http.StatusOK {
		t.Fatalf("GET /api/config: %d", code)
	}
	if got := stringsOf(cfg["defaultCapabilities"]); !slices.Equal(got, []string{defaultGrantRecorded, "api:tasks:read"}) {
		t.Fatalf("/api/config defaultCapabilities = %v", got)
	}

	// A task filed with no capabilities of its own starts with them.
	var task map[string]any
	if code := postJSON(t, ts, http.MethodPost, "/api/tasks", map[string]any{"title": "seeded"}, &task); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("POST /api/tasks: %d %v", code, task)
	}
	if caps := stringsOf(task["capabilities"]); !slices.Contains(caps, defaultGrantRecorded) || !slices.Contains(caps, "api:tasks:read") {
		t.Fatalf("task capabilities = %v", caps)
	}
}

func TestRepoDefaultAndCICapabilitiesTakeMethodGrants(t *testing.T) {
	ts, c := grantTestServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := c.Store.PutRepoConfig(context.Background(), model.RepoConfig{Repo: repo, SetupCommand: "make deps"}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	code := postJSON(t, ts, http.MethodPut, "/api/repos/acme/widgets/capabilities", map[string]any{
		"defaultCapabilities": []string{defaultGrantIn, "api:tasks:read"},
	}, &out)
	if code != http.StatusOK {
		t.Fatalf("PUT capabilities: %d %v", code, out)
	}
	if got := stringsOf(out["defaultCapabilities"]); !slices.Equal(got, []string{defaultGrantRecorded, "api:tasks:read"}) {
		t.Fatalf("defaultCapabilities = %v", got)
	}

	out = nil
	code = postJSON(t, ts, http.MethodPut, "/api/repos/acme/widgets/ci-capabilities", map[string]any{
		"ciCapabilities": []string{"github-sandbox", defaultGrantIn, defaultGrantRecorded},
	}, &out)
	if code != http.StatusOK {
		t.Fatalf("PUT ci-capabilities: %d %v", code, out)
	}
	if got := stringsOf(out["ciCapabilities"]); !slices.Equal(got, []string{"github-sandbox", defaultGrantRecorded}) {
		t.Fatalf("ciCapabilities = %v", got)
	}
	// The task defaults are left as they were.
	if got := stringsOf(out["defaultCapabilities"]); !slices.Equal(got, []string{defaultGrantRecorded, "api:tasks:read"}) {
		t.Fatalf("defaultCapabilities after the CI write = %v", got)
	}
	for _, bad := range []string{defaultGrantInvalid, grantTestCap + "#key=not-json"} {
		if code := postJSON(t, ts, http.MethodPut, "/api/repos/acme/widgets/ci-capabilities", map[string]any{
			"ciCapabilities": []string{bad},
		}, nil); code != http.StatusBadRequest {
			t.Fatalf("CI grant %s accepted: %d", bad, code)
		}
	}
}

func TestConsoleSessionCapabilitiesTakeMethodGrants(t *testing.T) {
	ts, _ := grantTestServer(t)
	var created map[string]any
	code := postJSON(t, ts, http.MethodPost, "/api/console-sessions", map[string]any{
		"id":           "grants",
		"title":        "Grants",
		"capabilities": []string{defaultGrantIn},
	}, &created)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("POST /api/console-sessions: %d %v", code, created)
	}
	var updated map[string]any
	code = postJSON(t, ts, http.MethodPut, "/api/console-sessions/grants", map[string]any{
		"capabilities": []string{"api:tasks:read", defaultGrantIn, defaultGrantRecorded},
	}, &updated)
	if code != http.StatusOK {
		t.Fatalf("PUT /api/console-sessions/grants: %d %v", code, updated)
	}
	var got []string
	sessions, _ := updated["sessions"].([]any)
	for _, s := range sessions {
		if s, _ := s.(map[string]any); s["id"] == "grants" {
			got = stringsOf(s["capabilities"])
		}
	}
	if !slices.Equal(got, []string{"api:tasks:read", defaultGrantRecorded}) {
		t.Fatalf("capabilities = %v", got)
	}
	if code := postJSON(t, ts, http.MethodPut, "/api/console-sessions/grants", map[string]any{
		"capabilities": []string{defaultGrantInvalid},
	}, nil); code != http.StatusBadRequest {
		t.Fatalf("a method grant failing its schema was saved: %d", code)
	}
}

func stringsOf(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}
