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

package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/redact"
)

// GrainFeedback is one proposed improvement to grain itself, as the REST API
// and UI report it. Body is omitted in GET /api/feedback listings and returned
// whole on GET /api/feedback/{slug} and write responses.
type GrainFeedback struct {
	Slug            string     `json:"slug"`
	Title           string     `json:"title"`
	Surface         string     `json:"surface"`
	Status          string     `json:"status"`
	Body            string     `json:"body,omitempty"`
	Length          int        `json:"length"`
	UpstreamURL     string     `json:"upstreamUrl,omitempty"`
	Repos           []string   `json:"repos"`
	TaskIDs         []string   `json:"taskIds"`
	OccurrenceCount int        `json:"occurrenceCount"`
	FirstSeenAt     *time.Time `json:"firstSeenAt,omitempty"`
	LastSeenAt      *time.Time `json:"lastSeenAt,omitempty"`
	UpdatedAt       *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy       string     `json:"updatedBy,omitempty"`
	UpdatedByKind   string     `json:"updatedByKind,omitempty"`
}

// recordGrainFeedbackRequest is POST /api/feedback's body.
type recordGrainFeedbackRequest struct {
	Slug       string   `json:"slug"`
	Title      string   `json:"title"`
	Surface    string   `json:"surface,omitempty"`
	Body       string   `json:"body"`
	Repo       string   `json:"repo,omitempty"`
	Repos      []string `json:"repos,omitempty"`
	TaskID     string   `json:"taskId,omitempty"`
	TaskIDs    []string `json:"taskIds,omitempty"`
	Author     string   `json:"author,omitempty"`
	AuthorKind string   `json:"authorKind,omitempty"`
}

// recordGrainFeedbackResponse is POST /api/feedback's answer.
type recordGrainFeedbackResponse struct {
	GrainFeedback
	Created bool `json:"created"`
}

// patchGrainFeedbackRequest is PATCH /api/feedback/{slug}'s body.
type patchGrainFeedbackRequest struct {
	Title       *string `json:"title,omitempty"`
	Surface     *string `json:"surface,omitempty"`
	Status      *string `json:"status,omitempty"`
	Body        *string `json:"body,omitempty"`
	UpstreamURL *string `json:"upstreamUrl,omitempty"`
}

func grainFeedbackFrom(f model.GrainFeedback) GrainFeedback {
	repos := f.Repos
	if repos == nil {
		repos = []string{}
	}
	tasks := f.TaskIDs
	if tasks == nil {
		tasks = []string{}
	}
	out := GrainFeedback{
		Slug:            f.Slug,
		Title:           f.Title,
		Surface:         string(f.Surface),
		Status:          string(f.Status),
		Body:            f.Body,
		Length:          f.Length,
		UpstreamURL:     f.UpstreamURL,
		Repos:           repos,
		TaskIDs:         tasks,
		OccurrenceCount: f.OccurrenceCount,
		UpdatedBy:       f.UpdatedBy.ID,
		UpdatedByKind:   string(f.UpdatedBy.Kind),
	}
	if out.Length == 0 {
		out.Length = len(f.Body)
	}
	if !f.FirstSeenAt.IsZero() {
		at := f.FirstSeenAt
		out.FirstSeenAt = &at
	}
	if !f.LastSeenAt.IsZero() {
		at := f.LastSeenAt
		out.LastSeenAt = &at
	}
	if !f.UpdatedAt.IsZero() {
		at := f.UpdatedAt
		out.UpdatedAt = &at
	}
	return out
}

// redactFeedbackText scrubs any configured secret values out of feedback prose
// before it enters the database, since operators copy entries directly into
// public upstream issues.
func (s *Server) redactFeedbackText(ctx context.Context, title, body string) (string, string) {
	if s.tasks != nil && s.tasks.Config.Secrets != nil {
		if vals, err := secretRedactionValues(ctx, s); err == nil && len(vals) > 0 {
			lines, _ := redact.New(vals).Redact([]string{title, body})
			if len(lines) == 2 {
				return lines[0], lines[1]
			}
		}
	}
	return title, body
}

// ListGrainFeedback returns stored feedback entries, ordered open-first then by
// occurrence count and recency.
func (c *Client) ListGrainFeedback(ctx context.Context) ([]GrainFeedback, error) {
	stored, err := c.Store.ListGrainFeedback(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]GrainFeedback, 0, len(stored))
	for _, f := range stored {
		out = append(out, grainFeedbackFrom(f))
	}
	return out, nil
}

// GetGrainFeedback returns a single feedback entry with its full body.
func (c *Client) GetGrainFeedback(ctx context.Context, slug string) (GrainFeedback, error) {
	if err := model.ValidateGrainFeedbackSlug(slug); err != nil {
		return GrainFeedback{}, validationErrorf("%v", err)
	}
	stored, err := c.Store.GetGrainFeedback(ctx, slug)
	if err != nil {
		return GrainFeedback{}, err
	}
	if stored == nil {
		return GrainFeedback{}, &NotFoundError{ID: slug}
	}
	return grainFeedbackFrom(*stored), nil
}

// RecordGrainFeedback inserts or bumps a feedback entry and, when newly
// created, raises a single low-severity info notification for the operator.
func (c *Client) RecordGrainFeedback(ctx context.Context, req recordGrainFeedbackRequest, by model.Principal, actingTaskID string) (recordGrainFeedbackResponse, error) {
	if err := model.ValidateGrainFeedbackSlug(req.Slug); err != nil {
		return recordGrainFeedbackResponse{}, validationErrorf("%v", err)
	}
	surf, err := model.ParseGrainFeedbackSurface(req.Surface)
	if err != nil {
		return recordGrainFeedbackResponse{}, validationErrorf("%v", err)
	}
	repos := append([]string(nil), req.Repos...)
	if strings.TrimSpace(req.Repo) != "" {
		repos = append(repos, req.Repo)
	}
	for _, r := range repos {
		if strings.TrimSpace(r) != "" {
			if _, err := model.ParseRepo(r); err != nil {
				return recordGrainFeedbackResponse{}, validationErrorf("invalid repository %q: %v", r, err)
			}
		}
	}
	tasks := append([]string(nil), req.TaskIDs...)
	if strings.TrimSpace(req.TaskID) != "" {
		tasks = append(tasks, req.TaskID)
	}
	if actingTaskID != "" && len(tasks) == 0 {
		tasks = append(tasks, actingTaskID)
	}

	author := c.memoryAuthorOrActor(ctx, by)
	now := c.now()
	stored, created, err := c.Store.RecordGrainFeedback(ctx, model.GrainFeedbackRecord{
		Slug:    req.Slug,
		Title:   req.Title,
		Surface: surf,
		Body:    req.Body,
		Repos:   repos,
		TaskIDs: tasks,
		Now:     now,
		By:      author,
	})
	if err != nil {
		return recordGrainFeedbackResponse{}, validationErrorf("%v", err)
	}

	if created {
		primaryRepo := ""
		if len(stored.Repos) > 0 {
			primaryRepo = stored.Repos[0]
		}
		primaryTask := actingTaskID
		if primaryTask == "" && len(stored.TaskIDs) > 0 {
			primaryTask = stored.TaskIDs[0]
		}
		raisedBy := model.PrincipalString(author)
		if author == (model.Principal{}) {
			raisedBy = "system:feedback"
		}
		notifBody := fmt.Sprintf("Recorded under `%s` (surface: `%s`). Inspect and triage on Context → Grain Feedback.", stored.Slug, stored.Surface)
		_, _ = c.raise(ctx, RaiseNotificationRequest{
			Subject:  model.FamilyGrainFeedback + ":" + stored.Slug,
			Severity: string(model.NotificationInfo),
			Title:    "New Grain feedback proposal: " + stored.Title,
			Body:     notifBody,
			Task:     primaryTask,
			Repo:     primaryRepo,
		}, raisedBy)
	}

	return recordGrainFeedbackResponse{
		GrainFeedback: grainFeedbackFrom(stored),
		Created:       created,
	}, nil
}

// UpdateGrainFeedback applies a human triage update to an existing entry.
func (c *Client) UpdateGrainFeedback(ctx context.Context, slug string, req patchGrainFeedbackRequest, by model.Principal) (GrainFeedback, error) {
	if err := model.ValidateGrainFeedbackSlug(slug); err != nil {
		return GrainFeedback{}, validationErrorf("%v", err)
	}
	patch := model.GrainFeedbackPatch{
		Title:       req.Title,
		Body:        req.Body,
		UpstreamURL: req.UpstreamURL,
		Now:         c.now(),
		By:          c.memoryAuthorOrActor(ctx, by),
	}
	if req.Surface != nil {
		surf, err := model.ParseGrainFeedbackSurface(*req.Surface)
		if err != nil {
			return GrainFeedback{}, validationErrorf("%v", err)
		}
		patch.Surface = &surf
	}
	if req.Status != nil {
		st, err := model.ParseGrainFeedbackStatus(*req.Status)
		if err != nil {
			return GrainFeedback{}, validationErrorf("%v", err)
		}
		patch.Status = &st
	}
	if req.UpstreamURL != nil {
		if _, err := model.ValidateGrainFeedbackURL(*req.UpstreamURL); err != nil {
			return GrainFeedback{}, validationErrorf("%v", err)
		}
	}
	updated, err := c.Store.UpdateGrainFeedback(ctx, slug, patch)
	if err != nil {
		if errors.Is(err, model.ErrGrainFeedbackNotFound) {
			return GrainFeedback{}, &NotFoundError{ID: slug}
		}
		return GrainFeedback{}, validationErrorf("%v", err)
	}
	// If a human triages, files, or dismisses the item, clear any open first-seen
	// notification for this slug so the Inbox stays tidy.
	if patch.Status != nil && *patch.Status != model.GrainFeedbackProposed {
		actor := model.PrincipalString(patch.By)
		_, _ = c.Store.ClearNotification(ctx, model.FamilyGrainFeedback+":"+slug, actor)
	}
	return grainFeedbackFrom(updated), nil
}

// DeleteGrainFeedback removes an entry by slug, records a config_audit entry,
// and clears any open notification.
func (c *Client) DeleteGrainFeedback(ctx context.Context, slug string, by model.Principal) error {
	if err := model.ValidateGrainFeedbackSlug(slug); err != nil {
		return validationErrorf("%v", err)
	}
	if err := c.Store.DeleteGrainFeedback(ctx, slug); err != nil {
		if errors.Is(err, model.ErrGrainFeedbackNotFound) {
			return &NotFoundError{ID: slug}
		}
		return err
	}
	who := c.memoryAuthorOrActor(ctx, by)
	now := c.now()
	_, _ = c.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At:      now,
		By:      who,
		Surface: "feedback",
		Summary: "deleted grain feedback " + slug,
	})
	_, _ = c.Store.ClearNotification(ctx, model.FamilyGrainFeedback+":"+slug, model.PrincipalString(who))
	return nil
}

// handleListGrainFeedback answers GET /api/feedback.
func (s *Server) handleListGrainFeedback(w http.ResponseWriter, r *http.Request) {
	list, err := parseListQuery(r, listPageLarge)
	if err != nil {
		writeClientError(w, err)
		return
	}
	statusFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	repoFilter := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repoFilter != "" {
		if parsed, err := model.ParseRepo(repoFilter); err == nil {
			repoFilter = parsed.String()
		}
	}
	items, err := s.tasks.ListGrainFeedback(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	items = filterList(items, func(f GrainFeedback) bool {
		if statusFilter != "" {
			switch statusFilter {
			case "open":
				if f.Status != string(model.GrainFeedbackProposed) && f.Status != string(model.GrainFeedbackTriaged) {
					return false
				}
			default:
				if f.Status != statusFilter {
					return false
				}
			}
		}
		if repoFilter != "" && !slices.Contains(f.Repos, repoFilter) {
			return false
		}
		return list.matches(f.Slug, f.Title, f.Surface, f.Status, strings.Join(f.Repos, " "))
	})
	writeListBy(w, items, list, func(f GrainFeedback) string { return f.Slug })
}

// handleGetGrainFeedback answers GET /api/feedback/{slug}.
func (s *Server) handleGetGrainFeedback(w http.ResponseWriter, r *http.Request) {
	item, err := s.tasks.GetGrainFeedback(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// handleRecordGrainFeedback answers POST /api/feedback.
func (s *Server) handleRecordGrainFeedback(w http.ResponseWriter, r *http.Request) {
	var req recordGrainFeedbackRequest
	if !readJSON(w, r, &req) {
		return
	}
	by, err := bodyAuthor(r.Context(), "a feedback entry's author", req.Author, req.AuthorKind)
	if err != nil {
		writeClientError(w, err)
		return
	}
	req.Title, req.Body = s.redactFeedbackText(r.Context(), req.Title, req.Body)
	actingTask := strings.TrimSpace(r.Header.Get(ActingTaskHeader))
	res, err := s.tasks.RecordGrainFeedback(r.Context(), req, by, actingTask)
	if err != nil {
		writeClientError(w, err)
		return
	}
	code := http.StatusOK
	if res.Created {
		code = http.StatusCreated
	}
	writeJSON(w, code, res)
}

// handlePatchGrainFeedback answers PATCH /api/feedback/{slug}.
// Agents may record observations via POST /api/feedback, whereas triage state
// transitions (triaged, filed, rejected) belong to humans.
func (s *Server) handlePatchGrainFeedback(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get(ActingTaskHeader)) != "" {
		writeClientError(w, forbiddenErrorf("triage status edits on grain feedback are for a person; agents record sightings with POST /api/feedback"))
		return
	}
	var req patchGrainFeedbackRequest
	if !readJSON(w, r, &req) {
		return
	}
	by, err := bodyAuthor(r.Context(), "a feedback triage author", "", "")
	if err != nil {
		writeClientError(w, err)
		return
	}
	if req.Title != nil || req.Body != nil {
		t, b := "", ""
		if req.Title != nil {
			t = *req.Title
		}
		if req.Body != nil {
			b = *req.Body
		}
		rt, rb := s.redactFeedbackText(r.Context(), t, b)
		if req.Title != nil {
			req.Title = &rt
		}
		if req.Body != nil {
			req.Body = &rb
		}
	}
	updated, err := s.tasks.UpdateGrainFeedback(r.Context(), r.PathValue("slug"), req, by)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleDeleteGrainFeedback answers DELETE /api/feedback/{slug}.
func (s *Server) handleDeleteGrainFeedback(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get(ActingTaskHeader)) != "" {
		writeClientError(w, forbiddenErrorf("deleting grain feedback entries is for a person"))
		return
	}
	by, err := bodyAuthor(r.Context(), "a feedback deletion author", "", "")
	if err != nil {
		writeClientError(w, err)
		return
	}
	if err := s.tasks.DeleteGrainFeedback(r.Context(), r.PathValue("slug"), by); err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("slug")})
}
