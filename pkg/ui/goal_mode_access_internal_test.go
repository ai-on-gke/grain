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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestGoalModeRequiresTasksAdminForLimitedRolesAndAgents(t *testing.T) {
	s := NewServerWithClient(nil)
	writer := roleHolding("writer@example.com", "tasks:read", "tasks:write")
	for _, tc := range []struct{ method, pattern, body string }{
		{http.MethodPost, "POST /api/tasks", `{"title":"x","goalMode":{"enabled":true}}`},
		{http.MethodPatch, "PATCH /api/tasks/{id}", `{"goalMode":{"enabled":true}}`},
		{http.MethodPatch, "PATCH /api/tasks/{id}", `{"goalMode":{"enabled":false}}`},
	} {
		reached := false
		r := httptest.NewRequest(tc.method, "/x", strings.NewReader(tc.body))
		r = r.WithContext(withAccessRole(r.Context(), writer))
		rec := httptest.NewRecorder()
		s.roleGate(tc.pattern, func(http.ResponseWriter, *http.Request) { reached = true })(rec, r)
		if reached || rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tasks:admin") {
			t.Errorf("%s (%s) by tasks:write role: reached=%v code=%d body=%q, want 403 citing tasks:admin",
				tc.pattern, tc.body, reached, rec.Code, rec.Body.String())
		}
	}

	taskWithGoal := model.Task{ID: "901", Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}
	_ = model.SetGoalMode(&taskWithGoal, &model.GoalModeConfig{Enabled: true})
	err := s.tasks.roleTaskWithin(context.Background(), writer, taskWithGoal, "approve",
		func(sc APIScope, repo string) bool { return writer.allows(sc, repo) }, nil)
	if err == nil || !strings.Contains(err.Error(), "tasks:admin") {
		t.Fatalf("roleTaskWithin(writer, goalMode) = %v, want tasks:admin refusal", err)
	}

	admin := roleHolding("admin@example.com", "tasks:read", "tasks:write", "tasks:admin")
	if err := s.tasks.roleTaskWithin(context.Background(), admin, taskWithGoal, "approve",
		func(sc APIScope, repo string) bool { return admin.allows(sc, repo) }, nil); err != nil {
		t.Fatalf("roleTaskWithin(admin, goalMode) = %v, want nil", err)
	}

	// An active agent cannot patch goalMode on an existing task or enable it from a non-goal parent.
	supervisedParent := &model.Task{ID: "902", Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}
	set := model.APIGrantsOf([]string{model.APIRef("tasks:write")})
	allowAll := func(APIScope, string) bool { return true }
	if err := s.filingEscalation(context.Background(), nil, set, allowAll, "PATCH /api/tasks/{id}", "tasks:write", "acme/widgets",
		[]byte(`{"goalMode":{"enabled":false}}`), filingEscalationOpts{acting: supervisedParent}); err == nil {
		t.Fatal("expected agent PATCH goalMode to be refused")
	}
	if err := s.filingEscalation(context.Background(), nil, set, allowAll, "POST /api/tasks", "tasks:write", "acme/widgets",
		[]byte(`{"goalMode":{"enabled":true}}`), filingEscalationOpts{acting: supervisedParent}); err == nil {
		t.Fatal("expected supervised agent enabling goalMode to be refused")
	}
}
