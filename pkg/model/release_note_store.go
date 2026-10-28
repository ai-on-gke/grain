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

// GetReleaseNote reads one week's notes whole, or nil (with no error)
// for a week nothing has been written about -- the same "nobody has
// written one" nil GetMemory returns.
func (s *Store) GetReleaseNote(ctx context.Context, period string) (*ReleaseNote, error) {
	if err := ValidateReleaseNotePeriod(period); err != nil {
		return nil, err
	}
	var title, text, byKind, byID string
	var created, updated sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT `title`,`text`,`created_at`,`updated_at`,`updated_by_kind`,`updated_by_id` "+
			"FROM `release_note` WHERE `period` = ?", period).
		Scan(&title, &text, &created, &updated, &byKind, &byID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the release notes for %s: %w", period, err)
	}
	n := &ReleaseNote{
		Period: period, Title: title, Text: text, Length: len(text),
		UpdatedBy: Principal{Kind: PrincipalKind(byKind), ID: byID},
	}
	if created.Valid {
		n.CreatedAt = created.Time
	}
	if updated.Valid {
		n.UpdatedAt = updated.Time
	}
	return n, nil
}

// ListReleaseNotes returns every week that has notes, newest first,
// without their text.
//
// Newest first because that is the order a reader wants them in: the
// pane opens on the week just gone, and the archive below it goes
// backwards. The period sorts lexically in calendar order (that is what
// the zero-padded ISO spelling is for), so "newest first" is a plain
// descending sort rather than a parse per row.
//
// The text is deliberately not read, for ListMemoryDocuments' reason: a
// listing is a listing, and a year of weeks is a megabyte of prose
// nobody asked for. Length carries the size, measured in the query.
func (s *Store) ListReleaseNotes(ctx context.Context) ([]ReleaseNote, error) {
	var out []ReleaseNote
	err := each(ctx, s.db,
		"SELECT `period`,`title`,LENGTH(CAST(`text` AS BLOB)),`created_at`,`updated_at`,"+
			"`updated_by_kind`,`updated_by_id` FROM `release_note` ORDER BY `period` DESC",
		nil,
		func(rows *sql.Rows) error {
			var period, title, byKind, byID string
			var length int
			var created, updated sql.NullTime
			if err := rows.Scan(&period, &title, &length, &created, &updated, &byKind, &byID); err != nil {
				return err
			}
			n := ReleaseNote{
				Period: period, Title: title, Length: length,
				UpdatedBy: Principal{Kind: PrincipalKind(byKind), ID: byID},
			}
			if created.Valid {
				n.CreatedAt = created.Time
			}
			if updated.Valid {
				n.UpdatedAt = updated.Time
			}
			out = append(out, n)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing release notes: %w", err)
	}
	return out, nil
}

// PutReleaseNote replaces one week's notes wholesale, and deletes the
// row when the text is empty -- PutMemory's two behaviours, for
// PutMemory's reasons: a week that has been rewritten should read as one
// account rather than two, and a week somebody wants taken back out of
// the state repository has to have somewhere to go.
//
// CreatedAt is kept from the row already there rather than restamped, so
// a correction does not rewrite when the week was first reported on. The
// period is validated here as well as at the API because it is a key and
// a file name: a row filed under a spelling nothing else uses is a week
// that exists and cannot be read.
//
// UpdatedBy is written exactly as given, the zero Principal included --
// deciding who an unattributed write belongs to is the API's job
// (ui.Client.PutReleaseNote), and a store that guessed would make an
// unknown author indistinguishable from a claimed one.
func (s *Store) PutReleaseNote(ctx context.Context, n ReleaseNote) error {
	if err := ValidateReleaseNotePeriod(n.Period); err != nil {
		return err
	}
	text := strings.TrimSpace(n.Text)
	title := strings.TrimSpace(n.Title)
	if n.UpdatedAt.IsZero() {
		n.UpdatedAt = time.Now().UTC()
	}
	return s.write(ctx, "update the release notes for "+n.Period, func(tx *sql.Tx) error {
		if text == "" {
			_, err := tx.ExecContext(ctx, "DELETE FROM `release_note` WHERE `period` = ?", n.Period)
			return err
		}
		created := n.CreatedAt
		var existing sql.NullTime
		err := tx.QueryRowContext(ctx,
			"SELECT `created_at` FROM `release_note` WHERE `period` = ?", n.Period).Scan(&existing)
		switch {
		case err == nil && existing.Valid:
			created = existing.Time
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if created.IsZero() {
			created = n.UpdatedAt
		}
		_, err = tx.ExecContext(ctx,
			"REPLACE INTO `release_note` "+
				"(`period`,`title`,`text`,`created_at`,`updated_at`,`updated_by_kind`,`updated_by_id`) "+
				"VALUES (?,?,?,?,?,?,?)",
			n.Period, title, text, created.UTC(), n.UpdatedAt.UTC(),
			string(n.UpdatedBy.Kind), n.UpdatedBy.ID)
		return err
	})
}
