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
	"fmt"
	"strings"
	"time"
)

// ConfigAuditEntry is one change somebody made to the deployment's
// configuration -- its settings, a repo's, a schedule, a template -- and
// who made it (operator-attribution spec §4.4). Summary says what changed
// and never carries a secret value.
type ConfigAuditEntry struct {
	ID         int64
	At         time.Time
	By         Principal
	OnBehalfOf *Principal
	// Surface is what was changed: `settings`, `repo:<owner/name>`,
	// `schedule:<id>`, ...
	Surface string
	Summary string
}

// RecordConfigAudit appends one entry and returns its id.
func (s *Store) RecordConfigAudit(ctx context.Context, e ConfigAuditEntry) (int64, error) {
	if strings.TrimSpace(e.Surface) == "" {
		return 0, fmt.Errorf("recording a config audit entry: no surface")
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	var id int64
	err := s.write(ctx, "record a change to "+e.Surface, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO `config_audit` (`at`,`actor_kind`,`actor_id`,`actor_source`,"+
				"`behalf_kind`,`behalf_id`,`behalf_source`,`surface`,`summary`) VALUES (?,?,?,?,?,?,?,?,?)",
			e.At.UTC(), string(e.By.Kind), e.By.ID, string(e.By.Source),
			behalfColumn(e.OnBehalfOf, func(p Principal) string { return string(p.Kind) }),
			behalfColumn(e.OnBehalfOf, func(p Principal) string { return p.ID }),
			sourceOf(e.OnBehalfOf), e.Surface, e.Summary)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// ConfigAudit is the config audit log, newest first, at most limit
// entries (all of them for limit <= 0).
func (s *Store) ConfigAudit(ctx context.Context, limit int) ([]ConfigAuditEntry, error) {
	if limit <= 0 {
		limit = -1
	}
	var out []ConfigAuditEntry
	err := each(ctx, s.db,
		"SELECT `id`,`at`,`actor_kind`,`actor_id`,`actor_source`,`behalf_kind`,`behalf_id`,`behalf_source`,"+
			"`surface`,`summary` FROM `config_audit` ORDER BY `at` DESC, `id` DESC LIMIT ?", limit,
		func(rows *sql.Rows) error {
			var e ConfigAuditEntry
			var kind, source, behalfSource string
			var behalfKind, behalfID sql.NullString
			if err := rows.Scan(&e.ID, &e.At, &kind, &e.By.ID, &source, &behalfKind, &behalfID, &behalfSource,
				&e.Surface, &e.Summary); err != nil {
				return err
			}
			e.By.Kind, e.By.Source = PrincipalKind(kind), AuthSource(source)
			e.OnBehalfOf = principalFrom(behalfKind, behalfID, behalfSource)
			out = append(out, e)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading the config audit log: %w", err)
	}
	return out, nil
}
