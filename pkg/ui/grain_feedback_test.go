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
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestGrainFeedbackAgentRecordNotificationAndHumanTriage(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()

	seedAPITask(t, client, "1", "acme/widgets", "api:feedback")
	seedAPITask(t, client, "2", "acme/gadgets", "api:memory:read")

	body := `{"slug":"pr-open-draft-flag","title":"grain pr open lacks --draft","surface":"cli","body":"## Friction\nAgents waste turns passing --draft.\n\n## Suggested change\nAdd --draft to grain pr open.","repos":["acme/widgets"],"taskIds":["1"]}`

	// Task 2 lacks api:feedback and is refused.
	if code, _ := asSandboxAgent(t, srv, "2", "run-2", http.MethodPost, "/api/feedback", body, ""); code != http.StatusForbidden {
		t.Fatalf("task without api:feedback got %d, want 403", code)
	}

	// Task 1 holds api:feedback and creates the entry (201), raising one info notification.
	code, resp := asSandboxAgent(t, srv, "1", "run-1", http.MethodPost, "/api/feedback", body, "")
	if code != http.StatusCreated {
		t.Fatalf("first POST /api/feedback = %d %s, want 201", code, resp)
	}
	var created struct {
		Slug            string `json:"slug"`
		OccurrenceCount int    `json:"occurrenceCount"`
		Created         bool   `json:"created"`
	}
	if err := json.Unmarshal([]byte(resp), &created); err != nil {
		t.Fatalf("unmarshal created: %v", err)
	}
	if !created.Created || created.OccurrenceCount != 1 {
		t.Fatalf("created = %+v, want Created=true OccurrenceCount=1", created)
	}

	openNotifs, err := client.Store.ListNotifications(ctx, model.NotificationFilter{
		Open:   true,
		Family: model.FamilyGrainFeedback,
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(openNotifs) != 1 || openNotifs[0].Severity != model.NotificationInfo {
		t.Fatalf("open feedback notifications = %+v, want 1 info notification", openNotifs)
	}

	// Agents cannot triage or delete entries even with api:feedback.
	if code, _ := asSandboxAgent(t, srv, "1", "run-1", http.MethodPatch, "/api/feedback/pr-open-draft-flag", `{"status":"rejected"}`, ""); code != http.StatusForbidden {
		t.Fatalf("agent PATCH /api/feedback got %d, want 403", code)
	}
	if code, _ := asSandboxAgent(t, srv, "1", "run-1", http.MethodDelete, "/api/feedback/pr-open-draft-flag", "", "cleanup"); code != http.StatusForbidden {
		t.Fatalf("agent DELETE /api/feedback got %d, want 403", code)
	}

	// Second sighting bumps OccurrenceCount without raising a second notification.
	code, resp = asSandboxAgent(t, srv, "1", "run-1b", http.MethodPost, "/api/feedback", body, "")
	if code != http.StatusOK {
		t.Fatalf("second POST /api/feedback = %d %s, want 200", code, resp)
	}
	if !strings.Contains(resp, `"occurrenceCount":2`) || !strings.Contains(resp, `"created":false`) {
		t.Errorf("second response = %s, want occurrenceCount=2 created=false", resp)
	}

	// Human triages the item as filed via the Server mux, which also clears the open notification.
	rec := doWithHeaders(t, srv, http.MethodPatch, "/api/feedback/pr-open-draft-flag",
		`{"status":"filed","upstreamUrl":"https://github.com/ai-on-gke/grain/issues/2001"}`,
		map[string]string{"Content-Type": "application/json"})
	code, resp = rec.Code, rec.Body.String()
	if code != http.StatusOK {
		t.Fatalf("human PATCH /api/feedback = %d %s, want 200", code, resp)
	}
	var patched ui.GrainFeedback
	if err := json.Unmarshal([]byte(resp), &patched); err != nil {
		t.Fatalf("unmarshal patched: %v", err)
	}
	if patched.Status != "filed" || patched.UpstreamURL != "https://github.com/ai-on-gke/grain/issues/2001" {
		t.Errorf("patched = %+v", patched)
	}

	openAfterTriage, err := client.Store.ListNotifications(ctx, model.NotificationFilter{
		Open:   true,
		Family: model.FamilyGrainFeedback,
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("ListNotifications after triage: %v", err)
	}
	if len(openAfterTriage) != 0 {
		t.Errorf("expected first-seen notification cleared upon human triage, got %+v", openAfterTriage)
	}
}
