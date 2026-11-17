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

package cli

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// `grain notifications clear` reaches POST /api/notifications/clear with
// every flag, refuses to run without -reason, and says how many come back.
func TestNotificationsClearSendsItsFilter(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("tester")}, store)
	for _, n := range []ui.RaiseNotificationRequest{
		{Subject: "ops:disk", Title: "disk", Severity: "warning"},
		{Subject: "ops:cpu", Title: "cpu", Severity: "info"},
		{Subject: model.FamilyNightlyRedUnattended + ":acme/widgets", Title: "nightly red", Severity: "warning"},
	} {
		if _, err := client.RaiseNotification(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(srv.Close)

	if err := runCLIWithAccessCode([]string{"-server", srv.URL, "notifications", "clear", "-subject", "ops:"}); err == nil ||
		!strings.Contains(err.Error(), "-reason") {
		t.Fatalf("clear without -reason = %v, want a refusal naming -reason", err)
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runCLIWithAccessCode([]string{"-server", srv.URL, "notifications", "clear",
			"-subject", "ops:", "-severity", "warning", "-reason", "disk resized"})
	})
	if runErr != nil || !strings.Contains(out, "Cleared 1 notification") {
		t.Fatalf("clear -subject ops: -severity warning: %v, printed %q", runErr, out)
	}
	out = captureStdout(t, func() {
		runErr = runCLIWithAccessCode([]string{"-server", srv.URL, "notifications", "clear", "-reason", "tidy"})
	})
	if runErr != nil || !strings.Contains(out, "Cleared 2 notification") ||
		!strings.Contains(out, "1 of them will be raised again") || !strings.Contains(out, model.FamilyNightlyRedUnattended) {
		t.Fatalf("clear all: %v, printed %q, want 2 cleared and nightly-red-unattended coming back", runErr, out)
	}
	if open, _ := store.ListNotifications(ctx, model.NotificationFilter{Open: true}); len(open) != 0 {
		t.Fatalf("still open after clear all: %+v", open)
	}
}
