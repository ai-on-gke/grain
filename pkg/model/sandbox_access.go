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

// SandboxAccessCredential is the persisted SHA-256 token digest and expiry
// for a live k8s-granule sandbox's reverse control channel
// (docs/design/grain-on-gke.md#GKE-2). The raw bearer token is never stored.
type SandboxAccessCredential struct {
	Sandbox   string
	TokenHash string
	NotAfter  time.Time
}

// SetSandboxAccess records sandbox's SHA-256 token digest and expiry time.
func (s *Store) SetSandboxAccess(ctx context.Context, sandbox, tokenHash string, notAfter time.Time) error {
	if sandbox == "" {
		return fmt.Errorf("model: sandbox id is required")
	}
	if tokenHash == "" {
		return fmt.Errorf("model: sandbox access token hash is required")
	}
	return s.write(ctx, "set sandbox "+sandbox+" access credential", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `sandbox_access` (`sandbox`,`token_hash`,`not_after`) VALUES (?,?,?) "+
				"ON CONFLICT (`sandbox`) DO UPDATE SET `token_hash` = excluded.`token_hash`, `not_after` = excluded.`not_after`",
			sandbox, tokenHash, notAfter.UTC())
		return err
	})
}

// DeleteSandboxAccess clears the stored token hash for sandbox when it is
// released or reaped.
func (s *Store) DeleteSandboxAccess(ctx context.Context, sandbox string) error {
	if sandbox == "" {
		return nil
	}
	return s.write(ctx, "clear sandbox "+sandbox+" access credential", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `sandbox_access` WHERE `sandbox` = ?", sandbox)
		return err
	})
}

// DeleteFinishedSandboxAccess clears the stored token hashes of sandboxes
// whose owning task_run or ci_job has finished and that no unfinished run
// or job still claims. SandboxAccessCredentials already leaves those rows
// out, so nothing else ever sees them to clear them: a process that died
// between finishing a run and releasing its sandbox would otherwise leave
// the row behind for good.
func (s *Store) DeleteFinishedSandboxAccess(ctx context.Context) error {
	return s.write(ctx, "clear finished sandboxes' access credentials", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
DELETE FROM `+"`sandbox_access`"+`
WHERE (
    EXISTS (
        SELECT 1 FROM `+"`task_run`"+` AS tr
        WHERE tr.`+"`sandbox`"+` = `+"`sandbox_access`"+`.`+"`sandbox`"+` AND tr.`+"`finished_at`"+` IS NOT NULL
    )
    OR EXISTS (
        SELECT 1 FROM `+"`ci_job`"+` AS cj
        WHERE cj.`+"`sandbox`"+` = `+"`sandbox_access`"+`.`+"`sandbox`"+` AND cj.`+"`finished_at`"+` IS NOT NULL
    )
)
AND NOT EXISTS (
    SELECT 1 FROM `+"`task_run`"+` AS tr
    WHERE tr.`+"`sandbox`"+` = `+"`sandbox_access`"+`.`+"`sandbox`"+` AND tr.`+"`finished_at`"+` IS NULL
)
AND NOT EXISTS (
    SELECT 1 FROM `+"`ci_job`"+` AS cj
    WHERE cj.`+"`sandbox`"+` = `+"`sandbox_access`"+`.`+"`sandbox`"+` AND cj.`+"`finished_at`"+` IS NULL
)`)
		return err
	})
}

// GetSandboxAccess returns the stored token hash and expiry for sandbox, if
// one is recorded.
func (s *Store) GetSandboxAccess(ctx context.Context, sandbox string) (SandboxAccessCredential, bool, error) {
	if sandbox == "" {
		return SandboxAccessCredential{}, false, nil
	}
	var c SandboxAccessCredential
	err := s.db.QueryRowContext(ctx,
		"SELECT `sandbox`, `token_hash`, `not_after` FROM `sandbox_access` WHERE `sandbox` = ?",
		sandbox).Scan(&c.Sandbox, &c.TokenHash, &c.NotAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return SandboxAccessCredential{}, false, nil
	}
	if err != nil {
		return SandboxAccessCredential{}, false, err
	}
	return c, true, nil
}

// SandboxAccessCredentials returns the stored sandbox-access credentials for
// all readoptable sandboxes. Rows whose owning task_run or ci_job has already
// finished are excluded so finished runs are never re-admitted on startup.
func (s *Store) SandboxAccessCredentials(ctx context.Context) ([]SandboxAccessCredential, error) {
	var out []SandboxAccessCredential
	err := each(ctx, s.db, `
SELECT sa.`+"`sandbox`"+`, sa.`+"`token_hash`"+`, sa.`+"`not_after`"+`
FROM `+"`sandbox_access`"+` AS sa
WHERE NOT EXISTS (
    SELECT 1 FROM `+"`task_run`"+` AS tr
    WHERE tr.`+"`sandbox`"+` = sa.`+"`sandbox`"+` AND tr.`+"`finished_at`"+` IS NOT NULL
)
AND NOT EXISTS (
    SELECT 1 FROM `+"`ci_job`"+` AS cj
    WHERE cj.`+"`sandbox`"+` = sa.`+"`sandbox`"+` AND cj.`+"`finished_at`"+` IS NOT NULL
)
ORDER BY sa.`+"`sandbox`"+` ASC`, nil,
		func(rows *sql.Rows) error {
			var c SandboxAccessCredential
			if err := rows.Scan(&c.Sandbox, &c.TokenHash, &c.NotAfter); err != nil {
				return err
			}
			out = append(out, c)
			return nil
		})
	return out, err
}
