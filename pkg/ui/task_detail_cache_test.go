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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func detailWith(t *testing.T, srv *ui.Server, id, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+id, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestTaskDetailAnswers304ToAPollerThatAlreadyHasIt(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	created := decode[ui.Task](t, do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`))

	first := detailWith(t, srv, created.ID, "")
	if first.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("task detail came back with no ETag")
	}
	second := detailWith(t, srv, created.ID, etag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("status for current ETag = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 carried %d bytes, want none", second.Body.Len())
	}
}

func TestTaskDetailCacheKeepsSeveralOpenPanes(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	first := decode[ui.Task](t, do(t, srv, http.MethodPost, "/api/tasks", `{"title":"first","approved":true}`))
	second := decode[ui.Task](t, do(t, srv, http.MethodPost, "/api/tasks", `{"title":"second","approved":true}`))

	firstResponse := detailWith(t, srv, first.ID, "")
	secondResponse := detailWith(t, srv, second.ID, "")
	builds := ui.TaskDetailBuilds(srv)
	for _, tc := range []struct {
		id   string
		etag string
	}{{first.ID, firstResponse.Header().Get("ETag")}, {second.ID, secondResponse.Header().Get("ETag")}} {
		if rec := detailWith(t, srv, tc.id, tc.etag); rec.Code != http.StatusNotModified {
			t.Fatalf("cached detail %s status = %d, want 304", tc.id, rec.Code)
		}
	}
	if got := ui.TaskDetailBuilds(srv); got != builds {
		t.Fatalf("two quiet detail polls caused %d rebuilds, want none", got-builds)
	}
}

func TestTaskDetailRebuildsAfterAWrite(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	created := decode[ui.Task](t, do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`))
	first := detailWith(t, srv, created.ID, "")
	etag := first.Header().Get("ETag")

	do(t, srv, http.MethodPost, "/api/tasks/"+created.ID+"/comments",
		`{"body":"a new observation"}`)
	after := detailWith(t, srv, created.ID, etag)
	if after.Code != http.StatusOK {
		t.Fatalf("detail after write status = %d, want 200", after.Code)
	}
	if after.Header().Get("ETag") == etag {
		t.Fatal("detail ETag did not change after a comment")
	}
	detail := decode[ui.TaskDetail](t, after)
	if len(detail.Comments) != 1 || detail.Comments[0].Body != "a new observation" {
		t.Fatalf("detail comments = %+v, want the new comment", detail.Comments)
	}
}

func TestTaskDetailRebuildsWhenLocalScriptApprovalRequestedOrExpired(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	created := decode[ui.Task](t, do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`))
	if err := client.Store.UpdateTask(context.Background(), created.ID, func(task *model.Task) error {
		task.Grants = append(task.Grants, model.Grant{Capability: "local-scripts:ops", Via: model.GrantByDefault})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	first := detailWith(t, srv, created.ID, "")
	if first.Code != http.StatusOK {
		t.Fatalf("initial detail status = %d, want 200", first.Code)
	}
	etag := first.Header().Get("ETag")

	approval := decode[ui.LocalScriptApproval](t, do(t, srv, http.MethodPost,
		"/api/tasks/"+created.ID+"/local-script-approvals",
		`{"id":"call-1","tool":"deploy_check","script":"/usr/local/bin/deploy-check","args":["--verify"]}`))

	afterReq := detailWith(t, srv, created.ID, etag)
	if afterReq.Code != http.StatusOK {
		t.Fatalf("detail after local-script request status = %d, want 200", afterReq.Code)
	}
	if afterReq.Header().Get("ETag") == etag {
		t.Fatal("detail ETag did not change after local-script approval request")
	}
	detail := decode[ui.TaskDetail](t, afterReq)
	if len(detail.LocalScriptApprovals) != 1 || detail.LocalScriptApprovals[0].ID != approval.ID || detail.LocalScriptApprovals[0].Status != ui.LocalScriptPending {
		t.Fatalf("detail LocalScriptApprovals = %+v, want pending %s", detail.LocalScriptApprovals, approval.ID)
	}

	etag2 := afterReq.Header().Get("ETag")
	do(t, srv, http.MethodPost, "/api/tasks/"+created.ID+"/local-script-approvals/"+approval.ID+"/expire", `{}`)

	afterExp := detailWith(t, srv, created.ID, etag2)
	if afterExp.Code != http.StatusOK {
		t.Fatalf("detail after local-script expire status = %d, want 200", afterExp.Code)
	}
	detailExp := decode[ui.TaskDetail](t, afterExp)
	if len(detailExp.LocalScriptApprovals) != 1 || detailExp.LocalScriptApprovals[0].Status != ui.LocalScriptExpired {
		t.Fatalf("detail LocalScriptApprovals after expire = %+v, want expired", detailExp.LocalScriptApprovals)
	}
}
