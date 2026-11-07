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

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DelegatingAppRow represents a row in `registry.delegating_apps`.
type DelegatingAppRow struct {
	Name                string        `json:"name"`
	Principal           string        `json:"principal"`
	DisplayName         string        `json:"display_name"`
	Description         string        `json:"description,omitempty"`
	Homepage            string        `json:"homepage,omitempty"`
	Owner               string        `json:"owner"`
	OwnerGroup          string        `json:"owner_group,omitempty"`
	MaxAccess           string        `json:"max_access"`
	MaxGrantTTL         time.Duration `json:"max_grant_ttl"`
	RequireSubjectToken bool          `json:"require_subject_token,omitempty"`
	RedirectURIs        []string      `json:"redirect_uris,omitempty"`
	CreatedBy           string        `json:"created_by"`
	CreatedAt           time.Time     `json:"created_at"`
	DisabledAt          *time.Time    `json:"disabled_at,omitempty"`
	DisabledBy          string        `json:"disabled_by,omitempty"`
}

// DelegationGrantRow represents a row in `registry.delegation_grants`.
type DelegationGrantRow struct {
	ID         string     `json:"id"`
	App        string     `json:"app"`
	Person     string     `json:"person"`
	Entries    []string   `json:"entries"`
	Access     string     `json:"access"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	RevokedBy  string     `json:"revoked_by,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// Disabled reports whether the delegating app has been disabled.
func (a *DelegatingAppRow) Disabled() bool {
	return a != nil && a.DisabledAt != nil
}

// CoversEntry reports whether g covers entryName (`"*"` or an exact match).
func (g *DelegationGrantRow) CoversEntry(entryName string) bool {
	if g == nil {
		return false
	}
	entryName = strings.ToLower(strings.TrimSpace(entryName))
	for _, e := range g.Entries {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "*" || (entryName != "" && e == entryName) {
			return true
		}
	}
	return false
}

// NormalizeAppPrincipal returns the canonical `serviceAccount:<email>` form
// `registry.delegating_apps.principal` is stored and matched in. A bare email
// gains the `serviceAccount:` prefix (any case of it is accepted), and the
// email is lower-cased; an empty input returns "".
func NormalizeAppPrincipal(raw string) string {
	return normalizeAppPrincipal(raw)
}

func normalizeAppPrincipal(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "serviceaccount:") {
		raw = raw[len("serviceAccount:"):]
	}
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return ""
	}
	return "serviceAccount:" + email
}

// CreateDelegatingApp registers a new delegating app in `registry.delegating_apps`.
func (s *Store) CreateDelegatingApp(ctx context.Context, app DelegatingAppRow) (*DelegatingAppRow, error) {
	name := strings.ToLower(strings.TrimSpace(app.Name))
	if name == "" {
		return nil, errors.New("delegating app name is required")
	}
	principal := normalizeAppPrincipal(app.Principal)
	if principal == "" {
		return nil, errors.New("delegating app principal is required")
	}
	owner := strings.ToLower(strings.TrimSpace(app.Owner))
	if owner == "" {
		return nil, errors.New("delegating app owner is required")
	}
	maxAccess := strings.ToLower(strings.TrimSpace(app.MaxAccess))
	if maxAccess == "" {
		maxAccess = "write"
	}
	if maxAccess != "read" && maxAccess != "write" {
		return nil, fmt.Errorf("invalid max_access %q: must be \"read\" or \"write\"", app.MaxAccess)
	}
	ttlSec := int64(app.MaxGrantTTL / time.Second)
	if ttlSec <= 0 {
		ttlSec = int64(90 * 24 * time.Hour / time.Second)
	}
	redirectJSON, err := json.Marshal(app.RedirectURIs)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if _, err := s.UpsertOwner(ctx, owner); err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO registry.delegating_apps (
    name, principal, display_name, description, homepage, owner, owner_group,
    max_access, max_grant_ttl_seconds, require_subject_token, redirect_uris,
    created_by, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13)`,
		name, principal, strings.TrimSpace(app.DisplayName), strings.TrimSpace(app.Description),
		strings.TrimSpace(app.Homepage), owner, strings.TrimSpace(app.OwnerGroup),
		maxAccess, ttlSec, app.RequireSubjectToken, string(redirectJSON),
		strings.ToLower(strings.TrimSpace(app.CreatedBy)), now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
			return nil, fmt.Errorf("%w: a delegating app named %q or with principal %q already exists", ErrConflict, name, principal)
		}
		return nil, err
	}
	_ = s.db.QueryRowContext(ctx, `SELECT pg_notify('registry_delegations', $1)`, "app:"+name).Scan()
	return s.GetDelegatingApp(ctx, name)
}

func scanDelegatingApp(scanner interface{ Scan(...any) error }) (*DelegatingAppRow, error) {
	var row DelegatingAppRow
	var ttlSec int64
	var redirectBytes []byte
	var disabledAt sql.NullTime
	if err := scanner.Scan(
		&row.Name, &row.Principal, &row.DisplayName, &row.Description, &row.Homepage,
		&row.Owner, &row.OwnerGroup, &row.MaxAccess, &ttlSec, &row.RequireSubjectToken,
		&redirectBytes, &row.CreatedBy, &row.CreatedAt, &disabledAt, &row.DisabledBy,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	row.MaxGrantTTL = time.Duration(ttlSec) * time.Second
	_ = json.Unmarshal(redirectBytes, &row.RedirectURIs)
	if disabledAt.Valid {
		t := disabledAt.Time.UTC()
		row.DisabledAt = &t
	}
	return &row, nil
}

// GetDelegatingApp fetches a delegating app by short name.
func (s *Store) GetDelegatingApp(ctx context.Context, name string) (*DelegatingAppRow, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT name, principal, display_name, description, homepage, owner, owner_group,
       max_access, max_grant_ttl_seconds, require_subject_token, redirect_uris,
       created_by, created_at, disabled_at, disabled_by
FROM registry.delegating_apps
WHERE name = $1`, strings.ToLower(strings.TrimSpace(name)))
	return scanDelegatingApp(row)
}

// GetDelegatingAppByPrincipal fetches a delegating app by its service account principal.
func (s *Store) GetDelegatingAppByPrincipal(ctx context.Context, principalOrEmail string) (*DelegatingAppRow, error) {
	principal := normalizeAppPrincipal(principalOrEmail)
	row := s.db.QueryRowContext(ctx, `
SELECT name, principal, display_name, description, homepage, owner, owner_group,
       max_access, max_grant_ttl_seconds, require_subject_token, redirect_uris,
       created_by, created_at, disabled_at, disabled_by
FROM registry.delegating_apps
WHERE LOWER(principal) = LOWER($1)`, principal)
	return scanDelegatingApp(row)
}

// ListDelegatingApps returns all registered delegating apps ordered by name.
func (s *Store) ListDelegatingApps(ctx context.Context) ([]DelegatingAppRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT name, principal, display_name, description, homepage, owner, owner_group,
       max_access, max_grant_ttl_seconds, require_subject_token, redirect_uris,
       created_by, created_at, disabled_at, disabled_by
FROM registry.delegating_apps
ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DelegatingAppRow
	for rows.Next() {
		app, err := scanDelegatingApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *app)
	}
	return out, rows.Err()
}

// SetDelegatingAppDisabled enables or disables a delegating app.
func (s *Store) SetDelegatingAppDisabled(ctx context.Context, name string, disabled bool, actorEmail string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	actorEmail = strings.ToLower(strings.TrimSpace(actorEmail))
	var res sql.Result
	var err error
	if disabled {
		res, err = s.db.ExecContext(ctx, `
UPDATE registry.delegating_apps
SET disabled_at = $2, disabled_by = $3
WHERE name = $1`, name, s.now(), actorEmail)
	} else {
		res, err = s.db.ExecContext(ctx, `
UPDATE registry.delegating_apps
SET disabled_at = NULL, disabled_by = ''
WHERE name = $1`, name)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, _ = s.db.ExecContext(ctx, `SELECT pg_notify('registry_delegations', $1)`, "app:"+name)
	return nil
}

func randomGrantID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "dlg_" + hex.EncodeToString(b[:])
}

// UpsertDelegationGrant creates or updates the live consent grant for `(app, person)`,
// keeping the same grant ID when an active grant already exists.
func (s *Store) UpsertDelegationGrant(ctx context.Context, appName, personEmail string, entries []string, access string, ttl time.Duration) (*DelegationGrantRow, error) {
	appName = strings.ToLower(strings.TrimSpace(appName))
	personEmail = strings.ToLower(strings.TrimSpace(personEmail))
	access = strings.ToLower(strings.TrimSpace(access))
	if access == "" {
		access = "write"
	}
	if access != "read" && access != "write" {
		return nil, fmt.Errorf("invalid grant access %q", access)
	}
	app, err := s.GetDelegatingApp(ctx, appName)
	if err != nil {
		return nil, err
	}
	if app.MaxAccess == "read" && access == "write" {
		return nil, fmt.Errorf("grant access %q exceeds app %q max_access %q", access, appName, app.MaxAccess)
	}
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	if app.MaxGrantTTL > 0 && ttl > app.MaxGrantTTL {
		ttl = app.MaxGrantTTL
	}
	now := s.now()
	if _, err := s.UpsertOwner(ctx, personEmail); err != nil {
		return nil, err
	}
	expiresAt := now.Add(ttl)
	entriesJSON, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}

	grantID := randomGrantID()
	var returnedID string
	err = s.db.QueryRowContext(ctx, `
INSERT INTO registry.delegation_grants (
    id, app, person, entries, access, created_at, updated_at, expires_at
) VALUES ($1, $2, $3, $4::jsonb, $5, $6, $6, $7)
ON CONFLICT (app, person) WHERE revoked_at IS NULL
DO UPDATE SET
    entries = EXCLUDED.entries,
    access = EXCLUDED.access,
    updated_at = EXCLUDED.updated_at,
    expires_at = EXCLUDED.expires_at
RETURNING id`,
		grantID, appName, personEmail, string(entriesJSON), access, now, expiresAt).Scan(&returnedID)
	if err != nil {
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx, `SELECT pg_notify('registry_delegations', $1)`, "grant:"+returnedID)
	return s.GetDelegationGrant(ctx, returnedID)
}

func scanDelegationGrant(scanner interface{ Scan(...any) error }) (*DelegationGrantRow, error) {
	var row DelegationGrantRow
	var entriesBytes []byte
	var revokedAt, lastUsedAt sql.NullTime
	if err := scanner.Scan(
		&row.ID, &row.App, &row.Person, &entriesBytes, &row.Access,
		&row.CreatedAt, &row.UpdatedAt, &row.ExpiresAt,
		&revokedAt, &row.RevokedBy, &lastUsedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal(entriesBytes, &row.Entries)
	if revokedAt.Valid {
		t := revokedAt.Time.UTC()
		row.RevokedAt = &t
	}
	if lastUsedAt.Valid {
		t := lastUsedAt.Time.UTC()
		row.LastUsedAt = &t
	}
	return &row, nil
}

// GetDelegationGrant fetches a delegation grant by ID.
func (s *Store) GetDelegationGrant(ctx context.Context, id string) (*DelegationGrantRow, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, app, person, entries, access, created_at, updated_at, expires_at,
       revoked_at, revoked_by, last_used_at
FROM registry.delegation_grants
WHERE id = $1`, strings.TrimSpace(id))
	return scanDelegationGrant(row)
}

// GetLiveDelegationGrant fetches the active (unrevoked) grant for `(appName, personEmail)`.
func (s *Store) GetLiveDelegationGrant(ctx context.Context, appName, personEmail string) (*DelegationGrantRow, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, app, person, entries, access, created_at, updated_at, expires_at,
       revoked_at, revoked_by, last_used_at
FROM registry.delegation_grants
WHERE app = $1 AND LOWER(person) = LOWER($2) AND revoked_at IS NULL`,
		strings.ToLower(strings.TrimSpace(appName)), strings.ToLower(strings.TrimSpace(personEmail)))
	return scanDelegationGrant(row)
}

// ListDelegationGrantsForPerson returns personEmail's delegation grants, newest
// first. Revoked grants are included only when includeRevoked is set.
func (s *Store) ListDelegationGrantsForPerson(ctx context.Context, personEmail string, includeRevoked bool) ([]DelegationGrantRow, error) {
	q := `
SELECT id, app, person, entries, access, created_at, updated_at, expires_at,
       revoked_at, revoked_by, last_used_at
FROM registry.delegation_grants
WHERE LOWER(person) = LOWER($1)`
	if !includeRevoked {
		q += ` AND revoked_at IS NULL`
	}
	q += ` ORDER BY created_at DESC, id ASC`
	rows, err := s.db.QueryContext(ctx, q, strings.ToLower(strings.TrimSpace(personEmail)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DelegationGrantRow
	for rows.Next() {
		g, err := scanDelegationGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// RevokeDelegationGrant marks a delegation grant revoked and emits `NOTIFY registry_delegations`.
func (s *Store) RevokeDelegationGrant(ctx context.Context, id, revokedBy string) error {
	id = strings.TrimSpace(id)
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.delegation_grants
SET revoked_at = $2, revoked_by = $3
WHERE id = $1 AND revoked_at IS NULL`, id, s.now(), strings.ToLower(strings.TrimSpace(revokedBy)))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, _ = s.db.ExecContext(ctx, `SELECT pg_notify('registry_delegations', $1)`, "grant:"+id)
	return nil
}

// RecordDelegationUsage increments the daily read or write counter in `registry.delegation_usage`
// and updates `last_used_at` on `registry.delegation_grants`.
func (s *Store) RecordDelegationUsage(ctx context.Context, grantID, entryName string, isWrite bool) error {
	now := s.now().UTC()
	day := now.Format("2006-01-02")
	var reads, writes int64
	if isWrite {
		writes = 1
	} else {
		reads = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO registry.delegation_usage (grant_id, name, day, reads, writes, last_at)
VALUES ($1, $2, $3::date, $4, $5, $6)
ON CONFLICT (grant_id, name, day) DO UPDATE SET
    reads = registry.delegation_usage.reads + EXCLUDED.reads,
    writes = registry.delegation_usage.writes + EXCLUDED.writes,
    last_at = EXCLUDED.last_at`,
		grantID, entryName, day, reads, writes, now)
	if err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE registry.delegation_grants SET last_used_at = $2 WHERE id = $1`, grantID, now)
	return nil
}
