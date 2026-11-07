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
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestTaskOwnerAPIAndTimelineProposerAttribution(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Cleanup(ui.RegisterIdentityVerifier(&fakeHeaderVerifier{
		name: "fake", headers: []string{"X-Fake-Identity"}, nonComparable: func() {},
		verifyFn: parseSimpleSignedHeader,
	}))
	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:alice@example.com", "user:bob@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)
	ctx := context.Background()

	aliceHeaders := map[string]string{"X-Fake-Identity": "ok:alice@example.com"}
	bobHeaders := map[string]string{"X-Fake-Identity": "ok:bob@example.com"}

	// 1. Default owner from signed-in creator, and Proposed timeline shows alice@example.com.
	rec := serveFrom(srv, "203.0.113.10:1234", http.MethodPost, "/api/tasks",
		`{"title":"Alice root task","description":"Body","approved":true}`, aliceHeaders)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/tasks (alice): %d: %s", rec.Code, rec.Body)
	}
	var rootTask ui.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &rootTask); err != nil {
		t.Fatal(err)
	}
	if rootTask.Owner != "alice@example.com" {
		t.Fatalf("rootTask.Owner = %q, want alice@example.com", rootTask.Owner)
	}

	detail, err := client.GetTask(ctx, rootTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Transitions) == 0 || detail.Transitions[0].State != model.StateProposed {
		t.Fatalf("detail.Transitions = %+v, want proposed first", detail.Transitions)
	}
	if detail.Transitions[0].ActorLabel != "alice@example.com" {
		t.Fatalf("Proposed ActorLabel = %q, want alice@example.com", detail.Transitions[0].ActorLabel)
	}

	// 2. Explicit owner at creation (`owner` field).
	rec = serveFrom(srv, "203.0.113.10:1234", http.MethodPost, "/api/tasks",
		`{"title":"Explicitly Bob's","description":"Body","owner":"user:bob@example.com"}`, aliceHeaders)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/tasks (explicit owner): %d: %s", rec.Code, rec.Body)
	}
	var bobTask ui.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &bobTask); err != nil {
		t.Fatal(err)
	}
	if bobTask.Owner != "bob@example.com" {
		t.Fatalf("bobTask.Owner = %q, want bob@example.com", bobTask.Owner)
	}

	// 3. Derived task proposed by a run: inherits owner, records proposing task+run+attempt,
	// and renders "task #<id> (run #<attempt>) on behalf of <owner>" on timeline.
	now := time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)
	if err := client.Store.StartRun(ctx, model.Run{
		ID:        "run-root-1",
		TaskID:    rootTask.ID,
		Sandbox:   "sb-root-1",
		Attempt:   1,
		StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	storedRoot, err := client.Store.GetTask(ctx, rootTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedRoot.Grants = append(storedRoot.Grants,
		model.Grant{Capability: "api:tasks:write", Via: model.GrantByLabel},
	)
	if err := client.Store.PutTask(ctx, *storedRoot); err != nil {
		t.Fatal(err)
	}

	agentHeaders := map[string]string{
		"Authorization":     "Bearer " + testAccessCode,
		ui.ActingTaskHeader: rootTask.ID,
		ui.ActingRunHeader:  "run-root-1",
	}
	// Agent cannot set owner at creation.
	rec = serveFrom(srv, "127.0.0.1:4444", http.MethodPost, "/api/tasks",
		`{"title":"Bad agent owner","description":"Body","owner":"bob@example.com"}`, agentHeaders)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent setting owner on POST /api/tasks = %d (%s), want 403", rec.Code, rec.Body)
	}

	// Agent filing without owner inherits rootTask.Owner and records proposing task & run.
	rec = serveFrom(srv, "127.0.0.1:4444", http.MethodPost, "/api/tasks",
		`{"title":"Proposed follow-up","description":"Body"}`, agentHeaders)
	if rec.Code != http.StatusCreated {
		t.Fatalf("agent POST /api/tasks: %d: %s", rec.Code, rec.Body)
	}
	var childTask ui.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &childTask); err != nil {
		t.Fatal(err)
	}
	if childTask.Owner != "alice@example.com" {
		t.Fatalf("childTask.Owner = %q, want inherited alice@example.com", childTask.Owner)
	}
	childDetail, err := client.GetTask(ctx, childTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	propTr := childDetail.Transitions[0]
	if propTr.ProposingTaskID != rootTask.ID || propTr.ProposingRunID != "run-root-1" || propTr.ProposingRunAttempt != 1 || propTr.OnBehalfOfOwner != "alice@example.com" {
		t.Fatalf("child Proposed transition = %+v", propTr)
	}
	wantLabel := "task #" + rootTask.ID + " (run #1) on behalf of alice@example.com"
	if propTr.ActorLabel != wantLabel {
		t.Fatalf("child Proposed ActorLabel = %q, want %q", propTr.ActorLabel, wantLabel)
	}

	// Agent cannot transfer owner via PATCH either.
	rec = serveFrom(srv, "127.0.0.1:4444", http.MethodPatch, "/api/tasks/"+childTask.ID,
		`{"owner":"bob@example.com"}`, agentHeaders)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent PATCH owner = %d (%s), want 403", rec.Code, rec.Body)
	}

	// 4. Ownership transfer by a verified person records audit entry and appears in transitions.
	rec = serveFrom(srv, "203.0.113.10:1234", http.MethodPatch, "/api/tasks/"+rootTask.ID,
		`{"owner":"bob@example.com"}`, bobHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH owner by bob: %d: %s", rec.Code, rec.Body)
	}
	var transferred ui.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &transferred); err != nil {
		t.Fatal(err)
	}
	if transferred.Owner != "bob@example.com" {
		t.Fatalf("transferred.Owner = %q, want bob@example.com", transferred.Owner)
	}
	rootDetailAfter, err := client.GetTask(ctx, rootTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ownerStep *ui.Transition
	for i := range rootDetailAfter.Transitions {
		if rootDetailAfter.Transitions[i].State == model.TransitionOwnerChanged {
			ownerStep = &rootDetailAfter.Transitions[i]
		}
	}
	if ownerStep == nil {
		t.Fatalf("expected owner_changed step in transitions, got %+v", rootDetailAfter.Transitions)
	}
	if ownerStep.OldOwner != "alice@example.com" || ownerStep.NewOwner != "bob@example.com" || ownerStep.ActorLabel != "bob@example.com" {
		t.Fatalf("owner_changed step = %+v, want alice->bob by bob@example.com", *ownerStep)
	}

	// 5. Filtering GET /api/tasks?owner=bob@example.com and ?owner=me.
	rec = serveFrom(srv, "203.0.113.10:1234", http.MethodGet, "/api/tasks?owner=me", "", bobHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks?owner=me: %d: %s", rec.Code, rec.Body)
	}
	var bobList []ui.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &bobList); err != nil {
		t.Fatal(err)
	}
	if len(bobList) != 2 {
		t.Fatalf("GET /api/tasks?owner=me (bob) returned %d tasks, want 2 (bobTask + transferred rootTask)", len(bobList))
	}

	// 6. Automation-filed task Proposed transition shows automation principal ("schedule").
	schedTime := now.Add(5 * time.Minute)
	schedTask := model.Task{
		ID:        "9001",
		Intent:    model.IntentImplement,
		Title:     "Nightly scheduled task",
		Body:      "Run nightly check",
		Binding:   model.BindingDirective,
		CreatedAt: &schedTime,
		Origin: model.Origin{
			Attribution: model.Attribution{
				Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "schedule", Source: model.SourceInternal},
			},
			Reason: model.ReasonSchedule,
		},
	}
	if err := client.Store.PutTask(ctx, schedTask); err != nil {
		t.Fatal(err)
	}
	schedDetail, err := client.GetTask(ctx, "9001")
	if err != nil {
		t.Fatal(err)
	}
	if len(schedDetail.Transitions) == 0 || !strings.EqualFold(schedDetail.Transitions[0].ActorLabel, "schedule") {
		t.Fatalf("schedule task Proposed transition = %+v, want ActorLabel=schedule", schedDetail.Transitions)
	}
}
