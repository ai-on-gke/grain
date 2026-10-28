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

package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// reconcileNotifications clears an open notification once its
// clears_when holds -- a ttl passed, a base branch green -- and leaves one
// whose condition does not.
func TestReconcileNotificationsClearsOnTheirConditions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	raise := func(subject string, c model.NotificationClear) int64 {
		t.Helper()
		r, err := store.RaiseNotification(ctx, model.Notification{Subject: subject, Title: subject,
			Severity: model.NotificationInfo, RaisedBy: "automation:test", RaisedAt: now, ClearsWhen: &c})
		if err != nil {
			t.Fatal(err)
		}
		return r.Notification.ID
	}
	ttl := raise("ops:ttl", model.NotificationClear{Kind: model.ClearsOnTTL, TTL: "1h"})
	green := raise("pipeline.base-red:acme/widgets@main",
		model.NotificationClear{Kind: model.ClearsOnBaseGreen, Repo: "acme/widgets", Branch: "main"})
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "abc",
		RunID: "run-1", Conclusion: model.BaseBranchCIFailure}); err != nil {
		t.Fatal(err)
	}
	if err := reconcileNotifications(ctx, Deps{Store: store}, now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{ttl, green} {
		if n, _ := store.GetNotification(ctx, id); !n.Open() {
			t.Fatalf("notification %s cleared before its condition held", n.Subject)
		}
	}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "def",
		RunID: "run-2", Conclusion: "success"}); err != nil {
		t.Fatal(err)
	}
	if err := reconcileNotifications(ctx, Deps{Store: store}, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{ttl, green} {
		if n, _ := store.GetNotification(ctx, id); n.Open() || n.ClearedBy != "condition" {
			t.Errorf("notification %s = %+v, want cleared by its condition", n.Subject, n)
		}
	}
}
