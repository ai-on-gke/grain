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

// A task's hostd pin (model.SetTaskHostd): which grain hostd its host
// work goes to -- a hostd's name, or label=value[,label=value] for any
// hostd carrying them all -- as the New Task form and PUT
// /api/tasks/{id}/hostd set it. Empty is the default hostd.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ai-on-gke/grain/pkg/model"
)

// setHostdRequest is PUT /api/tasks/{id}/hostd's body.
type setHostdRequest struct {
	// Hostd is a hostd name, label=value[,label=value...], or "" for the
	// default hostd.
	Hostd string `json:"hostd"`
}

// SetTaskHostd pins task id's host work to selector ("" for the default
// hostd) from its next run on, and says so on the task.
func (c *Client) SetTaskHostd(ctx context.Context, id, selector string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	normalized, err := model.NormalizeHostdSelector(selector)
	if err != nil {
		return validationErrorf("%v", err)
	}
	prev, err := c.Store.TaskHostd(ctx, id)
	if err != nil || prev == normalized {
		return err
	}
	if err := c.Store.SetTaskHostd(ctx, id, normalized); err != nil {
		return err
	}
	body := "Host work for this task goes to the default hostd from its next run on."
	if normalized != "" {
		body = fmt.Sprintf("Host work for this task goes to hostd %s from its next run on; it waits for that hostd rather than running anywhere else.", normalized)
	}
	_, err = c.Store.AddComment(ctx, model.Comment{
		TaskID: id, Author: model.Attribution{Actor: c.requestActor(ctx).Actor}, CreatedAt: c.now(), Body: body,
	})
	return err
}

func (s *Server) handleSetTaskHostd(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req setHostdRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.tasks.SetTaskHostd(r.Context(), id, req.Hostd); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}
