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
	"strconv"
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

// GET /api/tasks/{id}?comments_limit=&comments_before=&attempts_limit=
// pages a long record from its newest end (durable-tasks.design.md#DT-15):
// the newest comments and runs, how many older ones were left out, and a
// comment cursor for the page before.
func TestGetTaskPagesItsCommentsAndRuns(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	tk, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "long", NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := client.Store.AddComment(ctx, model.Comment{TaskID: tk.ID, Body: string(rune('a' + i)),
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			CreatedAt: at.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n <= 3; n++ {
		runID := tk.ID + "-" + strconv.Itoa(n)
		if err := client.Store.StartRun(ctx, model.Run{ID: runID, TaskID: tk.ID, Attempt: n,
			StartedAt: at.Add(time.Duration(n) * time.Hour)}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := client.Store.FinishRun(ctx, runID, at.Add(time.Duration(n)*time.Hour+time.Minute), "failed", ""); err != nil {
			t.Fatal(err)
		}
	}
	page := func(query string) ui.TaskDetail {
		t.Helper()
		code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+tk.ID+"?"+query, "")
		if code != http.StatusOK {
			t.Fatalf("GET ?%s: %d %s", query, code, body)
		}
		var detail ui.TaskDetail
		if err := json.Unmarshal([]byte(body), &detail); err != nil {
			t.Fatal(err)
		}
		return detail
	}
	bodies := func(d ui.TaskDetail) string {
		var out []string
		for _, c := range d.Comments {
			out = append(out, c.Body)
		}
		return strings.Join(out, ",")
	}

	newest := page("comments_limit=2&attempts_limit=1")
	if got := bodies(newest); got != "d,e" || newest.OlderComments != 3 {
		t.Errorf("newest page = %q with %d older, want d,e with 3 older", got, newest.OlderComments)
	}
	if len(newest.Attempts) != 1 || newest.Attempts[0].Number != 3 || newest.OlderAttempts != 2 {
		t.Errorf("newest runs = %+v with %d older, want run 3 with 2 older", newest.Attempts, newest.OlderAttempts)
	}
	before := page("comments_limit=2&comments_before=" + strconv.FormatInt(newest.Comments[0].ID, 10))
	if got := bodies(before); got != "b,c" || before.OlderComments != 1 {
		t.Errorf("page before = %q with %d older, want b,c with 1 older", got, before.OlderComments)
	}
	if len(before.Attempts) != 3 {
		t.Errorf("a page asking for no run limit answered %d runs, want all 3", len(before.Attempts))
	}
	first := page("comments_limit=2&comments_before=" + strconv.FormatInt(before.Comments[0].ID, 10))
	if got := bodies(first); got != "a" || first.OlderComments != 0 {
		t.Errorf("first page = %q with %d older, want a alone with none older", got, first.OlderComments)
	}
	if whole := page(""); bodies(whole) != "a,b,c,d,e" || whole.OlderComments != 0 {
		t.Errorf("unpaged read = %q, want the whole conversation", bodies(whole))
	}
	for _, bad := range []string{"comments_limit=0", "comments_limit=501", "attempts_limit=x", "comments_before=-1"} {
		if code, _ := serve(t, srv, http.MethodGet, "/api/tasks/"+tk.ID+"?"+bad, ""); code != http.StatusBadRequest {
			t.Errorf("?%s answered %d, want 400", bad, code)
		}
	}
}
