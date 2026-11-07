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
	"strings"
	"time"
)

// HostEnrollmentCodePrefix starts every shared host enrollment code.
const HostEnrollmentCodePrefix = "ghe_"

// HostJoinRequestTTL is how long a pending host join request waits for the
// deployment owner's pairing-code check before expiring.
const HostJoinRequestTTL = 24 * time.Hour

// DefaultHostEnrollmentCodeName is the name of the default enrollment code
// seeded the first time a deployment prepares its shared invite snippet.
const DefaultHostEnrollmentCodeName = "default"

var (
	// ErrEnrollmentCodeNotFound is an operation on an unknown enrollment code.
	ErrEnrollmentCodeNotFound = errors.New("model: host enrollment code not found")
	// ErrEnrollmentCodeExists is CreateHostEnrollmentCode on a name already taken.
	ErrEnrollmentCodeExists = errors.New("model: a host enrollment code by that name already exists")
	// ErrEnrollmentCodeRevoked is a join attempt with a revoked enrollment code.
	ErrEnrollmentCodeRevoked = errors.New("model: host enrollment code is revoked")
	// ErrEnrollmentCodeExpired is a join attempt with an expired enrollment code.
	ErrEnrollmentCodeExpired = errors.New("model: host enrollment code has expired")
	// ErrEnrollmentCodeExhausted is a join attempt with a code at max_uses.
	ErrEnrollmentCodeExhausted = errors.New("model: host enrollment code has reached its maximum uses")
	// ErrJoinRequestNotFound is an operation on a missing or expired join request.
	ErrJoinRequestNotFound = errors.New("model: host join request not found")
	// ErrJoinRequestAlreadyAccepted is Accept or Reject on an already-accepted request.
	ErrJoinRequestAlreadyAccepted = errors.New("model: host join request has already been accepted")
	// ErrRekeyKeyMismatch is a rekey of a host that is already key-bound by a
	// request signed with a different key: only its bound key may rekey it
	// (a copied credential alone must not move the host to a new key), and a
	// host whose key is lost re-enrolls with --join instead.
	ErrRekeyKeyMismatch = errors.New("model: this host is already key-bound, and only its bound Ed25519 key may rekey it; re-enroll it with --join instead")
)

// SameHostPublicKey reports whether a and b name the same "ed25519:<base64>"
// public key, ignoring surrounding space and base64 padding.
func SameHostPublicKey(a, b string) bool {
	norm := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "=") }
	return norm(a) != "" && norm(a) == norm(b)
}

// HostEnrollmentCode is one shared host enrollment code. It grants nothing
// except filing a pending join request.
type HostEnrollmentCode struct {
	Name string `json:"name"`
	// SecretCode holds the active raw code (ghe_...) when unrevoked so the
	// deployment owner's "Invite hosts" view can render the shared snippet;
	// json:"-" ensures accidental struct serialization never emits it.
	SecretCode        string            `json:"-"`
	Prefix            string            `json:"prefix"`
	DefaultLabels     map[string]string `json:"default_labels,omitempty"`
	DefaultCapacity   int               `json:"default_capacity,omitempty"`
	AllowedPrincipals []string          `json:"allowed_principals,omitempty"`
	MaxUses           int               `json:"max_uses,omitempty"`
	Uses              int               `json:"uses"`
	ExpiresAt         *time.Time        `json:"expires_at,omitempty"`
	CreatedBy         string            `json:"created_by,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	RotatedAt         *time.Time        `json:"rotated_at,omitempty"`
	RevokedAt         *time.Time        `json:"revoked_at,omitempty"`
}

// ActiveAt reports whether c can file a new join request at now.
func (c HostEnrollmentCode) ActiveAt(now time.Time) bool {
	if c.RevokedAt != nil {
		return false
	}
	if c.ExpiresAt != nil && !c.ExpiresAt.After(now.UTC()) {
		return false
	}
	if c.MaxUses > 0 && c.Uses >= c.MaxUses {
		return false
	}
	return true
}

// HashHostEnrollmentCode hashes a raw ghe_... enrollment code.
func HashHostEnrollmentCode(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

// GenerateHostEnrollmentCode mints a random ghe_... enrollment code.
func GenerateHostEnrollmentCode() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating host enrollment code: %w", err)
	}
	return HostEnrollmentCodePrefix + hex.EncodeToString(b), nil
}

func enrollmentCodeDisplayPrefix(code string) string {
	if len(code) > len(HostEnrollmentCodePrefix)+6 {
		return code[:len(HostEnrollmentCodePrefix)+6]
	}
	return code
}

// NormalizeHostEnrollmentPrincipal normalizes one allowed-principal entry on
// a host enrollment code: user:<email>, group:<email>, github:<login>, or org:<org>.
func NormalizeHostEnrollmentPrincipal(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return "", fmt.Errorf("allowed principal entry may not be empty")
	}
	kind, val, ok := strings.Cut(raw, ":")
	if !ok || val == "" {
		return "", fmt.Errorf("allowed principal %q must start with user:, group:, github: or org:", raw)
	}
	switch kind {
	case "user", "group":
		at := strings.Index(val, "@")
		if at <= 0 || at == len(val)-1 || strings.ContainsAny(val, " \t,") || strings.Count(val, "@") != 1 {
			return "", fmt.Errorf("allowed principal %q must carry a valid email address", raw)
		}
		return kind + ":" + val, nil
	case "github", "org":
		if !githubOwnerLoginRE.MatchString(val) {
			return "", fmt.Errorf("allowed principal %q must carry a valid GitHub login or org slug", raw)
		}
		return kind + ":" + val, nil
	default:
		return "", fmt.Errorf("allowed principal %q has unsupported prefix %q (want user:, group:, github: or org:)", raw, kind+":")
	}
}

// NormalizeHostEnrollmentPrincipals normalizes and deduplicates a list of
// allowed-principal entries.
func NormalizeHostEnrollmentPrincipals(entries []string) ([]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		norm, err := NormalizeHostEnrollmentPrincipal(raw)
		if err != nil {
			return nil, err
		}
		if !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// SanitizeHostdName derives a valid default host name from a raw hostname.
func SanitizeHostdName(hostname string) string {
	h := strings.ToLower(strings.TrimSpace(hostname))
	if dot := strings.IndexByte(h, '.'); dot > 0 {
		h = h[:dot]
	}
	var b strings.Builder
	prevDash := false
	for i := 0; i < len(h); i++ {
		ch := h[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			b.WriteByte(ch)
			prevDash = false
		} else if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	if out == "" || out == LocalHostd {
		if out == LocalHostd {
			return "host-local"
		}
		return "host"
	}
	return out
}

const hostEnrollmentCodeColumns = "`name`,`code_hash`,`code_value`,`prefix`,`default_labels`,`default_capacity`," +
	"`allowed_principals`,`max_uses`,`uses`,`expires_at`,`created_by`,`created_at`,`rotated_at`,`revoked_at`"

func scanHostEnrollmentCode(scan func(...any) error) (HostEnrollmentCode, string, error) {
	var c HostEnrollmentCode
	var codeHash, labels, allowed string
	var exp, rotated, revoked sql.NullTime
	if err := scan(
		&c.Name, &codeHash, &c.SecretCode, &c.Prefix, &labels, &c.DefaultCapacity,
		&allowed, &c.MaxUses, &c.Uses, &exp, &c.CreatedBy, &c.CreatedAt, &rotated, &revoked,
	); err != nil {
		return HostEnrollmentCode{}, "", err
	}
	c.CreatedAt = c.CreatedAt.UTC()
	if exp.Valid {
		t := exp.Time.UTC()
		c.ExpiresAt = &t
	}
	if rotated.Valid {
		t := rotated.Time.UTC()
		c.RotatedAt = &t
	}
	if revoked.Valid {
		t := revoked.Time.UTC()
		c.RevokedAt = &t
		c.SecretCode = ""
	}
	c.DefaultLabels, _ = ParseHostdLabels(labels)
	c.AllowedPrincipals = decodeHostdCSV(allowed)
	return c, codeHash, nil
}

// CreateHostEnrollmentCode creates a named shared host enrollment code and
// returns the record and raw ghe_... code.
func (s *Store) CreateHostEnrollmentCode(ctx context.Context, c HostEnrollmentCode) (*HostEnrollmentCode, string, error) {
	c.Name = strings.TrimSpace(c.Name)
	if err := ValidateHostdName(c.Name); err != nil {
		return nil, "", fmt.Errorf("enrollment code name %q must be a lowercase DNS label", c.Name)
	}
	if err := ValidateHostdLabels(c.DefaultLabels); err != nil {
		return nil, "", err
	}
	if c.DefaultCapacity < 0 {
		return nil, "", fmt.Errorf("default capacity %d may not be negative", c.DefaultCapacity)
	}
	if c.MaxUses < 0 {
		return nil, "", fmt.Errorf("max uses %d may not be negative", c.MaxUses)
	}
	principals, err := NormalizeHostEnrollmentPrincipals(c.AllowedPrincipals)
	if err != nil {
		return nil, "", err
	}
	rawCode, err := GenerateHostEnrollmentCode()
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	c.AllowedPrincipals = principals
	c.SecretCode = rawCode
	c.Prefix = enrollmentCodeDisplayPrefix(rawCode)
	c.Uses = 0
	c.CreatedAt = now
	c.RotatedAt = nil
	c.RevokedAt = nil
	var exp any
	if c.ExpiresAt != nil {
		t := c.ExpiresAt.UTC()
		c.ExpiresAt = &t
		exp = t
	}
	err = s.write(ctx, "create host enrollment code "+c.Name, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `host_enrollment_code` WHERE `name` = ?", c.Name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrEnrollmentCodeExists
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `host_enrollment_code` ("+hostEnrollmentCodeColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, NULL, NULL)",
			c.Name, HashHostEnrollmentCode(rawCode), rawCode, c.Prefix,
			encodeHostdLabels(c.DefaultLabels), c.DefaultCapacity,
			encodeHostdCSV(c.AllowedPrincipals), c.MaxUses, exp, c.CreatedBy, now)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return &c, rawCode, nil
}

// EnsureDefaultHostEnrollmentCode seeds a "default" enrollment code only when
// the deployment has never created any enrollment codes yet, so revoking every
// code genuinely stops new joins until the owner rotates or creates one.
func (s *Store) EnsureDefaultHostEnrollmentCode(ctx context.Context, createdBy string) (*HostEnrollmentCode, error) {
	var out *HostEnrollmentCode
	err := s.write(ctx, "ensure default host enrollment code", func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `host_enrollment_code`").Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		rawCode, err := GenerateHostEnrollmentCode()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		prefix := enrollmentCodeDisplayPrefix(rawCode)
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `host_enrollment_code` ("+hostEnrollmentCodeColumns+") VALUES (?, ?, ?, ?, '', 0, '', 0, 0, NULL, ?, ?, NULL, NULL)",
			DefaultHostEnrollmentCodeName, HashHostEnrollmentCode(rawCode), rawCode, prefix, createdBy, now); err != nil {
			return err
		}
		out = &HostEnrollmentCode{
			Name:       DefaultHostEnrollmentCodeName,
			SecretCode: rawCode,
			Prefix:     prefix,
			CreatedBy:  createdBy,
			CreatedAt:  now,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out != nil {
		return out, nil
	}
	return s.GetHostEnrollmentCode(ctx, DefaultHostEnrollmentCodeName)
}

// ListHostEnrollmentCodes returns all enrollment codes ordered by name.
func (s *Store) ListHostEnrollmentCodes(ctx context.Context) ([]HostEnrollmentCode, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+hostEnrollmentCodeColumns+" FROM `host_enrollment_code` ORDER BY `name`")
	if err != nil {
		return nil, fmt.Errorf("listing host enrollment codes: %w", err)
	}
	defer rows.Close()
	var out []HostEnrollmentCode
	for rows.Next() {
		c, _, err := scanHostEnrollmentCode(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetHostEnrollmentCode returns name's enrollment code, or nil if absent.
func (s *Store) GetHostEnrollmentCode(ctx context.Context, name string) (*HostEnrollmentCode, error) {
	c, _, err := scanHostEnrollmentCode(s.db.QueryRowContext(ctx,
		"SELECT "+hostEnrollmentCodeColumns+" FROM `host_enrollment_code` WHERE `name` = ?", name).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// LookupHostEnrollmentCode resolves a raw ghe_... code and checks whether it
// is active at now.
func (s *Store) LookupHostEnrollmentCode(ctx context.Context, rawCode string, now time.Time) (*HostEnrollmentCode, error) {
	rawCode = strings.TrimSpace(rawCode)
	if rawCode == "" {
		return nil, ErrEnrollmentCodeNotFound
	}
	c, _, err := scanHostEnrollmentCode(s.db.QueryRowContext(ctx,
		"SELECT "+hostEnrollmentCodeColumns+" FROM `host_enrollment_code` WHERE `code_hash` = ?",
		HashHostEnrollmentCode(rawCode)).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEnrollmentCodeNotFound
	}
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	if c.RevokedAt != nil {
		return nil, ErrEnrollmentCodeRevoked
	}
	if c.ExpiresAt != nil && !c.ExpiresAt.After(now) {
		return nil, ErrEnrollmentCodeExpired
	}
	if c.MaxUses > 0 && c.Uses >= c.MaxUses {
		return nil, ErrEnrollmentCodeExhausted
	}
	return &c, nil
}

// RotateHostEnrollmentCode mints a replacement ghe_... secret for name,
// invalidating the previous secret immediately while leaving accepted hosts
// untouched.
func (s *Store) RotateHostEnrollmentCode(ctx context.Context, name string) (*HostEnrollmentCode, string, error) {
	rawCode, err := GenerateHostEnrollmentCode()
	if err != nil {
		return nil, "", err
	}
	prefix := enrollmentCodeDisplayPrefix(rawCode)
	now := time.Now().UTC()
	err = s.write(ctx, "rotate host enrollment code "+name, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"UPDATE `host_enrollment_code` SET `code_hash` = ?, `code_value` = ?, `prefix` = ?, `uses` = 0, `rotated_at` = ?, `revoked_at` = NULL WHERE `name` = ?",
			HashHostEnrollmentCode(rawCode), rawCode, prefix, now, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrEnrollmentCodeNotFound
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	c, err := s.GetHostEnrollmentCode(ctx, name)
	if err != nil {
		return nil, "", err
	}
	return c, rawCode, nil
}

// RevokeHostEnrollmentCode revokes name so no further join requests can use it.
// Hosts already accepted through name are unaffected.
func (s *Store) RevokeHostEnrollmentCode(ctx context.Context, name string) (*HostEnrollmentCode, error) {
	now := time.Now().UTC()
	err := s.write(ctx, "revoke host enrollment code "+name, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"UPDATE `host_enrollment_code` SET `revoked_at` = COALESCE(`revoked_at`, ?), `code_value` = '' WHERE `name` = ?",
			now, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var c int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `host_enrollment_code` WHERE `name` = ?", name).Scan(&c); err != nil {
				return err
			}
			if c == 0 {
				return ErrEnrollmentCodeNotFound
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetHostEnrollmentCode(ctx, name)
}

// HostJoinRequest is one pending (or accepted-awaiting-claim) host handshake.
type HostJoinRequest struct {
	ID               string            `json:"id"`
	EnrollmentCode   string            `json:"enrollment_code,omitempty"`
	RekeyHostd       string            `json:"rekey_hostd,omitempty"`
	PublicKey        string            `json:"public_key"`
	KeyFingerprint   string            `json:"key_fingerprint"`
	ServerNonce      string            `json:"-"`
	PairingCode      string            `json:"pairing_code,omitempty"`
	Hostname         string            `json:"hostname"`
	OS               string            `json:"os"`
	Arch             string            `json:"arch"`
	CLIs             []string          `json:"clis,omitempty"`
	Version          string            `json:"version,omitempty"`
	LocalUser        string            `json:"local_user,omitempty"`
	EnrolledBy       string            `json:"enrolled_by"`
	DefaultLabels    map[string]string `json:"default_labels,omitempty"`
	DefaultCapacity  int               `json:"default_capacity,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	ExpiresAt        time.Time         `json:"expires_at"`
	AcceptedAt       *time.Time        `json:"accepted_at,omitempty"`
	AcceptedBy       string            `json:"accepted_by,omitempty"`
	AcceptedName     string            `json:"accepted_name,omitempty"`
	AcceptedLabels   map[string]string `json:"accepted_labels,omitempty"`
	AcceptedCapacity int               `json:"accepted_capacity,omitempty"`
}

const hostJoinRequestColumns = "`id`,`enrollment_code`,`rekey_hostd`,`public_key`,`key_fingerprint`,`server_nonce`," +
	"`hostname`,`os`,`arch`,`clis`,`version`,`local_user`,`enrolled_by`,`default_labels`,`default_capacity`," +
	"`created_at`,`expires_at`,`accepted_at`,`accepted_by`,`accepted_name`,`accepted_labels`,`accepted_capacity`"

func scanHostJoinRequest(scan func(...any) error) (HostJoinRequest, error) {
	var r HostJoinRequest
	var clis, defLabels, accLabels string
	var accepted sql.NullTime
	if err := scan(
		&r.ID, &r.EnrollmentCode, &r.RekeyHostd, &r.PublicKey, &r.KeyFingerprint, &r.ServerNonce,
		&r.Hostname, &r.OS, &r.Arch, &clis, &r.Version, &r.LocalUser, &r.EnrolledBy, &defLabels, &r.DefaultCapacity,
		&r.CreatedAt, &r.ExpiresAt, &accepted, &r.AcceptedBy, &r.AcceptedName, &accLabels, &r.AcceptedCapacity,
	); err != nil {
		return HostJoinRequest{}, err
	}
	r.CreatedAt = r.CreatedAt.UTC()
	r.ExpiresAt = r.ExpiresAt.UTC()
	if accepted.Valid {
		t := accepted.Time.UTC()
		r.AcceptedAt = &t
	}
	r.CLIs = decodeHostdCSV(clis)
	r.DefaultLabels, _ = ParseHostdLabels(defLabels)
	r.AcceptedLabels, _ = ParseHostdLabels(accLabels)
	return r, nil
}

func pruneExpiredHostJoinRequestsTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM `host_join_request` WHERE `expires_at` <= ?", now.UTC())
	return err
}

// PruneExpiredHostJoinRequests deletes expired join requests so nothing is
// left behind once the 24h handshake window passes.
func (s *Store) PruneExpiredHostJoinRequests(ctx context.Context, now time.Time) error {
	return s.write(ctx, "prune expired host join requests", func(tx *sql.Tx) error {
		return pruneExpiredHostJoinRequestsTx(ctx, tx, now)
	})
}

// CountPendingHostJoinRequests counts pending requests filed since `since` for
// rate-limiting per enrollment code and per enrolling identity.
func (s *Store) CountPendingHostJoinRequests(ctx context.Context, codeName, enrolledBy string, since time.Time) (byCode, byIdentity int, err error) {
	since = since.UTC()
	if codeName != "" {
		if err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM `host_join_request` WHERE `enrollment_code` = ? AND `created_at` >= ?",
			codeName, since).Scan(&byCode); err != nil {
			return 0, 0, err
		}
	}
	if enrolledBy != "" {
		if err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM `host_join_request` WHERE `enrolled_by` = ? AND `created_at` >= ?",
			strings.ToLower(strings.TrimSpace(enrolledBy)), since).Scan(&byIdentity); err != nil {
			return 0, 0, err
		}
	}
	return byCode, byIdentity, nil
}

// CreateHostJoinRequest files a pending 24-hour join request, consuming one
// use of rawCode (or attaching to an existing unrevoked host when RekeyHostd
// is set).
func (s *Store) CreateHostJoinRequest(ctx context.Context, r HostJoinRequest, rawCode string, now time.Time) (*HostJoinRequest, error) {
	now = now.UTC()
	idBytes := make([]byte, 12)
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	r.ID = "hjr_" + hex.EncodeToString(idBytes)
	r.ServerNonce = hex.EncodeToString(nonceBytes)
	r.EnrolledBy = strings.ToLower(strings.TrimSpace(r.EnrolledBy))
	r.CreatedAt = now
	r.ExpiresAt = now.Add(HostJoinRequestTTL)
	r.AcceptedAt = nil

	err := s.write(ctx, "create host join request "+r.ID, func(tx *sql.Tx) error {
		if err := pruneExpiredHostJoinRequestsTx(ctx, tx, now); err != nil {
			return err
		}
		if r.RekeyHostd != "" {
			if r.RekeyHostd == LocalHostd {
				return fmt.Errorf("%q is the co-located host and does not rekey", LocalHostd)
			}
			h, err := scanHostd(tx.QueryRowContext(ctx, "SELECT "+hostdColumns+" FROM `hostd` WHERE `name` = ?", r.RekeyHostd).Scan)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrHostdNotFound
			}
			if err != nil {
				return err
			}
			if h.Revoked() {
				return ErrHostdRevoked
			}
			if h.KeyBound() && !SameHostPublicKey(h.PublicKey, r.PublicKey) {
				return ErrRekeyKeyMismatch
			}
			r.DefaultLabels = h.Labels
			r.DefaultCapacity = h.Capacity
			r.EnrollmentCode = h.EnrollmentCode
		} else {
			c, _, err := scanHostEnrollmentCode(tx.QueryRowContext(ctx,
				"SELECT "+hostEnrollmentCodeColumns+" FROM `host_enrollment_code` WHERE `code_hash` = ?",
				HashHostEnrollmentCode(rawCode)).Scan)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrEnrollmentCodeNotFound
			}
			if err != nil {
				return err
			}
			if c.RevokedAt != nil {
				return ErrEnrollmentCodeRevoked
			}
			if c.ExpiresAt != nil && !c.ExpiresAt.After(now) {
				return ErrEnrollmentCodeExpired
			}
			if c.MaxUses > 0 && c.Uses >= c.MaxUses {
				return ErrEnrollmentCodeExhausted
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE `host_enrollment_code` SET `uses` = `uses` + 1 WHERE `name` = ?", c.Name); err != nil {
				return err
			}
			r.EnrollmentCode = c.Name
			r.DefaultLabels = c.DefaultLabels
			r.DefaultCapacity = c.DefaultCapacity
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `host_join_request` ("+hostJoinRequestColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, '', '', '', 0)",
			r.ID, r.EnrollmentCode, r.RekeyHostd, r.PublicKey, r.KeyFingerprint, r.ServerNonce,
			r.Hostname, r.OS, r.Arch, encodeHostdCSV(r.CLIs), r.Version, r.LocalUser, r.EnrolledBy,
			encodeHostdLabels(r.DefaultLabels), r.DefaultCapacity, r.CreatedAt, r.ExpiresAt)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListPendingHostJoinRequests prunes expired requests and returns all pending
// (unaccepted) join requests, newest first.
func (s *Store) ListPendingHostJoinRequests(ctx context.Context, now time.Time) ([]HostJoinRequest, error) {
	if err := s.PruneExpiredHostJoinRequests(ctx, now); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+hostJoinRequestColumns+" FROM `host_join_request` WHERE `accepted_at` IS NULL ORDER BY `created_at` DESC")
	if err != nil {
		return nil, fmt.Errorf("listing pending host join requests: %w", err)
	}
	defer rows.Close()
	var out []HostJoinRequest
	for rows.Next() {
		r, err := scanHostJoinRequest(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetHostJoinRequest prunes expired requests and returns id's row, or nil.
func (s *Store) GetHostJoinRequest(ctx context.Context, id string, now time.Time) (*HostJoinRequest, error) {
	if err := s.PruneExpiredHostJoinRequests(ctx, now); err != nil {
		return nil, err
	}
	r, err := scanHostJoinRequest(s.db.QueryRowContext(ctx,
		"SELECT "+hostJoinRequestColumns+" FROM `host_join_request` WHERE `id` = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// AcceptHostJoinOptions configures the accepted host's name, labels and capacity.
type AcceptHostJoinOptions struct {
	Name       string
	Labels     map[string]string
	HasLabels  bool
	Capacity   *int
	AcceptedBy string
}

// AcceptHostJoinRequest accepts a pending join request, creating (or rekeying)
// its key-bound hostd row so the waiting host's next signed poll claims a
// 30-day credential.
func (s *Store) AcceptHostJoinRequest(ctx context.Context, id string, opts AcceptHostJoinOptions, now time.Time) (*Hostd, *HostJoinRequest, error) {
	now = now.UTC()
	var outReq HostJoinRequest
	var acceptedName string

	err := s.write(ctx, "accept host join request "+id, func(tx *sql.Tx) error {
		if err := pruneExpiredHostJoinRequestsTx(ctx, tx, now); err != nil {
			return err
		}
		r, err := scanHostJoinRequest(tx.QueryRowContext(ctx,
			"SELECT "+hostJoinRequestColumns+" FROM `host_join_request` WHERE `id` = ?", id).Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrJoinRequestNotFound
		}
		if err != nil {
			return err
		}
		if r.AcceptedAt != nil {
			return ErrJoinRequestAlreadyAccepted
		}

		labels := r.DefaultLabels
		if opts.HasLabels {
			labels = opts.Labels
		}
		if err := ValidateHostdLabels(labels); err != nil {
			return err
		}
		capacity := r.DefaultCapacity
		if opts.Capacity != nil {
			capacity = *opts.Capacity
		}
		if capacity < 0 {
			return fmt.Errorf("host capacity %d may not be negative", capacity)
		}
		owner, err := NormalizeHostdOwner(r.EnrolledBy)
		if err != nil {
			owner = ""
		}

		if r.RekeyHostd != "" {
			name := r.RekeyHostd
			if opts.Name != "" && opts.Name != name {
				return fmt.Errorf("rekey request for %q cannot change host name to %q", name, opts.Name)
			}
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
			// The host may have become key-bound since this request was
			// filed (another rekey accepted first): never move a key-bound
			// host to a key other than its own.
			if h.KeyBound() && !SameHostPublicKey(h.PublicKey, r.PublicKey) {
				return ErrRekeyKeyMismatch
			}
			if owner == "" {
				owner = h.Owner
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE `hostd` SET `owner` = ?, `labels` = ?, `public_key` = ?, `key_fingerprint` = ?, `capacity` = ?, `hostname` = ?, `os` = ?, `arch` = ?, `clis` = ?, `version` = ?, `local_user` = ? WHERE `name` = ?",
				owner, encodeHostdLabels(labels), r.PublicKey, r.KeyFingerprint, capacity,
				r.Hostname, r.OS, r.Arch, encodeHostdCSV(r.CLIs), r.Version, r.LocalUser, name); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE `hostd_token` SET `revoked_at` = ? WHERE `hostd` = ? AND `revoked_at` IS NULL", now, name); err != nil {
				return err
			}
			acceptedName = name
		} else {
			name := strings.TrimSpace(opts.Name)
			if name == "" {
				name = SanitizeHostdName(r.Hostname)
			}
			if name == LocalHostd {
				return fmt.Errorf("%q is the co-located host's reserved name", LocalHostd)
			}
			if err := ValidateHostdName(name); err != nil {
				return err
			}
			var existing int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `hostd` WHERE `name` = ?", name).Scan(&existing); err != nil {
				return err
			}
			if existing > 0 {
				return ErrHostdExists
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `hostd` ("+hostdColumns+") VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)",
				name, owner, encodeHostdLabels(labels), opts.AcceptedBy, now,
				r.PublicKey, r.KeyFingerprint, r.EnrollmentCode, capacity,
				r.Hostname, r.OS, r.Arch, encodeHostdCSV(r.CLIs), r.Version, r.LocalUser); err != nil {
				return err
			}
			acceptedName = name
		}

		if _, err := tx.ExecContext(ctx,
			"UPDATE `host_join_request` SET `accepted_at` = ?, `accepted_by` = ?, `accepted_name` = ?, `accepted_labels` = ?, `accepted_capacity` = ? WHERE `id` = ?",
			now, opts.AcceptedBy, acceptedName, encodeHostdLabels(labels), capacity, id); err != nil {
			return err
		}
		r.AcceptedAt = &now
		r.AcceptedBy = opts.AcceptedBy
		r.AcceptedName = acceptedName
		r.AcceptedLabels = labels
		r.AcceptedCapacity = capacity
		outReq = r
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	h, err := s.GetHostd(ctx, acceptedName)
	if err != nil {
		return nil, nil, err
	}
	return h, &outReq, nil
}

// RejectHostJoinRequest rejects a pending join request and deletes it from the
// database so rejected requests leave nothing behind.
func (s *Store) RejectHostJoinRequest(ctx context.Context, id string, now time.Time) (*HostJoinRequest, error) {
	var out HostJoinRequest
	err := s.write(ctx, "reject host join request "+id, func(tx *sql.Tx) error {
		if err := pruneExpiredHostJoinRequestsTx(ctx, tx, now); err != nil {
			return err
		}
		r, err := scanHostJoinRequest(tx.QueryRowContext(ctx,
			"SELECT "+hostJoinRequestColumns+" FROM `host_join_request` WHERE `id` = ?", id).Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrJoinRequestNotFound
		}
		if err != nil {
			return err
		}
		if r.AcceptedAt != nil {
			return ErrJoinRequestAlreadyAccepted
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM `host_join_request` WHERE `id` = ?", id); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ClaimAcceptedHostJoinRequest mints the 30-day key-bound credential once for
// an accepted join request and deletes the handshake row. If the request is
// still awaiting owner approval, accepted is false and token is "".
func (s *Store) ClaimAcceptedHostJoinRequest(ctx context.Context, id string, now time.Time) (req *HostJoinRequest, host *Hostd, token string, expiresAt time.Time, accepted bool, err error) {
	now = now.UTC()
	var outReq HostJoinRequest
	var mintedToken string
	var mintedExp time.Time
	var hostName string

	err = s.write(ctx, "claim host join request "+id, func(tx *sql.Tx) error {
		if err := pruneExpiredHostJoinRequestsTx(ctx, tx, now); err != nil {
			return err
		}
		r, err := scanHostJoinRequest(tx.QueryRowContext(ctx,
			"SELECT "+hostJoinRequestColumns+" FROM `host_join_request` WHERE `id` = ?", id).Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrJoinRequestNotFound
		}
		if err != nil {
			return err
		}
		outReq = r
		if r.AcceptedAt == nil {
			return nil
		}
		h, err := scanHostd(tx.QueryRowContext(ctx,
			"SELECT "+hostdColumns+" FROM `hostd` WHERE `name` = ?", r.AcceptedName).Scan)
		if errors.Is(err, sql.ErrNoRows) {
			_, _ = tx.ExecContext(ctx, "DELETE FROM `host_join_request` WHERE `id` = ?", id)
			return ErrHostdNotFound
		}
		if err != nil {
			return err
		}
		if h.Revoked() {
			_, _ = tx.ExecContext(ctx, "DELETE FROM `host_join_request` WHERE `id` = ?", id)
			return ErrHostdRevoked
		}
		tok, err := GenerateHostdToken()
		if err != nil {
			return err
		}
		exp := now.Add(HostdCredentialTTL)
		if err := insertHostdToken(ctx, tx, h.Name, tok, now, &exp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM `host_join_request` WHERE `id` = ?", id); err != nil {
			return err
		}
		mintedToken = tok
		mintedExp = exp
		hostName = h.Name
		accepted = true
		return nil
	})
	if err != nil {
		return nil, nil, "", time.Time{}, false, err
	}
	if !accepted {
		return &outReq, nil, "", time.Time{}, false, nil
	}
	h, err := s.GetHostd(ctx, hostName)
	if err != nil {
		return nil, nil, "", time.Time{}, false, err
	}
	return &outReq, h, mintedToken, mintedExp, true, nil
}
