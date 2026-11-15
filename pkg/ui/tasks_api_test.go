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
	"bytes"
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

func TestTaskSummariesMatchesSingleTaskClampsLimitsAndValidatesIDs(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	t1, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "first", NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "second", NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if _, err := client.Store.AddComment(ctx, model.Comment{
			TaskID:    t1.ID,
			Body:      fmt.Sprintf("comment-%d", i),
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			CreatedAt: at.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n <= 3; n++ {
		runID := t1.ID + "-" + strconv.Itoa(n)
		if err := client.Store.StartRun(ctx, model.Run{
			ID: runID, TaskID: t1.ID, Attempt: n, StartedAt: at.Add(time.Duration(n) * time.Hour),
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := client.Store.FinishRun(ctx, runID, at.Add(time.Duration(n)*time.Hour+time.Minute), "failed", ""); err != nil {
			t.Fatal(err)
		}
	}

	// Each entry matches GET /api/tasks/{id} JSON byte-for-byte with the same limits,
	// and duplicates are de-duplicated.
	for _, tc := range []struct {
		name        string
		batchQuery  string
		singleQuery string
	}{
		{
			name:        "bounded",
			batchQuery:  fmt.Sprintf("ids=%s,%s,%s&comments_limit=1&attempts_limit=1", t1.ID, t2.ID, t1.ID),
			singleQuery: "?comments_limit=1&attempts_limit=1",
		},
		{
			name:        "unbounded",
			batchQuery:  fmt.Sprintf("ids=%s,%s", t1.ID, t2.ID),
			singleQuery: "",
		},
		{
			name:        "clamped-to-500",
			batchQuery:  fmt.Sprintf("ids=%s,%s&comments_limit=999&attempts_limit=999", t1.ID, t2.ID),
			singleQuery: "?comments_limit=500&attempts_limit=500",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := serve(t, srv, http.MethodGet, "/api/tasks/summaries?"+tc.batchQuery, "")
			if code != http.StatusOK {
				t.Fatalf("GET /api/tasks/summaries?%s = %d %s", tc.batchQuery, code, body)
			}
			var got struct {
				Tasks map[string]json.RawMessage `json:"tasks"`
			}
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("decode batch: %v", err)
			}
			if len(got.Tasks) != 2 {
				t.Fatalf("batch len = %d, want 2 (%s)", len(got.Tasks), body)
			}
			for _, id := range []string{t1.ID, t2.ID} {
				sCode, sBody := serve(t, srv, http.MethodGet, "/api/tasks/"+id+tc.singleQuery, "")
				if sCode != http.StatusOK {
					t.Fatalf("GET /api/tasks/%s%s = %d %s", id, tc.singleQuery, sCode, sBody)
				}
				if !bytes.Equal(bytes.TrimSpace(got.Tasks[id]), bytes.TrimSpace([]byte(sBody))) {
					t.Errorf("task %s JSON mismatch:\nbatch:  %s\nsingle: %s", id, got.Tasks[id], sBody)
				}
			}
		})
	}

	// 50 unique ids (even with duplicates bringing total items to 51) succeed.
	fifty := make([]string, 0, 51)
	for i := 1; i <= 50; i++ {
		fifty = append(fifty, strconv.Itoa(i))
	}
	fiftyWithDup := append(append([]string(nil), fifty...), t1.ID)
	if code, body := serve(t, srv, http.MethodGet, "/api/tasks/summaries?ids="+strings.Join(fiftyWithDup, ","), ""); code != http.StatusOK {
		t.Fatalf("50 unique ids with duplicate = %d %s, want 200", code, body)
	}

	// 51 unique ids return 400 with a clear message.
	fiftyOne := append(append([]string(nil), fifty...), "51")
	code, body := serve(t, srv, http.MethodGet, "/api/tasks/summaries?ids="+strings.Join(fiftyOne, ","), "")
	if code != http.StatusBadRequest || !strings.Contains(body, "50") {
		t.Errorf("51 unique ids = %d %s, want 400 naming 50", code, body)
	}

	// Missing, empty, and malformed ids or invalid limits return 400.
	for _, bad := range []string{
		"/api/tasks/summaries",
		"/api/tasks/summaries?ids=",
		"/api/tasks/summaries?ids=" + t1.ID + ",",
		"/api/tasks/summaries?ids=" + t1.ID + ",," + t2.ID,
		"/api/tasks/summaries?ids=" + t1.ID + ",bad/id",
		"/api/tasks/summaries?ids=../1",
		"/api/tasks/summaries?ids=bad%20id",
		"/api/tasks/summaries?ids=" + t1.ID + "&comments_limit=0",
		"/api/tasks/summaries?ids=" + t1.ID + "&comments_limit=-1",
		"/api/tasks/summaries?ids=" + t1.ID + "&attempts_limit=x",
	} {
		if c, b := serve(t, srv, http.MethodGet, bad, ""); c != http.StatusBadRequest {
			t.Errorf("GET %s = %d %s, want 400", bad, c, b)
		}
	}

	// ETag + If-None-Match -> 304, then invalidation on comment -> 200 with new ETag.
	req1 := httptest.NewRequest(http.MethodGet, "/api/tasks/summaries?ids="+t1.ID+"&comments_limit=1&attempts_limit=1", nil)
	rec1 := httptest.NewRecorder()
	srv.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first conditional summaries = %d %s", rec1.Code, rec1.Body.String())
	}
	etag1 := rec1.Header().Get("ETag")
	if etag1 == "" {
		t.Fatal("GET /api/tasks/summaries omitted ETag header")
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/tasks/summaries?ids="+t1.ID+"&comments_limit=1&attempts_limit=1", nil)
	req2.Header.Set("If-None-Match", etag1)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("conditional summaries poll = %d, want 304", rec2.Code)
	}
	if _, err := client.Store.AddComment(ctx, model.Comment{
		TaskID:    t1.ID,
		Body:      "newest",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		CreatedAt: at.Add(10 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	req3 := httptest.NewRequest(http.MethodGet, "/api/tasks/summaries?ids="+t1.ID+"&comments_limit=1&attempts_limit=1", nil)
	req3.Header.Set("If-None-Match", etag1)
	rec3 := httptest.NewRecorder()
	srv.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("conditional summaries after comment = %d, want 200", rec3.Code)
	}
	if etag3 := rec3.Header().Get("ETag"); etag3 == "" || etag3 == etag1 {
		t.Errorf("ETag after comment = %q (was %q), want distinct ETag", etag3, etag1)
	}
}

func TestTaskSummariesAuthorizationAndOmission(t *testing.T) {
	h := newRoleHarness(t)
	srv, client := h.srv, h.client
	enableExternalAgentAPI(t, client.Store)
	ctx := context.Background()

	widgets, _ := model.ParseRepo("acme/widgets")
	secret, _ := model.ParseRepo("acme/secret")
	for _, tk := range []model.Task{
		{ID: "200", Intent: model.IntentImplement, Title: "Widgets summary", Target: &widgets},
		{ID: "201", Intent: model.IntentImplement, Title: "Secret summary", Target: &secret},
		{ID: "202", Intent: model.IntentImplement, Title: "No-repo summary"},
	} {
		if err := client.Store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}

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

	h.allow(t, "user:admin@example.com", "grain/operator")
	h.allow(t, "user:limited@example.com", "plc/widgets-read")
	h.allow(t, "user:ci-only@example.com", "plc/ci-only")

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

	seedAPITask(t, client, "caller-widgets", "acme/widgets", "api:tasks:read@acme/widgets")
	seedAPITask(t, client, "caller-ci-only", "acme/widgets", "api:ci:read@acme/widgets")

	callExt := func(tok, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "198.51.100.9:4242"
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	batchPath := "/api/tasks/summaries?ids=200,201,202,999999&comments_limit=1&attempts_limit=1"
	foreignOnlyPath := "/api/tasks/summaries?ids=201&comments_limit=1&attempts_limit=1"
	missingOnlyPath := "/api/tasks/summaries?ids=999999&comments_limit=1&attempts_limit=1"

	// Callers lacking tasks:read are refused 403.
	if rec := doWithHeaders(t, srv, http.MethodGet, batchPath, "", h.as("ci-only@example.com")); rec.Code != http.StatusForbidden {
		t.Errorf("human without tasks:read = %d, want 403", rec.Code)
	}
	if code, _ := asAgent(t, srv, "caller-ci-only", http.MethodGet, batchPath, "", ""); code != http.StatusForbidden {
		t.Errorf("agent without tasks:read = %d, want 403", code)
	}
	if rec := callExt(noTasksTok.Token, batchPath); rec.Code != http.StatusForbidden {
		t.Errorf("gma_ without tasks:read = %d, want 403", rec.Code)
	}

	// Repo-limited callers (human role, acting agent, gma_ token) receive only 200;
	// foreign-repo (201), repo-less (202), and nonexistent (999999) are silently
	// omitted and indistinguishable from each other.
	type callerFn func(path string) (int, string)
	callers := map[string]callerFn{
		"limited-human": func(path string) (int, string) {
			rec := doWithHeaders(t, srv, http.MethodGet, path, "", h.as("limited@example.com"))
			return rec.Code, rec.Body.String()
		},
		"acting-agent": func(path string) (int, string) {
			return asAgent(t, srv, "caller-widgets", http.MethodGet, path, "", "")
		},
		"external-token": func(path string) (int, string) {
			rec := callExt(widgetsTok.Token, path)
			return rec.Code, rec.Body.String()
		},
	}
	for name, call := range callers {
		t.Run(name, func(t *testing.T) {
			code, body := call(batchPath)
			if code != http.StatusOK {
				t.Fatalf("GET %s = %d %s, want 200", batchPath, code, body)
			}
			var resp struct {
				Tasks map[string]ui.TaskDetail `json:"tasks"`
			}
			if err := json.Unmarshal([]byte(body), &resp); err != nil {
				t.Fatalf("decode: %v (%s)", err, body)
			}
			if len(resp.Tasks) != 1 {
				t.Fatalf("tasks = %v, want only 200", resp.Tasks)
			}
			if _, ok := resp.Tasks["200"]; !ok {
				t.Fatalf("missing allowed task 200 in %v", resp.Tasks)
			}
			if strings.Contains(body, "Secret summary") || strings.Contains(body, "No-repo summary") {
				t.Errorf("response leaked restricted task: %s", body)
			}
			fCode, fBody := call(foreignOnlyPath)
			mCode, mBody := call(missingOnlyPath)
			if fCode != http.StatusOK || mCode != http.StatusOK || fBody != mBody {
				t.Errorf("foreign vs missing probe diverged: foreign=(%d %q) missing=(%d %q)",
					fCode, fBody, mCode, mBody)
			}
		})
	}
}
