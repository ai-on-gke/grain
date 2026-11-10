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
		refuse string
	}{
		{"host script in mcp_servers", settingsOnly, []string{"tables/grain_config.json"}, "capabilities:admin"},
		{"widened scope set", settingsOnly, []string{"tables/api_scope_set.json"}, "capabilities:admin"},
		{"template grants", settingsOnly, []string{"settings/templates.yml"}, "capabilities:admin"},
		{"extracted script", settingsOnly, []string{"scripts/setup.sh"}, "capabilities:admin"},
		{"git credential", capAdmin, []string{"tables/git_credential.json"}, "secrets:write"},
		{"standing sandbox secret", capAdmin, []string{"tables/standing_sandbox_secret.json"}, "secrets:write"},
		{"capability admin adds a host script", capAdmin, []string{"tables/grain_config.json"}, ""},
		{"plain setting", settingsOnly, []string{"tables/usage_settings.json"}, ""},
		{"unlimited session", nil, []string{"tables/grain_config.json", "tables/git_credential.json"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.role != nil {
				ctx = withAccessRole(ctx, tc.role)
			}
			err := configChangeScopeRefusal(ctx, model.PendingConfigChange{Files: tc.files})
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

// Repointing grain/config at another remote hands every grant the config
// repository can make to whoever controls that remote (task 3965), so it
// is a route only an unlimited role reaches.
func TestRepointingTheConfigRepoNeedsAnUnlimitedRole(t *testing.T) {
	if sc := roleScopeOf("POST /api/state-repo/remote"); sc != "" {
		t.Fatalf("POST /api/state-repo/remote answers a role holding %s; want only an unlimited role", sc)
	}
}
