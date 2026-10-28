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

// Persistent sandboxes (model.PersistentSandbox) in the UI: their status,
// their output, the operator's controls, and -- for one exposed under a
// UI sub-path -- the reverse proxy that serves it at /staging/<name>/.
//
// The proxy is mounted on this server's own mux, so a staging
// environment is reachable wherever the UI is, behind the same
// authentication every other page is.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// PersistentSandboxes is the daemon's persistent sandbox supervisor, as
// the UI needs it (pkg/daemon's adapter over
// orchestrator.PersistentSandboxSupervisor). Every method answers from
// what the supervisor holds, never waiting on a sandbox being built.
type PersistentSandboxes interface {
	// List is every defined persistent sandbox and its state.
	List(ctx context.Context) ([]model.PersistentSandboxStatus, error)
	// Logs is the tail of name's command output; false when name is not
	// running.
	Logs(name string) (string, bool)
	// Control applies an operator action: "restart", "stop", "start" or
	// "rebuild".
	Control(ctx context.Context, name, action string) error
	// Dial opens a connection to name's sandbox port.
	Dial(ctx context.Context, name string) (net.Conn, error)
	// StripPrefix reports whether name's UI sub-path is stripped before
	// forwarding, and whether name is served under the UI at all.
	StripPrefix(name string) (strip, served bool)
}

// persistentActions are the controls POST
// /api/persistent-sandboxes/{name}/{action} accepts.
var persistentActions = map[string]bool{"restart": true, "stop": true, "start": true, "rebuild": true}

func (s *Server) persistentSandboxes(w http.ResponseWriter) (PersistentSandboxes, bool) {
	ps := s.tasks.Config.PersistentSandboxes
	if ps == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("this server does not supervise persistent sandboxes"))
		return nil, false
	}
	return ps, true
}

// handleListPersistentSandboxes is GET /api/persistent-sandboxes.
func (s *Server) handleListPersistentSandboxes(w http.ResponseWriter, r *http.Request) {
	ps := s.tasks.Config.PersistentSandboxes
	if ps == nil {
		writeJSON(w, http.StatusOK, []model.PersistentSandboxStatus{})
		return
	}
	list, err := ps.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if list == nil {
		list = []model.PersistentSandboxStatus{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handlePersistentSandboxLogs is GET /api/persistent-sandboxes/{name}/logs:
// {"logs": ...}, or the output itself as plain text with ?format=text,
// which is what the panel's logs link opens in a tab of its own.
func (s *Server) handlePersistentSandboxLogs(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.persistentSandboxes(w)
	if !ok {
		return
	}
	logs, ok := ps.Logs(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("persistent sandbox %q is not running", r.PathValue("name")))
		return
	}
	if r.URL.Query().Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = io.WriteString(w, logs)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

// handlePersistentSandboxAction is POST
// /api/persistent-sandboxes/{name}/{action}: a system admin's, since it
// stops or rebuilds an environment other people may be using.
func (s *Server) handlePersistentSandboxAction(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.persistentSandboxes(w)
	if !ok {
		return
	}
	action := r.PathValue("action")
	if !persistentActions[action] {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown action %q: want restart, stop, start or rebuild", action))
		return
	}
	if err := ps.Control(r.Context(), r.PathValue("name"), action); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleStagingProxy serves /staging/{name}/... from the persistent
// sandbox called name: plain HTTP, server-sent events (flushed as they
// arrive) and WebSocket upgrades alike, which httputil.ReverseProxy
// carries over the one connection Dial opens.
func (s *Server) handleStagingProxy(w http.ResponseWriter, r *http.Request) {
	// Who may reach it is decided before this runs: /staging/ is behind
	// the same gate as the API (requiresAccess).
	ps := s.tasks.Config.PersistentSandboxes
	name := r.PathValue("name")
	if ps == nil {
		http.NotFound(w, r)
		return
	}
	strip, served := ps.StripPrefix(name)
	if !served {
		writeError(w, http.StatusNotFound, fmt.Errorf("no persistent sandbox %q is served under the UI", name))
		return
	}
	prefix := model.PersistentSandboxSubpath(name)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "persistent-sandbox"
			pr.Out.Host = pr.In.Host
			if strip {
				pr.Out.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(pr.In.URL.Path, prefix), "/")
				pr.Out.URL.RawPath = ""
			}
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-Prefix", prefix)
			// The UI's own credentials are for the UI: the service behind
			// it is somebody's staging build, not something to hand them to.
			pr.Out.Header.Del("Authorization")
			stripGrainCookies(pr.Out)
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return ps.Dial(ctx, name)
			},
			DisableKeepAlives: true,
		},
		// Negative flushes every write at once, which is what SSE needs.
		FlushInterval: -1,
		// ReverseProxy calls this for a 101 upgrade too, and has already
		// dropped the response's hop-by-hop headers (bar, on a 101, the
		// Upgrade and Connection the upgrade needs) by the time it does.
		ModifyResponse: func(resp *http.Response) error {
			// Add, not Set: browsers enforce every policy they are sent,
			// so the app's own cannot loosen this one.
			resp.Header.Add("Content-Security-Policy", stagingSandboxPolicy)
			scopeStagingCookies(resp.Header, prefix, strip)
			// The response is served from grain's origin, where clearing
			// site data would clear grain's.
			resp.Header.Del("Clear-Site-Data")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadGateway, fmt.Errorf("staging environment %q: %w", name, err))
		},
	}
	// grain's own policy (security_headers.go) is for grain's pages: a
	// staging app's scripts and styles are its own, and are held instead
	// to its own policy, which is passed through, and stagingSandboxPolicy.
	w.Header().Del("Content-Security-Policy")
	proxy.ServeHTTP(w, r)
}

// stagingSandboxPolicy is the Content-Security-Policy every staging
// response is served with. A staging environment is served from the grain
// UI's own origin, so without it a page in one runs its scripts as grain's
// own -- able to call grain's API with whoever is looking at it. The
// sandbox directive without allow-same-origin gives the page an opaque
// origin instead: its scripts, forms and popups still work, but nothing it
// does is same-origin with grain. The cost is that the page cannot use
// its own cookies or storage from script.
const stagingSandboxPolicy = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads"

// requestLocalPort is the port of the local address r arrived on -- the
// daemon's own listening port as this request saw it -- or 0 when the
// server did not record one (a test calling the handler directly).
func requestLocalPort(r *http.Request) int {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || addr == nil {
		return 0
	}
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return n
}

// grainCookiePrefix begins the name of every cookie grain sets for
// itself. The staging proxy keeps those cookies away from a staging
// environment in both directions: grain's are not sent to it, and it
// cannot set its own under those names.
const grainCookiePrefix = "grain_"

// stripGrainCookies drops grain's own cookies (named grain_*) from a
// request bound for a staging environment, keeping the environment's own.
func stripGrainCookies(r *http.Request) {
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cookies {
		if !strings.HasPrefix(c.Name, grainCookiePrefix) {
			r.AddCookie(c)
		}
	}
}

// scopeStagingCookies rewrites the Set-Cookie headers of a response from
// the staging environment served under prefix. The response is served
// from grain's own origin, so a cookie it sets is set there: one named
// like grain's own would replace it, and one with Path=/ would ride on
// every request to grain. Cookies named grain_* (and any that do not
// parse) are dropped; the rest lose their Domain and are confined to
// prefix -- an app whose prefix is stripped thinks it is served from /,
// so its paths are taken as relative to prefix.
func scopeStagingCookies(h http.Header, prefix string, strip bool) {
	lines := h.Values("Set-Cookie")
	h.Del("Set-Cookie")
	for _, line := range lines {
		c, err := http.ParseSetCookie(line)
		if err != nil || strings.HasPrefix(c.Name, grainCookiePrefix) {
			continue
		}
		c.Domain = ""
		switch {
		case c.Path == prefix || strings.HasPrefix(c.Path, prefix+"/"):
		case strip && strings.HasPrefix(c.Path, "/"):
			c.Path = strings.TrimSuffix(prefix+c.Path, "/")
		default:
			c.Path = prefix
		}
		if v := c.String(); v != "" {
			h.Add("Set-Cookie", v)
		}
	}
}
