package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

const standingSandboxColumns = "`name`,`image`,`command`,`dir`,`cpus`,`memory_mb`,`disk_gb`,`network_access`,`enabled`"

func scanStandingSandbox(scan func(...any) error) (StandingSandbox, error) {
	var sb StandingSandbox
	var netAccess string
	if err := scan(&sb.Name, &sb.Image, &sb.Command, &sb.Dir, &sb.CPUs, &sb.MemoryMB, &sb.DiskGB, &netAccess, &sb.Enabled); err != nil {
		return StandingSandbox{}, err
	}
	sb.NetworkAccess = NetworkAccess(netAccess)
	return sb, nil
}

func (s *Store) hydrateStandingSandbox(ctx context.Context, q querier, sb *StandingSandbox) error {
	// Load secrets
	var secrets []StandingSandboxSecret
	err := each(ctx, q,
		"SELECT `sandbox_name`,`secret_name`,`env_var`,`file_path` FROM `standing_sandbox_secret` WHERE `sandbox_name` = ? ORDER BY `secret_name`,`env_var`,`file_path`",
		[]any{sb.Name},
		func(rows *sql.Rows) error {
			var sec StandingSandboxSecret
			if err := rows.Scan(&sec.SandboxName, &sec.SecretName, &sec.EnvVar, &sec.FilePath); err != nil {
				return err
			}
			secrets = append(secrets, sec)
			return nil
		})
	if err != nil {
		return fmt.Errorf("reading secrets for standing sandbox %s: %w", sb.Name, err)
	}
	sb.Secrets = secrets

	// Load env
	env := make(map[string]string)
	err = each(ctx, q,
		"SELECT `name`,`value` FROM `standing_sandbox_env` WHERE `sandbox_name` = ? ORDER BY `name`",
		[]any{sb.Name},
		func(rows *sql.Rows) error {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return err
			}
			env[k] = v
			return nil
		})
	if err != nil {
		return fmt.Errorf("reading env for standing sandbox %s: %w", sb.Name, err)
	}
	sb.Env = env
	return nil
}

// GetStandingSandbox retrieves a standing sandbox by name, or nil if not found.
func (s *Store) GetStandingSandbox(ctx context.Context, name string) (*StandingSandbox, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+standingSandboxColumns+" FROM `standing_sandbox` WHERE `name` = ?", name)
	sb, err := scanStandingSandbox(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading standing sandbox %s: %w", name, err)
	}
	if err := s.hydrateStandingSandbox(ctx, s.db, &sb); err != nil {
		return nil, err
	}
	return &sb, nil
}

// ListStandingSandboxes returns all defined standing sandboxes, sorted by name.
func (s *Store) ListStandingSandboxes(ctx context.Context) ([]StandingSandbox, error) {
	var list []StandingSandbox
	err := each(ctx, s.db,
		"SELECT "+standingSandboxColumns+" FROM `standing_sandbox` ORDER BY `name`",
		nil,
		func(rows *sql.Rows) error {
			sb, err := scanStandingSandbox(rows.Scan)
			if err != nil {
				return err
			}
			list = append(list, sb)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing standing sandboxes: %w", err)
	}

	for i := range list {
		if err := s.hydrateStandingSandbox(ctx, s.db, &list[i]); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// PutStandingSandbox writes or updates a standing sandbox definition and its secrets/env in a single transaction.
func (s *Store) PutStandingSandbox(ctx context.Context, sb StandingSandbox) error {
	if err := ValidateStandingSandbox(sb); err != nil {
		return err
	}

	return s.write(ctx, "put standing sandbox "+sb.Name, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `standing_sandbox` (`name`,`image`,`command`,`dir`,`cpus`,`memory_mb`,`disk_gb`,`network_access`,`enabled`) VALUES (?,?,?,?,?,?,?,?,?)",
			sb.Name, sb.Image, sb.Command, sb.Dir, sb.CPUs, sb.MemoryMB, sb.DiskGB, string(sb.NetworkAccess), sb.Enabled)
		if err != nil {
			return fmt.Errorf("saving standing sandbox row: %w", err)
		}

		// Replace secrets
		if _, err := tx.ExecContext(ctx, "DELETE FROM `standing_sandbox_secret` WHERE `sandbox_name` = ?", sb.Name); err != nil {
			return fmt.Errorf("clearing old standing sandbox secrets: %w", err)
		}
		for _, sec := range sb.Secrets {
			_, err := tx.ExecContext(ctx,
				"INSERT INTO `standing_sandbox_secret` (`sandbox_name`,`secret_name`,`env_var`,`file_path`) VALUES (?,?,?,?)",
				sb.Name, sec.SecretName, sec.EnvVar, sec.FilePath)
			if err != nil {
				return fmt.Errorf("inserting standing sandbox secret: %w", err)
			}
		}

		// Replace env
		if _, err := tx.ExecContext(ctx, "DELETE FROM `standing_sandbox_env` WHERE `sandbox_name` = ?", sb.Name); err != nil {
			return fmt.Errorf("clearing old standing sandbox env: %w", err)
		}
		var envKeys []string
		for k := range sb.Env {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
		for _, k := range envKeys {
			_, err := tx.ExecContext(ctx,
				"INSERT INTO `standing_sandbox_env` (`sandbox_name`,`name`,`value`) VALUES (?,?,?)",
				sb.Name, k, sb.Env[k])
			if err != nil {
				return fmt.Errorf("inserting standing sandbox env var: %w", err)
			}
		}

		return nil
	})
}

// DeleteStandingSandbox removes a standing sandbox and its associated secrets and env vars.
func (s *Store) DeleteStandingSandbox(ctx context.Context, name string) error {
	return s.write(ctx, "delete standing sandbox "+name, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM `standing_sandbox_secret` WHERE `sandbox_name` = ?", name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM `standing_sandbox_env` WHERE `sandbox_name` = ?", name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM `standing_sandbox` WHERE `name` = ?", name); err != nil {
			return err
		}
		return nil
	})
}
