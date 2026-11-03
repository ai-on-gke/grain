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

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
)

// V1Stats is the JSON payload returned by GET /pkg/v1/stats
// (design/design/grit-split.design.md, "Package cache").
type V1Stats struct {
	Enabled bool        `json:"enabled"`
	Entries int         `json:"entries"`
	Cache   []KindStats `json:"cache"`
}

// V1Flush is the JSON payload returned by POST /pkg/v1/flush?kind=.
type V1Flush struct {
	Kind    string      `json:"kind,omitempty"`
	Entries int         `json:"entries"`
	Bytes   int64       `json:"bytes"`
	Cache   []KindStats `json:"cache"`
}

// OpenAdmin opens an on-disk package cache at dir and returns it as an Admin.
func OpenAdmin(dir string, maxBytes int64) (Admin, error) {
	return NewCache(dir, maxBytes)
}

// NewV1Handler serves the cluster package cache's administrative routes
// under /pkg/v1:
//
//   - GET  /pkg/v1/stats
//   - POST /pkg/v1/flush?kind=<ecosystem>
//
// Authorization (the cache-admin scope) is enforced by grit's /v1 gateway
// in front of this handler.
func NewV1Handler(admin Admin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pkg/v1/stats":
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				httpErrorJSON(w, http.StatusMethodNotAllowed, "GET or HEAD required")
				return
			}
			if admin == nil {
				writeJSON(w, http.StatusOK, V1Stats{})
				return
			}
			stats, err := admin.Stats()
			if err != nil {
				httpErrorJSON(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, V1Stats{
				Enabled: true,
				Entries: admin.Entries(),
				Cache:   stats,
			})

		case "/pkg/v1/flush":
			if r.Method != http.MethodPost {
				httpErrorJSON(w, http.StatusMethodNotAllowed, "POST required")
				return
			}
			if admin == nil {
				httpErrorJSON(w, http.StatusNotImplemented, "package cache is not configured")
				return
			}
			kind := strings.TrimSpace(r.URL.Query().Get("kind"))
			entries, freed, err := admin.Flush(kind)
			if err != nil {
				httpErrorJSON(w, http.StatusBadRequest, err.Error())
				return
			}
			stats, _ := admin.Stats()
			writeJSON(w, http.StatusOK, V1Flush{
				Kind:    kind,
				Entries: entries,
				Bytes:   freed,
				Cache:   stats,
			})

		default:
			httpErrorJSON(w, http.StatusNotFound, "not found")
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErrorJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// JobAccess describes the package-cache permissions of one CI job pod
// (design/design/grit-split.design.md, "Package cache").
type JobAccess struct {
	// Job identifies the CI job pod for logs and diagnostics.
	Job string
	// ReadOnly marks a networkAccess: none job read-only, enforcing the
	// same restrictions as restrict.go's IP-based ClientSet.
	ReadOnly bool
	// Trusted marks a job belonging to a trustedCache: true run. Trusted
	// jobs write and read the "trusted" Go build-cache action namespace;
	// untrusted jobs write the "untrusted" namespace and read "trusted"
	// then "untrusted".
	Trusted bool
}

// JobTokenSet tracks active CI job pods authorized to reach the cluster
// package cache, keyed by per-job token and/or pod IP.
type JobTokenSet struct {
	require bool
	mu      sync.RWMutex
	tokens  map[string]JobAccess
	ips     map[string]JobAccess
}

// NewJobTokenSet creates a JobTokenSet. When requireIdentification is true,
// requests from callers presenting neither a registered token nor a
// registered pod IP are refused with 401 Unauthorized.
func NewJobTokenSet(requireIdentification bool) *JobTokenSet {
	return &JobTokenSet{
		require: requireIdentification,
		tokens:  make(map[string]JobAccess),
		ips:     make(map[string]JobAccess),
	}
}

// RequireIdentification reports whether unidentified callers are refused.
func (s *JobTokenSet) RequireIdentification() bool {
	return s != nil && s.require
}

// Register records a per-job token and its access permissions.
func (s *JobTokenSet) Register(token string, access JobAccess) {
	if s == nil || token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = access
}

// Revoke removes a per-job token when its job ends.
func (s *JobTokenSet) Revoke(token string) {
	if s == nil || token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

// RegisterIP records a pod IP and its access permissions.
func (s *JobTokenSet) RegisterIP(ip net.IP, access JobAccess) {
	if s == nil || ip == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ips[clientKey(ip)] = access
}

// RevokeIP removes a pod IP when its job ends.
func (s *JobTokenSet) RevokeIP(ip net.IP) {
	if s == nil || ip == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ips, clientKey(ip))
}

// Lookup resolves the JobAccess for r by Grit-Job-Token, Proxy-Authorization,
// Authorization (on /gocache/), or source IP.
func (s *JobTokenSet) Lookup(r *http.Request) (JobAccess, bool) {
	if s == nil {
		return JobAccess{}, false
	}
	if tok := extractJobToken(r); tok != "" {
		s.mu.RLock()
		for k, acc := range s.tokens {
			if subtle.ConstantTimeCompare([]byte(k), []byte(tok)) == 1 {
				s.mu.RUnlock()
				return acc, true
			}
		}
		s.mu.RUnlock()
		return JobAccess{}, false
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if ip := net.ParseIP(strings.TrimSpace(host)); ip != nil {
		s.mu.RLock()
		acc, ok := s.ips[clientKey(ip)]
		s.mu.RUnlock()
		if ok {
			return acc, true
		}
	}
	return JobAccess{}, false
}

func extractJobToken(r *http.Request) string {
	if tok := strings.TrimSpace(r.Header.Get("Grit-Job-Token")); tok != "" {
		return tok
	}
	if tok := parseAuthHeaderToken(r.Header.Get("Proxy-Authorization")); tok != "" {
		return tok
	}
	if strings.HasPrefix(r.URL.Path, goCachePrefix) {
		if tok := parseAuthHeaderToken(r.Header.Get("Authorization")); tok != "" {
			return tok
		}
	}
	return ""
}

func parseAuthHeaderToken(hdr string) string {
	hdr = strings.TrimSpace(hdr)
	if hdr == "" {
		return ""
	}
	scheme, rest, ok := strings.Cut(hdr, " ")
	if !ok {
		return ""
	}
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(scheme) {
	case "bearer":
		return rest
	case "basic":
		b, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return ""
		}
		if _, pass, ok := strings.Cut(string(b), ":"); ok && pass != "" {
			return pass
		}
		return string(b)
	default:
		return ""
	}
}
