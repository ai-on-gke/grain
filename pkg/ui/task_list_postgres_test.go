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

// GET /api/tasks on a Postgres store the size of a real deployment's
// (grain task 5812): about six thousand tasks, nearly all of them
// closed. The SQLite store answers "has anything changed?" from PRAGMA
// data_version; this is the same route on the engine that has no such
// pragma, which is where it cost three seconds a poll.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// postgresListServer is a server over a Postgres store holding total
// tasks, open of them not closed. Each task carries a few reads, grants,
// links and tags, as a real one does, so a list that hydrated every
// task's child rows and not only the open ones' would cost what that
// costs (review 5882).
func postgresListServer(t testing.TB, total, open int) (*ui.Server, *model.Store) {
	t.Helper()
	store, _ := postgrestest.Open(t)
	if store == nil {
		t.Skip("no postgres")
	}
	ctx := context.Background()
	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= total; i++ {
		created := base.Add(time.Duration(i) * time.Minute)
		task := model.Task{
			ID:        fmt.Sprint(i),
			Title:     fmt.Sprintf("task number %d", i),
			Body:      "a description long enough to look like one a person wrote for a task of this kind",
			Target:    &repo,
			CreatedAt: &created,
			Reads: []model.RepoRef{
				{Owner: "acme", Name: "docs"},
				{Owner: "acme", Name: "gadgets"},
				{Owner: "acme", Name: "tools"},
			},
			Grants: []model.Grant{
				{Capability: "api:ci-read", Via: model.GrantByDefault},
				{Capability: "api:tasks:read", Via: model.GrantByLabel},
				{Capability: "network", Via: model.GrantByDefault},
			},
			Links: []model.Link{
				{Kind: model.LinkProposedBy, Target: fmt.Sprint(i + total)},
				{Kind: model.LinkFixes, Target: fmt.Sprintf("https://github.com/acme/widgets/pull/%d", i)},
			},
			Tags: []string{"backlog", fmt.Sprintf("batch-%d", i%10)},
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if i > open {
			closed := created.Add(time.Hour)
			if err := store.Observe(ctx, model.Observation{TaskID: task.ID, ClosedAt: &closed}); err != nil {
				t.Fatal(err)
			}
		}
	}
	client := ui.NewClient(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}, store)
	return ui.NewServerWithClient(client), store
}

func timedList(t testing.TB, srv *ui.Server, path, ifNoneMatch string) (*httptest.ResponseRecorder, time.Duration) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	start := time.Now()
	srv.ServeHTTP(rec, req)
	return rec, time.Since(start)
}

// listRow is what the live and full lists are compared on: the row and
// the child rows hydrateTasks hangs off it.
func listRow(task ui.Task) string {
	if len(task.Reads) != 3 || len(task.Capabilities) == 0 {
		return fmt.Sprintf("%s: reads %v, capabilities %v, want them hydrated", task.ID, task.Reads, task.Capabilities)
	}
	return fmt.Sprint(task.ID, ":", task.Title, ":", task.State, ":", task.Reads, ":", task.Capabilities)
}

func median(ds []time.Duration) time.Duration {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

// TestTaskListOnPostgresIsCheapAtSixThousandTasks pins the two numbers
// grain task 5812 asked for: a quiet poll is a 304 built from nothing,
// and the poll that does build costs what the open tasks cost, not what
// the whole history does.
func TestTaskListOnPostgresIsCheapAtSixThousandTasks(t *testing.T) {
	if testing.Short() && os.Getenv(postgrestest.RequireEnv) == "" {
		t.Skip("seeds six thousand tasks")
	}
	srv, store := postgresListServer(t, 6000, 60)

	first, cold := timedList(t, srv, "/api/tasks", "")
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", first.Code, first.Body)
	}
	if got := len(decode[[]ui.Task](t, first)); got != 60 {
		t.Fatalf("listed %d tasks, want the 60 open ones", got)
	}
	etag := first.Header().Get("ETag")
	t.Logf("cold build: %v", cold)

	builds := ui.TaskListBuilds(srv)
	var quiet []time.Duration
	for i := 0; i < 9; i++ {
		rec, took := timedList(t, srv, "/api/tasks", etag)
		if rec.Code != http.StatusNotModified {
			t.Fatalf("quiet poll %d status = %d, want 304", i, rec.Code)
		}
		quiet = append(quiet, took)
	}
	if after := ui.TaskListBuilds(srv); after != builds {
		t.Errorf("nine quiet polls built the list %d times, want 0", after-builds)
	}
	t.Logf("quiet poll p50: %v", median(quiet))

	// A write invalidates it, and the rebuild is proportional to the
	// open tasks.
	var rebuilds []time.Duration
	for i := 0; i < 5; i++ {
		ctx := context.Background()
		if err := store.UpdateTask(ctx, "1", func(task *model.Task) error {
			task.Title = fmt.Sprintf("renamed %d", i)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		rec, took := timedList(t, srv, "/api/tasks", etag)
		if rec.Code != http.StatusOK {
			t.Fatalf("poll after a write status = %d, want 200", rec.Code)
		}
		etag = rec.Header().Get("ETag")
		rebuilds = append(rebuilds, took)
	}
	t.Logf("rebuild after a write p50: %v", median(rebuilds))

	// A narrowed read that can match no closed task, after a write each
	// time so that nothing is answered from the cache.
	var narrowed []time.Duration
	for i := 0; i < 5; i++ {
		if err := store.UpdateTask(context.Background(), "2", func(task *model.Task) error {
			task.Title = fmt.Sprintf("renamed again %d", i)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		rec, took := timedList(t, srv, "/api/tasks?state=queued,proposed,running&limit=1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("state filter status = %d: %s", rec.Code, rec.Body)
		}
		if got := len(decode[[]ui.Task](t, rec)); got != 1 {
			t.Fatalf("state filter with limit=1 listed %d tasks, want 1", got)
		}
		narrowed = append(narrowed, took)
	}
	t.Logf("?state=...&limit=1 p50 after a write: %v", median(narrowed))

	// The live list read only the open tasks; it must still say exactly
	// what the whole list says about them.
	live, _ := timedList(t, srv, "/api/tasks", "")
	full, _ := timedList(t, srv, "/api/tasks?fields=full", "")
	var want []string
	for _, task := range decode[[]ui.Task](t, full) {
		if task.State != model.StateClosed {
			want = append(want, listRow(task))
		}
	}
	var got []string
	for _, task := range decode[[]ui.Task](t, live) {
		got = append(got, listRow(task))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("live list = %v\nwant the full list's open tasks %v", got, want)
	}

	// The bound is loose on purpose -- CI runners vary -- but an order of
	// magnitude under the 3s this was.
	for name, p50 := range map[string]time.Duration{"quiet": median(quiet), "rebuild": median(rebuilds), "state filter": median(narrowed)} {
		if p50 > 500*time.Millisecond {
			t.Errorf("%s poll p50 = %v at 6000 tasks, want well under a second", name, p50)
		}
	}
}
