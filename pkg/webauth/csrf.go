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

package webauth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

var defaultCrossOriginProtection = http.NewCrossOriginProtection()

// CheckCrossOrigin refuses a cross-site browser request with an unsafe method
// using stdlib http.CrossOriginProtection. Safe methods (GET, HEAD, OPTIONS)
// and non-browser callers carrying neither Sec-Fetch-Site nor Origin pass.
func CheckCrossOrigin(r *http.Request) error {
	return defaultCrossOriginProtection.Check(r)
}

// VerifySameOriginForProxyAuth enforces CSRF protection on state-changing
// requests authenticated by IAP or another reverse-proxy identity header whose
// session cookie is not controlled by the backend and is not SameSite=Strict.
//
// Browser requests (any request carrying Sec-Fetch-Site, Origin, or Cookie)
// must carry Sec-Fetch-Site: same-origin (or none) or an Origin matching Host
// (or X-Forwarded-Host). Cookie-less programmatic requests carrying neither
// Sec-Fetch-Site nor Origin pass.
func VerifySameOriginForProxyAuth(r *http.Request) error {
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
		if OriginMatchesRequestHost(u.Host, r) {
			return nil
		}
		return fmt.Errorf("Origin %q does not match request host %q", origin, r.Host)
	}

	// Neither Sec-Fetch-Site nor Origin is present. If the request carries any
	// Cookie (such as IAP's session cookie), treat it as a browser request and
	// refuse it; cookie-less requests are programmatic clients (e.g. CLI with
	// Proxy-Authorization or OIDC bearer token).
	if strings.TrimSpace(r.Header.Get("Cookie")) != "" {
		return errors.New("state-changing cookie-authenticated request must carry Sec-Fetch-Site: same-origin or a matching Origin")
	}
	return nil
}

// OriginMatchesRequestHost reports whether originHost matches r.Host or any
// comma-separated entry in X-Forwarded-Host (case-insensitively). Sibling
// subdomains (e.g. bob.example.com vs alice.example.com) never match.
func OriginMatchesRequestHost(originHost string, r *http.Request) bool {
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
