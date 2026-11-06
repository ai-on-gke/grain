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
// each with a credential of its own (hostd_token) that names it and,
// for hosts enrolled through the join handshake, is bound to the host's
// Ed25519 public key. A connection's identity is the host its credential
// belongs to and nothing else -- what the host says about itself only
// has to agree.

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
// daemon, reached at its socket rather than dialing in. It has no token
// and requires no handshake.
const LocalHostd = "local"

// HostdTokenPrefix starts every minted hostd token, so one pasted into
// the wrong place is recognisable.
const HostdTokenPrefix = "ghd_"

// HostdCredentialTTL is how long an issued key-bound host credential
// lasts before it expires unless renewed while connected.
const HostdCredentialTTL = 30 * 24 * time.Hour

// hostdRenewGrace keeps the previous credential valid briefly across a
// silent renewal so an interrupted response write cannot strand a live host.
const hostdRenewGrace = 2 * time.Minute

var (
	// ErrHostdNotFound is a registry operation on a name it does not hold.
	ErrHostdNotFound = errors.New("model: host not found")
	// ErrHostdRevoked is a token minted for a hostd that is revoked.
	ErrHostdRevoked = errors.New("model: host is revoked")
	// ErrHostdExists is CreateHostd or RenameHostd on a name already taken.
	ErrHostdExists = errors.New("model: a host by that name already exists")
	// ErrHostdNotKeyBound is RenewHostdCredential on a host holding a legacy
	// per-host token: only key-bound credentials renew (migrate with rekey).
	ErrHostdNotKeyBound = errors.New("model: this host holds a legacy per-host token, which does not renew; migrate it with rekey")
	// ErrHostdLive is DeleteHostd on a hostd that is not revoked: a live
	// hostd is revoked first, so deleting one never cuts off a connected
	// hostd or its running work by surprise.
	ErrHostdLive = errors.New("model: host is not revoked; revoke it first")
)

// hostdNameRE is what a hostd name may be: a DNS label, so it reads the
// same in a log line, a URL path and a notification subject.
var hostdNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// githubOwnerLoginRE matches a GitHub login or org slug after "github:".
var githubOwnerLoginRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,37}[a-z0-9])?$`)

// ValidateHostdName refuses a name a hostd may not have.
func ValidateHostdName(name string) error {
	if !hostdNameRE.MatchString(name) {
		return fmt.Errorf("host name %q must be a lowercase DNS label: letters, digits and '-', at most 63", name)
	}
	return nil
}

// NormalizeHostdOwner is owner as the registry stores it: a full email
// address or service account, a "github:<login>" handle from OSS device-flow
// enrollment, lowercased, or "" for none.
func NormalizeHostdOwner(owner string) (string, error) {
	owner = strings.ToLower(strings.TrimSpace(owner))
	if owner == "" {
		return "", nil
	}
	if login, ok := strings.CutPrefix(owner, "github:"); ok {
		if !githubOwnerLoginRE.MatchString(login) {
			return "", fmt.Errorf("host owner %q must be a valid github:<login> handle", owner)
		}
		return "github:" + login, nil
	}
	at := strings.Index(owner, "@")
	if at <= 0 || at == len(owner)-1 || strings.ContainsAny(owner, " \t,") || strings.Count(owner, "@") != 1 {
		return "", fmt.Errorf("host owner %q must be a full email address or service account", owner)
	}
	return owner, nil
}

// Hostd is one row of the registry.
type Hostd struct {
	Name string `json:"name"`
	// Owner is the person or service account answerable for the host:
	// a full lowercased email, "github:<login>", or "".
	Owner string `json:"owner"`
	// Labels select the host for host work that asks by label rather
	// than by name.
	Labels    map[string]string `json:"labels,omitempty"`
	CreatedBy string            `json:"created_by,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	RevokedAt *time.Time        `json:"revoked_at,omitempty"`
	// PublicKey is the host's Ed25519 public key ("ed25519:<base64>"),
	// or "" for LocalHostd and legacy per-host tokens.
	PublicKey      string     `json:"public_key,omitempty"`
	KeyFingerprint string     `json:"key_fingerprint,omitempty"`
	EnrollmentCode string     `json:"enrollment_code,omitempty"`
	Capacity       int        `json:"capacity,omitempty"`
	Hostname       string     `json:"hostname,omitempty"`
	OS             string     `json:"os,omitempty"`
	Arch           string     `json:"arch,omitempty"`
	CLIs           []string   `json:"clis,omitempty"`
	Version        string     `json:"version,omitempty"`
	LocalUser      string     `json:"local_user,omitempty"`
	LastSeenAt     *time.Time `json:"last_seen_at,omitempty"`
	// TokenPrefix is the start of the host's live token, for telling
	// tokens apart in Settings; "" when it has none (LocalHostd, or a
	// revoked or expired host). Never stored on this row.
	TokenPrefix    string     `json:"token_prefix,omitempty"`
	TokenExpiresAt *time.Time `json:"token_expires_at,omitempty"`
}

// Revoked reports whether h no longer takes connections.
func (h Hostd) Revoked() bool { return h.RevokedAt != nil }

// KeyBound reports whether h requires Ed25519 proof-of-possession on
// every connection and renewal.
func (h Hostd) KeyBound() bool { return strings.TrimSpace(h.PublicKey) != "" }

// HashHostdToken is the verifier hostd_token stores for token.
func HashHostdToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// GenerateHostdToken mints a new hostd token.
func GenerateHostdToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating a host token: %w", err)
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
			return nil, fmt.Errorf("host label %q must be key=value", part)
		}
		out[k] = v
	}
	return out, nil
}

// ValidateHostdLabels refuses labels encodeHostdLabels cannot round-trip.
func ValidateHostdLabels(labels map[string]string) error {
	for k, v := range labels {
		if k == "" || v == "" || strings.ContainsAny(k+v, ",= \t\n\r") {
			return fmt.Errorf("host label %q=%q: keys and values may not be empty or hold ',', '=' or spaces", k, v)
		}
	}
	return nil
}

func encodeHostdCSV(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" || seen[it] {
			continue
		}
		seen[it] = true
		out = append(out, it)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func decodeHostdCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

const hostdColumns = "`name`,`owner`,`labels`,`created_by`,`created_at`,`revoked_at`," +
	"`public_key`,`key_fingerprint`,`enrollment_code`,`capacity`," +
	"`hostname`,`os`,`arch`,`clis`,`version`,`local_user`,`last_seen_at`"

func scanHostd(scan func(...any) error) (Hostd, error) {
	var h Hostd
	var labels, clis string
	var revoked, lastSeen sql.NullTime
	if err := scan(
		&h.Name, &h.Owner, &labels, &h.CreatedBy, &h.CreatedAt, &revoked,
		&h.PublicKey, &h.KeyFingerprint, &h.EnrollmentCode, &h.Capacity,
		&h.Hostname, &h.OS, &h.Arch, &clis, &h.Version, &h.LocalUser, &lastSeen,
	); err != nil {
		return Hostd{}, err
	}
	h.CreatedAt = h.CreatedAt.UTC()
	if revoked.Valid {
		t := revoked.Time.UTC()
		h.RevokedAt = &t
	}
	if lastSeen.Valid {
		t := lastSeen.Time.UTC()
		h.LastSeenAt = &t
	}
	h.Labels, _ = ParseHostdLabels(labels)
	h.CLIs = decodeHostdCSV(clis)
	return h, nil
}

// EnsureLocalHostd puts LocalHostd in the registry if it is not there.
func (s *Store) EnsureLocalHostd(ctx context.Context) error {
	return s.write(ctx, "ensure the local hostd", func(tx *sql.Tx) error {
		return ensureLocalHostd(ctx, tx)
	})
}

// ensureLocalHostd names only the original schema-92 columns so migration 92's
// Up step runs unchanged before migration 117 adds the enrollment columns.
func ensureLocalHostd(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx,
		"INSERT INTO `hostd` (`name`,`owner`,`labels`,`created_by`,`created_at`,`revoked_at`) VALUES (?, '', '', 'grain', ?, NULL) ON CONFLICT (`name`) DO NOTHING",
		LocalHostd, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("adding the local host to the registry: %w", err)
	}
	return nil
}

// CreateHostd adds a remote hostd to the registry and mints its first
// legacy per-host token, which is returned once and never stored.
func (s *Store) CreateHostd(ctx context.Context, h Hostd) (*Hostd, string, error) {
	if err := ValidateHostdName(h.Name); err != nil {
		return nil, "", err
	}
	if h.Name == LocalHostd {
		return nil, "", fmt.Errorf("%q is the co-located host's name, and it takes no token", LocalHostd)
	}
	owner, err := NormalizeHostdOwner(h.Owner)
	if err != nil {
		return nil, "", err
	}
	if err := ValidateHostdLabels(h.Labels); err != nil {
		return nil, "", err
	}
	if h.Capacity < 0 {
		return nil, "", fmt.Errorf("host capacity %d may not be negative", h.Capacity)
	}
	h.Owner = owner
	h.CreatedAt = time.Now().UTC()
	h.RevokedAt = nil
	var expiresAt *time.Time
	if h.KeyBound() {
		exp := h.CreatedAt.Add(HostdCredentialTTL)
		expiresAt = &exp
	}
	token, err := GenerateHostdToken()
	if err != nil {
		return nil, "", err
	}
	err = s.write(ctx, "create host "+h.Name, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `hostd` WHERE `name` = ?", h.Name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrHostdExists
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `hostd` ("+hostdColumns+") VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)",
			h.Name, h.Owner, encodeHostdLabels(h.Labels), h.CreatedBy, h.CreatedAt,
			h.PublicKey, h.KeyFingerprint, h.EnrollmentCode, h.Capacity,
			h.Hostname, h.OS, h.Arch, encodeHostdCSV(h.CLIs), h.Version, h.LocalUser); err != nil {
			return fmt.Errorf("inserting hostd: %w", err)
		}
		return insertHostdToken(ctx, tx, h.Name, token, h.CreatedAt, expiresAt)
	})
	if err != nil {
		return nil, "", err
	}
	h.TokenPrefix = hostdTokenDisplayPrefix(token)
	h.TokenExpiresAt = expiresAt
	return &h, token, nil
}

func insertHostdToken(ctx context.Context, tx *sql.Tx, name, token string, now time.Time, expiresAt *time.Time) error {
	var exp any
	if expiresAt != nil {
		exp = expiresAt.UTC()
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO `hostd_token` (`token_hash`,`hostd`,`prefix`,`created_at`,`expires_at`,`revoked_at`) VALUES (?, ?, ?, ?, ?, NULL)",
		HashHostdToken(token), name, hostdTokenDisplayPrefix(token), now.UTC(), exp)
	if err != nil {
		return fmt.Errorf("inserting host token: %w", err)
	}
	return nil
}

// ListHostds is the registry, by name: revoked hostds included until they
// are deleted (DeleteHostd).
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
	tokens, err := s.liveHostdTokens(ctx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	for i := range out {
		if tok, ok := tokens[out[i].Name]; ok {
			out[i].TokenPrefix = tok.prefix
			out[i].TokenExpiresAt = tok.expiresAt
		}
	}
	return out, nil
}

type liveTokenMeta struct {
	prefix    string
	expiresAt *time.Time
}

func (s *Store) liveHostdTokens(ctx context.Context, now time.Time) (map[string]liveTokenMeta, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `hostd`, `prefix`, `expires_at` FROM `hostd_token` WHERE `revoked_at` IS NULL ORDER BY `created_at`")
	if err != nil {
		return nil, fmt.Errorf("listing host tokens: %w", err)
	}
	defer rows.Close()
	now = now.UTC()
	out := map[string]liveTokenMeta{}
	for rows.Next() {
		var name, prefix string
		var exp sql.NullTime
		if err := rows.Scan(&name, &prefix, &exp); err != nil {
			return nil, err
		}
		var expiresAt *time.Time
		if exp.Valid {
			t := exp.Time.UTC()
			if !t.After(now) {
				continue
			}
			expiresAt = &t
		}
		out[name] = liveTokenMeta{prefix: prefix, expiresAt: expiresAt}
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
		return nil, fmt.Errorf("getting host %q: %w", name, err)
	}
	tokens, err := s.liveHostdTokens(ctx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if tok, ok := tokens[h.Name]; ok {
		h.TokenPrefix = tok.prefix
		h.TokenExpiresAt = tok.expiresAt
	}
	return &h, nil
}

// HostdForToken is the live hostd token names at the current time, or nil:
// a token never minted, one rotated away, one past its 30-day expiry, and
// one whose hostd is revoked all name none.
func (s *Store) HostdForToken(ctx context.Context, token string) (*Hostd, error) {
	return s.HostdForTokenAt(ctx, token, time.Now().UTC())
}

// HostdForTokenAt is HostdForToken evaluated at now.
func (s *Store) HostdForTokenAt(ctx context.Context, token string, now time.Time) (*Hostd, error) {
	if token == "" {
		return nil, nil
	}
	var name string
	var exp sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT `hostd`, `expires_at` FROM `hostd_token` WHERE `token_hash` = ? AND `revoked_at` IS NULL",
		HashHostdToken(token)).Scan(&name, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up a host token: %w", err)
	}
	if exp.Valid && !exp.Time.UTC().After(now.UTC()) {
		return nil, nil
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
		return "", fmt.Errorf("%q is the co-located host, and it takes no token", LocalHostd)
	}
	token, err := GenerateHostdToken()
	if err != nil {
		return "", err
	}
	err = s.write(ctx, "rotate host token "+name, func(tx *sql.Tx) error {
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
			return fmt.Errorf("revoking host tokens: %w", err)
		}
		var expiresAt *time.Time
		if h.KeyBound() {
			exp := now.Add(HostdCredentialTTL)
			expiresAt = &exp
		}
		return insertHostdToken(ctx, tx, name, token, now, expiresAt)
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// RenewHostdCredential silently renews a connected key-bound host's
// credential for a fresh HostdCredentialTTL window starting at now.
// currentToken must be a live, non-expired token for name, and the host
// must be key-bound: a legacy per-host token never renews (it would turn a
// non-expiring token into a 30-day one with no key behind it), and the
// caller has already checked the host's Ed25519 renewal proof.
func (s *Store) RenewHostdCredential(ctx context.Context, name, currentToken string, now time.Time) (string, time.Time, error) {
	if name == LocalHostd {
		return "", time.Time{}, fmt.Errorf("%q is the co-located hostd, and it takes no token", LocalHostd)
	}
	if strings.TrimSpace(currentToken) == "" {
		return "", time.Time{}, ErrHostdRevoked
	}
	now = now.UTC()
	nextToken, err := GenerateHostdToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := now.Add(HostdCredentialTTL)
	err = s.write(ctx, "renew host credential "+name, func(tx *sql.Tx) error {
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
		if !h.KeyBound() {
			return ErrHostdNotKeyBound
		}
		var gotName string
		var exp sql.NullTime
		err = tx.QueryRowContext(ctx,
			"SELECT `hostd`, `expires_at` FROM `hostd_token` WHERE `token_hash` = ? AND `revoked_at` IS NULL",
			HashHostdToken(currentToken)).Scan(&gotName, &exp)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && gotName != name) {
			return ErrHostdRevoked
		}
		if err != nil {
			return err
		}
		if exp.Valid && !exp.Time.UTC().After(now) {
			return ErrHostdRevoked
		}
		grace := now.Add(hostdRenewGrace)
		if _, err := tx.ExecContext(ctx,
			"UPDATE `hostd_token` SET `expires_at` = ? WHERE `hostd` = ? AND `revoked_at` IS NULL AND (`expires_at` IS NULL OR `expires_at` > ?)",
			grace, name, grace); err != nil {
			return fmt.Errorf("shortening superseded hostd tokens: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `hostd` SET `last_seen_at` = ? WHERE `name` = ?", now, name); err != nil {
			return err
		}
		return insertHostdToken(ctx, tx, name, nextToken, now, &expiresAt)
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return nextToken, expiresAt, nil
}

// TouchHostdLastSeen records that name checked in at at.
func (s *Store) TouchHostdLastSeen(ctx context.Context, name string, at time.Time) error {
	return s.write(ctx, "touch hostd "+name, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE `hostd` SET `last_seen_at` = ? WHERE `name` = ?", at.UTC(), name)
		return err
	})
}

// RecordHostdCheckin updates observed host details and last_seen_at when a
// host connects. Empty strings leave existing stored metadata untouched.
func (s *Store) RecordHostdCheckin(ctx context.Context, name, hostname, osName, arch string, clis []string, version, localUser string, at time.Time) error {
	return s.write(ctx, "record hostd check-in "+name, func(tx *sql.Tx) error {
		h, err := scanHostd(tx.QueryRowContext(ctx, "SELECT "+hostdColumns+" FROM `hostd` WHERE `name` = ?", name).Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if hostname == "" {
			hostname = h.Hostname
		}
		if osName == "" {
			osName = h.OS
		}
		if arch == "" {
			arch = h.Arch
		}
		if len(clis) == 0 {
			clis = h.CLIs
		}
		if version == "" {
			version = h.Version
		}
		if localUser == "" {
			localUser = h.LocalUser
		}
		_, err = tx.ExecContext(ctx,
			"UPDATE `hostd` SET `hostname` = ?, `os` = ?, `arch` = ?, `clis` = ?, `version` = ?, `local_user` = ?, `last_seen_at` = ? WHERE `name` = ?",
			hostname, osName, arch, encodeHostdCSV(clis), version, localUser, at.UTC(), name)
		return err
	})
}

// RevokeHostd revokes name and every token it has. Revoking again is a
// no-op that returns the row as it is.
func (s *Store) RevokeHostd(ctx context.Context, name string) (*Hostd, error) {
	if name == LocalHostd {
		return nil, fmt.Errorf("%q is the co-located host and cannot be revoked", LocalHostd)
	}
	err := s.write(ctx, "revoke host "+name, func(tx *sql.Tx) error {
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

// HostdUpdate carries optional edits to a host's mutable fields.
type HostdUpdate struct {
	Owner    *string
	Labels   *map[string]string
	Capacity *int
}

// DeleteHostd removes name's registry row and every hostd_token row it
// has, once it is revoked: ErrHostdLive for one that is not,
// ErrHostdNotFound for a name the registry does not hold, and a refusal
// for LocalHostd. Nothing else is touched: task_run_hostd and the spool
// cursors keep the name each past run recorded, and the name is free to
// be registered again as a new hostd with nothing of this one's.
func (s *Store) DeleteHostd(ctx context.Context, name string) error {
	if name == LocalHostd {
		return fmt.Errorf("%q is the co-located host and cannot be deleted", LocalHostd)
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
	return s.UpdateHostdDetails(ctx, name, HostdUpdate{Owner: owner, Labels: labels})
}

// UpdateHostdDetails sets any non-nil fields on name.
func (s *Store) UpdateHostdDetails(ctx context.Context, name string, u HostdUpdate) (*Hostd, error) {
	if u.Owner != nil {
		o, err := NormalizeHostdOwner(*u.Owner)
		if err != nil {
			return nil, err
		}
		u.Owner = &o
	}
	if u.Labels != nil {
		if err := ValidateHostdLabels(*u.Labels); err != nil {
			return nil, err
		}
	}
	if u.Capacity != nil && *u.Capacity < 0 {
		return nil, fmt.Errorf("host capacity %d may not be negative", *u.Capacity)
	}
	err := s.write(ctx, "update host "+name, func(tx *sql.Tx) error {
		var c int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `hostd` WHERE `name` = ?", name).Scan(&c); err != nil {
			return err
		}
		if c == 0 {
			return ErrHostdNotFound
		}
		if u.Owner != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE `hostd` SET `owner` = ? WHERE `name` = ?", *u.Owner, name); err != nil {
				return err
			}
		}
		if u.Labels != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE `hostd` SET `labels` = ? WHERE `name` = ?", encodeHostdLabels(*u.Labels), name); err != nil {
				return err
			}
		}
		if u.Capacity != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE `hostd` SET `capacity` = ? WHERE `name` = ?", *u.Capacity, name); err != nil {
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

// RenameHostd renames oldName to newName across the registry, its tokens,
// and recorded run attributions. LocalHostd cannot be renamed.
func (s *Store) RenameHostd(ctx context.Context, oldName, newName string) (*Hostd, error) {
	if oldName == LocalHostd || newName == LocalHostd {
		return nil, fmt.Errorf("%q is the co-located hostd and cannot be renamed", LocalHostd)
	}
	if err := ValidateHostdName(newName); err != nil {
		return nil, err
	}
	if oldName == newName {
		h, err := s.GetHostd(ctx, oldName)
		if err != nil {
			return nil, err
		}
		if h == nil {
			return nil, ErrHostdNotFound
		}
		return h, nil
	}
	err := s.write(ctx, "rename hostd "+oldName+" to "+newName, func(tx *sql.Tx) error {
		var c int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `hostd` WHERE `name` = ?", oldName).Scan(&c); err != nil {
			return err
		}
		if c == 0 {
			return ErrHostdNotFound
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `hostd` WHERE `name` = ?", newName).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return ErrHostdExists
		}
		if _, err := tx.ExecContext(ctx, "UPDATE `hostd` SET `name` = ? WHERE `name` = ?", newName, oldName); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE `hostd_token` SET `hostd` = ? WHERE `hostd` = ?", newName, oldName); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE `task_run_hostd` SET `hostd` = ? WHERE `hostd` = ?", newName, oldName); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetHostd(ctx, newName)
}
