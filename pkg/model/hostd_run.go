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

// Which grain hostd a task's host work goes to, and which one each of
// its runs went to.
//
// A task's pin (task_hostd) is a hostd selector: one hostd's registry
// name, or a comma-separated list of label=value pairs any hostd
// carrying every one of them matches. No row is the default hostd --
// local, or the deprecated shared token's hostd. Each run that resolved
// a hostd records it in task_run_hostd beside the frozen task_run row:
// the hostd's name, the machine it ran on (its instance) and its
// registry owner.

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
		if !ok || k == "" {
			return "", nil, fmt.Errorf("hostd selector %q: %q is not label=value", s, part)
		}
		labels[k] = v
	}
	if len(labels) == 0 {
		return "", nil, fmt.Errorf("hostd selector %q names no label", s)
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

// SetTaskHostd pins taskID's host work to selector; "" clears the pin,
// back to the default hostd.
func (s *Store) SetTaskHostd(ctx context.Context, taskID, selector string) error {
	selector, err := NormalizeHostdSelector(selector)
	if err != nil {
		return err
	}
	return s.write(ctx, "pin task "+taskID+"'s hostd", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM `task_hostd` WHERE `task_id` = ?", taskID); err != nil {
			return err
		}
		if selector == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `task_hostd` (`task_id`,`selector`,`updated_at`) VALUES (?,?,?)",
			taskID, selector, time.Now().UTC())
		return err
	})
}

// putTaskHostd is SetTaskHostd inside a transaction that is filing the
// task, for a filing whose composition carries a pin (Composition.Hostd);
// "" writes nothing.
func putTaskHostd(ctx context.Context, tx *sql.Tx, taskID, selector string) error {
	selector, err := NormalizeHostdSelector(selector)
	if err != nil || selector == "" {
		return err
	}
	_, err = tx.ExecContext(ctx,
		"REPLACE INTO `task_hostd` (`task_id`,`selector`,`updated_at`) VALUES (?,?,?)",
		taskID, selector, time.Now().UTC())
	return err
}

// RepoDefaultHostd is the hostd pin or label selector repo's tasks start
// with when neither their filing nor their template names one
// (RepoConfig.DefaultHostd), "" for a repository that names none.
func (s *Store) RepoDefaultHostd(ctx context.Context, repo RepoRef) (string, error) {
	return repoDefaultHostd(ctx, s.db, repo)
}

func repoDefaultHostd(ctx context.Context, q querier, repo RepoRef) (string, error) {
	cfg, err := getRepoConfig(ctx, q, repo)
	if err != nil || cfg == nil {
		return "", err
	}
	return cfg.DefaultHostd, nil
}

// applyRepoDefaultHostd fills a composition its template left on the
// default hostd with its target repository's own default
// (RepoConfig.DefaultHostd): the template's pin wins, and the repo's
// beats the deployment's default hostd -- the same precedence
// ui.(*Client).CreateTask applies to a filing.
func applyRepoDefaultHostd(ctx context.Context, q querier, comp *Composition) error {
	if comp.Hostd != "" || comp.Target == nil {
		return nil
	}
	hostd, err := repoDefaultHostd(ctx, q, *comp.Target)
	if err != nil || hostd == "" {
		return err
	}
	comp.Hostd, comp.Sources.Hostd = hostd, LayerRepo
	return nil
}

// TaskHostd is taskID's hostd selector, "" for the default hostd.
func (s *Store) TaskHostd(ctx context.Context, taskID string) (string, error) {
	var selector string
	err := s.db.QueryRowContext(ctx, "SELECT `selector` FROM `task_hostd` WHERE `task_id` = ?", taskID).Scan(&selector)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading task %s's hostd: %w", taskID, err)
	}
	return selector, nil
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
	out := map[string]TaskRunHostd{}
	err := each(ctx, s.db,
		"SELECT `run_id`,`task_id`,`hostd`,`instance`,`owner`,`at` FROM `task_run_hostd` WHERE `task_id` = ?",
		[]any{taskID},
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
