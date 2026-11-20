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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func seedObservedPRTaskForBench(tb testing.TB) (*ui.Server, *ui.Client, *shadowForge, string) {
	tb.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(tb.TempDir()))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		tb.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	yes := true
	success := "success"
	forge := &shadowForge{
		detail: github.PullRequestDetail{
			Number:    9000,
			HeadSHA:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			HeadRef:   "grain/task-2",
			BaseRef:   "main",
			State:     "open",
			Mergeable: &yes,
		},
		base:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		complete: true,
		checks: []github.CheckRun{
			{ID: 1, CheckSuiteID: 1, Name: "go", App: "github-actions", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "completed", Conclusion: &success},
			{ID: 2, CheckSuiteID: 1, Name: "ui", App: "github-actions", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "completed", Conclusion: &success},
		},
		workflows: []github.WorkflowRun{
			{ID: 10, CheckSuiteID: 1, Path: ".github/workflows/tests.yml", Event: "pull_request", HeadBranch: "grain/task-2", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RunAttempt: 1, Status: "completed", Conclusion: "success"},
		},
	}
	c := ui.NewClient(ui.Config{
		Actor:                  ui.DefaultActor("alice"),
		DefaultTarget:          &repo,
		GitHubPullRequests:     forge,
		GateObservationTaskIDs: []string{"2"},
	}, store)
	now := baseTime.Add(time.Minute)
	c.Now = func() time.Time { return now }

	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, Declarations: true}); err != nil {
		tb.Fatal(err)
	}
	task := model.Task{
		ID:        "2",
		Intent:    model.IntentImplement,
		Title:     "Bench allowlisted PR task with linked reviews",
		Target:    &repo,
		Base:      "main",
		Links:     []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#9000"}, {Kind: model.LinkTargetedReviewTrigger, Target: "api"}},
		CreatedAt: &baseTime,
	}
	for i := 3; i <= 12; i++ {
		rid := strconv.Itoa(i)
		if err := store.PutTask(ctx, model.Task{
			ID:        rid,
			Intent:    model.IntentReview,
			Title:     fmt.Sprintf("Review %d", i),
			Target:    &repo,
			CreatedAt: &baseTime,
		}); err != nil {
			tb.Fatal(err)
		}
		if i%3 == 0 {
			task.Links = append(task.Links, model.Link{Kind: model.LinkSecurityReviewTask, Target: rid})
			model.AddReviewFindings(&task, model.ReviewFindingsRecord{Gate: model.GateSecurityReview, ReviewTask: rid, HeadSHA: "head1"})
		} else {
			task.Links = append(task.Links, model.Link{Kind: model.LinkReviewTask, Target: rid})
			model.AddReviewFindings(&task, model.ReviewFindingsRecord{Gate: "trigger:api", ReviewTask: rid, HeadSHA: "head1"})
		}
	}
	if err := store.PutTask(ctx, task); err != nil {
		tb.Fatal(err)
	}
	srv := ui.NewServerWithClient(c)
	// Two warm-up reads: first writes the shadow row, second settles ChangeToken
	// and populates both taskDetailCache and gateObsCache.
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/api/tasks/2", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			tb.Fatalf("warmup GET /api/tasks/2: %d %s", rec.Code, rec.Body.String())
		}
	}
	return srv, c, forge, task.ID
}

// BenchmarkGateObservationCachedPRRead guards steady-state cache-eligible
// GET /api/tasks/{id} latency on an allowlisted PR task with 10 linked review
// tasks (the shape measured in Finding L2 of the 2026-11-18 walkthrough).
func BenchmarkGateObservationCachedPRRead(b *testing.B) {
	b.Run("allowlist-set", func(b *testing.B) {
		srv, _, forge, id := seedObservedPRTaskForBench(b)
		readsBefore := forge.reads
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+id, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("GET /api/tasks/%s = %d", id, rec.Code)
			}
		}
		b.StopTimer()
		if forge.reads != readsBefore {
			b.Fatalf("steady-state cached reads made %d forge calls, want 0", forge.reads-readsBefore)
		}
	})
	b.Run("allowlist-set-304", func(b *testing.B) {
		srv, _, forge, id := seedObservedPRTaskForBench(b)
		req0 := httptest.NewRequest(http.MethodGet, "/api/tasks/"+id, nil)
		rec0 := httptest.NewRecorder()
		srv.ServeHTTP(rec0, req0)
		etag := rec0.Header().Get("ETag")
		if rec0.Code != http.StatusOK || etag == "" {
			b.Fatalf("prime ETag: %d %q", rec0.Code, etag)
		}
		readsBefore := forge.reads
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+id, nil)
			req.Header.Set("If-None-Match", etag)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotModified {
				b.Fatalf("GET /api/tasks/%s = %d, want 304", id, rec.Code)
			}
		}
		b.StopTimer()
		if forge.reads != readsBefore {
			b.Fatalf("steady-state 304 reads made %d forge calls, want 0", forge.reads-readsBefore)
		}
	})
	b.Run("allowlist-empty", func(b *testing.B) {
		srv, c, _, id := seedObservedPRTaskForBench(b)
		c.Config.GateObservationTaskIDs = nil
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+id, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("GET /api/tasks/%s = %d", id, rec.Code)
			}
		}
	})
}
