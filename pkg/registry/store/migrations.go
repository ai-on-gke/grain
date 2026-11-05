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
}

// Migrate applies all pending registry schema migrations inside the `registry` PostgreSQL schema.
func Migrate(ctx context.Context, db *sql.DB) error {
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
