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
	"regexp"
	"strings"
	"sync"
	"time"
)

// AllowedPrincipal is one user, group, service-account, or delegate entry in
// the deployment's verified-identity sign-in allowlist (`allowed_principal`).
type AllowedPrincipal struct {
	// Entry is canonicalized as `user:<email>`, `group:<email>`,
	// `serviceAccount:<email>` or `delegate:<email>` with the email lowercased.
	Entry   string    `json:"entry"`
	AddedBy string    `json:"addedBy"`
	AddedAt time.Time `json:"addedAt"`
	// Reason is why the entry was added, as its adder gave it. Required
	// for serviceAccount: and delegate: entries.
	Reason string `json:"reason,omitempty"`
	// Role is the API scope set the entry's sessions are held to
	// (docs/design/access-rbac.md), DefaultAccessRole unless set (or
	// DefaultDelegateAccessRole for delegate: entries).
	Role string `json:"role"`
	// Label is an optional short display label ([a-z][a-z0-9-]{0,31}) for a
	// delegate: entry, shown in "(via <label>)" attribution when present.
	Label string `json:"label,omitempty"`
	// LastUsedAt is updated when a delegated request carries act.sub matching
	// a delegate: entry.
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// DefaultAccessRole is the role an allowlist entry has unless given
// another: every scope, which is what every entry had before roles.
const DefaultAccessRole = APIScopeSetBuiltinPrefix + "operator"

// DefaultDelegateAccessRole is the default role for a `delegate:` entry when
// no explicit role is provided (design/design/authentication.design.md §6).
const DefaultDelegateAccessRole = APIScopeSetBuiltinPrefix + "delegated"

// ServiceAccountDomainSuffix is the domain every Google service account
// address ends in; only such addresses may be serviceAccount: or delegate: entries.
const ServiceAccountDomainSuffix = ".gserviceaccount.com"

var delegateLabelPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidateDelegateLabel checks that label is either empty or matches
// [a-z][a-z0-9-]{0,31}.
func ValidateDelegateLabel(label string) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil
	}
	if !delegateLabelPattern.MatchString(label) {
		return fmt.Errorf("delegate label %q must match [a-z][a-z0-9-]{0,31}", label)
	}
	return nil
}

// ValidateDelegateRole checks that role may be assigned to a `delegate:`
// allowlist entry (AU-23.3): it must exist, must not be grain/operator, and
// must not hold any dangerous scope, access:write, or a requires_person flag.
func ValidateDelegateRole(role string) error {
	role = strings.TrimSpace(role)
	if role == "" {
		return errors.New("delegate role is required")
	}
	if role == DefaultAccessRole {
		return fmt.Errorf("role %q cannot be assigned to a delegate: entry: delegating services may never hold grain/operator", role)
	}
	resolved, ok := APIScopeSetNamed(role)
	if !ok {
		return fmt.Errorf("role %q is no scope set in effect", role)
	}
	if resolved.RequiresPerson || resolved.Person {
		return fmt.Errorf("role %q requires a person or holds a dangerous scope and cannot be assigned to a delegate: entry", role)
	}
	for _, sc := range resolved.Resolved {
		if sc == "access:write" || IsDangerousAPIScope(sc) {
			return fmt.Errorf("role %q grants %s, which cannot be assigned to a delegate: entry", role, sc)
		}
	}
	return nil
}

var (
	delegateLabelsMu sync.RWMutex
	delegateLabels   = map[string]string{}
)

// RegisterDelegateLabel records or clears the display label for a delegate
// service-account email so PrincipalLabel can render "(via <label>)".
func RegisterDelegateLabel(email, label string) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return
	}
	label = strings.TrimSpace(label)
	delegateLabelsMu.Lock()
	if label == "" {
		delete(delegateLabels, email)
	} else {
		delegateLabels[email] = label
	}
	delegateLabelsMu.Unlock()
}

// DelegateLabelForEmail returns the registered label for email if non-empty,
// and otherwise the local part of email before '@'.
func DelegateLabelForEmail(email string) string {
	norm := strings.ToLower(strings.TrimSpace(email))
	delegateLabelsMu.RLock()
	lbl := delegateLabels[norm]
	delegateLabelsMu.RUnlock()
	if lbl != "" {
		return lbl
	}
	local, _, hasAt := strings.Cut(norm, "@")
	if hasAt && local != "" {
		return local
	}
	return strings.TrimSpace(email)
}

// AllowedPrincipalsMigration records the one-time startup import off
// `-ui-allowed-principals` / `$GRAIN_ALLOWED_PRINCIPALS`.
type AllowedPrincipalsMigration struct {
	ImportedAt time.Time `json:"importedAt"`
	Source     string    `json:"source"`
	Dropped    []string  `json:"dropped,omitempty"`
}

// NormalizeAllowedPrincipalEntry validates and canonicalizes a `user:<email>`,
// `group:<email>`, `serviceAccount:<email>`, or `delegate:<email>` allowlist
// entry. `serviceAccount:` and `delegate:` entries must name a
// *.gserviceaccount.com address, and `user:` and `group:` entries must not;
// `domain:` entries are refused.
func NormalizeAllowedPrincipalEntry(raw string) (prefix, email, canonical string, err error) {
	item := strings.TrimSpace(raw)
	if item == "" {
		return "", "", "", fmt.Errorf("allowed principal entry is required (expected user:<email>, group:<email>, serviceAccount:<email> or delegate:<email>)")
	}
	p, val, ok := strings.Cut(item, ":")
	if !ok {
		return "", "", "", fmt.Errorf("invalid allowed principal %q: must start with user:, group:, serviceAccount: or delegate:", item)
	}
	p = strings.TrimSpace(p)
	val = strings.ToLower(strings.TrimSpace(val))
	switch p {
	case "user", "group", "serviceAccount", "delegate":
	case "domain":
		return "", "", "", fmt.Errorf("invalid allowed principal %q: domain: entries are not supported; use user:<email>, group:<email>, serviceAccount:<email> or delegate:<email>", item)
	default:
		return "", "", "", fmt.Errorf("invalid allowed principal %q: unknown prefix %q (expected user:, group:, serviceAccount: or delegate:)", item, p)
	}
	if val == "" {
		return "", "", "", fmt.Errorf("invalid allowed principal %q: empty %s email address", item, p)
	}
	local, domain, hasAt := strings.Cut(val, "@")
	if !hasAt || local == "" || domain == "" || strings.Contains(domain, "@") ||
		strings.ContainsAny(val, " \t\r\n/,;'\"<>") {
		return "", "", "", fmt.Errorf("invalid allowed principal %q: %s entry must be a valid email address", item, p)
	}
	isSA := strings.HasSuffix(domain, ServiceAccountDomainSuffix) && len(domain) > len(ServiceAccountDomainSuffix)
	if (p == "serviceAccount" || p == "delegate") && !isSA {
		return "", "", "", fmt.Errorf("invalid allowed principal %q: %s: entries must name a service account address (*%s)", item, p, ServiceAccountDomainSuffix)
	}
	if p != "serviceAccount" && p != "delegate" && isSA {
		return "", "", "", fmt.Errorf("invalid allowed principal %q: service account addresses (*%s) must be added as serviceAccount:<email> or delegate:<email>", item, ServiceAccountDomainSuffix)
	}
	return p, val, p + ":" + val, nil
}

// ListAllowedPrincipals returns every entry in the verified-identity allowlist,
// ordered by when it was added and then by entry name.
func (s *Store) ListAllowedPrincipals(ctx context.Context) ([]AllowedPrincipal, error) {
	var out []AllowedPrincipal
	err := each(ctx, s.db,
		"SELECT `entry`, `added_by`, `added_at`, `reason`, `role`, `label`, `last_used_at` FROM `allowed_principal` ORDER BY `added_at` ASC, `entry` ASC",
		nil,
		func(rows *sql.Rows) error {
			var p AllowedPrincipal
			var lastUsed sql.NullTime
			if err := rows.Scan(&p.Entry, &p.AddedBy, &p.AddedAt, &p.Reason, &p.Role, &p.Label, &lastUsed); err != nil {
				return err
			}
			p.AddedAt = p.AddedAt.UTC()
			if lastUsed.Valid {
				lu := lastUsed.Time.UTC()
				p.LastUsedAt = &lu
			}
			if email, ok := strings.CutPrefix(p.Entry, "delegate:"); ok {
				RegisterDelegateLabel(email, p.Label)
			}
			out = append(out, p)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing allowed principals: %w", err)
	}
	return out, nil
}

// AddAllowedPrincipal adds a `user:<email>`, `group:<email>`,
// `serviceAccount:<email>`, or `delegate:<email>` entry to the allowlist,
// recording who added it and why. `serviceAccount:` and `delegate:` entries
// need a reason. Adding an entry that is already present is a no-op and returns
// the existing row with created=false.
func (s *Store) AddAllowedPrincipal(ctx context.Context, rawEntry, addedBy, reason string, at time.Time) (AllowedPrincipal, bool, error) {
	return s.AddAllowedPrincipalWithRole(ctx, rawEntry, addedBy, reason, "", at)
}

// AddAllowedPrincipalWithRole is AddAllowedPrincipal giving the entry role
// ("" for DefaultAccessRole, or DefaultDelegateAccessRole on delegate: entries).
// An entry already present keeps its own role.
func (s *Store) AddAllowedPrincipalWithRole(ctx context.Context, rawEntry, addedBy, reason, role string, at time.Time) (AllowedPrincipal, bool, error) {
	return s.AddAllowedPrincipalWithOptions(ctx, rawEntry, addedBy, reason, role, "", at)
}

// AddAllowedPrincipalWithOptions adds an allowlist entry with optional role and
// delegate label.
func (s *Store) AddAllowedPrincipalWithOptions(ctx context.Context, rawEntry, addedBy, reason, role, label string, at time.Time) (AllowedPrincipal, bool, error) {
	prefix, email, canonical, err := NormalizeAllowedPrincipalEntry(rawEntry)
	if err != nil {
		return AllowedPrincipal{}, false, err
	}
	reason = strings.TrimSpace(reason)
	if (prefix == "serviceAccount" || prefix == "delegate") && reason == "" {
		return AllowedPrincipal{}, false, fmt.Errorf("allowed principal %q: a reason is required to admit a service account", canonical)
	}
	label = strings.TrimSpace(label)
	if prefix != "delegate" && label != "" {
		return AllowedPrincipal{}, false, fmt.Errorf("allowed principal %q: label is only supported on delegate: entries", canonical)
	}
	if err := ValidateDelegateLabel(label); err != nil {
		return AllowedPrincipal{}, false, err
	}
	if role = strings.TrimSpace(role); role == "" {
		if prefix == "delegate" {
			role = DefaultDelegateAccessRole
		} else {
			role = DefaultAccessRole
		}
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
		var lastUsed sql.NullTime
		qErr := tx.QueryRowContext(ctx,
			"SELECT `entry`, `added_by`, `added_at`, `reason`, `role`, `label`, `last_used_at` FROM `allowed_principal` WHERE `entry` = ?",
			canonical,
		).Scan(&existing.Entry, &existing.AddedBy, &existing.AddedAt, &existing.Reason, &existing.Role, &existing.Label, &lastUsed)
		if qErr == nil {
			existing.AddedAt = existing.AddedAt.UTC()
			if lastUsed.Valid {
				lu := lastUsed.Time.UTC()
				existing.LastUsedAt = &lu
			}
			result = existing
			created = false
			return nil
		}
		if !errors.Is(qErr, sql.ErrNoRows) {
			return qErr
		}
		if _, execErr := tx.ExecContext(ctx,
			"INSERT INTO `allowed_principal` (`entry`, `added_by`, `added_at`, `reason`, `role`, `label`) VALUES (?, ?, ?, ?, ?, ?)",
			canonical, addedBy, at, reason, role, label,
		); execErr != nil {
			return execErr
		}
		result = AllowedPrincipal{
			Entry:   canonical,
			AddedBy: addedBy,
			AddedAt: at,
			Reason:  reason,
			Role:    role,
			Label:   label,
		}
		created = true
		return nil
	})
	if err != nil {
		return AllowedPrincipal{}, false, fmt.Errorf("adding allowed principal %q: %w", canonical, err)
	}
	if prefix == "delegate" {
		RegisterDelegateLabel(email, result.Label)
	}
	return result, created, nil
}

// TouchAllowedPrincipalLastUsed updates last_used_at on an allowlist entry.
func (s *Store) TouchAllowedPrincipalLastUsed(ctx context.Context, rawEntry string, at time.Time) error {
	_, _, canonical, err := NormalizeAllowedPrincipalEntry(rawEntry)
	if err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	return s.write(ctx, "touch allowed principal "+canonical, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx,
			"UPDATE `allowed_principal` SET `last_used_at` = ? WHERE `entry` = ?",
			at, canonical,
		)
		return execErr
	})
}

// SetAllowedPrincipalRole gives an existing entry role and returns the
// role it had before. found is false for an entry not on the list.
func (s *Store) SetAllowedPrincipalRole(ctx context.Context, rawEntry, role string) (canonical, previous string, found bool, err error) {
	_, _, canonical, err = NormalizeAllowedPrincipalEntry(rawEntry)
	if err != nil {
		return "", "", false, err
	}
	if role = strings.TrimSpace(role); role == "" {
		return "", "", false, fmt.Errorf("allowed principal %q: a role is required", canonical)
	}
	err = s.write(ctx, "set role of allowed principal "+canonical, func(tx *sql.Tx) error {
		qErr := tx.QueryRowContext(ctx,
			"SELECT `role` FROM `allowed_principal` WHERE `entry` = ?", canonical,
		).Scan(&previous)
		if errors.Is(qErr, sql.ErrNoRows) {
			return nil
		}
		if qErr != nil {
			return qErr
		}
		found = true
		_, execErr := tx.ExecContext(ctx,
			"UPDATE `allowed_principal` SET `role` = ? WHERE `entry` = ?", role, canonical)
		return execErr
	})
	if err != nil {
		return "", "", false, fmt.Errorf("setting role of allowed principal %q: %w", canonical, err)
	}
	return canonical, previous, found, nil
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
	if removed {
		if email, ok := strings.CutPrefix(canonical, "delegate:"); ok {
			RegisterDelegateLabel(email, "")
		}
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
// is imported and didImport is false. serviceAccount: entries are never
// imported: an operator adds each one deliberately, with a reason.
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
		prefix, _, c, nErr := NormalizeAllowedPrincipalEntry(raw)
		if nErr != nil {
			return nil, false, nErr
		}
		if prefix == "serviceAccount" || prefix == "delegate" {
			return nil, false, fmt.Errorf("allowed principal %q: %s: entries are not imported from %s", c, prefix, source)
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
