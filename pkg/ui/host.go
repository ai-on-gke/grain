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
	"strconv"

	"github.com/ai-on-gke/grain/pkg/apierror"
)

// errRebootUnavailable is what handleRebootHost reports when
// Config.Reboot is nil -- see Config.Reboot's own doc comment.
var errRebootUnavailable = errors.New(
	"reboot is not available: this deployment has no reboot command configured")

// The same sentence for the two actions beside it (grain/task-40). Each
// is its own gate because each is its own host-side command: a
// deployment can be able to reboot and not to power off, or to restart
// its own service and neither of the other two.
var errShutdownUnavailable = errors.New(
	"shutdown is not available: this deployment has no shutdown command configured")

var errRestartUnavailable = errors.New(
	"restart is not available: this deployment has no restart command configured")

// handleRebootHost is the UI's "reboot host" button (bwsalmon/agents#395):
// a human operator's way to reboot the machine grain itself is running
// on without reaching for a separate SSH session -- the same escape
// hatch v1's mcp_server.py already gave a task via `reboot_controller`,
// but triggered directly by the operator here rather than granted to a
// task as a capability.
//
// A successful reboot cuts this same process along with everything else
// on the machine, so a caller seeing the connection drop instead of a
// 200 response is the expected outcome, not a sign the call failed.
func (s *Server) handleRebootHost(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.Reboot == nil {
		writeError(w, http.StatusNotFound, errRebootUnavailable)
		return
	}
	if err := s.tasks.Config.Reboot(r.Context()); err != nil {
		// Only a system administrator gets this far, and why the
		// command failed ("sudo: a password is required") is what they
		// need to fix it.
		writeError(w, http.StatusInternalServerError, apierror.Expose(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleShutdownHost is the button beside it (grain/task-40): power the
// machine off and leave it off. Everything handleRebootHost's own
// comment says about the response applies here and more so -- the
// machine is going away and is not coming back on its own, so a dropped
// connection is again the expected outcome rather than a failure.
func (s *Server) handleShutdownHost(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.Shutdown == nil {
		writeError(w, http.StatusNotFound, errShutdownUnavailable)
		return
	}
	if err := s.tasks.Config.Shutdown(r.Context()); err != nil {
		// Only a system administrator gets this far, and why the
		// command failed ("sudo: a password is required") is what they
		// need to fix it.
		writeError(w, http.StatusInternalServerError, apierror.Expose(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRestartDaemon is the third and smallest of them: restart the
// grain service -- the container, under a container deployment --
// without touching the machine it runs on (grain/task-40). Sandboxes
// running out on that host, and everything else it is doing, survive
// this; a task in flight does not, since the process driving it is the
// one being restarted.
//
// Same response caveat once more: the daemon this request is being
// served by is the daemon going down, so the connection commonly drops
// before the 200 lands.
func (s *Server) handleRestartDaemon(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.Restart == nil {
		writeError(w, http.StatusNotFound, errRestartUnavailable)
		return
	}
	if err := s.tasks.Config.Restart(r.Context()); err != nil {
		// Only a system administrator gets this far, and why the
		// command failed ("sudo: a password is required") is what they
		// need to fix it.
		writeError(w, http.StatusInternalServerError, apierror.Expose(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// maxTopLines caps what a caller can ask for in one request regardless
// of ?lines=, the same guard maxLogLines gives the Logs pane beside it.
// There is no defaultTopLines to go with it: an absent ?lines= is passed
// on as 0, which Config.HostTop reads as "your own default"
// (hosttop.DefaultLines in a real deployment) rather than a second
// number spelled here that could drift from it.
const maxTopLines = 500

// hostTopResponse is GET /api/host/top's whole body. Enabled is false,
// with no lines, when this deployment's UI has no Config.HostTop -- the
// same nil-means-unavailable convention logSourcesResponse and
// sandboxHealthResponse already give the other panels of the System
// overlay this one is a tab of.
type hostTopResponse struct {
	Enabled bool     `json:"enabled"`
	Lines   []string `json:"lines,omitempty"`
}

// handleGetHostTop is the System overlay's Top tab: what is actually
// running on the machine this daemon runs on, right now. GET
// /api/sandboxes' own host section already says how loaded that machine
// is; this says by what, which is the question an operator asks next and
// which no aggregate reading can answer (pkg/hosttop's doc comment).
// Raw process lists name commands, paths and arguments, so only a
// system administrator may read them.
func (s *Server) handleGetHostTop(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.HostTop == nil {
		writeJSON(w, http.StatusOK, hostTopResponse{Enabled: false})
		return
	}
	lines := 0
	if raw := r.URL.Query().Get("lines"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("lines must be a positive integer"))
			return
		}
		lines = parsed
	}
	if lines > maxTopLines {
		lines = maxTopLines
	}
	out, err := s.tasks.Config.HostTop(r.Context(), lines)
	if err != nil {
		// This route's own answer is raw process output from the host,
		// so why top failed to produce it discloses nothing more.
		writeError(w, http.StatusInternalServerError, apierror.Expose(err))
		return
	}
	writeJSON(w, http.StatusOK, hostTopResponse{Enabled: true, Lines: out})
}
