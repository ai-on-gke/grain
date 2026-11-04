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

// GET /api/tasks as the live list (grain task 2983): what the UI polls
// every three seconds carries the tasks that are not closed and none of
// their descriptions, closed tasks are paged in on demand, and every
// response is gzipped for a caller that asks.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fileListedTask files one approved task and returns its id.
func fileListedTask(t *testing.T, srv *ui.Server, title, description string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"title": title, "description": description, "approved": true})
	rec := do(t, srv, http.MethodPost, "/api/tasks", string(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("filing %q: status %d: %s", title, rec.Code, rec.Body)
	}
	return decode[ui.Task](t, rec).ID
}

func closeListedTask(t *testing.T, srv *ui.Server, id string) {
	t.Helper()
	if rec := do(t, srv, http.MethodPost, "/api/tasks/"+id+"/close", ""); rec.Code != http.StatusOK {
		t.Fatalf("closing %s: status %d: %s", id, rec.Code, rec.Body)
	}
}

// TestTaskListPollDoesNotGrowWithClosedTasks is the director's own
// acceptance test: the bytes a poll costs track live work, not history.
// Fifty closed tasks with long descriptions later, the poll is the same
// poll, byte for byte.
func TestTaskListPollDoesNotGrowWithClosedTasks(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	long := strings.Repeat("A paragraph of what a task asks for. ", 100)
	var open []string
	for i := 0; i < 3; i++ {
		open = append(open, fileListedTask(t, srv, fmt.Sprintf("open %d", i), long))
	}
	before := listWith(t, srv, "")
	if before.Code != http.StatusOK {
		t.Fatalf("list status = %d", before.Code)
	}

	for i := 0; i < 50; i++ {
		closeListedTask(t, srv, fileListedTask(t, srv, fmt.Sprintf("history %d", i), long))
	}
	after := listWith(t, srv, "")
	if after.Body.Len() != before.Body.Len() {
		t.Errorf("the poll was %d bytes with no closed tasks and %d with fifty, want the same", before.Body.Len(), after.Body.Len())
	}
	listed := decode[[]ui.Task](t, after)
	if len(listed) != len(open) {
		t.Fatalf("polled %d tasks, want the %d open ones", len(listed), len(open))
	}
	for _, task := range listed {
		if task.State == model.StateClosed {
			t.Errorf("the poll carried closed task %s", task.ID)
		}
	}

	// And a task that closes drops out of the next poll.
	closeListedTask(t, srv, open[0])
	if got := decode[[]ui.Task](t, listWith(t, srv, "")); len(got) != len(open)-1 {
		t.Errorf("after closing one, polled %d tasks, want %d", len(got), len(open)-1)
	}
}

// TestTaskListOmitsDescriptions: the list leaves descriptions to the
// detail fetch, which still has them, and ?fields=full is the old answer
// for the CLI -- every task, closed included, description and all.
func TestTaskListOmitsDescriptions(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	id := fileListedTask(t, srv, "open", "the open one's description")
	closed := fileListedTask(t, srv, "closed", "the closed one's description")
	closeListedTask(t, srv, closed)

	rec := listWith(t, srv, "")
	if strings.Contains(rec.Body.String(), `"description"`) {
		t.Errorf("the polled list carries descriptions: %s", rec.Body)
	}
	detail := do(t, srv, http.MethodGet, "/api/tasks/"+id, "")
	if !strings.Contains(detail.Body.String(), "the open one's description") {
		t.Errorf("GET /api/tasks/%s lost its description: %s", id, detail.Body)
	}

	full := decode[[]ui.Task](t, do(t, srv, http.MethodGet, "/api/tasks?fields=full", ""))
	if len(full) != 2 {
		t.Fatalf("fields=full listed %d tasks, want both", len(full))
	}
	for _, task := range full {
		if task.Description == "" {
			t.Errorf("fields=full left out task %s's description", task.ID)
		}
	}

	// The CLI's own client reads the full list.
	if !strings.Contains(do(t, srv, http.MethodGet, "/api/tasks?state=closed", "").Body.String(), "the closed one's description") {
		t.Error("?state=closed (the CLI's --state) lost the closed task's description")
	}
}

// TestTaskListPagesClosedTasksNewestClosedFirst: a view showing closed
// work pages it in -- every closed task once, newest-closed first, the
// last page saying so -- and searches it on the server, descriptions
// included.
func TestTaskListPagesClosedTasksNewestClosedFirst(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	fileListedTask(t, srv, "still open", "")
	var closed []string
	for i := 0; i < 7; i++ {
		id := fileListedTask(t, srv, fmt.Sprintf("history %d", i), fmt.Sprintf("needle-%d", i))
		closeListedTask(t, srv, id)
		closed = append(closed, id)
	}

	type page struct {
		Tasks      []ui.Task `json:"tasks"`
		NextCursor string    `json:"nextCursor"`
	}
	var got []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging never ended")
		}
		rec := do(t, srv, http.MethodGet, "/api/tasks?state=closed&limit=3&cursor="+url.QueryEscape(cursor), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("page status = %d: %s", rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), `"description"`) {
			t.Errorf("a page of closed tasks carries descriptions: %s", rec.Body)
		}
		p := decode[page](t, rec)
		for _, task := range p.Tasks {
			got = append(got, task.ID)
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(got) != len(closed) {
		t.Fatalf("paged %v, want every one of %v once", got, closed)
	}
	for i, id := range got {
		if want := closed[len(closed)-1-i]; id != want {
			t.Errorf("page order %v, want newest-closed first (%s at %d)", got, want, i)
			break
		}
	}

	found := decode[page](t, do(t, srv, http.MethodGet, "/api/tasks?state=closed&cursor=&search=needle-4", ""))
	if len(found.Tasks) != 1 || found.Tasks[0].ID != closed[4] {
		t.Errorf("searching closed tasks' descriptions found %+v, want task %s", found.Tasks, closed[4])
	}
	for query, want := range map[string]int{
		"closedSince=2000-01-01T00:00:00Z":  len(closed),
		"closedSince=2999-01-01T00:00:00Z":  0,
		"closedBefore=2000-01-01T00:00:00Z": 0,
	} {
		if n := len(decode[page](t, do(t, srv, http.MethodGet, "/api/tasks?cursor=&"+query, "")).Tasks); n != want {
			t.Errorf("%s paged %d tasks, want %d", query, n, want)
		}
	}
}

// TestTaskListPagesCountEveryClosedTask: every page says how many closed
// tasks match across all pages, not how many it holds, so the UI's rail
// can count closed work it has not paged in (2026-11-04 walkthrough: 205
// closed tasks read "Closed 200").
func TestTaskListPagesCountEveryClosedTask(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	fileListedTask(t, srv, "still open", "")
	for i := 0; i < 7; i++ {
		closeListedTask(t, srv, fileListedTask(t, srv, fmt.Sprintf("history %d", i), fmt.Sprintf("needle-%d", i)))
	}
	type page struct {
		Tasks      []ui.Task `json:"tasks"`
		NextCursor string    `json:"nextCursor"`
		Total      *int      `json:"total"`
		Merged     *int      `json:"merged"`
		Sessions   *int      `json:"sessions"`
	}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging never ended")
		}
		p := decode[page](t, do(t, srv, http.MethodGet, "/api/tasks?state=closed&limit=3&cursor="+url.QueryEscape(cursor), ""))
		if p.Total == nil || *p.Total != 7 {
			t.Errorf("page %d says total %v, want 7 (every closed task, on every page)", pages, p.Total)
		}
		if p.Merged == nil || *p.Merged != 0 || p.Sessions == nil || *p.Sessions != 0 {
			t.Errorf("page %d says merged %v, sessions %v, want 0 and 0", pages, p.Merged, p.Sessions)
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	found := decode[page](t, do(t, srv, http.MethodGet, "/api/tasks?state=closed&cursor=&search=needle-4", ""))
	if found.Total == nil || *found.Total != 1 {
		t.Errorf("a search's page says total %v, want the 1 task it matches", found.Total)
	}
}

// TestTaskListIsGzippedForACallerThatAsks: the same list, a fraction of
// the bytes, a weak ETag a poller's If-None-Match still matches, and a
// Vary that keeps a cache from handing it to anybody who did not ask.
func TestTaskListIsGzippedForACallerThatAsks(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	for i := 0; i < 20; i++ {
		fileListedTask(t, srv, fmt.Sprintf("task %d", i), "")
	}
	plain := listWith(t, srv, "")

	rec := doWithHeaders(t, srv, http.MethodGet, "/api/tasks", "", map[string]string{"Accept-Encoding": "gzip, deflate, br"})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	etag := rec.Header().Get("ETag")
	if etag != "W/"+plain.Header().Get("ETag") {
		t.Errorf("gzipped ETag = %q, want the weak form of %q", etag, plain.Header().Get("ETag"))
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, plain.Body.Bytes()) {
		t.Error("the gzipped list does not decompress to the plain one")
	}
	if rec.Body.Len() >= plain.Body.Len() {
		t.Errorf("gzipped %d bytes, plain %d: nothing saved", rec.Body.Len(), plain.Body.Len())
	}

	again := doWithHeaders(t, srv, http.MethodGet, "/api/tasks", "", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag})
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("a poller holding the weak ETag got %d with %d bytes, want an empty 304", again.Code, again.Body.Len())
	}
	if refused := doWithHeaders(t, srv, http.MethodGet, "/api/tasks", "", map[string]string{"Accept-Encoding": "gzip;q=0"}); refused.Header().Get("Content-Encoding") != "" {
		t.Error("gzip;q=0 was answered gzipped")
	}
}

// TestTaskListSizeReport is the measurement the pull request reports: a
// store of about three thousand tasks, most of them closed, with
// descriptions of the length real ones run to. It files every task over
// HTTP, so it runs only when GRAIN_TASK_LIST_SIZE_REPORT is set.
func TestTaskListSizeReport(t *testing.T) {
	if os.Getenv("GRAIN_TASK_LIST_SIZE_REPORT") == "" {
		t.Skip("set GRAIN_TASK_LIST_SIZE_REPORT=1 to measure")
	}
	srv, _ := testServer(t)
	// About 4 KB a description, which is 11.8 MB over 2,981 tasks, of
	// words drawn at random so that gzip has prose to work on rather than
	// one sentence repeated.
	words := strings.Fields("the a task list poll every closed open branch merge review agent run sandbox deployment " +
		"description server client request response measure bytes check failing passing commit push repository " +
		"pkg/ui/tasks.go App.jsx 2026-11-03 ETag gzip status queued running awaiting reply submit operator")
	rng := rand.New(rand.NewPCG(1, 2))
	prose := func(n int) string {
		var b strings.Builder
		for b.Len() < n {
			b.WriteString(words[rng.IntN(len(words))])
			b.WriteByte(' ')
		}
		return b.String()
	}
	const total, live = 2981, 117
	for i := 0; i < total; i++ {
		id := fileListedTask(t, srv, prose(60), prose(4000))
		if i >= live {
			closeListedTask(t, srv, id)
		}
	}
	measure := func(path string) {
		plain := do(t, srv, http.MethodGet, path, "")
		zipped := doWithHeaders(t, srv, http.MethodGet, path, "", map[string]string{"Accept-Encoding": "gzip"})
		start := time.Now()
		var v any
		_ = json.Unmarshal(plain.Body.Bytes(), &v)
		t.Logf("%-40s %10d bytes, %9d gzipped, Go json parse %v", path, plain.Body.Len(), zipped.Body.Len(), time.Since(start))
	}
	measure("/api/tasks?fields=full")
	measure("/api/tasks")
	measure("/api/tasks?state=closed&cursor=")
}
