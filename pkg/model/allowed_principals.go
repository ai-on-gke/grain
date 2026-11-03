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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AllowedPrincipal is one user or group entry in the deployment's
// verified-identity sign-in allowlist (`allowed_principal`).
type AllowedPrincipal struct {
	// Entry is canonicalized as `user:<email>` or `group:<email>` with the
	// email lowercased.
	Entry   string    `json:"entry"`
	AddedBy string    `json:"addedBy"`
	AddedAt time.Time `json:"addedAt"`
}

// AllowedPrincipalsMigration records the one-time startup import off
// `-ui-allowed-principals` / `$GRAIN_ALLOWED_PRINCIPALS`.
type AllowedPrincipalsMigration struct {
	ImportedAt time.Time `json:"importedAt"`
	Source     string    `json:"source"`
	Dropped    []string  `json:"dropped,omitempty"`
}

// NormalizeAllowedPrincipalEntry validates and canonicalizes a `user:<email>`
// or `group:<email>` allowlist entry.
func NormalizeAllowedPrincipalEntry(raw string) (prefix, email, canonical string, err error) {
	item := strings.TrimSpace(raw)
	if item == "" {
		return "", "", "", fmt.Errorf("allowed principal entry is required (expected user:<email> or group:<email>)")
	}
	p, val, ok := strings.Cut(item, ":")
	if !ok {
		return "", "", "", fmt.Errorf("invalid allowed principal %q: must start with user: or group:", item)
	}
	p = strings.TrimSpace(p)
	val = strings.ToLower(strings.TrimSpace(val))
	switch p {
	case "user", "group":
	case "serviceAccount", "domain":
		return "", "", "", fmt.Errorf("invalid allowed principal %q: %s: entries are not supported; use user:<email> or group:<email>", item, p)
	default:
		return "", "", "", fmt.Errorf("invalid allowed principal %q: unknown prefix %q (expected user: or group:)", item, p)
	}
	if val == "" {
		return "", "", "", fmt.Errorf("invalid allowed principal %q: empty %s email address", item, p)
	}
	local, domain, hasAt := strings.Cut(val, "@")
	if !hasAt || local == "" || domain == "" || strings.Contains(domain, "@") ||
		strings.ContainsAny(val, " \t\r\n/,;'\"<>") {
		return "", "", "", fmt.Errorf("invalid allowed principal %q: %s entry must be a valid email address", item, p)
	}
	return p, val, p + ":" + val, nil
}

// ListAllowedPrincipals returns every entry in the verified-identity allowlist,
// ordered by when it was added and then by entry name.
func (s *Store) ListAllowedPrincipals(ctx context.Context) ([]AllowedPrincipal, error) {
	var out []AllowedPrincipal
	err := each(ctx, s.db,
		"SELECT `entry`, `added_by`, `added_at` FROM `allowed_principal` ORDER BY `added_at` ASC, `entry` ASC",
		nil,
		func(rows *sql.Rows) error {
			var p AllowedPrincipal
			if err := rows.Scan(&p.Entry, &p.AddedBy, &p.AddedAt); err != nil {
				return err
			}
			p.AddedAt = p.AddedAt.UTC()
			out = append(out, p)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing allowed principals: %w", err)
	}
	return out, nil
}

// AddAllowedPrincipal adds a `user:<email>` or `group:<email>` entry to the
// allowlist. Adding an entry that is already present is a no-op and returns
// the existing row with created=false.
func (s *Store) AddAllowedPrincipal(ctx context.Context, rawEntry, addedBy string, at time.Time) (AllowedPrincipal, bool, error) {
	_, _, canonical, err := NormalizeAllowedPrincipalEntry(rawEntry)
	if err != nil {
		return AllowedPrincipal{}, false, err
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	addedBy = strings.TrimSpace(addedBy)

	var result AllowedPrincipal
	var created bool
	err = s.write(ctx, "add allowed principal "+canonical, func(tx *sql.Tx) error {
		var existing AllowedPrincipal
		qErr := tx.QueryRowContext(ctx,
			"SELECT `entry`, `added_by`, `added_at` FROM `allowed_principal` WHERE `entry` = ?",
			canonical,
		).Scan(&existing.Entry, &existing.AddedBy, &existing.AddedAt)
		if qErr == nil {
			existing.AddedAt = existing.AddedAt.UTC()
			result = existing
			created = false
			return nil
		}
		if !errors.Is(qErr, sql.ErrNoRows) {
			return qErr
		}
		if _, execErr := tx.ExecContext(ctx,
			"INSERT INTO `allowed_principal` (`entry`, `added_by`, `added_at`) VALUES (?, ?, ?)",
			canonical, addedBy, at,
		); execErr != nil {
			return execErr
		}
		result = AllowedPrincipal{
			Entry:   canonical,
			AddedBy: addedBy,
			AddedAt: at,
		}
		created = true
		return nil
	})
	if err != nil {
		return AllowedPrincipal{}, false, fmt.Errorf("adding allowed principal %q: %w", canonical, err)
	}
	return result, created, nil
}

// RemoveAllowedPrincipal removes an entry from the allowlist and reports
// whether it was present.
func (s *Store) RemoveAllowedPrincipal(ctx context.Context, rawEntry string) (string, bool, error) {
	_, _, canonical, err := NormalizeAllowedPrincipalEntry(rawEntry)
	if err != nil {
		return "", false, err
	}
	var removed bool
	err = s.write(ctx, "remove allowed principal "+canonical, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx,
			"DELETE FROM `allowed_principal` WHERE `entry` = ?",
			canonical,
		)
		if execErr != nil {
			return execErr
		}
		n, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		removed = n > 0
		return nil
	})
	if err != nil {
		return "", false, fmt.Errorf("removing allowed principal %q: %w", canonical, err)
	}
	return canonical, removed, nil
}

// GetAllowedPrincipalsMigration returns the one-time startup migration record,
// or nil if no startup flag migration has been recorded.
func (s *Store) GetAllowedPrincipalsMigration(ctx context.Context) (*AllowedPrincipalsMigration, error) {
	var m AllowedPrincipalsMigration
	var droppedRaw string
	err := s.db.QueryRowContext(ctx,
		"SELECT `imported_at`, `source`, `dropped` FROM `allowed_principal_migration` WHERE `id` = 1",
	).Scan(&m.ImportedAt, &m.Source, &droppedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading allowed principals migration: %w", err)
	}
	m.ImportedAt = m.ImportedAt.UTC()
	if strings.TrimSpace(droppedRaw) != "" {
		if err := json.Unmarshal([]byte(droppedRaw), &m.Dropped); err != nil {
			return nil, fmt.Errorf("decoding dropped allowed principals: %w", err)
		}
	}
	return &m, nil
}

// ImportAllowedPrincipalsFromFlag performs the one-time startup import from
// `-ui-allowed-principals` / `$GRAIN_ALLOWED_PRINCIPALS`. If the migration has
// already run once, or if `allowed_principal` already holds entries, nothing
// is imported and didImport is false.
func (s *Store) ImportAllowedPrincipalsFromFlag(
	ctx context.Context,
	entries []string,
	dropped []string,
	source string,
	at time.Time,
) (imported []AllowedPrincipal, didImport bool, err error) {
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	source = strings.TrimSpace(source)
	if source == "" {
		source = "-ui-allowed-principals"
	}

	var canonicalList []string
	seen := make(map[string]bool, len(entries))
	for _, raw := range entries {
		_, _, c, nErr := NormalizeAllowedPrincipalEntry(raw)
		if nErr != nil {
			return nil, false, nErr
		}
		if !seen[c] {
			seen[c] = true
			canonicalList = append(canonicalList, c)
		}
	}

	droppedClean := make([]string, 0, len(dropped))
	seenDropped := make(map[string]bool, len(dropped))
	for _, d := range dropped {
		d = strings.TrimSpace(d)
		if d != "" && !seenDropped[d] {
			seenDropped[d] = true
			droppedClean = append(droppedClean, d)
		}
	}
	droppedBytes, err := json.Marshal(droppedClean)
	if err != nil {
		return nil, false, err
	}

	err = s.write(ctx, "import allowed principals from "+source, func(tx *sql.Tx) error {
		var existingID int
		qErr := tx.QueryRowContext(ctx,
			"SELECT `id` FROM `allowed_principal_migration` WHERE `id` = 1",
		).Scan(&existingID)
		if qErr == nil {
			didImport = false
			return nil
		}
		if !errors.Is(qErr, sql.ErrNoRows) {
			return qErr
		}

		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `allowed_principal`").Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			// Record that the database allowlist was already populated when
			// the deprecated flag was observed, so emptying the list later
			// still never imports the stale flag.
			_, execErr := tx.ExecContext(ctx,
				"INSERT INTO `allowed_principal_migration` (`id`, `imported_at`, `source`, `dropped`) VALUES (1, ?, ?, '[]')",
				at, "already-seeded",
			)
			didImport = false
			return execErr
		}

		for _, c := range canonicalList {
			if _, execErr := tx.ExecContext(ctx,
				"INSERT INTO `allowed_principal` (`entry`, `added_by`, `added_at`) VALUES (?, ?, ?)",
				c, source, at,
			); execErr != nil {
				return execErr
			}
			imported = append(imported, AllowedPrincipal{
				Entry:   c,
				AddedBy: source,
				AddedAt: at,
			})
		}
		if _, execErr := tx.ExecContext(ctx,
			"INSERT INTO `allowed_principal_migration` (`id`, `imported_at`, `source`, `dropped`) VALUES (1, ?, ?, ?)",
			at, source, string(droppedBytes),
		); execErr != nil {
			return execErr
		}
		didImport = true
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("importing allowed principals from %s: %w", source, err)
	}
	return imported, didImport, nil
}
