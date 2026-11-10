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
	"net/http"

	"github.com/ai-on-gke/grain/pkg/redact"
)

// SecretsRedactRequest is POST /api/secrets/redact's body: lines a child
// process of the daemon (a per-run grain mcpserver serving
// read_grain_log) is about to hand a run, to have every value in this
// deployment's secret store cut out of them first.
type SecretsRedactRequest struct {
	Lines []string `json:"lines"`
}

// SecretsRedactResponse is the same lines, redacted, and how many
// redactions that made.
type SecretsRedactResponse struct {
	Lines    []string `json:"lines"`
	Redacted int      `json:"redacted"`
}

// maxRedactBody bounds a redact request. read_grain_log returns at most a
// few thousand lines; this is generous for that and still bounded.
const maxRedactBody = 8 << 20

// handleRedactSecrets answers POST /api/secrets/redact.
//
// It exists so that a child process does not have to open the secrets
// key itself to know what to redact (design/design/secrets-key-
// protection.design.md, Phase 0): the daemon already holds the store,
// with its key loaded once, and does the redaction here. The values never
// leave this process -- the response is the caller's own lines with
// "[redacted: secret <name>/<key>]" where a value stood -- which keeps
// the store write-only over the API, as GET /api/secrets is. The route is
// "internal", so no agent scope and no limited role reaches it.
func (s *Server) handleRedactSecrets(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.Secrets == nil {
		writeError(w, http.StatusNotFound, errSecretsUnavailable)
		return
	}
	var req SecretsRedactRequest
	if !readJSONLimit(w, r, &req, maxRedactBody) {
		return
	}
	vals, err := secretRedactionValues(r.Context(), s)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	lines, n := redact.New(vals).Redact(req.Lines)
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, SecretsRedactResponse{Lines: lines, Redacted: n})
}

// secretRedactionValues is every value in the store, labelled the way
// pkg/daemon's KnownSecretValuesFromStore labels them. An unreadable
// store is an error, never an empty list: "could not read" served as
// "nothing to redact" is how a credential reaches a run.
func secretRedactionValues(ctx context.Context, s *Server) ([]redact.KnownValue, error) {
	store := s.tasks.Config.Secrets
	if err := store.Check(); err != nil {
		return nil, err
	}
	vals, err := store.KnownValues(ctx)
	if err != nil {
		return nil, err
	}
	known := make([]redact.KnownValue, 0, len(vals))
	for _, v := range vals {
		known = append(known, redact.KnownValue{Label: "secret " + v.SecretName + "/" + v.Key, Value: v.Value})
	}
	return known, nil
}
