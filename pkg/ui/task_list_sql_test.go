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
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func openBothBackends(t *testing.T, fn func(t *testing.T, srv *ui.Server, store *model.Store)) {
	t.Helper()
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var store *model.Store
			if backend == "sqlite" {
				store, _ = sqlitetest.Open(t)
			} else {
				store, _ = postgrestest.Open(t)
				if store == nil {
					t.Skip("no postgres")
				}
			}
			ctx := context.Background()
			if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
				t.Fatal(err)
			}
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			client := ui.NewClient(ui.Config{
				Actor:         ui.DefaultActor("alice"),
				DefaultTarget: &repo,
				Capabilities:  ui.OfferedCapabilities(),
			}, store)
			srv := ui.NewServerWithClient(client)
			fn(t, srv, store)
		})
	}
}

func getJSON[T any](t *testing.T, srv *ui.Server, path string) T {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s -> %d: %s", path, rec.Code, rec.Body.String())
	}
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %s: %v (body=%s)", path, err, rec.Body.String())
	}
	return out
}

// TestTaskListSQLFilteringAndPagingParity exercises SQL-pushed filters, keyset
// cursor tie-breaking (same-second ties, numeric vs non-numeric IDs, timeless
// tasks, cross-set closed/completed/open merge), summary rows, and special
// search characters identically on SQLite and PostgreSQL.
func TestTaskListSQLFilteringAndPagingParity(t *testing.T) {
	openBothBackends(t, func(t *testing.T, srv *ui.Server, store *model.Store) {
		ctx := context.Background()
		wRepo := model.RepoRef{Owner: "acme", Name: "widgets"}
		gRepo := model.RepoRef{Owner: "acme", Name: "gadgets"}
		alice := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
		bob := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bob"}}
		base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

		put := func(tk model.Task) {
			t.Helper()
			if err := store.PutTask(ctx, tk); err != nil {
				t.Fatalf("PutTask %s: %v", tk.ID, err)
			}
		}

		// Same-timestamp ties across numeric and non-numeric IDs and across
		// Set A (closed), Set B (completed open), and Set C (open uncompleted).
		t1 := base.Add(1 * time.Hour)
		t2 := base.Add(2 * time.Hour)
		put(model.Task{
			ID: "2", Title: "Numeric 2 closed", Body: "literal 100%_test\\path",
			Target: &wRepo, CreatedAt: &base, Approval: &alice,
			Filing:      model.Filing{Attribution: alice, Reason: model.ReasonDirect},
			ExternalRef: "EXT-200", FiledVia: "cli-alpha",
		})
		put(model.Task{
			ID: "10", Title: "Numeric 10 closed", Body: "derived\nDerived from: acme/widgets#42",
			Target: &wRepo, CreatedAt: &base, Approval: &alice,
			Filing: model.Filing{Attribution: bob, Reason: model.ReasonDirect},
			Links:  []model.Link{{Kind: model.LinkFixes, Target: "https://github.com/acme/widgets/pull/10"}},
		})
		put(model.Task{
			ID: "alpha", Title: "Alpha open queued café", Body: "unicode résumé body",
			Target: &gRepo, CreatedAt: &t2, Approval: &alice,
			Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect},
			Links:  []model.Link{{Kind: model.LinkDependsOn, Target: "10"}},
		})
		put(model.Task{
			ID: "beta", Title: "Beta timeless proposed", Body: "no created timestamp",
			Target: &wRepo, CreatedAt: nil, Approval: nil,
			Filing: model.Filing{Attribution: bob, Reason: model.ReasonDirect},
		})
		put(model.Task{
			ID: "gamma", Title: "Gamma running", Body: "live run body",
			Target: &wRepo, CreatedAt: &t1, Approval: &alice,
			Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect},
		})
		// Close "2" and "10" at the exact same timestamp t2 (merged on 10).
		if err := store.Observe(ctx, model.Observation{TaskID: "2", ClosedAt: &t2, CompletedAt: &t2}); err != nil {
			t.Fatal(err)
		}
		if err := store.Observe(ctx, model.Observation{TaskID: "10", ClosedAt: &t2, CompletedAt: &t2, PrMergedAt: &t2}); err != nil {
			t.Fatal(err)
		}
		// Start live run on "gamma".
		if err := store.StartRun(ctx, model.Run{ID: "gamma-1", TaskID: "gamma", Sandbox: "sb-1", Attempt: 1, StartedAt: t1}, model.Limits{}); err != nil {
			t.Fatal(err)
		}

		// 1. Walk keyset cursor pages of size 2 across all 5 tasks.
		// At t2: numeric IDs sort before non-numeric, higher numeric first -> "10", "2", "alpha".
		// Then t1 -> "gamma". Then zero time -> "beta".
		wantOrder := []string{"10", "2", "alpha", "gamma", "beta"}
		var gotOrder []string
		cursor := ""
		for pageNum := 0; pageNum < 10; pageNum++ {
			type taskPage struct {
				Tasks      []ui.Task `json:"tasks"`
				NextCursor string    `json:"nextCursor"`
				Truncated  bool      `json:"truncated"`
				Total      int       `json:"total"`
				Merged     int       `json:"merged"`
				Sessions   int       `json:"sessions"`
			}
			pg := getJSON[taskPage](t, srv, "/api/tasks?limit=2&cursor="+url.QueryEscape(cursor))
			if pg.Total != 5 || pg.Merged != 1 || pg.Sessions != 0 {
				t.Fatalf("page %d counts = (%d,%d,%d), want (5,1,0)", pageNum, pg.Total, pg.Merged, pg.Sessions)
			}
			for _, tk := range pg.Tasks {
				gotOrder = append(gotOrder, tk.ID)
			}
			if !pg.Truncated {
				break
			}
			cursor = pg.NextCursor
		}
		if !slices.Equal(gotOrder, wantOrder) {
			t.Fatalf("cursor walk = %v, want %v", gotOrder, wantOrder)
		}

		// 2. Special literal characters (%, _, \) in q must NOT act as LIKE wildcards.
		literalMatch := getJSON[[]ui.Task](t, srv, "/api/tasks?q="+url.QueryEscape("100%_test\\path"))
		if len(literalMatch) != 1 || literalMatch[0].ID != "2" {
			t.Fatalf("literal q match = %v, want [2]", literalMatch)
		}
		noMatch := getJSON[[]ui.Task](t, srv, "/api/tasks?q="+url.QueryEscape("100XXtest\\path"))
		if len(noMatch) != 0 {
			t.Fatalf("wildcard check matched %d rows, want 0", len(noMatch))
		}

		// 3. Non-ASCII case-insensitive search falls back cleanly to Go PostMatch.
		uniMatch := getJSON[[]ui.Task](t, srv, "/api/tasks?q="+url.QueryEscape("CAFÉ"))
		if len(uniMatch) != 1 || uniMatch[0].ID != "alpha" {
			t.Fatalf("unicode q match = %v, want [alpha]", uniMatch)
		}

		// 4. #id, external_ref, owner, filed_via, derived_from, repo, and closedSince/Before filters.
		if got := getJSON[[]ui.Task](t, srv, "/api/tasks?q="+url.QueryEscape("#10")); len(got) != 1 || got[0].ID != "10" {
			t.Fatalf("#10 search = %v", got)
		}
		if got := getJSON[[]ui.Task](t, srv, "/api/tasks?externalRef=EXT-200"); len(got) != 1 || got[0].ID != "2" {
			t.Fatalf("external_ref filter = %v", got)
		}
		if got := getJSON[[]ui.Task](t, srv, "/api/tasks?filed_via=ALPHA"); len(got) != 1 || got[0].ID != "2" {
			t.Fatalf("filed_via filter = %v", got)
		}
		if got := getJSON[[]ui.Task](t, srv, "/api/tasks?owner=bob"); len(got) != 2 {
			t.Fatalf("owner=bob got %d rows, want 2", len(got))
		}
		if got := getJSON[[]ui.Task](t, srv, "/api/tasks?derived_from="+url.QueryEscape("acme/widgets#42")); len(got) != 1 || got[0].ID != "10" {
			t.Fatalf("derived_from filter = %v", got)
		}
		if got := getJSON[[]ui.Task](t, srv, "/api/tasks?repo=acme/gadgets"); len(got) != 1 || got[0].ID != "alpha" {
			t.Fatalf("repo filter = %v", got)
		}
		if got := getJSON[[]ui.Task](t, srv, "/api/tasks?state=running"); len(got) != 1 || got[0].ID != "gamma" {
			t.Fatalf("state=running = %v", got)
		}

		// 5. fields=summary returns lightweight rows without body, preserving blockedBy.
		type sumRow struct {
			ID        string      `json:"id"`
			State     model.State `json:"state"`
			BlockedBy []string    `json:"blockedBy"`
		}
		sums := getJSON[[]sumRow](t, srv, "/api/tasks?fields=summary&state=queued&limit=10")
		if len(sums) != 1 || sums[0].ID != "alpha" || len(sums[0].BlockedBy) != 0 {
			// Task "10" is closed, so "alpha" is NOT blocked by it.
			t.Fatalf("summary queued = %+v, want [alpha with empty blockedBy]", sums)
		}
	})
}

// TestTaskListCacheIgnoresLiveRunActivity verifies the split structural vs.
// activity cache tokens: heartbeats overlay live activity without rebuilding,
// transcripts and running-task progress comments do not invalidate at all,
// while awaiting-reply comments and structural writes rebuild immediately.
func TestTaskListCacheIgnoresLiveRunActivity(t *testing.T) {
	openBothBackends(t, func(t *testing.T, srv *ui.Server, store *model.Store) {
		ctx := context.Background()
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		alice := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
		now := time.Now().UTC()

		for _, id := range []string{"run1", "wait1", "q1"} {
			if err := store.PutTask(ctx, model.Task{
				ID: id, Title: "Task " + id, Body: "body", Target: &repo,
				CreatedAt: &now, Approval: &alice,
				Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect},
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.StartRun(ctx, model.Run{ID: "run1-1", TaskID: "run1", Sandbox: "sb1", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		qcid, err := store.AddComment(ctx, model.Comment{
			TaskID: "wait1", Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "agent"}},
			Body: "Need confirmation?", CreatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Observe(ctx, model.Observation{TaskID: "wait1", PendingQuestionCommentID: &qcid, BaselineCommentID: &qcid}); err != nil {
			t.Fatal(err)
		}

		// Prime live list, queued list, summary list, and inbox.
		rec1 := listWith(t, srv, "")
		if rec1.Code != http.StatusOK {
			t.Fatalf("live list status = %d", rec1.Code)
		}
		etag1 := rec1.Header().Get("ETag")
		_ = getJSON[[]ui.Task](t, srv, "/api/tasks?state=queued")
		_ = getJSON[[]ui.Task](t, srv, "/api/tasks?state=running,awaiting_reply")
		_ = getJSON[[]map[string]any](t, srv, "/api/tasks?fields=summary&limit=10")
		_ = getJSON[ui.InboxResponse](t, srv, "/api/inbox")
		buildsAfterPrime := ui.TaskListBuilds(srv)

		// 1. SetRunTranscript and a progress comment on the running task bump
		// NEITHER structural nor activity: live ETag stays 304 and builds stay flat.
		if err := store.SetRunTranscript(ctx, "run1-1", "step 1 transcript"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID: "run1", Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "agent"}},
			Body: "progress update", CreatedAt: now.Add(time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		if rec := listWith(t, srv, etag1); rec.Code != http.StatusNotModified {
			t.Fatalf("after transcript+progress comment: got %d, want 304", rec.Code)
		}
		_ = getJSON[[]ui.Task](t, srv, "/api/tasks?state=queued")
		_ = getJSON[ui.InboxResponse](t, srv, "/api/inbox")
		if got := ui.TaskListBuilds(srv); got != buildsAfterPrime {
			t.Fatalf("builds jumped from %d to %d on transcript/progress comment", buildsAfterPrime, got)
		}

		// 2. SetTaskActivity bumps only activity: live & running lists return the
		// updated Activity note via in-memory overlay WITHOUT incrementing builds,
		// and queued/summary/inbox keep serving cached entries.
		if _, err := store.SetTaskActivity(ctx, "run1", "running unit tests", now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		liveAfterHB := getJSON[[]ui.Task](t, srv, "/api/tasks")
		var gotNote string
		for _, tk := range liveAfterHB {
			if tk.ID == "run1" {
				gotNote = tk.Activity
			}
		}
		if gotNote != "running unit tests" {
			t.Fatalf("live list Activity = %q, want %q", gotNote, "running unit tests")
		}
		runningAfterHB := getJSON[[]ui.Task](t, srv, "/api/tasks?state=running,awaiting_reply")
		gotNote = ""
		for _, tk := range runningAfterHB {
			if tk.ID == "run1" {
				gotNote = tk.Activity
			}
		}
		if gotNote != "running unit tests" {
			t.Fatalf("filtered running list Activity = %q, want %q", gotNote, "running unit tests")
		}
		_ = getJSON[[]ui.Task](t, srv, "/api/tasks?state=queued")
		_ = getJSON[ui.InboxResponse](t, srv, "/api/inbox")
		if got := ui.TaskListBuilds(srv); got != buildsAfterPrime {
			t.Fatalf("builds jumped from %d to %d on SetTaskActivity heartbeat", buildsAfterPrime, got)
		}

		// 3. A comment on an awaiting_reply task DOES bump structural so the UI
		// sees the reply/undelivered badge immediately.
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID: "wait1", Author: alice, Body: "Go with option A", CreatedAt: now.Add(3 * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		_ = getJSON[[]ui.Task](t, srv, "/api/tasks")
		if got := ui.TaskListBuilds(srv); got <= buildsAfterPrime {
			t.Fatalf("builds did not increase after awaiting_reply comment: got %d, was %d", got, buildsAfterPrime)
		}
	})
}

// TestTaskListPersonDefaultCapsAndActivePriority verifies that person callers
// get bounded default pages (500 for live, 200 for narrowed) with active tasks
// prioritized ahead of historical failures, while ?limit=0 still returns all.
func TestTaskListPersonDefaultCapsAndActivePriority(t *testing.T) {
	store, db := sqlitetest.Open(t)
	ctx := context.Background()
	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	alice := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	// Seed 520 failed tasks (older, higher priority) + 5 queued + 1 running.
	for i := 0; i < 520; i++ {
		id := fmt.Sprintf("fail-%03d", i)
		ts := base.Add(time.Duration(i) * time.Second)
		if err := store.PutTask(ctx, model.Task{
			ID: id, Title: "Failed " + id, Body: "body", Target: &repo,
			Priority: 10, CreatedAt: &ts, Approval: &alice,
			Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO `task_observation` (`task_id`,`streak_count`) VALUES (?,?)", id, model.MaxConsecutiveFailures); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("q-%d", i)
		ts := base.Add(time.Hour + time.Duration(i)*time.Second)
		if err := store.PutTask(ctx, model.Task{
			ID: id, Title: "Queued " + id, Body: "body", Target: &repo,
			Priority: 1, CreatedAt: &ts, Approval: &alice,
			Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect},
		}); err != nil {
			t.Fatal(err)
		}
	}
	runTS := base.Add(2 * time.Hour)
	if err := store.PutTask(ctx, model.Task{
		ID: "active-run", Title: "Running task", Body: "body", Target: &repo,
		Priority: 1, CreatedAt: &runTS, Approval: &alice,
		Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "active-run-1", TaskID: "active-run", Sandbox: "sb", Attempt: 1, StartedAt: runTS}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice"), DefaultTarget: &repo, Capabilities: ui.OfferedCapabilities()}, store)
	srv := ui.NewServerWithClient(client)

	// Default live GET /api/tasks caps at 500 and keeps the running + queued tasks
	// even though 520 higher-priority failed tasks exist.
	live := getJSON[[]ui.Task](t, srv, "/api/tasks")
	if len(live) != 500 {
		t.Fatalf("live GET /api/tasks returned %d tasks, want 500", len(live))
	}
	seen := map[string]bool{}
	for _, tk := range live {
		seen[tk.ID] = true
	}
	if !seen["active-run"] || !seen["q-0"] || !seen["q-4"] {
		t.Fatalf("active tasks crowded out by failed backlog: active-run=%v q-0=%v q-4=%v", seen["active-run"], seen["q-0"], seen["q-4"])
	}

	// Narrowed GET /api/tasks?state=failed caps at 200 by default for a person,
	// while ?state=failed&limit=0 returns all 520.
	capped := getJSON[[]ui.Task](t, srv, "/api/tasks?state=failed")
	if len(capped) != 200 {
		t.Fatalf("narrowed default cap returned %d tasks, want 200", len(capped))
	}
	all := getJSON[[]ui.Task](t, srv, "/api/tasks?state=failed&limit=0")
	if len(all) != 520 {
		t.Fatalf("narrowed limit=0 returned %d tasks, want 520", len(all))
	}
}

// TestInboxIndexedAllCategories verifies GET /api/inbox returns questions,
// secrets, proposals, merge-held tasks, and notifications from the indexed
// SQL path on both SQLite and PostgreSQL, excluding ordinary queued/closed work.
func TestInboxIndexedAllCategories(t *testing.T) {
	openBothBackends(t, func(t *testing.T, srv *ui.Server, store *model.Store) {
		ctx := context.Background()
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		alice := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
		now := time.Now().UTC()

		put := func(tk model.Task) {
			t.Helper()
			if err := store.PutTask(ctx, tk); err != nil {
				t.Fatal(err)
			}
		}

		// Ordinary queued + closed noise tasks that must NOT appear in inbox.
		put(model.Task{ID: "noise-queued", Title: "Queued", Target: &repo, CreatedAt: &now, Approval: &alice, Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect}})
		put(model.Task{ID: "noise-closed", Title: "Closed", Target: &repo, CreatedAt: &now, Approval: &alice, Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect}})
		if err := store.Observe(ctx, model.Observation{TaskID: "noise-closed", ClosedAt: &now}); err != nil {
			t.Fatal(err)
		}

		// 1. Proposal (unapproved).
		put(model.Task{ID: "prop-1", Title: "Proposed work", Target: &repo, CreatedAt: &now, Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect}})

		// 2. Question (pending question comment).
		put(model.Task{ID: "q-1", Title: "Needs answer", Target: &repo, CreatedAt: &now, Approval: &alice, Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect}})
		qcid, err := store.AddComment(ctx, model.Comment{
			TaskID: "q-1", Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "agent"}},
			Body: "Choose A or B?", Summary: "Choose A or B?", CreatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Observe(ctx, model.Observation{TaskID: "q-1", PendingQuestionCommentID: &qcid, BaselineCommentID: &qcid}); err != nil {
			t.Fatal(err)
		}

		// 3. Secret request.
		put(model.Task{ID: "sec-1", Title: "Needs secret", Target: &repo, CreatedAt: &now, Approval: &alice, Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect}})
		scid, err := store.AddComment(ctx, model.Comment{
			TaskID: "sec-1", Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "agent"}},
			Body: "Set API key", CreatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Observe(ctx, model.Observation{TaskID: "sec-1", PendingQuestionCommentID: &scid, BaselineCommentID: &scid, PendingSecret: "stripe-key"}); err != nil {
			t.Fatal(err)
		}

		// 4. Merge-held via task_link hold + PR link.
		put(model.Task{
			ID: "hold-1", Title: "Security held", Target: &repo, CreatedAt: &now, Approval: &alice,
			Filing: model.Filing{Attribution: alice, Reason: model.ReasonDirect},
			Links: []model.Link{
				{Kind: model.LinkFixes, Target: "https://github.com/acme/widgets/pull/99"},
				{Kind: model.LinkSecurityHold, Target: "https://github.com/acme/widgets/pull/99"},
			},
		})
		if err := store.Observe(ctx, model.Observation{TaskID: "hold-1", CompletedAt: &now}); err != nil {
			t.Fatal(err)
		}

		resp := getJSON[ui.InboxResponse](t, srv, "/api/inbox")
		got := map[string]string{}
		for _, it := range resp.Items {
			got[it.Task] = it.Kind
		}
		want := map[string]string{
			"prop-1": "proposal",
			"q-1":    "question",
			"sec-1":  "secret",
			"hold-1": "merge-held",
		}
		if len(got) != len(want) {
			t.Fatalf("inbox items = %v, want %v", got, want)
		}
		for id, kind := range want {
			if got[id] != kind {
				t.Errorf("inbox[%s] = %q, want %q", id, got[id], kind)
			}
		}
	})
}
