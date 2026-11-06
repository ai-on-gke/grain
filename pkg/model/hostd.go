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

// The hostd registry: every grain hostd this daemon takes host work
// through, by name. The co-located hostd at /run/grain-hostd/hostd.sock
// is LocalHostd; any number of others dial in to /api/hostd/connect,
// each with a token of its own (hostd_token) that names it. A
// connection's identity is the hostd its token belongs to and nothing
// else -- what the hostd says about itself only has to agree.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LocalHostd is the registry's name for the hostd co-located with the
// daemon, reached at its socket rather than dialing in. It has no token.
const LocalHostd = "local"

// HostdTokenPrefix starts every minted hostd token, so one pasted into
// the wrong place is recognisable.
const HostdTokenPrefix = "ghd_"

var (
	// ErrHostdNotFound is a registry operation on a name it does not hold.
	ErrHostdNotFound = errors.New("model: hostd not found")
	// ErrHostdRevoked is a token minted for a hostd that is revoked.
	ErrHostdRevoked = errors.New("model: hostd is revoked")
	// ErrHostdExists is CreateHostd on a name already taken.
	ErrHostdExists = errors.New("model: a hostd by that name already exists")
	// ErrHostdLive is DeleteHostd on a hostd that is not revoked: a live
	// hostd is revoked first, so deleting one never cuts off a connected
	// hostd or its running work by surprise.
	ErrHostdLive = errors.New("model: hostd is not revoked; revoke it first")
)

// hostdNameRE is what a hostd name may be: a DNS label, so it reads the
// same in a log line, a URL path and a notification subject.
var hostdNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidateHostdName refuses a name a hostd may not have.
func ValidateHostdName(name string) error {
	if !hostdNameRE.MatchString(name) {
		return fmt.Errorf("hostd name %q must be a lowercase DNS label: letters, digits and '-', at most 63", name)
	}
	return nil
}

// NormalizeHostdOwner is owner as the registry stores it: a full email
// address or service account, lowercased, or "" for none.
func NormalizeHostdOwner(owner string) (string, error) {
	owner = strings.ToLower(strings.TrimSpace(owner))
	if owner == "" {
		return "", nil
	}
	at := strings.Index(owner, "@")
	if at <= 0 || at == len(owner)-1 || strings.ContainsAny(owner, " \t,") || strings.Count(owner, "@") != 1 {
		return "", fmt.Errorf("hostd owner %q must be a full email address or service account", owner)
	}
	return owner, nil
}

// Hostd is one row of the registry.
type Hostd struct {
	Name string `json:"name"`
	// Owner is the person or service account answerable for the hostd:
	// a full lowercased email, or "".
	Owner string `json:"owner"`
	// Labels select the hostd for host work that asks by label rather
	// than by name.
	Labels    map[string]string `json:"labels,omitempty"`
	CreatedBy string            `json:"created_by,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	RevokedAt *time.Time        `json:"revoked_at,omitempty"`
	// TokenPrefix is the start of the hostd's live token, for telling
	// tokens apart in Settings; "" when it has none (LocalHostd, or a
	// revoked hostd). Never stored on this row.
	TokenPrefix string `json:"token_prefix,omitempty"`
}

// Revoked reports whether h no longer takes connections.
func (h Hostd) Revoked() bool { return h.RevokedAt != nil }

// HashHostdToken is the verifier hostd_token stores for token.
func HashHostdToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// GenerateHostdToken mints a new hostd token.
func GenerateHostdToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating a hostd token: %w", err)
	}
	return HostdTokenPrefix + hex.EncodeToString(b), nil
}

// hostdTokenDisplayPrefix is how much of a token Settings shows.
func hostdTokenDisplayPrefix(token string) string {
	if len(token) > len(HostdTokenPrefix)+6 {
		return token[:len(HostdTokenPrefix)+6]
	}
	return token
}

// encodeHostdLabels is labels as the column stores them: sorted
// key=value pairs, comma separated.
func encodeHostdLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

// ParseHostdLabels reads "k=v,k2=v2" (encodeHostdLabels' form, which is
// also how the CLI and the allowlist's labels are written).
func ParseHostdLabels(s string) (map[string]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" || strings.ContainsAny(k+v, ",= \t\n\r") {
			return nil, fmt.Errorf("hostd label %q must be key=value", part)
		}
		out[k] = v
	}
	return out, nil
}

// ValidateHostdLabels refuses labels encodeHostdLabels cannot round-trip.
func ValidateHostdLabels(labels map[string]string) error {
	for k, v := range labels {
		if k == "" || v == "" || strings.ContainsAny(k+v, ",= \t\n\r") {
			return fmt.Errorf("hostd label %q=%q: keys and values may not be empty or hold ',', '=' or spaces", k, v)
		}
	}
	return nil
}

const hostdColumns = "`name`,`owner`,`labels`,`created_by`,`created_at`,`revoked_at`"

func scanHostd(scan func(...any) error) (Hostd, error) {
	var h Hostd
	var labels string
	var revoked sql.NullTime
	if err := scan(&h.Name, &h.Owner, &labels, &h.CreatedBy, &h.CreatedAt, &revoked); err != nil {
		return Hostd{}, err
	}
	h.CreatedAt = h.CreatedAt.UTC()
	if revoked.Valid {
		t := revoked.Time.UTC()
		h.RevokedAt = &t
	}
	h.Labels, _ = ParseHostdLabels(labels)
	return h, nil
}

// EnsureLocalHostd puts LocalHostd in the registry if it is not there.
func (s *Store) EnsureLocalHostd(ctx context.Context) error {
	return s.write(ctx, "ensure the local hostd", func(tx *sql.Tx) error {
		return ensureLocalHostd(ctx, tx)
	})
}

func ensureLocalHostd(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx,
		"INSERT INTO `hostd` ("+hostdColumns+") VALUES (?, '', '', 'grain', ?, NULL) ON CONFLICT (`name`) DO NOTHING",
		LocalHostd, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("adding the local hostd to the registry: %w", err)
	}
	return nil
}

// CreateHostd adds a remote hostd to the registry and mints its first
// token, which is returned once and never stored.
func (s *Store) CreateHostd(ctx context.Context, h Hostd) (*Hostd, string, error) {
	if err := ValidateHostdName(h.Name); err != nil {
		return nil, "", err
	}
	if h.Name == LocalHostd {
		return nil, "", fmt.Errorf("%q is the co-located hostd's name, and it takes no token", LocalHostd)
	}
	owner, err := NormalizeHostdOwner(h.Owner)
	if err != nil {
		return nil, "", err
	}
	if err := ValidateHostdLabels(h.Labels); err != nil {
		return nil, "", err
	}
	h.Owner = owner
	h.CreatedAt = time.Now().UTC()
	h.RevokedAt = nil
	token, err := GenerateHostdToken()
	if err != nil {
		return nil, "", err
	}
	err = s.write(ctx, "create hostd "+h.Name, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `hostd` WHERE `name` = ?", h.Name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrHostdExists
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `hostd` ("+hostdColumns+") VALUES (?, ?, ?, ?, ?, NULL)",
			h.Name, h.Owner, encodeHostdLabels(h.Labels), h.CreatedBy, h.CreatedAt); err != nil {
			return fmt.Errorf("inserting hostd: %w", err)
		}
		return insertHostdToken(ctx, tx, h.Name, token, h.CreatedAt)
	})
	if err != nil {
		return nil, "", err
	}
	h.TokenPrefix = hostdTokenDisplayPrefix(token)
	return &h, token, nil
}

func insertHostdToken(ctx context.Context, tx *sql.Tx, name, token string, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		"INSERT INTO `hostd_token` (`token_hash`,`hostd`,`prefix`,`created_at`,`revoked_at`) VALUES (?, ?, ?, ?, NULL)",
		HashHostdToken(token), name, hostdTokenDisplayPrefix(token), now)
	if err != nil {
		return fmt.Errorf("inserting hostd token: %w", err)
	}
	return nil
}

// ListHostds is the registry, revoked hostds included, by name.
func (s *Store) ListHostds(ctx context.Context) ([]Hostd, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+hostdColumns+" FROM `hostd` ORDER BY `name`")
	if err != nil {
		return nil, fmt.Errorf("listing hostds: %w", err)
	}
	var out []Hostd
	for rows.Next() {
		h, err := scanHostd(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning hostd: %w", err)
		}
		out = append(out, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	prefixes, err := s.liveHostdTokenPrefixes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].TokenPrefix = prefixes[out[i].Name]
	}
	return out, nil
}

func (s *Store) liveHostdTokenPrefixes(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `hostd`, `prefix` FROM `hostd_token` WHERE `revoked_at` IS NULL ORDER BY `created_at`")
	if err != nil {
		return nil, fmt.Errorf("listing hostd tokens: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, prefix string
		if err := rows.Scan(&name, &prefix); err != nil {
			return nil, err
		}
		out[name] = prefix
	}
	return out, rows.Err()
}

// GetHostd is the registry's row for name, or nil.
func (s *Store) GetHostd(ctx context.Context, name string) (*Hostd, error) {
	h, err := scanHostd(s.db.QueryRowContext(ctx, "SELECT "+hostdColumns+" FROM `hostd` WHERE `name` = ?", name).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting hostd %q: %w", name, err)
	}
	prefixes, err := s.liveHostdTokenPrefixes(ctx)
	if err != nil {
		return nil, err
	}
	h.TokenPrefix = prefixes[h.Name]
	return &h, nil
}

// HostdForToken is the live hostd token names, or nil: a token never
// minted, one rotated away, and one whose hostd is revoked all name none.
func (s *Store) HostdForToken(ctx context.Context, token string) (*Hostd, error) {
	if token == "" {
		return nil, nil
	}
	var name string
	err := s.db.QueryRowContext(ctx,
		"SELECT `hostd` FROM `hostd_token` WHERE `token_hash` = ? AND `revoked_at` IS NULL", HashHostdToken(token)).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up a hostd token: %w", err)
	}
	h, err := s.GetHostd(ctx, name)
	if err != nil || h == nil || h.Revoked() {
		return nil, err
	}
	return h, nil
}

// RotateHostdToken revokes every live token name has and mints a new
// one, returned once. A hostd still connected on the old token keeps its
// session until it drops; it cannot connect again with it.
func (s *Store) RotateHostdToken(ctx context.Context, name string) (string, error) {
	if name == LocalHostd {
		return "", fmt.Errorf("%q is the co-located hostd, and it takes no token", LocalHostd)
	}
	token, err := GenerateHostdToken()
	if err != nil {
		return "", err
	}
	err = s.write(ctx, "rotate hostd token "+name, func(tx *sql.Tx) error {
		h, err := scanHostd(tx.QueryRowContext(ctx, "SELECT "+hostdColumns+" FROM `hostd` WHERE `name` = ?", name).Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrHostdNotFound
		}
		if err != nil {
			return err
		}
		if h.Revoked() {
			return ErrHostdRevoked
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx,
			"UPDATE `hostd_token` SET `revoked_at` = ? WHERE `hostd` = ? AND `revoked_at` IS NULL", now, name); err != nil {
			return fmt.Errorf("revoking hostd tokens: %w", err)
		}
		return insertHostdToken(ctx, tx, name, token, now)
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// RevokeHostd revokes name and every token it has. Revoking again is a
// no-op that returns the row as it is.
func (s *Store) RevokeHostd(ctx context.Context, name string) (*Hostd, error) {
	if name == LocalHostd {
		return nil, fmt.Errorf("%q is the co-located hostd and cannot be revoked", LocalHostd)
	}
	err := s.write(ctx, "revoke hostd "+name, func(tx *sql.Tx) error {
		now := time.Now().UTC()
		res, err := tx.ExecContext(ctx, "UPDATE `hostd` SET `revoked_at` = ? WHERE `name` = ? AND `revoked_at` IS NULL", now, name)
		if err != nil {
			return fmt.Errorf("revoking hostd: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var c int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `hostd` WHERE `name` = ?", name).Scan(&c); err != nil {
				return err
			}
			if c == 0 {
				return ErrHostdNotFound
			}
		}
		_, err = tx.ExecContext(ctx,
			"UPDATE `hostd_token` SET `revoked_at` = ? WHERE `hostd` = ? AND `revoked_at` IS NULL", now, name)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetHostd(ctx, name)
}

// DeleteHostd removes name's registry row and every hostd_token row it
// has, once it is revoked: ErrHostdLive for one that is not,
// ErrHostdNotFound for a name the registry does not hold, and a refusal
// for LocalHostd. Nothing else is touched: task_run_hostd and the spool
// cursors keep the name each past run recorded, and the name is free to
// be registered again as a new hostd with nothing of this one's.
func (s *Store) DeleteHostd(ctx context.Context, name string) error {
	if name == LocalHostd {
		return fmt.Errorf("%q is the co-located hostd and cannot be deleted", LocalHostd)
	}
	return s.write(ctx, "delete hostd "+name, func(tx *sql.Tx) error {
		h, err := scanHostd(tx.QueryRowContext(ctx, "SELECT "+hostdColumns+" FROM `hostd` WHERE `name` = ?", name).Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrHostdNotFound
		}
		if err != nil {
			return err
		}
		if !h.Revoked() {
			return ErrHostdLive
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM `hostd_token` WHERE `hostd` = ?", name); err != nil {
			return fmt.Errorf("deleting hostd %s's tokens: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM `hostd` WHERE `name` = ?", name); err != nil {
			return fmt.Errorf("deleting hostd %s: %w", name, err)
		}
		return nil
	})
}

// UpdateHostd sets name's owner and labels; a nil pointer leaves that
// field as it is.
func (s *Store) UpdateHostd(ctx context.Context, name string, owner *string, labels *map[string]string) (*Hostd, error) {
	if owner != nil {
		o, err := NormalizeHostdOwner(*owner)
		if err != nil {
			return nil, err
		}
		owner = &o
	}
	if labels != nil {
		if err := ValidateHostdLabels(*labels); err != nil {
			return nil, err
		}
	}
	err := s.write(ctx, "update hostd "+name, func(tx *sql.Tx) error {
		var c int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `hostd` WHERE `name` = ?", name).Scan(&c); err != nil {
			return err
		}
		if c == 0 {
			return ErrHostdNotFound
		}
		if owner != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE `hostd` SET `owner` = ? WHERE `name` = ?", *owner, name); err != nil {
				return err
			}
		}
		if labels != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE `hostd` SET `labels` = ? WHERE `name` = ?", encodeHostdLabels(*labels), name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetHostd(ctx, name)
}
