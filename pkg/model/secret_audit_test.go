package model_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/secrets"
)

// TestSecretResolutionIsAudited: a task's resolution shows on the
// deployment's audit trail (task 3140, when it had to be charged to the
// default team to show at all).
func TestSecretResolutionIsAudited(t *testing.T) {
	store, ctx := open(t)
	if err := store.RecordSecretResolution(ctx, secrets.AuditEntry{TaskID: "task-1", RunID: "run-1", SecretName: "s", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListSecretAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != "task-1" {
		t.Fatalf("audit = %+v, want task-1's resolution", rows)
	}
}
