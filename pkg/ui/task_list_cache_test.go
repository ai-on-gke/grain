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

// GET /api/tasks as the thing every open tab polls every three seconds:
// what it costs to be asked again, and -- much more important -- that
// being cheap never makes it wrong. Every test here is a property of the
// cache in front of that route (pkg/ui/task_list_cache.go).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// listWith is one GET /api/tasks carrying the If-None-Match a poller
// would send with the ETag it last saw ("" for a caller that has none).
func listWith(t *testing.T, srv *ui.Server, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestTaskListAnswers304ToAPollerThatAlreadyHasIt is the whole point of
// the change from the wire's side: the quiet tick, which is nearly every
// tick, sends no task list at all.
func TestTaskListAnswers304ToAPollerThatAlreadyHasIt(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`)

	first := listWith(t, srv, "")
	if first.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("the task list came back with no ETag for a poller to send back")
	}
	if first.Body.Len() == 0 {
		t.Fatal("the first answer carried no body")
	}

	second := listWith(t, srv, etag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("status for a poller holding the current ETag = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("a 304 carried %d bytes of body, want none", second.Body.Len())
	}
	if got := second.Header().Get("ETag"); got != etag {
		t.Errorf("ETag on the 304 = %q, want the same %q", got, etag)
	}
}

// TestTaskListDoesNotRebuildWhileNothingIsCommitted is the same tick
// from the store's side, and the reason ChangeToken exists: the poll
// that has nothing to say does not read nine hundred tasks to find that
// out.
func TestTaskListDoesNotRebuildWhileNothingIsCommitted(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`)

	before := listWith(t, srv, "")
	builds := ui.TaskListBuilds(srv)
	etag := before.Header().Get("ETag")

	for i := 0; i < 5; i++ {
		rec := listWith(t, srv, etag)
		if rec.Code != http.StatusNotModified {
			t.Fatalf("poll %d status = %d, want 304", i, rec.Code)
		}
	}
	// And a caller with no ETag at all -- a tab that has just loaded --
	// is served the cached bytes rather than a fresh read.
	if rec := listWith(t, srv, ""); rec.Code != http.StatusOK {
		t.Fatalf("status for a caller with no ETag = %d, want 200", rec.Code)
	}
	if after := ui.TaskListBuilds(srv); after != builds {
		t.Errorf("the store was read %d more times across six quiet polls, want 0", after-builds)
	}
}

// TestTaskListRebuildsAfterAWrite is the property everything else here
// is only allowed to be cheap because of. A stale task list is a UI that
// does not show what somebody just did, which is worse than a slow one.
func TestTaskListRebuildsAfterAWrite(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`)

	before := listWith(t, srv, "")
	etag := before.Header().Get("ETag")
	if listed := decode[[]ui.Task](t, before); len(listed) != 1 {
		t.Fatalf("listed %d tasks, want 1", len(listed))
	}

	do(t, srv, http.MethodPost, "/api/tasks", `{"title":"and this","approved":true}`)

	after := listWith(t, srv, etag)
	if after.Code != http.StatusOK {
		t.Fatalf("status after a write = %d, want 200 -- the poller was told nothing had changed", after.Code)
	}
	if got := after.Header().Get("ETag"); got == etag {
		t.Errorf("ETag after a write = %q, unchanged -- a poller would never fetch the new task", got)
	}
	if listed := decode[[]ui.Task](t, after); len(listed) != 2 {
		t.Fatalf("listed %d tasks after the second was created, want 2", len(listed))
	}
}

// TestTaskListRebuildsAfterAnEditThatChangesNoCount: a write that leaves
// the same tasks in the same order still has to reach the poller. This
// is the case a cache keyed on anything cheaper than "the database was
// written" -- a count, a max id -- would get wrong.
func TestTaskListRebuildsAfterAnEditThatChangesNoCount(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`)
	created := decode[ui.Task](t, rec)

	first := listWith(t, srv, "")
	etag := first.Header().Get("ETag")

	do(t, srv, http.MethodPatch, "/api/tasks/"+created.ID, `{"title":"fix it properly"}`)

	after := listWith(t, srv, etag)
	if after.Code != http.StatusOK {
		t.Fatalf("status after a retitle = %d, want 200", after.Code)
	}
	listed := decode[[]ui.Task](t, after)
	if len(listed) != 1 || listed[0].Title != "fix it properly" {
		t.Fatalf("listed %+v, want the retitled task", listed)
	}
}

// TestTaskListETagSurvivesADifferentServerOverTheSameStore: the
// validator is a hash of the bytes, not a counter somebody keeps, so a
// tab that has been open across a daemon restart is still told 304
// rather than handed a megabyte it already has. Two Servers over one
// store is what that restart looks like from a test.
func TestTaskListETagSurvivesADifferentServerOverTheSameStore(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	first, second := ui.NewServerWithClient(client), ui.NewServerWithClient(client)
	do(t, first, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`)

	etag := listWith(t, first, "").Header().Get("ETag")
	if rec := listWith(t, second, etag); rec.Code != http.StatusNotModified {
		t.Fatalf("status from a second server holding the same store = %d, want 304", rec.Code)
	}
}

// TestTaskListIgnoresAStaleIfNoneMatch: an ETag from before a write is
// not the current one, and a poller sending one gets the list. Belt and
// braces over TestTaskListRebuildsAfterAWrite, for the header parsing
// rather than the cache: a list of candidates, and the weak form, are
// both things a client or a proxy in between may legitimately send.
func TestTaskListIgnoresAStaleIfNoneMatch(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`)
	etag := listWith(t, srv, "").Header().Get("ETag")

	if rec := listWith(t, srv, `"nothing-like-it"`); rec.Code != http.StatusOK {
		t.Errorf("status for an unrelated ETag = %d, want 200", rec.Code)
	}
	if rec := listWith(t, srv, `"nothing-like-it", `+etag); rec.Code != http.StatusNotModified {
		t.Errorf("status for a list containing the current ETag = %d, want 304", rec.Code)
	}
	if rec := listWith(t, srv, "W/"+etag); rec.Code != http.StatusNotModified {
		t.Errorf("status for the weak form of the current ETag = %d, want 304", rec.Code)
	}
}

// TestTaskListOfAnEmptyStoreIsAnEmptyArray: a deployment with no tasks in
// it yet answers [], not null. The UI maps over whatever comes back, and
// a null took the whole app down to its "something went wrong" page on a
// freshly installed daemon (2026-10-02 §6 walkthrough).
func TestTaskListOfAnEmptyStoreIsAnEmptyArray(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := listWith(t, srv, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("empty task list = %q, want []", got)
	}
}

// TestTaskListNarrowsToTheAskedRepo: ?repo= answers only that
// repository's tasks, compared case-insensitively, under its own ETag --
// the agent API gate admits a repository-scoped agent on the strength of
// the ?repo= it names, so the answer may hold nothing outside it.
func TestTaskListNarrowsToTheAskedRepo(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets")
	seedAPITask(t, client, "2", "acme/gadgets")

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, rec.Code)
		}
		return rec
	}
	whole := get("/api/tasks")
	for _, path := range []string{"/api/tasks?repo=acme/widgets", "/api/tasks?repo=ACME/Widgets"} {
		narrowed := get(path)
		body := narrowed.Body.String()
		if !strings.Contains(body, `"acme/widgets"`) || strings.Contains(body, "acme/gadgets") {
			t.Errorf("GET %s = %s, want only acme/widgets' task", path, body)
		}
		if narrowed.Header().Get("ETag") == whole.Header().Get("ETag") {
			t.Errorf("GET %s carries the whole list's ETag", path)
		}
	}
	if body := get("/api/tasks?repo=acme/nothing").Body.String(); strings.TrimSpace(body) != "[]" {
		t.Errorf("a repo with no tasks answered %s, want []", body)
	}
	if body := whole.Body.String(); !strings.Contains(body, "acme/gadgets") || !strings.Contains(body, "acme/widgets") {
		t.Errorf("the unfiltered list lost a repo: %s", body)
	}
}

// TestTaskListNarrowsToStateSearchAndLimit pins GET /api/tasks for a
// caller that is not the UI: ?state=, ?q= and ?limit= narrow it, the
// newest limit tasks are kept in filing order (never as strings, which
// puts 9 after 12), and fields=summary leaves every description out.
func TestTaskListNarrowsToStateSearchAndLimit(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	for i := 1; i <= 12; i++ {
		approved := i%2 == 1
		title := fmt.Sprintf("fix thing %d", i)
		if i == 9 {
			title = "Flaky go-long test"
		}
		do(t, srv, http.MethodPost, "/api/tasks",
			fmt.Sprintf(`{"title":%q,"description":"a very long description","approved":%v}`, title, approved))
	}
	get := func(path string) []map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d: %s", path, rec.Code, rec.Body.String())
		}
		var out []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET %s: %v: %s", path, err, rec.Body.String())
		}
		return out
	}
	ids := func(tasks []map[string]any) string {
		var out []string
		for _, task := range tasks {
			out = append(out, fmt.Sprint(task["id"]))
		}
		return strings.Join(out, ",")
	}

	if got := get("/api/tasks?limit=3"); ids(got) != "10,11,12" {
		t.Errorf("?limit=3 = %s, want the newest three in filing order", ids(got))
	}
	proposed := get("/api/tasks?state=proposed&limit=100")
	if len(proposed) != 6 {
		t.Errorf("?state=proposed = %s, want the six unapproved tasks", ids(proposed))
	}
	for _, task := range proposed {
		if task["state"] != "proposed" {
			t.Errorf("?state=proposed listed %v in state %v", task["id"], task["state"])
		}
	}
	if got := get("/api/tasks?state=proposed,queued&q=GO-LONG"); ids(got) != "9" {
		t.Errorf("?q=GO-LONG = %s, want 9", ids(got))
	}
	if got := get("/api/tasks?search=nothing+like+it"); len(got) != 0 {
		t.Errorf("?search with no match = %s, want []", ids(got))
	}
	summary := get("/api/tasks?fields=summary&limit=2")
	if ids(summary) != "11,12" {
		t.Errorf("?fields=summary&limit=2 = %s", ids(summary))
	}
	for _, task := range summary {
		if _, ok := task["description"]; ok {
			t.Errorf("fields=summary carried a description: %v", task)
		}
		if task["title"] == nil || task["state"] == nil {
			t.Errorf("fields=summary lost the title or state: %v", task)
		}
	}
	if full := get("/api/tasks?limit=1"); full[0]["description"] != "a very long description" {
		t.Errorf("?limit=1 without fields lost the description: %v", full[0])
	}
}
