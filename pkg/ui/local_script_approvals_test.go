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

// The local-script approval hop (docs/local-scripts.md, "Approval"): a
// run's mcpserver posts an approve: true call, the task shows it, a
// person answers, and the answer reaches the waiting call and is
// recorded on the task. A run may not answer its own call.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/localexec"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func localScriptsClient(t *testing.T) (*ui.Client, *model.Store, context.Context) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	client := ui.NewClient(ui.Config{
		Actor: ui.DefaultActor("alice"), DefaultTarget: &repo, Capabilities: ui.OfferedCapabilities(),
	}, store)
	return client, store, ctx
}

func grantLocalScripts(t *testing.T, store *model.Store, ctx context.Context, id string) {
	t.Helper()
	if err := store.UpdateTask(ctx, id, func(task *model.Task) error {
		task.Grants = append(task.Grants, model.Grant{Capability: `https://grain.dev/capabilities/host-script#host-script={"script":"deploy"}`, Via: model.GrantByDefault})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type approvalRequest struct {
	ID       string    `json:"id"`
	Tool     string    `json:"tool"`
	Script   string    `json:"script"`
	Args     []string  `json:"args"`
	Stdin    int       `json:"stdin_bytes"`
	Deadline time.Time `json:"deadline"`
}

func TestLocalScriptApprovalIsAnsweredByAPersonAndRecorded(t *testing.T) {
	t.Parallel()
	client, store, ctx := localScriptsClient(t)
	task := create(t, client, ctx)
	grantLocalScripts(t, store, ctx, task.ID)
	handler := ui.NewServerWithClient(client)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// The run's own hop: no person on it.
	run := serverAccessClient(handler, srv.URL)
	approval, err := run.RequestLocalScriptApproval(ctx, task.ID, approvalRequest{
		ID: "1", Tool: "local_deploy", Script: "deploy", Args: []string{"main", "$(rm -rf /)"},
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("requesting approval: %v", err)
	}
	if approval.Status != ui.LocalScriptPending || approval.TaskID != task.ID {
		t.Fatalf("approval %+v: want pending on %s", approval, task.ID)
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.LocalScriptApprovals) != 1 || detail.LocalScriptApprovals[0].ID != approval.ID {
		t.Fatalf("task detail approvals %+v: want the pending call", detail.LocalScriptApprovals)
	}

	// A run's principal may not answer.
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/local-script-approvals/"+approval.ID+"/answer",
		strings.NewReader(`{"approve":true}`))
	req = req.WithContext(ui.WithPrincipal(req.Context(), model.Principal{Kind: model.PrincipalAgent, ID: task.ID}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a run answering its own local-script call got %d %s, want 403", rec.Code, rec.Body.String())
	}

	person := serverAccessClient(handler, srv.URL)
	waited := make(chan ui.LocalScriptApproval, 1)
	go func() {
		a, err := run.WaitLocalScriptApproval(ctx, task.ID, approval.ID)
		if err != nil {
			t.Errorf("waiting: %v", err)
		}
		waited <- a
	}()
	answered, err := person.AnswerLocalScriptApproval(ctx, task.ID, approval.ID, true)
	if err != nil {
		t.Fatalf("answering: %v", err)
	}
	if answered.Status != ui.LocalScriptApproved {
		t.Fatalf("answered %+v: want approved", answered)
	}
	select {
	case a := <-waited:
		if a.Status != ui.LocalScriptApproved {
			t.Fatalf("the waiting call saw %+v: want approved", a)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting call never saw the answer")
	}
	if _, err := person.AnswerLocalScriptApproval(ctx, task.ID, approval.ID, false); err == nil {
		t.Fatal("an answered call was answered again")
	}
	detail, err = client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := detail.Comments[len(detail.Comments)-1].Body
	if !strings.Contains(last, "Approved local script call 1") || !strings.Contains(last, `"$(rm -rf /)"`) {
		t.Fatalf("last comment %q: want the answer recorded with the call's arguments", last)
	}
}

// GET /api/config says how many local-script calls wait on a person, so
// the sidebar's Inbox entry can count them on every page without polling
// the approvals list itself; an answered call stops counting (task 1808).
func TestConfigCountsPendingLocalScriptCalls(t *testing.T) {
	t.Parallel()
	client, store, ctx := localScriptsClient(t)
	task := create(t, client, ctx)
	grantLocalScripts(t, store, ctx, task.ID)
	handler := ui.NewServerWithClient(client)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	run := serverAccessClient(handler, srv.URL)

	pending := func() int {
		t.Helper()
		rec := doWithHeaders(t, handler, http.MethodGet, "/api/config", "",
			map[string]string{"Authorization": "Bearer " + testAccessCode})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/config: %d %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			PendingLocalScripts int `json:"pendingLocalScripts"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.PendingLocalScripts
	}

	if n := pending(); n != 0 {
		t.Fatalf("pendingLocalScripts before any call = %d, want 0", n)
	}
	var ids []string
	for _, id := range []string{"1", "2"} {
		a, err := run.RequestLocalScriptApproval(ctx, task.ID, approvalRequest{
			ID: id, Tool: "local_deploy", Script: "deploy", Deadline: time.Now().Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("requesting approval: %v", err)
		}
		ids = append(ids, a.ID)
	}
	if n := pending(); n != 2 {
		t.Fatalf("pendingLocalScripts with two calls waiting = %d, want 2", n)
	}
	if _, err := run.AnswerLocalScriptApproval(ctx, task.ID, ids[0], false); err != nil {
		t.Fatalf("answering: %v", err)
	}
	if n := pending(); n != 1 {
		t.Fatalf("pendingLocalScripts after one was answered = %d, want 1", n)
	}
}

// The approval carries the call's stdin preview and hash, as
// localexec.Request sends them, to the task and to the recorded answer;
// a preview longer than the bound is cut here too.
func TestLocalScriptApprovalShowsAStdinPreview(t *testing.T) {
	t.Parallel()
	client, store, ctx := localScriptsClient(t)
	task := create(t, client, ctx)
	grantLocalScripts(t, store, ctx, task.ID)
	handler := ui.NewServerWithClient(client)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	run := serverAccessClient(handler, srv.URL)
	preview := "```\nrm -rf /\n" + strings.Repeat("a", 5000)
	approval, err := run.RequestLocalScriptApproval(ctx, task.ID, localexec.Request{
		ID: "1", Tool: "local_deploy", Script: "deploy", Args: []string{},
		Stdin: 6000, StdinPreview: preview, StdinSHA256: "abc123",
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("requesting approval: %v", err)
	}
	if approval.StdinBytes != 6000 || approval.StdinSHA256 != "abc123" || !approval.StdinTruncated ||
		approval.StdinPreview != preview[:localexec.StdinPreviewMax] {
		t.Fatalf("approval stdin %d bytes, sha256 %q, truncated %v, preview %d bytes",
			approval.StdinBytes, approval.StdinSHA256, approval.StdinTruncated, len(approval.StdinPreview))
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.LocalScriptApprovals) != 1 || detail.LocalScriptApprovals[0].StdinPreview != approval.StdinPreview {
		t.Fatalf("task detail approvals %+v: want the stdin preview", detail.LocalScriptApprovals)
	}
	person := serverAccessClient(handler, srv.URL)
	if _, err := person.AnswerLocalScriptApproval(ctx, task.ID, approval.ID, false); err != nil {
		t.Fatalf("answering: %v", err)
	}
	detail, err = client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := detail.Comments[len(detail.Comments)-1].Body
	for _, want := range []string{"sha256 `abc123`", "first 4096 of 6000 bytes shown", "````\n```\nrm -rf /\n"} {
		if !strings.Contains(last, want) {
			t.Errorf("last comment %q: want %q", last, want)
		}
	}
}

func TestLocalScriptApprovalRefusedWithoutTheCapability(t *testing.T) {
	t.Parallel()
	client, _, ctx := localScriptsClient(t)
	task := create(t, client, ctx)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)
	_, err := accessCodeClient(client, srv.URL).RequestLocalScriptApproval(ctx, task.ID, approvalRequest{
		ID: "1", Tool: "local_deploy", Script: "deploy",
	})
	if err == nil || !strings.Contains(err.Error(), "holds no host-script grant") {
		t.Fatalf("err %v: want 403 for a task holding no local-scripts capability", err)
	}
}

func TestHostScriptGrantAndScopedCallbackToken(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	approvals := &ui.LocalScriptApprovals{}
	client := ui.NewClient(ui.Config{
		Actor:                ui.DefaultActor("alice"),
		DefaultTarget:        &repo,
		Capabilities:         ui.OfferedCapabilities(),
		LocalAPIToken:        "daemon-secret-token",
		LocalScriptApprovals: approvals,
	}, store)
	task := create(t, client, ctx)
	other := create(t, client, ctx)
	for _, id := range []string{task.ID, other.ID} {
		if err := store.UpdateTask(ctx, id, func(tk *model.Task) error {
			tk.Grants = append(tk.Grants, model.Grant{
				Capability: `https://grain.dev/capabilities/host-script#host-script={"script":"ops-deploy"}`,
				Via:        model.GrantByDefault,
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	handler := ui.NewServerWithClient(client)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cbTok, err := approvals.MintToken(task.ID, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cbTok, "ghsa_") {
		t.Fatalf("callback token = %q, want ghsa_ prefix", cbTok)
	}

	// End-to-end HTTPApprover using only the scoped ghsa_ token (while LocalAPIToken is set).
	person := serverAccessClient(handler, srv.URL)
	approver := localexec.HTTPApprover{BaseURL: srv.URL, TaskID: task.ID, Token: cbTok}
	done := make(chan error, 1)
	go func() {
		req := localexec.Request{
			ID: "hs-1", Tool: "grain host-script ops-deploy", Script: "ops-deploy",
			Args: []string{"prod"}, Deadline: time.Now().Add(time.Minute),
		}
		req.SetStdin([]byte("manifest: ok\n"))
		ok, err := approver.Approve(ctx, req)
		if err != nil {
			done <- err
			return
		}
		if !ok {
			done <- localexec.ErrDenied
			return
		}
		done <- nil
	}()

	// Wait for the approval to appear on the task, verify the callback token cannot answer it or access other routes/tasks.
	var approvalID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		detail, err := client.GetTask(ctx, task.ID)
		if err == nil && len(detail.LocalScriptApprovals) == 1 {
			approvalID = detail.LocalScriptApprovals[0].ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if approvalID == "" {
		t.Fatal("host-script approval never appeared on task")
	}

	cbHdr := map[string]string{"Authorization": "Bearer " + cbTok, "Content-Type": "application/json"}
	// Answering with the callback token must be 403/401 (never allowed).
	if rec := doWithHeaders(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/local-script-approvals/"+approvalID+"/answer", `{"approve":true}`, cbHdr); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Errorf("callback token answering own approval got %d %s, want 401/403", rec.Code, rec.Body.String())
	}
	// Posting an approval for another task with task.ID's token must be rejected.
	if rec := doWithHeaders(t, handler, http.MethodPost, "/api/tasks/"+other.ID+"/local-script-approvals", `{"id":"x","tool":"t","script":"s"}`, cbHdr); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Errorf("callback token posting to another task got %d %s, want 401/403", rec.Code, rec.Body.String())
	}
	// General API routes remain closed to the callback token.
	if rec := doWithHeaders(t, handler, http.MethodGet, "/api/config", "", cbHdr); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Errorf("callback token reading /api/config got %d %s, want 401/403", rec.Code, rec.Body.String())
	}

	// Person approves -> HTTPApprover returns ok.
	if _, err := person.AnswerLocalScriptApproval(ctx, task.ID, approvalID, true); err != nil {
		t.Fatalf("person answering: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("HTTPApprover = %v, want nil", err)
	}

	// Revoked token is immediately rejected, and cancelled Approve expires the pending call.
	cbTok2, err := approvals.MintToken(task.ID, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	approver2 := localexec.HTTPApprover{BaseURL: srv.URL, TaskID: task.ID, Token: cbTok2}
	done2 := make(chan error, 1)
	go func() {
		_, err := approver2.Approve(cancelCtx, localexec.Request{
			ID: "hs-2", Tool: "grain host-script ops-deploy", Script: "ops-deploy",
			Deadline: time.Now().Add(time.Minute),
		})
		done2 <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		detail, _ := client.GetTask(ctx, task.ID)
		if len(detail.LocalScriptApprovals) == 2 && detail.LocalScriptApprovals[1].Status == ui.LocalScriptPending {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done2
	// The client returns as soon as its context is cancelled; the server
	// notices the dropped request and expires the call on its own
	// goroutine, so wait for that rather than racing it.
	var detail ui.TaskDetail
	for deadline := time.Now().Add(5 * time.Second); ; {
		detail, err = client.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.LocalScriptApprovals) == 2 && detail.LocalScriptApprovals[1].Status == ui.LocalScriptExpired ||
			!time.Now().Before(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(detail.LocalScriptApprovals) != 2 || detail.LocalScriptApprovals[1].Status != ui.LocalScriptExpired {
		t.Errorf("cancelled HTTPApprover left approval %+v, want expired", detail.LocalScriptApprovals)
	}

	approvals.RevokeToken(cbTok2)
	if _, err := approver2.Approve(ctx, localexec.Request{ID: "hs-3", Tool: "t", Script: "s", Deadline: time.Now().Add(time.Minute)}); err == nil {
		t.Errorf("revoked callback token was accepted")
	}
}
