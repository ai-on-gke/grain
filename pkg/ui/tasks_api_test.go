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
	"fmt"
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
	olderRuns := page("comments_limit=1&attempts_limit=2&attempts_before=3")
	if len(olderRuns.Attempts) != 2 || olderRuns.Attempts[0].Number != 1 || olderRuns.Attempts[1].Number != 2 || olderRuns.OlderAttempts != 0 {
		t.Errorf("attempts_before=3 = %+v (older %d), want runs 1 and 2 with 0 older", olderRuns.Attempts, olderRuns.OlderAttempts)
	}
	for _, bad := range []string{"comments_limit=0", "comments_limit=501", "attempts_limit=x", "comments_before=-1", "attempts_before=0"} {
		if code, _ := serve(t, srv, http.MethodGet, "/api/tasks/"+tk.ID+"?"+bad, ""); code != http.StatusBadRequest {
			t.Errorf("?%s answered %d, want 400", bad, code)
		}
	}
}

// TestGetTaskPagePreservesDerivedFieldsAndCachesHead verifies that a paged
// detail built from the store answers the same derived fields (pending
// question summary even when parked on an older comment, failure streak,
// bounded transitions, total token usage, compactedThrough) and answers 304
// on quiet head polls without building the unpaged whole detail.
func TestGetTaskPagePreservesDerivedFieldsAndCachesHead(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	tk, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "derived", Approved: true, NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	agentAttr := model.Attribution{
		Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
		OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: tk.ID},
	}
	qID, err := client.Store.AddComment(ctx, model.Comment{
		TaskID:    tk.ID,
		Author:    agentAttr,
		Summary:   "Keep the v1 flag?",
		Body:      "Full question body.",
		CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := client.Store.AddComment(ctx, model.Comment{
			TaskID:    tk.ID,
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			Body:      fmt.Sprintf("note-%d", i),
			CreatedAt: at.Add(time.Duration(i+1) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Store.CompactTask(ctx, tk.ID, "Compacted handover summary.", 0, agentAttr, "alice", at.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.ObserveField(ctx, tk.ID, at.Add(11*time.Minute), func(o *model.Observation) {
		o.PendingQuestionCommentID = &qID
	}); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 4; n++ {
		runID := fmt.Sprintf("%s-r%d", tk.ID, n)
		start := at.Add(time.Duration(n) * time.Hour)
		finish := start.Add(2 * time.Minute)
		if err := client.Store.StartRun(ctx, model.Run{ID: runID, TaskID: tk.ID, Sandbox: "sb", Attempt: n, StartedAt: start}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		outcome := "failed"
		if n <= 2 {
			outcome = "succeeded"
		}
		if err := client.Store.FinishRun(ctx, runID, finish, outcome, fmt.Sprintf("fail-%d", n)); err != nil {
			t.Fatal(err)
		}
		if err := client.Store.RecordRunUsage(ctx, model.RunUsage{
			RunID: runID, TaskID: tk.ID, InputTokens: 100, OutputTokens: 50, StartedAt: start, FinishedAt: finish,
		}); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+tk.ID+"?comments_limit=2&attempts_limit=2", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET paged: %d %s", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("paged head response returned no ETag")
	}
	var paged ui.TaskDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &paged); err != nil {
		t.Fatal(err)
	}
	if paged.QuestionSummary != "Keep the v1 flag?" || paged.QuestionCommentID != qID {
		t.Errorf("QuestionSummary = %q (id %d), want Keep the v1 flag? (%d)", paged.QuestionSummary, paged.QuestionCommentID, qID)
	}
	if paged.FailedAttempts != 2 || paged.LastFailureReason != "fail-4" {
		t.Errorf("FailedAttempts = %d (%q), want 2 (fail-4)", paged.FailedAttempts, paged.LastFailureReason)
	}
	if paged.CompactedThrough <= 0 {
		t.Errorf("CompactedThrough = %d, want > 0", paged.CompactedThrough)
	}
	if paged.Usage == nil || paged.Usage.Runs != 4 || paged.Usage.InputTokens != 400 {
		t.Errorf("Usage = %+v, want total across all 4 runs", paged.Usage)
	}
	// Transitions are bounded to the 2 runs on the page rather than carrying
	// running/requeue entries for runs 1 and 2.
	runningTransitions := 0
	for _, tr := range paged.Transitions {
		if tr.State == model.StateRunning {
			runningTransitions++
		}
	}
	if runningTransitions != 2 {
		t.Errorf("running transitions on 2-run page = %d, want 2", runningTransitions)
	}

	// Second poll with If-None-Match answers 304 without rebuilding.
	builds := ui.TaskDetailBuilds(srv)
	req2 := httptest.NewRequest(http.MethodGet, "/api/tasks/"+tk.ID+"?comments_limit=2&attempts_limit=2", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("conditional paged poll = %d, want 304", rec2.Code)
	}
	if got := ui.TaskDetailBuilds(srv); got != builds {
		t.Errorf("quiet paged poll rebuilt detail %d times, want 0", got-builds)
	}

	// Client.Task for an awaiting_reply task also resolves QuestionSummary by comment id.
	one, err := client.Task(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.QuestionSummary != "Keep the v1 flag?" {
		t.Errorf("Client.Task QuestionSummary = %q, want Keep the v1 flag?", one.QuestionSummary)
	}
}

func TestConfigETagAnd304AndVariesOnChange(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	grain := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := client.Store.PutRepoWorkflows(ctx, grain, []model.RepoWorkflow{{
		Repo: grain, Path: ".grain/workflows/ci.yml", Text: "on: [push]\n",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "ci check", Repo: grain.String()}); err != nil {
		t.Fatal(err)
	}

	req1 := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec1 := httptest.NewRecorder()
	srv.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first GET /api/config = %d %s", rec1.Code, rec1.Body.String())
	}
	etag1 := rec1.Header().Get("ETag")
	if etag1 == "" {
		t.Fatal("GET /api/config omitted ETag header")
	}
	if got := rec1.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	vary := strings.Join(rec1.Header().Values("Vary"), ", ")
	if !strings.Contains(vary, "Cookie") || !strings.Contains(vary, "Authorization") {
		t.Errorf("Vary = %q, want Cookie and Authorization", vary)
	}
	var cfgResp struct {
		Deployment struct {
			Repos []ui.TeamRepoAssociation `json:"repos"`
		} `json:"deployment"`
		PipelinePause *struct {
			Paused bool `json:"paused"`
		} `json:"pipelinePause"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &cfgResp); err != nil {
		t.Fatal(err)
	}
	if len(cfgResp.Deployment.Repos) != 1 || cfgResp.Deployment.Repos[0].CIMode != "Grain CI" {
		t.Errorf("deployment.repos = %+v, want acme/widgets with Grain CI", cfgResp.Deployment.Repos)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req2.Header.Set("If-None-Match", etag1)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("conditional GET /api/config = %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 body len = %d, want 0", rec2.Body.Len())
	}

	// Changing pipeline pause must invalidate the ETag and serve 200.
	if err := client.Store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: time.Now().UTC(),
		Reason:   "maintenance",
		By:       model.Principal{Kind: model.PrincipalHuman, ID: "alice"},
	}); err != nil {
		t.Fatal(err)
	}
	req3 := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req3.Header.Set("If-None-Match", etag1)
	rec3 := httptest.NewRecorder()
	srv.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("GET /api/config after pause = %d, want 200", rec3.Code)
	}
	if etag3 := rec3.Header().Get("ETag"); etag3 == "" || etag3 == etag1 {
		t.Errorf("ETag after pause = %q (was %q), want distinct ETag", etag3, etag1)
	}
}
