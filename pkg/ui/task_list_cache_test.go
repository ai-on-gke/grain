package ui_test

// GET /api/tasks as the thing every open tab polls every three seconds:
// what it costs to be asked again, and -- much more important -- that
// being cheap never makes it wrong. Every test here is a property of the
// cache in front of that route (pkg/ui/task_list_cache.go).

import (
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
