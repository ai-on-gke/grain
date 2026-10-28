package ui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The deployment's named GCP keys, gcp-key:<name> (docs/team-gcp-keys.md).
// They were a team's until single-tenant reset stage 4e; they are now the
// deployment's, so saving, reading one and deleting are a system admin's,
// as every other deployment credential is, and the minter a key names is
// the system admin's choice. Listing them answers any member: a row names a
// project, accounts and a minter secret's name, never key material, and it
// is what a person granting gcp-key:<name> needs to see.

// SetGCPKeyRequest carries parameters to create or update a GCP key.
type SetGCPKeyRequest struct {
	Name                string `json:"name"`
	ProjectID           string `json:"projectId"`
	ServiceAccountEmail string `json:"serviceAccountEmail"`
	ServiceAccountCount int    `json:"serviceAccountCount,omitempty"`
	MaxKeyAge           string `json:"maxKeyAge,omitempty"`
	MaxKeyAgeHours      int    `json:"maxKeyAgeHours,omitempty"`
	MinterCredential    string `json:"minterCredential,omitempty"`
	// Description, when nil, keeps the key's existing description; an
	// empty string clears it.
	Description *string `json:"description,omitempty"`
	User        string  `json:"user,omitempty"`
}

// ListGCPKeys returns the deployment's GCP keys.
func (c *Client) ListGCPKeys(ctx context.Context) ([]model.GCPKey, error) {
	return c.Store.ListGCPKeys(ctx)
}

// GetGCPKeyWithUser returns the deployment's GCP key called name.
func (c *Client) GetGCPKeyWithUser(ctx context.Context, name, user string) (model.GCPKey, error) {
	key, err := c.Store.GetGCPKey(ctx, name)
	if err != nil {
		return model.GCPKey{}, err
	}
	if key == nil {
		return model.GCPKey{}, validationErrorf("no GCP key %q is configured", name)
	}
	return *key, nil
}

// SetGCPKeyWithUser creates or updates one of the deployment's GCP keys.
func (c *Client) SetGCPKeyWithUser(ctx context.Context, req SetGCPKeyRequest, user string) (model.GCPKey, error) {
	if err := c.checkSettingsWritableUnrehearsed(ctx, "gcp_key"); err != nil {
		return model.GCPKey{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return model.GCPKey{}, validationErrorf("name cannot be empty")
	}
	var maxAge time.Duration
	if req.MaxKeyAge != "" {
		d, err := time.ParseDuration(req.MaxKeyAge)
		if err != nil {
			return model.GCPKey{}, validationErrorf("invalid maxKeyAge: %v", err)
		}
		maxAge = d
	} else if req.MaxKeyAgeHours > 0 {
		maxAge = time.Duration(req.MaxKeyAgeHours) * time.Hour
	}

	key := model.GCPKey{
		Name:                name,
		ProjectID:           strings.TrimSpace(req.ProjectID),
		ServiceAccountEmail: strings.TrimSpace(req.ServiceAccountEmail),
		ServiceAccountCount: req.ServiceAccountCount,
		MaxKeyAge:           maxAge,
		MinterCredential:    strings.TrimSpace(req.MinterCredential),
	}
	if req.Description != nil {
		key.Description = strings.TrimSpace(*req.Description)
	}
	if err := key.Validate(); err != nil {
		return model.GCPKey{}, validationErrorf("%s", err)
	}
	existing, err := c.Store.GetGCPKey(ctx, name)
	if err != nil {
		return model.GCPKey{}, err
	}
	if req.Description == nil && existing != nil {
		// PutGCPKey replaces the whole row, so a save that does not
		// mention the description keeps the one it has.
		key.Description = existing.Description
	}
	if key.MinterCredential == "" {
		// An existing key keeps the minter it has; a new one gets
		// gcp-key-minter-<name>.
		if existing != nil {
			key.MinterCredential = existing.MinterCredential
		} else {
			key.MinterCredential = model.GCPKeyMinterCredential(name)
		}
	}
	if err := c.Store.PutGCPKey(ctx, key); err != nil {
		return model.GCPKey{}, err
	}
	saved, err := c.Store.GetGCPKey(ctx, name)
	if err != nil || saved == nil {
		return key, err
	}
	return *saved, nil
}

// DeleteGCPKeyWithUser removes one of the deployment's GCP keys.
func (c *Client) DeleteGCPKeyWithUser(ctx context.Context, name, user string) error {
	if err := c.checkSettingsWritableUnrehearsed(ctx, "gcp_key"); err != nil {
		return err
	}
	return c.Store.DeleteGCPKey(ctx, name)
}

func (s *Server) handleListGCPKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.tasks.ListGCPKeys(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	if keys == nil {
		keys = []model.GCPKey{}
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) handleGetGCPKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.tasks.GetGCPKeyWithUser(r.Context(), r.PathValue("name"), requestUser(r))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, key)
}

func (s *Server) handleSetGCPKey(w http.ResponseWriter, r *http.Request) {
	var req SetGCPKeyRequest
	if !readJSON(w, r, &req) {
		return
	}
	if name := r.PathValue("name"); name != "" {
		req.Name = name
	}
	key, err := s.tasks.SetGCPKeyWithUser(r.Context(), req, actingUser(r, req.User))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, key)
}

func (s *Server) handleDeleteGCPKey(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.DeleteGCPKeyWithUser(r.Context(), r.PathValue("name"), requestUser(r)); err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
