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
	if err := c.checkHostdSelector(ctx, normalized); err != nil {
		return err
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

// checkHostdSelector refuses a selector that no hostd in the registry
// can answer -- an unknown or revoked name, or labels no unrevoked
// hostd carries -- as the daemon's SelectHostd would at the task's
// first run, so whoever set it is told now rather than every run
// failing its setup and retrying. A registered hostd that is not
// connected is accepted: the task waits for it.
func (c *Client) checkHostdSelector(ctx context.Context, selector string) error {
	if selector == "" || c.Store == nil {
		return nil
	}
	name, labels, err := model.ParseHostdSelector(selector)
	if err != nil {
		return validationErrorf("%v", err)
	}
	if name != "" {
		if s, ok := c.Config.HostdRemote.(hostdSharedNamer); ok && s.SharedTokenName() == name {
			return nil
		}
		h, err := c.Store.GetHostd(ctx, name)
		if err != nil {
			return err
		}
		if h == nil {
			return validationErrorf("hostd %s is not in the registry (Settings -> Hostds)", name)
		}
		if h.Revoked() {
			return validationErrorf("hostd %s is revoked", name)
		}
		return nil
	}
	rows, err := c.Store.ListHostds(ctx)
	if err != nil {
		return err
	}
	for _, h := range rows {
		if !h.Revoked() && hostdLabelled(h.Labels, labels) {
			return nil
		}
	}
	return validationErrorf("no hostd in the registry is labelled %s (Settings -> Hostds)", selector)
}

// hostdLabelled is whether a hostd's registry labels carry every one
// of want's.
func hostdLabelled(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
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
