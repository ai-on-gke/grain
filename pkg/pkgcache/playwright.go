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

package pkgcache

// The Playwright half: serving both transparent requests for cdn.playwright.dev
// and playwright.azureedge.net (intercepted via DNS) and legacy /playwright/ paths.
//
// What is stored:
// - Browser and toolchain binaries (.zip, .tar.gz, .tgz) under /builds/...
//   (e.g., Chromium headless shell, WebKit, FFmpeg builds). These artifacts are
//   content-addressed / immutable builds published for specific browser revisions.
//
// What is forwarded fresh every time (never stored):
// - Non-archive or non-build paths.
// - Range requests (HTTP 206 Partial Content).

import (
	"net/http"
	"strings"
)

// playwrightPrefix is the legacy path prefix for playwright.
const playwrightPrefix = "/playwright/"

// DefaultPlaywrightURL is the upstream Playwright CDN base URL when none is configured.
const DefaultPlaywrightURL = "https://cdn.playwright.dev"

// DefaultPlaywrightHosts are the Playwright CDN hosts intercepted and served.
var DefaultPlaywrightHosts = []string{
	"cdn.playwright.dev",
	"playwright.azureedge.net",
	"playwright.download.prss.microsoft.com",
}

// servePlaywright answers one legacy request against this service's path /playwright/.
func (s *Server) servePlaywright(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.EscapedPath(), strings.TrimSuffix(playwrightPrefix, "/"))
	if strings.Contains(rest, "..") || strings.Contains(r.URL.Path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}
	upstream := strings.TrimSuffix(s.playwrightURL(), "/") + rest
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	cleanPath := r.URL.Path
	s.serve(w, r, object{kind: KindPlaywright, url: upstream, cacheable: playwrightCacheable(cleanPath)})
}

// serveTransparentPlaywright answers one transparent request against an intercepted Playwright CDN domain.
func (s *Server) serveTransparentPlaywright(w http.ResponseWriter, r *http.Request, host string) {
	if strings.Contains(r.URL.Path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}

	base := s.playwrightURL()
	if s.playwrightURLVal == "" && host != "" {
		scheme := "https"
		if s.fileScheme != "" {
			scheme = s.fileScheme
		} else if r.TLS == nil {
			scheme = "http"
		}
		base = scheme + "://" + host
	}

	upstream := strings.TrimSuffix(base, "/") + r.URL.RequestURI()
	cleanPath := r.URL.Path
	s.serve(w, r, object{kind: KindPlaywright, url: upstream, cacheable: playwrightCacheable(cleanPath)})
}

// playwrightCacheable reports whether a Playwright CDN path names an immutable archive
// artifact under /builds/...
func playwrightCacheable(rawPath string) bool {
	p := strings.ToLower(rawPath)
	if idx := strings.IndexByte(p, '?'); idx != -1 {
		p = p[:idx]
	}
	if strings.Contains(p, "/builds/") || strings.HasPrefix(p, "/builds/") {
		if strings.HasSuffix(p, ".zip") || strings.HasSuffix(p, ".tar.gz") || strings.HasSuffix(p, ".tgz") {
			return true
		}
	}
	return false
}

func (s *Server) playwrightURL() string {
	if s.playwrightURLVal != "" {
		return s.playwrightURLVal
	}
	return DefaultPlaywrightURL
}

func (s *Server) playwrightFileHosts() []string {
	if len(s.playwrightHosts) > 0 {
		return s.playwrightHosts
	}
	return DefaultPlaywrightHosts
}
