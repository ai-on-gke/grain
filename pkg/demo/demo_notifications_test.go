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

package demo

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// TestDemoNotificationsClearsAMetConditionOnItsOwn is -notifications: a
// notification whose ttl has passed is cleared by the loop ("condition")
// with nobody pressing Clear, and one whose ttl has not stays open.
func TestDemoNotificationsClearsAMetConditionOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	now := time.Now().UTC()
	past, future := now.Add(-time.Second), now.Add(time.Hour)
	raise := func(subject string, at time.Time) int64 {
		t.Helper()
		raised, err := store.RaiseNotification(ctx, model.Notification{
			Subject: subject, Severity: model.NotificationInfo, Title: subject, RaisedBy: "agent:19",
			RaisedAt: now.Add(-time.Minute), ClearsWhen: &model.NotificationClear{Kind: model.ClearsOnTTL, At: &at},
		})
		if err != nil {
			t.Fatalf("raising %s: %v", subject, err)
		}
		return raised.Notification.ID
	}
	expired, live := raise("agent.19:walk-ttl", past), raise("agent.19:walk-later", future)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runDemoNotifications(ctx, store, 10*time.Millisecond)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		n, err := store.GetNotification(ctx, expired)
		if err != nil {
			t.Fatalf("GetNotification: %v", err)
		}
		if !n.Open() {
			if n.ClearedBy != "condition" {
				t.Errorf("cleared by %q, want condition", n.ClearedBy)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("after 10s of -notifications the expired ttl notification is still open")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n, err := store.GetNotification(ctx, live); err != nil || n == nil || !n.Open() {
		t.Errorf("the notification whose ttl has not passed = %+v (%v), want it still open", n, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runDemoNotifications did not return once its context was done")
	}
}
