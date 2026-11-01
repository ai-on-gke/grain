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
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// crossOriginProtection refuses a state-changing request a browser sent
// on behalf of some other site (#2371). Every POST route here decodes
// its body as JSON whatever Content-Type it claims, so a plain <form> or
// a no-preflight fetch() from any page an operator's browser loads could
// otherwise drive the daemon as that operator.
//
// http.CrossOriginProtection lets GET, HEAD and OPTIONS through, and any
// request a browser marks Sec-Fetch-Site: same-origin (or none, for one
// the user typed). Without that header it falls back to comparing Origin
// with Host. A request carrying neither -- the CLI, pkg/mcp's client, a
// sandbox's curl, a webhook -- did not come from a browser and is not
// what CSRF is about, so it passes too.
var crossOriginProtection = http.NewCrossOriginProtection()

// checkCrossOrigin answers 403 and returns false for a cross-site
// browser request with an unsafe method.
func checkCrossOrigin(w http.ResponseWriter, r *http.Request) bool {
	if err := crossOriginProtection.Check(r); err != nil {
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
	if err := verifySameOriginForProxyAuth(r); err != nil {
		writeError(w, http.StatusForbidden, fmt.Errorf("CSRF check refused %s %s: %w", r.Method, r.URL.Path, err))
		return false
	}
	return true
}

func verifySameOriginForProxyAuth(r *http.Request) error {
	switch sfs := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")); sfs {
	case "":
		// Fall through to Origin check.
	case "same-origin", "none":
		return nil
	default:
		return fmt.Errorf("Sec-Fetch-Site %q is not same-origin", sfs)
	}

	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return fmt.Errorf("invalid Origin %q", origin)
		}
		if originMatchesRequestHost(u.Host, r) {
			return nil
		}
		return fmt.Errorf("Origin %q does not match request host %q", origin, r.Host)
	}

	// Neither Sec-Fetch-Site nor Origin is present. If the request carries any
	// Cookie (such as IAP's session cookie), treat it as a browser request and
	// refuse it; cookie-less requests are programmatic clients (e.g. CLI with
	// Proxy-Authorization, which IAP strips before forwarding).
	if strings.TrimSpace(r.Header.Get("Cookie")) != "" {
		return errors.New("state-changing cookie-authenticated request must carry Sec-Fetch-Site: same-origin or a matching Origin")
	}
	return nil
}

func originMatchesRequestHost(originHost string, r *http.Request) bool {
	if r.Host != "" && strings.EqualFold(originHost, r.Host) {
		return true
	}
	for _, xfh := range strings.Split(r.Header.Get("X-Forwarded-Host"), ",") {
		if xfh = strings.TrimSpace(xfh); xfh != "" && strings.EqualFold(originHost, xfh) {
			return true
		}
	}
	return false
}
