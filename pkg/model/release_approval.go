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
	"strings"
	"time"
)

// ReleaseApprovalHold is how long a person's dispatched answer keeps a
// pending release's commit answered while promote-release.yml runs and the
// vX.Y.Z-pending tag outlives the answer: the release-pending watcher does
// not raise release.pending for that commit, and DecideRelease refuses a
// second dispatch for it. If the -pending tag still points at the same
// commit after this (its promotion run failed), the watcher raises again
// and a person may dispatch again, as the daily train run would on grain CI.
const ReleaseApprovalHold = 24 * time.Hour

// ReleaseApprovalDecision is grain's durable note of the promote-release
// workflow dispatch a person started for a pending GitHub Actions release
// (vX.Y.Z-pending at Commit), keyed by (Repo, Version, Commit).
//
// Persisting this in the store rather than an in-memory 15-minute map lets
// both watchPendingReleases and ui.Client.DecideRelease consult the same
// fact: approving from the CLI or API before the watcher has polled does
// not re-raise release.pending:vX.Y.Z at the next poll, and pressing
// Approve past the signed record's 15-minute expiry (or across a daemon
// restart) while the dispatch is still outstanding does not start a second
// promote-release.yml run. Keying by Commit means a rejected or re-cut
// candidate staged later under the same vX.Y.Z at a new commit is raised
// and approvable at once.
type ReleaseApprovalDecision struct {
	Repo      RepoRef
	Version   string // vX.Y.Z
	Commit    string // the commit vX.Y.Z-pending pointed at when decided
	Decision  string // "approve" or "reject"
	Workflow  string // e.g. "promote-release.yml"
	DecidedBy string // the person's principal id
	DecidedAt time.Time
}

const releaseApprovalFailedPrefix = "failed:"

// Failed reports whether the dispatched promote-release run ended in failure
// without promoting or rejecting the release. A failed decision no longer
// holds the commit -- the pending release is raised again with the run's
// failure, and a person may dispatch again without waiting out
// ReleaseApprovalHold.
func (d ReleaseApprovalDecision) Failed() bool {
	return strings.HasPrefix(d.Decision, releaseApprovalFailedPrefix)
}

// DecisionVerb returns the underlying decision ("approve" or "reject"),
// stripping any failure marker.
func (d ReleaseApprovalDecision) DecisionVerb() string {
	return strings.TrimPrefix(d.Decision, releaseApprovalFailedPrefix)
}

// MarkFailed returns a copy of d marked as failed. DecidedAt is preserved so
// watchPendingReleases still ignores the notification clear that accompanied
// this dispatch.
func (d ReleaseApprovalDecision) MarkFailed() ReleaseApprovalDecision {
	if !d.Failed() {
		d.Decision = releaseApprovalFailedPrefix + d.Decision
	}
	return d
}

// ActiveAt reports whether d still holds its (Repo, Version, Commit) at
// now -- unfailed and within ReleaseApprovalHold of DecidedAt.
func (d ReleaseApprovalDecision) ActiveAt(now time.Time) bool {
	return !d.Failed() && now.Sub(d.DecidedAt) < ReleaseApprovalHold
}

// GetReleaseApprovalDecision reads what a person last dispatched for repo's
// pending version at commit, or (nil, nil) if that commit has not been
// answered.
func (s *Store) GetReleaseApprovalDecision(ctx context.Context, repo RepoRef, version, commit string) (*ReleaseApprovalDecision, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT `decision`,`workflow`,`decided_by`,`decided_at` "+
			"FROM `release_approval` WHERE `owner` = ? AND `name` = ? AND `version` = ? AND `commit` = ?",
		repo.StoredOwner(), repo.Name, version, commit)
	d := ReleaseApprovalDecision{Repo: repo, Version: version, Commit: commit}
	var decidedAt time.Time
	if err := row.Scan(&d.Decision, &d.Workflow, &d.DecidedBy, &decidedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading release approval decision for %s %s (%s): %w", repo, version, commit, err)
	}
	d.DecidedAt = decidedAt.UTC()
	return &d, nil
}

// ListReleaseApprovalDecisions returns every recorded pending-release
// decision for repo, ordered deterministically.
func (s *Store) ListReleaseApprovalDecisions(ctx context.Context, repo RepoRef) ([]ReleaseApprovalDecision, error) {
	var out []ReleaseApprovalDecision
	err := each(ctx, s.db,
		"SELECT `version`,`commit`,`decision`,`workflow`,`decided_by`,`decided_at` "+
			"FROM `release_approval` WHERE `owner` = ? AND `name` = ? ORDER BY `version`,`commit`",
		[]any{repo.StoredOwner(), repo.Name},
		func(rows *sql.Rows) error {
			d := ReleaseApprovalDecision{Repo: repo}
			var decidedAt time.Time
			if err := rows.Scan(&d.Version, &d.Commit, &d.Decision, &d.Workflow, &d.DecidedBy, &decidedAt); err != nil {
				return err
			}
			d.DecidedAt = decidedAt.UTC()
			out = append(out, d)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing release approval decisions for %s: %w", repo, err)
	}
	return out, nil
}

// PutReleaseApprovalDecision records or replaces the decision made for
// (d.Repo, d.Version, d.Commit).
func (s *Store) PutReleaseApprovalDecision(ctx context.Context, d ReleaseApprovalDecision) error {
	return s.write(ctx, "record release approval decision for "+d.Repo.String()+" "+d.Version, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `release_approval` (`owner`,`name`,`version`,`commit`,`decision`,`workflow`,`decided_by`,`decided_at`) "+
				"VALUES (?,?,?,?,?,?,?,?)",
			d.Repo.StoredOwner(), d.Repo.Name, d.Version, d.Commit, d.Decision, d.Workflow, d.DecidedBy, d.DecidedAt.UTC())
		return err
	})
}
