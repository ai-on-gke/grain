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
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Settings → Capabilities → API scope sets (docs/agent-api.md, "Scope
// sets"): every set in effect, built-in or declared in the config
// repository, with what it resolves to. A write goes to the
// api_scope_set table, which the state sync exports to
// tables/api_scope_set.json like every other setting, and is refused
// with the patch to commit instead when the config repository is
// managed externally (checkSettingsWritable).

// APIScopeSetsFile is where the config repository carries the sets.
const APIScopeSetsFile = "tables/api_scope_set.json"

// APIScopeSetRow is one set as Settings lists it.
type APIScopeSetRow struct {
	model.ResolvedAPIScopeSet
	// Source is "builtin" or "config".
	Source string `json:"source"`
	// Path is the config repository file a declared set lives in.
	Path string `json:"path,omitempty"`
}

// APIScopeSetsResponse is GET /api/api-scope-sets.
type APIScopeSetsResponse struct {
	Sets []APIScopeSetRow `json:"sets"`
	// Error is why the deployment's declared sets are not in effect --
	// they did not resolve when last loaded -- so the ones listed are
	// the previous version.
	Error string `json:"error,omitempty"`
	Path  string `json:"path"`
}

// PutAPIScopeSetRequest is PUT /api/api-scope-sets.
type PutAPIScopeSetRequest struct {
	Set model.APIScopeSet `json:"set"`
}

// DeleteAPIScopeSetRequest is POST /api/api-scope-sets/delete.
type DeleteAPIScopeSetRequest struct {
	Name string `json:"name"`
}

// APIScopeSets lists the sets in effect.
func (c *Client) APIScopeSets(ctx context.Context) APIScopeSetsResponse {
	out := APIScopeSetsResponse{Path: APIScopeSetsFile, Sets: []APIScopeSetRow{}}
	for _, r := range model.APIScopeSetsInEffect() {
		row := APIScopeSetRow{ResolvedAPIScopeSet: r, Source: "builtin"}
		if !r.Builtin {
			row.Source, row.Path = "config", APIScopeSetsFile
		}
		out.Sets = append(out.Sets, row)
	}
	if err := model.APIScopeSetsError(); err != nil {
		out.Error = err.Error()
	}
	return out
}

// PutAPIScopeSet creates or replaces one declared set and puts the
// result in effect at once.
func (c *Client) PutAPIScopeSet(ctx context.Context, set model.APIScopeSet) error {
	if c.Store == nil {
		return errors.New("this server has no store to declare a scope set in")
	}
	set.Name = strings.TrimSpace(set.Name)
	if err := c.checkSettingsWritable(ctx, "api_scope_set"); err != nil {
		return err
	}
	if err := c.Store.PutAPIScopeSet(ctx, set); err != nil {
		return &ValidationError{err: err}
	}
	return c.Store.LoadAPIScopeSets(ctx)
}

// DeleteAPIScopeSet removes one declared set.
func (c *Client) DeleteAPIScopeSet(ctx context.Context, name string) error {
	if c.Store == nil {
		return errors.New("this server has no store to remove a scope set from")
	}
	if strings.HasPrefix(name, model.APIScopeSetBuiltinPrefix) {
		return errors.New(name + " is one of grain's own scope sets and cannot be removed")
	}
	if err := c.checkSettingsWritable(ctx, "api_scope_set"); err != nil {
		return err
	}
	if err := c.Store.DeleteAPIScopeSet(ctx, name); err != nil {
		return &ValidationError{err: err}
	}
	return c.Store.LoadAPIScopeSets(ctx)
}

func (s *Server) handleListAPIScopeSets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.tasks.APIScopeSets(r.Context()))
}

func (s *Server) handlePutAPIScopeSet(w http.ResponseWriter, r *http.Request) {
	var req PutAPIScopeSetRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.tasks.PutAPIScopeSet(r.Context(), req.Set); err != nil {
		writeClientError(w, err)
		return
	}
	s.handleListAPIScopeSets(w, r)
}

func (s *Server) handleDeleteAPIScopeSet(w http.ResponseWriter, r *http.Request) {
	var req DeleteAPIScopeSetRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.tasks.DeleteAPIScopeSet(r.Context(), strings.TrimSpace(req.Name)); err != nil {
		writeClientError(w, err)
		return
	}
	s.handleListAPIScopeSets(w, r)
}
