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
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// The deployment's provider connections (model.AgentBackend), one at a
// time: what Settings -> Models & connections saves through
// /api/deployment/agent-backends/{id}. PUT /api/settings still accepts the
// whole list.

// PutTeamAgentBackend creates or replaces one of the deployment's
// connections. id names the deployment (the default team); credentials
// may be reused (CredentialBackendID) only from another saved connection.
func (c *Client) PutTeamAgentBackend(ctx context.Context, id, backendID string, backend model.AgentBackend, user string) (model.AgentBackend, error) {
	if err := c.checkBootstrapWritable(ctx, "grain_config"); err != nil {
		return model.AgentBackend{}, err
	}
	if _, err := c.existingTeam(ctx, id); err != nil {
		return model.AgentBackend{}, err
	}
	backendID = strings.TrimSpace(backendID)
	if backendID == "" || strings.ContainsAny(backendID, " /\\") {
		return model.AgentBackend{}, validationErrorf("invalid connection id %q", backendID)
	}
	backend.ID = backendID
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return model.AgentBackend{}, err
	}
	var current []model.AgentBackend
	if cfg != nil {
		current = cfg.AgentBackends
	}
	next := slices.Clone(current)
	replaced := false
	for i, existing := range next {
		if existing.ID != backendID {
			continue
		}
		next[i] = backend
		replaced = true
	}
	if !replaced {
		next = append(next, backend)
	}
	if source := strings.TrimSpace(backend.CredentialBackendID); source != "" {
		ok := false
		for _, candidate := range next {
			if candidate.ID == source {
				ok = true
			}
		}
		if !ok {
			return model.AgentBackend{}, validationErrorf("credentialBackendId must name another of this deployment's connections")
		}
	}
	planned, err := c.plannedConfig(ctx, UpdateSettingsRequest{AgentBackends: &next})
	if err != nil {
		return model.AgentBackend{}, err
	}
	if err := c.Store.PutConfig(ctx, planned); err != nil {
		return model.AgentBackend{}, err
	}
	c.auditCLIPathChanges(ctx, current, planned.AgentBackends, user)
	c.liftEditedConnectionPauses(current, planned.AgentBackends)
	c.kickModelDiscovery()
	for _, saved := range planned.AgentBackends {
		if saved.ID == backendID {
			return saved, nil
		}
	}
	return backend, nil
}

// DeleteTeamAgentBackend removes one of the deployment's connections, and the
// credential stored for it. A connection another one still borrows its
// credential from is refused, as the settings page refuses it.
func (c *Client) DeleteTeamAgentBackend(ctx context.Context, id, backendID, user string) error {
	if err := c.checkBootstrapWritable(ctx, "grain_config"); err != nil {
		return err
	}
	if _, err := c.existingTeam(ctx, id); err != nil {
		return err
	}
	backendID = strings.TrimSpace(backendID)
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return err
	}
	if cfg == nil {
		return &NotFoundError{ID: backendID, message: "no connection " + backendID}
	}
	found := false
	var next []model.AgentBackend
	for _, existing := range cfg.AgentBackends {
		if existing.ID != backendID {
			if existing.CredentialBackendID == backendID {
				return validationErrorf("connection %q reuses the credentials of %q; remove it first", existing.ID, backendID)
			}
			next = append(next, existing)
			continue
		}
		found = true
	}
	if !found {
		return &NotFoundError{ID: backendID, message: "no connection " + backendID}
	}
	updated := *cfg
	updated.AgentBackends = next
	if err := c.Store.PutConfig(ctx, updated); err != nil {
		return err
	}
	c.liftConnectionPauses(backendID)
	if store := c.Config.Secrets; store != nil {
		if err := store.DeleteSecret(secrets.AgentBackendCredentialSecret(backendID)); err != nil && !errors.Is(err, secrets.ErrNoSecret) {
			return err
		}
	}
	return nil
}

// authorizeAgentBackendChange is whether the credential of connection
// backendID may be set or cleared: by every caller serveHTTP let in, the
// deployment's own actor, once there is a store to keep the connection
// in.
func (c *Client) authorizeAgentBackendChange(_ context.Context, _, _ string) error {
	if c.Store == nil {
		return forbiddenErrorf("no store configured")
	}
	return nil
}

// requireAgentBackendAdmin is authorizeAgentBackendChange for a handler.
func (s *Server) requireAgentBackendAdmin(w http.ResponseWriter, r *http.Request) bool {
	if err := s.tasks.authorizeAgentBackendChange(r.Context(), strings.TrimSpace(r.PathValue("id")), requestUser(r)); err != nil {
		writeClientError(w, err)
		return false
	}
	return true
}
