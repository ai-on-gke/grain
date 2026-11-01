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

	"github.com/ai-on-gke/grain/pkg/apierror"
)

// SandboxProbeRequest is the body of POST /api/sandbox-probe: build one
// throwaway sandbox, run Argv in it over the deployment's guest-access
// channel, and tear it down. Its own type rather than orchestrator's, for
// the reason SandboxRecreation gives.
type SandboxProbeRequest struct {
	// ID names the probe; the sandbox is called "probe-<id>" and its pod
	// carries that in its name.
	ID string `json:"id"`
	// Access is the channel the caller expects ("dialout" or "kubectl");
	// empty accepts whichever the deployment uses.
	Access string   `json:"access,omitempty"`
	Argv   []string `json:"argv"`
}

// SandboxProbeResult is what POST /api/sandbox-probe ran and what came
// back. A non-zero ExitCode is the command's own; a probe that could not
// run anything is an error response instead.
type SandboxProbeResult struct {
	Sandbox  string `json:"sandbox"`
	Pod      string `json:"pod"`
	Access   string `json:"access"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

// SandboxProber is implemented by pkg/daemon's adapter over
// orchestrator.ProbeSandbox and the daemon's own sandbox backend.
type SandboxProber interface {
	ProbeSandbox(ctx context.Context, req SandboxProbeRequest) (SandboxProbeResult, error)
}

var errSandboxProbeUnavailable = errors.New(
	"sandbox probes are not available: this deployment's UI is not wired to the daemon that owns its sandboxes")

// handleSandboxProbe answers POST /api/sandbox-probe -- what `grain
// sandbox probe` calls. It holds the request open for as long as the
// sandbox takes to boot; a caller that gives up cancels the context,
// and the backend deletes whatever it had built.
func (s *Server) handleSandboxProbe(w http.ResponseWriter, r *http.Request) {
	prober := s.tasks.Config.SandboxProbe
	if prober == nil {
		writeError(w, http.StatusNotFound, errSandboxProbeUnavailable)
		return
	}
	var req SandboxProbeRequest
	if !readJSON(w, r, &req) {
		return
	}
	result, err := prober.ProbeSandbox(r.Context(), req)
	if err != nil {
		writeClientError(w, apierror.Expose(err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ProbeSandbox is POST /api/sandbox-probe.
func (c *HTTPClient) ProbeSandbox(ctx context.Context, req SandboxProbeRequest) (SandboxProbeResult, error) {
	var out SandboxProbeResult
	if err := c.do(ctx, http.MethodPost, "/api/sandbox-probe", req, &out); err != nil {
		return SandboxProbeResult{}, err
	}
	return out, nil
}
