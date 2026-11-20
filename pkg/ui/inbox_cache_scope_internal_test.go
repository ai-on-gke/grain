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

package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// Two limited roles whose grants agree on every repository that has
// tasks, but differ on one that only has a notification, must not share
// a cached inbox: the second never reads the first one's notification.
// (Roles, unlike external tokens, write nothing on each call, so nothing
// moves the store's token between their two requests.)
func TestInboxCacheDoesNotShareNotificationsAcrossRepoScopes(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	c := NewClient(Config{Actor: DefaultActor("alice")}, store)
	s := NewServerWithClient(c)
	if err := store.PutTask(ctx, model.Task{ID: "1", Title: "widgets work", Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RaiseNotification(ctx, RaiseNotificationRequest{
		Subject: "ci:vault", Title: "vault is red", Severity: "warning", Repo: "acme/vault",
	}); err != nil {
		t.Fatal(err)
	}
	role := func(who string, grants ...string) *accessRole {
		return &accessRole{Who: who, set: model.APIGrantsOf(grants), grants: grants, deny: map[APIScope]bool{}}
	}
	vaultReader := role("vault@example.com", "api:tasks:read@acme/widgets", "api:tasks:read@acme/vault")
	otherReader := role("other@example.com", "api:tasks:read@acme/widgets", "api:tasks:read@acme/other")
	get := func(a *accessRole) []InboxItem {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/inbox?kind=notification", nil)
		r = r.WithContext(withAccessRole(r.Context(), a))
		rec := httptest.NewRecorder()
		s.handleInbox(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/inbox as %s: %d %s", a.Who, rec.Code, rec.Body.String())
		}
		var resp InboxResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Items
	}
	sawVault := func(items []InboxItem) bool {
		for _, it := range items {
			if it.Repo == "acme/vault" {
				return true
			}
		}
		return false
	}
	if !sawVault(get(vaultReader)) {
		t.Fatal("a role reading acme/vault did not see its notification")
	}
	if sawVault(get(otherReader)) {
		t.Error("a role held to acme/widgets and acme/other read acme/vault's notification out of the inbox cache")
	}
	if !sawVault(get(vaultReader)) {
		t.Error("a role reading acme/vault lost its notification after another role's call")
	}
}
