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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/ai-on-gke/grain/pkg/registry/api"
)

// Visibility controls which callers can view or access an entry.
type Visibility string

const (
	VisibilityOwner  Visibility = "owner"
	VisibilityShared Visibility = "shared"
	VisibilityPublic Visibility = "public"
)

// ScopeClaim represents a declared or reported scope claim stored in `registry.entry_scopes`.
type ScopeClaim struct {
	Repo       string `json:"repo"`
	PathPrefix string `json:"path_prefix,omitempty"`
	Primary    bool   `json:"primary,omitempty"`
	Branch     string `json:"branch,omitempty"`
}

var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("registry store: not found")

	// ErrConflict is returned when an optimistic lock (`version`), unique name,
	// or session_epoch check fails.
	ErrConflict = errors.New("registry store: conflict")

	// ErrNameReserved is returned when a name is still within its 30-day tombstone reservation window for another owner.
	ErrNameReserved = errors.New("registry store: entry name is reserved by a recently deleted entry")

	// ErrPrimaryScopeConflict is returned when two entries claim primary_claim = TRUE
	// on overlapping (repo, path_prefix) scopes.
	ErrPrimaryScopeConflict = errors.New("registry store: overlapping primary scope claim")

	// ErrQuotaExceeded is returned when an owner exceeds their instance units or connected entry cap.
	ErrQuotaExceeded = errors.New("registry store: quota exceeded")

	// ErrInvalidCredential is returned when a `grc_` token is unknown or revoked.
	ErrInvalidCredential = errors.New("registry store: invalid or revoked entry credential")
)

// Thresholds configures runtime status derivation from heartbeat and replica timestamps.
type Thresholds struct {
	DegradedAfter     time.Duration
	OfflineAfter      time.Duration
	ReplicaTTL        time.Duration
	DefaultUnitsQuota int
	DefaultConnQuota  int
	ReservationPeriod time.Duration
}

// DefaultThresholds returns the standard v1 registry thresholds.
func DefaultThresholds() Thresholds {
	return Thresholds{
		DegradedAfter:     45 * time.Second,
		OfflineAfter:      90 * time.Second,
		ReplicaTTL:        15 * time.Second,
		DefaultUnitsQuota: 2,
		DefaultConnQuota:  20,
		ReservationPeriod: 720 * time.Hour,
	}
}

// ShapeRow represents a row in `registry.shapes`.
type ShapeRow struct {
	Name         string `json:"name"`
	MachineType  string `json:"machine_type"`
	BootDiskGB   int    `json:"boot_disk_gb"`
	BootDiskIOPS int    `json:"boot_disk_iops,omitempty"`
	QuotaUnits   int    `json:"quota_units"`
	Description  string `json:"description,omitempty"`
}

// SQLInstanceRow represents a row in `registry.sql_instances`.
type SQLInstanceRow struct {
	Name           string `json:"name"`
	ConnectionName string `json:"connection_name"`
	PrivateIP      string `json:"private_ip"`
	MaxDatabases   int    `json:"max_databases"`
	Draining       bool   `json:"draining"`
}

// OwnerRow represents a row in `registry.owners`.
type OwnerRow struct {
	Email      string    `json:"email"`
	Disabled   bool      `json:"disabled"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// QuotaRow represents an owner's effective quota limits and current usage.
type QuotaRow struct {
	OwnerEmail        string    `json:"owner_email"`
	MaxInstanceUnits  int       `json:"max_instance_units"`
	UsedInstanceUnits int       `json:"used_instance_units"`
	MaxConnected      int       `json:"max_connected"`
	UsedConnected     int       `json:"used_connected"`
	UpdatedBy         string    `json:"updated_by,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ScopeRow represents a row in `registry.entry_scopes`.
type ScopeRow struct {
	EntryID      string `json:"entry_id"`
	Repo         string `json:"repo"`
	PathPrefix   string `json:"path_prefix"`
	Source       string `json:"source"`
	PrimaryClaim bool   `json:"primary_claim"`
	Branch       string `json:"branch,omitempty"`
}

// EntryRow represents a catalog entry in `registry.entries` along with its
// combined scopes and active tunnel state.
type EntryRow struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Kind           api.EntryKind  `json:"kind"`
	OwnerEmail     string         `json:"owner_email"`
	OwnerGroup     string         `json:"owner_group,omitempty"`
	Listed         bool           `json:"listed"`
	Visibility     Visibility     `json:"visibility"`
	AccessList     []string       `json:"access_list"`
	DisplayName    string         `json:"display_name,omitempty"`
	Purpose        string         `json:"purpose,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
	LifecycleState api.EntryState `json:"lifecycle_state"`
	StatusReason   string         `json:"status_reason,omitempty"`
	Version        int64          `json:"version"`

	Shape             string     `json:"shape,omitempty"`
	Zone              string     `json:"zone,omitempty"`
	GCEInstanceID     string     `json:"gce_instance_id,omitempty"`
	InternalIP        string     `json:"internal_ip,omitempty"`
	BackendURL        string     `json:"backend_url,omitempty"`
	ServiceAccount    string     `json:"service_account,omitempty"`
	SQLInstance       string     `json:"sql_instance,omitempty"`
	DBName            string     `json:"db_name,omitempty"`
	DBPurgeAfter      *time.Time `json:"db_purge_after,omitempty"`
	SecretPrefix      string     `json:"secret_prefix,omitempty"`
	ConfigRepo        string     `json:"config_repo,omitempty"`
	SSHGrantExpiresAt *time.Time `json:"ssh_grant_expires_at,omitempty"`
	SSHGrantedTo      string     `json:"ssh_granted_to,omitempty"`

	GrainVersion         string     `json:"grain_version,omitempty"`
	ActiveTasks          int        `json:"active_tasks"`
	PendingInbox         int        `json:"pending_inbox"`
	LastHeartbeatAt      *time.Time `json:"last_heartbeat_at,omitempty"`
	SessionEpoch         int64      `json:"session_epoch"`
	ConnectedPrincipal   string     `json:"connected_principal,omitempty"`
	HostKind             string     `json:"host_kind,omitempty"`
	LastDisconnectAt     *time.Time `json:"last_disconnect_at,omitempty"`
	LastDisconnectReason string     `json:"last_disconnect_reason,omitempty"`
	OwnerDisabled        bool       `json:"owner_disabled,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	PurgeAfter *time.Time `json:"purge_after,omitempty"`

	Scopes []ScopeRow        `json:"scopes,omitempty"`
	Tunnel *TunnelSessionRow `json:"tunnel,omitempty"`
}

// TunnelSessionRow represents an active `grain connect` session in `registry.tunnel_sessions`.
type TunnelSessionRow struct {
	EntryID         string    `json:"entry_id"`
	EntryName       string    `json:"entry_name,omitempty"`
	ReplicaID       string    `json:"replica_id"`
	PeerAddr        string    `json:"peer_addr,omitempty"`
	ReplicaLastSeen time.Time `json:"replica_last_seen"`
	SessionEpoch    int64     `json:"session_epoch"`
	RemoteAddr      string    `json:"remote_addr"`
	ConnectedAt     time.Time `json:"connected_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
	RTTMs           int       `json:"rtt_ms,omitempty"`
	CredID          string    `json:"cred_id,omitempty"`
}

// ReplicaRow represents a registry replica in `registry.replicas`.
type ReplicaRow struct {
	ReplicaID   string    `json:"replica_id"`
	PeerAddr    string    `json:"peer_addr"`
	StartedAt   time.Time `json:"started_at"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
}

// AuditEntry represents a row in `registry.audit_log`.
type AuditEntry struct {
	ID         int64          `json:"id"`
	At         time.Time      `json:"at"`
	ActorEmail string         `json:"actor_email"`
	Action     string         `json:"action"`
	EntryID    string         `json:"entry_id,omitempty"`
	EntryName  string         `json:"entry_name,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
}

// Store manages the `registry` PostgreSQL schema.
type Store struct {
	db         *sql.DB
	thresholds Thresholds
	nowFn      func() time.Time
}

// Open opens a PostgreSQL connection pool, verifies connectivity, and runs migrations.
func Open(ctx context.Context, databaseURL string, thresholds Thresholds) (*Store, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("opening postgres connection: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	s, err := New(ctx, db, thresholds)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// New wraps an existing *sql.DB and runs all pending schema migrations.
func New(ctx context.Context, db *sql.DB, thresholds Thresholds) (*Store, error) {
	def := DefaultThresholds()
	if thresholds.DegradedAfter <= 0 {
		thresholds.DegradedAfter = def.DegradedAfter
	}
	if thresholds.OfflineAfter <= 0 {
		thresholds.OfflineAfter = def.OfflineAfter
	}
	if thresholds.ReplicaTTL <= 0 {
		thresholds.ReplicaTTL = def.ReplicaTTL
	}
	if thresholds.DefaultUnitsQuota <= 0 {
		thresholds.DefaultUnitsQuota = def.DefaultUnitsQuota
	}
	if thresholds.DefaultConnQuota <= 0 {
		thresholds.DefaultConnQuota = def.DefaultConnQuota
	}
	if thresholds.ReservationPeriod <= 0 {
		thresholds.ReservationPeriod = def.ReservationPeriod
	}
	if err := Migrate(ctx, db); err != nil {
		return nil, err
	}
	return &Store{
		db:         db,
		thresholds: thresholds,
		nowFn:      time.Now,
	}, nil
}

// DB returns the underlying *sql.DB handle.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close closes the underlying *sql.DB handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// SetNow overrides the clock used by Store (for deterministic unit tests).
func (s *Store) SetNow(fn func() time.Time) {
	if fn != nil {
		s.nowFn = fn
	}
}

func (s *Store) now() time.Time {
	if s != nil && s.nowFn != nil {
		return s.nowFn().UTC()
	}
	return time.Now().UTC()
}

// SeedShapes upserts configured VM shapes into `registry.shapes`.
func (s *Store) SeedShapes(ctx context.Context, shapes []ShapeRow) error {
	for _, sh := range shapes {
		_, err := s.db.ExecContext(ctx, `
INSERT INTO registry.shapes (name, machine_type, boot_disk_gb, boot_disk_iops, quota_units, description)
VALUES ($1, $2, $3, NULLIF($4, 0), $5, $6)
ON CONFLICT (name) DO UPDATE SET
    machine_type   = EXCLUDED.machine_type,
    boot_disk_gb   = EXCLUDED.boot_disk_gb,
    boot_disk_iops = EXCLUDED.boot_disk_iops,
    quota_units    = EXCLUDED.quota_units,
    description    = EXCLUDED.description`,
			sh.Name, sh.MachineType, sh.BootDiskGB, sh.BootDiskIOPS, sh.QuotaUnits, sh.Description)
		if err != nil {
			return fmt.Errorf("seeding shape %q: %w", sh.Name, err)
		}
	}
	return nil
}

// ListShapes returns all shapes ordered by quota_units, boot_disk_gb, name.
func (s *Store) ListShapes(ctx context.Context) ([]ShapeRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT name, machine_type, boot_disk_gb, COALESCE(boot_disk_iops, 0), quota_units, description
FROM registry.shapes
ORDER BY quota_units ASC, boot_disk_gb ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShapeRow
	for rows.Next() {
		var r ShapeRow
		if err := rows.Scan(&r.Name, &r.MachineType, &r.BootDiskGB, &r.BootDiskIOPS, &r.QuotaUnits, &r.Description); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SeedSQLInstances upserts configured Cloud SQL instances into `registry.sql_instances`.
func (s *Store) SeedSQLInstances(ctx context.Context, instances []SQLInstanceRow) error {
	for _, inst := range instances {
		_, err := s.db.ExecContext(ctx, `
INSERT INTO registry.sql_instances (name, connection_name, private_ip, max_databases, draining)
VALUES ($1, $2, $3::inet, $4, $5)
ON CONFLICT (name) DO UPDATE SET
    connection_name = EXCLUDED.connection_name,
    private_ip      = EXCLUDED.private_ip,
    max_databases   = EXCLUDED.max_databases`,
			inst.Name, inst.ConnectionName, inst.PrivateIP, inst.MaxDatabases, inst.Draining)
		if err != nil {
			return fmt.Errorf("seeding sql_instance %q: %w", inst.Name, err)
		}
	}
	return nil
}

// UpsertOwner ensures an owner record exists and updates `last_seen_at`.
func (s *Store) UpsertOwner(ctx context.Context, email string) (*OwnerRow, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, errors.New("owner email is required")
	}
	now := s.now()
	var row OwnerRow
	err := s.db.QueryRowContext(ctx, `
INSERT INTO registry.owners (email, created_at, last_seen_at)
VALUES ($1, $2, $2)
ON CONFLICT (email) DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at
RETURNING email, disabled, created_at, last_seen_at`, email, now).
		Scan(&row.Email, &row.Disabled, &row.CreatedAt, &row.LastSeenAt)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// IsOwnerDisabled checks whether an owner exists and has `disabled = TRUE`.
func (s *Store) IsOwnerDisabled(ctx context.Context, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var disabled bool
	err := s.db.QueryRowContext(ctx, `SELECT disabled FROM registry.owners WHERE email = $1`, email).Scan(&disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return disabled, nil
}

// SetOwnerDisabled updates `disabled` on `registry.owners` (creating the owner row if needed).
// When disabling an owner, any active tunnel sessions for entries owned by `email` are
// terminated and `NOTIFY registry_entries` / `NOTIFY registry_tunnels` are emitted.
func (s *Store) SetOwnerDisabled(ctx context.Context, email string, disabled bool) error {
	email = strings.ToLower(strings.TrimSpace(email))
	now := s.now()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO registry.owners (email, disabled, created_at, last_seen_at)
VALUES ($1, $2, $3, $3)
ON CONFLICT (email) DO UPDATE SET disabled = EXCLUDED.disabled`, email, disabled, now)
	if err != nil {
		return err
	}
	if disabled {
		_, _ = s.db.ExecContext(ctx, `
DELETE FROM registry.tunnel_sessions
WHERE entry_id IN (SELECT id FROM registry.entries WHERE owner_email = $1 AND deleted_at IS NULL)`, email)
		_, _ = s.db.ExecContext(ctx, `
UPDATE registry.entries
SET last_disconnect_at     = $1,
    last_disconnect_reason = 'owner disabled',
    updated_at             = $1
WHERE owner_email = $2 AND deleted_at IS NULL`, now, email)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM registry.entries WHERE owner_email = $1 AND deleted_at IS NULL`, email)
	if err == nil {
		var names []string
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				names = append(names, n)
			}
		}
		_ = rows.Close()
		for _, n := range names {
			s.notify(ctx, "registry_entries", n)
			s.notify(ctx, "registry_tunnels", n)
		}
	}
	return nil
}

func (s *Store) notify(ctx context.Context, channel, payload string) {
	if s == nil || s.db == nil || channel == "" {
		return
	}
	_, _ = s.db.ExecContext(ctx, `SELECT pg_notify($1, $2)`, channel, payload)
}

// SetQuota upserts a per-owner quota override in `registry.quotas`.
func (s *Store) SetQuota(ctx context.Context, ownerEmail string, maxInstanceUnits, maxConnected int, updatedBy string) error {
	ownerEmail = strings.ToLower(strings.TrimSpace(ownerEmail))
	if _, err := s.UpsertOwner(ctx, ownerEmail); err != nil {
		return err
	}
	now := s.now()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO registry.quotas (owner_email, max_instance_units, max_connected, updated_by, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (owner_email) DO UPDATE SET
    max_instance_units = EXCLUDED.max_instance_units,
    max_connected      = EXCLUDED.max_connected,
    updated_by         = EXCLUDED.updated_by,
    updated_at         = EXCLUDED.updated_at`,
		ownerEmail, maxInstanceUnits, maxConnected, updatedBy, now)
	return err
}

// GetQuota returns the effective quota limits and current active usage for ownerEmail.
func (s *Store) GetQuota(ctx context.Context, ownerEmail string) (*QuotaRow, error) {
	ownerEmail = strings.ToLower(strings.TrimSpace(ownerEmail))
	q := &QuotaRow{
		OwnerEmail:       ownerEmail,
		MaxInstanceUnits: s.thresholds.DefaultUnitsQuota,
		MaxConnected:     s.thresholds.DefaultConnQuota,
		UpdatedAt:        s.now(),
	}
	var updatedBy sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT max_instance_units, max_connected, updated_by, updated_at
FROM registry.quotas
WHERE owner_email = $1`, ownerEmail).
		Scan(&q.MaxInstanceUnits, &q.MaxConnected, &updatedBy, &q.UpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	q.UpdatedBy = updatedBy.String

	err = s.db.QueryRowContext(ctx, `
SELECT
    COALESCE(SUM(CASE WHEN e.kind = 'instance' THEN COALESCE(sh.quota_units, 1) ELSE 0 END), 0),
    COALESCE(SUM(CASE WHEN e.kind = 'connected' THEN 1 ELSE 0 END), 0)
FROM registry.entries e
LEFT JOIN registry.shapes sh ON sh.name = e.shape
WHERE e.owner_email = $1 AND e.deleted_at IS NULL AND e.lifecycle_state NOT IN ('deleted', 'DELETED')`,
		ownerEmail).Scan(&q.UsedInstanceUnits, &q.UsedConnected)
	if err != nil {
		return nil, err
	}
	return q, nil
}

// NameAvailability describes whether an entry name can be claimed by callerEmail.
type NameAvailability struct {
	Name       string         `json:"name"`
	State      string         `json:"state"`  // "available", "invalid", "reserved", "active"
	Status     api.NameStatus `json:"status"` // "free", "invalid", "reserved", "taken"
	OwnerEmail string         `json:"owner_email,omitempty"`
	PurgeAfter *time.Time     `json:"reserved_until,omitempty"`
	Reason     string         `json:"reason,omitempty"`
}

// CheckNameAvailability checks syntax, reserved names, active entries, and 30-day
// deleted-entry tombstones for `name`.
func (s *Store) CheckNameAvailability(ctx context.Context, name, callerEmail string) (*NameAvailability, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	callerEmail = strings.ToLower(strings.TrimSpace(callerEmail))
	if err := api.ValidateEntryName(name); err != nil {
		return &NameAvailability{
			Name:   name,
			State:  "invalid",
			Status: api.NameInvalid,
			Reason: err.Error(),
		}, nil
	}

	var activeOwner string
	err := s.db.QueryRowContext(ctx, `
SELECT owner_email FROM registry.entries
WHERE name = $1 AND deleted_at IS NULL`, name).Scan(&activeOwner)
	if err == nil {
		return &NameAvailability{
			Name:       name,
			State:      "active",
			Status:     api.NameTaken,
			OwnerEmail: activeOwner,
			Reason:     "entry name is currently active",
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	now := s.now()
	var reservedOwner string
	var purgeAfter time.Time
	err = s.db.QueryRowContext(ctx, `
SELECT owner_email, purge_after
FROM registry.entries
WHERE name = $1 AND deleted_at IS NOT NULL AND purge_after > $2
ORDER BY purge_after DESC
LIMIT 1`, name, now).Scan(&reservedOwner, &purgeAfter)
	if err == nil {
		if callerEmail != "" && strings.EqualFold(reservedOwner, callerEmail) {
			return &NameAvailability{
				Name:   name,
				State:  "available",
				Status: api.NameFree,
			}, nil
		}
		return &NameAvailability{
			Name:       name,
			State:      "reserved",
			Status:     api.NameReserved,
			OwnerEmail: reservedOwner,
			PurgeAfter: &purgeAfter,
			Reason:     "entry name is within the 30-day tombstone reservation window",
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	return &NameAvailability{
		Name:   name,
		State:  "available",
		Status: api.NameFree,
	}, nil
}

// CreateEntryInput holds parameters for creating an `instance` or `connected` entry.
type CreateEntryInput struct {
	Name               string
	Kind               api.EntryKind
	OwnerEmail         string
	OwnerGroup         string
	ConnectedPrincipal string
	HostKind           string
	Listed             *bool
	Visibility         Visibility
	AccessList         []string
	DisplayName        string
	Purpose            string
	Tags               []string
	LifecycleState     api.EntryState
	Shape              string
	Zone               string
	BackendURL         string
	DeclaredScopes     []ScopeClaim
}

// CreateEntry validates name availability, quotas, backend URL scheme, and primary
// scope claims, then inserts a new row into `registry.entries`.
func (s *Store) CreateEntry(ctx context.Context, in CreateEntryInput) (*EntryRow, error) {
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if err := api.ValidateEntryName(name); err != nil {
		return nil, err
	}
	if !in.Kind.Valid() {
		return nil, fmt.Errorf("unsupported entry kind %q (only \"instance\" and \"connected\" are supported in v1)", in.Kind)
	}
	ownerEmail := strings.ToLower(strings.TrimSpace(in.OwnerEmail))
	if ownerEmail == "" {
		return nil, errors.New("owner_email is required")
	}
	vis := in.Visibility
	if vis == "" {
		vis = VisibilityOwner
	}
	if vis != VisibilityOwner && vis != VisibilityShared && vis != VisibilityPublic {
		return nil, fmt.Errorf("invalid visibility %q", vis)
	}
	listed := true
	if in.Listed != nil {
		listed = *in.Listed
	}
	state := in.LifecycleState
	if state == "" {
		if in.Kind == api.KindInstance {
			state = api.StateCreating
		} else {
			state = api.StateRunning
		}
	}
	if in.BackendURL != "" {
		if err := validateBackendURL(in.BackendURL); err != nil {
			return nil, err
		}
	}

	if _, err := s.UpsertOwner(ctx, ownerEmail); err != nil {
		return nil, err
	}

	avail, err := s.CheckNameAvailability(ctx, name, ownerEmail)
	if err != nil {
		return nil, err
	}
	switch avail.State {
	case "available":
	case "active":
		return nil, fmt.Errorf("%w: entry %q already exists", ErrConflict, name)
	case "reserved":
		return nil, fmt.Errorf("%w: entry %q is reserved until %s", ErrNameReserved, name, avail.PurgeAfter.Format(time.RFC3339))
	default:
		return nil, fmt.Errorf("invalid entry name %q: %s", name, avail.Reason)
	}

	quota, err := s.GetQuota(ctx, ownerEmail)
	if err != nil {
		return nil, err
	}
	switch in.Kind {
	case api.KindInstance:
		units := 1
		if in.Shape != "" {
			err := s.db.QueryRowContext(ctx, `SELECT quota_units FROM registry.shapes WHERE name = $1`, in.Shape).Scan(&units)
			if err != nil {
				return nil, fmt.Errorf("lookup shape %q: %w", in.Shape, err)
			}
		}
		if quota.UsedInstanceUnits+units > quota.MaxInstanceUnits {
			return nil, fmt.Errorf("%w: instance quota units %d + %d exceeds limit %d", ErrQuotaExceeded, quota.UsedInstanceUnits, units, quota.MaxInstanceUnits)
		}
	case api.KindConnected:
		if quota.UsedConnected+1 > quota.MaxConnected {
			return nil, fmt.Errorf("%w: connected entries %d + 1 exceeds limit %d", ErrQuotaExceeded, quota.UsedConnected, quota.MaxConnected)
		}
	}

	accessList := in.AccessList
	if accessList == nil {
		accessList = []string{}
	}
	accessJSON, err := json.Marshal(accessList)
	if err != nil {
		return nil, err
	}
	tags := in.Tags
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return nil, err
	}

	id := uuid.NewString()
	now := s.now()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
INSERT INTO registry.entries (
    id, name, kind, owner_email, owner_group, listed, visibility, access_list,
    display_name, purpose, tags,
    lifecycle_state, version, shape, zone, backend_url,
    connected_principal, host_kind,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8::jsonb,
    $9, $10, $11::jsonb,
    $12, 1, NULLIF($13, ''), NULLIF($14, ''), NULLIF($15, ''),
    $16, $17,
    $18, $18
)`,
		id, name, string(in.Kind), ownerEmail, in.OwnerGroup, listed, string(vis), string(accessJSON),
		in.DisplayName, in.Purpose, string(tagsJSON),
		string(state), in.Shape, in.Zone, in.BackendURL,
		strings.TrimSpace(in.ConnectedPrincipal), strings.TrimSpace(in.HostKind), now)
	if err != nil {
		return nil, fmt.Errorf("inserting entry %q: %w", name, err)
	}

	if len(in.DeclaredScopes) > 0 {
		if err := replaceEntryScopesTx(ctx, tx, id, "declared", in.DeclaredScopes); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.notify(ctx, "registry_entries", name)
	return s.GetEntryByID(ctx, id)
}

func validateBackendURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid backend_url %q: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("backend_url %q must include scheme (http:// or https://) and host", raw)
	}
	return nil
}

// GetEntryByName looks up an active (non-deleted) entry by name.
func (s *Store) GetEntryByName(ctx context.Context, name string) (*EntryRow, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM registry.entries WHERE name = $1 AND deleted_at IS NULL`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.GetEntryByID(ctx, id)
}

// GetLatestDeletedEntryByName returns the most recently soft-deleted entry for `name`
// that is still within its retention window (`purge_after > now`).
func (s *Store) GetLatestDeletedEntryByName(ctx context.Context, name string) (*EntryRow, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	now := s.now()
	var id string
	err := s.db.QueryRowContext(ctx, `
SELECT id FROM registry.entries
WHERE name = $1 AND deleted_at IS NOT NULL AND (purge_after IS NULL OR purge_after > $2)
ORDER BY deleted_at DESC
LIMIT 1`, name, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.GetEntryByID(ctx, id)
}

// GetEntryByID looks up an entry by UUID (including soft-deleted rows).
func (s *Store) GetEntryByID(ctx context.Context, id string) (*EntryRow, error) {
	row := &EntryRow{}
	var (
		kindStr, visStr, stateStr string
		accessJSON, tagsJSON      []byte
		shape, zone, gceID        sql.NullString
		internalIP, backendURL    sql.NullString
		sa, sqlInst, dbName       sql.NullString
		secretPrefix, configRepo  sql.NullString
		sshGrantedTo, grainVer    sql.NullString
		dbPurge, sshExp, hbAt     sql.NullTime
		delAt, purgeAfter         sql.NullTime
		discAt                    sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, `
SELECT
    e.id, e.name, e.kind, e.owner_email, e.owner_group, e.listed, e.visibility, e.access_list,
    e.display_name, e.purpose, e.tags,
    e.lifecycle_state, e.status_reason, e.version,
    e.shape, e.zone, e.gce_instance_id, host(e.internal_ip), e.backend_url,
    e.service_account, e.sql_instance, e.db_name, e.db_purge_after,
    e.secret_prefix, e.config_repo, e.ssh_grant_expires_at, e.ssh_granted_to,
    e.grain_version, e.active_tasks, e.pending_inbox, e.last_heartbeat_at,
    e.session_epoch, e.connected_principal, e.host_kind,
    e.last_disconnect_at, e.last_disconnect_reason,
    COALESCE(o.disabled, FALSE),
    e.created_at, e.updated_at, e.deleted_at, e.purge_after
FROM registry.entries e
LEFT JOIN registry.owners o ON o.email = e.owner_email
WHERE e.id = $1`, id).Scan(
		&row.ID, &row.Name, &kindStr, &row.OwnerEmail, &row.OwnerGroup, &row.Listed, &visStr, &accessJSON,
		&row.DisplayName, &row.Purpose, &tagsJSON,
		&stateStr, &row.StatusReason, &row.Version,
		&shape, &zone, &gceID, &internalIP, &backendURL,
		&sa, &sqlInst, &dbName, &dbPurge,
		&secretPrefix, &configRepo, &sshExp, &sshGrantedTo,
		&grainVer, &row.ActiveTasks, &row.PendingInbox, &hbAt,
		&row.SessionEpoch, &row.ConnectedPrincipal, &row.HostKind,
		&discAt, &row.LastDisconnectReason,
		&row.OwnerDisabled,
		&row.CreatedAt, &row.UpdatedAt, &delAt, &purgeAfter,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	row.Kind = api.EntryKind(kindStr)
	row.Visibility = Visibility(visStr)
	row.LifecycleState = api.EntryState(stateStr)
	_ = json.Unmarshal(accessJSON, &row.AccessList)
	_ = json.Unmarshal(tagsJSON, &row.Tags)
	row.Shape = shape.String
	row.Zone = zone.String
	row.GCEInstanceID = gceID.String
	row.InternalIP = internalIP.String
	row.BackendURL = backendURL.String
	row.ServiceAccount = sa.String
	row.SQLInstance = sqlInst.String
	row.DBName = dbName.String
	row.SecretPrefix = secretPrefix.String
	row.ConfigRepo = configRepo.String
	row.SSHGrantedTo = sshGrantedTo.String
	row.GrainVersion = grainVer.String
	if dbPurge.Valid {
		t := dbPurge.Time
		row.DBPurgeAfter = &t
	}
	if sshExp.Valid {
		t := sshExp.Time
		row.SSHGrantExpiresAt = &t
	}
	if hbAt.Valid {
		t := hbAt.Time
		row.LastHeartbeatAt = &t
	}
	if discAt.Valid {
		t := discAt.Time
		row.LastDisconnectAt = &t
	}
	if delAt.Valid {
		t := delAt.Time
		row.DeletedAt = &t
	}
	if purgeAfter.Valid {
		t := purgeAfter.Time
		row.PurgeAfter = &t
	}

	scopes, err := s.ListEntryScopes(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	row.Scopes = scopes

	ts, err := s.GetActiveTunnelSession(ctx, row.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	row.Tunnel = ts
	return row, nil
}

// ListEntries returns all active (non-deleted) entries ordered by name.
func (s *Store) ListEntries(ctx context.Context) ([]*EntryRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id FROM registry.entries
WHERE deleted_at IS NULL
ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	out := make([]*EntryRow, 0, len(ids))
	for _, id := range ids {
		e, err := s.GetEntryByID(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// TransitionEntryState performs an optimistic-locked (`version`) lifecycle state transition.
func (s *Store) TransitionEntryState(ctx context.Context, entryID string, expectedVersion int64, nextState api.EntryState, statusReason string) (*EntryRow, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state = $1,
    status_reason   = $2,
    version         = version + 1,
    updated_at      = $3
WHERE id = $4 AND version = $5`,
		string(nextState), statusReason, now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch", ErrConflict, entryID, expectedVersion)
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
	}
	return out, err
}

// UpdateInstancePlacement records the provisioned VM's placement and scheme-qualified backend_url.
func (s *Store) UpdateInstancePlacement(ctx context.Context, entryID string, expectedVersion int64, gceInstanceID, internalIP, backendURL, serviceAccount, sqlInstance, dbName, secretPrefix string) (*EntryRow, error) {
	if backendURL != "" {
		if err := validateBackendURL(backendURL); err != nil {
			return nil, err
		}
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET gce_instance_id = NULLIF($1, ''),
    internal_ip     = CASE WHEN $2 = '' THEN NULL ELSE $2::inet END,
    backend_url     = NULLIF($3, ''),
    service_account = NULLIF($4, ''),
    sql_instance    = NULLIF($5, ''),
    db_name         = NULLIF($6, ''),
    secret_prefix   = NULLIF($7, ''),
    version         = version + 1,
    updated_at      = $8
WHERE id = $9 AND version = $10`,
		gceInstanceID, internalIP, backendURL, serviceAccount, sqlInstance, dbName, secretPrefix, now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch", ErrConflict, entryID, expectedVersion)
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
	}
	return out, err
}

// UpdateEntryAccess updates an entry's visibility and access_list under optimistic concurrency.
func (s *Store) UpdateEntryAccess(ctx context.Context, entryID string, expectedVersion int64, visibility Visibility, accessList []string) (*EntryRow, error) {
	if accessList == nil {
		accessList = []string{}
	}
	accessJSON, err := json.Marshal(accessList)
	if err != nil {
		return nil, err
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET visibility  = $1,
    access_list = $2::jsonb,
    version     = version + 1,
    updated_at  = $3
WHERE id = $4 AND version = $5`,
		string(visibility), string(accessJSON), now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch", ErrConflict, entryID, expectedVersion)
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
	}
	return out, err
}

// TransferEntryOwner transfers ownership of an entry to `newOwnerEmail` (and optional `newOwnerGroup`),
// bumps `version`, terminates any active tunnel session, and emits `NOTIFY registry_entries` / `NOTIFY registry_tunnels`.
func (s *Store) TransferEntryOwner(ctx context.Context, entryID string, expectedVersion int64, newOwnerEmail, newOwnerGroup string) (*EntryRow, error) {
	newOwnerEmail = strings.ToLower(strings.TrimSpace(newOwnerEmail))
	if newOwnerEmail == "" {
		return nil, errors.New("new_owner_email is required")
	}
	if _, err := s.UpsertOwner(ctx, newOwnerEmail); err != nil {
		return nil, err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET owner_email            = $1,
    owner_group            = $2,
    last_disconnect_at     = $3,
    last_disconnect_reason = 'ownership transferred',
    version                = version + 1,
    updated_at             = $3
WHERE id = $4 AND version = $5 AND deleted_at IS NULL`,
		newOwnerEmail, strings.TrimSpace(newOwnerGroup), now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch", ErrConflict, entryID, expectedVersion)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM registry.tunnel_sessions WHERE entry_id = $1`, entryID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
		s.notify(ctx, "registry_tunnels", out.Name)
	}
	return out, err
}

// UpdateConnectedPrincipal updates `connected_principal` on a connected entry,
// bumps `version`, terminates any active tunnel session, and emits `NOTIFY registry_entries` / `NOTIFY registry_tunnels`.
func (s *Store) UpdateConnectedPrincipal(ctx context.Context, entryID string, expectedVersion int64, connectedPrincipal string) (*EntryRow, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET connected_principal    = $1,
    last_disconnect_at     = $2,
    last_disconnect_reason = 'connected_principal changed',
    version                = version + 1,
    updated_at             = $2
WHERE id = $3 AND version = $4 AND deleted_at IS NULL`,
		strings.TrimSpace(connectedPrincipal), now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch", ErrConflict, entryID, expectedVersion)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM registry.tunnel_sessions WHERE entry_id = $1`, entryID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
		s.notify(ctx, "registry_tunnels", out.Name)
	}
	return out, err
}

// SoftDeleteEntry transitions an entry to `deleted`, sets `deleted_at` and
// `purge_after` (30-day name reservation), and clears its active scopes and credentials.
func (s *Store) SoftDeleteEntry(ctx context.Context, entryID string, expectedVersion int64) (*EntryRow, error) {
	now := s.now()
	purgeAfter := now.Add(s.thresholds.ReservationPeriod)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state        = $1,
    deleted_at             = $2,
    purge_after            = $3,
    last_disconnect_at     = $2,
    last_disconnect_reason = 'entry deleted',
    version                = version + 1,
    updated_at             = $2
WHERE id = $4 AND version = $5 AND deleted_at IS NULL`,
		string(api.StateDeleted), now, purgeAfter, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch or already deleted", ErrConflict, entryID, expectedVersion)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM registry.entry_scopes WHERE entry_id = $1`, entryID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE registry.entry_credentials SET revoked_at = $1 WHERE entry_id = $2 AND revoked_at IS NULL`, now, entryID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM registry.tunnel_sessions WHERE entry_id = $1`, entryID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
		s.notify(ctx, "registry_tunnels", out.Name)
	}
	return out, err
}

// NormalizeScopePath canonicalizes a scope path prefix by trimming leading/trailing slashes.
func NormalizeScopePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, "/")
	return p
}

// ScopesOverlap reports whether two normalized path prefixes within the same
// repository overlap.
func ScopesOverlap(prefixA, prefixB string) bool {
	return api.PrimaryPrefixesOverlap(prefixA, prefixB)
}

// ReplaceEntryScopes replaces all scopes for `(entryID, source)` ("reported" or "declared")
// while enforcing primary-claim non-overlap under `pg_advisory_xact_lock`.
func (s *Store) ReplaceEntryScopes(ctx context.Context, entryID, source string, claims []ScopeClaim) error {
	if source != "reported" && source != "declared" {
		return fmt.Errorf("invalid scope source %q (expected \"reported\" or \"declared\")", source)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := replaceEntryScopesTx(ctx, tx, entryID, source, claims); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceEntryScopesTx(ctx context.Context, tx *sql.Tx, entryID, source string, claims []ScopeClaim) error {
	type normClaim struct {
		repo    string
		prefix  string
		primary bool
		branch  string
	}
	seen := make(map[string]bool, len(claims))
	normalized := make([]normClaim, 0, len(claims))
	reposToLock := make(map[string]bool)

	for _, c := range claims {
		repo := strings.ToLower(strings.TrimSpace(c.Repo))
		if repo == "" {
			return errors.New("scope repo must be non-empty")
		}
		prefix := NormalizeScopePath(c.PathPrefix)
		key := repo + "\x00" + prefix
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, normClaim{
			repo:    repo,
			prefix:  prefix,
			primary: c.Primary,
			branch:  strings.TrimSpace(c.Branch),
		})
		if c.Primary {
			reposToLock[repo] = true
		}
	}

	sortedRepos := make([]string, 0, len(reposToLock))
	for r := range reposToLock {
		sortedRepos = append(sortedRepos, r)
	}
	sort.Strings(sortedRepos)
	for _, r := range sortedRepos {
		lockKey := "scope:" + r
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
			return fmt.Errorf("acquiring scope advisory lock for %s: %w", r, err)
		}
	}

	for _, nc := range normalized {
		if !nc.primary {
			continue
		}
		rows, err := tx.QueryContext(ctx, `
SELECT e.name, es.path_prefix
FROM registry.entry_scopes es
JOIN registry.entries e ON e.id = es.entry_id
WHERE es.repo = $1
  AND es.primary_claim = TRUE
  AND es.entry_id != $2
  AND e.deleted_at IS NULL`, nc.repo, entryID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var otherName, otherPrefix string
			if err := rows.Scan(&otherName, &otherPrefix); err != nil {
				_ = rows.Close()
				return err
			}
			if ScopesOverlap(nc.prefix, otherPrefix) {
				_ = rows.Close()
				return fmt.Errorf("%w: repo %q path %q overlaps with primary claim on %q held by entry %q",
					ErrPrimaryScopeConflict, nc.repo, nc.prefix, otherPrefix, otherName)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM registry.entry_scopes WHERE entry_id = $1 AND source = $2`, entryID, source); err != nil {
		return err
	}
	for _, nc := range normalized {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO registry.entry_scopes (entry_id, repo, path_prefix, source, primary_claim, branch)
VALUES ($1, $2, $3, $4, $5, $6)`,
			entryID, nc.repo, nc.prefix, source, nc.primary, nc.branch); err != nil {
			return err
		}
	}
	return nil
}

// ListEntryScopes returns the combined `reported` and `declared` scopes for entryID.
func (s *Store) ListEntryScopes(ctx context.Context, entryID string) ([]ScopeRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT entry_id, repo, path_prefix, source, primary_claim, branch
FROM registry.entry_scopes
WHERE entry_id = $1
ORDER BY repo ASC, path_prefix ASC, source ASC`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScopeRow
	for rows.Next() {
		var r ScopeRow
		if err := rows.Scan(&r.EntryID, &r.Repo, &r.PathPrefix, &r.Source, &r.PrimaryClaim, &r.Branch); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CombinedAPIScopes converts `reported` and `declared` ScopeRows into `api.Scope` values.
func CombinedAPIScopes(rows []ScopeRow) []api.Scope {
	type key struct {
		repo   string
		prefix string
	}
	byKey := make(map[key]*api.Scope, len(rows))
	var order []key
	for _, r := range rows {
		k := key{repo: r.Repo, prefix: r.PathPrefix}
		role := api.RoleSecondary
		if r.PrimaryClaim {
			role = api.RolePrimary
		}
		if existing, ok := byKey[k]; ok {
			if r.PrimaryClaim {
				existing.Role = api.RolePrimary
			}
			if existing.Branch == "" && r.Branch != "" {
				existing.Branch = r.Branch
			}
			continue
		}
		sc := &api.Scope{
			Repo:       r.Repo,
			PathPrefix: r.PathPrefix,
			Role:       role,
			Source:     api.ScopeSource(r.Source),
			Branch:     r.Branch,
			Active:     true,
		}
		byKey[k] = sc
		order = append(order, k)
	}
	out := make([]api.Scope, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

// IssueCredential generates a new `grc_<hex>` bearer credential for `entryID`
// and stores only its SHA-256 digest in `registry.entry_credentials`.
func (s *Store) IssueCredential(ctx context.Context, entryID string) (token string, credID string, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", fmt.Errorf("generating random credential bytes: %w", err)
	}
	token = "grc_" + hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	credID = uuid.NewString()
	now := s.now()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO registry.entry_credentials (id, entry_id, token_sha256, created_at)
VALUES ($1, $2, $3, $4)`,
		credID, entryID, sum[:], now)
	if err != nil {
		return "", "", fmt.Errorf("storing entry credential: %w", err)
	}
	return token, credID, nil
}

// AuthenticateCredential verifies a `grc_` token against `registry.entry_credentials`,
// updates `last_used_at`, and returns the owning active EntryRow.
func (s *Store) AuthenticateCredential(ctx context.Context, token string) (*EntryRow, error) {
	row, _, err := s.AuthenticateCredentialWithID(ctx, token)
	return row, err
}

// AuthenticateCredentialWithID verifies a `grc_` token against `registry.entry_credentials`,
// updates `last_used_at`, and returns the owning active EntryRow and credential UUID.
func (s *Store) AuthenticateCredentialWithID(ctx context.Context, token string) (*EntryRow, string, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "grc_") {
		return nil, "", ErrInvalidCredential
	}
	sum := sha256.Sum256([]byte(token))
	now := s.now()
	var entryID, credID string
	err := s.db.QueryRowContext(ctx, `
UPDATE registry.entry_credentials ec
SET last_used_at = $1
FROM registry.entries e
WHERE ec.token_sha256 = $2
  AND ec.revoked_at IS NULL
  AND e.id = ec.entry_id
  AND e.deleted_at IS NULL
RETURNING ec.entry_id, ec.id::text`, now, sum[:]).Scan(&entryID, &credID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrInvalidCredential
	}
	if err != nil {
		return nil, "", err
	}
	row, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, "", err
	}
	return row, credID, nil
}

// RevokeCredential marks a single credential as revoked, terminates any active tunnel
// session that authenticated with that credential, and emits `NOTIFY registry_tunnels`.
func (s *Store) RevokeCredential(ctx context.Context, entryID, credID string) error {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
UPDATE registry.entry_credentials
SET revoked_at = $1
WHERE id = $2::uuid AND entry_id = $3::uuid AND revoked_at IS NULL`,
		now, credID, entryID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}

	delRes, err := tx.ExecContext(ctx, `
DELETE FROM registry.tunnel_sessions
WHERE entry_id = $1::uuid AND (cred_id IS NULL OR cred_id = $2::uuid)`, entryID, credID)
	if err != nil {
		return err
	}
	if dn, _ := delRes.RowsAffected(); dn > 0 {
		_, _ = tx.ExecContext(ctx, `
UPDATE registry.entries
SET last_disconnect_at     = $1,
    last_disconnect_reason = 'credential revoked',
    updated_at             = $1
WHERE id = $2::uuid`, now, entryID)
	}
	var name string
	_ = tx.QueryRowContext(ctx, `SELECT name FROM registry.entries WHERE id = $1::uuid`, entryID).Scan(&name)
	if err := tx.Commit(); err != nil {
		return err
	}
	if name != "" {
		s.notify(ctx, "registry_tunnels", name)
	}
	return nil
}

// IsCredentialActive reports whether `credID` is still unrevoked for `entryID`.
func (s *Store) IsCredentialActive(ctx context.Context, entryID, credID string) (bool, error) {
	if credID == "" {
		return true, nil
	}
	var ok bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM registry.entry_credentials
    WHERE id = $1::uuid AND entry_id = $2::uuid AND revoked_at IS NULL
)`, credID, entryID).Scan(&ok)
	return ok, err
}

// RecordHeartbeat updates an entry's heartbeat telemetry (`grain_version`,
// `active_tasks`, `pending_inbox`, `last_heartbeat_at`) and replaces its `reported` scopes.
func (s *Store) RecordHeartbeat(ctx context.Context, entryID string, hb api.HeartbeatRequest) (*EntryRow, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET grain_version     = COALESCE(NULLIF($1, ''), grain_version),
    active_tasks      = $2,
    pending_inbox     = $3,
    last_heartbeat_at = $4,
    updated_at        = $4
WHERE id = $5 AND deleted_at IS NULL`,
		hb.GrainVersion, hb.ActiveTasks, hb.QueueDepth, now, entryID)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	if hb.Scopes != nil {
		claims := make([]ScopeClaim, 0, len(hb.Scopes))
		for _, sc := range hb.Scopes {
			claims = append(claims, ScopeClaim{
				Repo:       sc.Repo,
				PathPrefix: sc.PathPrefix,
				Branch:     sc.Branch,
			})
		}
		if err := replaceEntryScopesTx(ctx, tx, entryID, "reported", claims); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetEntryByID(ctx, entryID)
}

// UpsertReplica registers or refreshes a registry replica's heartbeat in `registry.replicas`.
func (s *Store) UpsertReplica(ctx context.Context, replicaID, peerAddr string) error {
	now := s.now()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO registry.replicas (replica_id, peer_addr, started_at, heartbeat_at)
VALUES ($1, $2, $3, $3)
ON CONFLICT (replica_id) DO UPDATE SET
    peer_addr    = EXCLUDED.peer_addr,
    heartbeat_at = EXCLUDED.heartbeat_at`,
		replicaID, peerAddr, now)
	return err
}

// DeleteReplica removes a replica and cascades to its `registry.tunnel_sessions`.
func (s *Store) DeleteReplica(ctx context.Context, replicaID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM registry.replicas WHERE replica_id = $1`, replicaID)
	return err
}

// AcquireTunnelSession increments `entries.session_epoch` and binds `tunnel_sessions`
// to `replicaID` at that fenced epoch.
func (s *Store) AcquireTunnelSession(ctx context.Context, entryID, replicaID, remoteAddr string) (*TunnelSessionRow, error) {
	return s.AcquireTunnelSessionWithCred(ctx, entryID, replicaID, remoteAddr, "")
}

// AcquireTunnelSessionWithCred increments `entries.session_epoch`, records `credID` (when non-empty),
// binds `tunnel_sessions` to `replicaID` at that fenced epoch, and emits `NOTIFY registry_tunnels`.
func (s *Store) AcquireTunnelSessionWithCred(ctx context.Context, entryID, replicaID, remoteAddr, credID string) (*TunnelSessionRow, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var epoch int64
	var name string
	err = tx.QueryRowContext(ctx, `
UPDATE registry.entries
SET session_epoch          = session_epoch + 1,
    last_heartbeat_at      = $1,
    last_disconnect_at     = NULL,
    last_disconnect_reason = '',
    updated_at             = $1
WHERE id = $2 AND deleted_at IS NULL
RETURNING session_epoch, name`, now, entryID).Scan(&epoch, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO registry.tunnel_sessions (entry_id, replica_id, session_epoch, remote_addr, connected_at, last_seen_at, cred_id)
VALUES ($1, $2, $3, $4, $5, $5, NULLIF($6, '')::uuid)
ON CONFLICT (entry_id) DO UPDATE SET
    replica_id    = EXCLUDED.replica_id,
    session_epoch = EXCLUDED.session_epoch,
    remote_addr   = EXCLUDED.remote_addr,
    connected_at  = EXCLUDED.connected_at,
    last_seen_at  = EXCLUDED.last_seen_at,
    cred_id       = EXCLUDED.cred_id
WHERE registry.tunnel_sessions.session_epoch <= EXCLUDED.session_epoch`,
		entryID, replicaID, epoch, remoteAddr, now, credID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if name != "" {
		s.notify(ctx, "registry_tunnels", name)
	}
	return s.GetActiveTunnelSession(ctx, entryID)
}

// TouchTunnelSession updates `last_seen_at` and `rtt_ms` on an active tunnel session
// only if `sessionEpoch` still matches the fenced epoch.
func (s *Store) TouchTunnelSession(ctx context.Context, entryID string, sessionEpoch int64, rttMs int) error {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.tunnel_sessions
SET last_seen_at = $1,
    rtt_ms       = $2
WHERE entry_id = $3 AND session_epoch = $4`,
		now, rttMs, entryID, sessionEpoch)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE registry.entries
SET last_heartbeat_at = $1,
    updated_at        = $1
WHERE id = $2 AND session_epoch = $3`, now, entryID, sessionEpoch)
	return err
}

// ReleaseTunnelSession deletes the `tunnel_sessions` row only if `sessionEpoch` matches,
// preventing a stale disconnecting session from wiping out a newer session.
func (s *Store) ReleaseTunnelSession(ctx context.Context, entryID string, sessionEpoch int64) (bool, error) {
	return s.ReleaseTunnelSessionWithReason(ctx, entryID, sessionEpoch, "disconnected")
}

// ReleaseTunnelSessionWithReason deletes the `tunnel_sessions` row only if `sessionEpoch` matches,
// records `last_disconnect_at` / `last_disconnect_reason` on `entries`, and emits `NOTIFY registry_tunnels`.
func (s *Store) ReleaseTunnelSessionWithReason(ctx context.Context, entryID string, sessionEpoch int64, reason string) (bool, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
DELETE FROM registry.tunnel_sessions
WHERE entry_id = $1 AND session_epoch = $2`, entryID, sessionEpoch)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	var name string
	_ = s.db.QueryRowContext(ctx, `
UPDATE registry.entries
SET last_heartbeat_at      = GREATEST(COALESCE(last_heartbeat_at, $1), $1),
    last_disconnect_at     = $1,
    last_disconnect_reason = $2,
    updated_at             = $1
WHERE id = $3 AND session_epoch = $4
RETURNING name`, now, reason, entryID, sessionEpoch).Scan(&name)
	if name != "" {
		s.notify(ctx, "registry_tunnels", name)
	}
	return true, nil
}

// GetActiveTunnelSession returns the current `tunnel_sessions` row joined with its `replicas` row.
func (s *Store) GetActiveTunnelSession(ctx context.Context, entryID string) (*TunnelSessionRow, error) {
	var row TunnelSessionRow
	var rtt sql.NullInt64
	var credID sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT
    ts.entry_id, COALESCE(e.name, ''), ts.replica_id, r.peer_addr, r.heartbeat_at,
    ts.session_epoch, ts.remote_addr, ts.connected_at, ts.last_seen_at, ts.rtt_ms,
    ts.cred_id::text
FROM registry.tunnel_sessions ts
JOIN registry.replicas r ON r.replica_id = ts.replica_id
LEFT JOIN registry.entries e ON e.id = ts.entry_id
WHERE ts.entry_id = $1`, entryID).Scan(
		&row.EntryID, &row.EntryName, &row.ReplicaID, &row.PeerAddr, &row.ReplicaLastSeen,
		&row.SessionEpoch, &row.RemoteAddr, &row.ConnectedAt, &row.LastSeenAt, &rtt,
		&credID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if rtt.Valid {
		row.RTTMs = int(rtt.Int64)
	}
	row.CredID = credID.String
	return &row, nil
}

// ListActiveTunnelSessions returns all active `tunnel_sessions` rows joined with `replicas` and `entries`.
func (s *Store) ListActiveTunnelSessions(ctx context.Context) ([]TunnelSessionRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
    ts.entry_id, e.name, ts.replica_id, r.peer_addr, r.heartbeat_at,
    ts.session_epoch, ts.remote_addr, ts.connected_at, ts.last_seen_at, ts.rtt_ms,
    ts.cred_id::text
FROM registry.tunnel_sessions ts
JOIN registry.replicas r ON r.replica_id = ts.replica_id
JOIN registry.entries e ON e.id = ts.entry_id
WHERE e.deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TunnelSessionRow
	for rows.Next() {
		var row TunnelSessionRow
		var rtt sql.NullInt64
		var credID sql.NullString
		if err := rows.Scan(
			&row.EntryID, &row.EntryName, &row.ReplicaID, &row.PeerAddr, &row.ReplicaLastSeen,
			&row.SessionEpoch, &row.RemoteAddr, &row.ConnectedAt, &row.LastSeenAt, &rtt,
			&credID,
		); err != nil {
			return nil, err
		}
		if rtt.Valid {
			row.RTTMs = int(rtt.Int64)
		}
		row.CredID = credID.String
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeriveStatus computes the runtime `api.EntryStatus` from `EntryRow.LifecycleState`,
// `LastHeartbeatAt`, and any active `TunnelSessionRow` + `ReplicaLastSeen`.
func (s *Store) DeriveStatus(e *EntryRow) api.EntryStatus {
	now := s.now()
	switch e.LifecycleState {
	case api.StateRunning, "ACTIVE":
		// Evaluated below by kind.
	default:
		return api.StatusOffline
	}

	switch e.Kind {
	case api.KindInstance:
		if e.LastHeartbeatAt == nil {
			return api.StatusOffline
		}
		age := now.Sub(*e.LastHeartbeatAt)
		if age <= s.thresholds.DegradedAfter {
			return api.StatusOnline
		}
		if age <= s.thresholds.OfflineAfter {
			return api.StatusDegraded
		}
		return api.StatusOffline

	case api.KindConnected:
		if e.Tunnel == nil {
			return api.StatusOffline
		}
		if now.Sub(e.Tunnel.ReplicaLastSeen) > s.thresholds.ReplicaTTL {
			return api.StatusOffline
		}
		age := now.Sub(e.Tunnel.LastSeenAt)
		if e.LastHeartbeatAt != nil {
			if hbAge := now.Sub(*e.LastHeartbeatAt); hbAge < age {
				age = hbAge
			}
		}
		if age <= s.thresholds.DegradedAfter {
			return api.StatusOnline
		}
		if age <= s.thresholds.OfflineAfter {
			return api.StatusDegraded
		}
		return api.StatusOffline
	default:
		return api.StatusOffline
	}
}

// ToAPIEntry converts an EntryRow into an `api.Entry` with derived runtime status.
func (s *Store) ToAPIEntry(e *EntryRow, instanceDomain string) api.Entry {
	urlStr := ""
	if instanceDomain != "" {
		urlStr = fmt.Sprintf("https://%s.%s", e.Name, instanceDomain)
	}
	return api.Entry{
		Name:            e.Name,
		URL:             urlStr,
		Kind:            e.Kind,
		State:           e.LifecycleState,
		Status:          s.DeriveStatus(e),
		StatusDetail:    e.StatusReason,
		DisplayName:     e.DisplayName,
		Purpose:         e.Purpose,
		Owner:           e.OwnerEmail,
		OwnerGroup:      e.OwnerGroup,
		Listed:          e.Listed,
		Tags:            e.Tags,
		Shape:           e.Shape,
		Zone:            e.Zone,
		BackendURL:      e.BackendURL,
		GrainVersion:    e.GrainVersion,
		ActiveTasks:     e.ActiveTasks,
		QueueDepth:      e.PendingInbox,
		LastHeartbeatAt: e.LastHeartbeatAt,
		Scopes:          CombinedAPIScopes(e.Scopes),
		CreatedAt:       e.CreatedAt,
		UpdatedAt:       e.UpdatedAt,
		DeletedAt:       e.DeletedAt,
	}
}

// ToDirectoryEntry converts an EntryRow into a public `api.DirectoryEntry`.
func (s *Store) ToDirectoryEntry(e *EntryRow, instanceDomain string) api.DirectoryEntry {
	urlStr := ""
	if instanceDomain != "" {
		urlStr = fmt.Sprintf("https://%s.%s", e.Name, instanceDomain)
	}
	return api.DirectoryEntry{
		Name:         e.Name,
		URL:          urlStr,
		Kind:         e.Kind,
		Status:       s.DeriveStatus(e),
		DisplayName:  e.DisplayName,
		Owner:        e.OwnerEmail,
		OwnerGroup:   e.OwnerGroup,
		Tags:         e.Tags,
		GrainVersion: e.GrainVersion,
		Scopes:       CombinedAPIScopes(e.Scopes),
		ActiveTasks:  e.ActiveTasks,
		QueueDepth:   e.PendingInbox,
		UpdatedAt:    e.UpdatedAt,
	}
}

// RouteRepoPath resolves the best-matching entries for `(repo, filePath)` using
// longest-path-prefix matching.
func (s *Store) RouteRepoPath(ctx context.Context, repo, filePath, instanceDomain string, filter func(*EntryRow) bool) (*api.RouteResponse, error) {
	repo = strings.ToLower(strings.TrimSpace(repo))
	normPath := NormalizeScopePath(filePath)
	entries, err := s.ListEntries(ctx)
	if err != nil {
		return nil, err
	}

	var matches []api.RouteCandidate
	for _, e := range entries {
		if filter != nil && !filter(e) {
			continue
		}
		combined := CombinedAPIScopes(e.Scopes)
		bestLen := -1
		var bestScope *api.Scope
		for i := range combined {
			sc := &combined[i]
			if !strings.EqualFold(sc.Repo, repo) {
				continue
			}
			matched, spec := api.PathMatchesPrefix(normPath, sc.PathPrefix)
			if !matched {
				continue
			}
			if spec > bestLen || (spec == bestLen && sc.Role == api.RolePrimary) {
				bestLen = spec
				bestScope = sc
			}
		}
		if bestScope != nil {
			urlStr := ""
			if instanceDomain != "" {
				urlStr = fmt.Sprintf("https://%s.%s", e.Name, instanceDomain)
			}
			matches = append(matches, api.RouteCandidate{
				Name:        e.Name,
				URL:         urlStr,
				Kind:        e.Kind,
				Status:      s.DeriveStatus(e),
				DisplayName: e.DisplayName,
				Purpose:     e.Purpose,
				Owner:       e.OwnerEmail,
				OwnerGroup:  e.OwnerGroup,
				MatchedScope: api.MatchedScope{
					Repo:        bestScope.Repo,
					PathPrefix:  bestScope.PathPrefix,
					Role:        bestScope.Role,
					Source:      bestScope.Source,
					Specificity: bestLen,
				},
			})
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].MatchedScope.Specificity != matches[j].MatchedScope.Specificity {
			return matches[i].MatchedScope.Specificity > matches[j].MatchedScope.Specificity
		}
		iPrimary := matches[i].MatchedScope.Role == api.RolePrimary
		jPrimary := matches[j].MatchedScope.Role == api.RolePrimary
		if iPrimary != jPrimary {
			return iPrimary
		}
		return matches[i].Name < matches[j].Name
	})

	res := &api.RouteResponse{
		Repo:       repo,
		Path:       normPath,
		Candidates: matches,
	}
	if res.Candidates == nil {
		res.Candidates = []api.RouteCandidate{}
	}
	for i := range matches {
		if matches[i].MatchedScope.Role == api.RolePrimary {
			m := matches[i]
			res.Primary = &m
			break
		}
	}
	if res.Primary == nil && len(matches) == 1 {
		m := matches[0]
		res.Primary = &m
	}
	return res, nil
}

// AcquireOrRenewLease acquires or renews a named lease in `registry.leases` if it is
// currently unheld, expired, or already held by `holderID`.
func (s *Store) AcquireOrRenewLease(ctx context.Context, leaseName, holderID string, ttl time.Duration) (bool, error) {
	now := s.now()
	expiresAt := now.Add(ttl)
	res, err := s.db.ExecContext(ctx, `
INSERT INTO registry.leases (name, holder_id, expires_at)
VALUES ($1, $2, $3)
ON CONFLICT (name) DO UPDATE SET
    holder_id  = EXCLUDED.holder_id,
    expires_at = EXCLUDED.expires_at
WHERE registry.leases.holder_id = EXCLUDED.holder_id
   OR registry.leases.expires_at <= $4`,
		leaseName, holderID, expiresAt, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RecordAudit writes an audit log row to `registry.audit_log`.
func (s *Store) RecordAudit(ctx context.Context, actorEmail, action, entryID, entryName string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	var idParam any
	if strings.TrimSpace(entryID) != "" {
		idParam = entryID
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO registry.audit_log (at, actor_email, action, entry_id, entry_name, detail)
VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6::jsonb)`,
		s.now(), actorEmail, action, idParam, entryName, string(detailJSON))
	return err
}

// ListAuditLog returns recent audit log entries (most recent first).
func (s *Store) ListAuditLog(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, at, actor_email, action, COALESCE(entry_id::text, ''), COALESCE(entry_name, ''), detail
FROM registry.audit_log
ORDER BY id DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		var detailBytes []byte
		if err := rows.Scan(&a.ID, &a.At, &a.ActorEmail, &a.Action, &a.EntryID, &a.EntryName, &detailBytes); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(detailBytes, &a.Detail)
		out = append(out, a)
	}
	return out, rows.Err()
}
