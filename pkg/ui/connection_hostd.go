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

// Where host work runs is the placement of what does it: a model
// connection names the grain hostd its agent CLI runs on
// (model.AgentBackend.Hostd), and a run: hostd capability server the
// hostd its process runs on (protocol.ServerConfig.Hostd). Nothing a
// task is filed with -- the New Task form, a template, a schedule, a
// suite, a repository's settings -- chooses one any more, and a request
// that still names one is refused with errTaskHostd rather than
// ignored.

import (
	"context"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TaskHostdRefusal is what a request that still sets a hostd on a
// task, template, schedule or repository is told.
const TaskHostdRefusal = "hostd is set on the model connection or capability server, not the task " +
	"(Settings -> Models or Settings -> Capabilities)"

// errTaskHostd is TaskHostdRefusal as a validation error, prefixed with
// the field that carried it.
func errTaskHostd(field string) error {
	return validationErrorf("%s: %s", field, TaskHostdRefusal)
}

// refuseTaskHostd is errTaskHostd for field when value names a hostd.
func refuseTaskHostd(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return errTaskHostd(field)
}

// normalizeConnectionHostd normalizes b's hostd placement and fallback
// list, checking each selector against the registry when it changed
// from what stored holds (checkHostdSelector), so a hostd revoked after
// the connection was saved makes it unavailable rather than failing
// every unrelated save.
func (c *Client) normalizeConnectionHostd(ctx context.Context, stored []model.AgentBackend, b *model.AgentBackend) error {
	var prev model.AgentBackend
	for _, s := range stored {
		if s.ID == b.ID {
			prev = s
		}
	}
	norm := func(field, selector, before string) (string, error) {
		n, err := model.NormalizeHostdSelector(selector)
		if err != nil {
			return "", validationErrorf("%s: %v", field, err)
		}
		if n != before {
			if err := c.checkHostdSelector(ctx, n); err != nil {
				return "", validationErrorf("%s: %v", field, err)
			}
		}
		return n, nil
	}
	h, err := norm("hostd", b.Hostd, prev.Hostd)
	if err != nil {
		return err
	}
	b.Hostd = h
	var fallback []string
	for i, f := range b.HostdFallback {
		before := ""
		if i < len(prev.HostdFallback) {
			before = prev.HostdFallback[i]
		}
		n, err := norm("hostdFallback", f, before)
		if err != nil {
			return err
		}
		if n != "" && n != h {
			fallback = append(fallback, n)
		}
	}
	if len(fallback) > 0 && h == "" {
		return validationErrorf("hostdFallback lists where to go when hostd is not connected; set hostd first")
	}
	b.HostdFallback = fallback
	return nil
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
		hv, ok := have[k]
		if !ok || hv != v {
			return false
		}
	}
	return true
}

// handleSetTaskHostd is what is left of PUT /api/tasks/{id}/hostd: a
// refusal that says where a hostd is set now.
func (s *Server) handleSetTaskHostd(w http.ResponseWriter, r *http.Request) {
	writeClientError(w, errTaskHostd("hostd"))
}
