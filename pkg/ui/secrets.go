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
	"errors"
	"net/http"

	"github.com/ai-on-gke/grain/pkg/secrets"
)

// errSecretsUnavailable is what every secrets handler reports when
// Config.Secrets is nil -- this Server was not told where a secrets
// directory lives, which is the normal case for `grain demo`'s throwaway
// UI (no real secrets directory to name) but never for `grain daemon`'s
// own in-process UI, which always has its own -data-dir/secrets to point
// at (bwsalmon/agents#357, #363). Mapped to 404: there is no secrets
// resource here to act on, the same as any other name nothing exists
// behind.
var errSecretsUnavailable = errors.New(
	"secrets are not available: this UI has no local secrets directory to write to")

// secretsResponse is GET /api/secrets' whole body, and what every
// mutation below re-fetches and returns afterward, the same
// respond-with-the-current-shape convention respondWithTask already
// uses. Enabled is false, with an empty Secrets list, when this
// deployment's UI has no local secrets directory configured -- the
// frontend uses that to hide the pane entirely rather than show controls
// that can only ever 404.
type secretsResponse struct {
	Enabled bool          `json:"enabled"`
	Secrets []secretEntry `json:"secrets"`
}

// secretEntry is one secret as GET /api/secrets lists it: its name and
// keys, and who uses it, so Legacy capabilities can tell a capability
// server's own secret from one nothing claims.
type secretEntry struct {
	secrets.SecretInfo
	// CapabilityServers are the configured capability servers that use
	// this secret (capabilityServerSecretOwners), by capability_id.
	CapabilityServers []string `json:"capabilityServers,omitempty"`
	// Orphaned is set on a secret grain created for a capability server
	// (grainServerSecret) that no configured server uses any more -- one
	// left behind by a rename, or by a removal whose cleanup failed.
	Orphaned bool `json:"orphaned,omitempty"`
}

// handleListSecrets answers GET /api/secrets: the deployment's whole
// credential inventory.
func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.Secrets == nil {
		writeJSON(w, http.StatusOK, secretsResponse{Enabled: false, Secrets: []secretEntry{}})
		return
	}
	s.respondWithSecrets(w, r)
}

type setSecretRequest struct {
	Value string `json:"value"`
}

// handleSetSecret is the UI's only way to put a value into a secret --
// and, per this package's own doc comment on Config.Secrets, the only
// direction a value ever travels through this package at all.
func (s *Server) handleSetSecret(w http.ResponseWriter, r *http.Request) {
	store := s.tasks.Config.Secrets
	if store == nil {
		writeError(w, http.StatusNotFound, errSecretsUnavailable)
		return
	}
	var req setSecretRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Value == "" {
		writeError(w, http.StatusBadRequest, errors.New("value is required"))
		return
	}
	if err := store.Set(r.PathValue("secret"), r.PathValue("key"), []byte(req.Value)); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.respondWithSecrets(w, r)
}

func (s *Server) handleDeleteSecretKey(w http.ResponseWriter, r *http.Request) {
	store := s.tasks.Config.Secrets
	if store == nil {
		writeError(w, http.StatusNotFound, errSecretsUnavailable)
		return
	}
	if err := store.DeleteKey(r.PathValue("secret"), r.PathValue("key")); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.respondWithSecrets(w, r)
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	store := s.tasks.Config.Secrets
	if store == nil {
		writeError(w, http.StatusNotFound, errSecretsUnavailable)
		return
	}
	if err := store.DeleteSecret(r.PathValue("secret")); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.respondWithSecrets(w, r)
}

func (s *Server) respondWithSecrets(w http.ResponseWriter, r *http.Request) {
	list, err := s.tasks.Config.Secrets.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	owners, err := s.tasks.CapabilityServerSecretOwners(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, secretsResponse{Enabled: true, Secrets: secretEntries(list, owners)})
}

// secretEntries is list with each secret's owners from owners
// (capabilityServerSecretOwners) filled in.
func secretEntries(list []secrets.SecretInfo, owners map[string][]string) []secretEntry {
	out := make([]secretEntry, 0, len(list))
	for _, info := range list {
		e := secretEntry{SecretInfo: info, CapabilityServers: owners[info.Name]}
		e.Orphaned = len(e.CapabilityServers) == 0 && grainServerSecret(info.Name)
		out = append(out, e)
	}
	return out
}
