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
	"database/sql"
	"fmt"
)

type migration struct {
	version int
	name    string
	upSQL   string
}

var migrations = []migration{
	{
		version: 1,
		name:    "initial_registry_schema",
		upSQL: `
CREATE SCHEMA IF NOT EXISTS registry;

CREATE TABLE IF NOT EXISTS registry.owners (
    email        TEXT PRIMARY KEY,
    disabled     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS registry.shapes (
    name           TEXT PRIMARY KEY,
    machine_type   TEXT NOT NULL,
    boot_disk_gb   INT NOT NULL,
    boot_disk_iops INT,
    quota_units    INT NOT NULL DEFAULT 1,
    description    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS registry.sql_instances (
    name            TEXT PRIMARY KEY,
    connection_name TEXT NOT NULL,
    private_ip      INET NOT NULL,
    max_databases   INT NOT NULL DEFAULT 50,
    draining        BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS registry.quotas (
    owner_email        TEXT PRIMARY KEY REFERENCES registry.owners(email),
    max_instance_units INT NOT NULL DEFAULT 2,
    max_connected      INT NOT NULL DEFAULT 20,
    updated_by         TEXT,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS registry.entries (
    id                   UUID PRIMARY KEY,
    name                 TEXT NOT NULL,
    kind                 TEXT NOT NULL CHECK (kind IN ('instance', 'connected')),
    owner_email          TEXT NOT NULL REFERENCES registry.owners(email),
    owner_group          TEXT NOT NULL DEFAULT '',
    listed               BOOLEAN NOT NULL DEFAULT TRUE,
    visibility           TEXT NOT NULL DEFAULT 'owner' CHECK (visibility IN ('owner', 'shared', 'public')),
    access_list          JSONB NOT NULL DEFAULT '[]'::jsonb,
    display_name         TEXT NOT NULL DEFAULT '',
    purpose              TEXT NOT NULL DEFAULT '',
    tags                 JSONB NOT NULL DEFAULT '[]'::jsonb,
    lifecycle_state      TEXT NOT NULL CHECK (lifecycle_state IN ('creating', 'running', 'replacing', 'deleting', 'deleted', 'failed', 'PROVISIONING', 'ACTIVE', 'DEPROVISIONING', 'DELETED', 'FAILED')),
    status_reason        TEXT NOT NULL DEFAULT '',
    version              BIGINT NOT NULL DEFAULT 1,

    shape                TEXT REFERENCES registry.shapes(name),
    zone                 TEXT,
    gce_instance_id      TEXT,
    internal_ip          INET,
    backend_url          TEXT,
    service_account      TEXT,
    sql_instance         TEXT REFERENCES registry.sql_instances(name),
    db_name              TEXT,
    db_purge_after       TIMESTAMPTZ,
    secret_prefix        TEXT,
    config_repo          TEXT,
    ssh_grant_expires_at TIMESTAMPTZ,
    ssh_granted_to       TEXT,

    grain_version        TEXT,
    active_tasks         INT NOT NULL DEFAULT 0,
    pending_inbox        INT NOT NULL DEFAULT 0,
    last_heartbeat_at    TIMESTAMPTZ,

    session_epoch        BIGINT NOT NULL DEFAULT 0,

    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at           TIMESTAMPTZ,
    purge_after          TIMESTAMPTZ
) WITH (FILLFACTOR = 80);

CREATE UNIQUE INDEX IF NOT EXISTS entries_active_name_idx
    ON registry.entries (name) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS entries_reserved_name_idx
    ON registry.entries (name, purge_after) WHERE deleted_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS entries_owner_idx
    ON registry.entries (owner_email) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS registry.entry_scopes (
    entry_id      UUID NOT NULL REFERENCES registry.entries(id) ON DELETE CASCADE,
    repo          TEXT NOT NULL,
    path_prefix   TEXT NOT NULL DEFAULT '',
    source        TEXT NOT NULL DEFAULT 'reported' CHECK (source IN ('reported', 'declared')),
    primary_claim BOOLEAN NOT NULL DEFAULT FALSE,
    branch        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (entry_id, repo, path_prefix, source)
);

CREATE INDEX IF NOT EXISTS entry_scopes_repo_idx
    ON registry.entry_scopes (repo, path_prefix);

CREATE INDEX IF NOT EXISTS entry_scopes_primary_idx
    ON registry.entry_scopes (repo, path_prefix) WHERE primary_claim = TRUE;

CREATE TABLE IF NOT EXISTS registry.entry_credentials (
    id           UUID PRIMARY KEY,
    entry_id     UUID NOT NULL REFERENCES registry.entries(id) ON DELETE CASCADE,
    token_sha256 BYTEA NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS registry.audit_log (
    id          BIGSERIAL PRIMARY KEY,
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_email TEXT NOT NULL,
    action      TEXT NOT NULL,
    entry_id    UUID,
    entry_name  TEXT,
    detail      JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS audit_log_entry_idx ON registry.audit_log (entry_id, at DESC);
CREATE INDEX IF NOT EXISTS audit_log_actor_idx ON registry.audit_log (actor_email, at DESC);

CREATE TABLE IF NOT EXISTS registry.leases (
    name       TEXT PRIMARY KEY,
    holder_id  TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS registry.replicas (
    replica_id   TEXT PRIMARY KEY,
    peer_addr    TEXT NOT NULL,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS registry.tunnel_sessions (
    entry_id      UUID PRIMARY KEY REFERENCES registry.entries(id) ON DELETE CASCADE,
    replica_id    TEXT NOT NULL REFERENCES registry.replicas(replica_id) ON DELETE CASCADE,
    session_epoch BIGINT NOT NULL,
    remote_addr   TEXT NOT NULL,
    connected_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    rtt_ms        INT
);

CREATE INDEX IF NOT EXISTS tunnel_sessions_replica_idx
    ON registry.tunnel_sessions (replica_id);
`,
	},
	{
		version: 2,
		name:    "tunnel_and_routing_extensions",
		upSQL: `
ALTER TABLE registry.entries
    ADD COLUMN IF NOT EXISTS connected_principal TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS host_kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_disconnect_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_disconnect_reason TEXT NOT NULL DEFAULT '';

ALTER TABLE registry.tunnel_sessions
    ADD COLUMN IF NOT EXISTS cred_id UUID;
`,
	},
	{
		version: 3,
		name:    "catalog_metadata_scopes_and_heartbeats",
		upSQL: `
ALTER TABLE registry.entries
    ADD COLUMN IF NOT EXISTS pending_owner TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS replace_step TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS target_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS allow_ssh BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS ship_logs BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS grain_commit TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS health_state TEXT NOT NULL DEFAULT 'ok',
    ADD COLUMN IF NOT EXISTS health_detail TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS awaiting_reply_tasks INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS active_sessions INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS has_reported_scopes BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS reserved_until TIMESTAMPTZ;

ALTER TABLE registry.entry_scopes
    ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS inactive_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS conflict_with TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS entry_scopes_conflict_idx
    ON registry.entry_scopes (repo, conflict_with) WHERE conflict_with <> '';

ALTER TABLE registry.entry_credentials
    ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
`,
	},
	{
		version: 4,
		name:    "delegating_apps_and_grants",
		upSQL: `
CREATE TABLE IF NOT EXISTS registry.delegating_apps (
    name                  TEXT PRIMARY KEY,
    principal             TEXT NOT NULL UNIQUE,
    display_name          TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    homepage              TEXT NOT NULL DEFAULT '',
    owner                 TEXT NOT NULL REFERENCES registry.owners(email),
    owner_group           TEXT NOT NULL DEFAULT '',
    max_access            TEXT NOT NULL DEFAULT 'write' CHECK (max_access IN ('read', 'write')),
    max_grant_ttl_seconds BIGINT NOT NULL DEFAULT 7776000,
    require_subject_token BOOLEAN NOT NULL DEFAULT FALSE,
    redirect_uris         JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_by            TEXT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    disabled_at           TIMESTAMPTZ,
    disabled_by           TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS registry.delegation_grants (
    id           TEXT PRIMARY KEY,
    app          TEXT NOT NULL REFERENCES registry.delegating_apps(name) ON DELETE CASCADE,
    person       TEXT NOT NULL,
    entries      JSONB NOT NULL DEFAULT '[]'::jsonb,
    access       TEXT NOT NULL CHECK (access IN ('read', 'write')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    revoked_by   TEXT NOT NULL DEFAULT '',
    last_used_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS delegation_grants_live_idx
    ON registry.delegation_grants (app, person) WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS delegation_grants_person_idx
    ON registry.delegation_grants (person) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS registry.delegation_usage (
    grant_id TEXT NOT NULL REFERENCES registry.delegation_grants(id) ON DELETE CASCADE,
    name     TEXT NOT NULL,
    day      DATE NOT NULL,
    reads    BIGINT NOT NULL DEFAULT 0,
    writes   BIGINT NOT NULL DEFAULT 0,
    last_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (grant_id, name, day)
);

ALTER TABLE registry.audit_log
    ADD COLUMN IF NOT EXISTS via TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS grant_id TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 5,
		name:    "fleet_upgrades_and_stop_start",
		upSQL: `
ALTER TABLE registry.entries
    DROP CONSTRAINT IF EXISTS entries_lifecycle_state_check;

ALTER TABLE registry.entries
    ADD CONSTRAINT entries_lifecycle_state_check
    CHECK (lifecycle_state IN (
        'creating', 'running', 'replacing', 'stopping', 'stopped', 'deleting', 'deleted', 'failed',
        'PROVISIONING', 'ACTIVE', 'STOPPING', 'STOPPED', 'DEPROVISIONING', 'DELETED', 'FAILED'
    ));

ALTER TABLE registry.entries
    ADD COLUMN IF NOT EXISTS auto_upgrade TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS auto_wake BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS upgrade_available TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS stopped_at TIMESTAMPTZ;
`,
	},
	{
		version: 6,
		name:    "transfer_expiry_and_admin_recovery",
		upSQL: `
ALTER TABLE registry.entries
    ADD COLUMN IF NOT EXISTS transfer_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS purged_at           TIMESTAMPTZ;

ALTER TABLE registry.leases
    ADD COLUMN IF NOT EXISTS acquired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS renewed_at  TIMESTAMPTZ NOT NULL DEFAULT now();
`,
	},
	{
		version: 7,
		name:    "custom_boot_disk_gb",
		upSQL: `
ALTER TABLE registry.entries
    ADD COLUMN IF NOT EXISTS boot_disk_gb INT;
`,
	},
	{
		// Hosted VMs run with nested virtualization, which GCE's E2 machine
		// types do not support, so a shape row seeded by an older config with an
		// e2-* machine type can never provision. Move each onto its N2
		// equivalent (e2-standard-8 -> n2-standard-8; the shared-core
		// e2-micro/small/medium -> n2-standard-2), keeping any zonal
		// "zones/<z>/machineTypes/" prefix. Shapes still in configuration are
		// re-seeded from it at startup, and configuration now refuses e2-*.
		version: 8,
		name:    "replace_e2_shape_machine_types",
		upSQL: `
UPDATE registry.shapes
SET machine_type = regexp_replace(
        regexp_replace(machine_type, '(^|/)e2-(micro|small|medium)$', '\1n2-standard-2'),
        '(^|/)e2-', '\1n2-')
WHERE machine_type ~ '(^|/)e2-';
`,
	},
}

// migrateLockKey is the pg_advisory_lock key every grain-registry replica takes
// before touching the registry schema ("grnreg" in ASCII), so replicas that
// start together against one database apply each migration exactly once.
const migrateLockKey int64 = 0x67726e726567

// Migrate applies all pending registry schema migrations inside the `registry` PostgreSQL schema.
//
// Replicas of one registry share a database and normally start at the same
// time (a Deployment rollout), so Migrate holds a session-level advisory lock
// for its whole run: without it two replicas both see a migration as pending
// and race on CREATE ... IF NOT EXISTS, and the loser exits with
// `duplicate key value violates unique constraint "pg_type_typname_nsp_index"`.
func Migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquiring migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrateLockKey); err != nil {
		return fmt.Errorf("taking registry migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrateLockKey)
	}()
	return migrateLocked(ctx, conn)
}

func migrateLocked(ctx context.Context, db *sql.Conn) error {
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS registry`); err != nil {
		return fmt.Errorf("creating registry schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS registry.schema_migrations (
    version    INT PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("creating registry.schema_migrations: %w", err)
	}

	for _, m := range migrations {
		var exists bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM registry.schema_migrations WHERE version = $1)`, m.version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("checking migration %d: %w", m.version, err)
		}
		if exists {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("beginning migration %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, m.upSQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("applying migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO registry.schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("recording migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", m.version, err)
		}
	}
	return nil
}
