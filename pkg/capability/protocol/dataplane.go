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

package protocol

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// This file is the data plane of a grant's mcp_endpoint (§5.2): where a
// server's /runs/<attempt_id>/mcp is reached, the loopback proxy Grain
// puts in front of it (§5.2.2), and the handshake that learns the
// endpoint's server name and tools (§5.2.3).

// DataPlaneTokenEnv is the environment variable a stdio server with an
// attempt-scoped toolset reads its data-plane listener's bearer token
// from; Grain's proxy sends it on every /runs/<attempt_id>/mcp request.
const DataPlaneTokenEnv = "GRAIN_CAPABILITY_TOKEN"

// DataPlaneTokenConfig is the configure field (§4.5) a stdio server with
// an attempt-scoped toolset may take that same token through instead: a
// secret field of its config schema, which, once configured, overrides
// DataPlaneTokenEnv on the server and on Grain's proxy alike.
const DataPlaneTokenConfig = "dataPlaneToken"

// AuditTaskChangelistField is the field of an audit record (§4.4.4)
// naming a CL, as cl/<number>, that the record shows is the task's own
// -- one the grant created, or acted on once it had checked it belongs to
// the task. grain links the task to each one (model.LinkChangelist) when
// the run's pass is released.
const AuditTaskChangelistField = "task_changelist"

// DataPlane is where a server's attempt toolsets are served: Origin
// (scheme://host[:port], no path) and the headers every proxied request
// carries. It holds resolved secrets: keep it in memory only.
type DataPlane struct {
	Origin  string
	Headers map[string]string
}

// DataPlane is where a resolved entry serves /runs/<attempt_id>/mcp.
// An HTTP server serves it on serverUrl's origin, behind the same headers
// as its control plane (§5.2.2). A stdio server has no origin of its own
// to read, so it is the loopback listener its args name with -listen
// (the in-tree servers' convention), behind a bearer token taken from
// DataPlaneTokenEnv in its env.
func (c ServerConfig) DataPlane() (DataPlane, error) {
	if !c.Stdio() {
		u, err := url.Parse(c.ServerURL)
		if err != nil || u.Host == "" {
			return DataPlane{}, fmt.Errorf("serverUrl %q has no origin", c.ServerURL)
		}
		return DataPlane{Origin: u.Scheme + "://" + u.Host, Headers: copyHeaders(c.Headers)}, nil
	}
	listen := ""
	for i, a := range c.Args {
		switch {
		case a == "-listen" || a == "--listen":
			if i+1 < len(c.Args) {
				listen = c.Args[i+1]
			}
		case strings.HasPrefix(a, "-listen="), strings.HasPrefix(a, "--listen="):
			_, listen, _ = strings.Cut(a, "=")
		}
	}
	if listen == "" {
		return DataPlane{}, errors.New("this stdio server's args name no -listen address, so it serves no mcp_endpoint grain can reach")
	}
	host, _, err := net.SplitHostPort(listen)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return DataPlane{}, fmt.Errorf("this stdio server's -listen %q is not a loopback IP address and port", listen)
	}
	dp := DataPlane{Origin: "http://" + listen, Headers: map[string]string{}}
	if tok := c.Env[DataPlaneTokenEnv]; tok != "" {
		dp.Headers["Authorization"] = "Bearer " + tok
	}
	return dp, nil
}

func copyHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// EndpointPath is the data-plane path of attemptID's toolset (§5.2.1).
func EndpointPath(attemptID string) string { return "/runs/" + attemptID + "/mcp" }

// proxiedHeaders are the request headers a client's request keeps on its
// way through the proxy (§5.2.2); everything else, Authorization and the
// hop-by-hop headers included, is dropped.
var proxiedHeaders = []string{"Accept", "Content-Type", "Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-Id"}

// proxiedResponseHeaders are the response headers handed back.
var proxiedResponseHeaders = []string{"Content-Type", "Mcp-Session-Id", "Cache-Control"}

// maxProxiedBody bounds one proxied request body.
const maxProxiedBody = 32 << 20

// Proxy forwards a consumer's Streamable HTTP requests to one attempt's
// toolset (§5.2.2): whatever path and query a request names, it goes to
// exactly EndpointPath(attemptID) on the data plane's origin, with only
// POST, GET and DELETE carried, only proxiedHeaders kept, and the data
// plane's own headers injected.
type Proxy struct {
	dp        DataPlane
	attemptID string
	client    *http.Client

	mu      sync.RWMutex
	observe ToolCallObserver
}

// ToolCallObserver is told of each tools/call a Proxy forwards, before
// it is forwarded: the tool's name and its arguments, exactly as the
// consumer sent them and so not to be trusted. It runs on the request's
// own goroutine, so it must not block: whatever it does that can take
// time is its own to do elsewhere.
type ToolCallObserver func(tool string, args map[string]any)

// ObserveToolCalls has p tell observe of every tools/call it forwards
// from now on; nil stops it. Safe to call while p is serving.
func (p *Proxy) ObserveToolCalls(observe ToolCallObserver) {
	p.mu.Lock()
	p.observe = observe
	p.mu.Unlock()
}

// NewProxy returns the proxy for attemptID's toolset on dp. client nil is a
// plain client; redirects are never followed either way.
func NewProxy(dp DataPlane, attemptID string, client *http.Client) (*Proxy, error) {
	if !ValidAttemptID(attemptID) {
		return nil, fmt.Errorf("attempt id %q does not match %s", attemptID, attemptIDPattern)
	}
	hc := &http.Client{}
	if client != nil {
		copied := *client
		hc = &copied
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Proxy{dp: dp, attemptID: attemptID, client: hc}, nil
}

// Target is the one URL every request is sent to.
func (p *Proxy) Target() string { return p.dp.Origin + EndpointPath(p.attemptID) }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost, http.MethodGet, http.MethodDelete:
	default:
		w.Header().Set("Allow", "POST, GET, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body io.Reader
	if r.Method == http.MethodPost {
		body = http.MaxBytesReader(w, r.Body, maxProxiedBody)
		p.mu.RLock()
		observe := p.observe
		p.mu.RUnlock()
		if observe != nil {
			// Read whole so the call can be named before it is sent on;
			// the same bound applies as when it was streamed through.
			buf, err := io.ReadAll(body)
			if err != nil {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			body = bytes.NewReader(buf)
			for _, c := range toolCalls(buf) {
				observe(c.Name, c.Arguments)
			}
		}
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, p.Target(), body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, h := range proxiedHeaders {
		if v := r.Header.Values(h); len(v) > 0 {
			req.Header[http.CanonicalHeaderKey(h)] = append([]string(nil), v...)
		}
	}
	for k, v := range p.dp.Headers {
		req.Header.Set(k, v)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// The upstream's address is the operator's; the error is not the
		// consumer's to read.
		http.Error(w, "the capability server did not answer", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range proxiedResponseHeaders {
		if v := resp.Header.Values(h); len(v) > 0 {
			w.Header()[http.CanonicalHeaderKey(h)] = append([]string(nil), v...)
		}
	}
	if resp.StatusCode/100 == 3 {
		// A redirect is never followed and never handed on: it could
		// only point the consumer somewhere other than the granted path.
		http.Error(w, "the capability server answered with a redirect", http.StatusBadGateway)
		return
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// proxiedToolCall is the params of one tools/call.
type proxiedToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// toolCalls is every tools/call request in body, a JSON-RPC message or a
// batch of them. Anything that does not parse is simply not a call: the
// upstream answers it however it answers it, and the proxy never refuses
// a request for its own reading of it.
func toolCalls(body []byte) []proxiedToolCall {
	type message struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	var msgs []message
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '[' {
		if json.Unmarshal(trimmed, &msgs) != nil {
			return nil
		}
	} else {
		var m message
		if json.Unmarshal(trimmed, &m) != nil {
			return nil
		}
		msgs = []message{m}
	}
	var calls []proxiedToolCall
	for _, m := range msgs {
		if m.Method != "tools/call" {
			continue
		}
		var c proxiedToolCall
		if json.Unmarshal(m.Params, &c) != nil || c.Name == "" {
			continue
		}
		calls = append(calls, c)
	}
	return calls
}

// Endpoint is a Proxy served on a loopback listener of Grain's own, at a
// path holding a random secret: loopback is not a trust boundary (§1.3.2),
// and the proxy injects the server's credentials into whatever it is
// sent, so only a holder of URL can use it.
type Endpoint struct {
	// URL is what a consumer is configured with.
	URL   string
	proxy *Proxy
	srv   *http.Server
}

// ServeEndpoint serves p on a new listener at 127.0.0.1:0.
func ServeEndpoint(p *Proxy) (*Endpoint, error) {
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	path := "/" + hex.EncodeToString(secret) + "/mcp"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, p.ServeHTTP)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 30 * time.Second}
	go srv.Serve(ln)
	return &Endpoint{URL: "http://" + ln.Addr().String() + path, proxy: p, srv: srv}, nil
}

// Handler is the proxy URL serves, for a consumer that cannot reach
// that listener and is carried to grain some other way (a remote
// executor's stream): the same §5.2.2 rules apply to what it is sent.
func (e *Endpoint) Handler() http.Handler { return e.proxy }

// Proxy is the proxy URL serves.
func (e *Endpoint) Proxy() *Proxy { return e.proxy }

// Close stops the listener and every request in flight on it.
func (e *Endpoint) Close() error {
	if e == nil {
		return nil
	}
	return e.srv.Close()
}

// toolNamePattern is the tool names an endpoint may advertise: what every
// agent CLI's qualified spelling (mcp__<server>__<tool>, a TOML key, an
// allow rule) can carry unescaped.
var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// EndpointRoster is what the data-plane handshake learned of an endpoint
// (§5.2.3): serverInfo.name and the attempt's tool names.
type EndpointRoster struct {
	ServerName string
	Tools      []string
	// Labels is what each tool calls itself, for a person: its
	// annotations.title, else its title, else its description, keyed by
	// tool name, and absent for a tool that says none of them. The
	// server's own text, so untrusted.
	Labels map[string]string
}

// Handshake performs initialize and tools/list against url, a proxy
// Endpoint's, and checks what it reports: a tool whose name cannot be
// wired into an agent CLI is a validation failure, as is an endpoint that
// lists none. The server name is checked by AssignServerNames.
func Handshake(ctx context.Context, url string) (EndpointRoster, error) {
	c, err := Dial(ctx, ServerConfig{ServerURL: url}, DialOptions{})
	if err != nil {
		return EndpointRoster{}, err
	}
	defer c.Close()
	tools, err := c.ListTools(ctx)
	if err != nil {
		return EndpointRoster{}, err
	}
	out := EndpointRoster{ServerName: c.ServerName}
	seen := map[string]bool{}
	for _, t := range tools {
		if !toolNamePattern.MatchString(t.Name) {
			return EndpointRoster{}, fmt.Errorf("tools/list names a tool %q, which does not match %s", truncate(t.Name, 80), toolNamePattern)
		}
		if strings.HasPrefix(t.Name, mintPrefix) || strings.HasPrefix(t.Name, releasePrefix) {
			return EndpointRoster{}, fmt.Errorf("tools/list names %s, a control-plane tool no attempt toolset may offer", t.Name)
		}
		if !seen[t.Name] {
			seen[t.Name] = true
			out.Tools = append(out.Tools, t.Name)
			if label := toolLabel(t); label != "" {
				if out.Labels == nil {
					out.Labels = map[string]string{}
				}
				out.Labels[t.Name] = label
			}
		}
	}
	if len(out.Tools) == 0 {
		return EndpointRoster{}, errors.New("tools/list names no tools")
	}
	return out, nil
}

// toolLabel is t's own words for itself, the most specific first.
func toolLabel(t Tool) string {
	for _, s := range []string{annotationTitle(t), t.Title, t.Description} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

func annotationTitle(t Tool) string {
	if t.Annotations == nil {
		return ""
	}
	return t.Annotations.Title
}
