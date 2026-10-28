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
	"strings"
	"time"
)

// MemoryScope names a scope for MemoryDocument queries: Repo nil for the
// deployment's own, or one repository's.
type MemoryScope struct {
	Repo *RepoRef
}

// GetMemory reads one memory -- repo nil for the deployment-wide one --
// or nil (with no error) when nothing has been written for that scope.
func (s *Store) GetMemory(ctx context.Context, repo *RepoRef) (*Memory, error) {
	owner, name := memoryKey(repo)
	var text, byKind, byID string
	var updated sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT `text`,`updated_at`,`updated_by_kind`,`updated_by_id` FROM `memory` "+
			"WHERE `owner` = ? AND `name` = ?",
		owner, name).Scan(&text, &updated, &byKind, &byID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading memory for %s: %w", memoryScope(repo), err)
	}
	m := &Memory{Repo: repo, Text: text, UpdatedBy: Principal{Kind: PrincipalKind(byKind), ID: byID}}
	if updated.Valid {
		m.UpdatedAt = updated.Time
	}
	return m, nil
}

// ListMemories returns every memory that has something to say: the
// deployment-wide one first, then each repo's in name order.
func (s *Store) ListMemories(ctx context.Context) ([]Memory, error) {
	var out []Memory
	err := each(ctx, s.db,
		"SELECT `owner`,`name`,`text`,`updated_at`,`updated_by_kind`,`updated_by_id` FROM `memory` "+
			"ORDER BY `owner`,`name`",
		nil,
		func(rows *sql.Rows) error {
			var owner, name, text, byKind, byID string
			var updated sql.NullTime
			if err := rows.Scan(&owner, &name, &text, &updated, &byKind, &byID); err != nil {
				return err
			}
			m := Memory{Text: text, UpdatedBy: Principal{Kind: PrincipalKind(byKind), ID: byID}}
			if owner != "" || name != "" {
				m.Repo = &RepoRef{Owner: owner, Name: name}
			}
			if updated.Valid {
				m.UpdatedAt = updated.Time
			}
			out = append(out, m)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing memories: %w", err)
	}
	return out, nil
}

// PutMemory replaces one memory wholesale -- one row per scope, replaced
// rather than diffed, the same shape PutRepoConfig and PutConfig have.
//
// A memory whose text is empty (or nothing but whitespace) deletes the
// row instead of writing a blank one: forgetting is a thing the memory
// agent has to be able to do, and a scope that says nothing should leave
// nothing behind to be exported, listed or wondered about later.
//
// UpdatedBy is written exactly as given, the zero Principal included.
func (s *Store) PutMemory(ctx context.Context, m Memory) error {
	owner, name := memoryKey(m.Repo)
	text := strings.TrimSpace(m.Text)
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = time.Now().UTC()
	}
	return s.write(ctx, "update memory for "+memoryScope(m.Repo), func(tx *sql.Tx) error {
		if text == "" {
			_, err := tx.ExecContext(ctx,
				"DELETE FROM `memory` WHERE `owner` = ? AND `name` = ?", owner, name)
			return err
		}
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `memory` (`owner`,`name`,`text`,`updated_at`,`updated_by_kind`,`updated_by_id`) "+
				"VALUES (?,?,?,?,?,?)",
			owner, name, text, m.UpdatedAt.UTC(), string(m.UpdatedBy.Kind), m.UpdatedBy.ID)
		return err
	})
}

// GetMemoryDocument reads one named document -- repo nil for one filed
// under the deployment itself -- or nil (with no error) when that scope
// has no document by that name, the same "nobody has written one" nil
// GetMemory returns.
func (s *Store) GetMemoryDocument(ctx context.Context, repo *RepoRef, name string) (*MemoryDocument, error) {
	owner, scope := memoryKey(repo)
	var summary, text, byKind, byID string
	var updated sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT `summary`,`text`,`updated_at`,`updated_by_kind`,`updated_by_id` FROM `memory_document` "+
			"WHERE `owner` = ? AND `name` = ? AND `document` = ?",
		owner, scope, name).Scan(&summary, &text, &updated, &byKind, &byID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading memory document %q for %s: %w", name, memoryScope(repo), err)
	}
	d := &MemoryDocument{
		Repo: repo, Name: name, Summary: summary, Text: text, Length: len(text),
		UpdatedBy: Principal{Kind: PrincipalKind(byKind), ID: byID},
	}
	if updated.Valid {
		d.UpdatedAt = updated.Time
	}
	return d, nil
}

// ListMemoryDocuments returns what documents exist, without their text:
// every scope's when called with no scopes at all, and otherwise only
// the scopes named.
//
// Ordered by owner, name, and then document name, so the deployment's
// own come first.
func (s *Store) ListMemoryDocuments(ctx context.Context, scopes ...MemoryScope) ([]MemoryDocument, error) {
	query := "SELECT `owner`,`name`,`document`,`summary`,LENGTH(CAST(`text` AS BLOB)),`updated_at`," +
		"`updated_by_kind`,`updated_by_id` FROM `memory_document`"
	var args []any
	if len(scopes) > 0 {
		var where []string
		for _, scope := range scopes {
			owner, name := memoryKey(scope.Repo)
			where = append(where, "(`owner` = ? AND `name` = ?)")
			args = append(args, owner, name)
		}
		query += " WHERE " + strings.Join(where, " OR ")
	}
	query += " ORDER BY `owner`,`name`,`document`"
	var out []MemoryDocument
	err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
		var owner, name, document, summary, byKind, byID string
		var length int
		var updated sql.NullTime
		if err := rows.Scan(&owner, &name, &document, &summary, &length, &updated, &byKind, &byID); err != nil {
			return err
		}
		d := MemoryDocument{
			Name: document, Summary: summary, Length: length,
			UpdatedBy: Principal{Kind: PrincipalKind(byKind), ID: byID},
		}
		if owner != "" || name != "" {
			d.Repo = &RepoRef{Owner: owner, Name: name}
		}
		if updated.Valid {
			d.UpdatedAt = updated.Time
		}
		out = append(out, d)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing memory documents: %w", err)
	}
	return out, nil
}

// PutMemoryDocument replaces one document wholesale, and deletes it when
// its text is empty.
func (s *Store) PutMemoryDocument(ctx context.Context, d MemoryDocument) error {
	if err := ValidateMemoryDocumentName(d.Name); err != nil {
		return err
	}
	owner, scope := memoryKey(d.Repo)
	text := strings.TrimSpace(d.Text)
	summary := strings.TrimSpace(d.Summary)
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = time.Now().UTC()
	}
	return s.write(ctx, "update memory document "+d.Name+" for "+memoryScope(d.Repo), func(tx *sql.Tx) error {
		if text == "" {
			_, err := tx.ExecContext(ctx,
				"DELETE FROM `memory_document` WHERE `owner` = ? AND `name` = ? AND `document` = ?",
				owner, scope, d.Name)
			return err
		}
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `memory_document` "+
				"(`owner`,`name`,`document`,`summary`,`text`,`updated_at`,`updated_by_kind`,`updated_by_id`) "+
				"VALUES (?,?,?,?,?,?,?,?)",
			owner, scope, d.Name, summary, text, d.UpdatedAt.UTC(),
			string(d.UpdatedBy.Kind), d.UpdatedBy.ID)
		return err
	})
}

// memoryKey is the (owner, name) a memory is stored under: the repo's
// own for a repo memory, and two empty strings for the deployment-wide
// memory.
func memoryKey(repo *RepoRef) (string, string) {
	if repo == nil {
		return "", ""
	}
	return repo.Owner, repo.Name
}

// memoryScope names a memory in an error message or prompt.
func memoryScope(repo *RepoRef) string {
	if repo == nil {
		return "this deployment"
	}
	return repo.String()
}
