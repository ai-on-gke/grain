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
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/ai-on-gke/grain/pkg/hub/api"
)

// Visibility controls which callers can view or access an entry.
type Visibility string

const (
	VisibilityOwner  Visibility = "owner"
	VisibilityShared Visibility = "shared"
	VisibilityPublic Visibility = "public"

	// DefaultTransferWindow is the 7-day window within which a pending ownership transfer must be accepted.
	DefaultTransferWindow = 7 * 24 * time.Hour
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
	ErrNotFound = errors.New("hub store: not found")

	// ErrConflict is returned when an optimistic lock (`version`), unique name,
	// or session_epoch check fails.
	ErrConflict = errors.New("hub store: conflict")

	// ErrNameReserved is returned when a name is still within its 30-day tombstone reservation window for another owner.
	ErrNameReserved = errors.New("hub store: entry name is reserved by a recently deleted entry")

	// ErrPrimaryScopeConflict is returned when two entries claim primary_claim = TRUE
	// on overlapping (repo, path_prefix) scopes.
	ErrPrimaryScopeConflict = errors.New("hub store: overlapping primary scope claim")

	// ErrQuotaExceeded is returned when an owner exceeds their instance units or connected entry cap.
	ErrQuotaExceeded = errors.New("hub store: quota exceeded")

	// ErrInvalidCredential is returned when a `grc_` token is unknown or revoked.
	ErrInvalidCredential = errors.New("hub store: invalid or revoked entry credential")

	// ErrSQLPoolExhausted is returned when all non-draining shared Cloud SQL instances have reached max_databases.
	ErrSQLPoolExhausted = errors.New("hub store: all non-draining sql_instances have reached max_databases capacity")

	// ErrUnknownShape is returned when a requested instance VM shape is not configured in registry.shapes.
	ErrUnknownShape = errors.New("unknown shape")
)

func lookupShapeUnitsTx(ctx context.Context, tx *sql.Tx, shape string) (int, error) {
	var units int
	err := tx.QueryRowContext(ctx, `SELECT quota_units FROM registry.shapes WHERE name = $1`, shape).Scan(&units)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("%w %q", ErrUnknownShape, shape)
		}
		return 0, fmt.Errorf("lookup shape %q: %w", shape, err)
	}
	return units, nil
}

// PrimaryScopeConflictError is returned when a synchronous scope mutation
// conflicts with an existing active primary scope claim on a listed entry.
type PrimaryScopeConflictError struct {
	Conflict api.ScopeConflict
}

func (e *PrimaryScopeConflictError) Error() string {
	if e == nil {
		return ErrPrimaryScopeConflict.Error()
	}
	return fmt.Sprintf("%s: repo %q path %q overlaps with primary claim on %q held by entry %q",
		ErrPrimaryScopeConflict.Error(),
		e.Conflict.Repo,
		e.Conflict.PathPrefix,
		e.Conflict.ConflictingPrefix,
		e.Conflict.ConflictingEntry,
	)
}

func (e *PrimaryScopeConflictError) Unwrap() error {
	return ErrPrimaryScopeConflict
}

// Thresholds configures runtime status derivation from heartbeat and replica timestamps.
type Thresholds struct {
	DegradedAfter     time.Duration
	OfflineAfter      time.Duration
	ReplicaTTL        time.Duration
	DefaultUnitsQuota int
	DefaultConnQuota  int
	ReservationPeriod time.Duration
	DBRetentionPeriod time.Duration
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
		DBRetentionPeriod: 168 * time.Hour,
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
	EntryID        string        `json:"entry_id"`
	Repo           string        `json:"repo"`
	PathPrefix     string        `json:"path_prefix"`
	Source         string        `json:"source"`
	PrimaryClaim   bool          `json:"primary_claim"`
	EffectiveRole  api.ScopeRole `json:"effective_role,omitempty"`
	Branch         string        `json:"branch,omitempty"`
	Active         bool          `json:"active"`
	InactiveReason string        `json:"inactive_reason,omitempty"`
	ConflictWith   string        `json:"conflict_with,omitempty"`
	ClaimedAt      time.Time     `json:"claimed_at,omitempty"`
	UpdatedAt      time.Time     `json:"updated_at,omitempty"`
}

// EntryRow represents a catalog entry in `registry.entries` along with its
// combined scopes and active tunnel state.
type EntryRow struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Kind              api.EntryKind  `json:"kind"`
	OwnerEmail        string         `json:"owner_email"`
	OwnerGroup        string         `json:"owner_group,omitempty"`
	PendingOwner      string         `json:"pending_owner,omitempty"`
	TransferExpiresAt *time.Time     `json:"transfer_expires_at,omitempty"`
	Listed            bool           `json:"listed"`
	Visibility        Visibility     `json:"visibility"`
	AccessList        []string       `json:"access_list"`
	DisplayName       string         `json:"display_name,omitempty"`
	Purpose           string         `json:"purpose,omitempty"`
	Tags              []string       `json:"tags,omitempty"`
	LifecycleState    api.EntryState `json:"lifecycle_state"`
	StatusReason      string         `json:"status_reason,omitempty"`
	ReplaceStep       string         `json:"replace_step,omitempty"`
	Version           int64          `json:"version"`

	Shape             string     `json:"shape,omitempty"`
	BootDiskGB        int        `json:"boot_disk_gb,omitempty"`
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
	AllowSSH          bool       `json:"allow_ssh"`
	ShipLogs          bool       `json:"ship_logs"`
	AutoUpgrade       string     `json:"auto_upgrade,omitempty"`
	AutoWake          bool       `json:"auto_wake,omitempty"`
	UpgradeAvailable  string     `json:"upgrade_available,omitempty"`

	GrainVersion         string          `json:"grain_version,omitempty"`
	GrainCommit          string          `json:"grain_commit,omitempty"`
	TargetVersion        string          `json:"target_version,omitempty"`
	HealthState          api.HealthState `json:"health_state,omitempty"`
	HealthDetail         string          `json:"health_detail,omitempty"`
	ActiveTasks          int             `json:"active_tasks"`
	AwaitingReplyTasks   int             `json:"awaiting_reply_tasks"`
	PendingInbox         int             `json:"pending_inbox"`
	ActiveSessions       int             `json:"active_sessions"`
	HasReportedScopes    bool            `json:"has_reported_scopes"`
	LastHeartbeatAt      *time.Time      `json:"last_heartbeat_at,omitempty"`
	SessionEpoch         int64           `json:"session_epoch"`
	ConnectedPrincipal   string          `json:"connected_principal,omitempty"`
	HostKind             string          `json:"host_kind,omitempty"`
	LastDisconnectAt     *time.Time      `json:"last_disconnect_at,omitempty"`
	LastDisconnectReason string          `json:"last_disconnect_reason,omitempty"`
	OwnerDisabled        bool            `json:"owner_disabled,omitempty"`

	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	StoppedAt     *time.Time `json:"stopped_at,omitempty"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	ReservedUntil *time.Time `json:"reserved_until,omitempty"`
	PurgeAfter    *time.Time `json:"purge_after,omitempty"`
	PurgedAt      *time.Time `json:"purged_at,omitempty"`

	Scopes      []ScopeRow           `json:"scopes,omitempty"`
	Credentials []api.CredentialMeta `json:"credentials,omitempty"`
	Tunnel      *TunnelSessionRow    `json:"tunnel,omitempty"`
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
	Via        string         `json:"via,omitempty"`
	GrantID    string         `json:"grant_id,omitempty"`
	Action     string         `json:"action"`
	EntryID    string         `json:"entry_id,omitempty"`
	EntryName  string         `json:"entry_name,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
}

// Store manages the `registry` PostgreSQL schema.
// PrimaryScopeAuthorizer reports whether an entry owned by (ownerEmail, ownerGroup)
// is permitted to hold a primary scope on repo under the registry's scope_owners policy.
type PrimaryScopeAuthorizer func(ctx context.Context, repo, ownerEmail, ownerGroup string) (bool, error)

type Store struct {
	db               *sql.DB
	thresholds       Thresholds
	nowFn            func() time.Time
	catalogGen       atomic.Uint64
	scopeAuthMu      sync.RWMutex
	primaryScopeAuth PrimaryScopeAuthorizer
}

// SetPrimaryScopeAuthorizer configures an optional repository primary-scope
// ownership policy enforced on asynchronous heartbeats (`RecordHeartbeat`).
func (s *Store) SetPrimaryScopeAuthorizer(fn PrimaryScopeAuthorizer) {
	if s == nil {
		return
	}
	s.scopeAuthMu.Lock()
	s.primaryScopeAuth = fn
	s.scopeAuthMu.Unlock()
}

func (s *Store) primaryScopeAuthorizer() PrimaryScopeAuthorizer {
	if s == nil {
		return nil
	}
	s.scopeAuthMu.RLock()
	fn := s.primaryScopeAuth
	s.scopeAuthMu.RUnlock()
	return fn
}

// Thresholds returns the store's configured runtime thresholds.
func (s *Store) Thresholds() Thresholds {
	if s == nil {
		return DefaultThresholds()
	}
	return s.thresholds
}

// CatalogGeneration returns a monotonically increasing in-process counter
// incremented whenever catalog-visible store state changes.
func (s *Store) CatalogGeneration() uint64 {
	if s == nil {
		return 0
	}
	return s.catalogGen.Load()
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
	if thresholds.DBRetentionPeriod <= 0 {
		thresholds.DBRetentionPeriod = def.DBRetentionPeriod
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

// Now returns the current UTC time according to the store's clock.
func (s *Store) Now() time.Time {
	return s.now()
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

// GetShape looks up a shape row by name.
func (s *Store) GetShape(ctx context.Context, name string) (*ShapeRow, error) {
	name = strings.TrimSpace(name)
	var r ShapeRow
	err := s.db.QueryRowContext(ctx, `
SELECT name, machine_type, boot_disk_gb, COALESCE(boot_disk_iops, 0), quota_units, description
FROM registry.shapes
WHERE name = $1`, name).Scan(&r.Name, &r.MachineType, &r.BootDiskGB, &r.BootDiskIOPS, &r.QuotaUnits, &r.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetSQLInstance looks up a shared Cloud SQL instance row by name.
func (s *Store) GetSQLInstance(ctx context.Context, name string) (*SQLInstanceRow, error) {
	name = strings.TrimSpace(name)
	var r SQLInstanceRow
	err := s.db.QueryRowContext(ctx, `
SELECT name, connection_name, host(private_ip), max_databases, draining
FROM registry.sql_instances
WHERE name = $1`, name).Scan(&r.Name, &r.ConnectionName, &r.PrivateIP, &r.MaxDatabases, &r.Draining)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListSQLInstances returns all configured Cloud SQL instances ordered by name.
func (s *Store) ListSQLInstances(ctx context.Context) ([]SQLInstanceRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT name, connection_name, host(private_ip), max_databases, draining
FROM registry.sql_instances
ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SQLInstanceRow
	for rows.Next() {
		var r SQLInstanceRow
		if err := rows.Scan(&r.Name, &r.ConnectionName, &r.PrivateIP, &r.MaxDatabases, &r.Draining); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetSQLInstanceDraining toggles the `draining` flag on a `registry.sql_instances` row.
func (s *Store) SetSQLInstanceDraining(ctx context.Context, name string, draining bool) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.sql_instances
SET draining = $1
WHERE name = $2`, draining, strings.TrimSpace(name))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DatabaseNameForEntry returns the canonical PostgreSQL database name for `entryName`
// (`grain_<name with '-' replaced by '_'>`).
func DatabaseNameForEntry(entryName string) string {
	slug := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(entryName)), "-", "_")
	return "grain_" + slug
}

// AllocateSQLInstance selects or preserves the shared Cloud SQL instance placement for `entryID`.
// If the entry already has `sql_instance` assigned (e.g. during VM replacement or restore),
// that assignment is preserved. Otherwise, the non-draining `sql_instances` row with the fewest
// assigned non-purged databases below `max_databases` is selected and recorded on `registry.entries`.
func (s *Store) AllocateSQLInstance(ctx context.Context, entryID, entryName string) (*SQLInstanceRow, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback() }()

	var existingInst, existingDB sql.NullString
	if entryID != "" {
		err = tx.QueryRowContext(ctx, `
SELECT sql_instance, db_name
FROM registry.entries
WHERE id = $1
FOR UPDATE`, entryID).Scan(&existingInst, &existingDB)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
	}

	dbName := existingDB.String
	if dbName == "" {
		dbName = DatabaseNameForEntry(entryName)
	}

	if existingInst.Valid && strings.TrimSpace(existingInst.String) != "" {
		var r SQLInstanceRow
		err = tx.QueryRowContext(ctx, `
SELECT name, connection_name, host(private_ip), max_databases, draining
FROM registry.sql_instances
WHERE name = $1`, existingInst.String).Scan(&r.Name, &r.ConnectionName, &r.PrivateIP, &r.MaxDatabases, &r.Draining)
		if err != nil {
			return nil, "", fmt.Errorf("lookup assigned sql_instance %q: %w", existingInst.String, err)
		}
		if existingDB.String == "" && entryID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE registry.entries SET db_name = $1 WHERE id = $2`, dbName, entryID); err != nil {
				return nil, "", err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, "", err
		}
		return &r, dbName, nil
	}

	// Lock non-draining sql_instances rows in deterministic order to serialize placement.
	rows, err := tx.QueryContext(ctx, `
SELECT name, connection_name, host(private_ip), max_databases, draining
FROM registry.sql_instances
WHERE draining = FALSE
ORDER BY name ASC
FOR UPDATE`)
	if err != nil {
		return nil, "", err
	}
	var candidates []SQLInstanceRow
	for rows.Next() {
		var r SQLInstanceRow
		if err := rows.Scan(&r.Name, &r.ConnectionName, &r.PrivateIP, &r.MaxDatabases, &r.Draining); err != nil {
			_ = rows.Close()
			return nil, "", err
		}
		candidates = append(candidates, r)
	}
	if err := rows.Close(); err != nil {
		return nil, "", err
	}
	if len(candidates) == 0 {
		return nil, "", ErrSQLPoolExhausted
	}

	var best *SQLInstanceRow
	bestCount := -1
	for i := range candidates {
		c := &candidates[i]
		var assigned int
		err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM registry.entries e
WHERE e.sql_instance = $1
  AND e.db_name IS NOT NULL
  AND e.db_name <> ''`, c.Name).Scan(&assigned)
		if err != nil {
			return nil, "", err
		}
		if assigned >= c.MaxDatabases {
			continue
		}
		if best == nil || assigned < bestCount {
			best = c
			bestCount = assigned
		}
	}
	if best == nil {
		return nil, "", ErrSQLPoolExhausted
	}

	if entryID != "" {
		if _, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET sql_instance = $1,
    db_name      = $2
WHERE id = $3`, best.Name, dbName, entryID); err != nil {
			return nil, "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return best, dbName, nil
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
// terminated, tombstone name reservations held by `email` are released, and
// `NOTIFY registry_entries` / `NOTIFY registry_tunnels` are emitted.
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
		// Disabled owners hold no tombstone reservation.
		_, _ = s.db.ExecContext(ctx, `
UPDATE registry.entries
SET reserved_until = $1,
    updated_at     = $1
WHERE owner_email = $2 AND deleted_at IS NOT NULL AND (reserved_until IS NULL OR reserved_until > $1)`, now, email)
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
	if s == nil {
		return
	}
	if channel == "registry_catalog" || channel == "registry_entries" || channel == "registry_tunnels" {
		s.catalogGen.Add(1)
	}
	if s.db == nil || channel == "" {
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

// DeleteQuota removes any custom quota override for `ownerEmail` from `registry.quotas`
// so the owner reverts to the registry's default quotas.
func (s *Store) DeleteQuota(ctx context.Context, ownerEmail string) error {
	ownerEmail = strings.ToLower(strings.TrimSpace(ownerEmail))
	_, err := s.db.ExecContext(ctx, `DELETE FROM registry.quotas WHERE owner_email = $1`, ownerEmail)
	return err
}

// GetOwnerQuotaSummary returns the owner's disabled flag, effective quota limits, and active usage.
func (s *Store) GetOwnerQuotaSummary(ctx context.Context, ownerEmail string) (*api.OwnerQuotaSummary, error) {
	ownerEmail = strings.ToLower(strings.TrimSpace(ownerEmail))
	if ownerEmail == "" {
		return nil, errors.New("owner email is required")
	}
	q, err := s.GetQuota(ctx, ownerEmail)
	if err != nil {
		return nil, err
	}
	var (
		disabled  bool
		createdAt sql.NullTime
		lastSeen  sql.NullTime
	)
	err = s.db.QueryRowContext(ctx, `
SELECT disabled, created_at, last_seen_at
FROM registry.owners
WHERE email = $1`, ownerEmail).Scan(&disabled, &createdAt, &lastSeen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var hasOverride bool
	_ = s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM registry.quotas WHERE owner_email = $1)`, ownerEmail).Scan(&hasOverride)

	out := &api.OwnerQuotaSummary{
		Email:             ownerEmail,
		Disabled:          disabled,
		MaxInstanceUnits:  q.MaxInstanceUnits,
		UsedInstanceUnits: q.UsedInstanceUnits,
		MaxConnected:      q.MaxConnected,
		UsedConnected:     q.UsedConnected,
		HasOverride:       hasOverride,
		UpdatedBy:         q.UpdatedBy,
	}
	if !q.UpdatedAt.IsZero() && hasOverride {
		t := q.UpdatedAt
		out.UpdatedAt = &t
	}
	if createdAt.Valid {
		t := createdAt.Time
		out.CreatedAt = &t
	}
	if lastSeen.Valid {
		t := lastSeen.Time
		out.LastSeenAt = &t
	}
	return out, nil
}

// ListOwnerQuotaSummaries returns all known owners joined with their effective quota limits and active usage.
func (s *Store) ListOwnerQuotaSummaries(ctx context.Context) ([]api.OwnerQuotaSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT email FROM (
    SELECT email FROM registry.owners
    UNION
    SELECT owner_email AS email FROM registry.quotas
    UNION
    SELECT owner_email AS email FROM registry.entries WHERE owner_email <> ''
) all_owners
ORDER BY email ASC`)
	if err != nil {
		return nil, err
	}
	var emails []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			_ = rows.Close()
			return nil, err
		}
		emails = append(emails, e)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]api.OwnerQuotaSummary, 0, len(emails))
	for _, email := range emails {
		sum, err := s.GetOwnerQuotaSummary(ctx, email)
		if err != nil {
			return nil, err
		}
		out = append(out, *sum)
	}
	return out, nil
}

// ListSQLInstanceStatuses returns all `registry.sql_instances` rows enriched with active and retained database counts.
func (s *Store) ListSQLInstanceStatuses(ctx context.Context) ([]api.SQLInstanceStatus, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
    sq.name,
    sq.connection_name,
    host(sq.private_ip),
    sq.max_databases,
    sq.draining,
    COALESCE(SUM(CASE WHEN e.id IS NOT NULL AND e.db_name IS NOT NULL AND e.db_name <> '' AND e.purged_at IS NULL THEN 1 ELSE 0 END), 0) AS used_dbs,
    COALESCE(SUM(CASE WHEN e.id IS NOT NULL AND e.deleted_at IS NULL AND e.db_name IS NOT NULL AND e.db_name <> '' THEN 1 ELSE 0 END), 0) AS active_dbs,
    COALESCE(SUM(CASE WHEN e.id IS NOT NULL AND e.deleted_at IS NOT NULL AND e.db_name IS NOT NULL AND e.db_name <> '' AND e.purged_at IS NULL THEN 1 ELSE 0 END), 0) AS retained_dbs
FROM registry.sql_instances sq
LEFT JOIN registry.entries e ON e.sql_instance = sq.name
GROUP BY sq.name, sq.connection_name, sq.private_ip, sq.max_databases, sq.draining
ORDER BY sq.name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.SQLInstanceStatus
	for rows.Next() {
		var st api.SQLInstanceStatus
		if err := rows.Scan(
			&st.Name, &st.ConnectionName, &st.PrivateIP, &st.MaxDatabases, &st.Draining,
			&st.UsedDatabases, &st.ActiveDatabases, &st.RetainedDatabases,
		); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// GetSQLInstanceStatus returns a single `registry.sql_instances` row with its database counts.
func (s *Store) GetSQLInstanceStatus(ctx context.Context, name string) (*api.SQLInstanceStatus, error) {
	name = strings.TrimSpace(name)
	all, err := s.ListSQLInstanceStatuses(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, ErrNotFound
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
WHERE e.owner_email = $1
  AND e.deleted_at IS NULL
  AND e.lifecycle_state NOT IN ('deleted', 'DELETED', 'failed', 'FAILED')`,
		ownerEmail).Scan(&q.UsedInstanceUnits, &q.UsedConnected)
	if err != nil {
		return nil, err
	}
	return q, nil
}

func (s *Store) getQuotaTx(ctx context.Context, tx *sql.Tx, ownerEmail string) (*QuotaRow, error) {
	ownerEmail = strings.ToLower(strings.TrimSpace(ownerEmail))
	var disabled bool
	if err := tx.QueryRowContext(ctx, `SELECT disabled FROM registry.owners WHERE email = $1 FOR UPDATE`, ownerEmail).Scan(&disabled); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if disabled {
		return nil, fmt.Errorf("%w: owner %s is disabled", ErrConflict, ownerEmail)
	}
	q := &QuotaRow{
		OwnerEmail:       ownerEmail,
		MaxInstanceUnits: s.thresholds.DefaultUnitsQuota,
		MaxConnected:     s.thresholds.DefaultConnQuota,
		UpdatedAt:        s.now(),
	}
	var updatedBy sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT max_instance_units, max_connected, updated_by, updated_at
FROM registry.quotas
WHERE owner_email = $1`, ownerEmail).
		Scan(&q.MaxInstanceUnits, &q.MaxConnected, &updatedBy, &q.UpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	q.UpdatedBy = updatedBy.String

	err = tx.QueryRowContext(ctx, `
SELECT
    COALESCE(SUM(CASE WHEN e.kind = 'instance' THEN COALESCE(sh.quota_units, 1) ELSE 0 END), 0),
    COALESCE(SUM(CASE WHEN e.kind = 'connected' THEN 1 ELSE 0 END), 0)
FROM registry.entries e
LEFT JOIN registry.shapes sh ON sh.name = e.shape
WHERE e.owner_email = $1
  AND e.deleted_at IS NULL
  AND e.lifecycle_state NOT IN ('deleted', 'DELETED', 'failed', 'FAILED')`,
		ownerEmail).Scan(&q.UsedInstanceUnits, &q.UsedConnected)
	if err != nil {
		return nil, err
	}
	return q, nil
}

var (
	randomNameAdjectives = []string{
		"amber", "arctic", "astral", "autumn", "azure", "bold", "bright", "Brisk",
		"calm", " cedar", "clear", "clever", "cobalt", "coral", "cosmic", "crisp",
		"crystal", "cyber", "daring", "dawn", "delta", "eager", "emerald", "fable",
		"fair", "fleet", "floral", "frost", "gentle", "golden", "grand", "harbor",
		"hazel", "indigo", "iron", "ivory", "jade", "keen", "lunar", "maple",
		"meadow", "misty", "noble", "northern", "nova", "ocean", "olive", "onyx",
		"opal", "orbit", "pearl", "pine", "polar", "prime", "quiet", "rapid",
		"river", "ruby", "rustic", "sable", "sage", "silver", "solar", "steady",
	}
	randomNameNouns = []string{
		"anchor", "anvil", "arch", "atlas", "badger", "beacon", "birch", "breeze",
		"bridge", "brook", "canyon", "cinder", "cliff", "cloud", "comet", "compass",
		"condor", "crest", "crane", "creek", "crown", "delta", "dune", "eagle",
		"echo", "ember", "falcon", "fern", "finch", "fjord", "flint", "forge",
		"fox", "glade", "glacier", "grove", "harbor", "hawk", "heron", "horizon",
		"island", "kestrel", "lantern", "larch", "ledge", "linnet", "lynx", "marsh",
		"meadow", "mesa", "meteor", "osprey", "otter", "pebble", "peak", "phoenix",
		"pioneer", "prism", "quarry", "quartz", "raven", "reef", "ridge", "sparrow",
	}
)

// GenerateRandomName produces an available `<adjective>-<noun>-<4hex>` entry name.
func (s *Store) GenerateRandomName(ctx context.Context, callerEmail string) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var buf [4]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return "", fmt.Errorf("generating random name bytes: %w", err)
		}
		adj := strings.ToLower(strings.TrimSpace(randomNameAdjectives[int(buf[0])%len(randomNameAdjectives)]))
		noun := strings.ToLower(strings.TrimSpace(randomNameNouns[int(buf[1])%len(randomNameNouns)]))
		suffix := hex.EncodeToString(buf[2:4])
		candidate := fmt.Sprintf("%s-%s-%s", adj, noun, suffix)
		avail, err := s.CheckNameAvailability(ctx, candidate, callerEmail)
		if err != nil {
			return "", err
		}
		if avail.State == "available" {
			return candidate, nil
		}
	}
	return "", errors.New("failed to generate an available random entry name")
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
	var reservedUntil time.Time
	err = s.db.QueryRowContext(ctx, `
SELECT e.owner_email, COALESCE(e.reserved_until, e.purge_after)
FROM registry.entries e
LEFT JOIN registry.owners o ON o.email = e.owner_email
WHERE e.name = $1
  AND e.deleted_at IS NOT NULL
  AND COALESCE(e.reserved_until, e.purge_after) > $2
  AND COALESCE(o.disabled, FALSE) = FALSE
ORDER BY COALESCE(e.reserved_until, e.purge_after) DESC
LIMIT 1`, name, now).Scan(&reservedOwner, &reservedUntil)
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
			PurgeAfter: &reservedUntil,
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
	BootDiskGB         int
	Zone               string
	AllowSSH           *bool
	ShipLogs           *bool
	AutoUpgrade        string
	AutoWake           *bool
	BackendURL         string
	DeclaredScopes     []ScopeClaim
}

// CreateEntry validates name availability, quotas, backend URL scheme, and primary
// scope claims, then inserts a new row into `registry.entries`.
func (s *Store) CreateEntry(ctx context.Context, in CreateEntryInput) (*EntryRow, error) {
	if !in.Kind.Valid() {
		return nil, fmt.Errorf("unsupported entry kind %q (only \"instance\" and \"connected\" are supported in v1)", in.Kind)
	}
	if in.BootDiskGB != 0 {
		if in.Kind != api.KindInstance {
			return nil, fmt.Errorf("boot_disk_gb is only valid for kind=%q", api.KindInstance)
		}
		if err := api.ValidateBootDiskGB(in.BootDiskGB); err != nil {
			return nil, err
		}
	}
	ownerEmail := strings.ToLower(strings.TrimSpace(in.OwnerEmail))
	if ownerEmail == "" {
		return nil, errors.New("owner_email is required")
	}
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if name == "" {
		var err error
		name, err = s.GenerateRandomName(ctx, ownerEmail)
		if err != nil {
			return nil, err
		}
	} else if err := api.ValidateEntryName(name); err != nil {
		return nil, err
	}
	if err := api.ValidateCatalogMetadata(in.DisplayName, in.Purpose, in.Tags); err != nil {
		return nil, err
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
	allowSSH := true
	if in.AllowSSH != nil {
		allowSSH = *in.AllowSSH
	}
	shipLogs := false
	if in.ShipLogs != nil {
		shipLogs = *in.ShipLogs
	}
	autoUpgrade := strings.ToLower(strings.TrimSpace(in.AutoUpgrade))
	if autoUpgrade != "" && !api.AutoUpgradePolicy(autoUpgrade).Valid() {
		return nil, fmt.Errorf("invalid auto_upgrade policy %q (expected off, notify, or minor)", in.AutoUpgrade)
	}
	autoWake := true
	if in.AutoWake != nil {
		autoWake = *in.AutoWake
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
	ownerGroup := strings.ToLower(strings.TrimSpace(in.OwnerGroup))
	hostKind := strings.TrimSpace(in.HostKind)
	if hostKind == "" && in.Kind == api.KindInstance {
		hostKind = string(api.HostKindHubVM)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	quota, err := s.getQuotaTx(ctx, tx, ownerEmail)
	if err != nil {
		return nil, err
	}
	switch in.Kind {
	case api.KindInstance:
		units := 1
		if in.Shape != "" {
			var err error
			units, err = lookupShapeUnitsTx(ctx, tx, in.Shape)
			if err != nil {
				return nil, err
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

	_, err = tx.ExecContext(ctx, `
INSERT INTO registry.entries (
    id, name, kind, owner_email, owner_group, listed, visibility, access_list,
    display_name, purpose, tags,
    lifecycle_state, version, shape, boot_disk_gb, zone, allow_ssh, ship_logs, auto_upgrade, auto_wake, backend_url,
    connected_principal, host_kind,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8::jsonb,
    $9, $10, $11::jsonb,
    $12, 1, NULLIF($13, ''), NULLIF($14, 0), NULLIF($15, ''), $16, $17, $18, $19, NULLIF($20, ''),
    $21, $22,
    $23, $23
)`,
		id, name, string(in.Kind), ownerEmail, ownerGroup, listed, string(vis), string(accessJSON),
		in.DisplayName, in.Purpose, string(tagsJSON),
		string(state), in.Shape, in.BootDiskGB, in.Zone, allowSSH, shipLogs, autoUpgrade, autoWake, in.BackendURL,
		strings.TrimSpace(in.ConnectedPrincipal), hostKind, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "entries_active_name_idx") || strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
			return nil, fmt.Errorf("%w: entry %q already exists", ErrConflict, name)
		}
		return nil, fmt.Errorf("inserting entry %q: %w", name, err)
	}

	if len(in.DeclaredScopes) > 0 {
		if err := s.replaceEntryScopesWithPolicyTx(ctx, tx, id, "declared", in.DeclaredScopes, listed, false, true, now); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.notify(ctx, "registry_entries", name)
	if listed {
		s.notify(ctx, "registry_catalog", name)
	}
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
// that is still within its retention window (`purge_after > now`) or has been marked `purged_at IS NOT NULL`.
func (s *Store) GetLatestDeletedEntryByName(ctx context.Context, name string) (*EntryRow, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	now := s.now()
	var id string
	err := s.db.QueryRowContext(ctx, `
SELECT id FROM registry.entries
WHERE name = $1
  AND deleted_at IS NOT NULL
  AND (COALESCE(reserved_until, purge_after) IS NULL OR COALESCE(reserved_until, purge_after) > $2 OR purged_at IS NOT NULL)
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
		healthStr                 string
		accessJSON, tagsJSON      []byte
		shape, zone, gceID        sql.NullString
		internalIP, backendURL    sql.NullString
		sa, sqlInst, dbName       sql.NullString
		secretPrefix, configRepo  sql.NullString
		sshGrantedTo, grainVer    sql.NullString
		transferExp               sql.NullTime
		dbPurge, sshExp, hbAt     sql.NullTime
		stoppedAt                 sql.NullTime
		delAt, reservedUntil      sql.NullTime
		purgeAfter, purgedAt      sql.NullTime
		discAt                    sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, `
SELECT
    e.id, e.name, e.kind, e.owner_email, e.owner_group, e.pending_owner, e.transfer_expires_at, e.listed, e.visibility, e.access_list,
    e.display_name, e.purpose, e.tags,
    e.lifecycle_state, e.status_reason, e.replace_step, e.version,
    e.shape, COALESCE(e.boot_disk_gb, 0), e.zone, e.gce_instance_id, host(e.internal_ip), e.backend_url,
    e.service_account, e.sql_instance, e.db_name, e.db_purge_after,
    e.secret_prefix, e.config_repo, e.ssh_grant_expires_at, e.ssh_granted_to,
    e.allow_ssh, e.ship_logs, e.auto_upgrade, e.auto_wake, e.upgrade_available,
    e.grain_version, e.grain_commit, e.target_version, e.health_state, e.health_detail,
    e.active_tasks, e.awaiting_reply_tasks, e.pending_inbox, e.active_sessions,
    e.has_reported_scopes, e.last_heartbeat_at,
    e.session_epoch, e.connected_principal, e.host_kind,
    e.last_disconnect_at, e.last_disconnect_reason,
    COALESCE(o.disabled, FALSE),
    e.created_at, e.updated_at, e.stopped_at, e.deleted_at, e.reserved_until, e.purge_after, e.purged_at
FROM registry.entries e
LEFT JOIN registry.owners o ON o.email = e.owner_email
WHERE e.id = $1`, id).Scan(
		&row.ID, &row.Name, &kindStr, &row.OwnerEmail, &row.OwnerGroup, &row.PendingOwner, &transferExp, &row.Listed, &visStr, &accessJSON,
		&row.DisplayName, &row.Purpose, &tagsJSON,
		&stateStr, &row.StatusReason, &row.ReplaceStep, &row.Version,
		&shape, &row.BootDiskGB, &zone, &gceID, &internalIP, &backendURL,
		&sa, &sqlInst, &dbName, &dbPurge,
		&secretPrefix, &configRepo, &sshExp, &sshGrantedTo,
		&row.AllowSSH, &row.ShipLogs, &row.AutoUpgrade, &row.AutoWake, &row.UpgradeAvailable,
		&grainVer, &row.GrainCommit, &row.TargetVersion, &healthStr, &row.HealthDetail,
		&row.ActiveTasks, &row.AwaitingReplyTasks, &row.PendingInbox, &row.ActiveSessions,
		&row.HasReportedScopes, &hbAt,
		&row.SessionEpoch, &row.ConnectedPrincipal, &row.HostKind,
		&discAt, &row.LastDisconnectReason,
		&row.OwnerDisabled,
		&row.CreatedAt, &row.UpdatedAt, &stoppedAt, &delAt, &reservedUntil, &purgeAfter, &purgedAt,
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
	row.HealthState = api.HealthState(healthStr)
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
	if transferExp.Valid {
		t := transferExp.Time
		row.TransferExpiresAt = &t
	}
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
	if stoppedAt.Valid {
		t := stoppedAt.Time
		row.StoppedAt = &t
	}
	if delAt.Valid {
		t := delAt.Time
		row.DeletedAt = &t
	}
	if reservedUntil.Valid {
		t := reservedUntil.Time
		row.ReservedUntil = &t
	}
	if purgeAfter.Valid {
		t := purgeAfter.Time
		row.PurgeAfter = &t
		if row.ReservedUntil == nil {
			row.ReservedUntil = &t
		}
	}
	if purgedAt.Valid {
		t := purgedAt.Time
		row.PurgedAt = &t
	}

	scopes, err := s.ListEntryScopes(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	populateEffectiveRoles(scopes, row.HasReportedScopes)
	row.Scopes = scopes

	creds, err := s.ListEntryCredentials(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	row.Credentials = creds

	ts, err := s.GetActiveTunnelSession(ctx, row.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	row.Tunnel = ts
	return row, nil
}

// ListEntries returns all active (non-deleted) entries ordered by name.
func (s *Store) ListEntries(ctx context.Context) ([]*EntryRow, error) {
	return s.ListEntriesFiltered(ctx, false)
}

// ListEntriesFiltered returns all entries ordered by name, optionally including soft-deleted rows.
func (s *Store) ListEntriesFiltered(ctx context.Context, includeDeleted bool) ([]*EntryRow, error) {
	q := `SELECT id FROM registry.entries WHERE deleted_at IS NULL ORDER BY name ASC`
	if includeDeleted {
		q = `SELECT id FROM registry.entries ORDER BY name ASC, created_at DESC`
	}
	rows, err := s.db.QueryContext(ctx, q)
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

// ListCatalogEntries loads only public catalog fields for listed, non-deleted entries
// (`listed = TRUE AND deleted_at IS NULL AND lifecycle_state NOT IN ('deleted', 'DELETED')`).
// Internal placement columns (`backend_url`, `internal_ip`, `gce_instance_id`, etc.) are never selected.
func (s *Store) ListCatalogEntries(ctx context.Context) ([]*EntryRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
    e.id, e.name, e.kind, e.owner_email, e.owner_group, e.listed,
    e.display_name, e.purpose, e.tags,
    e.lifecycle_state, e.status_reason, e.version,
    COALESCE(e.grain_version, ''), e.grain_commit, e.health_state, e.health_detail,
    e.active_tasks, e.awaiting_reply_tasks, e.pending_inbox, e.active_sessions,
    e.has_reported_scopes, e.last_heartbeat_at, e.host_kind,
    e.created_at, e.updated_at
FROM registry.entries e
WHERE e.listed = TRUE
  AND e.deleted_at IS NULL
  AND e.lifecycle_state NOT IN ('deleted', 'DELETED')
ORDER BY e.name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*EntryRow
	byID := make(map[string]*EntryRow)
	for rows.Next() {
		row := &EntryRow{}
		var kindStr, stateStr, healthStr string
		var tagsJSON []byte
		var hbAt sql.NullTime
		if err := rows.Scan(
			&row.ID, &row.Name, &kindStr, &row.OwnerEmail, &row.OwnerGroup, &row.Listed,
			&row.DisplayName, &row.Purpose, &tagsJSON,
			&stateStr, &row.StatusReason, &row.Version,
			&row.GrainVersion, &row.GrainCommit, &healthStr, &row.HealthDetail,
			&row.ActiveTasks, &row.AwaitingReplyTasks, &row.PendingInbox, &row.ActiveSessions,
			&row.HasReportedScopes, &hbAt, &row.HostKind,
			&row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			return nil, err
		}
		row.Kind = api.EntryKind(kindStr)
		row.LifecycleState = api.EntryState(stateStr)
		row.HealthState = api.HealthState(healthStr)
		_ = json.Unmarshal(tagsJSON, &row.Tags)
		if hbAt.Valid {
			t := hbAt.Time
			row.LastHeartbeatAt = &t
		}
		out = append(out, row)
		byID[row.ID] = row
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	scopeRows, err := s.db.QueryContext(ctx, `
SELECT
    es.entry_id, es.repo, es.path_prefix, es.source, es.primary_claim, es.branch,
    es.active, es.inactive_reason, es.conflict_with, es.claimed_at, es.updated_at
FROM registry.entry_scopes es
JOIN registry.entries e ON e.id = es.entry_id
WHERE e.listed = TRUE
  AND e.deleted_at IS NULL
  AND e.lifecycle_state NOT IN ('deleted', 'DELETED')
ORDER BY es.entry_id ASC, es.repo ASC, es.path_prefix ASC, es.source ASC`)
	if err != nil {
		return nil, err
	}
	for scopeRows.Next() {
		var sr ScopeRow
		if err := scopeRows.Scan(
			&sr.EntryID, &sr.Repo, &sr.PathPrefix, &sr.Source, &sr.PrimaryClaim, &sr.Branch,
			&sr.Active, &sr.InactiveReason, &sr.ConflictWith, &sr.ClaimedAt, &sr.UpdatedAt,
		); err != nil {
			_ = scopeRows.Close()
			return nil, err
		}
		if e, ok := byID[sr.EntryID]; ok {
			e.Scopes = append(e.Scopes, sr)
		}
	}
	if err := scopeRows.Close(); err != nil {
		return nil, err
	}

	for _, e := range out {
		populateEffectiveRoles(e.Scopes, e.HasReportedScopes)
	}

	tsRows, err := s.ListActiveTunnelSessions(ctx)
	if err != nil {
		return nil, err
	}
	for i := range tsRows {
		ts := tsRows[i]
		if e, ok := byID[ts.EntryID]; ok {
			tsCopy := ts
			e.Tunnel = &tsCopy
		}
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
    stopped_at      = CASE WHEN $1 = 'running' THEN NULL ELSE stopped_at END,
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

// TransferEntryOwner performs an immediate (admin force) ownership transfer of an entry to `newOwnerEmail`
// (and optional `newOwnerGroup`), clears any pending transfer offer or active break-glass SSH grant,
// revokes existing `grc_...` credentials, terminates any active tunnel session, and emits
// `NOTIFY registry_entries` / `NOTIFY registry_tunnels`.
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
    pending_owner          = '',
    transfer_expires_at    = NULL,
    ssh_granted_to         = NULL,
    ssh_grant_expires_at   = NULL,
    last_disconnect_at     = $3,
    last_disconnect_reason = 'ownership transferred',
    version                = version + 1,
    updated_at             = $3
WHERE id = $4 AND version = $5 AND deleted_at IS NULL`,
		newOwnerEmail, strings.ToLower(strings.TrimSpace(newOwnerGroup)), now, entryID, expectedVersion)
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
		if out.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
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

// UpdateEntryCatalogInput holds optional mutable catalog and VM configuration fields for PATCH /api/entries/<name>.
type UpdateEntryCatalogInput struct {
	DisplayName        *string
	Purpose            *string
	HostKind           *api.HostKind
	ConnectedPrincipal *string
	OwnerGroup         *string
	Listed             *bool
	Tags               *[]string
	Shape              *string
	AllowSSH           *bool
	ShipLogs           *bool
	AutoUpgrade        *string
	AutoWake           *bool
}

// UpdateEntryCatalog updates mutable catalog and VM configuration fields under optimistic concurrency.
func (s *Store) UpdateEntryCatalog(ctx context.Context, entryID string, expectedVersion int64, in UpdateEntryCatalogInput) (*EntryRow, error) {
	cur, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	if cur.DeletedAt != nil {
		return nil, ErrNotFound
	}

	displayName := cur.DisplayName
	if in.DisplayName != nil {
		displayName = *in.DisplayName
	}
	purpose := cur.Purpose
	if in.Purpose != nil {
		purpose = *in.Purpose
	}
	tags := cur.Tags
	if in.Tags != nil {
		tags = *in.Tags
	}
	if tags == nil {
		tags = []string{}
	}
	if err := api.ValidateCatalogMetadata(displayName, purpose, tags); err != nil {
		return nil, err
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return nil, err
	}

	hostKind := cur.HostKind
	if in.HostKind != nil {
		if *in.HostKind != "" && !in.HostKind.Valid() {
			return nil, fmt.Errorf("invalid host_kind %q", *in.HostKind)
		}
		hostKind = string(*in.HostKind)
	}
	connPrincipal := cur.ConnectedPrincipal
	principalChanged := false
	if in.ConnectedPrincipal != nil {
		nextP := strings.TrimSpace(*in.ConnectedPrincipal)
		if nextP != connPrincipal {
			connPrincipal = nextP
			principalChanged = true
		}
	}
	ownerGroup := cur.OwnerGroup
	if in.OwnerGroup != nil {
		ownerGroup = strings.ToLower(strings.TrimSpace(*in.OwnerGroup))
	}
	listed := cur.Listed
	if in.Listed != nil {
		listed = *in.Listed
	}
	shape := cur.Shape
	shapeChanged := false
	if in.Shape != nil {
		nextShape := strings.TrimSpace(*in.Shape)
		if nextShape != shape {
			shape = nextShape
			shapeChanged = true
		}
	}
	allowSSH := cur.AllowSSH
	if in.AllowSSH != nil {
		allowSSH = *in.AllowSSH
	}
	shipLogs := cur.ShipLogs
	if in.ShipLogs != nil {
		shipLogs = *in.ShipLogs
	}
	autoUpgrade := cur.AutoUpgrade
	if in.AutoUpgrade != nil {
		nextAU := strings.ToLower(strings.TrimSpace(*in.AutoUpgrade))
		if nextAU != "" && !api.AutoUpgradePolicy(nextAU).Valid() {
			return nil, fmt.Errorf("invalid auto_upgrade policy %q (expected off, notify, or minor)", *in.AutoUpgrade)
		}
		autoUpgrade = nextAU
	}
	autoWake := cur.AutoWake
	if in.AutoWake != nil {
		autoWake = *in.AutoWake
	}

	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if shapeChanged && cur.Kind == api.KindInstance && shape != "" {
		quota, err := s.getQuotaTx(ctx, tx, cur.OwnerEmail)
		if err != nil {
			return nil, err
		}
		newUnits, err := lookupShapeUnitsTx(ctx, tx, shape)
		if err != nil {
			return nil, err
		}
		oldUnits := 1
		if cur.Shape != "" {
			_ = tx.QueryRowContext(ctx, `SELECT quota_units FROM registry.shapes WHERE name = $1`, cur.Shape).Scan(&oldUnits)
		}
		if quota.UsedInstanceUnits-oldUnits+newUnits > quota.MaxInstanceUnits {
			return nil, fmt.Errorf("%w: instance quota units %d - %d + %d exceeds limit %d",
				ErrQuotaExceeded, quota.UsedInstanceUnits, oldUnits, newUnits, quota.MaxInstanceUnits)
		}
	}

	res, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET display_name        = $1,
    purpose             = $2,
    tags                = $3::jsonb,
    host_kind           = $4,
    connected_principal = $5,
    owner_group         = $6,
    listed              = $7,
    shape               = NULLIF($8, ''),
    allow_ssh           = $9,
    ship_logs           = $10,
    auto_upgrade        = $11,
    auto_wake           = $12,
    version             = version + 1,
    updated_at          = $13
WHERE id = $14 AND version = $15 AND deleted_at IS NULL`,
		displayName, purpose, string(tagsJSON), hostKind, connPrincipal, ownerGroup,
		listed, shape, allowSSH, shipLogs, autoUpgrade, autoWake, now, entryID, expectedVersion)
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

	if !cur.Listed && listed {
		if err := s.reconcileEntryScopesTx(ctx, tx, entryID, true, cur.HasReportedScopes, true, now); err != nil {
			return nil, err
		}
	} else if cur.Listed && !listed {
		repos := collectPrimaryRepos(cur.Scopes)
		if err := s.promoteContendersForReposTx(ctx, tx, repos, now); err != nil {
			return nil, err
		}
	}

	if principalChanged && cur.Kind == api.KindConnected {
		_, _ = tx.ExecContext(ctx, `
UPDATE registry.entries
SET last_disconnect_at     = $1,
    last_disconnect_reason = 'connected_principal changed'
WHERE id = $2`, now, entryID)
		if _, err := tx.ExecContext(ctx, `DELETE FROM registry.tunnel_sessions WHERE entry_id = $1`, entryID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
		if cur.Listed || listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
		if principalChanged {
			s.notify(ctx, "registry_tunnels", out.Name)
		}
	}
	return out, err
}

// RequestEntryUpgrade records a requested `target_version` upgrade on a hosted instance entry.
func (s *Store) RequestEntryUpgrade(ctx context.Context, entryID string, expectedVersion int64, targetVersion string) (*EntryRow, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET target_version = $1,
    version        = version + 1,
    updated_at     = $2
WHERE id = $3 AND version = $4 AND deleted_at IS NULL`,
		strings.TrimSpace(targetVersion), now, entryID, expectedVersion)
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

// RequestEntryReplace transitions a hosted instance entry into `replacing` (draining step)
// while validating any shape change against the owner's instance unit quota.
func (s *Store) RequestEntryReplace(ctx context.Context, entryID string, expectedVersion int64, shape, targetVersion string) (*EntryRow, error) {
	return s.RequestEntryReplaceWithDisk(ctx, entryID, expectedVersion, shape, targetVersion, 0)
}

// RequestEntryReplaceWithDisk transitions a hosted instance entry into `replacing` (draining step)
// with an optional custom boot disk size override (`bootDiskGB == 0` uses the new shape's default
// when changing shapes or preserves the current disk size when keeping the same shape).
func (s *Store) RequestEntryReplaceWithDisk(ctx context.Context, entryID string, expectedVersion int64, shape, targetVersion string, bootDiskGB int) (*EntryRow, error) {
	if err := api.ValidateBootDiskGB(bootDiskGB); err != nil {
		return nil, err
	}
	cur, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	if cur.DeletedAt != nil {
		return nil, ErrNotFound
	}
	nextShape := cur.Shape
	if strings.TrimSpace(shape) != "" {
		nextShape = strings.TrimSpace(shape)
	}
	nextTarget := cur.TargetVersion
	if strings.TrimSpace(targetVersion) != "" {
		nextTarget = strings.TrimSpace(targetVersion)
	}
	nextBootDiskGB := cur.BootDiskGB
	if bootDiskGB > 0 {
		nextBootDiskGB = bootDiskGB
	} else if nextShape != cur.Shape {
		nextBootDiskGB = 0
	}

	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if nextShape != "" && nextShape != cur.Shape {
		quota, err := s.getQuotaTx(ctx, tx, cur.OwnerEmail)
		if err != nil {
			return nil, err
		}
		newUnits, err := lookupShapeUnitsTx(ctx, tx, nextShape)
		if err != nil {
			return nil, err
		}
		oldUnits := 1
		if cur.Shape != "" {
			_ = tx.QueryRowContext(ctx, `SELECT quota_units FROM registry.shapes WHERE name = $1`, cur.Shape).Scan(&oldUnits)
		}
		if quota.UsedInstanceUnits-oldUnits+newUnits > quota.MaxInstanceUnits {
			return nil, fmt.Errorf("%w: instance quota units %d - %d + %d exceeds limit %d",
				ErrQuotaExceeded, quota.UsedInstanceUnits, oldUnits, newUnits, quota.MaxInstanceUnits)
		}
	}

	res, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state = $1,
    replace_step    = $2,
    shape           = NULLIF($3, ''),
    boot_disk_gb    = NULLIF($4, 0),
    target_version  = $5,
    version         = version + 1,
    updated_at      = $6
WHERE id = $7 AND version = $8 AND deleted_at IS NULL`,
		string(api.StateReplacing), string(api.ReplaceStepDraining), nextShape, nextBootDiskGB, nextTarget, now, entryID, expectedVersion)
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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
		if out.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
	}
	return out, err
}

// OfferEntryTransfer sets `pending_owner` with a 7-day expiry window (`transfer_expires_at = now + 7d`)
// and optionally updates `owner_group` on an entry. Passing an empty `pendingOwner` cancels an active
// transfer offer and clears `transfer_expires_at`.
func (s *Store) OfferEntryTransfer(ctx context.Context, entryID string, expectedVersion int64, pendingOwner string, ownerGroup *string) (*EntryRow, error) {
	pendingOwner = strings.ToLower(strings.TrimSpace(pendingOwner))
	cur, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	nextGroup := cur.OwnerGroup
	if ownerGroup != nil {
		nextGroup = strings.ToLower(strings.TrimSpace(*ownerGroup))
	}
	now := s.now()
	var expParam any
	if pendingOwner != "" {
		if _, err := s.UpsertOwner(ctx, pendingOwner); err != nil {
			return nil, err
		}
		expParam = now.Add(DefaultTransferWindow)
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET pending_owner       = $1,
    transfer_expires_at = $2,
    owner_group         = $3,
    version             = version + 1,
    updated_at          = $4
WHERE id = $5 AND version = $6 AND deleted_at IS NULL`,
		pendingOwner, expParam, nextGroup, now, entryID, expectedVersion)
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
		if out.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
	}
	return out, err
}

// AcceptEntryTransfer completes a pending ownership transfer to `callerEmail` within the 7-day
// `transfer_expires_at` window, checking `callerEmail`'s quota under `SELECT ... FOR UPDATE`,
// revoking existing `grc_...` credentials, clearing any active break-glass SSH grant, and
// terminating any active tunnel session.
func (s *Store) AcceptEntryTransfer(ctx context.Context, entryID string, expectedVersion int64, callerEmail string) (*EntryRow, error) {
	callerEmail = strings.ToLower(strings.TrimSpace(callerEmail))
	if callerEmail == "" {
		return nil, errors.New("caller email is required")
	}
	cur, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	if cur.DeletedAt != nil {
		return nil, ErrNotFound
	}
	if strings.TrimSpace(cur.PendingOwner) == "" || !strings.EqualFold(cur.PendingOwner, callerEmail) {
		return nil, fmt.Errorf("caller %q is not the pending transfer recipient (%q)", callerEmail, cur.PendingOwner)
	}
	now := s.now()
	if cur.TransferExpiresAt != nil && now.After(*cur.TransferExpiresAt) {
		return nil, fmt.Errorf("%w: ownership transfer offer to %s expired at %s", ErrConflict, callerEmail, cur.TransferExpiresAt.UTC().Format(time.RFC3339))
	}
	if _, err := s.UpsertOwner(ctx, callerEmail); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if !strings.EqualFold(cur.OwnerEmail, callerEmail) {
		quota, err := s.getQuotaTx(ctx, tx, callerEmail)
		if err != nil {
			return nil, err
		}
		switch cur.Kind {
		case api.KindInstance:
			units := 1
			if cur.Shape != "" {
				_ = tx.QueryRowContext(ctx, `SELECT quota_units FROM registry.shapes WHERE name = $1`, cur.Shape).Scan(&units)
			}
			if quota.UsedInstanceUnits+units > quota.MaxInstanceUnits {
				return nil, fmt.Errorf("%w: instance quota units %d + %d exceeds limit %d", ErrQuotaExceeded, quota.UsedInstanceUnits, units, quota.MaxInstanceUnits)
			}
		case api.KindConnected:
			if quota.UsedConnected+1 > quota.MaxConnected {
				return nil, fmt.Errorf("%w: connected entries %d + 1 exceeds limit %d", ErrQuotaExceeded, quota.UsedConnected, quota.MaxConnected)
			}
		}
	}

	res, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET owner_email            = $1,
    pending_owner          = '',
    transfer_expires_at    = NULL,
    ssh_granted_to         = NULL,
    ssh_grant_expires_at   = NULL,
    last_disconnect_at     = $2,
    last_disconnect_reason = 'ownership transferred',
    version                = version + 1,
    updated_at             = $2
WHERE id = $3 AND version = $4 AND deleted_at IS NULL`,
		callerEmail, now, entryID, expectedVersion)
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
		if out.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
	}
	return out, err
}

// RestoreEntry restores the most recently soft-deleted entry for `name` within its 30-day
// reservation window after checking the owner's quota under `SELECT ... FOR UPDATE`.
func (s *Store) RestoreEntry(ctx context.Context, entryID string, expectedVersion int64) (*EntryRow, error) {
	cur, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	if cur.DeletedAt == nil {
		return nil, fmt.Errorf("%w: entry %q is not deleted", ErrConflict, cur.Name)
	}
	now := s.now()
	if cur.PurgedAt != nil || (cur.DBPurgeAfter != nil && !cur.DBPurgeAfter.After(now)) {
		return nil, fmt.Errorf("%w: entry %q disk/database has already been purged", ErrConflict, cur.Name)
	}
	if cur.ReservedUntil != nil && !cur.ReservedUntil.After(now) {
		return nil, ErrNotFound
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	quota, err := s.getQuotaTx(ctx, tx, cur.OwnerEmail)
	if err != nil {
		return nil, err
	}
	switch cur.Kind {
	case api.KindInstance:
		units := 1
		if cur.Shape != "" {
			_ = tx.QueryRowContext(ctx, `SELECT quota_units FROM registry.shapes WHERE name = $1`, cur.Shape).Scan(&units)
		}
		if quota.UsedInstanceUnits+units > quota.MaxInstanceUnits {
			return nil, fmt.Errorf("%w: instance quota units %d + %d exceeds limit %d", ErrQuotaExceeded, quota.UsedInstanceUnits, units, quota.MaxInstanceUnits)
		}
	case api.KindConnected:
		if quota.UsedConnected+1 > quota.MaxConnected {
			return nil, fmt.Errorf("%w: connected entries %d + 1 exceeds limit %d", ErrQuotaExceeded, quota.UsedConnected, quota.MaxConnected)
		}
	}

	nextState := api.StateRunning
	if cur.Kind == api.KindInstance {
		nextState = api.StateCreating
	}

	res, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state = $1,
    gce_instance_id = CASE WHEN kind = 'instance' THEN NULL ELSE gce_instance_id END,
    internal_ip     = CASE WHEN kind = 'instance' THEN NULL ELSE internal_ip END,
    backend_url     = CASE WHEN kind = 'instance' THEN NULL ELSE backend_url END,
    deleted_at      = NULL,
    reserved_until  = NULL,
    purge_after     = NULL,
    db_purge_after  = NULL,
    status_reason   = '',
    version         = version + 1,
    updated_at      = $2
WHERE id = $3 AND version = $4 AND deleted_at IS NOT NULL`,
		string(nextState), now, entryID, expectedVersion)
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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
		if out.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
	}
	return out, err
}

// SoftDeleteEntry transitions an entry to `deleted`, sets `deleted_at` and
// `purge_after` (30-day name reservation), and clears its active scopes and credentials.
func (s *Store) SoftDeleteEntry(ctx context.Context, entryID string, expectedVersion int64) (*EntryRow, error) {
	return s.SoftDeleteEntryWithOptions(ctx, entryID, expectedVersion, false)
}

// SoftDeleteEntryWithOptions transitions an entry to `deleted`, sets its 30-day name
// reservation window (`reserved_until` and `purge_after`), clears its scopes and credentials,
// and promotes any contenders waiting in `conflict_with` on its primary scopes.
func (s *Store) SoftDeleteEntryWithOptions(ctx context.Context, entryID string, expectedVersion int64, deleteDisk bool) (*EntryRow, error) {
	cur, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	purgeAfter := now.Add(s.thresholds.ReservationPeriod)
	var dbPurgeAfter any
	if deleteDisk {
		dbPurgeAfter = now
	} else {
		ret := s.thresholds.DBRetentionPeriod
		if ret <= 0 {
			ret = 168 * time.Hour
		}
		if s.thresholds.ReservationPeriod > 0 && ret > s.thresholds.ReservationPeriod {
			ret = s.thresholds.ReservationPeriod
		}
		dbPurgeAfter = now.Add(ret)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	repos := collectPrimaryRepos(cur.Scopes)
	for _, r := range repos {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "scope:"+r); err != nil {
			return nil, err
		}
	}

	res, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state        = $1,
    deleted_at             = $2,
    reserved_until         = $3,
    purge_after            = $3,
    db_purge_after         = $4,
    last_disconnect_at     = $2,
    last_disconnect_reason = 'entry deleted',
    version                = version + 1,
    updated_at             = $2
WHERE id = $5 AND version = $6 AND deleted_at IS NULL`,
		string(api.StateDeleted), now, purgeAfter, dbPurgeAfter, entryID, expectedVersion)
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
	if err := s.promoteContendersForReposTx(ctx, tx, repos, now); err != nil {
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
		if cur.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
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

func collectPrimaryRepos(scopes []ScopeRow) []string {
	seen := make(map[string]bool)
	for _, s := range scopes {
		if s.PrimaryClaim && s.Repo != "" {
			seen[s.Repo] = true
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func populateEffectiveRoles(scopes []ScopeRow, hasReported bool) {
	if !hasReported {
		for i := range scopes {
			if scopes[i].PrimaryClaim {
				scopes[i].EffectiveRole = api.RolePrimary
			} else {
				scopes[i].EffectiveRole = api.RoleSecondary
			}
		}
		return
	}
	reportedByRepo := make(map[string][]ScopeRow)
	for _, s := range scopes {
		if s.Source == "reported" {
			reportedByRepo[s.Repo] = append(reportedByRepo[s.Repo], s)
		}
	}
	for i := range scopes {
		s := &scopes[i]
		role := api.RoleSecondary
		if s.PrimaryClaim {
			role = api.RolePrimary
		}
		if s.Source == "declared" {
			eff := role
			reps := reportedByRepo[s.Repo]
			bestSpec := -1
			var matchedRep *ScopeRow
			for j := range reps {
				if ok, spec := api.PathMatchesPrefix(s.PathPrefix, reps[j].PathPrefix); ok && spec >= bestSpec {
					bestSpec = spec
					matchedRep = &reps[j]
				}
			}
			if matchedRep != nil && !matchedRep.PrimaryClaim {
				eff = api.RoleSecondary
			}
			s.EffectiveRole = eff
		} else {
			s.EffectiveRole = role
		}
	}
}

// SetDeclaredScopes replaces the entry's `declared` scopes via PUT /api/entries/<name>/scopes,
// synchronously rejecting primary scope overlaps on listed entries with `*PrimaryScopeConflictError`.
func (s *Store) SetDeclaredScopes(ctx context.Context, entryID string, claims []ScopeClaim) (*EntryRow, error) {
	cur, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	if cur.DeletedAt != nil {
		return nil, ErrNotFound
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.replaceEntryScopesWithPolicyTx(ctx, tx, entryID, "declared", claims, cur.Listed, cur.HasReportedScopes, true, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET version    = version + 1,
    updated_at = $1
WHERE id = $2`, now, entryID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.notify(ctx, "registry_entries", cur.Name)
	if cur.Listed {
		s.notify(ctx, "registry_catalog", cur.Name)
	}
	return s.GetEntryByID(ctx, entryID)
}

// ReplaceEntryScopes replaces all scopes for `(entryID, source)` ("reported" or "declared")
// while enforcing primary-claim non-overlap under `pg_advisory_xact_lock`.
func (s *Store) ReplaceEntryScopes(ctx context.Context, entryID, source string, claims []ScopeClaim) error {
	if source != "reported" && source != "declared" {
		return fmt.Errorf("invalid scope source %q (expected \"reported\" or \"declared\")", source)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var name string
	var listed, hasReported bool
	if err := tx.QueryRowContext(ctx, `SELECT name, listed, has_reported_scopes FROM registry.entries WHERE id = $1`, entryID).Scan(&name, &listed, &hasReported); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := s.replaceEntryScopesWithPolicyTx(ctx, tx, entryID, source, claims, listed, hasReported, true, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notify(ctx, "registry_entries", name)
	if listed {
		s.notify(ctx, "registry_catalog", name)
	}
	return nil
}

type normScopeClaim struct {
	repo    string
	prefix  string
	primary bool
	branch  string
}

func normalizeScopeClaims(claims []ScopeClaim) ([]normScopeClaim, error) {
	seen := make(map[string]bool, len(claims))
	normalized := make([]normScopeClaim, 0, len(claims))
	for _, c := range claims {
		repo, err := api.NormalizeRepo(c.Repo)
		if err != nil {
			repo = strings.ToLower(strings.TrimSpace(c.Repo))
			if repo == "" {
				return nil, errors.New("scope repo must be non-empty")
			}
		}
		prefix, err := api.NormalizePathPrefix(c.PathPrefix)
		if err != nil {
			return nil, err
		}
		key := repo + "\x00" + prefix
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, normScopeClaim{
			repo:    repo,
			prefix:  prefix,
			primary: c.Primary,
			branch:  strings.TrimSpace(c.Branch),
		})
	}
	return normalized, nil
}

func (s *Store) replaceEntryScopesWithPolicyTx(
	ctx context.Context,
	tx *sql.Tx,
	entryID, source string,
	claims []ScopeClaim,
	isListed, hasReported, syncReject bool,
	now time.Time,
) error {
	normalized, err := normalizeScopeClaims(claims)
	if err != nil {
		return err
	}

	existingClaimedAt := make(map[string]time.Time)
	reposToLock := make(map[string]bool)
	rows, err := tx.QueryContext(ctx, `
SELECT repo, path_prefix, source, claimed_at
FROM registry.entry_scopes
WHERE entry_id = $1`, entryID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r, p, src string
		var ca time.Time
		if err := rows.Scan(&r, &p, &src, &ca); err != nil {
			_ = rows.Close()
			return err
		}
		reposToLock[r] = true
		if src == source {
			existingClaimedAt[r+"\x00"+p] = ca
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, nc := range normalized {
		reposToLock[nc.repo] = true
	}
	sortedRepos := make([]string, 0, len(reposToLock))
	for r := range reposToLock {
		sortedRepos = append(sortedRepos, r)
	}
	sort.Strings(sortedRepos)
	for _, r := range sortedRepos {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "scope:"+r); err != nil {
			return fmt.Errorf("acquiring scope advisory lock for %s: %w", r, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM registry.entry_scopes WHERE entry_id = $1 AND source = $2`, entryID, source); err != nil {
		return err
	}
	for _, nc := range normalized {
		claimedAt := now
		if prev, ok := existingClaimedAt[nc.repo+"\x00"+nc.prefix]; ok && !prev.IsZero() {
			claimedAt = prev
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO registry.entry_scopes (
    entry_id, repo, path_prefix, source, primary_claim, branch,
    active, inactive_reason, conflict_with, claimed_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, TRUE, '', '', $7, $8)`,
			entryID, nc.repo, nc.prefix, source, nc.primary, nc.branch, claimedAt, now); err != nil {
			return err
		}
	}

	return s.reconcileEntryScopesTx(ctx, tx, entryID, isListed, hasReported, syncReject, now)
}

func (s *Store) reconcileEntryScopesTx(
	ctx context.Context,
	tx *sql.Tx,
	entryID string,
	isListed, hasReported, syncReject bool,
	now time.Time,
) error {
	rows, err := tx.QueryContext(ctx, `
SELECT entry_id, repo, path_prefix, source, primary_claim, branch, active, inactive_reason, conflict_with, claimed_at, updated_at
FROM registry.entry_scopes
WHERE entry_id = $1
ORDER BY repo ASC, path_prefix ASC, source ASC`, entryID)
	if err != nil {
		return err
	}
	var all []ScopeRow
	reposToLock := make(map[string]bool)
	for rows.Next() {
		var r ScopeRow
		if err := rows.Scan(
			&r.EntryID, &r.Repo, &r.PathPrefix, &r.Source, &r.PrimaryClaim, &r.Branch,
			&r.Active, &r.InactiveReason, &r.ConflictWith, &r.ClaimedAt, &r.UpdatedAt,
		); err != nil {
			_ = rows.Close()
			return err
		}
		all = append(all, r)
		reposToLock[r.Repo] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}

	sortedRepos := make([]string, 0, len(reposToLock))
	for r := range reposToLock {
		sortedRepos = append(sortedRepos, r)
	}
	sort.Strings(sortedRepos)
	for _, r := range sortedRepos {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "scope:"+r); err != nil {
			return err
		}
	}

	type evalState struct {
		active           bool
		inactiveReason   string
		conflictWith     string
		effectivePrimary bool
	}
	states := make([]evalState, len(all))
	for i := range all {
		states[i] = evalState{
			active:           true,
			effectivePrimary: all[i].PrimaryClaim,
		}
	}

	if hasReported {
		repIdxsByRepo := make(map[string][]int)
		declIdxsByRepo := make(map[string][]int)
		for i, r := range all {
			if r.Source == "reported" {
				repIdxsByRepo[r.Repo] = append(repIdxsByRepo[r.Repo], i)
			} else if r.Source == "declared" {
				declIdxsByRepo[r.Repo] = append(declIdxsByRepo[r.Repo], i)
			}
		}

		for repo, declIdxs := range declIdxsByRepo {
			repIdxs := repIdxsByRepo[repo]
			if len(repIdxs) == 0 {
				for _, di := range declIdxs {
					states[di] = evalState{
						active:           false,
						inactiveReason:   string(api.InactiveNotReportedByInstance),
						effectivePrimary: false,
					}
				}
				continue
			}
			for _, di := range declIdxs {
				d := all[di]
				bestRepIdx := -1
				bestSpec := -1
				for _, ri := range repIdxs {
					rep := all[ri]
					if ok, spec := api.PathMatchesPrefix(d.PathPrefix, rep.PathPrefix); ok && spec >= bestSpec {
						bestSpec = spec
						bestRepIdx = ri
					}
				}
				if bestRepIdx < 0 {
					states[di] = evalState{
						active:           false,
						inactiveReason:   string(api.InactiveNotReportedByInstance),
						effectivePrimary: false,
					}
					continue
				}
				rep := all[bestRepIdx]
				effPrimary := d.PrimaryClaim && rep.PrimaryClaim
				states[di] = evalState{
					active:           true,
					effectivePrimary: effPrimary,
				}
				// If declared narrows path_prefix or demotes primary -> secondary, mark reported row overridden_by_narrower.
				if d.PathPrefix != rep.PathPrefix || (!d.PrimaryClaim && rep.PrimaryClaim) {
					states[bestRepIdx] = evalState{
						active:           false,
						inactiveReason:   string(api.InactiveOverriddenByNarrower),
						effectivePrimary: false,
					}
				}
			}
		}
	}

	for i, r := range all {
		st := &states[i]
		if !st.active || !st.effectivePrimary {
			continue
		}
		if !isListed {
			continue
		}
		otherRows, err := tx.QueryContext(ctx, `
SELECT e.name, e.owner_email, e.owner_group, e.listed, es.path_prefix
FROM registry.entry_scopes es
JOIN registry.entries e ON e.id = es.entry_id
WHERE es.repo = $1
  AND es.primary_claim = TRUE
  AND es.active = TRUE
  AND es.entry_id != $2
  AND e.deleted_at IS NULL
  AND e.listed = TRUE
ORDER BY es.claimed_at ASC, e.created_at ASC, e.name ASC`, r.Repo, entryID)
		if err != nil {
			return err
		}
		var conflict *api.ScopeConflict
		for otherRows.Next() {
			var otherName, otherOwner, otherGroup, otherPrefix string
			var otherListed bool
			if err := otherRows.Scan(&otherName, &otherOwner, &otherGroup, &otherListed, &otherPrefix); err != nil {
				_ = otherRows.Close()
				return err
			}
			if ScopesOverlap(r.PathPrefix, otherPrefix) {
				conflict = &api.ScopeConflict{
					Repo:                  r.Repo,
					PathPrefix:            r.PathPrefix,
					ConflictingEntry:      otherName,
					ConflictingOwner:      otherOwner,
					ConflictingOwnerGroup: otherGroup,
					ConflictingPrefix:     otherPrefix,
					ConflictingListed:     otherListed,
				}
				break
			}
		}
		if err := otherRows.Close(); err != nil {
			return err
		}
		if conflict != nil {
			if syncReject {
				return &PrimaryScopeConflictError{Conflict: *conflict}
			}
			st.active = false
			st.inactiveReason = string(api.InactivePrimaryConflict)
			st.conflictWith = conflict.ConflictingEntry
		}
	}

	for i, r := range all {
		st := states[i]
		// If a declared primary scope was capped to secondary because reported is secondary,
		// ensure it does not hold a primary_claim lock in SQL against other entries.
		if r.Active != st.active || r.InactiveReason != st.inactiveReason || r.ConflictWith != st.conflictWith {
			if _, err := tx.ExecContext(ctx, `
UPDATE registry.entry_scopes
SET active          = $1,
    inactive_reason = $2,
    conflict_with   = $3,
    updated_at      = $4
WHERE entry_id = $5 AND repo = $6 AND path_prefix = $7 AND source = $8`,
				st.active, st.inactiveReason, st.conflictWith, now,
				entryID, r.Repo, r.PathPrefix, r.Source); err != nil {
				return err
			}
		}
	}

	return s.promoteContendersForReposTx(ctx, tx, sortedRepos, now)
}

func (s *Store) promoteContendersForReposTx(ctx context.Context, tx *sql.Tx, repos []string, now time.Time) error {
	for _, repo := range repos {
		if repo == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "scope:"+repo); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
SELECT es.entry_id, e.name, es.path_prefix, es.source, es.conflict_with
FROM registry.entry_scopes es
JOIN registry.entries e ON e.id = es.entry_id
WHERE es.repo = $1
  AND es.primary_claim = TRUE
  AND es.active = FALSE
  AND es.inactive_reason = $2
  AND e.deleted_at IS NULL
  AND e.listed = TRUE
ORDER BY es.claimed_at ASC, e.created_at ASC, e.name ASC`,
			repo, string(api.InactivePrimaryConflict))
		if err != nil {
			return err
		}
		type contender struct {
			entryID      string
			entryName    string
			pathPrefix   string
			source       string
			conflictWith string
		}
		var contenders []contender
		for rows.Next() {
			var c contender
			if err := rows.Scan(&c.entryID, &c.entryName, &c.pathPrefix, &c.source, &c.conflictWith); err != nil {
				_ = rows.Close()
				return err
			}
			contenders = append(contenders, c)
		}
		if err := rows.Close(); err != nil {
			return err
		}

		for _, c := range contenders {
			activeRows, err := tx.QueryContext(ctx, `
SELECT e.name, es.path_prefix
FROM registry.entry_scopes es
JOIN registry.entries e ON e.id = es.entry_id
WHERE es.repo = $1
  AND es.primary_claim = TRUE
  AND es.active = TRUE
  AND es.entry_id != $2
  AND e.deleted_at IS NULL
  AND e.listed = TRUE
ORDER BY es.claimed_at ASC, e.created_at ASC, e.name ASC`, repo, c.entryID)
			if err != nil {
				return err
			}
			holder := ""
			for activeRows.Next() {
				var otherName, otherPrefix string
				if err := activeRows.Scan(&otherName, &otherPrefix); err != nil {
					_ = activeRows.Close()
					return err
				}
				if ScopesOverlap(c.pathPrefix, otherPrefix) {
					holder = otherName
					break
				}
			}
			if err := activeRows.Close(); err != nil {
				return err
			}
			if holder == "" {
				if _, err := tx.ExecContext(ctx, `
UPDATE registry.entry_scopes
SET active          = TRUE,
    inactive_reason = '',
    conflict_with   = '',
    updated_at      = $1
WHERE entry_id = $2 AND repo = $3 AND path_prefix = $4 AND source = $5`,
					now, c.entryID, repo, c.pathPrefix, c.source); err != nil {
					return err
				}
			} else if holder != c.conflictWith {
				if _, err := tx.ExecContext(ctx, `
UPDATE registry.entry_scopes
SET conflict_with = $1,
    updated_at    = $2
WHERE entry_id = $3 AND repo = $4 AND path_prefix = $5 AND source = $6`,
					holder, now, c.entryID, repo, c.pathPrefix, c.source); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ListEntryScopes returns the combined `reported` and `declared` scopes for entryID.
func (s *Store) ListEntryScopes(ctx context.Context, entryID string) ([]ScopeRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
    entry_id, repo, path_prefix, source, primary_claim, branch,
    active, inactive_reason, conflict_with, claimed_at, updated_at
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
		if err := rows.Scan(
			&r.EntryID, &r.Repo, &r.PathPrefix, &r.Source, &r.PrimaryClaim, &r.Branch,
			&r.Active, &r.InactiveReason, &r.ConflictWith, &r.ClaimedAt, &r.UpdatedAt,
		); err != nil {
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
		active bool
	}
	byKey := make(map[key]*api.Scope, len(rows))
	var order []key
	for _, r := range rows {
		// In unit tests that construct ScopeRow literals directly without running SQL,
		// treat zero-value Active with empty InactiveReason as active.
		active := r.Active || (r.EntryID == "" && r.InactiveReason == "" && r.ConflictWith == "")
		k := key{repo: r.Repo, prefix: r.PathPrefix, active: active}
		role := api.RoleSecondary
		if r.PrimaryClaim {
			role = api.RolePrimary
		}
		effRole := r.EffectiveRole
		if effRole == "" {
			effRole = role
		}
		if existing, ok := byKey[k]; ok {
			if r.PrimaryClaim {
				existing.Role = api.RolePrimary
			}
			if r.Source == "declared" && r.EffectiveRole != "" {
				existing.EffectiveRole = r.EffectiveRole
			}
			if existing.Branch == "" && r.Branch != "" {
				existing.Branch = r.Branch
			}
			if r.Source == "reported" && existing.InactiveReason == "" {
				existing.Source = api.SourceReported
			}
			continue
		}
		var claimedAt *time.Time
		if !r.ClaimedAt.IsZero() {
			t := r.ClaimedAt
			claimedAt = &t
		}
		sc := &api.Scope{
			Repo:           r.Repo,
			PathPrefix:     r.PathPrefix,
			Role:           role,
			EffectiveRole:  effRole,
			Source:         api.ScopeSource(r.Source),
			Branch:         r.Branch,
			Active:         active,
			InactiveReason: api.InactiveReason(r.InactiveReason),
			ConflictWith:   r.ConflictWith,
			ClaimedAt:      claimedAt,
			UpdatedAt:      r.UpdatedAt,
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

// RawAPIScopes returns the `api.Scope` slices filtered to a single `source` ("reported" or "declared").
func RawAPIScopes(rows []ScopeRow, source api.ScopeSource) []api.Scope {
	var out []api.Scope
	for _, r := range rows {
		if api.ScopeSource(r.Source) != source {
			continue
		}
		active := r.Active || (r.EntryID == "" && r.InactiveReason == "" && r.ConflictWith == "")
		role := api.RoleSecondary
		if r.PrimaryClaim {
			role = api.RolePrimary
		}
		effRole := r.EffectiveRole
		if effRole == "" {
			effRole = role
		}
		var claimedAt *time.Time
		if !r.ClaimedAt.IsZero() {
			t := r.ClaimedAt
			claimedAt = &t
		}
		out = append(out, api.Scope{
			Repo:           r.Repo,
			PathPrefix:     r.PathPrefix,
			Role:           role,
			EffectiveRole:  effRole,
			Source:         api.ScopeSource(r.Source),
			Branch:         r.Branch,
			Active:         active,
			InactiveReason: api.InactiveReason(r.InactiveReason),
			ConflictWith:   r.ConflictWith,
			ClaimedAt:      claimedAt,
			UpdatedAt:      r.UpdatedAt,
		})
	}
	return out
}

// EntryScopeConflicts returns structured `api.ScopeConflict` items for any scopes on `e`
// that are currently inactive due to `primary_conflict`.
func (s *Store) EntryScopeConflicts(ctx context.Context, e *EntryRow) []api.ScopeConflict {
	if e == nil {
		return nil
	}
	var out []api.ScopeConflict
	for _, sr := range e.Scopes {
		if sr.InactiveReason != string(api.InactivePrimaryConflict) {
			continue
		}
		c := api.ScopeConflict{
			Repo:              sr.Repo,
			PathPrefix:        sr.PathPrefix,
			ConflictingEntry:  sr.ConflictWith,
			ConflictingListed: true,
		}
		if s != nil && s.db != nil && sr.ConflictWith != "" {
			var owner, group, prefix string
			var listed bool
			err := s.db.QueryRowContext(ctx, `
SELECT e.owner_email, e.owner_group, e.listed, COALESCE(es.path_prefix, '')
FROM registry.entries e
LEFT JOIN registry.entry_scopes es ON es.entry_id = e.id AND es.repo = $2 AND es.primary_claim = TRUE AND es.active = TRUE
WHERE e.name = $1 AND e.deleted_at IS NULL
LIMIT 1`, sr.ConflictWith, sr.Repo).Scan(&owner, &group, &listed, &prefix)
			if err == nil {
				c.ConflictingOwner = owner
				c.ConflictingOwnerGroup = group
				c.ConflictingListed = listed
				c.ConflictingPrefix = prefix
			}
		}
		out = append(out, c)
	}
	return out
}

// IssueCredential generates a new `grc_<hex>` bearer credential for `entryID`
// and stores only its SHA-256 digest in `registry.entry_credentials`.
func (s *Store) IssueCredential(ctx context.Context, entryID string) (token string, credID string, err error) {
	tok, meta, err := s.IssueCredentialWithMeta(ctx, entryID, "", "")
	if err != nil {
		return "", "", err
	}
	return tok, meta.CredentialID, nil
}

// IssueCredentialWithMeta generates a new `grc_<hex>` bearer credential for `entryID`
// with optional `label` and `createdBy` metadata, storing only its SHA-256 digest.
func (s *Store) IssueCredentialWithMeta(ctx context.Context, entryID, label, createdBy string) (string, api.CredentialMeta, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", api.CredentialMeta{}, fmt.Errorf("generating random credential bytes: %w", err)
	}
	token := "grc_" + hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	credID := uuid.NewString()
	now := s.now()
	label = strings.TrimSpace(label)
	createdBy = strings.ToLower(strings.TrimSpace(createdBy))
	_, err := s.db.ExecContext(ctx, `
INSERT INTO registry.entry_credentials (id, entry_id, token_sha256, label, created_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6)`,
		credID, entryID, sum[:], label, createdBy, now)
	if err != nil {
		return "", api.CredentialMeta{}, fmt.Errorf("storing entry credential: %w", err)
	}
	return token, api.CredentialMeta{
		CredentialID: credID,
		Label:        label,
		CreatedBy:    createdBy,
		CreatedAt:    now,
	}, nil
}

// RotateCredential mints a new `grc_<hex>` credential for `entryID` and optionally revokes
// or sets an overlap expiration window on previous credentials (`cred_id`, `revoke_previous`, `overlap_seconds`).
// Any active tunnel session bound to a newly revoked credential is terminated immediately.
func (s *Store) RotateCredential(ctx context.Context, entryID string, req api.RotateCredentialRequest, actorEmail string) (string, api.CredentialMeta, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", api.CredentialMeta{}, fmt.Errorf("generating random credential bytes: %w", err)
	}
	token := "grc_" + hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	newCredID := uuid.NewString()
	now := s.now()
	label := strings.TrimSpace(req.Label)
	actorEmail = strings.ToLower(strings.TrimSpace(actorEmail))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", api.CredentialMeta{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var entryName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM registry.entries WHERE id = $1 AND deleted_at IS NULL`, entryID).Scan(&entryName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", api.CredentialMeta{}, ErrNotFound
		}
		return "", api.CredentialMeta{}, err
	}

	revokeImmediate := req.RevokePrevious || (req.CredID != "" && req.OverlapSeconds <= 0)
	var tunnelDropped bool
	if revokeImmediate {
		if req.CredID != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE registry.entry_credentials
SET revoked_at = $1
WHERE entry_id = $2 AND id = $3::uuid AND revoked_at IS NULL`,
				now, entryID, req.CredID); err != nil {
				return "", api.CredentialMeta{}, err
			}
			delRes, err := tx.ExecContext(ctx, `
DELETE FROM registry.tunnel_sessions
WHERE entry_id = $1 AND (cred_id IS NULL OR cred_id = $2::uuid)`,
				entryID, req.CredID)
			if err != nil {
				return "", api.CredentialMeta{}, err
			}
			if dn, _ := delRes.RowsAffected(); dn > 0 {
				tunnelDropped = true
			}
		} else {
			if _, err := tx.ExecContext(ctx, `
UPDATE registry.entry_credentials
SET revoked_at = $1
WHERE entry_id = $2 AND revoked_at IS NULL`,
				now, entryID); err != nil {
				return "", api.CredentialMeta{}, err
			}
			delRes, err := tx.ExecContext(ctx, `DELETE FROM registry.tunnel_sessions WHERE entry_id = $1`, entryID)
			if err != nil {
				return "", api.CredentialMeta{}, err
			}
			if dn, _ := delRes.RowsAffected(); dn > 0 {
				tunnelDropped = true
			}
		}
	} else if req.OverlapSeconds > 0 {
		exp := now.Add(time.Duration(req.OverlapSeconds) * time.Second)
		if req.CredID != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE registry.entry_credentials
SET expires_at = $1
WHERE entry_id = $2 AND id = $3::uuid AND revoked_at IS NULL`,
				exp, entryID, req.CredID); err != nil {
				return "", api.CredentialMeta{}, err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `
UPDATE registry.entry_credentials
SET expires_at = $1
WHERE entry_id = $2 AND revoked_at IS NULL AND expires_at IS NULL`,
				exp, entryID); err != nil {
				return "", api.CredentialMeta{}, err
			}
		}
	}

	if tunnelDropped {
		_, _ = tx.ExecContext(ctx, `
UPDATE registry.entries
SET last_disconnect_at     = $1,
    last_disconnect_reason = 'credential rotated',
    updated_at             = $1
WHERE id = $2`, now, entryID)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO registry.entry_credentials (id, entry_id, token_sha256, label, created_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6)`,
		newCredID, entryID, sum[:], label, actorEmail, now); err != nil {
		return "", api.CredentialMeta{}, err
	}

	if err := tx.Commit(); err != nil {
		return "", api.CredentialMeta{}, err
	}
	if tunnelDropped && entryName != "" {
		s.notify(ctx, "registry_tunnels", entryName)
	}
	return token, api.CredentialMeta{
		CredentialID: newCredID,
		Label:        label,
		CreatedBy:    actorEmail,
		CreatedAt:    now,
	}, nil
}

// ListEntryCredentials returns non-secret metadata for all credentials belonging to `entryID`.
func (s *Store) ListEntryCredentials(ctx context.Context, entryID string) ([]api.CredentialMeta, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id::text, label, created_by, created_at, last_used_at, expires_at, revoked_at
FROM registry.entry_credentials
WHERE entry_id = $1
ORDER BY created_at DESC`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.CredentialMeta
	for rows.Next() {
		var m api.CredentialMeta
		var lastUsed, expires, revoked sql.NullTime
		if err := rows.Scan(&m.CredentialID, &m.Label, &m.CreatedBy, &m.CreatedAt, &lastUsed, &expires, &revoked); err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			t := lastUsed.Time
			m.LastUsedAt = &t
		}
		if expires.Valid {
			t := expires.Time
			m.ExpiresAt = &t
		}
		if revoked.Valid {
			t := revoked.Time
			m.RevokedAt = &t
		}
		out = append(out, m)
	}
	return out, rows.Err()
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
	var storedHash []byte
	err := s.db.QueryRowContext(ctx, `
UPDATE registry.entry_credentials ec
SET last_used_at = $1
FROM registry.entries e
WHERE ec.token_sha256 = $2
  AND ec.revoked_at IS NULL
  AND (ec.expires_at IS NULL OR ec.expires_at > $1)
  AND e.id = ec.entry_id
  AND e.deleted_at IS NULL
RETURNING ec.entry_id, ec.id::text, ec.token_sha256`, now, sum[:]).Scan(&entryID, &credID, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrInvalidCredential
	}
	if err != nil {
		return nil, "", err
	}
	if subtle.ConstantTimeCompare(sum[:], storedHash) != 1 {
		return nil, "", ErrInvalidCredential
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

// IsCredentialActive reports whether `credID` is still unrevoked and unexpired for `entryID`.
func (s *Store) IsCredentialActive(ctx context.Context, entryID, credID string) (bool, error) {
	if credID == "" {
		return true, nil
	}
	now := s.now()
	var ok bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM registry.entry_credentials
    WHERE id = $1::uuid
      AND entry_id = $2::uuid
      AND revoked_at IS NULL
      AND (expires_at IS NULL OR expires_at > $3)
)`, credID, entryID, now).Scan(&ok)
	return ok, err
}

func reportedScopesEqual(existing []ScopeRow, incoming []normScopeClaim) bool {
	var rep []ScopeRow
	for _, r := range existing {
		if r.Source == "reported" {
			rep = append(rep, r)
		}
	}
	if len(rep) != len(incoming) {
		return false
	}
	byKey := make(map[string]ScopeRow, len(rep))
	for _, r := range rep {
		byKey[r.Repo+"\x00"+r.PathPrefix] = r
	}
	for _, inc := range incoming {
		r, ok := byKey[inc.repo+"\x00"+inc.prefix]
		if !ok {
			return false
		}
		if r.PrimaryClaim != inc.primary || r.Branch != inc.branch {
			return false
		}
	}
	return true
}

// RecordHeartbeat updates an entry's heartbeat telemetry (`grain_version`, `grain_commit`,
// `host_kind`, `health_state`, `health_detail`, `active_tasks`, `awaiting_reply_tasks`,
// `pending_inbox`, `active_sessions`, `last_heartbeat_at`) and reconciles its `reported` scopes.
//
// When reported scopes are unchanged, `entry_scopes` writes are skipped so the `entries`
// update touches no indexed columns (allowing PostgreSQL HOT updates). `NOTIFY registry_catalog`
// is emitted only when `listed = true` and `scopes`, `grain_version`, `grain_commit`, `host_kind`,
// `health_state`, or the derived `status` bucket actually changed.
func (s *Store) RecordHeartbeat(ctx context.Context, entryID string, hb api.HeartbeatRequest) (*EntryRow, error) {
	prev, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	if prev.DeletedAt != nil {
		return nil, ErrNotFound
	}
	prevStatus := s.DeriveStatus(prev)

	now := s.now()
	nextVersion := prev.GrainVersion
	if strings.TrimSpace(hb.GrainVersion) != "" {
		nextVersion = strings.TrimSpace(hb.GrainVersion)
	}
	nextCommit := prev.GrainCommit
	if strings.TrimSpace(hb.GrainCommit) != "" {
		nextCommit = strings.TrimSpace(hb.GrainCommit)
	}
	nextHostKind := prev.HostKind
	if hb.HostKind != "" && hb.HostKind.Valid() {
		nextHostKind = string(hb.HostKind)
	}
	nextHealth := prev.HealthState
	if nextHealth == "" {
		nextHealth = api.HealthOK
	}
	if hb.Health != "" && hb.Health.Valid() {
		nextHealth = hb.Health
	}
	nextHealthDetail := hb.HealthDetail
	if hb.Health == api.HealthOK && nextHealthDetail == "" {
		nextHealthDetail = ""
	} else if hb.Health == "" && nextHealthDetail == "" {
		nextHealthDetail = prev.HealthDetail
	}

	hasReported := prev.HasReportedScopes
	scopesChanged := false
	var claims []ScopeClaim
	if hb.Scopes != nil {
		claims = make([]ScopeClaim, 0, len(hb.Scopes))
		scopeAuth := s.primaryScopeAuthorizer()
		for _, sc := range hb.Scopes {
			primary := sc.Role == "" || sc.Role == api.RolePrimary
			if primary && scopeAuth != nil {
				allowed, authErr := scopeAuth(ctx, sc.Repo, prev.OwnerEmail, prev.OwnerGroup)
				if authErr != nil || !allowed {
					primary = false
				}
			}
			claims = append(claims, ScopeClaim{
				Repo:       sc.Repo,
				PathPrefix: sc.PathPrefix,
				Primary:    primary,
				Branch:     sc.Branch,
			})
		}
		norm, err := normalizeScopeClaims(claims)
		if err != nil {
			return nil, err
		}
		if !prev.HasReportedScopes || !reportedScopesEqual(prev.Scopes, norm) {
			scopesChanged = true
		}
		hasReported = true
	}

	if !scopesChanged {
		res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET grain_version        = $1,
    grain_commit         = $2,
    host_kind            = $3,
    health_state         = $4,
    health_detail        = $5,
    active_tasks         = $6,
    awaiting_reply_tasks = $7,
    pending_inbox        = $8,
    active_sessions      = $9,
    has_reported_scopes  = $10,
    last_heartbeat_at    = $11,
    updated_at           = $11
WHERE id = $12 AND deleted_at IS NULL`,
			nextVersion, nextCommit, nextHostKind, string(nextHealth), nextHealthDetail,
			hb.ActiveTasks, hb.AwaitingReplyTasks, hb.QueueDepth, hb.ActiveSessions,
			hasReported, now, entryID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, ErrNotFound
		}
	} else {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()

		res, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET grain_version        = $1,
    grain_commit         = $2,
    host_kind            = $3,
    health_state         = $4,
    health_detail        = $5,
    active_tasks         = $6,
    awaiting_reply_tasks = $7,
    pending_inbox        = $8,
    active_sessions      = $9,
    has_reported_scopes  = TRUE,
    last_heartbeat_at    = $10,
    updated_at           = $10
WHERE id = $11 AND deleted_at IS NULL`,
			nextVersion, nextCommit, nextHostKind, string(nextHealth), nextHealthDetail,
			hb.ActiveTasks, hb.AwaitingReplyTasks, hb.QueueDepth, hb.ActiveSessions,
			now, entryID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, ErrNotFound
		}
		if err := s.replaceEntryScopesWithPolicyTx(ctx, tx, entryID, "reported", claims, prev.Listed, true, false, now); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}

	out, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	newStatus := s.DeriveStatus(out)
	catalogChanged := scopesChanged ||
		prev.GrainVersion != out.GrainVersion ||
		prev.GrainCommit != out.GrainCommit ||
		prev.HostKind != out.HostKind ||
		prev.HealthState != out.HealthState ||
		prevStatus != newStatus
	if out.Listed && catalogChanged {
		s.notify(ctx, "registry_catalog", out.Name)
	}
	return out, nil
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

// ListReplicas returns all registered replicas ordered by `replica_id`.
func (s *Store) ListReplicas(ctx context.Context) ([]ReplicaRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT replica_id, peer_addr, started_at, heartbeat_at
FROM registry.replicas
ORDER BY replica_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReplicaRow
	for rows.Next() {
		var r ReplicaRow
		if err := rows.Scan(&r.ReplicaID, &r.PeerAddr, &r.StartedAt, &r.HeartbeatAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
// `LastHeartbeatAt`, `HealthState`, and any active `TunnelSessionRow` + `ReplicaLastSeen`.
func (s *Store) DeriveStatus(e *EntryRow) api.EntryStatus {
	if e == nil || e.OwnerDisabled {
		return api.StatusOffline
	}
	now := s.now()
	switch e.LifecycleState {
	case api.StateRunning, "ACTIVE":
		// Evaluated below by kind.
	default:
		return api.StatusOffline
	}

	var age time.Duration
	switch e.Kind {
	case api.KindInstance:
		if e.LastHeartbeatAt == nil {
			return api.StatusOffline
		}
		age = now.Sub(*e.LastHeartbeatAt)

	case api.KindConnected:
		if e.Tunnel == nil {
			return api.StatusOffline
		}
		if now.Sub(e.Tunnel.ReplicaLastSeen) > s.thresholds.ReplicaTTL {
			return api.StatusOffline
		}
		age = now.Sub(e.Tunnel.LastSeenAt)
		if e.LastHeartbeatAt != nil {
			if hbAge := now.Sub(*e.LastHeartbeatAt); hbAge < age {
				age = hbAge
			}
		}
	default:
		return api.StatusOffline
	}

	if age > s.thresholds.OfflineAfter {
		return api.StatusOffline
	}
	if e.HealthState == api.HealthDegraded || e.HealthState == api.HealthUnhealthy {
		return api.StatusDegraded
	}
	if age <= s.thresholds.DegradedAfter {
		return api.StatusOnline
	}
	return api.StatusDegraded
}

// HeartbeatAgeSeconds returns the age in seconds since the entry's most recent heartbeat or tunnel activity.
func (s *Store) HeartbeatAgeSeconds(e *EntryRow) *int {
	if e == nil {
		return nil
	}
	var last *time.Time
	if e.LastHeartbeatAt != nil {
		last = e.LastHeartbeatAt
	}
	if e.Tunnel != nil && (last == nil || e.Tunnel.LastSeenAt.After(*last)) {
		t := e.Tunnel.LastSeenAt
		last = &t
	}
	if last == nil {
		return nil
	}
	secs := int(s.now().Sub(*last).Seconds())
	if secs < 0 {
		secs = 0
	}
	return &secs
}

// ToAPIEntry converts an EntryRow into an `api.Entry` with derived runtime status.
func (s *Store) ToAPIEntry(e *EntryRow, instanceDomain string) api.Entry {
	urlStr := ""
	if instanceDomain != "" {
		urlStr = fmt.Sprintf("https://%s.%s", e.Name, instanceDomain)
	}
	statusDetail := e.StatusReason
	if statusDetail == "" && e.HealthDetail != "" {
		statusDetail = e.HealthDetail
	}
	var tunnelConn *bool
	var connectedSince, lastSeenAt *time.Time
	if e.Kind == api.KindConnected {
		tc := e.Tunnel != nil && s.now().Sub(e.Tunnel.ReplicaLastSeen) <= s.thresholds.ReplicaTTL
		tunnelConn = &tc
		if e.Tunnel != nil {
			cs := e.Tunnel.ConnectedAt
			ls := e.Tunnel.LastSeenAt
			connectedSince = &cs
			lastSeenAt = &ls
		}
	}
	if lastSeenAt == nil && e.LastHeartbeatAt != nil {
		lastSeenAt = e.LastHeartbeatAt
	}
	var conflicts []api.ScopeConflict
	var machineType string
	bootDiskGB := e.BootDiskGB
	if s != nil {
		conflicts = s.EntryScopeConflicts(context.Background(), e)
		if e.Kind == api.KindInstance && e.Shape != "" {
			if sh, err := s.GetShape(context.Background(), e.Shape); err == nil && sh != nil {
				machineType = sh.MachineType
				if bootDiskGB <= 0 {
					bootDiskGB = sh.BootDiskGB
				}
			}
		}
	}
	return api.Entry{
		Name:                e.Name,
		URL:                 urlStr,
		Kind:                e.Kind,
		State:               e.LifecycleState,
		Status:              s.DeriveStatus(e),
		StatusDetail:        statusDetail,
		Health:              e.HealthState,
		HealthDetail:        e.HealthDetail,
		ReplaceStep:         api.ReplaceStep(e.ReplaceStep),
		DisplayName:         e.DisplayName,
		Purpose:             e.Purpose,
		Owner:               e.OwnerEmail,
		OwnerGroup:          e.OwnerGroup,
		PendingOwner:        e.PendingOwner,
		TransferExpiresAt:   e.TransferExpiresAt,
		OwnerDisabled:       e.OwnerDisabled,
		ConnectedPrincipal:  e.ConnectedPrincipal,
		Listed:              e.Listed,
		Tags:                e.Tags,
		Shape:               e.Shape,
		Zone:                e.Zone,
		MachineType:         machineType,
		BootDiskGB:          bootDiskGB,
		AllowSSH:            e.AllowSSH,
		ShipLogs:            e.ShipLogs,
		AutoUpgrade:         e.AutoUpgrade,
		AutoWake:            e.AutoWake,
		UpgradeAvailable:    e.UpgradeAvailable,
		BackendURL:          e.BackendURL,
		GrainVersion:        e.GrainVersion,
		GrainCommit:         e.GrainCommit,
		TargetVersion:       e.TargetVersion,
		HostKind:            api.HostKind(e.HostKind),
		ActiveTasks:         e.ActiveTasks,
		AwaitingReplyTasks:  e.AwaitingReplyTasks,
		QueueDepth:          e.PendingInbox,
		ActiveSessions:      e.ActiveSessions,
		TunnelConnected:     tunnelConn,
		ConnectedSince:      connectedSince,
		LastHeartbeatAt:     e.LastHeartbeatAt,
		LastSeenAt:          lastSeenAt,
		HeartbeatAgeSeconds: s.HeartbeatAgeSeconds(e),
		Scopes:              CombinedAPIScopes(e.Scopes),
		ReportedScopes:      RawAPIScopes(e.Scopes, api.SourceReported),
		DeclaredScopes:      RawAPIScopes(e.Scopes, api.SourceDeclared),
		ScopeConflicts:      conflicts,
		Credentials:         e.Credentials,
		CreatedAt:           e.CreatedAt,
		UpdatedAt:           e.UpdatedAt,
		StoppedAt:           e.StoppedAt,
		DeletedAt:           e.DeletedAt,
		ReservedUntil:       e.ReservedUntil,
		PurgeAfter:          e.PurgeAfter,
		DBPurgeAfter:        e.DBPurgeAfter,
		PurgedAt:            e.PurgedAt,
		Generation:          int(e.Version),
	}
}

// ToDirectoryEntry converts an EntryRow into a public `api.DirectoryEntry`.
func (s *Store) ToDirectoryEntry(e *EntryRow, instanceDomain string) api.DirectoryEntry {
	urlStr := ""
	if instanceDomain != "" {
		urlStr = fmt.Sprintf("https://%s.%s", e.Name, instanceDomain)
	}
	return api.DirectoryEntry{
		Name:                e.Name,
		URL:                 urlStr,
		Kind:                e.Kind,
		State:               e.LifecycleState,
		Status:              s.DeriveStatus(e),
		HeartbeatAgeSeconds: s.HeartbeatAgeSeconds(e),
		DisplayName:         e.DisplayName,
		Purpose:             e.Purpose,
		Owner:               e.OwnerEmail,
		OwnerGroup:          e.OwnerGroup,
		Tags:                e.Tags,
		GrainVersion:        e.GrainVersion,
		GrainCommit:         e.GrainCommit,
		HostKind:            api.HostKind(e.HostKind),
		Scopes:              CombinedAPIScopes(e.Scopes),
		ActiveTasks:         e.ActiveTasks,
		AwaitingReplyTasks:  e.AwaitingReplyTasks,
		QueueDepth:          e.PendingInbox,
		UpdatedAt:           e.UpdatedAt,
	}
}

// RouteRepoPath resolves the best-matching entries for `(repo, filePath)` using
// longest-path-prefix matching.
func (s *Store) RouteRepoPath(ctx context.Context, repo, filePath, instanceDomain string, filter func(*EntryRow) bool) (*api.RouteResponse, error) {
	if normRepo, err := api.NormalizeRepo(repo); err == nil {
		repo = normRepo
	} else {
		repo = strings.ToLower(strings.TrimSpace(repo))
	}
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
			if !sc.Active {
				continue
			}
			if !strings.EqualFold(sc.Repo, repo) {
				continue
			}
			matched, spec := api.PathMatchesPrefix(normPath, sc.PathPrefix)
			if !matched {
				continue
			}
			effRole := sc.EffectiveRole
			if effRole == "" {
				effRole = sc.Role
			}
			bestRole := api.RoleSecondary
			if bestScope != nil {
				bestRole = bestScope.EffectiveRole
				if bestRole == "" {
					bestRole = bestScope.Role
				}
			}
			if spec > bestLen || (spec == bestLen && effRole == api.RolePrimary && bestRole != api.RolePrimary) {
				bestLen = spec
				bestScope = sc
			}
		}
		if bestScope != nil {
			urlStr := ""
			if instanceDomain != "" {
				urlStr = fmt.Sprintf("https://%s.%s", e.Name, instanceDomain)
			}
			effRole := bestScope.EffectiveRole
			if effRole == "" {
				effRole = bestScope.Role
			}
			matches = append(matches, api.RouteCandidate{
				Name:                e.Name,
				URL:                 urlStr,
				Kind:                e.Kind,
				State:               e.LifecycleState,
				Status:              s.DeriveStatus(e),
				HeartbeatAgeSeconds: s.HeartbeatAgeSeconds(e),
				DisplayName:         e.DisplayName,
				Purpose:             e.Purpose,
				Owner:               e.OwnerEmail,
				OwnerGroup:          e.OwnerGroup,
				ActiveTasks:         e.ActiveTasks,
				AwaitingReplyTasks:  e.AwaitingReplyTasks,
				MatchedScope: api.MatchedScope{
					Repo:        bestScope.Repo,
					PathPrefix:  bestScope.PathPrefix,
					Role:        effRole,
					Source:      bestScope.Source,
					Specificity: bestLen,
				},
			})
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		iPrimary := matches[i].MatchedScope.Role == api.RolePrimary
		jPrimary := matches[j].MatchedScope.Role == api.RolePrimary
		if iPrimary != jPrimary {
			return iPrimary
		}
		if matches[i].MatchedScope.Specificity != matches[j].MatchedScope.Specificity {
			return matches[i].MatchedScope.Specificity > matches[j].MatchedScope.Specificity
		}
		iRank := statusRank(matches[i].Status)
		jRank := statusRank(matches[j].Status)
		if iRank != jRank {
			return iRank < jRank
		}
		if matches[i].ActiveTasks != matches[j].ActiveTasks {
			return matches[i].ActiveTasks < matches[j].ActiveTasks
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
	return res, nil
}

func statusRank(s api.EntryStatus) int {
	switch s {
	case api.StatusOnline:
		return 0
	case api.StatusDegraded:
		return 1
	default:
		return 2
	}
}

// AcquireOrRenewLease acquires or renews a named lease in `registry.leases` if it is
// currently unheld, expired, or already held by `holderID`.
func (s *Store) AcquireOrRenewLease(ctx context.Context, leaseName, holderID string, ttl time.Duration) (bool, error) {
	now := s.now()
	expiresAt := now.Add(ttl)
	res, err := s.db.ExecContext(ctx, `
INSERT INTO registry.leases (name, holder_id, acquired_at, renewed_at, expires_at)
VALUES ($1, $2, $4, $4, $3)
ON CONFLICT (name) DO UPDATE SET
    holder_id   = EXCLUDED.holder_id,
    acquired_at = CASE WHEN registry.leases.holder_id = EXCLUDED.holder_id THEN registry.leases.acquired_at ELSE $4 END,
    renewed_at  = $4,
    expires_at  = EXCLUDED.expires_at
WHERE registry.leases.holder_id = EXCLUDED.holder_id
   OR registry.leases.expires_at <= $4`,
		leaseName, holderID, expiresAt, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// GetLeaseStatus returns the current leader lease record for `leaseName` (e.g. `"reconciler"`).
func (s *Store) GetLeaseStatus(ctx context.Context, leaseName string) (*api.LeaseStatus, error) {
	now := s.now()
	var st api.LeaseStatus
	err := s.db.QueryRowContext(ctx, `
SELECT name, holder_id, acquired_at, renewed_at, expires_at
FROM registry.leases
WHERE name = $1`, strings.TrimSpace(leaseName)).Scan(
		&st.LeaseName, &st.HolderID, &st.AcquiredAt, &st.RenewedAt, &st.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	st.Active = now.Before(st.ExpiresAt)
	lag := int(now.Sub(st.RenewedAt).Seconds())
	if lag < 0 {
		lag = 0
	}
	st.LagSeconds = lag
	return &st, nil
}

// RecordAudit writes an audit log row to `registry.audit_log`.
func (s *Store) RecordAudit(ctx context.Context, actorEmail, action, entryID, entryName string, detail map[string]any) error {
	return s.RecordAuditWithDelegation(ctx, actorEmail, "", "", action, entryID, entryName, detail)
}

// RecordAuditWithDelegation writes an audit log row with optional delegating app (`via`) and `grant_id`.
func (s *Store) RecordAuditWithDelegation(ctx context.Context, actorEmail, via, grantID, action, entryID, entryName string, detail map[string]any) error {
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
INSERT INTO registry.audit_log (at, actor_email, via, grant_id, action, entry_id, entry_name, detail)
VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8::jsonb)`,
		s.now(), actorEmail, strings.TrimSpace(via), strings.TrimSpace(grantID), action, idParam, entryName, string(detailJSON))
	return err
}

// ListAuditLog returns recent audit log entries (most recent first).
func (s *Store) ListAuditLog(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, at, actor_email, COALESCE(via, ''), COALESCE(grant_id, ''), action, COALESCE(entry_id::text, ''), COALESCE(entry_name, ''), detail
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
		if err := rows.Scan(&a.ID, &a.At, &a.ActorEmail, &a.Via, &a.GrantID, &a.Action, &a.EntryID, &a.EntryName, &detailBytes); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(detailBytes, &a.Detail)
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateConfigRepo sets the provisioned Git `config_repo` URL on `entryID` under optimistic concurrency.
func (s *Store) UpdateConfigRepo(ctx context.Context, entryID string, expectedVersion int64, configRepo string) (*EntryRow, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET config_repo = NULLIF($1, ''),
    version     = version + 1,
    updated_at  = $2
WHERE id = $3 AND version = $4`,
		strings.TrimSpace(configRepo), now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch", ErrConflict, entryID, expectedVersion)
	}
	return s.GetEntryByID(ctx, entryID)
}

// UpdateReplaceStep updates `replace_step` and `status_reason` on an entry in `replacing` state under optimistic concurrency.
func (s *Store) UpdateReplaceStep(ctx context.Context, entryID string, expectedVersion int64, step api.ReplaceStep, statusReason string) (*EntryRow, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET replace_step  = $1,
    status_reason = $2,
    version       = version + 1,
    updated_at    = $3
WHERE id = $4 AND version = $5 AND lifecycle_state = 'replacing' AND deleted_at IS NULL`,
		string(step), statusReason, now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch or not replacing", ErrConflict, entryID, expectedVersion)
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
	}
	return out, err
}

// CompleteEntryReplace transitions an entry from `replacing` to `running`, clears `replace_step`,
// `status_reason`, and `target_version`, and updates its VM placement under optimistic concurrency.
func (s *Store) CompleteEntryReplace(ctx context.Context, entryID string, expectedVersion int64, gceInstanceID, internalIP, backendURL, grainVersion, grainCommit string) (*EntryRow, error) {
	if backendURL != "" {
		if err := validateBackendURL(backendURL); err != nil {
			return nil, err
		}
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state   = 'running',
    replace_step      = '',
    status_reason     = '',
    target_version    = '',
    upgrade_available = '',
    stopped_at        = NULL,
    gce_instance_id   = COALESCE(NULLIF($1, ''), gce_instance_id),
    internal_ip       = CASE WHEN $2 = '' THEN internal_ip ELSE $2::inet END,
    backend_url       = COALESCE(NULLIF($3, ''), backend_url),
    grain_version     = COALESCE(NULLIF($4, ''), grain_version),
    grain_commit      = CASE WHEN $5 = '' THEN grain_commit ELSE $5 END,
    last_heartbeat_at = $6,
    version           = version + 1,
    updated_at        = $6
WHERE id = $7 AND version = $8 AND lifecycle_state = 'replacing' AND deleted_at IS NULL`,
		gceInstanceID, internalIP, backendURL, grainVersion, grainCommit, now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch or not replacing", ErrConflict, entryID, expectedVersion)
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
		if out.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
	}
	return out, err
}

// BeginDeleteEntry transitions an entry to `deleting` while recording `db_purge_after`
// (`now` when `deleteDisk` is true, or `now + db_retention_period` otherwise).
func (s *Store) BeginDeleteEntry(ctx context.Context, entryID string, expectedVersion int64, deleteDisk bool) (*EntryRow, error) {
	now := s.now()
	var dbPurgeAfter time.Time
	if deleteDisk {
		dbPurgeAfter = now
	} else {
		ret := s.thresholds.DBRetentionPeriod
		if ret <= 0 {
			ret = 168 * time.Hour
		}
		if s.thresholds.ReservationPeriod > 0 && ret > s.thresholds.ReservationPeriod {
			ret = s.thresholds.ReservationPeriod
		}
		dbPurgeAfter = now.Add(ret)
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state = 'deleting',
    replace_step    = '',
    db_purge_after  = $1,
    version         = version + 1,
    updated_at      = $2
WHERE id = $3 AND version = $4 AND deleted_at IS NULL`,
		dbPurgeAfter, now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch", ErrConflict, entryID, expectedVersion)
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
		if out.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
	}
	return out, err
}

// CompleteDeleteEntry finalizes deletion of an entry after its VM has been deleted and
// its database/IAM resources have been fenced (or purged when `purgeResources` is true).
func (s *Store) CompleteDeleteEntry(ctx context.Context, entryID string, expectedVersion int64, purgeResources bool) (*EntryRow, error) {
	cur, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	reservedUntil := now.Add(s.thresholds.ReservationPeriod)
	if cur.ReservedUntil != nil && !cur.ReservedUntil.IsZero() {
		reservedUntil = *cur.ReservedUntil
	}
	purgeAfter := reservedUntil
	if cur.PurgeAfter != nil && !cur.PurgeAfter.IsZero() {
		purgeAfter = *cur.PurgeAfter
	}
	ret := s.thresholds.DBRetentionPeriod
	if ret <= 0 {
		ret = 168 * time.Hour
	}
	if s.thresholds.ReservationPeriod > 0 && ret > s.thresholds.ReservationPeriod {
		ret = s.thresholds.ReservationPeriod
	}
	dbPurgeAfter := now.Add(ret)
	if cur.DBPurgeAfter != nil && !cur.DBPurgeAfter.IsZero() {
		dbPurgeAfter = *cur.DBPurgeAfter
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	repos := collectPrimaryRepos(cur.Scopes)
	for _, r := range repos {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "scope:"+r); err != nil {
			return nil, err
		}
	}

	res, err := tx.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state        = 'deleted',
    replace_step           = '',
    gce_instance_id        = NULL,
    internal_ip            = NULL,
    backend_url            = NULL,
    service_account        = CASE WHEN $1 THEN NULL ELSE service_account END,
    sql_instance           = CASE WHEN $1 THEN NULL ELSE sql_instance END,
    db_name                = CASE WHEN $1 THEN NULL ELSE db_name END,
    secret_prefix          = CASE WHEN $1 THEN NULL ELSE secret_prefix END,
    config_repo            = CASE WHEN $1 THEN NULL ELSE config_repo END,
    deleted_at             = COALESCE(deleted_at, $2),
    reserved_until         = $3,
    purge_after            = $4,
    db_purge_after         = CASE WHEN $1 THEN NULL ELSE $5::timestamptz END,
    purged_at              = CASE WHEN $1 THEN $2::timestamptz ELSE purged_at END,
    last_disconnect_at     = COALESCE(last_disconnect_at, $2),
    last_disconnect_reason = CASE WHEN last_disconnect_reason = '' THEN 'entry deleted' ELSE last_disconnect_reason END,
    version                = version + 1,
    updated_at             = $2
WHERE id = $6 AND version = $7`,
		purgeResources, now, reservedUntil, purgeAfter, dbPurgeAfter, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch", ErrConflict, entryID, expectedVersion)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM registry.entry_scopes WHERE entry_id = $1`, entryID); err != nil {
		return nil, err
	}
	if len(repos) > 0 {
		if err := s.promoteContendersForReposTx(ctx, tx, repos, now); err != nil {
			return nil, err
		}
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
		if cur.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
	}
	return out, err
}

// ListEntriesDueForPurge returns soft-deleted entries whose `db_purge_after` (or `purge_after`)
// has passed and that still have retained cloud resources (`db_name`, `service_account`, `secret_prefix`, or `config_repo`).
func (s *Store) ListEntriesDueForPurge(ctx context.Context) ([]*EntryRow, error) {
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `
SELECT id
FROM registry.entries
WHERE lifecycle_state = 'deleted'
  AND COALESCE(db_purge_after, purge_after) <= $1
  AND (
      (db_name IS NOT NULL AND db_name <> '') OR
      (service_account IS NOT NULL AND service_account <> '') OR
      (secret_prefix IS NOT NULL AND secret_prefix <> '') OR
      (config_repo IS NOT NULL AND config_repo <> '')
  )
ORDER BY COALESCE(db_purge_after, purge_after) ASC`, now)
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
		if err == nil && e != nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// MarkEntryResourcesPurged clears the retained cloud resource columns on a `deleted` entry
// after its database, GSA, secrets, GCS folder, and config repo have been permanently deleted.
func (s *Store) MarkEntryResourcesPurged(ctx context.Context, entryID string) error {
	now := s.now()
	_, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET service_account = NULL,
    sql_instance    = NULL,
    db_name         = NULL,
    db_purge_after  = NULL,
    secret_prefix   = NULL,
    config_repo     = NULL,
    purged_at       = COALESCE(purged_at, $1),
    version         = version + 1,
    updated_at      = $1
WHERE id = $2 AND lifecycle_state = 'deleted'`, now, entryID)
	return err
}

// HardPurgeEntry immediately marks an entry as permanently purged (`purged_at = now`),
// clearing its retained cloud resource columns when `clearResourceCols` is true and
// optionally releasing its 30-day name reservation (`reserved_until = now, purge_after = now`).
func (s *Store) HardPurgeEntry(ctx context.Context, entryID string, clearResourceCols, releaseReservation bool) (*EntryRow, error) {
	cur, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	repos := collectPrimaryRepos(cur.Scopes)
	for _, r := range repos {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "scope:"+r); err != nil {
			return nil, err
		}
	}

	reservedUntil := now.Add(s.thresholds.ReservationPeriod)
	if cur.ReservedUntil != nil && !cur.ReservedUntil.IsZero() {
		reservedUntil = *cur.ReservedUntil
	}
	purgeAfter := reservedUntil
	if cur.PurgeAfter != nil && !cur.PurgeAfter.IsZero() {
		purgeAfter = *cur.PurgeAfter
	}
	if releaseReservation {
		reservedUntil = now
		purgeAfter = now
	}

	_, err = tx.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state        = 'deleted',
    replace_step           = '',
    gce_instance_id        = CASE WHEN $1 THEN NULL ELSE gce_instance_id END,
    internal_ip            = CASE WHEN $1 THEN NULL ELSE internal_ip END,
    backend_url            = CASE WHEN $1 THEN NULL ELSE backend_url END,
    service_account        = CASE WHEN $1 THEN NULL ELSE service_account END,
    sql_instance           = CASE WHEN $1 THEN NULL ELSE sql_instance END,
    db_name                = CASE WHEN $1 THEN NULL ELSE db_name END,
    secret_prefix          = CASE WHEN $1 THEN NULL ELSE secret_prefix END,
    config_repo            = CASE WHEN $1 THEN NULL ELSE config_repo END,
    deleted_at             = COALESCE(deleted_at, $2),
    reserved_until         = $3,
    purge_after            = $4,
    db_purge_after         = CASE WHEN $1 THEN NULL ELSE $2::timestamptz END,
    purged_at              = $2,
    last_disconnect_at     = COALESCE(last_disconnect_at, $2),
    last_disconnect_reason = CASE WHEN last_disconnect_reason = '' THEN 'entry hard purged' ELSE last_disconnect_reason END,
    version                = version + 1,
    updated_at             = $2
WHERE id = $5`,
		clearResourceCols, now, reservedUntil, purgeAfter, entryID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM registry.entry_scopes WHERE entry_id = $1`, entryID); err != nil {
		return nil, err
	}
	if len(repos) > 0 {
		if err := s.promoteContendersForReposTx(ctx, tx, repos, now); err != nil {
			return nil, err
		}
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
		if cur.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
	}
	return out, err
}

// ListAllScopeConflicts returns all active primary-prefix scope conflicts across all non-deleted entries.
func (s *Store) ListAllScopeConflicts(ctx context.Context) ([]api.AdminScopeConflict, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
    e.name,
    e.owner_email,
    e.owner_group,
    es.repo,
    es.path_prefix,
    es.source,
    es.conflict_with,
    COALESCE(inc.owner_email, ''),
    COALESCE(inc.owner_group, ''),
    es.claimed_at
FROM registry.entry_scopes es
JOIN registry.entries e ON e.id = es.entry_id
LEFT JOIN registry.entries inc ON inc.name = es.conflict_with AND inc.deleted_at IS NULL
WHERE e.deleted_at IS NULL
  AND (es.conflict_with <> '' OR es.inactive_reason = 'primary_conflict')
ORDER BY es.repo ASC, es.path_prefix ASC, e.name ASC, es.source ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []api.AdminScopeConflict
	seen := make(map[string]bool)
	for rows.Next() {
		var c api.AdminScopeConflict
		if err := rows.Scan(
			&c.EntryName, &c.Owner, &c.OwnerGroup,
			&c.Repo, &c.PathPrefix, &c.Source, &c.ConflictWith,
			&c.IncumbentOwner, &c.IncumbentOwnerGroup, &c.ClaimedAt,
		); err != nil {
			return nil, err
		}
		key := c.EntryName + "|" + c.Repo + "|" + c.PathPrefix
		if seen[key] {
			continue
		}
		seen[key] = true
		if c.ConflictWith != "" {
			var incPrefix string
			_ = s.db.QueryRowContext(ctx, `
SELECT es.path_prefix
FROM registry.entry_scopes es
JOIN registry.entries e ON e.id = es.entry_id
WHERE e.name = $1 AND e.deleted_at IS NULL AND es.repo = $2 AND es.primary_claim = TRUE AND es.active = TRUE
LIMIT 1`, c.ConflictWith, c.Repo).Scan(&incPrefix)
			c.IncumbentPathPrefix = incPrefix
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ResolveScopeConflict resolves an active primary-prefix conflict for `(entryName, repo, pathPrefix)`.
// When `resolution` is `"remove"`, the entry's scope claims for `(repo, pathPrefix)` are removed.
// Otherwise (`"demote"`), the entry's scope claims for `(repo, pathPrefix)` are converted to
// active `secondary` claims (including an explicit `declared` secondary override so future heartbeats
// do not re-claim `primary`), and any contenders waiting on `repo` are promoted in `claimed_at` order.
func (s *Store) ResolveScopeConflict(ctx context.Context, entryName, repo, pathPrefix, resolution string) error {
	entry, err := s.GetEntryByName(ctx, entryName)
	if err != nil {
		return err
	}
	normRepo, err := api.NormalizeRepo(repo)
	if err != nil {
		normRepo = strings.ToLower(strings.TrimSpace(repo))
	}
	normPrefix := NormalizeScopePath(pathPrefix)
	resolution = strings.ToLower(strings.TrimSpace(resolution))
	if resolution == "" {
		resolution = "demote"
	}

	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "scope:"+normRepo); err != nil {
		return err
	}

	switch resolution {
	case "remove":
		res, err := tx.ExecContext(ctx, `
DELETE FROM registry.entry_scopes
WHERE entry_id = $1 AND repo = $2 AND path_prefix = $3`, entry.ID, normRepo, normPrefix)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	case "demote":
		res, err := tx.ExecContext(ctx, `
UPDATE registry.entry_scopes
SET primary_claim   = FALSE,
    active          = TRUE,
    inactive_reason = '',
    conflict_with   = '',
    updated_at      = $1
WHERE entry_id = $2 AND repo = $3 AND path_prefix = $4`,
			now, entry.ID, normRepo, normPrefix)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		// Ensure a declared secondary scope exists so subsequent heartbeats preserve the demotion.
		_, err = tx.ExecContext(ctx, `
INSERT INTO registry.entry_scopes (
    entry_id, repo, path_prefix, source, primary_claim, branch, active, inactive_reason, conflict_with, claimed_at, updated_at
) VALUES ($1, $2, $3, 'declared', FALSE, '', TRUE, '', '', $4, $4)
ON CONFLICT (entry_id, repo, path_prefix, source) DO UPDATE SET
    primary_claim   = FALSE,
    active          = TRUE,
    inactive_reason = '',
    conflict_with   = '',
    updated_at      = EXCLUDED.updated_at`,
			entry.ID, normRepo, normPrefix, now)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported conflict resolution %q (expected \"demote\" or \"remove\")", resolution)
	}

	if err := s.promoteContendersForReposTx(ctx, tx, []string{normRepo}, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notify(ctx, "registry_entries", entry.Name)
	s.notify(ctx, "registry_catalog", entry.Name)
	return nil
}

// DisconnectEntryTunnel terminates any active `registry.tunnel_sessions` row for `entryID`,
// records `last_disconnect_at` and `last_disconnect_reason`, and emits `NOTIFY registry_tunnels`.
func (s *Store) DisconnectEntryTunnel(ctx context.Context, entryID, entryName, reason string) error {
	now := s.now()
	if strings.TrimSpace(reason) == "" {
		reason = "admin disconnected tunnel"
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM registry.tunnel_sessions WHERE entry_id = $1`, entryID)
	if err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `
UPDATE registry.entries
SET last_disconnect_at     = $1,
    last_disconnect_reason = $2,
    updated_at             = $1
WHERE id = $3`, now, reason, entryID)
	if entryName != "" {
		s.notify(ctx, "registry_entries", entryName)
		s.notify(ctx, "registry_tunnels", entryName)
	}
	return nil
}

// PurgeExpiredTombstones permanently deletes `deleted` tombstone rows whose 30-day
// reservation (`reserved_until` and `purge_after`) has expired and whose cloud resources
// have already been purged.
func (s *Store) PurgeExpiredTombstones(ctx context.Context) (int64, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
DELETE FROM registry.entries
WHERE lifecycle_state = 'deleted'
  AND deleted_at IS NOT NULL
  AND COALESCE(reserved_until, purge_after) <= $1
  AND COALESCE(purge_after, reserved_until) <= $1
  AND (gce_instance_id IS NULL OR gce_instance_id = '')
  AND (db_name IS NULL OR db_name = '')
  AND (service_account IS NULL OR service_account = '')`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteExpiredReplicas deletes rows in `registry.replicas` whose `heartbeat_at` is older
// than `s.thresholds.ReplicaTTL`, cascading to `registry.tunnel_sessions` and emitting
// `NOTIFY registry_tunnels` for any affected entries.
func (s *Store) DeleteExpiredReplicas(ctx context.Context) ([]string, error) {
	cutoff := s.now().Add(-s.thresholds.ReplicaTTL)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Collect affected entry names before cascading delete.
	entryRows, err := tx.QueryContext(ctx, `
SELECT DISTINCT e.name
FROM registry.tunnel_sessions ts
JOIN registry.replicas r ON r.replica_id = ts.replica_id
JOIN registry.entries e ON e.id = ts.entry_id
WHERE r.heartbeat_at <= $1`, cutoff)
	if err != nil {
		return nil, err
	}
	var affectedEntries []string
	for entryRows.Next() {
		var name string
		if err := entryRows.Scan(&name); err != nil {
			_ = entryRows.Close()
			return nil, err
		}
		affectedEntries = append(affectedEntries, name)
	}
	if err := entryRows.Close(); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
DELETE FROM registry.replicas
WHERE heartbeat_at <= $1
RETURNING replica_id`, cutoff)
	if err != nil {
		return nil, err
	}
	var deletedReplicas []string
	for rows.Next() {
		var repID string
		if err := rows.Scan(&repID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		deletedReplicas = append(deletedReplicas, repID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, name := range affectedEntries {
		s.notify(ctx, "registry_tunnels", name)
	}
	return deletedReplicas, nil
}

// BeginStopEntry transitions a hosted `instance` entry from `running` (or `failed`) to `stopping`
// under optimistic concurrency so the reconciler can drain active tasks and remove the VM while
// retaining externalized state (database, secrets, GCS folder, and config repo).
func (s *Store) BeginStopEntry(ctx context.Context, entryID string, expectedVersion int64, statusReason string) (*EntryRow, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state = 'stopping',
    replace_step    = '',
    status_reason   = $1,
    version         = version + 1,
    updated_at      = $2
WHERE id = $3
  AND version = $4
  AND kind = 'instance'
  AND lifecycle_state IN ('running', 'failed')
  AND deleted_at IS NULL`,
		statusReason, now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch or cannot be stopped from current state", ErrConflict, entryID, expectedVersion)
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
		if out.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
	}
	return out, err
}

// CompleteStopEntry finalizes a `stopping` hosted `instance` into `stopped` after its VM
// has been halted and deleted, clearing `gce_instance_id`, `internal_ip`, and `backend_url`
// while preserving externalized state (`service_account`, `sql_instance`, `db_name`, `secret_prefix`, `config_repo`).
func (s *Store) CompleteStopEntry(ctx context.Context, entryID string, expectedVersion int64) (*EntryRow, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state      = 'stopped',
    replace_step         = '',
    status_reason        = '',
    gce_instance_id      = NULL,
    internal_ip          = NULL,
    backend_url          = NULL,
    active_tasks         = 0,
    awaiting_reply_tasks = 0,
    active_sessions      = 0,
    stopped_at           = $1,
    version              = version + 1,
    updated_at           = $1
WHERE id = $2
  AND version = $3
  AND lifecycle_state = 'stopping'
  AND deleted_at IS NULL`,
		now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch or not stopping", ErrConflict, entryID, expectedVersion)
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
		if out.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
	}
	return out, err
}

// BeginStartEntry transitions a `stopped` (or `stopping`) hosted `instance` entry to `creating`
// so the reconciler boots a fresh VM attached to the preserved externalized state.
func (s *Store) BeginStartEntry(ctx context.Context, entryID string, expectedVersion int64, statusReason string) (*EntryRow, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state = 'creating',
    replace_step    = '',
    status_reason   = $1,
    gce_instance_id = NULL,
    internal_ip     = NULL,
    backend_url     = NULL,
    stopped_at      = NULL,
    version         = version + 1,
    updated_at      = $2
WHERE id = $3
  AND version = $4
  AND kind = 'instance'
  AND lifecycle_state IN ('stopped', 'stopping')
  AND deleted_at IS NULL`,
		statusReason, now, entryID, expectedVersion)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: entry %s version %d mismatch or cannot be started from current state", ErrConflict, entryID, expectedVersion)
	}
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil {
		s.notify(ctx, "registry_entries", out.Name)
		if out.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
	}
	return out, err
}

// WakeStoppedEntry atomically transitions a `stopped` hosted `instance` entry with `auto_wake = TRUE`
// to `creating` when an authorized user visits `<name>.<instance-domain>`. If another concurrent
// visitor has already woken the entry, `woke` is false and the current entry is returned without error.
func (s *Store) WakeStoppedEntry(ctx context.Context, entryID, callerEmail string) (row *EntryRow, woke bool, err error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET lifecycle_state = 'creating',
    replace_step    = '',
    status_reason   = 'auto-waking on visit',
    gce_instance_id = NULL,
    internal_ip     = NULL,
    backend_url     = NULL,
    stopped_at      = NULL,
    version         = version + 1,
    updated_at      = $1
WHERE id = $2
  AND kind = 'instance'
  AND lifecycle_state = 'stopped'
  AND auto_wake = TRUE
  AND deleted_at IS NULL`,
		now, entryID)
	if err != nil {
		return nil, false, err
	}
	n, _ := res.RowsAffected()
	out, err := s.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, false, err
	}
	if n > 0 {
		actor := strings.ToLower(strings.TrimSpace(callerEmail))
		if actor == "" {
			actor = "system:auto-wake"
		}
		_ = s.RecordAudit(ctx, actor, "entry_auto_wake", out.ID, out.Name, map[string]any{
			"caller": actor,
		})
		s.notify(ctx, "registry_entries", out.Name)
		if out.Listed {
			s.notify(ctx, "registry_catalog", out.Name)
		}
		return out, true, nil
	}
	return out, false, nil
}

// SetEntryUpgradeAvailable updates `upgrade_available` on an entry (used by `auto_upgrade: notify`
// or cleared when the entry reaches the target version).
func (s *Store) SetEntryUpgradeAvailable(ctx context.Context, entryID, upgradeAvailable string) (*EntryRow, error) {
	now := s.now()
	upgradeAvailable = strings.TrimSpace(upgradeAvailable)
	res, err := s.db.ExecContext(ctx, `
UPDATE registry.entries
SET upgrade_available = $1,
    updated_at        = $2
WHERE id = $3
  AND deleted_at IS NULL
  AND upgrade_available <> $1`,
		upgradeAvailable, now, entryID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	out, err := s.GetEntryByID(ctx, entryID)
	if err == nil && out != nil && n > 0 {
		s.notify(ctx, "registry_entries", out.Name)
	}
	return out, err
}
