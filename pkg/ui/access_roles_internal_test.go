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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestRoleGateCoversEveryRoute holds the role gate's classification of
// every route total, beside TestRouteScopesAreTotal: each registered route
// either needs a grantable scope of a person's role, or is internal and
// classified -- open to every admitted caller or needing a scope
// (humanRouteScopes), or only for a role holding every scope
// (operatorOnlyRoutes) -- so no route is reachable by a limited role by
// being forgotten (docs/design/access-rbac.md §5).
func TestRoleGateCoversEveryRoute(t *testing.T) {
	s := NewServerWithClient(nil)
	routes := map[string]bool{}
	for _, pattern := range s.Routes() {
		if pattern == "/" {
			continue // the SPA's static files: no data, every admitted caller
		}
		routes[pattern] = true
		human, classified := humanRouteScopes[pattern]
		if classified && operatorOnlyRoutes[pattern] {
			t.Errorf("route %q is both in humanRouteScopes and operatorOnlyRoutes", pattern)
		}
		if routeScopes[pattern] == scopeInternal && !classified && !operatorOnlyRoutes[pattern] {
			t.Errorf("internal route %q is in neither humanRouteScopes nor operatorOnlyRoutes", pattern)
		}
		if classified && human != scopeSignedIn {
			if _, ok := apiScopeInfo(human); !ok {
				t.Errorf("humanRouteScopes gives %q scope %q, which is not in the vocabulary", pattern, human)
			}
		}
		got := roleScopeOf(pattern)
		if got != "" && got != scopeSignedIn {
			if _, ok := apiScopeInfo(got); !ok {
				t.Errorf("route %q needs %q of a role, which is no grantable scope", pattern, got)
			}
		}
	}
	for pattern := range humanRouteScopes {
		if !routes[pattern] {
			t.Errorf("humanRouteScopes names %q, which Server does not register", pattern)
		}
	}
	for pattern := range operatorOnlyRoutes {
		if !routes[pattern] {
			t.Errorf("operatorOnlyRoutes names %q, which Server does not register", pattern)
		}
	}
}

// TestRoleGateFailsClosed: a route with no scope a limited role could
// hold is refused it, and grain/operator passes everything.
func TestRoleGateFailsClosed(t *testing.T) {
	s := NewServerWithClient(nil)
	viewer := resolveAccessRoles("v@example.com", []string{"grain/viewer"})
	operator := resolveAccessRoles("o@example.com", []string{model.DefaultAccessRole})
	if !operator.Full || viewer.Full {
		t.Fatalf("operator.Full=%v viewer.Full=%v", operator.Full, viewer.Full)
	}
	reached := false
	next := func(http.ResponseWriter, *http.Request) { reached = true }
	for _, pattern := range []string{"POST /api/tasks/{id}/config-change", "/staging/{name}/{path...}", "POST /api/tasks/{id}/activity", "no such route"} {
		for _, tc := range []struct {
			role *accessRole
			want bool
		}{{viewer, false}, {operator, true}} {
			reached = false
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			r = r.WithContext(withAccessRole(r.Context(), tc.role))
			rec := httptest.NewRecorder()
			s.roleGate(pattern, next)(rec, r)
			if reached != tc.want {
				t.Errorf("%s as %s: reached=%v (%d %s), want %v", pattern, tc.role.label(), reached, rec.Code, rec.Body, tc.want)
			}
		}
	}
	// Both MCP endpoints pass roleGate for signed-in roles and enforce
	// authorizeRoleCall per tool call inside their dispatchers.
	for _, pattern := range []string{"POST /api/mcp", "POST /mcp"} {
		for _, role := range []*accessRole{viewer, operator} {
			reached = false
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			r = r.WithContext(withAccessRole(r.Context(), role))
			rec := httptest.NewRecorder()
			s.roleGate(pattern, next)(rec, r)
			if !reached {
				t.Errorf("%s as %s: reached=false (%d %s), want true", pattern, role.label(), rec.Code, rec.Body)
			}
		}
	}
}
