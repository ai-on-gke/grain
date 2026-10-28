package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// StagedAttachment is one file a live run attached by path to
// comment_on_issue or ask_question, read out of its sandbox at the moment
// it made the call and held here until the run's words are relayed
// (task_run_staged_attachment). Path is the path exactly as the run wrote
// it in the call, which is the key the finish path finds it by again.
type StagedAttachment struct {
	RunID       string
	Path        string
	Filename    string
	ContentType string
	Content     []byte
	CreatedAt   time.Time
}

// StageAttachment holds a's file against taskID's live run, replacing
// whatever that run staged under the same path before. live is false,
// and nothing is written, when the task has no run in flight: there is
// then no finish left to relay the file, and the caller says so rather
// than storing bytes nothing will ever read.
func (s *Store) StageAttachment(ctx context.Context, taskID string, a StagedAttachment) (live bool, err error) {
	err = s.write(ctx, "stage attachment "+a.Path+" for task "+taskID, func(tx *sql.Tx) error {
		var runID string
		err := tx.QueryRowContext(ctx,
			"SELECT `id` FROM `task_run` WHERE `task_id` = ? AND `finished_at` IS NULL "+
				"ORDER BY `started_at` DESC LIMIT 1", taskID).Scan(&runID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		live = true
		_, err = tx.ExecContext(ctx,
			"INSERT INTO `task_run_staged_attachment` "+
				"(`run_id`,`path`,`filename`,`content_type`,`content`,`created_at`) VALUES (?,?,?,?,?,?) "+
				"ON CONFLICT (`run_id`,`path`) DO UPDATE SET `filename` = excluded.`filename`, "+
				"`content_type` = excluded.`content_type`, `content` = excluded.`content`, "+
				"`created_at` = excluded.`created_at`",
			runID, a.Path, a.Filename, a.ContentType, a.Content, a.CreatedAt.UTC())
		return err
	})
	return live, err
}

// StagedAttachments is every file runID staged, keyed by the path it
// named.
func (s *Store) StagedAttachments(ctx context.Context, runID string) (map[string]StagedAttachment, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+
		"`path`, `filename`, `content_type`, `content`, `created_at` "+
		"FROM `task_run_staged_attachment` WHERE `run_id` = ?", runID)
	if err != nil {
		return nil, fmt.Errorf("reading staged attachments of run %s: %w", runID, err)
	}
	defer rows.Close()
	out := map[string]StagedAttachment{}
	for rows.Next() {
		a := StagedAttachment{RunID: runID}
		if err := rows.Scan(&a.Path, &a.Filename, &a.ContentType, &a.Content, &a.CreatedAt); err != nil {
			return nil, err
		}
		out[a.Path] = a
	}
	return out, rows.Err()
}

// ClearStagedAttachments deletes every file runID staged: what the finish
// path does once it has relayed them, or decided it never will.
func (s *Store) ClearStagedAttachments(ctx context.Context, runID string) error {
	return s.write(ctx, "clear staged attachments of run "+runID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM `task_run_staged_attachment` WHERE `run_id` = ?", runID)
		return err
	})
}
