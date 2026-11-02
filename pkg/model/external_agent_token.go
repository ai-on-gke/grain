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
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// ExternalAgentTokenPrefix prefixes every minted external-agent bearer
	// token, so it is distinguishable in logs and never mistaken for an
	// access code.
	ExternalAgentTokenPrefix = "gma_"

	// ExternalAgentPrincipalPrefix is the prefix before the credential's
	// name in Principal.ID ("external:<name>").
	ExternalAgentPrincipalPrefix = "external:"

	// DefaultExternalAgentRateLimit is the default per-credential request
	// rate limit (requests per minute) when ExternalAgentToken.RateLimit is 0.
	DefaultExternalAgentRateLimit = 60

	// DefaultExternalAgentMaxConcurrent is the default per-credential cap
	// on in-flight MCP/API calls when ExternalAgentToken.MaxConcurrent is 0.
	DefaultExternalAgentMaxConcurrent = 8

	// MaxExternalAgentNameLen bounds a credential's name so external:<name>
	// stays readable in journal lines and task conversation attribution.
	MaxExternalAgentNameLen = 64
)

// ErrExternalAgentTokenRevoked is returned when rotating or using a token
// that has already been revoked.
var ErrExternalAgentTokenRevoked = errors.New("external agent credential is revoked")

// ExternalAgentToken is a scoped bearer credential issued to an external
// agent (docs/agent-api.md, "External agents"). Only
// the SHA-256 hash of the secret is stored; the raw token is returned once
// at issue or rotation and never shown again.
type ExternalAgentToken struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	TokenHash     string     `json:"-"`
	Prefix        string     `json:"prefix"`
	ScopeSet      string     `json:"scopeSet,omitempty"`
	Scopes        []string   `json:"scopes,omitempty"`
	Repos         []string   `json:"repos,omitempty"`
	RateLimit     int        `json:"rateLimit,omitempty"`
	MaxConcurrent int        `json:"maxConcurrent,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt    *time.Time `json:"lastUsedAt,omitempty"`
	CreatedBy     string     `json:"createdBy,omitempty"`
}

// PrincipalID returns the external principal identifier ("external:<name>")
// recorded in the journal and in affected tasks' conversations.
func (t ExternalAgentToken) PrincipalID() string {
	return ExternalAgentPrincipalPrefix + t.Name
}

// Principal returns the model.Principal for this external agent credential.
func (t ExternalAgentToken) Principal() Principal {
	return Principal{Kind: PrincipalAgent, ID: t.PrincipalID()}
}

// Active reports whether the credential is neither revoked nor expired at now.
func (t ExternalAgentToken) Active(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	if t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
		return false
	}
	return true
}

// AllowsRepo reports whether repo is permitted by this credential's repository
// restriction (empty Repos permits all repositories).
func (t ExternalAgentToken) AllowsRepo(repo string) bool {
	if len(t.Repos) == 0 {
		return true
	}
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	return containsFold(t.Repos, repo)
}

// EffectiveRateLimit returns the per-minute request limit for this credential.
func (t ExternalAgentToken) EffectiveRateLimit() int {
	if t.RateLimit > 0 {
		return t.RateLimit
	}
	return DefaultExternalAgentRateLimit
}

// EffectiveMaxConcurrent returns the maximum concurrent in-flight requests
// for this credential.
func (t ExternalAgentToken) EffectiveMaxConcurrent() int {
	if t.MaxConcurrent > 0 {
		return t.MaxConcurrent
	}
	return DefaultExternalAgentMaxConcurrent
}

// ResolveGrants resolves the credential's ScopeSet, Scopes, and Repos into
// canonical api:... grant strings, the resulting APIGrantSet, and whether
// any granted scope or scope set requires a person to issue.
func (t ExternalAgentToken) ResolveGrants() ([]string, APIGrantSet, bool, error) {
	return ResolveExternalAgentGrants(t.ScopeSet, t.Scopes, t.Repos)
}

// ValidateExternalAgentName checks and trims a credential name. Names use
// letters, digits, '.', '_' or '-', starting with a letter or digit, so
// external:<name> is unambiguous in logs and task conversations.
func ValidateExternalAgentName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("name is required")
	}
	if len(name) > MaxExternalAgentNameLen {
		return "", fmt.Errorf("name exceeds %d characters", MaxExternalAgentNameLen)
	}
	for i, r := range name {
		alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if i == 0 && !alnum {
			return "", fmt.Errorf("name %q must start with a letter or digit", name)
		}
		if !alnum && r != '-' && r != '_' && r != '.' {
			return "", fmt.Errorf("name %q may contain only letters, digits, '-', '_' and '.'", name)
		}
	}
	return name, nil
}

// NormalizeExternalAgentSpec validates and normalizes a credential's
// scopeSet, scopes, and repos, returning the canonical values, resolved
// grant strings, grant set, and whether issuing requires a person.
func NormalizeExternalAgentSpec(scopeSet string, scopes, repos []string) (cleanSet string, cleanScopes, cleanRepos, grants []string, set APIGrantSet, dangerous bool, err error) {
	for _, r := range repos {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		ref, perr := ParseRepo(r)
		if perr != nil {
			return "", nil, nil, nil, APIGrantSet{}, false, fmt.Errorf("repos: %w", perr)
		}
		canon := ref.String()
		if !containsFold(cleanRepos, canon) {
			cleanRepos = append(cleanRepos, canon)
		}
	}

	cleanSet = strings.TrimSpace(scopeSet)
	cleanSet, _ = strings.CutPrefix(cleanSet, APIGrantPrefix)
	if cleanSet != "" {
		if p, ok := APIPresetNamed(cleanSet); ok {
			cleanSet = p.Name
		} else if s, ok := APIScopeSetNamed(cleanSet); ok {
			cleanSet = s.Name
		} else {
			return "", nil, nil, nil, APIGrantSet{}, false, fmt.Errorf("scopeSet %q is neither a built-in preset nor a declared API scope set", scopeSet)
		}
	}

	for _, raw := range scopes {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		s, _ = strings.CutPrefix(s, APIGrantPrefix)
		if !slices.Contains(cleanScopes, s) {
			cleanScopes = append(cleanScopes, s)
		}
	}

	grants, set, dangerous, err = ResolveExternalAgentGrants(cleanSet, cleanScopes, cleanRepos)
	if err != nil {
		return "", nil, nil, nil, APIGrantSet{}, false, err
	}
	return cleanSet, cleanScopes, cleanRepos, grants, set, dangerous, nil
}

// ResolveExternalAgentGrants expands a credential's scopeSet, scopes, and
// repos into api:... grant strings and an APIGrantSet.
func ResolveExternalAgentGrants(scopeSet string, scopes, repos []string) ([]string, APIGrantSet, bool, error) {
	var cleanRepos []string
	for _, r := range repos {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		ref, err := ParseRepo(r)
		if err != nil {
			return nil, APIGrantSet{}, false, fmt.Errorf("repos: %w", err)
		}
		canon := ref.String()
		if !containsFold(cleanRepos, canon) {
			cleanRepos = append(cleanRepos, canon)
		}
	}

	var grants []string
	var dangerous bool
	addTarget := func(target, explicitRepo string) error {
		if explicitRepo != "" {
			g := APIGrantPrefix + target + "@" + explicitRepo
			parsed, ok, err := ParseAPIGrant(g)
			if !ok || err != nil {
				return err
			}
			dangerous = dangerous || parsed.Dangerous()
			if !slices.Contains(grants, g) {
				grants = append(grants, g)
			}
			return nil
		}
		if len(cleanRepos) == 0 {
			g := APIGrantPrefix + target
			parsed, ok, err := ParseAPIGrant(g)
			if !ok || err != nil {
				return err
			}
			dangerous = dangerous || parsed.Dangerous()
			if !slices.Contains(grants, g) {
				grants = append(grants, g)
			}
			return nil
		}
		for _, repo := range cleanRepos {
			g := APIGrantPrefix + target + "@" + repo
			parsed, ok, err := ParseAPIGrant(g)
			if !ok || err != nil {
				return err
			}
			dangerous = dangerous || parsed.Dangerous()
			if !slices.Contains(grants, g) {
				grants = append(grants, g)
			}
		}
		return nil
	}

	setTrim := strings.TrimSpace(scopeSet)
	setTrim, _ = strings.CutPrefix(setTrim, APIGrantPrefix)
	if setTrim != "" {
		if _, ok := APIPresetNamed(setTrim); !ok {
			if _, ok := APIScopeSetNamed(setTrim); !ok {
				return nil, APIGrantSet{}, false, fmt.Errorf("scopeSet %q is neither a built-in preset nor a declared API scope set", scopeSet)
			}
		}
		if err := addTarget(setTrim, ""); err != nil {
			return nil, APIGrantSet{}, false, err
		}
	}

	for _, raw := range scopes {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		s, _ = strings.CutPrefix(s, APIGrantPrefix)
		what, explicitRepo, hasRepo := strings.Cut(s, "@")
		if hasRepo {
			ref, err := ParseRepo(explicitRepo)
			if err != nil {
				return nil, APIGrantSet{}, false, fmt.Errorf("scope %q: %w", raw, err)
			}
			explicitRepo = ref.String()
			if len(cleanRepos) > 0 && !containsFold(cleanRepos, explicitRepo) {
				return nil, APIGrantSet{}, false, fmt.Errorf("scope %q names repository %s outside the credential's repo restriction (%s)", raw, explicitRepo, strings.Join(cleanRepos, ", "))
			}
		}
		if _, ok := APIPresetNamed(what); ok {
			if err := addTarget(what, explicitRepo); err != nil {
				return nil, APIGrantSet{}, false, err
			}
			continue
		}
		if _, ok := APIScopeSetNamed(what); ok {
			if err := addTarget(what, explicitRepo); err != nil {
				return nil, APIGrantSet{}, false, err
			}
			continue
		}
		expanded, err := ExpandAPIScopePattern(what)
		if err != nil {
			// Fall back to ParseAPIGrant so internal/unknown scopes get
			// the standard error phrasing.
			if _, _, perr := ParseAPIGrant(APIGrantPrefix + s); perr != nil {
				return nil, APIGrantSet{}, false, perr
			}
			return nil, APIGrantSet{}, false, err
		}
		for _, sc := range expanded {
			if err := addTarget(string(sc), explicitRepo); err != nil {
				return nil, APIGrantSet{}, false, err
			}
		}
	}

	if len(grants) == 0 {
		return nil, APIGrantSet{}, false, errors.New("credential must specify a scopeSet or at least one scope")
	}
	set := APIGrantsOf(grants)
	if set.Empty() {
		return nil, APIGrantSet{}, false, errors.New("credential grants no scopes on the selected repositories")
	}
	return grants, set, dangerous, nil
}

// GenerateExternalAgentToken generates a fresh random gma_ bearer token,
// its SHA-256 hex digest, and its display prefix.
func GenerateExternalAgentToken() (raw, hash, prefix string, err error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", "", fmt.Errorf("generating external agent token: %w", err)
	}
	raw = ExternalAgentTokenPrefix + hex.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	prefix = raw[:8]
	return raw, hash, prefix, nil
}

// HashExternalAgentToken returns the hex-encoded SHA-256 hash of a raw token.
func HashExternalAgentToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// IsExternalAgentToken reports whether raw has the external-agent bearer
// token prefix ("gma_").
func IsExternalAgentToken(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), ExternalAgentTokenPrefix)
}

const externalAgentTokenColumns = "`id`, `name`, `token_hash`, `prefix`, `scope_set`, `scopes`, `repos`, `rate_limit`, `max_concurrent`, `created_at`, `expires_at`, `revoked_at`, `last_used_at`, `created_by`"

func scanExternalAgentToken(scan func(dest ...any) error) (ExternalAgentToken, error) {
	var t ExternalAgentToken
	var scopes, repos string
	var expiresAt, revokedAt, lastUsedAt sql.NullTime
	if err := scan(
		&t.ID, &t.Name, &t.TokenHash, &t.Prefix,
		&t.ScopeSet, &scopes, &repos,
		&t.RateLimit, &t.MaxConcurrent,
		&t.CreatedAt, &expiresAt, &revokedAt, &lastUsedAt, &t.CreatedBy,
	); err != nil {
		return ExternalAgentToken{}, err
	}
	t.Scopes = splitCSV(scopes)
	t.Repos = splitCSV(repos)
	if expiresAt.Valid {
		v := expiresAt.Time.UTC()
		t.ExpiresAt = &v
	}
	if revokedAt.Valid {
		v := revokedAt.Time.UTC()
		t.RevokedAt = &v
	}
	if lastUsedAt.Valid {
		v := lastUsedAt.Time.UTC()
		t.LastUsedAt = &v
	}
	t.CreatedAt = t.CreatedAt.UTC()
	return t, nil
}

// CreateExternalAgentToken stores a new ExternalAgentToken, allocating an ID
// if t.ID is empty.
func (s *Store) CreateExternalAgentToken(ctx context.Context, t ExternalAgentToken) (ExternalAgentToken, error) {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	} else {
		t.CreatedAt = t.CreatedAt.UTC()
	}
	err := s.write(ctx, "create external agent token "+t.Name, func(tx *sql.Tx) error {
		if t.ID == "" {
			res, err := tx.ExecContext(ctx, "INSERT INTO `external_agent_token_sequence` (`issued_at`) VALUES (?)", time.Now().UTC())
			if err != nil {
				return fmt.Errorf("allocating external agent token id: %w", err)
			}
			seq, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("reading external agent token id: %w", err)
			}
			t.ID = strconv.FormatInt(seq, 10)
		}
		var expiresAt, revokedAt, lastUsedAt sql.NullTime
		if t.ExpiresAt != nil {
			expiresAt = sql.NullTime{Time: t.ExpiresAt.UTC(), Valid: true}
		}
		if t.RevokedAt != nil {
			revokedAt = sql.NullTime{Time: t.RevokedAt.UTC(), Valid: true}
		}
		if t.LastUsedAt != nil {
			lastUsedAt = sql.NullTime{Time: t.LastUsedAt.UTC(), Valid: true}
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `external_agent_token` ("+externalAgentTokenColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			t.ID, t.Name, t.TokenHash, t.Prefix,
			t.ScopeSet, joinCSV(t.Scopes), joinCSV(t.Repos),
			t.RateLimit, t.MaxConcurrent,
			t.CreatedAt, expiresAt, revokedAt, lastUsedAt, t.CreatedBy,
		)
		return err
	})
	return t, err
}

// ListExternalAgentTokens returns all external agent tokens ordered by
// creation time descending.
func (s *Store) ListExternalAgentTokens(ctx context.Context) ([]ExternalAgentToken, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+externalAgentTokenColumns+" FROM `external_agent_token` ORDER BY `created_at` DESC, `id` DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExternalAgentToken
	for rows.Next() {
		t, err := scanExternalAgentToken(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetExternalAgentToken looks up an external agent token by ID. Returns nil,
// nil when not found.
func (s *Store) GetExternalAgentToken(ctx context.Context, id string) (*ExternalAgentToken, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+externalAgentTokenColumns+" FROM `external_agent_token` WHERE `id` = ?", id)
	t, err := scanExternalAgentToken(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetExternalAgentTokenByHash looks up an external agent token by its
// SHA-256 token hash. Returns nil, nil when not found.
func (s *Store) GetExternalAgentTokenByHash(ctx context.Context, hash string) (*ExternalAgentToken, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+externalAgentTokenColumns+" FROM `external_agent_token` WHERE `token_hash` = ?", hash)
	t, err := scanExternalAgentToken(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// RotateExternalAgentToken replaces a non-revoked credential's secret hash
// and prefix in place, immediately invalidating the previous secret.
func (s *Store) RotateExternalAgentToken(ctx context.Context, id, newHash, newPrefix string) (*ExternalAgentToken, error) {
	var updated *ExternalAgentToken
	err := s.write(ctx, "rotate external agent token "+id, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx,
			"SELECT "+externalAgentTokenColumns+" FROM `external_agent_token` WHERE `id` = ?", id)
		t, err := scanExternalAgentToken(row.Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		if err != nil {
			return err
		}
		if t.RevokedAt != nil {
			return ErrExternalAgentTokenRevoked
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `external_agent_token` SET `token_hash` = ?, `prefix` = ? WHERE `id` = ?",
			newHash, newPrefix, id,
		); err != nil {
			return err
		}
		t.TokenHash = newHash
		t.Prefix = newPrefix
		updated = &t
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// RevokeExternalAgentToken marks a credential revoked.
func (s *Store) RevokeExternalAgentToken(ctx context.Context, id string) error {
	now := time.Now().UTC()
	return s.write(ctx, "revoke external agent token "+id, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"UPDATE `external_agent_token` SET `revoked_at` = ? WHERE `id` = ?",
			now, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// DeleteExternalAgentToken deletes a credential record from the store.
func (s *Store) DeleteExternalAgentToken(ctx context.Context, id string) error {
	return s.write(ctx, "delete external agent token "+id, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"DELETE FROM `external_agent_token` WHERE `id` = ?", id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// RevokeAllExternalAgentTokens is the kill switch: it marks every currently
// non-revoked external agent credential as revoked and returns the count of
// credentials newly revoked.
func (s *Store) RevokeAllExternalAgentTokens(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	var count int64
	err := s.write(ctx, "revoke all external agent tokens", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"UPDATE `external_agent_token` SET `revoked_at` = ? WHERE `revoked_at` IS NULL",
			now)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		count = n
		return nil
	})
	return count, err
}

// TouchExternalAgentTokenUsed records when an external agent token was last
// used to authenticate a call.
func (s *Store) TouchExternalAgentTokenUsed(ctx context.Context, id string, usedAt time.Time) error {
	return s.write(ctx, "touch external agent token "+id, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `external_agent_token` SET `last_used_at` = ? WHERE `id` = ?",
			usedAt.UTC(), id)
		return err
	})
}
