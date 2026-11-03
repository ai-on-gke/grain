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
	"time"
)

// ScheduleRefCursor is where a git_ref schedule (RecurrenceGitRef) last
// saw the ref it watches: the repository and ref it was watching then,
// and the commit that ref peeled to. Grain's note to itself, off
// SettingsTables like path_trigger_cursor.
//
// Repo and Ref are kept so a schedule whose trigger is edited to watch
// something else starts over from where that ref is, rather than reading
// the old ref's commit as the new one's and firing on the difference.
type ScheduleRefCursor struct {
	ScheduleID string
	Repo       string
	Ref        string
	Commit     string
	UpdatedAt  time.Time
}

// GetScheduleRefCursor reads scheduleID's cursor, or nil (with no error)
// for a schedule that has never looked.
func (s *Store) GetScheduleRefCursor(ctx context.Context, scheduleID string) (*ScheduleRefCursor, error) {
	c := ScheduleRefCursor{ScheduleID: scheduleID}
	err := s.db.QueryRowContext(ctx,
		"SELECT `repo`,`ref`,`commit`,`updated_at` FROM `git_ref_cursor` WHERE `schedule_id` = ?",
		scheduleID).Scan(&c.Repo, &c.Ref, &c.Commit, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading schedule %s's ref cursor: %w", scheduleID, err)
	}
	c.UpdatedAt = c.UpdatedAt.UTC()
	return &c, nil
}

// PutScheduleRefCursor records c, replacing the schedule's cursor before it.
func (s *Store) PutScheduleRefCursor(ctx context.Context, c ScheduleRefCursor) error {
	return s.write(ctx, "record ref cursor for schedule "+c.ScheduleID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `git_ref_cursor` (`schedule_id`,`repo`,`ref`,`commit`,`updated_at`) VALUES (?,?,?,?,?)",
			c.ScheduleID, c.Repo, c.Ref, c.Commit, c.UpdatedAt.UTC())
		return err
	})
}
