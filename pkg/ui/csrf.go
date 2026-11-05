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
	"fmt"
	"net/http"

	"github.com/ai-on-gke/grain/pkg/webauth"
)

// checkCrossOrigin answers 403 and returns false for a cross-site
// browser request with an unsafe method (#2371).
func checkCrossOrigin(w http.ResponseWriter, r *http.Request) bool {
	if err := webauth.CheckCrossOrigin(r); err != nil {
		writeError(w, http.StatusForbidden, fmt.Errorf("cross-origin %s %s refused: %w", r.Method, r.URL.Path, err))
		return false
	}
	return true
}

// checkVerifiedIdentityCSRF enforces CSRF protection on state-changing
// requests (any method other than GET or HEAD) authenticated by IAP or
// another IdentityVerifier, whose browser session cookie is not controlled by
// grain and is not SameSite=Strict.
//
// Browser requests (any request carrying Sec-Fetch-Site, Origin, or Cookie)
// must carry Sec-Fetch-Site: same-origin (or none) or an Origin matching Host
// (or X-Forwarded-Host). Non-browser programmatic callers carrying none of
// those headers pass.
func checkVerifiedIdentityCSRF(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if err := webauth.VerifySameOriginForProxyAuth(r); err != nil {
		writeError(w, http.StatusForbidden, fmt.Errorf("CSRF check refused %s %s: %w", r.Method, r.URL.Path, err))
		return false
	}
	return true
}
