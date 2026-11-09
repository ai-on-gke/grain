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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// #XC-22: GET /api/tasks/wait answers an already-met condition at once,
// wakes on the commit that meets one (and, for awaiting-reply, not
// before the run asking has finished), answers a closed-unmerged task
// under until=merged as a failure, times out after its timeout, checks
// every task it names against the caller's grants, and credits no wait.
// (The CLI half, grain wait's exit 5, is pkg/cli's test of this name.)
func TestTasksWaitEndpointWakesOnConditionsEarnsNoWaitCreditAndExits5OnTimeout(t *testing.T) {
	srv, client := testServer(t)
	enableExternalAgentAPI(t, client.Store)
	ctx := context.Background()
	seedAPITask(t, client, "500", "acme/widgets")
	seedAPITask(t, client, "501", "acme/widgets")
	seedAPITask(t, client, "502", "acme/gadgets")
	closed := baseTime
	if err := client.Store.Observe(ctx, model.Observation{TaskID: "501", ClosedAt: &closed}); err != nil {
		t.Fatal(err)
	}

	wait := func(token, query string) (int, ui.TasksWaitResponse, time.Duration) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/tasks/wait?"+query, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		start := time.Now()
		srv.ServeHTTP(rec, req)
		var resp ui.TasksWaitResponse
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, resp, time.Since(start)
	}

	code, resp, took := wait("", "ids=501&until=closed")
	if code != http.StatusOK || !resp.Met || resp.Condition != "closed" || took > 5*time.Second {
		t.Fatalf("an already-closed task: %d %+v after %s, want met at once", code, resp, took)
	}
	code, resp, _ = wait("", "ids=501&until=merged")
	if code != http.StatusOK || !resp.Met || !resp.Failed {
		t.Errorf("until=merged on a task closed with nothing merged: %+v, want met and failed", resp)
	}

	code, resp, took = wait("", "ids=500&until=closed&timeout=1")
	if code != http.StatusOK || resp.Met || !resp.TimedOut || took < time.Second || took > 10*time.Second {
		t.Errorf("an open task with timeout=1: %d %+v after %s, want a timed-out answer after about 1s", code, resp, took)
	}

	// A run is live on 500, and posts its question before it has
	// finished (orchestrator.relayParkingCalls writes the comment and
	// PendingQuestionCommentID as the run is tearing down). That is not
	// parked yet: a wait until awaiting-reply must not wake on it.
	if err := client.Store.StartRun(ctx, model.Run{ID: "run-500", TaskID: "500", Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	qid, err := client.Store.AddComment(ctx, model.Comment{TaskID: "500", Body: "which branch?",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run"}}, CreatedAt: baseTime})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Store.Observe(ctx, model.Observation{TaskID: "500", PendingQuestionCommentID: &qid}); err != nil {
		t.Fatal(err)
	}
	code, resp, took = wait("", "ids=500,501&until=awaiting-reply&timeout=1")
	if code != http.StatusOK || resp.Met || !resp.TimedOut || took < time.Second {
		t.Fatalf("awaiting-reply with a question pending while its run is still live: %d %+v after %s, want no wake until the timeout", code, resp, took)
	}

	// Woken by the commit that finishes the run and leaves the task
	// parked, not by a timer.
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = client.Store.FinishRun(ctx, "run-500", baseTime.Add(time.Minute), "succeeded", "")
	}()
	code, resp, took = wait("", "ids=500,501&until=awaiting-reply&timeout=30")
	if code != http.StatusOK || !resp.Met || resp.Condition != "awaiting-reply" || took > 10*time.Second {
		t.Fatalf("awaiting-reply: %d %+v after %s, want woken by the park", code, resp, took)
	}
	if resp.Tasks[0].State != model.StateAwaitingReply {
		t.Errorf("woke with task 500 in state %q, want %q", resp.Tasks[0].State, model.StateAwaitingReply)
	}
	if resp.Tasks[0].Question != "which branch?" {
		t.Errorf("the parked question = %q", resp.Tasks[0].Question)
	}

	// any wakes on the next change after since.
	since := resp.Revision
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = client.Store.Observe(ctx, model.Observation{TaskID: "500", ClosedAt: &closed})
	}()
	code, resp, took = wait("", "ids=500,501&until=any&timeout=30&since="+since)
	if code != http.StatusOK || !resp.Met || resp.Condition != "any" || resp.Revision == since || took > 10*time.Second {
		t.Errorf("until=any: %d %+v after %s", code, resp, took)
	}

	if code, _, _ := wait("", "ids=999&until=closed"); code != http.StatusNotFound {
		t.Errorf("a wait on no task answered %d, want 404", code)
	}
	if code, _, _ := wait("", "ids=500&until=forever"); code != http.StatusBadRequest {
		t.Errorf("an unknown until answered %d, want 400", code)
	}

	operator := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "ops@example.com", Source: model.SourceIAP})
	issued, err := client.CreateExternalAgentToken(operator, ui.CreateExternalAgentTokenRequest{
		Name: "widgets-reader", ScopeSet: "grain/external-read", Repos: []string{"acme/widgets"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, resp, _ := wait(issued.Token, "ids=501&until=closed"); code != http.StatusOK || !resp.Met {
		t.Errorf("a token reading acme/widgets waiting on its task: %d %+v", code, resp)
	}
	if code, _, _ := wait(issued.Token, "ids=501,502&until=closed"); code != http.StatusForbidden {
		t.Errorf("a token reading acme/widgets waiting on an acme/gadgets task too got %d, want 403", code)
	}
	if code, _, _ := wait(issued.Token, "ids=501,999&until=closed"); code != http.StatusForbidden {
		t.Errorf("a token reading acme/widgets waiting on a task id that names no task got %d, want 403", code)
	}

	credits, err := client.Store.RunWaitCredits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(credits) != 0 {
		t.Errorf("external waits accrued wait credit: %+v", credits)
	}
}
