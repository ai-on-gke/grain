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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestExternalRefCreateInheritFilterAndSummary(t *testing.T) {
	srv, client := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	hc := ui.NewHTTPClient(ts.URL)
	hc.Token = testAccessCode
	ctx := context.Background()

	parent, err := hc.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Parent capability task",
		Repo:        "acme/widgets",
		Approved:    true,
		ExternalRef: "CA-204",
	})
	if err != nil {
		t.Fatalf("CreateTask parent: %v", err)
	}
	if parent.ExternalRef != "CA-204" {
		t.Fatalf("parent.ExternalRef = %q, want CA-204", parent.ExternalRef)
	}

	// Spawned proposal inherits ExternalRef automatically.
	child, err := hc.CreateTask(ctx, ui.CreateTaskRequest{
		Title:      "Spawned follow-up",
		Repo:       "acme/widgets",
		ProposedBy: parent.ID,
	})
	if err != nil {
		t.Fatalf("CreateTask child: %v", err)
	}
	if child.ExternalRef != "CA-204" {
		t.Fatalf("child.ExternalRef = %q, want inherited CA-204", child.ExternalRef)
	}

	// Unrelated task with a different externalRef.
	other, err := hc.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Other capability",
		Repo:        "acme/gadgets",
		ExternalRef: "CA-999",
	})
	if err != nil {
		t.Fatalf("CreateTask other: %v", err)
	}

	// Park child on a question and mark parent's PR merged so the rollup has
	// rich counts.
	now := time.Date(2026, 11, 5, 5, 0, 0, 0, time.UTC)
	qid, err := client.Store.AddComment(ctx, model.Comment{
		TaskID:    child.ID,
		Summary:   "Use batch size 50 or 100?",
		Body:      "Clarify pagination page size.",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run-child"}},
		CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Store.Observe(ctx, model.Observation{
		TaskID:                   child.ID,
		PendingQuestionCommentID: &qid,
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.UpdateTask(ctx, parent.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#42"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.Observe(ctx, model.Observation{
		TaskID:     parent.ID,
		PrOpenedAt: &now,
		PrMergedAt: &now,
		ClosedAt:   &now,
	}); err != nil {
		t.Fatal(err)
	}

	// GET /api/tasks?externalRef=CA-204 filters accurately in both full and summary projections.
	for _, path := range []string{"/api/tasks?externalRef=CA-204", "/api/tasks?externalRef=CA-204&fields=summary"} {
		rec := doWithHeaders(t, srv, http.MethodGet, path, "", map[string]string{"Authorization": "Bearer " + testAccessCode})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		var list []ui.Task
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 {
			t.Fatalf("GET %s returned %d tasks, want 2", path, len(list))
		}
		for _, item := range list {
			if item.ExternalRef != "CA-204" {
				t.Errorf("GET %s item %s externalRef = %q", path, item.ID, item.ExternalRef)
			}
		}
	}

	// HTTPClient.RefSummary hits GET /api/refs/CA-204/summary.
	sum, err := hc.RefSummary(ctx, "CA-204", "")
	if err != nil {
		t.Fatalf("RefSummary: %v", err)
	}
	if sum.ExternalRef != "CA-204" || sum.Total != 2 {
		t.Fatalf("RefSummary = %+v, want ExternalRef=CA-204 Total=2", sum)
	}
	if sum.ByState[model.StateClosed] != 1 || sum.ByState[model.StateAwaitingReply] != 1 {
		t.Errorf("ByState = %+v", sum.ByState)
	}
	if sum.PullRequests != (ui.RefPullRequestCounts{Opened: 1, Merged: 1, Open: 0}) {
		t.Errorf("PullRequests = %+v, want {1 1 0}", sum.PullRequests)
	}
	if len(sum.OpenQuestions) != 1 || sum.OpenQuestions[0].TaskID != child.ID || sum.OpenQuestions[0].Summary != "Use batch size 50 or 100?" {
		t.Errorf("OpenQuestions = %+v", sum.OpenQuestions)
	}

	// Unknown externalRef returns an empty 200 rollup rather than 404 so
	// callers polling before tasks land do not treat "no tasks yet" as an error.
	emptySum, err := hc.RefSummary(ctx, "CA-NONE", "")
	if err != nil {
		t.Fatalf("RefSummary(CA-NONE): %v", err)
	}
	if emptySum.Total != 0 || len(emptySum.ByState) != 0 {
		t.Errorf("emptySum = %+v, want zero counts", emptySum)
	}

	// PATCH /api/tasks/{id} updates and clears externalRef.
	cleared := ""
	updated, err := hc.UpdateTask(ctx, other.ID, ui.UpdateTaskRequest{ExternalRef: &cleared})
	if err != nil {
		t.Fatalf("UpdateTask clear: %v", err)
	}
	if updated.ExternalRef != "" {
		t.Errorf("cleared ExternalRef = %q, want empty", updated.ExternalRef)
	}
}

func TestRefSummaryAndTaskFilterVisibility(t *testing.T) {
	h := newRoleHarness(t)
	srv, client := h.srv, h.client
	enableExternalAgentAPI(t, client.Store)
	ctx := context.Background()

	widgets, _ := model.ParseRepo("acme/widgets")
	secret, _ := model.ParseRepo("acme/secret")
	for _, tk := range []model.Task{
		{ID: "100", Intent: model.IntentImplement, Title: "Widgets task", Target: &widgets, ExternalRef: "CA-204"},
		{ID: "101", Intent: model.IntentImplement, Title: "Secret task", Target: &secret, ExternalRef: "CA-204"},
	} {
		if err := client.Store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	qid, _ := client.Store.AddComment(ctx, model.Comment{
		TaskID:    "101",
		Summary:   "Secret question",
		Body:      "Confidential details",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run"}},
		CreatedAt: time.Now(),
	})
	_ = client.Store.Observe(ctx, model.Observation{TaskID: "101", PendingQuestionCommentID: &qid})

	// Declare a repo-scoped scope set granting tasks:read on acme/widgets only.
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })
	if err := client.PutAPIScopeSet(ctx, model.APIScopeSet{
		Name:        "plc/widgets-read",
		Description: "Read tasks in acme/widgets only",
		Scopes:      []string{"tasks:read"},
		Repos:       []string{"acme/widgets"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.PutAPIScopeSet(ctx, model.APIScopeSet{
		Name:        "plc/ci-only",
		Description: "CI read without tasks:read",
		Scopes:      []string{"ci:read"},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Sandbox agent scoped to acme/widgets sees only w1 in both endpoints
	// and is refused ?repo=acme/secret.
	seedAPITask(t, client, "caller-widgets", "acme/widgets", "api:tasks:read@acme/widgets")
	code, body := asAgent(t, srv, "caller-widgets", http.MethodGet, "/api/refs/CA-204/summary", "", "")
	if code != http.StatusOK {
		t.Fatalf("sandbox agent summary = %d: %s", code, body)
	}
	var sum ui.RefSummary
	if err := json.Unmarshal([]byte(body), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Total != 1 || len(sum.OpenQuestions) != 0 {
		t.Errorf("sandbox agent summary leaked secret repo: %+v", sum)
	}
	code, _ = asAgent(t, srv, "caller-widgets", http.MethodGet, "/api/refs/CA-204/summary?repo=acme/secret", "", "")
	if code != http.StatusForbidden {
		t.Errorf("sandbox agent ?repo=acme/secret = %d, want 403", code)
	}
	code, listBody := asAgent(t, srv, "caller-widgets", http.MethodGet, "/api/tasks?externalRef=CA-204", "", "")
	if code != http.StatusOK || strings.Contains(listBody, "Secret task") || !strings.Contains(listBody, "Widgets task") {
		t.Errorf("sandbox agent filtered task list = %d %s", code, listBody)
	}

	// 2. External agent token without tasks:read is refused (403); repo-scoped
	// external token sees only acme/widgets.
	// Behind IAP a token works only while its owner is on the list.
	h.allow(t, "user:admin@example.com", "grain/operator")
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "admin@example.com"})
	noTasksTok, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name: "ci-bot", ScopeSet: "plc/ci-only", Owner: "admin@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	widgetsTok, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name: "plc-widgets", ScopeSet: "plc/widgets-read", Owner: "admin@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	callExt := func(tok, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "198.51.100.9:4242"
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	if rec := callExt(noTasksTok.Token, "/api/refs/CA-204/summary"); rec.Code != http.StatusForbidden {
		t.Errorf("external agent without tasks:read = %d, want 403", rec.Code)
	}
	rec := callExt(widgetsTok.Token, "/api/refs/CA-204/summary")
	if rec.Code != http.StatusOK {
		t.Fatalf("external agent widgets summary = %d: %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sum)
	if sum.Total != 1 || len(sum.OpenQuestions) != 0 {
		t.Errorf("external agent summary leaked secret repo: %+v", sum)
	}
	if rec := callExt(widgetsTok.Token, "/api/refs/CA-204/summary?repo=acme/secret"); rec.Code != http.StatusForbidden {
		t.Errorf("external agent ?repo=acme/secret = %d, want 403", rec.Code)
	}

	// 3. Verified human with a repo-limited role sees only acme/widgets.
	h.allow(t, "user:limited@example.com", "plc/widgets-read")
	hRec := doWithHeaders(t, srv, http.MethodGet, "/api/refs/CA-204/summary", "", h.as("limited@example.com"))
	if hRec.Code != http.StatusOK {
		t.Fatalf("limited human summary = %d: %s", hRec.Code, hRec.Body.String())
	}
	_ = json.Unmarshal(hRec.Body.Bytes(), &sum)
	if sum.Total != 1 || len(sum.OpenQuestions) != 0 {
		t.Errorf("limited human summary leaked secret repo: %+v", sum)
	}
	if hRec := doWithHeaders(t, srv, http.MethodGet, "/api/refs/CA-204/summary?repo=acme/secret", "", h.as("limited@example.com")); hRec.Code != http.StatusForbidden {
		t.Errorf("limited human ?repo=acme/secret = %d, want 403", hRec.Code)
	}

	// 4. Write-only agent creating a task with origin=task:101, "task 101", or
	// reverts=101 gets a redacted response and does not stamp 101's externalRef
	// onto the new task without tasks:read.
	seedAPITask(t, client, "writer-only", "acme/widgets", "api:tasks:write@acme/widgets")
	for _, payload := range []string{
		`{"repo":"acme/widgets","title":"probe-colon","origin":"task:101"}`,
		`{"repo":"acme/widgets","title":"probe-space","origin":"task 100"}`,
		`{"repo":"acme/widgets","title":"probe-revert","reverts":"100"}`,
	} {
		code, createResp := asAgent(t, srv, "writer-only", http.MethodPost, "/api/tasks", payload, "filing follow-up")
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("writer-only POST /api/tasks (%s) = %d: %s", payload, code, createResp)
		}
		if strings.Contains(createResp, "CA-204") {
			t.Errorf("writer-only POST /api/tasks (%s) leaked externalRef without tasks:read: %s", payload, createResp)
		}
		var stub struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(createResp), &stub); err != nil || stub.ID == "" {
			t.Fatalf("unmarshal created stub (%s): %v (%s)", payload, err, createResp)
		}
		if stored, err := client.Store.GetTask(ctx, stub.ID); err != nil || stored.ExternalRef != "" {
			t.Errorf("writer-only stored task %s ExternalRef = %q (err=%v), want empty", stub.ID, stored.ExternalRef, err)
		}
	}

	// 5. Cross-repo exfiltration check: an agent holding BOTH tasks:read and
	// tasks:write on acme/widgets (so its own repo's POST /api/tasks response is
	// unredacted) cannot copy acme/secret's externalRef via origin or reverts,
	// while still inheriting from a readable task in acme/widgets.
	if err := client.Store.PutTask(ctx, model.Task{
		ID: "102", Intent: model.IntentImplement, Title: "Secret exclusive", Target: &secret, ExternalRef: "SEC-ONLY",
	}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "rw-widgets", "acme/widgets", "api:tasks:read@acme/widgets", "api:tasks:write@acme/widgets")
	for _, payload := range []string{
		`{"repo":"acme/widgets","title":"xrepo-colon","origin":"task:102"}`,
		`{"repo":"acme/widgets","title":"xrepo-space","origin":"task 102"}`,
		`{"repo":"acme/widgets","title":"xrepo-revert","reverts":"102"}`,
	} {
		code, createResp := asAgent(t, srv, "rw-widgets", http.MethodPost, "/api/tasks", payload, "filing follow-up")
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("rw-widgets POST /api/tasks (%s) = %d: %s", payload, code, createResp)
		}
		if strings.Contains(createResp, "SEC-ONLY") {
			t.Errorf("rw-widgets POST /api/tasks (%s) leaked acme/secret externalRef: %s", payload, createResp)
		}
	}
	if secSum, err := client.RefSummary(ctx, "SEC-ONLY", nil); err != nil || secSum.Total != 1 {
		t.Errorf("SEC-ONLY summary = %+v (err=%v), want Total=1 (unpolluted by acme/widgets)", secSum, err)
	}
	code, sameRepoResp := asAgent(t, srv, "rw-widgets", http.MethodPost, "/api/tasks",
		`{"repo":"acme/widgets","title":"same-repo-origin","origin":"task:100"}`, "filing follow-up")
	if code != http.StatusCreated || !strings.Contains(sameRepoResp, `"externalRef":"CA-204"`) {
		t.Errorf("rw-widgets same-repo origin inheritance = %d: %s", code, sameRepoResp)
	}

	// 6. Limited-role human cannot pass daemon-internal proposedBy on POST /api/tasks
	// or inherit externalRef across unreadable repositories.
	if err := client.PutAPIScopeSet(ctx, model.APIScopeSet{
		Name:        "plc/widgets-rw",
		Description: "Read/write tasks in acme/widgets only",
		Scopes:      []string{"tasks:read", "tasks:write"},
		Repos:       []string{"acme/widgets"},
	}); err != nil {
		t.Fatal(err)
	}
	h.allow(t, "user:rwlimited@example.com", "plc/widgets-rw")
	if propRec := doWithHeaders(t, srv, http.MethodPost, "/api/tasks",
		`{"repo":"acme/widgets","title":"forged-prop","proposedBy":"102"}`, h.as("rwlimited@example.com")); propRec.Code != http.StatusForbidden {
		t.Errorf("signed-in user proposedBy = %d (%s), want 403", propRec.Code, propRec.Body.String())
	}
	xrepoHumanRec := doWithHeaders(t, srv, http.MethodPost, "/api/tasks",
		`{"repo":"acme/widgets","title":"human-xrepo","origin":"task:102","reverts":"102"}`, h.as("rwlimited@example.com"))
	if xrepoHumanRec.Code != http.StatusCreated || strings.Contains(xrepoHumanRec.Body.String(), "SEC-ONLY") {
		t.Errorf("limited human cross-repo origin = %d: %s", xrepoHumanRec.Code, xrepoHumanRec.Body.String())
	}
	var xrepoCreated ui.Task
	if err := json.Unmarshal(xrepoHumanRec.Body.Bytes(), &xrepoCreated); err != nil || xrepoCreated.ID == "" {
		t.Fatalf("unmarshal human-xrepo response: %v (%s)", err, xrepoHumanRec.Body.String())
	}
	// A daemon-spawned child of human-xrepo must not inherit acme/secret's
	// externalRef on a second hop either.
	spawnedFromBridge, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:      "daemon follow-up from human-xrepo",
		Repo:       "acme/widgets",
		ProposedBy: xrepoCreated.ID,
	})
	if err != nil {
		t.Fatalf("CreateTask spawnedFromBridge: %v", err)
	}
	if spawnedFromBridge.ExternalRef != "" {
		t.Errorf("spawnedFromBridge.ExternalRef = %q, want empty (no two-hop cross-repo leak)", spawnedFromBridge.ExternalRef)
	}

	// 7. Repeated ?repo=&repo=acme/secret and empty ?externalRef= query parameter
	// guards, plus multi-repo GET /api/tasks?externalRef=CA-204 visibility.
	for _, badPath := range []string{
		"/api/refs/CA-204/summary?repo=&repo=acme/secret",
		"/api/tasks?externalRef=CA-204&repo=&repo=acme/secret",
	} {
		if c, _ := asAgent(t, srv, "caller-widgets", http.MethodGet, badPath, "", ""); c != http.StatusForbidden {
			t.Errorf("sandbox agent %s = %d, want 403", badPath, c)
		}
		if r := callExt(widgetsTok.Token, badPath); r.Code != http.StatusForbidden {
			t.Errorf("external agent %s = %d, want 403", badPath, r.Code)
		}
		if r := doWithHeaders(t, srv, http.MethodGet, badPath, "", h.as("limited@example.com")); r.Code != http.StatusForbidden {
			t.Errorf("limited human %s = %d, want 403", badPath, r.Code)
		}
	}

	gadgets, _ := model.ParseRepo("acme/gadgets")
	if err := client.Store.PutTask(ctx, model.Task{
		ID: "103", Intent: model.IntentImplement, Title: "Gadgets task", Target: &gadgets, ExternalRef: "CA-204",
	}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "caller-multi", "acme/widgets", "api:tasks:read@acme/widgets", "api:tasks:read@acme/gadgets")
	code, multiList := asAgent(t, srv, "caller-multi", http.MethodGet, "/api/tasks?externalRef=CA-204", "", "")
	if code != http.StatusOK || !strings.Contains(multiList, "Widgets task") || !strings.Contains(multiList, "Gadgets task") || strings.Contains(multiList, "Secret task") {
		t.Errorf("multi-repo sandbox agent filtered task list = %d: %s", code, multiList)
	}
	if err := client.PutAPIScopeSet(ctx, model.APIScopeSet{
		Name:        "plc/multi-read",
		Description: "Read tasks in widgets and gadgets",
		Scopes:      []string{"tasks:read"},
		Repos:       []string{"acme/widgets", "acme/gadgets"},
	}); err != nil {
		t.Fatal(err)
	}
	multiTok, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name: "plc-multi", ScopeSet: "plc/multi-read", Owner: "admin@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if emptyRefRec := callExt(multiTok.Token, "/api/tasks?externalRef="); emptyRefRec.Code != http.StatusForbidden {
		t.Errorf("multi-repo external agent empty ?externalRef= = %d, want 403", emptyRefRec.Code)
	}
	if dupRepoRec := doWithHeaders(t, srv, http.MethodGet, "/api/refs/CA-204/summary?repo=acme/widgets&repo=acme/gadgets", "", h.as("admin@example.com")); dupRepoRec.Code != http.StatusBadRequest {
		t.Errorf("operator repeated ?repo= on summary = %d, want 400", dupRepoRec.Code)
	}
	if dupExtRec := callExt(multiTok.Token, "/api/refs/CA-204/summary?repo=acme/widgets&repo=acme/gadgets"); dupExtRec.Code != http.StatusBadRequest {
		t.Errorf("multi-repo external agent repeated ?repo= on summary = %d, want 400", dupExtRec.Code)
	}
}
