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
	"time"
)

// EnvironmentFailure is the host-wide reading of EnvironmentOutcome: runs
// that could not start a tool because the host running them needs a
// human (today, expired host credentials, which a registered authfix
// classifier names the command for). It is one
// finding for the deployment rather than one per task, which is how the
// deployment-health pane shows it.
type EnvironmentFailure struct {
	// Detail is the most recent such run's own detail: the sentence that
	// says what to run, as who, on which host.
	Detail string
	// Since is when the first run of the current spell finished, and
	// Last when the most recent did.
	Since, Last time.Time
	// Runs is how many runs have ended this way since the last run that
	// ended any other way, and TaskID the most recent one's task.
	Runs   int
	TaskID string
}

// CurrentEnvironmentFailure is the spell of EnvironmentOutcome runs this
// deployment is in, or nil when the most recent finished run ended any
// other way -- which is how it clears: once the credentials are renewed, the next
// run dispatched gets past starting its tools, whatever it then does.
func (s *Store) CurrentEnvironmentFailure(ctx context.Context) (*EnvironmentFailure, error) {
	var (
		f        EnvironmentFailure
		detail   sql.NullString
		finished time.Time
	)
	err := s.db.QueryRowContext(ctx,
		"SELECT `task_id`,`finished_at`,`detail` FROM `task_run` "+
			"WHERE `outcome` = ? AND `finished_at` IS NOT NULL ORDER BY `finished_at` DESC LIMIT 1",
		EnvironmentOutcome).Scan(&f.TaskID, &finished, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f.Detail, f.Last = detail.String, finished

	// The last run since then that ended any other way ends the spell.
	var later int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_run` WHERE `finished_at` > ? AND `outcome` <> ?",
		finished.UTC(), EnvironmentOutcome).Scan(&later); err != nil {
		return nil, err
	}
	if later > 0 {
		return nil, nil
	}
	// And the last one before it is where the spell began.
	var boundary time.Time
	err = s.db.QueryRowContext(ctx,
		"SELECT `finished_at` FROM `task_run` WHERE `finished_at` IS NOT NULL AND `outcome` <> ? "+
			"ORDER BY `finished_at` DESC LIMIT 1", EnvironmentOutcome).Scan(&boundary)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	runs, err := s.db.QueryContext(ctx,
		"SELECT `finished_at` FROM `task_run` WHERE `outcome` = ? AND `finished_at` > ? ORDER BY `finished_at`",
		EnvironmentOutcome, boundary.UTC())
	if err != nil {
		return nil, err
	}
	defer runs.Close()
	for runs.Next() {
		var at time.Time
		if err := runs.Scan(&at); err != nil {
			return nil, err
		}
		if f.Runs == 0 {
			f.Since = at
		}
		f.Runs++
	}
	if err := runs.Err(); err != nil {
		return nil, err
	}
	return &f, nil
}
