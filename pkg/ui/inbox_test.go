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
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// seedInbox files one task of every kind the Inbox lists on
// acme/widgets, a question on acme/gadgets, and a warning and an info
// notification on acme/widgets.
func seedInbox(t *testing.T, client *ui.Client) {
	t.Helper()
	ctx := context.Background()
	park := func(id, repo, secret string) {
		t.Helper()
		seedAPITask(t, client, id, repo)
		approve(t, client, id)
		qid, err := client.Store.AddComment(ctx, model.Comment{TaskID: id, Body: "which branch?",
			Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run"}}, CreatedAt: baseTime})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Store.Observe(ctx, model.Observation{TaskID: id, PendingQuestionCommentID: &qid, PendingSecret: secret}); err != nil {
			t.Fatal(err)
		}
	}
	park("400", "acme/widgets", "")
	park("401", "acme/widgets", "stripe-api-key")
	park("402", "acme/gadgets", "")
	seedAPITask(t, client, "403", "acme/widgets") // unapproved: a proposal
	seedAPITask(t, client, "404", "acme/widgets")
	approve(t, client, "404")
	if err := client.Store.UpdateTask(ctx, "404", func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#42"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := baseTime.Add(time.Hour)
	if err := client.Store.Observe(ctx, model.Observation{TaskID: "404", CompletedAt: &done,
		MergeQueueBlockedAt: &done, MergeQueueState: "merge_failed", MergeQueueNote: "conflicts with main"}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []ui.RaiseNotificationRequest{
		{Subject: "ci:widgets", Title: "main is red", Severity: "warning", Repo: "acme/widgets"},
		{Subject: "info:widgets", Title: "just so you know", Severity: "info", Repo: "acme/widgets"},
	} {
		if _, err := client.RaiseNotification(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
}

func approve(t *testing.T, client *ui.Client, id string) {
	t.Helper()
	if err := client.Store.UpdateTask(context.Background(), id, func(tk *model.Task) error {
		tk.Approval = &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func inboxKinds(items []ui.InboxItem) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Kind+":"+it.Task)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// #XC-21: GET /api/inbox lists every actionable kind, each with its
// root-relative path, and only within the caller's repository grants.
func TestGrainInboxMatchesActionableCategoriesWithinRepoGrants(t *testing.T) {
	srv, client := testServer(t)
	enableExternalAgentAPI(t, client.Store)
	seedInbox(t, client)

	get := func(token, query string) ui.InboxResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/inbox"+query, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/inbox%s: %d %s", query, rec.Code, rec.Body.String())
		}
		var resp ui.InboxResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	all := get("", "")
	if got, want := inboxKinds(all.Items), "merge-held:404 notification: proposal:403 question:400 question:402 secret:401"; got != want {
		t.Errorf("inbox = %q, want %q", got, want)
	}
	for _, it := range all.Items {
		if !strings.HasPrefix(it.Path, "/") || strings.Contains(it.Path, "://") {
			t.Errorf("%s item path %q is not root-relative", it.Kind, it.Path)
		}
		switch it.Kind {
		case ui.InboxNotification:
			if it.Severity != "warning" || it.Title != "main is red" {
				t.Errorf("notification item = %+v", it)
			}
		case ui.InboxMergeHeld:
			if it.PR != "acme/widgets#42" || it.Reason != "conflicts with main" {
				t.Errorf("merge-held item = %+v", it)
			}
		case ui.InboxSecret:
			if it.Path != "/tasks/401" {
				t.Errorf("secret item path = %q", it.Path)
			}
		case ui.InboxQuestion:
			// A question links to the comment it is parked on.
			if !regexp.MustCompile(`^/tasks/` + it.Task + `#comment=\d+$`).MatchString(it.Path) {
				t.Errorf("question item path = %q, want /tasks/%s#comment=<id>", it.Path, it.Task)
			}
		}
	}
	if got := inboxKinds(get("", "?kind=question").Items); got != "question:400 question:402" {
		t.Errorf("kind=question = %q", got)
	}
	if got := inboxKinds(get("", "?repo=acme/gadgets").Items); got != "question:402" {
		t.Errorf("repo=acme/gadgets = %q", got)
	}

	operator := ui.WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalHuman, ID: "ops@example.com", Source: model.SourceIAP})
	issued, err := client.CreateExternalAgentToken(operator, ui.CreateExternalAgentTokenRequest{
		Name: "widgets-reader", ScopeSet: "grain/external-read", Repos: []string{"acme/widgets"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := inboxKinds(get(issued.Token, "").Items), "merge-held:404 notification: proposal:403 question:400 secret:401"; got != want {
		t.Errorf("a token held to acme/widgets saw %q, want %q", got, want)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/inbox?repo=acme/gadgets", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a token held to acme/widgets asking for acme/gadgets got %d, want 403", rec.Code)
	}
}

// A notice raised on a task without a repository (grain notify --task)
// is that task's repository's: a token held to another never reads it.
func TestGrainInboxHidesTaskNoticeOfAnUnreadRepository(t *testing.T) {
	srv, client := testServer(t)
	enableExternalAgentAPI(t, client.Store)
	seedInbox(t, client)
	ctx := context.Background()
	if _, err := client.RaiseNotification(ctx, ui.RaiseNotificationRequest{
		Subject: "task:402", Title: "gadgets needs a look", Severity: "warning", Task: "402",
	}); err != nil {
		t.Fatal(err)
	}
	operator := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "ops@example.com", Source: model.SourceIAP})
	issued, err := client.CreateExternalAgentToken(operator, ui.CreateExternalAgentTokenRequest{
		Name: "widgets-reader", ScopeSet: "grain/external-read", Repos: []string{"acme/widgets"},
	})
	if err != nil {
		t.Fatal(err)
	}
	get := func(token string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/inbox?kind=notification", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/inbox: %d %s", rec.Code, rec.Body.String())
		}
		var resp ui.InboxResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return inboxKinds(resp.Items)
	}
	if got, want := get(""), "notification: notification:402"; got != want {
		t.Errorf("operator saw %q, want %q", got, want)
	}
	if got, want := get(issued.Token), "notification:"; got != want {
		t.Errorf("a token held to acme/widgets saw %q, want %q", got, want)
	}
}
