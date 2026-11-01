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

// The HTTP surface, over the same real store client_test.go uses. These
// assert on status codes and JSON shape; what the calls actually do to a
// task is client_test.go's job, since Server is a thin shim over Client
// and nothing more.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
	"github.com/ai-on-gke/grain/pkg/version"
)

func testServer(t *testing.T) (*ui.Server, *ui.Client) {
	t.Helper()
	client, _, _ := testClient(t)
	return ui.NewServerWithClient(client), client
}

// storeSettingsRow gives c's store a seeded settings row, as a daemon's
// first start does, for the routes that read one.
func storeSettingsRow(t *testing.T, c *ui.Client) {
	t.Helper()
	ctx := context.Background()
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = &model.Config{}
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = model.DefaultPollInterval
	}
	if err := c.Store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
}

func do(t *testing.T, srv *ui.Server, method, path, body string) *httptest.ResponseRecorder {
	return doWithHeaders(t, srv, method, path, body, nil)
}

func doWithHeaders(t *testing.T, srv *ui.Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return v
}

func TestCreateThenListAndGet(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"fix it","description":"please","approved":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Task](t, rec)
	if created.ID == "" {
		t.Fatal("created task came back with no id")
	}
	if created.State != model.StateQueued {
		t.Fatalf("state = %q, want queued", created.State)
	}

	rec = do(t, srv, http.MethodGet, "/api/tasks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	if listed := decode[[]ui.Task](t, rec); len(listed) != 1 {
		t.Fatalf("listed %d tasks, want 1", len(listed))
	}

	rec = do(t, srv, http.MethodGet, "/api/tasks/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", rec.Code)
	}
	if detail := decode[ui.TaskDetail](t, rec); detail.ID != created.ID {
		t.Fatalf("got task %q, want %q", detail.ID, created.ID)
	}
}

// TestAttachmentRoutesRoundTripThroughCreateCommentAndDownload is the HTTP
// surface for bwsalmon/agents#522: a base64 upload on POST /api/tasks
// lands as metadata on GET /api/tasks/{id} (never content -- Attachment's
// own doc comment), one on POST .../comments lands on that comment
// instead, and GET .../attachments/{attachmentId} is the one route that
// serves an attachment's actual bytes back, with its own content type.
func TestAttachmentRoutesRoundTripThroughCreateCommentAndDownload(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks", `{
		"title":"fix it","approved":true,
		"attachments":[{"filename":"repro.zip","contentType":"application/zip","content":"UEsDBA=="}]
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Task](t, rec)

	rec = do(t, srv, http.MethodPost, "/api/tasks/"+created.ID+"/comments", `{
		"attachments":[{"filename":"screenshot.png","contentType":"image/png","content":"ZmFrZQ=="}]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("comment status = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodGet, "/api/tasks/"+created.ID, "")
	detail := decode[ui.TaskDetail](t, rec)
	if len(detail.Attachments) != 1 || detail.Attachments[0].Filename != "repro.zip" {
		t.Fatalf("task attachments = %+v, want one repro.zip", detail.Attachments)
	}
	if len(detail.Comments) != 1 || len(detail.Comments[0].Attachments) != 1 ||
		detail.Comments[0].Attachments[0].Filename != "screenshot.png" {
		t.Fatalf("comment attachments = %+v, want one screenshot.png", detail.Comments)
	}

	rec = do(t, srv, http.MethodGet,
		fmt.Sprintf("/api/tasks/%s/attachments/%d", created.ID, detail.Attachments[0].ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d, want 200: %s", rec.Code, rec.Body)
	}
	// A zip is not one of the types served inline (#2374), so it goes out
	// as an opaque download rather than under its uploader-chosen type.
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("content type = %q, want application/octet-stream", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename=repro.zip` {
		t.Errorf("content disposition = %q, want an attachment download", got)
	}
	if rec.Body.String() != "PK\x03\x04" {
		t.Errorf("body = %q, want the decoded attachment bytes", rec.Body.String())
	}
}

// TestAttachmentDownloadNeverServesUploaderTypesInline is #2374: an
// attachment's content type is whatever its uploader (an HTTP caller, or a
// prompt-injected agent) said, so "text/html" or "image/svg+xml" must not
// come back inline from the daemon's own origin where a browser would run
// its script. Only the safe raster images and plain text keep their type
// and inline disposition, and every response carries nosniff and a
// sandboxing CSP.
func TestAttachmentDownloadNeverServesUploaderTypesInline(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	cases := []struct {
		contentType     string
		wantType        string
		wantDisposition string
	}{
		{"text/html", "application/octet-stream", "attachment"},
		{"TEXT/HTML; charset=utf-8", "application/octet-stream", "attachment"},
		{"image/svg+xml", "application/octet-stream", "attachment"},
		{"application/xhtml+xml", "application/octet-stream", "attachment"},
		{"not a media type", "application/octet-stream", "attachment"},
		{"image/png", "image/png", "inline"},
		{"Image/JPEG", "image/jpeg", "inline"},
		{"text/plain", "text/plain; charset=utf-8", "inline"},
	}
	for _, tc := range cases {
		body := fmt.Sprintf(`{"title":"fix it","approved":true,
			"attachments":[{"filename":"poc","contentType":%q,"content":"PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg=="}]}`,
			tc.contentType)
		rec := do(t, srv, http.MethodPost, "/api/tasks", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: create status = %d, want 201: %s", tc.contentType, rec.Code, rec.Body)
		}
		created := decode[ui.Task](t, rec)
		detail := decode[ui.TaskDetail](t, do(t, srv, http.MethodGet, "/api/tasks/"+created.ID, ""))
		if len(detail.Attachments) != 1 {
			t.Fatalf("%s: attachments = %+v, want one", tc.contentType, detail.Attachments)
		}

		rec = do(t, srv, http.MethodGet,
			fmt.Sprintf("/api/tasks/%s/attachments/%d", created.ID, detail.Attachments[0].ID), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: download status = %d, want 200: %s", tc.contentType, rec.Code, rec.Body)
		}
		h := rec.Header()
		if got := h.Get("Content-Type"); got != tc.wantType {
			t.Errorf("%s: content type = %q, want %q", tc.contentType, got, tc.wantType)
		}
		if got := h.Get("Content-Disposition"); got != tc.wantDisposition+"; filename=poc" {
			t.Errorf("%s: content disposition = %q, want %s", tc.contentType, got, tc.wantDisposition)
		}
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", tc.contentType, got)
		}
		if got := h.Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") || !strings.Contains(got, "default-src 'none'") {
			t.Errorf("%s: Content-Security-Policy = %q, want a sandboxing default-src 'none' policy", tc.contentType, got)
		}
	}
}

func TestAttachmentDownloadUnknownIDIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`)
	created := decode[ui.Task](t, rec)

	rec = do(t, srv, http.MethodGet, "/api/tasks/"+created.ID+"/attachments/999", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// TestReorderRoute is the HTTP surface for Client.Reorder
// (client_test.go's TestReorderMovesATaskInTheBacklog already covers what
// it does to the store): POST /api/tasks/reorder, a literal path segment
// that has to win over the PATCH /api/tasks/{id} wildcard registered at
// the same depth rather than being swallowed by it.
func TestReorderRoute(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	var ids [3]string
	for i := range ids {
		rec := do(t, srv, http.MethodPost, "/api/tasks",
			`{"title":"t","approved":true}`)
		ids[i] = decode[ui.Task](t, rec).ID
	}

	rec := do(t, srv, http.MethodPost, "/api/tasks/reorder",
		`{"ids":["`+ids[2]+`"],"beforeId":"`+ids[0]+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder status = %d, want 200: %s", rec.Code, rec.Body)
	}
	listed := decode[[]ui.Task](t, rec)
	if len(listed) != 3 {
		t.Fatalf("reorder returned %d tasks, want 3", len(listed))
	}
	// The list the route hands back is backlog order, so the move is
	// visible in it directly: the third task, dropped before the first,
	// is now at the top.
	if listed[0].ID != ids[2] {
		t.Fatalf("listed[0] = %q, want %q (moved to the front of the backlog)", listed[0].ID, ids[2])
	}
}

func TestReorderRouteRejectsEmptyIDs(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/tasks/reorder", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// A drop naming a task that is not there any more -- a list left open in
// another tab while the task was closed -- is the caller's stale view,
// not a broken server. It used to fall out of the store as a 500 (found
// by hand, task 244).
func TestReorderRouteWithAnUnknownIDIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"t","approved":true}`)
	id := decode[ui.Task](t, rec).ID

	if rec := do(t, srv, http.MethodPost, "/api/tasks/reorder", `{"ids":["999"]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	// The neighbour a drop lands against counts the same way.
	if rec := do(t, srv, http.MethodPost, "/api/tasks/reorder",
		`{"ids":["`+id+`"],"beforeId":"999"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status with an unknown beforeId = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestGetUnknownTaskIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	if rec := do(t, srv, http.MethodGet, "/api/tasks/404", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestCreateRejectionsAre400(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	for name, body := range map[string]string{
		"empty title":        `{"title":"  "}`,
		"unknown capability": `{"title":"t","capabilities":["nope"]}`,
		"malformed json":     `{`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(t, srv, http.MethodPost, "/api/tasks", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
		})
	}
}

// Every mutating route answers with the task as it now stands, so the
// frontend never has to assume its own optimistic update was right.
func TestMutatingRoutesRespondWithTheTask(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it"}`)
	id := decode[ui.Task](t, rec).ID

	// A slice, not a map: these run against one task in sequence, so the
	// expected state after each step depends on the step before it. Map
	// iteration order is randomised, which would make that a coin flip.
	for _, call := range []struct {
		name string
		path string
		body string
		want model.State
	}{
		{name: "approve", path: "/approve", want: model.StateQueued},
		// Approve's own undo, and then the re-approval that undoes that:
		// the route answers with the task in whichever state its approval
		// now implies, the same as every other row here.
		{name: "withdraw approval", path: "/withdraw-approval", want: model.StateProposed},
		{name: "approve again", path: "/approve", want: model.StateQueued},
		// Deferring and picking back up, which is the other pair that
		// moves a task without touching what it says: the approval
		// survives the round trip, so the task lands back on 'queued'
		// rather than among the proposals.
		{name: "defer", path: "/defer", want: model.StateDeferred},
		{name: "undefer", path: "/undefer", want: model.StateQueued},
		{name: "capability", path: "/capabilities", body: `{"id":"gemini-key","attach":true}`, want: model.StateQueued},
		// Asking for a run leaves the task queued -- the run itself is a
		// dispatch cycle's to start (ui.Client.RunNow) -- so this row
		// proves the wiring rather than a transition.
		{name: "run now", path: "/run-now", want: model.StateQueued},
		{name: "comment", path: "/comments", body: `{"body":"hello"}`, want: model.StateQueued},
		{name: "close", path: "/close", want: model.StateClosed},
	} {
		name := call.name
		t.Run(name, func(t *testing.T) {
			rec := do(t, srv, http.MethodPost, "/api/tasks/"+id+call.path, call.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			task := decode[ui.Task](t, rec)
			if task.ID != id {
				t.Fatalf("responded with task %q, want %q", task.ID, id)
			}
			if task.State != call.want {
				t.Fatalf("state = %q, want %q", task.State, call.want)
			}
		})
	}

	rec = do(t, srv, http.MethodPost, "/api/tasks/"+id+"/reopen", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reopen status = %d, want 200", rec.Code)
	}
	if task := decode[ui.Task](t, rec); task.State != model.StateQueued {
		t.Fatalf("state after reopen = %q, want queued", task.State)
	}
}

// The close route's one flag, over the wire and in all three shapes a
// caller can send it in. The default matters more than the flag: a body
// that does not mention it, and no body at all, both have to mean "leave
// the pull request alone", because that is what every caller written
// before the flag existed sends -- the CLI without it, the batch Close
// button, a bare `curl -X POST`.
func TestCloseRouteClosesThePullRequestOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{name: "no body at all", body: "", want: 0},
		{name: "a body that says nothing about it", body: `{}`, want: 0},
		{name: "asked for explicitly", body: `{"close_pull_request":false}`, want: 0},
		{name: "asked against explicitly", body: `{"close_pull_request":true}`, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, store, ctx := testClient(t)
			closer := &recordingCloser{}
			client.Config.PullRequestCloser = closer
			client.Config.PullRequestComments = &recordingComments{}
			srv := ui.NewServerWithClient(client)
			task, _ := linkedTask(t, client, store, ctx)

			rec := do(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/close", tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			if decoded := decode[ui.Task](t, rec); decoded.State != model.StateClosed {
				t.Fatalf("state = %q, want closed", decoded.State)
			}
			if len(closer.closed) != tc.want {
				t.Fatalf("closed on GitHub = %+v, want %d", closer.closed, tc.want)
			}
		})
	}
}

type recordingTaskCanceller struct {
	cancelled []string
}

func (r *recordingTaskCanceller) CancelTask(ctx context.Context, taskID string) bool {
	r.cancelled = append(r.cancelled, taskID)
	return true
}

func TestStopRouteInterruptsTurn(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	canceller := &recordingTaskCanceller{}
	client.Config.TaskCanceller = canceller
	srv := ui.NewServerWithClient(client)
	task := create(t, client, ctx)

	rec := do(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/stop", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	decoded := decode[ui.Task](t, rec)
	if decoded.State != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", decoded.State)
	}
	if len(canceller.cancelled) != 1 || canceller.cancelled[0] != task.ID {
		t.Fatalf("cancelled = %+v, want [%s]", canceller.cancelled, task.ID)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil || len(comments) == 0 {
		t.Fatalf("comments: %v, len = %d", err, len(comments))
	}
	lastComment := comments[len(comments)-1]
	if lastComment.Body != "Turn stopped by user" {
		t.Errorf("last comment body = %q, want 'Turn stopped by user'", lastComment.Body)
	}
}

// The defer route's one field, over the wire and in the shapes a caller
// sends it in. The default matters for the reason the close route's does:
// a body that does not mention a wake time, and no body at all, are the
// indefinite deferral every caller written before it existed makes.
//
// The clock these run against is the test client's own fixed one
// (baseTime), which is why the dates below are absolute rather than
// relative to the machine running the test: "in the future" is a fact
// about that clock.
func TestDeferRouteCarriesTheWakeTime(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		body   string
		status int
		want   string // the deferredUntil the task comes back with, RFC 3339
	}{
		{name: "no body at all", body: "", status: http.StatusOK},
		{name: "a body that says nothing about it", body: `{}`, status: http.StatusOK},
		{name: "an empty wake time", body: `{"deferredUntil":""}`, status: http.StatusOK},
		{
			name:   "a date to come back on",
			body:   `{"deferredUntil":"2026-09-14T00:00:00Z"}`,
			status: http.StatusOK,
			want:   "2026-09-14T00:00:00Z",
		},
		// Refused by Client.Defer: baseTime is 2026-08-28.
		{name: "a wake time that has passed", body: `{"deferredUntil":"2026-08-01T00:00:00Z"}`, status: http.StatusBadRequest},
		// Refused here, naming the field, rather than by the decoder.
		{name: "not a time at all", body: `{"deferredUntil":"next tuesday"}`, status: http.StatusBadRequest},
		// A broken body is still the caller's mistake, tolerating an
		// absent one notwithstanding.
		{name: "a broken body", body: `{`, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := testServer(t)
			rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"maybe next month"}`)
			id := decode[ui.Task](t, rec).ID

			rec = do(t, srv, http.MethodPost, "/api/tasks/"+id+"/defer", tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.status != http.StatusOK {
				return
			}
			task := decode[ui.Task](t, rec)
			if task.State != model.StateDeferred {
				t.Fatalf("state = %q, want deferred", task.State)
			}
			var got string
			if task.DeferredUntil != nil {
				got = task.DeferredUntil.UTC().Format(time.RFC3339)
			}
			if got != tc.want {
				t.Fatalf("deferredUntil = %q, want %q", got, tc.want)
			}
		})
	}
}

// A body that is there and is nonsense is still the caller's mistake --
// tolerating an absent body is not tolerating a broken one, or a typo in
// the one field would silently read as false.
func TestCloseRouteRejectsAMalformedBody(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it"}`)
	id := decode[ui.Task](t, rec).ID

	if rec := do(t, srv, http.MethodPost, "/api/tasks/"+id+"/close", `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// TestRetryRouteClearsAFailedTasksStreak is handleRetry's own route test
// (bwsalmon/agents#403's "Retry" button) -- client_test.go's
// TestRetryClearsAFailedTasksStreak already proves Client.Retry itself
// works; this proves the route wires a POST through to it and answers
// with the task in its now-unfailed state, the same "respond with the
// task" contract TestMutatingRoutesRespondWithTheTask checks for every
// other mutating route.
func TestRetryRouteClearsAFailedTasksStreak(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`)
	id := decode[ui.Task](t, rec).ID

	for i := 0; i < model.MaxConsecutiveFailures; i++ {
		runID := id + "-r" + strconv.Itoa(i+1)
		started := baseTime.Add(time.Duration(i) * time.Hour)
		if err := client.Store.StartRun(context.Background(), model.Run{
			ID: runID, TaskID: id, Sandbox: "s1", Attempt: i + 1, StartedAt: started,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := client.Store.FinishRun(context.Background(), runID, started.Add(time.Minute), "failed", "boom"); err != nil {
			t.Fatal(err)
		}
	}

	rec = do(t, srv, http.MethodGet, "/api/tasks/"+id, "")
	if got := decode[ui.TaskDetail](t, rec).State; got != model.StateFailed {
		t.Fatalf("state before retrying = %q, want failed", got)
	}

	rec = do(t, srv, http.MethodPost, "/api/tasks/"+id+"/retry", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200: %s", rec.Code, rec.Body)
	}
	task := decode[ui.Task](t, rec)
	if task.ID != id {
		t.Fatalf("responded with task %q, want %q", task.ID, id)
	}
	if task.State != model.StateQueued {
		t.Fatalf("state after retrying = %q, want queued", task.State)
	}
}

func TestRetryRouteOnAnUnknownTaskIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/tasks/nope/retry", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// TestUpdateRouteEditsFields is handleUpdateTask's own route test --
// PATCH /api/tasks/{id} exists so the CLI's `grain update` (cmdUpdate)
// has an endpoint to call now that it is an HTTPClient rather than a
// direct Client caller; client_test.go's own TestUpdateTask* already
// cover every field-level rule, so this only has to prove the route
// itself is wired to Client.UpdateTask correctly.
func TestUpdateRouteEditsFields(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it"}`)
	id := decode[ui.Task](t, rec).ID

	rec = do(t, srv, http.MethodPatch, "/api/tasks/"+id,
		`{"title":"fix it for real","base":"release","autoMerge":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	task := decode[ui.Task](t, rec)
	if task.Title != "fix it for real" || task.Base != "release" || !task.AutoMerge {
		t.Fatalf("task after update = %+v, want the three fields changed", task)
	}
}

func TestUpdateRouteUnknownTaskIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPatch, "/api/tasks/does-not-exist", `{"title":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestSubmitRouteSetsAutoMerge(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","approved":true}`)
	id := decode[ui.Task](t, rec).ID

	rec = do(t, srv, http.MethodPost, "/api/tasks/"+id+"/submit", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if task := decode[ui.Task](t, rec); !task.AutoMerge {
		t.Fatalf("autoMerge = %v, want true", task.AutoMerge)
	}
}

func TestDependsOnRouteAttachesAndReportsBlocked(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"blocker"}`)
	blockerID := decode[ui.Task](t, rec).ID
	rec = do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it"}`)
	id := decode[ui.Task](t, rec).ID

	rec = do(t, srv, http.MethodPost, "/api/tasks/"+id+"/depends-on",
		`{"id":"`+blockerID+`","attach":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	task := decode[ui.Task](t, rec)
	if !task.Blocked {
		t.Fatal("blocked = false, want true")
	}
	if len(task.DependsOn) != 1 || task.DependsOn[0] != blockerID {
		t.Fatalf("dependsOn = %v, want [%s]", task.DependsOn, blockerID)
	}
}

func TestDependsOnRouteRejectsSelfDependency(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it"}`)
	id := decode[ui.Task](t, rec).ID

	rec = do(t, srv, http.MethodPost, "/api/tasks/"+id+"/depends-on", `{"id":"`+id+`","attach":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// TestConfigEndpointReportsTargetRepos is the dropdown source
// bwsalmon/agents#447 added the frontend's repo fields: whatever
// CreateTask enforces a task's repo against should be exactly what GET
// /api/config reports, so the UI never offers a repo the server would
// then park off the allowlist.
func TestConfigEndpointReportsTargetRepos(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "acme/other"})
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cfg := decode[struct {
		TargetRepos []string `json:"targetRepos"`
	}](t, rec)
	if !reflect.DeepEqual(cfg.TargetRepos, []string{"acme/widgets", "acme/other"}) {
		t.Fatalf("targetRepos = %v, want [acme/widgets acme/other]", cfg.TargetRepos)
	}
}

// TestConfigEndpointReportsShowClosedByDefault is bwsalmon/agents#537's
// global default reaching a list before Settings has ever been opened
// this session: unconfigured (no UpdateSettings call yet) reports false,
// matching "hide closed tasks by default", and an operator turning it on
// through Settings is reflected immediately, the same store round trip
// TestConfigEndpointReportsTargetRepos already covers for targetRepos.
func TestConfigEndpointReportsShowClosedByDefault(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	cfg := decode[struct {
		ShowClosedByDefault bool   `json:"showClosedByDefault"`
		DefaultFilters      string `json:"defaultFilters"`
	}](t, rec)
	if cfg.ShowClosedByDefault {
		t.Fatalf("showClosedByDefault = true with nothing configured, want false")
	}
	if cfg.DefaultFilters != model.DefaultTaskFilters {
		t.Fatalf("defaultFilters = %q, want %q", cfg.DefaultFilters, model.DefaultTaskFilters)
	}

	if _, err := client.UpdateSettings(context.Background(), firstSettings()); err != nil {
		t.Fatal(err)
	}
	show := true
	if _, err := client.UpdateSettings(context.Background(), ui.UpdateSettingsRequest{ShowClosedByDefault: &show}); err != nil {
		t.Fatal(err)
	}

	rec = do(t, srv, http.MethodGet, "/api/config", "")
	cfg = decode[struct {
		ShowClosedByDefault bool   `json:"showClosedByDefault"`
		DefaultFilters      string `json:"defaultFilters"`
	}](t, rec)
	if !cfg.ShowClosedByDefault {
		t.Fatalf("showClosedByDefault = false after UpdateSettings, want true")
	}

	stored, err := client.Store.GetConfig(context.Background())
	if err != nil || stored == nil {
		t.Fatalf("reading config to simulate a config-repository edit: (%+v, %v)", stored, err)
	}
	stored.DefaultFilters = "f=state:any:queued|in_progress&f=repo:none:acme/widgets"
	if err := client.Store.PutConfig(context.Background(), *stored); err != nil {
		t.Fatalf("writing config-repository defaults: %v", err)
	}
	rec = do(t, srv, http.MethodGet, "/api/config", "")
	cfg = decode[struct {
		ShowClosedByDefault bool   `json:"showClosedByDefault"`
		DefaultFilters      string `json:"defaultFilters"`
	}](t, rec)
	if cfg.DefaultFilters != stored.DefaultFilters {
		t.Fatalf("defaultFilters = %q, want config-repository value %q", cfg.DefaultFilters, stored.DefaultFilters)
	}
}

// TestConfigEndpointReportsEnvironmentName is grain/task-69's label
// reaching the frontend on the one call it makes before rendering
// anything: unconfigured reports nothing (an unnamed deployment, and the
// sidebar draws no badge), and naming the deployment through Settings is
// reflected immediately, the same store round trip
// TestConfigEndpointReportsShowClosedByDefault covers above.
func TestConfigEndpointReportsEnvironmentName(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)

	type environment struct {
		EnvironmentName string `json:"environmentName"`
	}
	rec := do(t, srv, http.MethodGet, "/api/config", "")
	cfg := decode[environment](t, rec)
	if cfg.EnvironmentName != "" {
		t.Fatalf("environmentName = %q with nothing configured, want empty", cfg.EnvironmentName)
	}

	if _, err := client.UpdateSettings(context.Background(), firstSettings()); err != nil {
		t.Fatal(err)
	}
	name := "staging"
	if _, err := client.UpdateSettings(context.Background(), ui.UpdateSettingsRequest{EnvironmentName: &name}); err != nil {
		t.Fatal(err)
	}

	rec = do(t, srv, http.MethodGet, "/api/config", "")
	cfg = decode[environment](t, rec)
	if cfg.EnvironmentName != "staging" {
		t.Fatalf("environmentName = %q after UpdateSettings, want %q", cfg.EnvironmentName, "staging")
	}
}

// TestConfigEndpointReportsTaskDefaults is the same round trip for
// bwsalmon/agents#612's pair, which differs from showClosedByDefault
// above in what "nothing configured" reports: both default on
// (model.DefaultConfig), so a deployment with no stored row yet has to
// say so rather than report the zero value of the field, and an operator
// turning one off through Settings is reflected immediately.
func TestConfigEndpointReportsTaskDefaults(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)

	type taskDefaults struct {
		ApprovedByDefault  bool `json:"approvedByDefault"`
		AutoMergeByDefault bool `json:"autoMergeByDefault"`
	}
	rec := do(t, srv, http.MethodGet, "/api/config", "")
	cfg := decode[taskDefaults](t, rec)
	if !cfg.ApprovedByDefault || !cfg.AutoMergeByDefault {
		t.Fatalf("approvedByDefault/autoMergeByDefault = %v/%v with nothing configured, want true/true",
			cfg.ApprovedByDefault, cfg.AutoMergeByDefault)
	}

	if _, err := client.UpdateSettings(context.Background(), firstSettings()); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := client.UpdateSettings(context.Background(), ui.UpdateSettingsRequest{ApprovedByDefault: &off}); err != nil {
		t.Fatal(err)
	}

	rec = do(t, srv, http.MethodGet, "/api/config", "")
	cfg = decode[taskDefaults](t, rec)
	if cfg.ApprovedByDefault {
		t.Fatalf("approvedByDefault = true after UpdateSettings turned it off, want false")
	}
	if !cfg.AutoMergeByDefault {
		t.Fatalf("autoMergeByDefault = false after a save that never mentioned it, want true")
	}
}

// grain/task-202: the new-task form's "Add to backlog" picker opens on
// the end the last task added chose, so GET /api/config -- the one call
// the frontend makes before its first paint -- has to carry it. Filing a
// task through the API is what moves it, not only the Settings pane.
func TestConfigEndpointReportsWhereNewTasksJoinTheBacklog(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()

	type backlogEnd struct {
		NewestFirst bool `json:"newestFirst"`
	}
	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if decode[backlogEnd](t, rec).NewestFirst {
		t.Fatalf("newestFirst = true with nothing configured, want the end of the backlog")
	}

	if _, err := client.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	atFront := true
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "run this next", AtFront: &atFront}); err != nil {
		t.Fatal(err)
	}

	rec = do(t, srv, http.MethodGet, "/api/config", "")
	if !decode[backlogEnd](t, rec).NewestFirst {
		t.Fatalf("newestFirst = false after a task was filed at the front, want it remembered")
	}
}

func TestSubmitUnknownTaskIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	if rec := do(t, srv, http.MethodPost, "/api/tasks/404/submit", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestConfigEndpointReportsActorAndCapabilities(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cfg := decode[struct {
		Actor        string          `json:"actor"`
		Capabilities []ui.Capability `json:"capabilities"`
	}](t, rec)

	if cfg.Actor != "alice" {
		t.Fatalf("actor = %+v, want the configured one", cfg.Actor)
	}
	if len(cfg.Capabilities) != len(ui.OfferedCapabilities()) {
		t.Fatalf("capabilities = %d, want %d", len(cfg.Capabilities), len(ui.OfferedCapabilities()))
	}
	// The GitHub label a capability used to carry is gone from the wire
	// shape along with the labels themselves.
	if strings.Contains(rec.Body.String(), "grain-gemini-key") {
		t.Fatalf("config still reports a GitHub label: %s", rec.Body)
	}
}

// GET /api/config carries the deployment's default capability set so
// NewTaskOverlay.jsx can open its picker with those boxes already
// ticked -- the form is where whoever files the task sees them, and
// where they untick one they do not want (grain/task-14). A stored
// id this build no longer offers is filtered out, so the form ticks what
// the task would really be filed with.
func TestConfigEndpointReportsDefaultCapabilities(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)

	cfg := model.DefaultConfig()
	cfg.DefaultCapabilities = []string{"gcp-key", "scratch-repo"}
	if err := client.Store.PutConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decode[struct {
		DefaultCapabilities []string `json:"defaultCapabilities"`
	}](t, rec)
	if !reflect.DeepEqual(got.DefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("defaultCapabilities = %v, want [gcp-key]", got.DefaultCapabilities)
	}
}

// The per-repo layer travels with the same response, keyed by repo, so
// the new-task form can re-seed its picker the moment the repo picker
// changes rather than asking the server once per keystroke
// (grain/task-24). Filtered the same way, and only repos that add
// something appear at all.
func TestConfigEndpointReportsRepoDefaultCapabilities(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()

	if err := client.Store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                model.RepoRef{Owner: "acme", Name: "widgets"},
		DefaultCapabilities: []string{"gcp-key", "scratch-repo"},
	}); err != nil {
		t.Fatal(err)
	}
	// Nothing but a retired id: the repo has nothing this build can
	// grant, so it is not reported as adding anything.
	if err := client.Store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                model.RepoRef{Owner: "acme", Name: "gadgets"},
		DefaultCapabilities: []string{"scratch-repo"},
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decode[struct {
		RepoDefaultCapabilities map[string][]string `json:"repoDefaultCapabilities"`
	}](t, rec)
	want := map[string][]string{"acme/widgets": {"gcp-key"}}
	if !reflect.DeepEqual(got.RepoDefaultCapabilities, want) {
		t.Fatalf("repoDefaultCapabilities = %v, want %v", got.RepoDefaultCapabilities, want)
	}
}

// NewTaskOverlay sends what its picker shows as an explicit capability set.
// The config repository's built-in CI floor therefore has to travel in this
// response even though no repo_config row declares it.
func TestSettingsRoutesReadAndWrite(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.Settings](t, rec); got.Configured {
		t.Fatalf("settings = %+v, want Configured false on a fresh store", got)
	}

	rec = do(t, srv, http.MethodPut, "/api/settings",
		`{"pollInterval":"1m","maxWorkers":2,"geminiModel":"gemini-2.5-pro","claudeModel":"claude-sonnet-5","githubHost":"github.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	saved := decode[ui.Settings](t, rec)
	if !saved.Configured || saved.PollInterval != "1m0s" {
		t.Fatalf("saved settings = %+v", saved)
	}

	rec = do(t, srv, http.MethodGet, "/api/settings", "")
	if got := decode[ui.Settings](t, rec); !reflect.DeepEqual(got, saved) {
		t.Fatalf("get after put = %+v, want %+v", got, saved)
	}
}

// The Models settings flow crosses three API resources: each framework's
// live catalog, the persisted backend pool, and write-only credentials. Keep
// them together here so changing one response without the others cannot leave
// the pane appearing to save a configuration dispatch will not receive.
func TestModelsSettingsCatalogsBackendsAndCredentialsRoundTrip(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	secretStore := secrets.New(t.TempDir())
	client.Config.Secrets = secretStore
	client.Config.CodexModels = fakeModelLister{catalog: ui.AgentModelCatalog{
		Models: []ui.AgentModel{{
			ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol",
			SupportedEfforts: []string{"low", "high"}, DefaultEffort: "high",
		}},
		Efforts: []string{"low", "medium", "high", "xhigh"},
	}}
	client.Config.AgentModels = fakeModelLister{catalog: ui.AgentModelCatalog{
		Models: []ui.AgentModel{
			{ID: "gemini-3.1-flash-low", Family: "gemini-3.1-flash", Effort: "low"},
			{ID: "gemini-3.1-flash-high", Family: "gemini-3.1-flash", Effort: "high"},
		},
		Efforts: []string{"low", "medium", "high"},
	}}

	codexCatalog := do(t, srv, http.MethodGet, "/api/agent-models?framework=codex", "")
	if codexCatalog.Code != http.StatusOK {
		t.Fatalf("Codex catalog status = %d: %s", codexCatalog.Code, codexCatalog.Body)
	}
	codexModels := decode[struct {
		Available bool            `json:"available"`
		Models    []ui.AgentModel `json:"models"`
		Efforts   []string        `json:"efforts"`
	}](t, codexCatalog)
	if !codexModels.Available ||
		!reflect.DeepEqual(codexModels.Models[0].SupportedEfforts, []string{"low", "high"}) ||
		!slices.Contains(codexModels.Efforts, "xhigh") {
		t.Fatalf("Codex catalog = %+v, want its model-specific and custom-model effort constraints", codexModels)
	}

	geminiCatalog := do(t, srv, http.MethodGet, "/api/agent-models?framework=antigravity", "")
	if geminiCatalog.Code != http.StatusOK {
		t.Fatalf("Antigravity catalog status = %d: %s", geminiCatalog.Code, geminiCatalog.Body)
	}
	geminiModels := decode[struct {
		Available bool            `json:"available"`
		Models    []ui.AgentModel `json:"models"`
		Efforts   []string        `json:"efforts"`
	}](t, geminiCatalog)
	if !geminiModels.Available || geminiModels.Models[1].Family != "gemini-3.1-flash" ||
		geminiModels.Models[1].Effort != "high" {
		t.Fatalf("Antigravity catalog = %+v, want its own family/effort variants", geminiModels)
	}

	const body = `{
		"pollInterval":"1m","maxWorkers":2,
		"githubHost":"github.com",
		"agentBackends":[
			{"id":"codex-preview","name":"Preview account","framework":"codex",
			 "models":["gpt-private-preview"]},
			{"id":"gemini-fast","name":"Gemini account","framework":"antigravity",
			 "models":["gemini-3.1-flash-high"]}
		]
	}`
	savedRec := do(t, srv, http.MethodPut, "/api/settings", body)
	if savedRec.Code != http.StatusOK {
		t.Fatalf("saving Models settings = %d: %s", savedRec.Code, savedRec.Body)
	}
	saved := decode[ui.Settings](t, savedRec)
	wantBackends := []model.AgentBackend{
		{ID: "codex-preview", Name: "Preview account", Framework: model.AgentFrameworkCodex,
			Models: []string{"gpt-private-preview"}},
		{ID: "gemini-fast", Name: "Gemini account", Framework: model.AgentFrameworkAntigravity,
			Models: []string{"gemini-3.1-flash-high"}},
	}
	if !reflect.DeepEqual(saved.AgentBackends, wantBackends) {
		t.Fatalf("saved backends = %+v, want %+v", saved.AgentBackends, wantBackends)
	}

	for id, credential := range map[string]string{
		"codex-preview": "sk-preview",
		"gemini-fast":   "gemini-key",
	} {
		rec := do(t, srv, http.MethodPut, "/api/agent-backends/"+id+"/credential",
			`{"value":"  `+credential+`  "}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("saving %s credential = %d: %s", id, rec.Code, rec.Body)
		}
		got, err := secretStore.Resolve(context.Background(), secrets.AgentBackendCredentialSecret(id))
		if err != nil || got != credential {
			t.Fatalf("reloading %s credential = %q, %v; want the trimmed saved value", id, got, err)
		}
	}

	reloaded := decode[ui.Settings](t, do(t, srv, http.MethodGet, "/api/settings", ""))
	// Each save kicked model discovery, whose cache of what the provider
	// listed (Discovered) is grain's, not part of what was saved.
	for i := range reloaded.AgentBackends {
		reloaded.AgentBackends[i].Discovered = nil
	}
	if !reflect.DeepEqual(reloaded.AgentBackends, wantBackends) {
		t.Fatalf("reloaded backends = %+v, want %+v", reloaded.AgentBackends, wantBackends)
	}
}

func TestSettingsRejectionsAre400(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPut, "/api/settings", `{"pollInterval":"not-a-duration"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// The build stamp rides on /api/config (version.go) so the sidebar can
// print which grain is answering. Which of the two branches below runs
// is a property of how this test binary was built, not of the server:
// `go test` stamps no VCS information, so the unstamped contract -- the
// key absent entirely rather than an object of empty strings -- is what
// this asserts in practice, and version_test.go covers the shape of a
// stamped one directly.
func TestConfigReportsTheBuildVersion(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)

	info := version.Get()
	if info.Revision == "" && info.Release == "" && info.Edition == "" {
		if v, ok := got["version"]; ok {
			t.Fatalf("version = %v out of a binary carrying no VCS stamp, want the key absent", v)
		}
		return
	}
	v, ok := got["version"].(map[string]any)
	if !ok {
		t.Fatalf("version = %v, want an object naming commit %s", got["version"], info.Revision)
	}
	if info.Revision != "" && v["commit"] != info.Revision {
		t.Fatalf("version.commit = %v, want %q", v["commit"], info.Revision)
	}
	if info.Release != "" && v["release"] != info.Release {
		t.Fatalf("version.release = %v, want %q", v["release"], info.Release)
	}
	if info.Edition != "" && v["edition"] != info.Edition {
		t.Fatalf("version.edition = %v, want %q", v["edition"], info.Edition)
	}
}

func TestStaticFrontendIsServed(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodGet, "/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// --- secrets ----------------------------------------------------------
//
// bwsalmon/agents#357: a UI colocated with the server can set and delete
// secrets, and list which ones exist, but the API surface never hands a
// value back -- these assert on both halves, the presence of names/keys
// and the total absence of any value anywhere in a response body.

func testServerWithSecrets(t *testing.T) (*ui.Server, *secrets.Store) {
	t.Helper()
	_, store, _ := testClient(t)
	secretStore := secrets.New(t.TempDir())
	cfg := ui.Config{
		Actor:         ui.DefaultActor("alice"),
		Capabilities:  ui.OfferedCapabilities(),
		Secrets:       secretStore,
		CodexAuthFile: filepath.Join(t.TempDir(), "auth.json"),
	}
	srv := ui.NewServer(cfg, store)
	// The secrets pane is a system administrator's (GET /api/secrets).
	storeSettingsRow(t, ui.NewClient(cfg, store))
	return srv, secretStore
}

func TestSecretsDisabledByDefault(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	rec := do(t, srv, http.MethodGet, "/api/secrets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[map[string]any](t, rec)
	if got["enabled"] != false {
		t.Fatalf("enabled = %v, want false", got["enabled"])
	}
}

func TestSecretsDisabledRejectsMutation(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/secrets/db/password", `{"value":"hunter2"}`},
		{http.MethodDelete, "/api/secrets/db/password", ""},
		{http.MethodDelete, "/api/secrets/db", ""},
	} {
		rec := do(t, srv, call.method, call.path, call.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404: %s", call.method, call.path, rec.Code, rec.Body)
		}
	}
}

func TestSetSecretThenList(t *testing.T) {
	t.Parallel()
	srv, store := testServerWithSecrets(t)

	rec := do(t, srv, http.MethodPut, "/api/secrets/db/password", `{"value":"hunter2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("response body leaked the secret value: %s", rec.Body)
	}
	got := decode[map[string]any](t, rec)
	if got["enabled"] != true {
		t.Fatalf("enabled = %v, want true", got["enabled"])
	}

	// Confirmed independently of the API surface, which never reads a
	// value back: the value actually landed on disk.
	value, err := store.Resolve(context.Background(), "db/password")
	if err != nil || value != "hunter2" {
		t.Fatalf("Resolve(db/password) = %q, %v", value, err)
	}

	rec = do(t, srv, http.MethodGet, "/api/secrets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("list response leaked the secret value: %s", rec.Body)
	}
	list := decode[map[string]any](t, rec)
	secretsList, _ := list["secrets"].([]any)
	if len(secretsList) != 1 {
		t.Fatalf("secrets = %+v, want one entry", list["secrets"])
	}
}

func TestSetSecretRequiresValue(t *testing.T) {
	t.Parallel()
	srv, _ := testServerWithSecrets(t)
	rec := do(t, srv, http.MethodPut, "/api/secrets/db/password", `{"value":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestSetSecretRejectsTraversalNames(t *testing.T) {
	t.Parallel()
	srv, _ := testServerWithSecrets(t)
	rec := do(t, srv, http.MethodPut, "/api/secrets/..%2Fescape/key", `{"value":"x"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("status = %d, want a non-2xx rejection: %s", rec.Code, rec.Body)
	}
}

// --- host reboot --------------------------------------------------------
//
// bwsalmon/agents#395: a human operator's "reboot host" button. Disabled
// by default the same way secrets are (testServer's Config.Reboot is
// nil, the same as a Server nothing has wired one into) -- these assert
// on both halves, /api/config reporting whether it's available and
// POST /api/host/reboot actually calling (or refusing to call) the
// configured func.

func TestRebootDisabledByDefault(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	_ = client.Store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["rebootEnabled"] != false {
		t.Fatalf("rebootEnabled = %v, want false", got["rebootEnabled"])
	}

	rec = do(t, srv, http.MethodPost, "/api/host/reboot", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestRebootCallsConfiguredFunc(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	_ = store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval})
	calls := 0
	client.Config.Reboot = func(ctx context.Context) error {
		calls++
		return nil
	}
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["rebootEnabled"] != true {
		t.Fatalf("rebootEnabled = %v, want true", got["rebootEnabled"])
	}

	rec = do(t, srv, http.MethodPost, "/api/host/reboot", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

// --- host shutdown and daemon restart ------------------------------------
//
// grain/task-40: the two actions beside the reboot -- power the machine
// off, and restart the grain service alone without touching the machine.
// Each is its own gate, because each is its own host-side command
// (scripts/setup.sh's control units): a deployment can be able to do one
// and not the others, and the pane offers what this one can do.

func TestShutdownAndRestartDisabledByDefault(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	_ = client.Store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	for _, key := range []string{"shutdownEnabled", "restartEnabled"} {
		if got[key] != false {
			t.Fatalf("%s = %v, want false", key, got[key])
		}
	}

	for _, path := range []string{"/api/host/shutdown", "/api/host/restart"} {
		rec = do(t, srv, http.MethodPost, path, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("POST %s: status = %d, want 404: %s", path, rec.Code, rec.Body)
		}
	}
}

func TestShutdownAndRestartCallTheirOwnFuncs(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	_ = store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval})
	shutdowns, restarts := 0, 0
	client.Config.Shutdown = func(ctx context.Context) error {
		shutdowns++
		return nil
	}
	client.Config.Restart = func(ctx context.Context) error {
		restarts++
		return nil
	}
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	for _, key := range []string{"shutdownEnabled", "restartEnabled"} {
		if got[key] != true {
			t.Fatalf("%s = %v, want true", key, got[key])
		}
	}
	// Still off: this deployment wired up two of the three, and the
	// reboot button has to stay hidden rather than ride along on them.
	if got["rebootEnabled"] != false {
		t.Fatalf("rebootEnabled = %v, want false", got["rebootEnabled"])
	}

	if rec = do(t, srv, http.MethodPost, "/api/host/shutdown", ""); rec.Code != http.StatusOK {
		t.Fatalf("POST /api/host/shutdown: status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec = do(t, srv, http.MethodPost, "/api/host/restart", ""); rec.Code != http.StatusOK {
		t.Fatalf("POST /api/host/restart: status = %d, want 200: %s", rec.Code, rec.Body)
	}
	// One each, and not one of them standing in for the other: powering
	// the machine off when an operator asked for a service restart is the
	// mix-up worth pinning down here.
	if shutdowns != 1 || restarts != 1 {
		t.Fatalf("shutdowns = %d, restarts = %d, want 1 and 1", shutdowns, restarts)
	}
}

func TestShutdownSurfacesError(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	_ = store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval})
	client.Config.Shutdown = func(ctx context.Context) error {
		return errors.New("sudo: a password is required")
	}
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodPost, "/api/host/shutdown", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "a password is required") {
		t.Fatalf("body = %s, want the underlying error message", rec.Body)
	}
}

// --- auto-merge degraded -------------------------------------------------
//
// bwsalmon/agents#483: Submit sets AutoMerge, but on a deployment whose
// GitHub credential can't read check runs (Config.AutoMergeDegraded's own
// doc comment), the merge that's supposed to follow never happens, and
// nothing said so anywhere the UI could see -- Submit looked like a
// no-op. These assert /api/config reports the (un)availability nil vs.
// a real func each give, the same "expose the func, don't hardcode true"
// shape TestRebootDisabledByDefault/TestRebootCallsConfiguredFunc already
// give Config.Reboot above.

func TestAutoMergeNotDegradedByDefault(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["autoMergeDegraded"] != nil && got["autoMergeDegraded"] != false {
		t.Fatalf("autoMergeDegraded = %v, want false or omitted", got["autoMergeDegraded"])
	}
}

func TestAutoMergeDegradedReportsConfiguredFunc(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.AutoMergeDegraded = func() bool { return true }
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["autoMergeDegraded"] != true {
		t.Fatalf("autoMergeDegraded = %v, want true", got["autoMergeDegraded"])
	}
}

// --- reconciler down -------------------------------------------------
//
// bwsalmon/agents#576: runDaemon dying (a one-time setup step that
// exhausted its own retries, or any other fatal-but-non-crashing error)
// used to be visible only in a server log line -- the UI/API server
// stays up regardless (bwsalmon/agents#550), so nothing said, anywhere
// the UI could see, that reconciliation itself had actually stopped.
// These mirror TestAutoMergeNotDegradedByDefault/
// TestAutoMergeDegradedReportsConfiguredFunc immediately above: the same
// "expose the func, don't hardcode true" shape, for Config.
// ReconcilerDown instead of Config.AutoMergeDegraded.

func TestReconcilerNotDownByDefault(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["reconcilerDown"] != nil && got["reconcilerDown"] != false {
		t.Fatalf("reconcilerDown = %v, want false or omitted", got["reconcilerDown"])
	}
}

func TestReconcilerDownReportsConfiguredFunc(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.ReconcilerDown = func() bool { return true }
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["reconcilerDown"] != true {
		t.Fatalf("reconcilerDown = %v, want true", got["reconcilerDown"])
	}
}

// A daemon on its way down for a restart says so on the call the UI
// polls, so the banner for the gap that follows can say "restarting"
// (docs/non-disruptive-upgrade.md, Stage 1).
func TestConfigReportsRestarting(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if got := decode[map[string]any](t, rec); got["restarting"] != nil {
		t.Fatalf("restarting = %v with no Config.Restarting, want omitted", got["restarting"])
	}

	client, _, _ := testClient(t)
	client.Config.Restarting = func() bool { return true }
	srv = ui.NewServerWithClient(client)
	rec = do(t, srv, http.MethodGet, "/api/config", "")
	if got := decode[map[string]any](t, rec); got["restarting"] != true {
		t.Fatalf("restarting = %v, want true", got["restarting"])
	}
}

// A daemon with little to drain stops answering within milliseconds of
// being told to, so the frontend cannot count on a GET /api/config
// landing in between. GET /api/restart-watch is already in flight when
// the shutdown starts, answers it as it does, and http.Server.Shutdown
// waits for that answer to go out before the process can exit
// (grain/task-3543).
func TestRestartWatchAnswersAsShutdownStarts(t *testing.T) {
	t.Parallel()
	shutdown := make(chan struct{})
	client, _, _ := testClient(t)
	client.Config.ShutdownStarted = shutdown
	client.Config.LocalAPIToken = testAccessCode
	ts := httptest.NewUnstartedServer(ui.NewServerWithClient(client))
	active := make(chan struct{}, 1)
	ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateActive {
			select {
			case active <- struct{}{}:
			default:
			}
		}
	}
	ts.Start()
	t.Cleanup(ts.Close)

	type result struct {
		body map[string]any
		err  error
	}
	answered := make(chan result, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/restart-watch", nil)
		if err != nil {
			answered <- result{err: err}
			return
		}
		req.Header.Set("Authorization", "Bearer "+testAccessCode)
		resp, err := ts.Client().Do(req)
		if err != nil {
			answered <- result{err: err}
			return
		}
		defer resp.Body.Close()
		var body map[string]any
		err = json.NewDecoder(resp.Body).Decode(&body)
		answered <- result{body: body, err: err}
	}()
	<-active
	select {
	case r := <-answered:
		t.Fatalf("the watch answered %v (%v) before the shutdown started, want it held", r.body, r.err)
	case <-time.After(50 * time.Millisecond):
	}

	// What run() does on SIGTERM: the context is cancelled, then the
	// server is shut down -- here at once, with nothing else to drain.
	close(shutdown)
	if err := ts.Config.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case r := <-answered:
		if r.err != nil {
			t.Fatalf("the watch failed instead of answering: %v", r.err)
		}
		if r.body["restarting"] != true {
			t.Fatalf("the watch answered %v, want restarting: true", r.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the watch never answered")
	}
}

// Already restarting answers at once, and a deployment with nothing to
// watch (no ShutdownStarted, no Restarting) does not hold the request.
func TestRestartWatchAnswersAtOnceWhenItHasNothingToWaitFor(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodGet, "/api/restart-watch", "")
	if got := decode[map[string]any](t, rec); got["restarting"] != false {
		t.Fatalf("restarting = %v with nothing wired, want false", got["restarting"])
	}

	client, _, _ := testClient(t)
	client.Config.Restarting = func() bool { return true }
	client.Config.ShutdownStarted = make(chan struct{})
	srv = ui.NewServerWithClient(client)
	rec = do(t, srv, http.MethodGet, "/api/restart-watch", "")
	if got := decode[map[string]any](t, rec); got["restarting"] != true {
		t.Fatalf("restarting = %v while restarting, want true", got["restarting"])
	}
}

// ?quick=1 is the frontend asking whether grain is answering again after
// an ask of its own failed: it is answered at once even where the watch
// would otherwise be held (grain/task-2258).
func TestRestartWatchQuickDoesNotHold(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.Restarting = func() bool { return false }
	client.Config.ShutdownStarted = make(chan struct{})
	srv := ui.NewServerWithClient(client)
	done := make(chan map[string]any, 1)
	go func() {
		rec := do(t, srv, http.MethodGet, "/api/restart-watch?quick=1", "")
		done <- decode[map[string]any](t, rec)
	}()
	select {
	case got := <-done:
		if got["restarting"] != false {
			t.Fatalf("restarting = %v, want false", got["restarting"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("?quick=1 was held, want an answer at once")
	}
}

// --- state that lives on one disk --------------------------------------
//
// grain/task-27: a local-only deployment's whole history and its secrets
// key exist on this host and nowhere else. The State pane says so in
// full, but only to somebody who opens it -- so the fact rides on the one
// call every view already polls, and the sidebar draws a marker from it.

func TestConfigReportsTheConfigRepo(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.StateRepoRef = func() model.RepoRef {
		return model.RepoRef{Owner: "acme", Name: "grain-config"}
	}
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["configRepo"] != "acme/grain-config" {
		t.Fatalf("configRepo = %v, want acme/grain-config", got["configRepo"])
	}
}

func TestConfigRepoCannotBeRemoved(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	client.Config.StateRepoRef = func() model.RepoRef {
		return model.RepoRef{Owner: "acme", Name: "grain-config"}
	}

	if _, err := client.RemoveTargetRepo(ctx, "ACME/Grain-Config"); err == nil || !strings.Contains(err.Error(), "cannot be removed") {
		t.Fatalf("RemoveTargetRepo(config repo): error = %v, want cannot be removed", err)
	}
}

// A merged settings change that replaced a value and could not be
// reported elsewhere rides on this route as a bare boolean: the values
// belong on the call the pane makes when it opens, not on every poll.
func TestAnUndeliveredResetNoticeIsReportedOnTheConfig(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.StateRepoResetNotice = func() bool { return true }
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["stateRepoResetNotice"] != true {
		t.Fatalf("stateRepoResetNotice = %v, want true", got["stateRepoResetNotice"])
	}
}

func TestNoResetNoticeIsReportedByDefault(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["stateRepoResetNotice"] != nil && got["stateRepoResetNotice"] != false {
		t.Fatalf("stateRepoResetNotice = %v, want false or omitted", got["stateRepoResetNotice"])
	}
}

// --- sandbox shape ignored -------------------------------------------
//
// grain/task-26: the new-task form's per-task sandbox shape override
// reads GET /api/config and nothing else, so the flag the Settings pane
// already gets from GET /api/settings (grain/task-9) has to ride here
// too. On a backend with no shape at all the override is not merely
// ignored -- HostSandboxes.Acquire refuses it and the run dies at
// sandbox setup -- so the form has to be able to see it before the task
// is filed. Same two-test shape as the flags above.

func TestSandboxShapeIgnoredNotReportedByDefault(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["sandboxShapeIgnored"] != nil && got["sandboxShapeIgnored"] != false {
		t.Fatalf("sandboxShapeIgnored = %v, want false or omitted", got["sandboxShapeIgnored"])
	}
}

func TestSandboxShapeIgnoredReportedWhenConfigured(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.SandboxShapeIgnored = true
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["sandboxShapeIgnored"] != true {
		t.Fatalf("sandboxShapeIgnored = %v, want true", got["sandboxShapeIgnored"])
	}
}

// hostSandboxes is what the frontend's "no isolation" alert reads, and it
// comes from the backend the daemon is actually running, not a flag of
// its own.
func TestConfigReportsHostSandboxesFromTheRunningBackend(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		backend model.SandboxBackend
		want    bool
	}{
		{model.SandboxBackendHost, true},
		{model.SandboxBackendContainer, false},
		{"", false},
	} {
		client, _, _ := testClient(t)
		client.Config.RunningConfig = func() model.Config { return model.Config{SandboxBackend: tc.backend} }
		srv := ui.NewServerWithClient(client)

		got := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/config", ""))
		if (got["hostSandboxes"] == true) != tc.want {
			t.Errorf("backend %q: hostSandboxes = %v, want %v", tc.backend, got["hostSandboxes"], tc.want)
		}
	}
}

// --- Authentication & Allowed User List checks ---

func TestUnauthenticatedAccessAllowed(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	// Unauthenticated request to /api/config
	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decode[map[string]any](t, rec)
	if got["authenticated"] != false {
		t.Fatalf("authenticated = %v, want false", got["authenticated"])
	}
	if got["allowed"] != true {
		t.Fatalf("allowed = %v, want true", got["allowed"])
	}

	// Unauthenticated request to protected endpoint /api/tasks
	rec = do(t, srv, http.MethodGet, "/api/tasks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/tasks status = %d, want 200", rec.Code)
	}
}

func TestRebootSurfacesError(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	_ = store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval})
	client.Config.Reboot = func(ctx context.Context) error {
		return errors.New("sudo: a password is required")
	}
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodPost, "/api/host/reboot", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "a password is required") {
		t.Fatalf("body = %s, want the underlying error message", rec.Body)
	}
}

func TestDeleteSecretKeyLeavesOtherKeys(t *testing.T) {
	t.Parallel()
	srv, store := testServerWithSecrets(t)
	if err := store.Set("db", "username", []byte("app")); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("db", "password", []byte("hunter2")); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodDelete, "/api/secrets/db/password", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if _, err := store.Resolve(context.Background(), "db/password"); err == nil {
		t.Fatal("expected password to be gone")
	}
	if v, err := store.Resolve(context.Background(), "db/username"); err != nil || v != "app" {
		t.Fatalf("username should survive: %q, %v", v, err)
	}
}

func TestDeleteWholeSecret(t *testing.T) {
	t.Parallel()
	srv, store := testServerWithSecrets(t)
	if err := store.Set("db", "username", []byte("app")); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodDelete, "/api/secrets/db", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("got %+v, want no secrets left", list)
	}
}

func TestDeleteMissingSecretIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServerWithSecrets(t)
	rec := do(t, srv, http.MethodDelete, "/api/secrets/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// --- upgrade ------------------------------------------------------------
//
// bwsalmon/agents#396: fakeUpgrader stands in for a real
// *upgrade.Upgrader (which would need a real git checkout, docker
// daemon, and restart command behind it) so these can assert on the
// route wiring -- disabled-by-default, the branch/status shape, and
// error mapping -- without any of that. pkg/upgrade's own tests already
// cover the checkout/build/install/restart pipeline itself.

type fakeUpgrader struct {
	startErr error
	status   upgrade.Status
	started  string
	// mode is what the pipeline this stands in for would run; the zero
	// value means the image path, which is what every deployment
	// scripts/setup.sh installs actually runs.
	mode upgrade.Mode
	// running is where this stand-in deployment already is, and
	// runningErr the "cannot say" a real one gives when its ref file or
	// its installed binary cannot be read. Both zero means a deployment
	// with nothing to report and nothing wrong with it, which is what
	// every test that is not about this field wants.
	running    upgrade.Running
	runningErr error
}

func (f *fakeUpgrader) Start(branch string) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.started = branch
	f.status.Branch = branch
	f.status.Phase = upgrade.PhaseRunning
	return nil
}

func (f *fakeUpgrader) Status() (upgrade.Status, error) { return f.status, nil }

// ValidateBranch makes only the format half of the real check: this
// stand-in has no allowlist of its own, and the tests that drive it are
// about scheduling rather than about which branches are trusted.
func (f *fakeUpgrader) ValidateBranch(branch string) (string, error) {
	return upgrade.ValidBranchName(branch)
}

func (f *fakeUpgrader) Mode() upgrade.Mode {
	if f.mode == "" {
		return upgrade.ModeImage
	}
	return f.mode
}

func (f *fakeUpgrader) Running() (upgrade.Running, error) { return f.running, f.runningErr }

func testServerWithUpgrader(t *testing.T, up ui.Upgrader) *ui.Server {
	t.Helper()
	_, store, ctx := testClient(t)
	cfg := model.DefaultConfig()
	_ = store.PutConfig(ctx, cfg)
	uicfg := ui.Config{Actor: ui.DefaultActor("alice"), Capabilities: ui.OfferedCapabilities(), Upgrader: up}
	return ui.NewServer(uicfg, store)
}

func TestUpgradeDisabledByDefault(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	_ = client.Store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})

	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[map[string]any](t, rec)
	if got["enabled"] != false {
		t.Fatalf("enabled = %v, want false", got["enabled"])
	}

	rec = do(t, srv, http.MethodPost, "/api/upgrade", `{"branch":"main"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("post status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestStartUpgradeRoute(t *testing.T) {
	t.Parallel()
	fake := &fakeUpgrader{}
	srv := testServerWithUpgrader(t, fake)

	rec := do(t, srv, http.MethodPost, "/api/upgrade", `{"branch":"grain/issue-396"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if fake.started != "grain/issue-396" {
		t.Fatalf("Start was called with %q, want %q", fake.started, "grain/issue-396")
	}
	got := decode[map[string]any](t, rec)
	if got["enabled"] != true || got["branch"] != "grain/issue-396" || got["phase"] != "running" {
		t.Fatalf("response = %+v", got)
	}

	rec = do(t, srv, http.MethodGet, "/api/upgrade", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got = decode[map[string]any](t, rec)
	if got["phase"] != "running" {
		t.Fatalf("status phase = %v, want running", got["phase"])
	}
}

func TestStartUpgradeRequiresBranch(t *testing.T) {
	t.Parallel()
	srv := testServerWithUpgrader(t, &fakeUpgrader{})
	rec := do(t, srv, http.MethodPost, "/api/upgrade", `{"branch":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestStartUpgradeRejectsWhenAlreadyRunning(t *testing.T) {
	t.Parallel()
	fake := &fakeUpgrader{startErr: upgrade.ErrUpgradeInProgress}
	srv := testServerWithUpgrader(t, fake)
	rec := do(t, srv, http.MethodPost, "/api/upgrade", `{"branch":"main"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}
