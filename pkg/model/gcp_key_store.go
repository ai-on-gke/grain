package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The deployment's GCP keys are gcp_key's rows, keyed by name. They were
// team_gcp_key's rows under DefaultTeamID until schema 23
// (docs/single-tenant-reset.md, 6-ii-b3).
const gcpKeyColumns = "`name`,`project_id`,`service_account_email`,`service_account_count`,`minter_credential`,`max_key_age_seconds`,`description`,`created_at`,`updated_at`"

func scanGCPKey(scan func(...any) error) (GCPKey, error) {
	var k GCPKey
	var maxKeyAgeSec int64
	var createdAt, updatedAt time.Time
	if err := scan(&k.Name, &k.ProjectID, &k.ServiceAccountEmail,
		&k.ServiceAccountCount, &k.MinterCredential, &maxKeyAgeSec,
		&k.Description, &createdAt, &updatedAt); err != nil {
		return GCPKey{}, err
	}
	k.MaxKeyAge = time.Duration(maxKeyAgeSec) * time.Second
	k.CreatedAt = createdAt.UTC()
	k.UpdatedAt = updatedAt.UTC()
	if k.ServiceAccountCount <= 0 {
		k.ServiceAccountCount = 1
	}
	return k, nil
}

// GetGCPKey returns the deployment's GCP key called name, or nil when
// there is none.
func (s *Store) GetGCPKey(ctx context.Context, name string) (*GCPKey, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+gcpKeyColumns+" FROM `gcp_key` WHERE `name` = ?", name)
	k, err := scanGCPKey(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading GCP key %s: %w", name, err)
	}
	return &k, nil
}

// ListGCPKeys returns the deployment's GCP keys, ordered by name.
func (s *Store) ListGCPKeys(ctx context.Context) ([]GCPKey, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+gcpKeyColumns+" FROM `gcp_key` ORDER BY `name` ASC")
	if err != nil {
		return nil, fmt.Errorf("listing GCP keys: %w", err)
	}
	defer rows.Close()

	var keys []GCPKey
	for rows.Next() {
		k, err := scanGCPKey(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning GCP key: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating GCP keys: %w", err)
	}
	return keys, nil
}

// PutGCPKey creates or updates one of the deployment's GCP keys.
func (s *Store) PutGCPKey(ctx context.Context, k GCPKey) error {
	if err := k.Validate(); err != nil {
		return err
	}
	return s.write(ctx, "put GCP key", func(tx *sql.Tx) error {
		now := time.Now().UTC()
		if k.CreatedAt.IsZero() {
			var existingCreatedAt time.Time
			err := tx.QueryRowContext(ctx,
				"SELECT `created_at` FROM `gcp_key` WHERE `name` = ?", k.Name).Scan(&existingCreatedAt)
			if err == nil {
				k.CreatedAt = existingCreatedAt.UTC()
			} else {
				k.CreatedAt = now
			}
		}
		k.UpdatedAt = now
		if k.ServiceAccountCount <= 0 {
			k.ServiceAccountCount = 1
		}

		maxAgeSec := int64(k.MaxKeyAge / time.Second)
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `gcp_key` ("+gcpKeyColumns+") VALUES (?,?,?,?,?,?,?,?,?)",
			k.Name, k.ProjectID, k.ServiceAccountEmail,
			k.ServiceAccountCount, k.MinterCredential, maxAgeSec,
			k.Description, k.CreatedAt, k.UpdatedAt)
		if err != nil {
			return fmt.Errorf("saving GCP key %s: %w", k.Name, err)
		}
		return nil
	})
}

// DeleteGCPKey deletes the deployment's GCP key called name.
func (s *Store) DeleteGCPKey(ctx context.Context, name string) error {
	return s.write(ctx, "delete GCP key", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `gcp_key` WHERE `name` = ?", name)
		if err != nil {
			return fmt.Errorf("deleting GCP key %s: %w", name, err)
		}
		return nil
	})
}
