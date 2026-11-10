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

// A task's context over the API: its compaction cursor, handover age and
// current brief (model.TaskContext,
// design/design/durable-tasks.design.md#DT-15), read by the task view and
// `grain task context`, and changed by `grain task compact` and
// `grain task brief`; and rolling a task over to a successor
// (`grain rollover`, model.Store.Rollover).

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TaskContextView is GET /api/tasks/{id}/context.
type TaskContextView struct {
	model.TaskContext
	// OriginalDescription is the task's description as it was filed,
	// which the current brief (when there is one) replaces in prompts.
	OriginalDescription string                `json:"original_description"`
	HandoverAge         model.HandoverAge     `json:"handover_age"`
	HandoverStale       bool                  `json:"handover_stale"`
	BriefEdits          []model.TaskBriefEdit `json:"brief_edits,omitempty"`
	// Summarised is how many of the task's comments sit at or before
	// the compaction cursor: still in the record and the UI, collapsed
	// under the handover, and left out of prompts.
	Summarised int `json:"summarised"`
}

// CompactRequest is POST /api/tasks/{id}/compact. With a handover, the
// task is compacted to it at once, through every comment posted so far.
// Without one, the task's next run is asked to compact first.
type CompactRequest struct {
	Handover string `json:"handover,omitempty"`
}

// BriefRequest is POST /api/tasks/{id}/brief. An empty brief reverts
// prompts to the original description.
type BriefRequest struct {
	Brief  string `json:"brief"`
	Reason string `json:"reason"`
}

// TaskContext reads id's context view.
func (c *Client) TaskContext(ctx context.Context, id string) (TaskContextView, error) {
	id = model.NormalizeTaskID(id)
	t, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return TaskContextView{}, err
	}
	if t == nil {
		return TaskContextView{}, &NotFoundError{ID: id}
	}
	tc, err := c.Store.TaskContext(ctx, id)
	if err != nil {
		return TaskContextView{}, err
	}
	age, err := c.Store.TaskHandoverAge(ctx, id)
	if err != nil {
		return TaskContextView{}, err
	}
	edits, err := c.Store.TaskBriefEdits(ctx, id)
	if err != nil {
		return TaskContextView{}, err
	}
	view := TaskContextView{
		TaskContext:         tc,
		OriginalDescription: t.Body,
		HandoverAge:         age,
		HandoverStale:       age.Stale(),
		BriefEdits:          edits,
	}
	if tc.CompactedThrough > 0 {
		comments, err := c.Store.Comments(ctx, id)
		if err != nil {
			return TaskContextView{}, err
		}
		for _, cm := range comments {
			if cm.ID <= tc.CompactedThrough {
				view.Summarised++
			}
		}
	}
	return view, nil
}

// CompactTask compacts id to req.Handover at once, or asks its next run
// to compact when there is none. by is who is asking.
func (c *Client) CompactTask(ctx context.Context, id string, by model.Principal, req CompactRequest) (TaskContextView, error) {
	id = model.NormalizeTaskID(id)
	if _, err := c.TaskContext(ctx, id); err != nil {
		return TaskContextView{}, err
	}
	now := c.now()
	label := principalLabel(by)
	if strings.TrimSpace(req.Handover) == "" {
		if err := c.Store.RequestCompaction(ctx, id, label, now); err != nil {
			return TaskContextView{}, err
		}
		return c.TaskContext(ctx, id)
	}
	if _, err := c.Store.CompactTask(ctx, id, req.Handover, 0, model.Attribution{Actor: by}, label, now); err != nil {
		return TaskContextView{}, err
	}
	return c.TaskContext(ctx, id)
}

// SetTaskBrief replaces id's current brief.
func (c *Client) SetTaskBrief(ctx context.Context, id string, by model.Principal, req BriefRequest) (TaskContextView, error) {
	id = model.NormalizeTaskID(id)
	if _, err := c.TaskContext(ctx, id); err != nil {
		return TaskContextView{}, err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return TaskContextView{}, validationErrorf("say why the brief changed (reason)")
	}
	if err := c.Store.SetTaskBrief(ctx, id, req.Brief, principalLabel(by), req.Reason, c.now()); err != nil {
		return TaskContextView{}, err
	}
	return c.TaskContext(ctx, id)
}

// RolloverTask rolls id over to a successor (model.Store.Rollover): a
// fresh task carrying its current brief, handover and settings, with id
// closed as rolled_over, under the name of whoever ctx says is asking.
func (c *Client) RolloverTask(ctx context.Context, id string) (model.RolloverResult, error) {
	id = model.NormalizeTaskID(id)
	t, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return model.RolloverResult{}, err
	}
	if t == nil {
		return model.RolloverResult{}, &NotFoundError{ID: id}
	}
	result, err := c.Store.Rollover(ctx, id, c.requestActor(ctx), "", c.now())
	if errors.Is(err, model.ErrRolloverRefused) {
		return model.RolloverResult{}, validationErrorf("%s", err.Error())
	}
	if err != nil {
		return model.RolloverResult{}, err
	}
	if c.Config.Wake != nil {
		c.Config.Wake()
	}
	return result, nil
}

func contextActor(r *http.Request) model.Principal {
	if p, ok := PrincipalFromContext(r.Context()); ok {
		return p
	}
	return model.Principal{Kind: model.PrincipalHuman}
}

func (s *Server) handleGetTaskContext(w http.ResponseWriter, r *http.Request) {
	view, err := s.tasks.TaskContext(r.Context(), r.PathValue("id"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleCompactTask(w http.ResponseWriter, r *http.Request) {
	var req CompactRequest
	if !readJSON(w, r, &req) {
		return
	}
	view, err := s.tasks.CompactTask(r.Context(), r.PathValue("id"), contextActor(r), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleSetTaskBrief(w http.ResponseWriter, r *http.Request) {
	var req BriefRequest
	if !readJSON(w, r, &req) {
		return
	}
	view, err := s.tasks.SetTaskBrief(r.Context(), r.PathValue("id"), contextActor(r), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleRolloverTask(w http.ResponseWriter, r *http.Request) {
	result, err := s.tasks.RolloverTask(r.Context(), r.PathValue("id"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// TaskContext is GET /api/tasks/{id}/context over the wire.
func (c *HTTPClient) TaskContext(ctx context.Context, id string) (TaskContextView, error) {
	var out TaskContextView
	err := c.do(ctx, http.MethodGet, "/api/tasks/"+url.PathEscape(id)+"/context", nil, &out)
	return out, err
}

// CompactTask is POST /api/tasks/{id}/compact over the wire.
func (c *HTTPClient) CompactTask(ctx context.Context, id string, req CompactRequest) (TaskContextView, error) {
	var out TaskContextView
	err := c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/compact", req, &out)
	return out, err
}

// SetTaskBrief is POST /api/tasks/{id}/brief over the wire.
func (c *HTTPClient) SetTaskBrief(ctx context.Context, id string, req BriefRequest) (TaskContextView, error) {
	var out TaskContextView
	err := c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/brief", req, &out)
	return out, err
}

// RolloverTask is POST /api/tasks/{id}/rollover over the wire.
func (c *HTTPClient) RolloverTask(ctx context.Context, id string) (model.RolloverResult, error) {
	var out model.RolloverResult
	err := c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/rollover", nil, &out)
	return out, err
}
