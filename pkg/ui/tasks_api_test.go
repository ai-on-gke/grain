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

// serve sends one request to srv as the local caller and answers its
// status and body.
func serve(t *testing.T, srv http.Handler, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// #XC-18: GET /api/tasks narrows by derived_from and filed_via, and GET
// /api/tasks/{id}?comments_since= answers only the later conversation.
func TestListDerivedFromAndFiledViaFiltersAndGetCommentsSince(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	derived, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "derived", NoRepo: true,
		DerivedFrom: []string{"https://github.com/acme/widgets/pull/42"}, Designs: []string{"design/spec/x/x.spec.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(derived.DerivedFrom) != 1 || derived.DerivedFrom[0] != "acme/widgets#42" {
		t.Fatalf("derivedFrom = %v, want the URL stored as acme/widgets#42", derived.DerivedFrom)
	}
	if len(derived.Designs) != 1 || derived.Designs[0] != "design/spec/x/x.spec.md" {
		t.Fatalf("designs = %v", derived.Designs)
	}
	byLine, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "by line", NoRepo: true,
		Description: "Some work.\n\nDerived from: acme/widgets#42"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "other", NoRepo: true,
		DerivedFrom: []string{"acme/widgets#7"}}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.UpdateTask(ctx, byLine.ID, func(tk *model.Task) error {
		tk.FiledVia = "jetski/1.2 session=abc123"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "bad", NoRepo: true, DerivedFrom: []string{"not a pr"}}); err == nil {
		t.Fatal("a derived-from that names no pull request was accepted")
	}

	list := func(query string) []string {
		t.Helper()
		code, body := serve(t, srv, http.MethodGet, "/api/tasks?"+query, "")
		if code != http.StatusOK {
			t.Fatalf("GET /api/tasks?%s: %d %s", query, code, body)
		}
		var tasks []ui.Task
		if err := json.Unmarshal([]byte(body), &tasks); err != nil {
			t.Fatal(err)
		}
		var titles []string
		for _, tk := range tasks {
			titles = append(titles, tk.Title)
		}
		return titles
	}
	if got := strings.Join(list("derived_from=acme/widgets%2342"), ","); got != "derived,by line" {
		t.Errorf("derived_from=acme/widgets#42 listed %q, want the linked task and the one whose body says so", got)
	}
	if got := strings.Join(list("derived_from=https://github.com/acme/widgets/pull/7"), ","); got != "other" {
		t.Errorf("derived_from=<url of #7> listed %q", got)
	}
	if got := strings.Join(list("filed_via=SESSION%3Dabc123"), ","); got != "by line" {
		t.Errorf("filed_via=SESSION=abc123 listed %q, want the case-insensitive match", got)
	}

	early := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	for i, body := range []string{"first", "second", "third"} {
		if _, err := client.Store.AddComment(ctx, model.Comment{TaskID: derived.ID, Body: body,
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			CreatedAt: early.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+derived.ID+"?comments_since="+early.Add(time.Minute).Format(time.RFC3339), "")
	if code != http.StatusOK {
		t.Fatalf("comments_since: %d %s", code, body)
	}
	var detail ui.TaskDetail
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, c := range detail.Comments {
		bodies = append(bodies, c.Body)
	}
	if strings.Join(bodies, ",") != "third" {
		t.Errorf("comments_since answered %v, want only the comment strictly after it", bodies)
	}
	if code, _ := serve(t, srv, http.MethodGet, "/api/tasks/"+derived.ID+"?comments_since=yesterday", ""); code != http.StatusBadRequest {
		t.Errorf("a comments_since that is no time answered %d, want 400", code)
	}
}
