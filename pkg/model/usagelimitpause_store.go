package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// StoredUsageLimitPause is one usage_limit_pause row: a pause
// orchestrator.Pause had in force when it last wrote, under the key it
// files that pause by (opaque here).
//
// Runtime state rather than configuration: the table is off
// SettingsTables, so a pause is neither exported to the state repository
// nor imported from it. A pause is a provider's refusal of one
// credential, as this deployment met it; importing one would stop a
// deployment dispatching onto a key that never refused it, and every
// write would be churn in a repository meant for settings.
type StoredUsageLimitPause struct {
	Key   string
	Pause UsageLimitPause
}

// PutUsageLimitPause records pause under key, replacing whatever was
// recorded there.
func (s *Store) PutUsageLimitPause(ctx context.Context, key string, pause UsageLimitPause) error {
	labels := ""
	if len(pause.Labels) > 0 {
		raw, err := json.Marshal(pause.Labels)
		if err != nil {
			return fmt.Errorf("encoding the usage-limit pause %s's labels: %w", key, err)
		}
		labels = string(raw)
	}
	return s.write(ctx, "record the usage-limit pause "+key, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `usage_limit_pause` (`scope_key`,`connection_id`,`model`,`labels`,`team`,`framework`,"+
				"`until_at`,`reason`,`escalated`,`updated_at`) VALUES (?,?,?,?,?,?,?,?,?,?)",
			key, pause.Connection, pause.Model, labels, pause.Team, pause.Framework,
			pause.Until.UTC(), pause.Reason, pause.Escalated, time.Now().UTC())
		return err
	})
}

// DeleteUsageLimitPause forgets the pause recorded under key. Deleting
// one that is not there is not an error.
func (s *Store) DeleteUsageLimitPause(ctx context.Context, key string) error {
	return s.write(ctx, "forget the usage-limit pause "+key, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM `usage_limit_pause` WHERE `scope_key` = ?", key)
		return err
	})
}

// ListUsageLimitPauses is every recorded pause, expired or not, ordered
// by key.
func (s *Store) ListUsageLimitPauses(ctx context.Context) ([]StoredUsageLimitPause, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `scope_key`,`connection_id`,`model`,`labels`,`team`,`framework`,`until_at`,`reason`,`escalated` "+
			"FROM `usage_limit_pause` ORDER BY `scope_key`")
	if err != nil {
		return nil, fmt.Errorf("listing usage-limit pauses: %w", err)
	}
	defer rows.Close()
	var out []StoredUsageLimitPause
	for rows.Next() {
		var row StoredUsageLimitPause
		var labels string
		p := &row.Pause
		if err := rows.Scan(&row.Key, &p.Connection, &p.Model, &labels, &p.Team, &p.Framework,
			&p.Until, &p.Reason, &p.Escalated); err != nil {
			return nil, fmt.Errorf("reading a usage-limit pause: %w", err)
		}
		if strings.TrimSpace(labels) != "" {
			if err := json.Unmarshal([]byte(labels), &p.Labels); err != nil {
				return nil, fmt.Errorf("reading the usage-limit pause %s's labels: %w", row.Key, err)
			}
		}
		p.Until = p.Until.UTC()
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing usage-limit pauses: %w", err)
	}
	return out, nil
}
