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
		task.Grants = append(task.Grants, model.Grant{Capability: "local-scripts:ops", Via: model.GrantByDefault})
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
	if err == nil || !strings.Contains(err.Error(), "holds no local-scripts capability") {
		t.Fatalf("err %v: want 403 for a task holding no local-scripts capability", err)
	}
}
