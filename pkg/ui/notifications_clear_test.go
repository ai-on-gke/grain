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

// POST /api/notifications/clear: clearing many notifications at once, each
// exactly as the per-id clear does, with a reason and one audit entry.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func raiseFor(t *testing.T, client *ui.Client, ctx context.Context, subject, severity string) int64 {
	t.Helper()
	resp, err := client.RaiseNotification(ctx, ui.RaiseNotificationRequest{Subject: subject, Title: subject, Severity: severity})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Notification.ID
}

func openSubjects(t *testing.T, client *ui.Client, ctx context.Context) []string {
	t.Helper()
	open, err := client.ListNotifications(ctx, model.NotificationFilter{Open: true})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range open {
		out = append(out, n.Subject)
	}
	slices.Sort(out)
	return out
}

func TestClearAllNotificationsNeedsAReasonAndAuditsOnce(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	raiseFor(t, client, ctx, "ops:disk", "warning")
	raiseFor(t, client, ctx, "ops:cpu", "info")
	raiseFor(t, client, ctx, model.FamilyNightlyRedUnattended+":acme/widgets", "warning")
	raiseFor(t, client, ctx, model.FamilyReleasePending+":v3.5.0", "info")

	if _, err := client.ClearNotifications(ctx, ui.ClearNotificationsRequest{Reason: "  "}); err == nil ||
		!strings.Contains(err.Error(), "reason") {
		t.Fatalf("a clear with no reason = %v, want a refusal naming the reason", err)
	}
	if got := openSubjects(t, client, ctx); len(got) != 4 {
		t.Fatalf("a refused clear cleared something: open %v", got)
	}

	dry, err := client.ClearNotifications(ctx, ui.ClearNotificationsRequest{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Matched != 3 || dry.Cleared != 0 || dry.ExpectedBack != 1 {
		t.Fatalf("dry run = %+v, want 3 matched (the pending release left alone), none cleared, 1 expected back", dry)
	}
	if got := openSubjects(t, client, ctx); len(got) != 4 {
		t.Fatalf("a dry run cleared something: open %v", got)
	}

	resp, err := client.ClearNotifications(ctx, ui.ClearNotificationsRequest{Reason: "noise after the outage"})
	if err != nil {
		t.Fatalf("ClearNotifications: %v", err)
	}
	if resp.Cleared != 3 || resp.ExpectedBack != 1 ||
		!slices.Equal(resp.ExpectedBackFamilies, []string{model.FamilyNightlyRedUnattended}) {
		t.Fatalf("clear all = %+v, want 3 cleared, nightly-red-unattended expected back", resp)
	}
	if got := openSubjects(t, client, ctx); !slices.Equal(got, []string{model.FamilyReleasePending + ":v3.5.0"}) {
		t.Fatalf("open after clear all = %v, want only the pending release", got)
	}
	for _, id := range resp.IDs {
		n, _ := store.GetNotification(ctx, id)
		if n == nil || n.Open() || n.ClearedBy != "human:alice" {
			t.Errorf("notification %d = %+v, want cleared by alice", id, n)
		}
	}
	audits, err := store.ConfigAuditOfSurface(ctx, "notifications", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || !strings.Contains(audits[0].Summary, "cleared 3") ||
		!strings.Contains(audits[0].Summary, "noise after the outage") || audits[0].By.ID != "alice" {
		t.Fatalf("audit entries = %+v, want one naming the count, the reason and alice", audits)
	}

	// The level-triggered one comes back as a new instance while its
	// condition holds, as after a one-at-a-time clear.
	again := raiseFor(t, client, ctx, model.FamilyNightlyRedUnattended+":acme/widgets", "warning")
	if slices.Contains(resp.IDs, again) {
		t.Fatalf("a raise after the clear reopened instance %d, want a new one", again)
	}
}

func TestClearNotificationsByFilter(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	disk := raiseFor(t, client, ctx, "ops:disk", "warning")
	raiseFor(t, client, ctx, "ops:cpu", "critical")
	raiseFor(t, client, ctx, "pipeline.base-red:acme/widgets@main", "warning")
	// Last raised two days ago: only it is older than a day.
	stale, err := store.RaiseNotification(ctx, model.Notification{Subject: "pipeline.merge-stalled:12", Title: "stalled",
		Severity: model.NotificationInfo, RaisedAt: client.Now().Add(-48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	old := stale.Notification.ID
	raiseFor(t, client, ctx, model.FamilyReleasePending+":v3.5.0", "info")

	resp, err := client.ClearNotifications(ctx, ui.ClearNotificationsRequest{Subject: "ops:", Severity: "warning", Reason: "disk resized"})
	if err != nil || resp.Cleared != 1 || !slices.Equal(resp.IDs, []int64{disk}) {
		t.Fatalf("subject+severity clear = %+v, %v, want only ops:disk", resp, err)
	}

	if _, err := client.ClearNotifications(ctx, ui.ClearNotificationsRequest{OlderThan: "soon", Reason: "x"}); err == nil {
		t.Fatalf("olderThan \"soon\" was accepted")
	}
	if _, err := client.ClearNotifications(ctx, ui.ClearNotificationsRequest{Severity: "loud", Reason: "x"}); err == nil {
		t.Fatalf("severity \"loud\" was accepted")
	}
	resp, err = client.ClearNotifications(ctx, ui.ClearNotificationsRequest{OlderThan: "1d", Reason: "stale"})
	if err != nil || !slices.Equal(resp.IDs, []int64{old}) {
		t.Fatalf("olderThan 1d = %+v, %v, want only %d", resp, err, old)
	}

	// Named ids, and a subject prefix that names the pending release.
	resp, err = client.ClearNotifications(ctx, ui.ClearNotificationsRequest{Subject: model.FamilyReleasePending, Reason: "rejected out of band"})
	if err != nil || resp.Cleared != 1 {
		t.Fatalf("clearing release.pending by subject = %+v, %v", resp, err)
	}
	if got := openSubjects(t, client, ctx); !slices.Equal(got, []string{"ops:cpu", "pipeline.base-red:acme/widgets@main"}) {
		t.Fatalf("open = %v", got)
	}
}

func TestAgentsBulkClearOnlyTheirOwnNotifications(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)
	grantTask(t, store, task.ID, func(m *model.Task) {
		m.Grants = append(m.Grants, model.Grant{Capability: "api:task-admin"})
	})
	other := raiseFor(t, client, ctx, "ops:disk", "warning")
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: task.ID})
	mine, err := client.RaiseNotification(agentCtx, ui.RaiseNotificationRequest{Subject: ui.AgentNamespace(task.ID) + "progress", Title: "halfway"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.ClearNotifications(agentCtx, ui.ClearNotificationsRequest{IDs: []int64{other}, Reason: "mine now"}); err == nil {
		t.Fatalf("a run bulk-cleared notification %d it did not raise", other)
	}
	resp, err := client.ClearNotifications(agentCtx, ui.ClearNotificationsRequest{Reason: "done"})
	if err != nil {
		t.Fatalf("agent clear all: %v", err)
	}
	if !slices.Equal(resp.IDs, []int64{mine.Notification.ID}) {
		t.Fatalf("an agent's clear all cleared %v, want only its own %d", resp.IDs, mine.Notification.ID)
	}
	if n, _ := store.GetNotification(ctx, other); n == nil || !n.Open() {
		t.Fatalf("an agent's clear all cleared another's notification: %+v", n)
	}
}

func TestBulkClearNeedsNotificationsWrite(t *testing.T) {
	h := newRoleHarness(t)
	h.allow(t, "user:viv@example.com", "grain/viewer")
	resp, err := h.client.RaiseNotification(context.Background(), ui.RaiseNotificationRequest{Subject: "ops:disk", Title: "disk"})
	if err != nil {
		t.Fatal(err)
	}
	code, body := h.call("viv@example.com", http.MethodPost, "/api/notifications/clear", `{"reason":"tidy"}`)
	if code != http.StatusForbidden || !strings.Contains(body, "notifications:write") {
		t.Fatalf("viewer POST /api/notifications/clear: %d %s, want 403 naming notifications:write", code, body)
	}
	if n, _ := h.client.Store.GetNotification(context.Background(), resp.Notification.ID); n == nil || !n.Open() {
		t.Fatalf("a refused bulk clear cleared %+v", n)
	}
}

// A store error partway through a bulk clear leaves what it already cleared
// cleared, so that much is audited, with its reason, before the error says
// how far it got.
func TestABulkClearThatFailsPartwayAuditsWhatItCleared(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	raiseFor(t, client, ctx, "ops:disk", "warning")
	raiseFor(t, client, ctx, "ops:cpu", "warning")
	raiseFor(t, client, ctx, "ops:net", "warning")
	calls := 0
	client.SetClearNotificationForTest(func(ctx context.Context, id int64, by string) (bool, error) {
		calls++
		if calls == 2 {
			return false, errors.New("database is locked")
		}
		return store.ClearNotificationByID(ctx, id, by)
	})
	resp, err := client.ClearNotifications(ctx, ui.ClearNotificationsRequest{Reason: "outage noise"})
	if err == nil || !strings.Contains(err.Error(), "cleared 1 of 3") || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("a clear failing on its second notification = %+v, %v, want an error saying 1 of 3 were cleared", resp, err)
	}
	if got := openSubjects(t, client, ctx); len(got) != 2 {
		t.Fatalf("open after the failure = %v, want the two not cleared", got)
	}
	audits, err := store.ConfigAuditOfSurface(ctx, "notifications", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || !strings.Contains(audits[0].Summary, "cleared 1 ") || !strings.Contains(audits[0].Summary, "outage noise") {
		t.Fatalf("audit entries = %+v, want one recording the one cleared and the reason", audits)
	}
}
