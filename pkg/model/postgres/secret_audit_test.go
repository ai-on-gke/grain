package postgres_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

func TestPostgresSecretAudit(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	// Test Secret Audit Recording & Retrieval
	now := time.Now().UTC().Truncate(time.Millisecond)
	err := store.RecordSecretResolution(ctx, secrets.AuditEntry{
		Timestamp:  now,
		TaskID:     "task-100",
		RunID:      "run-200",
		SecretName: "prod-db",
		Key:        "password",
		Principal:  "alice@example.com",
	})
	if err != nil {
		t.Fatalf("RecordSecretResolution: %v", err)
	}

	// Record another audit entry directly
	err = store.RecordSecretAuditEntry(ctx, model.SecretAuditEntry{
		Timestamp:  now.Add(time.Second),
		TaskID:     "task-101",
		RunID:      "run-201",
		SecretName: "api-key",
		Key:        "token",
		Principal:  "bob@example.com",
	})
	if err != nil {
		t.Fatalf("RecordSecretAuditEntry: %v", err)
	}

	// List audit entries
	logs, err := store.ListSecretAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListSecretAudit: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 audit entries, got %d", len(logs))
	}
	// Ordered descending by timestamp
	if logs[0].TaskID != "task-101" || logs[1].TaskID != "task-100" {
		t.Fatalf("unexpected order or tasks: %+v", logs)
	}
	if logs[1].SecretName != "prod-db" || logs[1].Principal != "alice@example.com" {
		t.Fatalf("unexpected audit entry details: %+v", logs[1])
	}
}
