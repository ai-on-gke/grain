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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrGrainFeedbackNotFound reports that no grain_feedback row exists for a slug.
var ErrGrainFeedbackNotFound = errors.New("grain feedback entry not found")

func canonicalRepoString(raw string) string {
	r, err := ParseRepo(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return r.String()
}

func decodeStringSlice(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out) == 0 {
		return nil
	}
	return out
}

func encodeStringSlice(vals []string) string {
	if len(vals) == 0 {
		return "[]"
	}
	b, err := json.Marshal(vals)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func getGrainFeedbackTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, slug string) (*GrainFeedback, error) {
	var title, surface, status, body, upstreamURL, reposJSON, tasksJSON, byKind, byID, bySource string
	var count int
	var firstSeen, lastSeen, updated sql.NullTime
	err := q.QueryRowContext(ctx,
		"SELECT `title`,`surface`,`status`,`body`,`upstream_url`,`repos_json`,`task_ids_json`,"+
			"`occurrence_count`,`first_seen_at`,`last_seen_at`,`updated_at`,"+
			"`updated_by_kind`,`updated_by_id`,`updated_by_source` "+
			"FROM `grain_feedback` WHERE `slug` = ?", slug).
		Scan(&title, &surface, &status, &body, &upstreamURL, &reposJSON, &tasksJSON,
			&count, &firstSeen, &lastSeen, &updated, &byKind, &byID, &bySource)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading grain feedback %q: %w", slug, err)
	}
	surf, err := ParseGrainFeedbackSurface(surface)
	if err != nil {
		surf = GrainFeedbackSurfaceOther
	}
	st, err := ParseGrainFeedbackStatus(status)
	if err != nil {
		st = GrainFeedbackProposed
	}
	f := &GrainFeedback{
		Slug:            slug,
		Title:           title,
		Surface:         surf,
		Status:          st,
		Body:            body,
		Length:          len(body),
		UpstreamURL:     upstreamURL,
		Repos:           decodeStringSlice(reposJSON),
		TaskIDs:         decodeStringSlice(tasksJSON),
		OccurrenceCount: count,
		UpdatedBy:       Principal{Kind: PrincipalKind(byKind), ID: byID, Source: AuthSource(bySource)},
	}
	if firstSeen.Valid {
		f.FirstSeenAt = firstSeen.Time
	}
	if lastSeen.Valid {
		f.LastSeenAt = lastSeen.Time
	}
	if updated.Valid {
		f.UpdatedAt = updated.Time
	}
	return f, nil
}

// GetGrainFeedback reads one feedback entry whole, or nil when none exists.
func (s *Store) GetGrainFeedback(ctx context.Context, slug string) (*GrainFeedback, error) {
	if err := ValidateGrainFeedbackSlug(slug); err != nil {
		return nil, err
	}
	return getGrainFeedbackTx(ctx, s.db, slug)
}

// ListGrainFeedback lists feedback entries without their full Markdown body,
// ordered with open items first, then highest occurrence count, then most
// recently seen.
func (s *Store) ListGrainFeedback(ctx context.Context) ([]GrainFeedback, error) {
	var out []GrainFeedback
	err := each(ctx, s.db,
		"SELECT `slug`,`title`,`surface`,`status`,LENGTH(CAST(`body` AS BLOB)),`upstream_url`,"+
			"`repos_json`,`task_ids_json`,`occurrence_count`,`first_seen_at`,`last_seen_at`,"+
			"`updated_at`,`updated_by_kind`,`updated_by_id`,`updated_by_source` "+
			"FROM `grain_feedback` "+
			"ORDER BY CASE WHEN `status` IN ('proposed','triaged') THEN 0 ELSE 1 END ASC, "+
			"`occurrence_count` DESC, `last_seen_at` DESC, `slug` ASC",
		nil,
		func(rows *sql.Rows) error {
			var slug, title, surface, status, upstreamURL, reposJSON, tasksJSON, byKind, byID, bySource string
			var length, count int
			var firstSeen, lastSeen, updated sql.NullTime
			if err := rows.Scan(&slug, &title, &surface, &status, &length, &upstreamURL,
				&reposJSON, &tasksJSON, &count, &firstSeen, &lastSeen,
				&updated, &byKind, &byID, &bySource); err != nil {
				return err
			}
			surf, err := ParseGrainFeedbackSurface(surface)
			if err != nil {
				surf = GrainFeedbackSurfaceOther
			}
			st, err := ParseGrainFeedbackStatus(status)
			if err != nil {
				st = GrainFeedbackProposed
			}
			f := GrainFeedback{
				Slug:            slug,
				Title:           title,
				Surface:         surf,
				Status:          st,
				Length:          length,
				UpstreamURL:     upstreamURL,
				Repos:           decodeStringSlice(reposJSON),
				TaskIDs:         decodeStringSlice(tasksJSON),
				OccurrenceCount: count,
				UpdatedBy:       Principal{Kind: PrincipalKind(byKind), ID: byID, Source: AuthSource(bySource)},
			}
			if firstSeen.Valid {
				f.FirstSeenAt = firstSeen.Time
			}
			if lastSeen.Valid {
				f.LastSeenAt = lastSeen.Time
			}
			if updated.Valid {
				f.UpdatedAt = updated.Time
			}
			out = append(out, f)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing grain feedback: %w", err)
	}
	return out, nil
}

// RecordGrainFeedback inserts a new feedback entry (returning created=true) or
// merges a repeat observation into an existing slug (created=false).
//
// Crucially, an agent recording a repeat observation never resets a human-set
// Status (triaged, filed, rejected) or UpstreamURL, and when an entry has
// already been triaged/filed/rejected by a person its curated Title and Body
// are kept intact while OccurrenceCount, LastSeenAt, Repos and TaskIDs still
// record the new sighting.
func (s *Store) RecordGrainFeedback(ctx context.Context, rec GrainFeedbackRecord) (GrainFeedback, bool, error) {
	if err := ValidateGrainFeedbackSlug(rec.Slug); err != nil {
		return GrainFeedback{}, false, err
	}
	surf, err := ParseGrainFeedbackSurface(string(rec.Surface))
	if err != nil {
		return GrainFeedback{}, false, err
	}
	title := strings.TrimSpace(rec.Title)
	body := strings.TrimSpace(rec.Body)
	if len(title) > MaxGrainFeedbackTitleLength {
		return GrainFeedback{}, false, fmt.Errorf("feedback title is %d characters; at most %d allowed", len(title), MaxGrainFeedbackTitleLength)
	}
	if len(body) > MaxGrainFeedbackBodyLength {
		return GrainFeedback{}, false, fmt.Errorf("feedback body is %d bytes; at most %d allowed", len(body), MaxGrainFeedbackBodyLength)
	}
	for _, r := range rec.Repos {
		if strings.TrimSpace(r) != "" {
			if _, err := ParseRepo(r); err != nil {
				return GrainFeedback{}, false, fmt.Errorf("invalid repository %q: %w", r, err)
			}
		}
	}
	now := rec.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	var out GrainFeedback
	var created bool
	err = s.write(ctx, "record grain feedback "+rec.Slug, func(tx *sql.Tx) error {
		existing, err := getGrainFeedbackTx(ctx, tx, rec.Slug)
		if err != nil {
			return err
		}
		if existing == nil {
			if title == "" {
				return fmt.Errorf("a new feedback entry needs a title")
			}
			if body == "" {
				return fmt.Errorf("a new feedback entry needs a body describing the friction and suggested change")
			}
			repos := normalizeFeedbackRefs(nil, rec.Repos, canonicalRepoString)
			tasks := normalizeFeedbackRefs(nil, rec.TaskIDs, NormalizeTaskID)
			_, err := tx.ExecContext(ctx,
				"INSERT INTO `grain_feedback` "+
					"(`slug`,`title`,`surface`,`status`,`body`,`upstream_url`,`repos_json`,`task_ids_json`,"+
					"`occurrence_count`,`first_seen_at`,`last_seen_at`,`updated_at`,"+
					"`updated_by_kind`,`updated_by_id`,`updated_by_source`) "+
					"VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
				rec.Slug, title, string(surf), string(GrainFeedbackProposed), body, "",
				encodeStringSlice(repos), encodeStringSlice(tasks),
				1, now, now, now,
				string(rec.By.Kind), rec.By.ID, string(rec.By.Source))
			if err != nil {
				return err
			}
			created = true
			out = GrainFeedback{
				Slug:            rec.Slug,
				Title:           title,
				Surface:         surf,
				Status:          GrainFeedbackProposed,
				Body:            body,
				Length:          len(body),
				Repos:           repos,
				TaskIDs:         tasks,
				OccurrenceCount: 1,
				FirstSeenAt:     now,
				LastSeenAt:      now,
				UpdatedAt:       now,
				UpdatedBy:       rec.By,
			}
			return nil
		}

		repos := normalizeFeedbackRefs(existing.Repos, rec.Repos, canonicalRepoString)
		tasks := normalizeFeedbackRefs(existing.TaskIDs, rec.TaskIDs, NormalizeTaskID)
		nextTitle := existing.Title
		nextBody := existing.Body
		nextSurf := existing.Surface
		nextBy := existing.UpdatedBy
		nextUpdated := existing.UpdatedAt
		// Only refresh prose while the entry is still in untouched `proposed`
		// state (or when a human is the caller). Once a human triages, files
		// or dismisses an entry, agents only increment sightings and evidence.
		if existing.Status == GrainFeedbackProposed || rec.By.Kind == PrincipalHuman {
			if title != "" {
				nextTitle = title
			}
			if body != "" {
				nextBody = body
			}
			if rec.Surface != "" {
				nextSurf = surf
			}
			nextBy = rec.By
			nextUpdated = now
		}
		count := existing.OccurrenceCount + 1
		_, err = tx.ExecContext(ctx,
			"UPDATE `grain_feedback` SET "+
				"`title` = ?, `surface` = ?, `body` = ?, `repos_json` = ?, `task_ids_json` = ?, "+
				"`occurrence_count` = ?, `last_seen_at` = ?, `updated_at` = ?, "+
				"`updated_by_kind` = ?, `updated_by_id` = ?, `updated_by_source` = ? "+
				"WHERE `slug` = ?",
			nextTitle, string(nextSurf), nextBody, encodeStringSlice(repos), encodeStringSlice(tasks),
			count, now, nextUpdated,
			string(nextBy.Kind), nextBy.ID, string(nextBy.Source), rec.Slug)
		if err != nil {
			return err
		}
		out = *existing
		out.Title = nextTitle
		out.Surface = nextSurf
		out.Body = nextBody
		out.Length = len(nextBody)
		out.Repos = repos
		out.TaskIDs = tasks
		out.OccurrenceCount = count
		out.LastSeenAt = now
		out.UpdatedAt = nextUpdated
		out.UpdatedBy = nextBy
		return nil
	})
	return out, created, err
}

// UpdateGrainFeedback applies a human triage patch (status, upstream URL,
// title, surface or body) to an existing entry.
func (s *Store) UpdateGrainFeedback(ctx context.Context, slug string, patch GrainFeedbackPatch) (GrainFeedback, error) {
	if err := ValidateGrainFeedbackSlug(slug); err != nil {
		return GrainFeedback{}, err
	}
	now := patch.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	var out GrainFeedback
	err := s.write(ctx, "triage grain feedback "+slug, func(tx *sql.Tx) error {
		existing, err := getGrainFeedbackTx(ctx, tx, slug)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrGrainFeedbackNotFound
		}
		title := existing.Title
		if patch.Title != nil {
			t := strings.TrimSpace(*patch.Title)
			if t == "" {
				return fmt.Errorf("a feedback entry needs a title")
			}
			if len(t) > MaxGrainFeedbackTitleLength {
				return fmt.Errorf("feedback title is %d characters; at most %d allowed", len(t), MaxGrainFeedbackTitleLength)
			}
			title = t
		}
		surf := existing.Surface
		if patch.Surface != nil {
			parsed, err := ParseGrainFeedbackSurface(string(*patch.Surface))
			if err != nil {
				return err
			}
			surf = parsed
		}
		status := existing.Status
		if patch.Status != nil {
			parsed, err := ParseGrainFeedbackStatus(string(*patch.Status))
			if err != nil {
				return err
			}
			status = parsed
		}
		body := existing.Body
		if patch.Body != nil {
			b := strings.TrimSpace(*patch.Body)
			if b == "" {
				return fmt.Errorf("a feedback entry needs a body")
			}
			if len(b) > MaxGrainFeedbackBodyLength {
				return fmt.Errorf("feedback body is %d bytes; at most %d allowed", len(b), MaxGrainFeedbackBodyLength)
			}
			body = b
		}
		upstreamURL := existing.UpstreamURL
		if patch.UpstreamURL != nil {
			u, err := ValidateGrainFeedbackURL(*patch.UpstreamURL)
			if err != nil {
				return err
			}
			upstreamURL = u
		}
		_, err = tx.ExecContext(ctx,
			"UPDATE `grain_feedback` SET "+
				"`title` = ?, `surface` = ?, `status` = ?, `body` = ?, `upstream_url` = ?, "+
				"`updated_at` = ?, `updated_by_kind` = ?, `updated_by_id` = ?, `updated_by_source` = ? "+
				"WHERE `slug` = ?",
			title, string(surf), string(status), body, upstreamURL,
			now, string(patch.By.Kind), patch.By.ID, string(patch.By.Source), slug)
		if err != nil {
			return err
		}
		out = *existing
		out.Title = title
		out.Surface = surf
		out.Status = status
		out.Body = body
		out.Length = len(body)
		out.UpstreamURL = upstreamURL
		out.UpdatedAt = now
		out.UpdatedBy = patch.By
		return nil
	})
	return out, err
}

// DeleteGrainFeedback deletes one feedback entry by slug.
func (s *Store) DeleteGrainFeedback(ctx context.Context, slug string) error {
	if err := ValidateGrainFeedbackSlug(slug); err != nil {
		return err
	}
	return s.write(ctx, "delete grain feedback "+slug, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM `grain_feedback` WHERE `slug` = ?", slug)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n == 0 {
			return ErrGrainFeedbackNotFound
		}
		return nil
	})
}
