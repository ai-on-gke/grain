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

	"github.com/ai-on-gke/grain/pkg/secrets"
)

// Compile-time check that *Store implements secrets.Catalogue.
var _ secrets.Catalogue = (*Store)(nil)

// ListSecretEntries returns all non-secret catalogue entries recorded in
// secret_entries, ordered by (secret_name, key).
func (s *Store) ListSecretEntries(ctx context.Context) ([]secrets.CatalogueEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `secret_name`, `key`, `sm_secret`, `version`, `updated_at` "+
			"FROM `secret_entries` ORDER BY `secret_name` ASC, `key` ASC")
	if err != nil {
		return nil, fmt.Errorf("listing secret entries: %w", err)
	}
	defer rows.Close()

	var out []secrets.CatalogueEntry
	for rows.Next() {
		var e secrets.CatalogueEntry
		if err := rows.Scan(&e.SecretName, &e.Key, &e.SMSecret, &e.Version, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning secret entry: %w", err)
		}
		e.UpdatedAt = e.UpdatedAt.UTC()
		out = append(out, e)
	}
	if out == nil {
		out = []secrets.CatalogueEntry{}
	}
	return out, rows.Err()
}

// GetSecretEntry fetches one catalogue row from secret_entries by
// (secret_name, key).
func (s *Store) GetSecretEntry(ctx context.Context, secretName, key string) (secrets.CatalogueEntry, bool, error) {
	var e secrets.CatalogueEntry
	err := s.db.QueryRowContext(ctx,
		"SELECT `secret_name`, `key`, `sm_secret`, `version`, `updated_at` "+
			"FROM `secret_entries` WHERE `secret_name` = ? AND `key` = ?",
		secretName, key).Scan(&e.SecretName, &e.Key, &e.SMSecret, &e.Version, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return secrets.CatalogueEntry{}, false, nil
	}
	if err != nil {
		return secrets.CatalogueEntry{}, false, fmt.Errorf("getting secret entry %s/%s: %w", secretName, key, err)
	}
	e.UpdatedAt = e.UpdatedAt.UTC()
	return e, true, nil
}

// PutSecretEntry inserts or updates one non-secret catalogue row in
// secret_entries.
func (s *Store) PutSecretEntry(ctx context.Context, entry secrets.CatalogueEntry) error {
	if entry.SecretName == "" || entry.Key == "" {
		return errors.New("model: secret_name and key are required")
	}
	if entry.SMSecret == "" {
		return errors.New("model: sm_secret is required")
	}
	ts := entry.UpdatedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	} else {
		ts = ts.UTC()
	}
	return s.write(ctx, "put secret entry "+entry.SecretName+"/"+entry.Key, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `secret_entries` (`secret_name`, `key`, `sm_secret`, `version`, `updated_at`) "+
				"VALUES (?, ?, ?, ?, ?) "+
				"ON CONFLICT (`secret_name`, `key`) DO UPDATE SET "+
				"`sm_secret` = excluded.`sm_secret`, "+
				"`version` = excluded.`version`, "+
				"`updated_at` = excluded.`updated_at`",
			entry.SecretName, entry.Key, entry.SMSecret, entry.Version, ts)
		if err != nil {
			return fmt.Errorf("upserting secret entry %s/%s: %w", entry.SecretName, entry.Key, err)
		}
		return nil
	})
}

// DeleteSecretEntry removes a single (secret_name, key) row from
// secret_entries.
func (s *Store) DeleteSecretEntry(ctx context.Context, secretName, key string) error {
	return s.write(ctx, "delete secret entry "+secretName+"/"+key, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `secret_entries` WHERE `secret_name` = ? AND `key` = ?",
			secretName, key)
		if err != nil {
			return fmt.Errorf("deleting secret entry %s/%s: %w", secretName, key, err)
		}
		return nil
	})
}

// DeleteSecretEntries removes all rows for secretName from secret_entries.
func (s *Store) DeleteSecretEntries(ctx context.Context, secretName string) error {
	return s.write(ctx, "delete secret entries for "+secretName, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `secret_entries` WHERE `secret_name` = ?",
			secretName)
		if err != nil {
			return fmt.Errorf("deleting secret entries for %s: %w", secretName, err)
		}
		return nil
	})
}
