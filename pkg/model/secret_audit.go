package model

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/secrets"
)

// SecretAuditEntry records one resolution of a secret in the deployment.
type SecretAuditEntry struct {
	ID         int64     `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	TaskID     string    `json:"taskId,omitempty"`
	RunID      string    `json:"runId,omitempty"`
	SecretName string    `json:"secretName"`
	Key        string    `json:"key"`
	Principal  string    `json:"principal,omitempty"`
}

// RecordSecretResolution implements secrets.AuditLogger on *Store,
// recording a secret resolution event into secret_resolution_audit.
func (s *Store) RecordSecretResolution(ctx context.Context, entry secrets.AuditEntry) error {
	ts := entry.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	} else {
		ts = ts.UTC()
	}
	return s.write(ctx, "record secret resolution audit", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `secret_resolution_audit` (`timestamp`, `task_id`, `run_id`, `secret_name`, `key`, `principal`) "+
				"VALUES (?, ?, ?, ?, ?, ?)",
			ts, entry.TaskID, entry.RunID, entry.SecretName, entry.Key, entry.Principal)
		if err != nil {
			return fmt.Errorf("recording secret resolution: %w", err)
		}
		return nil
	})
}

// RecordSecretAuditEntry records a SecretAuditEntry directly.
func (s *Store) RecordSecretAuditEntry(ctx context.Context, entry SecretAuditEntry) error {
	return s.RecordSecretResolution(ctx, secrets.AuditEntry{
		Timestamp:  entry.Timestamp,
		TaskID:     entry.TaskID,
		RunID:      entry.RunID,
		SecretName: entry.SecretName,
		Key:        entry.Key,
		Principal:  entry.Principal,
	})
}

// ListSecretAudit returns the deployment's most recent secret
// resolutions, newest first.
func (s *Store) ListSecretAudit(ctx context.Context, limit int) ([]SecretAuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id`, `timestamp`, `task_id`, `run_id`, `secret_name`, `key`, `principal` "+
			"FROM `secret_resolution_audit` ORDER BY `timestamp` DESC, `id` DESC LIMIT ?",
		limit)
	if err != nil {
		return nil, fmt.Errorf("listing secret audit: %w", err)
	}
	defer rows.Close()

	var entries []SecretAuditEntry
	for rows.Next() {
		var e SecretAuditEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.TaskID, &e.RunID, &e.SecretName, &e.Key, &e.Principal); err != nil {
			return nil, fmt.Errorf("scanning secret audit row: %w", err)
		}
		e.Timestamp = e.Timestamp.UTC()
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []SecretAuditEntry{}
	}
	return entries, rows.Err()
}
