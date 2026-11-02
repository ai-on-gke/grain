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
	"net/http"

	"github.com/ai-on-gke/grain/pkg/apierror"
)

// hostdConnectRoute reports whether r is a grain hostd dialing in to
// /api/hostd/connect. It authenticates in its own protocol -- it is let
// in only for the hostd token it presents (hostd.Remote.ServeHTTP),
// whatever else the request carries -- so it does not ask who the
// caller is (requiresAccess), and a single-tenant daemon serves it
// without its access code (singleTenantOpenRoute) and does not count
// the hostd or IAP token it sends as a bearer as a guess at it.
func hostdConnectRoute(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return r.URL.Path == "/api/hostd/connect"
}

// handleHostdConnect hands a grain hostd's upgrade of /api/hostd/connect
// to Config.HostdRemote, which authenticates it with the hostd token and
// serves the session host CLIs and run: host capability servers start
// over.
func (s *Server) handleHostdConnect(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil || s.tasks.Config.HostdRemote == nil {
		writeError(w, http.StatusServiceUnavailable, apierror.Public("this daemon takes no grain hostd dialing in: start it with -hostd-token-file"))
		return
	}
	s.tasks.Config.HostdRemote.ServeHTTP(w, r)
}
