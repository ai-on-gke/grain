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
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestTaskListCacheDoesNotRebuildOnLiveActivityOrTranscript(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()

	runTask := decode[ui.Task](t, do(t, srv, http.MethodPost, "/api/tasks", `{"title":"running work","approved":true}`))
	_ = decode[ui.Task](t, do(t, srv, http.MethodPost, "/api/tasks", `{"title":"queued work","approved":true}`))
	running(t, client.Store, ctx, runTask.ID)

	// Prime live list, full list, and filtered list caches.
	live1 := listWith(t, srv, "")
	if live1.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks = %d", live1.Code)
	}
	liveETag1 := live1.Header().Get("ETag")
	if rec := do(t, srv, http.MethodGet, "/api/tasks?fields=full", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks?fields=full = %d", rec.Code)
	}
	if rec := do(t, srv, http.MethodGet, "/api/tasks?state=queued", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks?state=queued = %d", rec.Code)
	}
	if rec := do(t, srv, http.MethodGet, "/api/inbox", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/inbox = %d", rec.Code)
	}

	buildsBefore := ui.TaskListBuilds(srv)

	// Heartbeat + transcript writes + flush must not invalidate the cached list snapshots.
	if _, err := client.Store.SetTaskActivity(ctx, runTask.ID, "running unit tests", baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SetTaskActivity: %v", err)
	}
	if err := client.Store.SetRunTranscript(ctx, "run-"+runTask.ID, "test output line 1"); err != nil {
		t.Fatalf("SetRunTranscript: %v", err)
	}
	if err := client.Store.FlushLiveRunWrites(ctx); err != nil {
		t.Fatalf("FlushLiveRunWrites: %v", err)
	}

	// Live list immediately surfaces the new activity without re-reading the whole store.
	live2 := listWith(t, srv, liveETag1)
	if live2.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks after activity = %d, want 200", live2.Code)
	}
	rows := decode[[]ui.Task](t, live2)
	var found bool
	for _, r := range rows {
		if r.ID == runTask.ID {
			found = true
			if r.Activity != "running unit tests" {
				t.Fatalf("listed activity = %q, want running unit tests", r.Activity)
			}
		}
	}
	if !found {
		t.Fatalf("running task %s missing from list", runTask.ID)
	}
	liveETag2 := live2.Header().Get("ETag")

	// Quiet poll returns 304.
	if rec := listWith(t, srv, liveETag2); rec.Code != http.StatusNotModified {
		t.Fatalf("quiet poll after activity = %d, want 304", rec.Code)
	}
	// Filtered running list also sees the fresh activity.
	filtered := do(t, srv, http.MethodGet, "/api/tasks?state=running", "")
	if filtered.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks?state=running = %d", filtered.Code)
	}
	frows := decode[[]ui.Task](t, filtered)
	if len(frows) != 1 || frows[0].Activity != "running unit tests" {
		t.Fatalf("filtered running tasks = %+v, want updated activity", frows)
	}
	// Queued list and inbox also hit cache.
	if rec := do(t, srv, http.MethodGet, "/api/tasks?state=queued", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks?state=queued = %d", rec.Code)
	}
	if rec := do(t, srv, http.MethodGet, "/api/inbox", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/inbox = %d", rec.Code)
	}

	if got := ui.TaskListBuilds(srv); got != buildsBefore {
		t.Fatalf("live-run activity/transcript caused %d task-list store rebuild(s), want 0", got-buildsBefore)
	}
}

func TestTaskDetailCacheIsolatesLiveActivityPerTask(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()

	taskA := decode[ui.Task](t, do(t, srv, http.MethodPost, "/api/tasks", `{"title":"task A","approved":true}`))
	taskB := decode[ui.Task](t, do(t, srv, http.MethodPost, "/api/tasks", `{"title":"task B","approved":true}`))
	taskC := decode[ui.Task](t, do(t, srv, http.MethodPost, "/api/tasks", `{"title":"task C","approved":true}`))
	running(t, client.Store, ctx, taskA.ID)
	running(t, client.Store, ctx, taskC.ID)

	etagA := detailWith(t, srv, taskA.ID, "").Header().Get("ETag")
	etagB := detailWith(t, srv, taskB.ID, "").Header().Get("ETag")
	etagC := detailWith(t, srv, taskC.ID, "").Header().Get("ETag")
	buildsBefore := ui.TaskDetailBuilds(srv)

	if _, err := client.Store.SetTaskActivity(ctx, taskA.ID, "compiling pkg/model", baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SetTaskActivity A: %v", err)
	}
	if err := client.Store.SetRunTranscript(ctx, "run-"+taskA.ID, "transcript A"); err != nil {
		t.Fatalf("SetRunTranscript A: %v", err)
	}
	if err := client.Store.FlushLiveRunWrites(ctx); err != nil {
		t.Fatalf("FlushLiveRunWrites: %v", err)
	}

	// Non-running task B and other running task C must remain 304 cache hits.
	if rec := detailWith(t, srv, taskB.ID, etagB); rec.Code != http.StatusNotModified {
		t.Fatalf("task B detail after task A heartbeat = %d, want 304", rec.Code)
	}
	if rec := detailWith(t, srv, taskC.ID, etagC); rec.Code != http.StatusNotModified {
		t.Fatalf("task C detail after task A heartbeat = %d, want 304", rec.Code)
	}
	if got := ui.TaskDetailBuilds(srv); got != buildsBefore {
		t.Fatalf("unrelated task details rebuilt %d time(s) after task A heartbeat, want 0", got-buildsBefore)
	}

	// Task A itself immediately serves its updated activity.
	recA := detailWith(t, srv, taskA.ID, etagA)
	if recA.Code != http.StatusOK {
		t.Fatalf("task A detail after its own heartbeat = %d, want 200", recA.Code)
	}
	detailA := decode[ui.TaskDetail](t, recA)
	if detailA.Activity != "compiling pkg/model" {
		t.Fatalf("task A detail activity = %q, want compiling pkg/model", detailA.Activity)
	}
	etagA2 := recA.Header().Get("ETag")
	buildsAfterA := ui.TaskDetailBuilds(srv)

	// A transcript-only update on task A must not invalidate task A's detail cache.
	if err := client.Store.SetRunTranscript(ctx, "run-"+taskA.ID, "transcript A v2"); err != nil {
		t.Fatalf("SetRunTranscript A v2: %v", err)
	}
	if err := client.Store.FlushLiveRunWrites(ctx); err != nil {
		t.Fatalf("FlushLiveRunWrites 2: %v", err)
	}
	if rec := detailWith(t, srv, taskA.ID, etagA2); rec.Code != http.StatusNotModified {
		t.Fatalf("task A detail after transcript-only write = %d, want 304", rec.Code)
	}
	if got := ui.TaskDetailBuilds(srv); got != buildsAfterA {
		t.Fatalf("transcript-only write caused %d task detail rebuild(s), want 0", got-buildsAfterA)
	}
}
