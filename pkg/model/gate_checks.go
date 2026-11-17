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
	"slices"
	"strconv"

	"github.com/ai-on-gke/grain/pkg/gates"
)

// CheckSubjects is the complete task-owned change scope. Off-target external
// links are never a reason to read another repository. More than eight changes
// is an explained incomplete collection, not a silently truncated passing set.
func CheckSubjects(t Task) []gates.Subject {
	out := []gates.Subject{}
	for _, l := range t.Links {
		var s gates.Subject
		switch l.Kind {
		case LinkFixes, LinkExternalPR:
			ref, err := ParsePullRequestRef(l.Target)
			if err != nil || PullRequestOffTarget(t, ref) {
				continue
			}
			s = gates.Subject{Kind: "change", Authority: ref.Repo.GitHost(), Repository: ref.Repo.String(), ChangeKind: "pr", ID: strconv.Itoa(ref.Number)}
		case LinkChangelist:
			repo := "unresolved"
			if t.ChangeTarget != nil {
				repo = t.ChangeTarget.Kind
			}
			s = gates.Subject{Kind: "change", Authority: "provider", Repository: repo, ChangeKind: "cl", ID: l.Target}
		default:
			continue
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// GateChecksInput reads ordinary local records, never a transported snapshot or
// evaluator state. The fence is re-read under diagnostic acceptance's writer
// transaction, so head CI/task/policy changes during external reads cannot pass.
func (s *Store) GateChecksInput(ctx context.Context, id string) (*Task, *Observation, string, error) {
	return gateChecksInput(ctx, s.db, id)
}
func gateChecksInput(ctx context.Context, q querier, id string) (*Task, *Observation, string, error) {
	t, err := getTask(ctx, q, id)
	if err != nil {
		return nil, nil, "", err
	}
	if t == nil {
		return nil, nil, "", sql.ErrNoRows
	}
	o, err := getObservation(ctx, q, id)
	if err != nil {
		return nil, nil, "", err
	}
	cfg, err := scanConfig(q.QueryRowContext(ctx, "SELECT "+configColumns+" FROM `grain_config` WHERE `id` = 1").Scan)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, "", err
	}
	parts := []any{t.ID, t.Target, t.ChangeTarget, t.Grants, t.Links, t.AutoMerge, cfg}
	if o != nil {
		parts = append(parts, o)
	}
	repos := []RepoRef{}
	for _, sub := range CheckSubjects(*t) {
		if sub.ChangeKind == "pr" {
			r, e := ParseRepo(sub.Repository)
			if e == nil && !slices.Contains(repos, r) {
				repos = append(repos, r)
			}
		}
	}
	for _, repo := range repos {
		c, e := getRepoConfig(ctx, q, repo)
		if e != nil {
			return nil, nil, "", e
		}
		parts = append(parts, c)
		var rules string
		e = q.QueryRowContext(ctx, "SELECT `rules` FROM `repo_merge_rules` WHERE `owner` = ? AND `name` = ?", repo.StoredOwner(), repo.Name).Scan(&rules)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return nil, nil, "", e
		}
		parts = append(parts, rules)
		// These ordinary scoped records explain queue/repair preconditions. Fence
		// them as well: accepting an old recheck/base row after it changed would
		// mislabel readiness even though CI itself never controls the executor.
		for _, query := range []struct {
			sql  string
			args []any
		}{
			{"SELECT `branch`,`base_commit`,`commit`,`members`,`run_id`,`conclusion`,`finished_at` FROM `merge_batch` WHERE `owner` = ? AND `name` = ? AND `task_id` = ? ORDER BY `branch` LIMIT 513", []any{repo.StoredOwner(), repo.Name, id}},
			{"SELECT `branch`,`run_id`,`commit`,`conclusion`,`confirmation` FROM `base_branch_ci` WHERE `owner` = ? AND `name` = ? ORDER BY `branch` LIMIT 513", []any{repo.StoredOwner(), repo.Name}},
		} {
			version, e := gateRowsVersion(ctx, q, query.sql, query.args...)
			if e != nil {
				return nil, nil, "", e
			}
			parts = append(parts, version)
		}
		rows, e := q.QueryContext(ctx, "SELECT `path`,`text` FROM `repo_workflow` WHERE `owner` = ? AND `name` = ? ORDER BY `path`", repo.StoredOwner(), repo.Name)
		if e != nil {
			return nil, nil, "", e
		}
		n := 0
		for rows.Next() {
			var path, text string
			if e = rows.Scan(&path, &text); e != nil {
				rows.Close()
				return nil, nil, "", e
			}
			parts = append(parts, gates.Digest([]string{path, text}))
			n++
			if n > 512 {
				rows.Close()
				return nil, nil, "", fmt.Errorf("workflow collection exceeds bound")
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, nil, "", e
		}
	}
	return t, o, gates.Digest(parts), nil
}

func gateRowsVersion(ctx context.Context, q querier, query string, args ...any) (string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	records := [][]any{}
	for rows.Next() {
		if len(records) >= 512 {
			return "", fmt.Errorf("readiness context exceeds bound")
		}
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err = rows.Scan(ptrs...); err != nil {
			return "", err
		}
		records = append(records, values)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return gates.Digest(records), nil
}
