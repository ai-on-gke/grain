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

// Hostd selectors, and which grain hostd each run went to.
//
// A hostd selector is one hostd's registry name, or a comma-separated
// list of label=value pairs any hostd carrying every one of them
// matches. It is what a model connection (AgentBackend.Hostd) and a
// run: hostd capability server (protocol.ServerConfig.Hostd) name to
// place their work; no task carries one (schema 111 dropped the task,
// template and repository selectors). Each run that resolved a hostd
// for its agent CLI records it in task_run_hostd beside the frozen
// task_run row: the hostd's name, the machine it ran on (its instance)
// and its registry owner. That record is read-only history.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ParseHostdSelector reads a stored or submitted hostd selector: "" is
// the default hostd, a bare name pins that hostd, and k=v[,k=v...]
// selects by labels.
func ParseHostdSelector(s string) (name string, labels map[string]string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil, nil
	}
	if !strings.Contains(s, "=") {
		if err := ValidateHostdName(s); err != nil {
			return "", nil, err
		}
		return s, nil, nil
	}
	labels = map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" || strings.ContainsAny(k+v, ",= \t\n\r") {
			return "", nil, fmt.Errorf("host selector %q: %q is not label=value", s, part)
		}
		labels[k] = v
	}
	if len(labels) == 0 {
		return "", nil, fmt.Errorf("host selector %q names no label", s)
	}
	return "", labels, nil
}

// NormalizeHostdSelector is s as it is stored: a name as given, labels
// sorted by key, "" for the default.
func NormalizeHostdSelector(s string) (string, error) {
	name, labels, err := ParseHostdSelector(s)
	if err != nil || name != "" || len(labels) == 0 {
		return name, err
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ","), nil
}

// TaskRunHostd is which grain hostd one run's host work went to: its
// registry name, the machine that hostd ran on (hostd.RemotePeer.Host,
// "" for the co-located one) and its registry owner.
type TaskRunHostd struct {
	RunID    string    `json:"-"`
	TaskID   string    `json:"-"`
	Hostd    string    `json:"hostd"`
	Instance string    `json:"instance,omitempty"`
	Owner    string    `json:"owner,omitempty"`
	At       time.Time `json:"at"`
	// Deleted is set by a reader, never stored: the registry no longer
	// holds the hostd this run used -- it was deleted, or deleted and its
	// name registered again since (ui's markDeletedRunHostds). Hostd
	// still names it, as the run recorded.
	Deleted bool `json:"deleted,omitempty"`
}

// RecordTaskRunHostd records which hostd r.RunID resolved to, replacing
// what an earlier resolution of the same run said.
func (s *Store) RecordTaskRunHostd(ctx context.Context, r TaskRunHostd) error {
	if r.At.IsZero() {
		r.At = time.Now()
	}
	return s.write(ctx, "record run "+r.RunID+"'s hostd", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM `task_run_hostd` WHERE `run_id` = ?", r.RunID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `task_run_hostd` (`run_id`,`task_id`,`hostd`,`instance`,`owner`,`at`) VALUES (?,?,?,?,?,?)",
			r.RunID, r.TaskID, r.Hostd, r.Instance, r.Owner, r.At.UTC())
		return err
	})
}

// RunHostd is the hostd runID recorded, or nil.
func (s *Store) RunHostd(ctx context.Context, runID string) (*TaskRunHostd, error) {
	var r TaskRunHostd
	err := s.db.QueryRowContext(ctx,
		"SELECT `run_id`,`task_id`,`hostd`,`instance`,`owner`,`at` FROM `task_run_hostd` WHERE `run_id` = ?", runID).
		Scan(&r.RunID, &r.TaskID, &r.Hostd, &r.Instance, &r.Owner, &r.At)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading run %s's hostd: %w", runID, err)
	}
	r.At = r.At.UTC()
	return &r, nil
}

// TaskRunHostds is the hostd each of taskID's runs recorded, by run id.
func (s *Store) TaskRunHostds(ctx context.Context, taskID string) (map[string]TaskRunHostd, error) {
	return s.TaskRunHostdsInRange(ctx, taskID, 0, 0)
}

// TaskRunHostdsInRange is TaskRunHostds bounded to attempts [minAttempt,
// maxAttempt] when maxAttempt > 0.
func (s *Store) TaskRunHostdsInRange(ctx context.Context, taskID string, minAttempt, maxAttempt int) (map[string]TaskRunHostd, error) {
	out := map[string]TaskRunHostd{}
	query := "SELECT `h`.`run_id`,`h`.`task_id`,`h`.`hostd`,`h`.`instance`,`h`.`owner`,`h`.`at` FROM `task_run_hostd` AS `h` WHERE `h`.`task_id` = ?"
	args := []any{taskID}
	if maxAttempt > 0 {
		query = "SELECT `h`.`run_id`,`h`.`task_id`,`h`.`hostd`,`h`.`instance`,`h`.`owner`,`h`.`at` FROM `task_run_hostd` AS `h` " +
			"JOIN `task_run` AS `r` ON `r`.`id` = `h`.`run_id` " +
			"WHERE `h`.`task_id` = ? AND `r`.`attempt` >= ? AND `r`.`attempt` <= ?"
		args = append(args, minAttempt, maxAttempt)
	}
	err := each(ctx, s.db,
		query,
		args,
		func(rows *sql.Rows) error {
			var r TaskRunHostd
			if err := rows.Scan(&r.RunID, &r.TaskID, &r.Hostd, &r.Instance, &r.Owner, &r.At); err != nil {
				return err
			}
			r.At = r.At.UTC()
			out[r.RunID] = r
			return nil
		})
	return out, err
}
