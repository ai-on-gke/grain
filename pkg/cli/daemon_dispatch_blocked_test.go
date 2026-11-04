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

// A daemon whose git proxy cannot bind dispatches nothing, and used to
// say so only in its journal and as GET /api/config's reconcilerDown
// boolean: a task queued behind it read "queued/ready" for as long as it
// sat there, and GET /api/sandboxes reported sandbox statistics for a
// deployment that would never build another sandbox. An agent on the
// API could not learn what an operator with journalctl could.
//
// This drives run() with the git proxy's port taken, as
// TestAGitProxyPortTakenFailsOnlyTheGitProxy does, and reads the
// condition back through every surface that has to carry it: the health
// route lists it as a failing check with its error and since when, a
// queued task's reason names it, the pipeline feed has the moment it
// was set -- and every one of them clears when the condition does.
package cli

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestAGitProxyBindFailureIsADispatchConditionOnHealthAndQueuedTasks(t *testing.T) {
	// Both are process-global (globals.go); a failed run here must not
	// leak into another test's expectations.
	t.Cleanup(func() {
		daemon.ReconcilerDown.Store(false)
		for _, c := range daemon.DispatchConditions.Active() {
			daemon.DispatchConditions.Clear(c.Name)
		}
	})

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")
	seedTestAccessCode(t, dataDir)

	base := freeAlignedBlock(t)
	taken, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(base+daemon.OffsetGitProxy)))
	if err != nil {
		t.Fatalf("occupying the git proxy's port: %v", err)
	}
	defer taken.Close()

	uiAddr := freeTCPAddr(t)
	cfg := daemon.Config{
		DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
		GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true,
		UiAddr: uiAddr, Actor: "tester",
		SandboxPortBase: base,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- daemon.Run(ctx, cfg) }()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)

	// Wait for runDaemon to have given up, so what follows reads the
	// daemon after the failure rather than racing its startup.
	deadline := time.Now().Add(daemonWaitLimit)
	for !daemon.ReconcilerDown.Load() {
		if time.Now().After(deadline) {
			cancel()
			<-runErr
			t.Fatal("runDaemon never reported failing with the git proxy's port taken")
		}
		select {
		case err := <-runErr:
			t.Fatalf("run() returned (%v) before ctx was cancelled", err)
		case <-time.After(20 * time.Millisecond):
		}
	}

	// The health route: a named, failing check with the error and the
	// time, not sandbox statistics alone.
	health, err := client.SandboxHealth(ctx)
	if err != nil {
		t.Fatalf("GET /api/sandboxes: %v", err)
	}
	if len(health.DispatchBlocked) != 1 {
		t.Fatalf("health.dispatchBlocked = %+v, want the one git-proxy condition", health.DispatchBlocked)
	}
	cond := health.DispatchBlocked[0]
	if cond.Name != daemon.ConditionGitProxy || !strings.Contains(cond.Error, "address already in use") ||
		cond.Since.IsZero() || cond.Held == "" || !strings.HasPrefix(cond.Check, "dispatch blocked") || cond.Remedy == "" {
		t.Errorf("health.dispatchBlocked[0] = %+v, want git-proxy, the bind error, since, held, a check line and a remedy", cond)
	}

	// GET /api/config carries the same conditions beside reconcilerDown.
	var config struct {
		ReconcilerDown  bool                   `json:"reconcilerDown"`
		DispatchBlocked []ui.DispatchCondition `json:"dispatchBlocked"`
	}
	getJSON(t, "http://"+uiAddr+"/api/config", &config)
	if !config.ReconcilerDown || len(config.DispatchBlocked) != 1 || config.DispatchBlocked[0].Name != daemon.ConditionGitProxy {
		t.Errorf("GET /api/config = %+v, want reconcilerDown with the git-proxy condition beside it", config)
	}

	// A task queued behind the failure says why it is not being
	// dispatched, in its own reason.
	created, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "queued behind a dead git proxy", Repo: "acme/widgets"})
	if err != nil {
		t.Fatalf("filing a task: %v", err)
	}
	if created.State != model.StateQueued {
		if err := client.Approve(ctx, created.ID); err != nil {
			t.Fatalf("approving the task: %v", err)
		}
	}
	detail, err := client.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GET /api/tasks/%s: %v", created.ID, err)
	}
	if detail.State != model.StateQueued {
		t.Fatalf("task state = %s, want queued", detail.State)
	}
	if detail.Reason == nil || detail.Reason.Reason != "queued/dispatch_blocked" ||
		!strings.Contains(detail.Reason.Resource, "git proxy") || !strings.Contains(detail.Reason.Resource, "address already in use") {
		t.Errorf("queued task's reason = %+v, want queued/dispatch_blocked naming the git proxy's bind error", detail.Reason)
	}
	list, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatalf("GET /api/tasks: %v", err)
	}
	for _, row := range list {
		if row.ID == created.ID && (row.Reason == nil || row.Reason.Reason != "queued/dispatch_blocked") {
			t.Errorf("the task list's row reason = %+v, want queued/dispatch_blocked too", row.Reason)
		}
	}

	// The pipeline feed has the moment it was set.
	var feed struct {
		Changes []ui.PipelineChange `json:"changes"`
	}
	getJSON(t, "http://"+uiAddr+"/api/pipeline/changes", &feed)
	if !hasDispatchChange(feed.Changes, "blocked") {
		t.Errorf("pipeline changes = %+v, want a dispatch entry for git-proxy going blocked", feed.Changes)
	}

	// Cleared, every surface says so: the check is gone, the task is
	// queued/ready again, and the feed has the clear.
	daemon.DispatchConditions.Clear(daemon.ConditionGitProxy)
	if health, err = client.SandboxHealth(ctx); err != nil {
		t.Fatalf("GET /api/sandboxes after the clear: %v", err)
	}
	if len(health.DispatchBlocked) != 0 {
		t.Errorf("health.dispatchBlocked after the clear = %+v, want none", health.DispatchBlocked)
	}
	if detail, err = client.GetTask(ctx, created.ID); err != nil {
		t.Fatalf("GET /api/tasks/%s after the clear: %v", created.ID, err)
	}
	if detail.Reason == nil || detail.Reason.Reason != "queued/ready" {
		t.Errorf("queued task's reason after the clear = %+v, want queued/ready", detail.Reason)
	}
	getJSON(t, "http://"+uiAddr+"/api/pipeline/changes", &feed)
	if !hasDispatchChange(feed.Changes, "dispatching") {
		t.Errorf("pipeline changes after the clear = %+v, want a dispatch entry for git-proxy going dispatching", feed.Changes)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run() = %v, want nil once ctx is cancelled", err)
		}
	case <-time.After(daemonWaitLimit):
		t.Fatal("run() never returned after ctx was cancelled")
	}
}

func hasDispatchChange(changes []ui.PipelineChange, to string) bool {
	for _, ch := range changes {
		if ch.Kind == ui.PipelineChangeDispatch && ch.ID == daemon.ConditionGitProxy && ch.To == to {
			return true
		}
	}
	return false
}

// getJSON reads url with the test access code into out.
func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := accessGet(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s = %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decoding GET %s: %v", url, err)
	}
}
