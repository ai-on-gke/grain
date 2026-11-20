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

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
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
	page, err := parseListQuery(r, listPageMedium)
	if err != nil {
		writeClientError(w, err)
		return
	}
	ps := s.tasks.Config.PersistentSandboxes
	if ps == nil {
		writeList(w, []model.PersistentSandboxStatus{}, page)
		return
	}
	list, err := ps.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	list = filterList(list, func(st model.PersistentSandboxStatus) bool {
		return page.matches(st.Name, st.Repo, st.State)
	})
	writeListBy(w, list, page, func(st model.PersistentSandboxStatus) string { return st.Name })
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
	redactor := grainlog.NewRedactor(nil)
	if s.tasks != nil && s.tasks.Config.LogRedactor != nil {
		var err error
		redactor, err = s.tasks.Config.LogRedactor()
		if err != nil || redactor == nil {
			if err == nil {
				err = errors.New("no redactor")
			}
			writeError(w, http.StatusInternalServerError, apierror.Expose(fmt.Errorf(
				"this log cannot be shown: the credentials that would have to be redacted out of it "+
					"could not be read, and it is not served unredacted instead: %w", err)))
			return
		}
	}
	// Text, not Line: the log is many lines, and a private key's body
	// is only cut by the pass that sees the lines after its header.
	logs, _ = redactor.Text(logs)
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
		w.Header().Set("Cache-Control", cacheControlNoStore)
		http.NotFound(w, r)
		return
	}
	strip, served := ps.StripPrefix(name)
	if !served {
		writeError(w, http.StatusNotFound, fmt.Errorf("no persistent sandbox %q is served under the UI", name))
		return
	}
	prefix := model.PersistentSandboxSubpath(name)
	edge := relayedByVerifiedEdge(r)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "persistent-sandbox"
			pr.Out.Host = pr.In.Host
			if strip {
				pr.Out.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(pr.In.URL.Path, prefix), "/")
				pr.Out.URL.RawPath = ""
			}
			setStagingForwarded(pr, edge)
			pr.Out.Header.Set("X-Forwarded-Prefix", prefix)
			// The UI's own credentials are for the UI: the service behind
			// it is somebody's staging build, not something to hand them to.
			stripStagingCredentials(pr.Out)
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

// relayedByVerifiedEdge reports whether r came through the identity-aware
// edge this daemon is configured behind: it carries an assertion a
// registered IdentityVerifier (IAP, the hub, or any other) accepted, which
// a caller reaching the daemon directly cannot mint. Only then are the
// edge's forwarding headers its own rather than whatever the caller wrote.
// The API socket's callers (whose forwarding headers HostAccountHandler
// drops), the access code and in-process requests are never relayed.
func relayedByVerifiedEdge(r *http.Request) bool {
	cv := cachedOrVerifyCaller(r)
	return cv.present && cv.err == nil && cv.verifier != nil
}

// setStagingForwarded sets the X-Forwarded-* headers a staging app
// receives. ReverseProxy has already dropped the inbound ones from
// pr.Out; SetXForwarded alone describes the hop into this daemon, whose
// listener is plain http and whose client is the edge. Behind a verified
// edge, the edge's X-Forwarded-For chain is kept, with the edge's address
// appended to it, and its X-Forwarded-Proto (http or https only) replaces
// the daemon's own scheme.
func setStagingForwarded(pr *httputil.ProxyRequest, edge bool) {
	if edge {
		if chain := pr.In.Header.Values("X-Forwarded-For"); len(chain) > 0 {
			pr.Out.Header["X-Forwarded-For"] = append([]string(nil), chain...)
		}
	}
	pr.SetXForwarded()
	if !edge {
		return
	}
	if protos := pr.In.Header.Values("X-Forwarded-Proto"); len(protos) == 1 {
		switch p := strings.ToLower(strings.TrimSpace(protos[0])); p {
		case "http", "https":
			pr.Out.Header.Set("X-Forwarded-Proto", p)
		}
	}
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

// isGrainCookie reports whether name is one of grain's own or upstream edge
// cookies that a staging environment must neither read nor shadow:
//   - any __Host- cookie (browsers require Path=/ for __Host-, so a staging
//     app confined to /staging/<name> can never set one, and any __Host-
//     cookie on a request belongs to the root origin or its edge proxy),
//   - grain_* (with or without a __Secure- prefix), and
//   - Google Cloud IAP's browser session cookies (GCP_IAP*, GCP_IAAP*, with or
//     without __Secure-), which IAP forwards in Cookie to the backend.
//
// Browsers match __Host- and __Secure- prefixes case-insensitively, so this
// does too.
func isGrainCookie(name string) bool {
	name = strings.TrimSpace(name)
	if len(name) >= len("__Host-") && strings.EqualFold(name[:len("__Host-")], "__Host-") {
		return true
	}
	if len(name) >= len("__Secure-") && strings.EqualFold(name[:len("__Secure-")], "__Secure-") {
		name = name[len("__Secure-"):]
	}
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, grainCookiePrefix) ||
		strings.HasPrefix(lower, "gcp_iap") ||
		strings.HasPrefix(lower, "gcp_iaap")
}

// stripStagingCredentials drops grain's own request credentials, signed
// proxy identity assertions, and controller/edge cookies before forwarding a
// request to a staging environment, so an untrusted staging build never
// receives tokens, session cookies, or replayable assertions valid against
// grain itself.
//
// A header is matched with its underscores read as dashes too: CGI, WSGI,
// Rack and the servers behind them file X_Goog_IAP_JWT_Assertion and
// X-Goog-IAP-JWT-Assertion under the same HTTP_X_GOOG_IAP_JWT_ASSERTION,
// so a caller could otherwise hand the app an identity header in the name
// of the edge the real one was stripped from.
func stripStagingCredentials(r *http.Request) {
	names := []string{
		"Authorization",
		"Proxy-Authorization",
		"X-Grain-Tunnel-Token",
		"X-Grain-Hub-Token",
		"X-Grain-Registry-Token",
		"X-Grain-Subject-Token",
		"X-Grain-On-Behalf-Of",
		AccessCodeHeader,
		AccessNameHeader,
		ActingTaskHeader,
		ActingRunHeader,
		ActingReasonHeader,
		ChannelHeader,
	}
	names = append(names, proxyIdentityHeaders...)
	names = append(names, PlaintextIdentityHeaders...)
	for _, v := range registeredVerifiersSnapshot() {
		names = append(names, v.Headers()...)
	}
	strip := make(map[string]bool, len(names))
	for _, h := range names {
		strip[stagingHeaderKey(h)] = true
	}
	for k := range r.Header {
		key := stagingHeaderKey(k)
		if strip[key] || strings.HasPrefix(key, "x-goog-iap-") || strings.HasPrefix(key, "x-goog-authenticated-user-") {
			delete(r.Header, k)
		}
	}
	stripGrainCookies(r)
}

// stagingHeaderKey is the form stripStagingCredentials compares header
// names in: lower case, with underscores read as dashes.
func stagingHeaderKey(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

// stripGrainCookies drops grain's own cookies (isGrainCookie) from a
// request bound for a staging environment, keeping the environment's own
// exactly as the browser sent them. It filters the Cookie lines' pairs by
// name rather than parsing and re-serialising them: net/http's parser
// discards values it holds invalid (JSON, a backslash, UTF-8) and quotes
// ones with spaces, which would lose or change the app's own cookies.
func stripGrainCookies(r *http.Request) {
	lines := r.Header.Values("Cookie")
	if len(lines) == 0 {
		return
	}
	var kept []string
	for _, line := range lines {
		for _, pair := range strings.Split(line, ";") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			name, _, _ := strings.Cut(pair, "=")
			if isGrainCookie(name) {
				continue
			}
			kept = append(kept, pair)
		}
	}
	r.Header.Del("Cookie")
	if len(kept) > 0 {
		r.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}

// scopeStagingCookies rewrites the Set-Cookie headers of a response from
// the staging environment served under prefix. The response is served
// from grain's own origin, so a cookie it sets is set there: one named
// like grain's own would replace it, and one with Path=/ would ride on
// every request to grain. grain's own cookies (isGrainCookie) and any
// that do not parse are dropped; the rest lose their Domain and are confined to
// prefix -- an app whose prefix is stripped thinks it is served from /,
// so its paths are taken as relative to prefix.
func scopeStagingCookies(h http.Header, prefix string, strip bool) {
	lines := h.Values("Set-Cookie")
	h.Del("Set-Cookie")
	for _, line := range lines {
		c, err := http.ParseSetCookie(line)
		if err != nil || isGrainCookie(c.Name) {
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
