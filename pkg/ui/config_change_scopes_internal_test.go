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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// roleHolding is a limited role holding exactly scopes.
func roleHolding(who string, scopes ...string) *accessRole {
	var grants []string
	for _, sc := range scopes {
		grants = append(grants, model.APIRef(sc))
	}
	return &accessRole{Who: who, set: model.APIGrantsOf(grants), grants: grants, deny: map[APIScope]bool{}}
}

// A person whose role holds settings:write but not capabilities:admin
// (insider profile A, task 3965) may not approve a config change that adds
// a host script or widens a scope set: approving applies it at once, a
// grant they could not make directly. A change confined to settings that
// confer nothing more they may still approve, and one holding the scopes
// the change confers may approve it.
func TestApprovingAConfigChangeNeedsTheScopesItConfers(t *testing.T) {
	settingsOnly := roleHolding("settings@example.com", "settings:write")
	capAdmin := roleHolding("caps@example.com", "settings:write", "capabilities:admin")
	for _, tc := range []struct {
		name   string
		role   *accessRole
		files  []string
		cols   []string
		refuse string
	}{
		{"host script in mcp_servers", settingsOnly, []string{"tables/grain_config.json"}, nil, "capabilities:admin"},
		// Task 3973: judged by the columns the change alters, where they
		// were recorded when it was proposed.
		{"mcp_servers column", settingsOnly, []string{"tables/grain_config.json"}, []string{"mcp_servers"}, "capabilities:admin"},
		{"max_workers column", settingsOnly, []string{"tables/grain_config.json"}, []string{"max_workers"}, ""},
		{"widened scope set", settingsOnly, []string{"tables/api_scope_set.json"}, nil, "capabilities:admin"},
		{"template grants", settingsOnly, []string{"settings/templates.yml"}, nil, "capabilities:admin"},
		{"extracted script", settingsOnly, []string{"scripts/setup.sh"}, nil, "capabilities:admin"},
		{"git credential", capAdmin, []string{"tables/git_credential.json"}, nil, "secrets:write"},
		{"standing sandbox secret", capAdmin, []string{"tables/standing_sandbox_secret.json"}, nil, "secrets:write"},
		{"capability admin adds a host script", capAdmin, []string{"tables/grain_config.json"}, nil, ""},
		{"plain setting", settingsOnly, []string{"tables/usage_settings.json"}, nil, ""},
		{"unlimited session", nil, []string{"tables/grain_config.json", "tables/git_credential.json"}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.role != nil {
				ctx = withAccessRole(ctx, tc.role)
			}
			err := configChangeScopeRefusal(ctx, model.PendingConfigChange{Files: tc.files, Columns: columnsOf(tc.files, tc.cols)})
			if tc.refuse == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var forbidden *ForbiddenError
			if !errors.As(err, &forbidden) || !strings.Contains(err.Error(), tc.refuse) {
				t.Fatalf("err = %v, want a forbidden error naming %s", err, tc.refuse)
			}
		})
	}
}

// columnsOf records cols as the columns a change to the first of files
// alters, or nothing when cols is nil.
func columnsOf(files, cols []string) map[string][]string {
	if cols == nil {
		return nil
	}
	return map[string][]string{files[0]: cols}
}

// Repointing grain/config at another remote hands every grant the config
// repository can make to whoever controls that remote (task 3965), so it
// is a route only an unlimited role reaches.
func TestRepointingTheConfigRepoNeedsAnUnlimitedRole(t *testing.T) {
	if sc := roleScopeOf("POST /api/state-repo/remote"); sc != "" {
		t.Fatalf("POST /api/state-repo/remote answers a role holding %s; want only an unlimited role", sc)
	}
}

// A limited role cannot Submit (or turn on auto-merge for) any change,
// a grain/config pull request adding a host script among them: Submit
// releases RequiresHuman holds such as trustboundary.StatePrivilege, so it
// stays an unlimited role's (task 3965, insider profile A).
func TestALimitedRoleCannotSubmitAConfigChange(t *testing.T) {
	s := NewServerWithClient(nil)
	role := roleHolding("settings@example.com", "tasks:read", "tasks:write", "settings:write")
	for _, tc := range []struct{ pattern, body string }{
		{"POST /api/tasks/{id}/submit", ``},
		{"PATCH /api/tasks/{id}", `{"autoMerge":true}`},
	} {
		reached := false
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tc.body))
		r = r.WithContext(withAccessRole(r.Context(), role))
		rec := httptest.NewRecorder()
		s.roleGate(tc.pattern, func(http.ResponseWriter, *http.Request) { reached = true })(rec, r)
		if reached || !strings.Contains(rec.Body.String(), "unlimited role") {
			t.Errorf("%s as a settings:write role: reached=%v (%d %s), want refused for want of an unlimited role",
				tc.pattern, reached, rec.Code, rec.Body)
		}
	}
}
