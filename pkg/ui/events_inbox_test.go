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
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// apiEventsOf decodes a GET /api/events JSON body, failing on a non-200.
func apiEventsOf(t *testing.T, code int, body string) []ui.IntegrationEvent {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("GET /api/events: %d %s", code, body)
	}
	var resp ui.EventsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Events
}

// Raising, acknowledging and clearing a notification each put an event
// on GET /api/events, so an open Inbox learns of it at once rather than
// on its next poll; a reader limited to one repository hears only that
// repository's notifications, live on the stream as in a JSON read, and
// a deployment-wide one (no repository) only reaches unrestricted
// readers, as a no-repository task's events do (task 4284).
func TestEventsCarryNotificationsFilteredByWhatTheReaderMayRead(t *testing.T) {
	ui.LogToTest(t)
	ctx := context.Background()
	h := newRoleHarness(t)
	installScopeSets(t, model.APIScopeSet{
		Name:        "widgets-reader",
		Description: "Task reader restricted to acme/widgets",
		Scopes:      []string{"tasks:read"},
		Repos:       []string{"acme/widgets"},
	})
	h.allow(t, "user:widgets@example.com", "widgets-reader")
	h.allow(t, "user:boss@example.com", "grain/operator")
	seedAPITask(t, h.client, "711", "acme/widgets")
	seedAPITask(t, h.client, "713", "acme/secret")
	store := h.client.Store

	head, err := store.LatestIntegrationEventID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	since := "/api/events?since=" + itoa(head) + "&types=notification.raised,notification.acked,notification.cleared"

	widgets, err := store.RaiseNotification(ctx, model.Notification{
		Subject: "ci.red:acme/widgets", Severity: model.NotificationWarning,
		Title: "acme/widgets main is red", Body: "SECRET-BODY-TEXT", TaskID: "711",
	})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := store.RaiseNotification(ctx, model.Notification{
		Subject: "ci.red:acme/secret", Title: "acme/secret main is red", Repo: "acme/secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject: "hosts.down", Title: "every host is down",
	}); err != nil {
		t.Fatal(err)
	}
	// Raised again at the same severity: only its count moves, which is
	// no news to a reader.
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject: "ci.red:acme/widgets", Severity: model.NotificationWarning, Title: "acme/widgets main is red",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AckNotification(ctx, widgets.Notification.ID, "boss@example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClearNotificationByID(ctx, secret.Notification.ID, "boss@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClearNotification(ctx, "ci.red:acme/widgets", "boss@example.com"); err != nil {
		t.Fatal(err)
	}

	code, body := h.call("boss@example.com", http.MethodGet, since, "")
	all := apiEventsOf(t, code, body)
	var got []string
	for _, ev := range all {
		got = append(got, string(ev.Type)+" "+ev.Subject)
	}
	want := []string{
		"notification.raised ci.red:acme/widgets",
		"notification.raised ci.red:acme/secret",
		"notification.raised hosts.down",
		"notification.acked ci.red:acme/widgets",
		"notification.cleared ci.red:acme/secret",
		"notification.cleared ci.red:acme/widgets",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("operator's notification events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	first := all[0]
	if first.NotificationID != widgets.Notification.ID || first.Severity != "warning" || first.Summary != "acme/widgets main is red" ||
		first.TaskID != "711" || first.Repo != "acme/widgets" {
		t.Fatalf("raised event %+v: want the notification's id, severity, title, task, and the task's repo", first)
	}
	if strings.Contains(body, "SECRET-BODY-TEXT") {
		t.Fatalf("an event carried a notification's body: %s", body)
	}

	code, body = h.call("widgets@example.com", http.MethodGet, since, "")
	mine := apiEventsOf(t, code, body)
	if len(mine) != 3 {
		t.Fatalf("widgets-reader's notification events = %+v, want the three on acme/widgets", mine)
	}
	if strings.Contains(body, "acme/secret") || strings.Contains(body, "hosts.down") {
		t.Fatalf("widgets-reader read notifications it may not: %s", body)
	}

	// Live: the stream delivers a new acme/widgets raise to the
	// widgets-reader, and never the acme/secret one raised before it.
	ts := httptest.NewServer(h.srv)
	defer ts.Close()
	sseCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(sseCtx, http.MethodGet, ts.URL+"/api/events?types=notification.raised", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h.as("widgets@example.com") {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	req.Header.Set("Accept", "text/event-stream")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("opening the SSE stream: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("SSE stream: %d", res.StatusCode)
	}
	frames := make(chan sseFrame, 16)
	reader := bufio.NewReader(res.Body)
	go readSSEFrames(sseCtx, reader, frames)
	select {
	case f := <-frames:
		if !strings.HasPrefix(f.comment, "ready") {
			t.Fatalf("first SSE frame %+v, want the ready comment", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ready frame on the SSE stream")
	}

	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject: "ci.red:acme/secret", Title: "acme/secret main is red again", Repo: "acme/secret",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject: "ci.red:acme/widgets", Title: "acme/widgets main is red again", Repo: "acme/widgets",
		Severity: model.NotificationCritical,
	}); err != nil {
		t.Fatal(err)
	}
	ev := waitSSEEvent(t, frames, 10*time.Second)
	if ev.Type != model.IntegrationEventNotificationRaised || ev.Repo != "acme/widgets" || ev.Severity != "critical" {
		t.Fatalf("streamed event %+v, want acme/widgets's critical raise", ev)
	}
}

// A host-script call waiting for a person, and the answer to it, are
// events on GET /api/events on the call's task -- the script and its
// argument count, never its arguments or stdin (task 4284).
func TestEventsCarryLocalScriptApprovals(t *testing.T) {
	t.Parallel()
	client, store, ctx := localScriptsClient(t)
	task := create(t, client, ctx)
	grantLocalScripts(t, store, ctx, task.ID)
	handler := ui.NewServerWithClient(client)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	run := serverAccessClient(handler, srv.URL)
	person := serverAccessClient(handler, srv.URL)

	head, err := store.LatestIntegrationEventID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := run.RequestLocalScriptApproval(ctx, task.ID, approvalRequest{
		ID: "1", Tool: "local_deploy", Script: "deploy", Args: []string{"main", "TOP-SECRET-ARG"},
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := person.AnswerLocalScriptApproval(ctx, task.ID, first.ID, false); err != nil {
		t.Fatal(err)
	}
	second, err := run.RequestLocalScriptApproval(ctx, task.ID, approvalRequest{
		ID: "2", Tool: "local_deploy", Script: "deploy", Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec := doWithHeaders(t, handler, http.MethodPost,
		"/api/tasks/"+task.ID+"/local-script-approvals/"+second.ID+"/expire", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("expiring: %d %s", rec.Code, rec.Body.String())
	}

	rec := doWithHeaders(t, handler, http.MethodGet,
		"/api/events?since="+itoa(head)+"&types=local_script.pending,local_script.answered", "", nil)
	events := apiEventsOf(t, rec.Code, rec.Body.String())
	var got []string
	for _, ev := range events {
		if ev.TaskID != task.ID || ev.Repo != "acme/widgets" || ev.Script != "deploy" {
			t.Fatalf("event %+v: want task %s's deploy call on acme/widgets", ev, task.ID)
		}
		got = append(got, string(ev.Type)+" "+ev.ApprovalID+" "+ev.Decision+" "+ev.Summary)
	}
	want := []string{
		"local_script.pending " + first.ID + "  deploy (2 arguments)",
		"local_script.answered " + first.ID + " denied deploy (2 arguments)",
		"local_script.pending " + second.ID + "  deploy",
		"local_script.answered " + second.ID + " expired deploy",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("local-script events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(rec.Body.String(), "TOP-SECRET-ARG") {
		t.Fatalf("an event carried a call's argument: %s", rec.Body.String())
	}
}
