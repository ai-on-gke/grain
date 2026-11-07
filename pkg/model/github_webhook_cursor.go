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

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

const defaultGitHubWebhookCursorID = "default"

// GitHubWebhookCursor records the highest GitHub webhook sequence number the
// daemon has acknowledged when persisting its cursor in the database.
type GitHubWebhookCursor struct {
	LastAckedSeq uint64
	UpdatedAt    time.Time
}

// GetGitHubWebhookCursor returns the stored GitHub webhook cursor, or (nil, nil)
// when no cursor row has been recorded yet.
func (s *Store) GetGitHubWebhookCursor(ctx context.Context) (*GitHubWebhookCursor, error) {
	var (
		seq       int64
		updatedAt time.Time
	)
	err := s.db.QueryRowContext(ctx,
		"SELECT `last_acked_seq`, `updated_at` FROM `github_webhook_cursor` WHERE `id` = ?",
		defaultGitHubWebhookCursorID).Scan(&seq, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading GitHub webhook cursor: %w", err)
	}
	if seq < 0 {
		seq = 0
	}
	return &GitHubWebhookCursor{
		LastAckedSeq: uint64(seq),
		UpdatedAt:    updatedAt.UTC(),
	}, nil
}

// SetGitHubWebhookCursor upserts the stored GitHub webhook cursor.
func (s *Store) SetGitHubWebhookCursor(ctx context.Context, lastAckedSeq uint64, updatedAt time.Time) error {
	if lastAckedSeq > math.MaxInt64 {
		return fmt.Errorf("github webhook cursor sequence %d overflows int64", lastAckedSeq)
	}
	return s.write(ctx, "advance github webhook cursor", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `github_webhook_cursor` (`id`, `last_acked_seq`, `updated_at`) VALUES (?, ?, ?)",
			defaultGitHubWebhookCursorID, int64(lastAckedSeq), updatedAt.UTC())
		return err
	})
}
