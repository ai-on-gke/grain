package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// persistentSandboxColumns is the persistent_sandbox row. Env and
// capabilities are JSON text rather than tables of their own: neither is
// secret (a secret reaches the sandbox through a capability, never
// through this row) and neither is ever queried by.
const persistentSandboxColumns = "`name`,`repo`,`ref`,`auto_update`,`command`,`dir`,`env`,`capabilities`," +
	"`cpus`,`memory_mb`,`disk_gb`,`network_access`,`sandbox_port`,`host_port`,`ui_subpath`,`strip_prefix`,`enabled`," +
	"`refresh`,`rollback_on_crash`"

func scanPersistentSandbox(scan func(...any) error) (PersistentSandbox, error) {
	var sb PersistentSandbox
	var env, caps, network string
	if err := scan(&sb.Name, &sb.Repo, &sb.Ref, &sb.AutoUpdate, &sb.Command, &sb.Dir, &env, &caps,
		&sb.CPUs, &sb.MemoryMB, &sb.DiskGB, &network, &sb.SandboxPort, &sb.HostPort, &sb.UISubpath, &sb.StripPrefix, &sb.Enabled,
		&sb.Refresh, &sb.RollbackOnCrash); err != nil {
		return PersistentSandbox{}, err
	}
	sb.NetworkAccess = NetworkAccess(network)
	if env != "" {
		if err := json.Unmarshal([]byte(env), &sb.Env); err != nil {
			return PersistentSandbox{}, fmt.Errorf("persistent sandbox %s: env: %w", sb.Name, err)
		}
	}
	if caps != "" {
		if err := json.Unmarshal([]byte(caps), &sb.Capabilities); err != nil {
			return PersistentSandbox{}, fmt.Errorf("persistent sandbox %s: capabilities: %w", sb.Name, err)
		}
	}
	return sb, nil
}

// GetPersistentSandbox returns the persistent sandbox called name, or nil.
func (s *Store) GetPersistentSandbox(ctx context.Context, name string) (*PersistentSandbox, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+persistentSandboxColumns+" FROM `persistent_sandbox` WHERE `name` = ?", name)
	sb, err := scanPersistentSandbox(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading persistent sandbox %s: %w", name, err)
	}
	return &sb, nil
}

// ListPersistentSandboxes returns every persistent sandbox, by name.
func (s *Store) ListPersistentSandboxes(ctx context.Context) ([]PersistentSandbox, error) {
	var list []PersistentSandbox
	err := each(ctx, s.db,
		"SELECT "+persistentSandboxColumns+" FROM `persistent_sandbox` ORDER BY `name`",
		nil,
		func(rows *sql.Rows) error {
			sb, err := scanPersistentSandbox(rows.Scan)
			if err != nil {
				return err
			}
			list = append(list, sb)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing persistent sandboxes: %w", err)
	}
	return list, nil
}

// PutPersistentSandbox writes or replaces one definition.
func (s *Store) PutPersistentSandbox(ctx context.Context, sb PersistentSandbox) error {
	if err := ValidatePersistentSandbox(sb); err != nil {
		return err
	}
	env, caps := "", ""
	if len(sb.Env) > 0 {
		b, err := json.Marshal(sb.Env)
		if err != nil {
			return err
		}
		env = string(b)
	}
	if len(sb.Capabilities) > 0 {
		b, err := json.Marshal(sb.Capabilities)
		if err != nil {
			return err
		}
		caps = string(b)
	}
	return s.write(ctx, "put persistent sandbox "+sb.Name, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `persistent_sandbox` ("+persistentSandboxColumns+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			sb.Name, sb.Repo, sb.Ref, sb.AutoUpdate, sb.Command, sb.Dir, env, caps,
			sb.CPUs, sb.MemoryMB, sb.DiskGB, string(sb.NetworkAccess), sb.SandboxPort, sb.HostPort, sb.UISubpath, sb.StripPrefix, sb.Enabled,
			sb.Refresh, sb.RollbackOnCrash)
		if err != nil {
			return fmt.Errorf("saving persistent sandbox %s: %w", sb.Name, err)
		}
		return nil
	})
}

// DeletePersistentSandbox removes one definition.
func (s *Store) DeletePersistentSandbox(ctx context.Context, name string) error {
	return s.write(ctx, "delete persistent sandbox "+name, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM `persistent_sandbox` WHERE `name` = ?", name)
		return err
	})
}
