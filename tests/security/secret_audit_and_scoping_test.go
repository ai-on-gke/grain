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

package security

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// resolveAuditedSecret resolves a secret on behalf of a task of team-sec
// against a store that audits it, and hands back both stores and the
// team's admin and plain member.
func resolveAuditedSecret(t *testing.T) (store *model.Store, secStore *secrets.Store, teamAdmin, teamMember string) {
	t.Helper()
	store, _ = sqlitetest.Open(t)

	teamAdmin = "admin@team-sec.org"
	teamMember = "dev@team-sec.org"

	secDir := t.TempDir()
	secStore = secrets.New(secDir)
	secStore.SetAuditLogger(store)

	if err := secStore.Set("cloud-keys", "prod_token", []byte("super-secret-token")); err != nil {
		t.Fatal(err)
	}

	// Resolve secret with context metadata
	resolveCtx := context.Background()
	resolveCtx = secrets.WithTask(resolveCtx, "task-audit-1")
	resolveCtx = secrets.WithRun(resolveCtx, "run-audit-1")
	resolveCtx = secrets.WithPrincipal(resolveCtx, "agent-runner@internal")

	val, err := secStore.Resolve(resolveCtx, "cloud-keys/prod_token")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if val != "super-secret-token" {
		t.Fatalf("unexpected secret value: %s", val)
	}
	return store, secStore, teamAdmin, teamMember
}

// Resolving a secret for a run records who resolved what, for which
// task and run.
func TestSecretResolutionAuditTrailLogging(t *testing.T) {
	ctx := context.Background()
	store, _, _, _ := resolveAuditedSecret(t)

	// Verify audit log recorded in store
	entries, err := store.ListSecretAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListSecretAudit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	e := entries[0]
	if e.TaskID != "task-audit-1" || e.RunID != "run-audit-1" {
		t.Fatalf("audit entry metadata mismatch: %+v", e)
	}
	if e.SecretName != "cloud-keys" || e.Key != "prod_token" || e.Principal != "agent-runner@internal" {
		t.Fatalf("audit entry payload mismatch: %+v", e)
	}
}
