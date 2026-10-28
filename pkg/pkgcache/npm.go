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

// The npm half: serving both transparent requests for registry.npmjs.org
// (intercepted via DNS) and legacy /npm/ paths.
//
// Everything is forwarded to the registry, and what is stored is the
// tarballs. A published version's .tgz is immutable -- npm has forbidden
// republishing a version since 2014 -- and it is essentially all of the
// bytes. A packument is the half that changes, and is never stored.
//
// Integrity is untouched: every tarball npm fetches is checked against
// the integrity (sha512) in the packument or the lockfile.

import (
	"net/http"
	"strings"
)

// npmPrefix is the legacy path prefix for npm.
const npmPrefix = "/npm/"

// DefaultNPMRegistry is the registry proxied when a deployment names no
// other -- the real one.
const DefaultNPMRegistry = "https://registry.npmjs.org"

// serveNPM answers one legacy request against this service's npm registry path /npm/.
func (s *Server) serveNPM(w http.ResponseWriter, r *http.Request) {
	// EscapedPath rather than Path: a scoped package arrives as
	// "@scope%2fname", and that is what upstream must be asked for.
	rest := strings.TrimPrefix(r.URL.EscapedPath(), strings.TrimSuffix(npmPrefix, "/"))
	if strings.Contains(rest, "..") || strings.Contains(r.URL.Path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}
	upstream := strings.TrimSuffix(s.npmRegistry(), "/") + rest
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	s.serve(w, r, object{kind: KindNPM, url: upstream, cacheable: npmCacheable(rest)})
}

// serveTransparentNPM answers one transparent request against an intercepted NPM registry domain.
func (s *Server) serveTransparentNPM(w http.ResponseWriter, r *http.Request, host ...string) {
	rest := r.URL.EscapedPath()
	if strings.Contains(rest, "..") || strings.Contains(r.URL.Path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}
	registry := s.npmRegistry()
	if len(host) > 0 && host[0] != "" && s.npmRegistryURL == "" {
		registry = "https://" + host[0]
	}
	upstream := strings.TrimSuffix(registry, "/") + rest
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	s.serve(w, r, object{kind: KindNPM, url: upstream, cacheable: npmCacheable(rest)})
}

// npmCacheable reports whether a registry path names a published
// tarball rather than a document about one.
func npmCacheable(filePath string) bool {
	lower := strings.ToLower(filePath)
	return strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".tar.gz")
}

// npmRegistry is the registry being proxied.
func (s *Server) npmRegistry() string {
	if s.npmRegistryURL != "" {
		return s.npmRegistryURL
	}
	return DefaultNPMRegistry
}
