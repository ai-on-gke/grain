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
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrNotFound = errors.New("qa flow version not found")

type QAFlowVersion struct {
	Hash         string
	Key          string
	Version      int
	ParentHash   string
	Label        string
	Summary      string
	SpecJSON     string
	SourceYAML   string
	QAEngineRev  int
	CreatedBy    string
	CreatedAt    time.Time
	RetiredAt    *time.Time
	SchemaVer    string
	SourceKind   string
	SourceRepo   string
	SourcePath   string
	SourceCommit string
}

func (s *Store) InsertQAFlowVersion(ctx context.Context, fv QAFlowVersion) error {
	if fv.SchemaVer == "" {
		fv.SchemaVer = "grain.dev/qa-flow/v2"
	}
	if fv.SourceKind == "" {
		fv.SourceKind = "ui"
	}
	return s.write(ctx, "insert qa flow version", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `qa_flow_version` (`hash`, `key`, `version`, `parent_hash`, `label`, `summary`, `spec_json`, `source_yaml`, `qa_engine_rev`, `created_by`, `created_at`, `retired_at`, `schema_ver`, `source_kind`, `source_repo`, `source_path`, `source_commit`) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			fv.Hash, fv.Key, fv.Version, fv.ParentHash, fv.Label, fv.Summary, fv.SpecJSON, fv.SourceYAML, fv.QAEngineRev, fv.CreatedBy, fv.CreatedAt, fv.RetiredAt, fv.SchemaVer, fv.SourceKind, fv.SourceRepo, fv.SourcePath, fv.SourceCommit,
		)
		return err
	})
}

func (s *Store) GetQAFlowVersion(ctx context.Context, hash string) (QAFlowVersion, error) {
	var fv QAFlowVersion
	var retired sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT `hash`, `key`, `version`, `parent_hash`, `label`, `summary`, `spec_json`, `source_yaml`, `qa_engine_rev`, `created_by`, `created_at`, `retired_at`, `schema_ver`, `source_kind`, `source_repo`, `source_path`, `source_commit` FROM `qa_flow_version` WHERE `hash` = ?", hash,
	).Scan(&fv.Hash, &fv.Key, &fv.Version, &fv.ParentHash, &fv.Label, &fv.Summary, &fv.SpecJSON, &fv.SourceYAML, &fv.QAEngineRev, &fv.CreatedBy, &fv.CreatedAt, &retired, &fv.SchemaVer, &fv.SourceKind, &fv.SourceRepo, &fv.SourcePath, &fv.SourceCommit)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QAFlowVersion{}, ErrNotFound
		}
		return QAFlowVersion{}, err
	}
	fv.CreatedAt = fv.CreatedAt.UTC()
	if retired.Valid {
		t := retired.Time.UTC()
		fv.RetiredAt = &t
	}
	return fv, nil
}

func (s *Store) GetQAFlowVersionByKeyVersion(ctx context.Context, key string, version int) (QAFlowVersion, error) {
	var fv QAFlowVersion
	var retired sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT `hash`, `key`, `version`, `parent_hash`, `label`, `summary`, `spec_json`, `source_yaml`, `qa_engine_rev`, `created_by`, `created_at`, `retired_at`, `schema_ver`, `source_kind`, `source_repo`, `source_path`, `source_commit` FROM `qa_flow_version` WHERE `key` = ? AND `version` = ?", key, version,
	).Scan(&fv.Hash, &fv.Key, &fv.Version, &fv.ParentHash, &fv.Label, &fv.Summary, &fv.SpecJSON, &fv.SourceYAML, &fv.QAEngineRev, &fv.CreatedBy, &fv.CreatedAt, &retired, &fv.SchemaVer, &fv.SourceKind, &fv.SourceRepo, &fv.SourcePath, &fv.SourceCommit)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QAFlowVersion{}, ErrNotFound
		}
		return QAFlowVersion{}, err
	}
	fv.CreatedAt = fv.CreatedAt.UTC()
	if retired.Valid {
		t := retired.Time.UTC()
		fv.RetiredAt = &t
	}
	return fv, nil
}

func (s *Store) NextQAFlowVersionNumber(ctx context.Context, key string) (int, error) {
	var max sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT MAX(`version`) FROM `qa_flow_version` WHERE `key` = ?", key).Scan(&max)
	if err != nil {
		return 0, err
	}
	if !max.Valid {
		return 1, nil
	}
	return int(max.Int64) + 1, nil
}

func (s *Store) ListQAFlowVersions(ctx context.Context, key string, limit int) ([]QAFlowVersion, error) {
	query := "SELECT `hash`, `key`, `version`, `parent_hash`, `label`, `summary`, `spec_json`, `source_yaml`, `qa_engine_rev`, `created_by`, `created_at`, `retired_at`, `schema_ver`, `source_kind`, `source_repo`, `source_path`, `source_commit` FROM `qa_flow_version`"
	args := []any{}
	if key != "" {
		query += " WHERE `key` = ?"
		args = append(args, key)
	}
	query += " ORDER BY `created_at` DESC, `hash` ASC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QAFlowVersion
	for rows.Next() {
		var fv QAFlowVersion
		var retired sql.NullTime
		if err := rows.Scan(&fv.Hash, &fv.Key, &fv.Version, &fv.ParentHash, &fv.Label, &fv.Summary, &fv.SpecJSON, &fv.SourceYAML, &fv.QAEngineRev, &fv.CreatedBy, &fv.CreatedAt, &retired, &fv.SchemaVer, &fv.SourceKind, &fv.SourceRepo, &fv.SourcePath, &fv.SourceCommit); err != nil {
			return nil, err
		}
		fv.CreatedAt = fv.CreatedAt.UTC()
		if retired.Valid {
			t := retired.Time.UTC()
			fv.RetiredAt = &t
		}
		out = append(out, fv)
	}
	return out, rows.Err()
}

func (s *Store) RetireQAFlowVersion(ctx context.Context, hash string, at time.Time) error {
	return s.write(ctx, "retire qa flow version", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE `qa_flow_version` SET `retired_at` = ? WHERE `hash` = ?", at.UTC(), hash)
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
		return nil
	})
}
