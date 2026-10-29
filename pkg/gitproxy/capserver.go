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

package gitproxy

// capserver.go serves an external capability server's sandbox-callable
// tools under /cap/ (docs/capabilities-spec.md §5.2.6): GET /cap/ lists
// the servers granted to the sandbox's attempt (or CI job) and their
// sandbox tools, GET /cap/<server> one of them, and /cap/<server>/mcp is
// the server's own MCP endpoint, carried to the proxy grain already holds
// open for the grant (orchestrator's wireEndpoints). Every tools/call is
// checked against the tool's resolved delivery first: one naming a
// model-only tool -- or a tool the server never listed -- is refused with
// 403 before any byte reaches the server.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// maxCapRequestBytes bounds one /cap/<server>/mcp request body, which the
// proxy reads whole to check its tools/calls before forwarding it.
const maxCapRequestBytes = 4 << 20

// CapTool is one tool of a capability server's attempt toolset, with the
// delivery grain resolved for it (protocol.ResolveModelOnly).
type CapTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	ModelOnly   bool           `json:"modelOnly"`
}

// CapServer is one granted capability server as a sandbox reaches it.
type CapServer struct {
	// Name is the server's name in the attempt (protocol.AssignServerNames).
	Name string
	// Grant is the capability reference it was granted under.
	Grant string
	Tools []CapTool
	// Handler is the grant's endpoint proxy: it injects the server's own
	// credentials, so it is reached only after the delivery check.
	Handler http.Handler
}

// tool reports whether the server listed the named tool, and whether
// grain resolved it model-only.
func (s CapServer) tool(name string) (listed, modelOnly bool) {
	for _, t := range s.Tools {
		if t.Name == name {
			return true, t.ModelOnly
		}
	}
	return false, false
}

// CapRegistry is where the servers a sandbox may reach under /cap/ are
// registered: by an agent run's run ID (CapRunKey) or a CI job's sandbox
// name (CapSandboxKey), for exactly as long as the materialization that
// proxies them is open.
type CapRegistry struct {
	mu   sync.Mutex
	next uint64
	by   map[string]capEntry
}

type capEntry struct {
	gen     uint64
	servers []CapServer
}

// NewCapRegistry returns an empty registry.
func NewCapRegistry() *CapRegistry { return &CapRegistry{by: map[string]capEntry{}} }

// CapRunKey is the key an agent run's servers are registered under.
func CapRunKey(runID string) string { return "run/" + runID }

// CapSandboxKey is the key a CI job's servers are registered under: its
// sandbox has no task run to be resolved through.
func CapSandboxKey(sandbox string) string { return "sandbox/" + sandbox }

// Register makes servers the ones key reaches, replacing any before, and
// returns what removes them again -- which removes nothing once a later
// Register has replaced them.
func (r *CapRegistry) Register(key string, servers []CapServer) (unregister func()) {
	if r == nil || key == "" {
		return func() {}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.by == nil {
		r.by = map[string]capEntry{}
	}
	r.next++
	gen := r.next
	r.by[key] = capEntry{gen: gen, servers: append([]CapServer(nil), servers...)}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if e, ok := r.by[key]; ok && e.gen == gen {
			delete(r.by, key)
		}
	}
}

// Servers returns what key reaches, and whether anything is registered.
func (r *CapRegistry) Servers(key string) ([]CapServer, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.by[key]
	return e.servers, ok
}

// capListing is GET /cap/'s answer: each server's sandbox tools, and the
// names of the tools only the model may call, so a listing explains why
// a tool the prompt mentions is not here.
type capListing struct {
	Name           string    `json:"name"`
	Grant          string    `json:"grant,omitempty"`
	Tools          []CapTool `json:"tools"`
	ModelOnlyTools []string  `json:"modelOnlyTools,omitempty"`
}

func listCapServer(s CapServer) capListing {
	out := capListing{Name: s.Name, Grant: s.Grant, Tools: []CapTool{}}
	for _, t := range s.Tools {
		if t.ModelOnly {
			out.ModelOnlyTools = append(out.ModelOnlyTools, t.Name)
			continue
		}
		out.Tools = append(out.Tools, t)
	}
	return out
}

// serveExternalCap answers /cap/<rest> from servers. runID, when set, is
// the live attempt the call is counted against in task_run_tool; a CI
// job's call has none and goes to the daemon's log alone.
func (p *GitProxy) serveExternalCap(w http.ResponseWriter, r *http.Request, servers []CapServer, rest, taskID, runID, who string) {
	name, tail, _ := strings.Cut(rest, "/")
	act := RouteActivity{Tool: "cap_list", Phrase: "listing capability tools"}
	if name != "" {
		act = RouteActivity{Tool: "cap__" + name, Phrase: fmt.Sprintf("calling capability %s", name)}
	}
	cw := &countingResponseWriter{ResponseWriter: w}
	defer func() {
		if runID != "" {
			p.recordRouteCall(r.Context(), runID, act, cw)
		} else {
			log.Printf("grain: sandbox proxy call %s -> %s (status %d, %d bytes)", who, act.Tool, cw.statusCode(), cw.bytes)
		}
	}()
	if name == "" {
		if r.Method != http.MethodGet {
			http.Error(cw, "GET /cap/ lists this sandbox's capability servers", http.StatusMethodNotAllowed)
			return
		}
		p.noteActivity(r.Context(), taskID, act.Phrase)
		list := make([]capListing, 0, len(servers))
		for _, s := range servers {
			list = append(list, listCapServer(s))
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		writeCapJSON(cw, map[string]any{"servers": list})
		return
	}
	var srv *CapServer
	for i := range servers {
		if servers[i].Name == name {
			srv = &servers[i]
			break
		}
	}
	if srv == nil {
		http.Error(cw, fmt.Sprintf("forbidden: capability server %q is not granted to this sandbox, or none of its tools may be called from it (run `grain cap list`)", name), http.StatusForbidden)
		return
	}
	switch tail {
	case "", "tools":
		if r.Method != http.MethodGet {
			http.Error(cw, "GET /cap/<server> describes the server's sandbox tools", http.StatusMethodNotAllowed)
			return
		}
		p.noteActivity(r.Context(), taskID, act.Phrase)
		writeCapJSON(cw, listCapServer(*srv))
		return
	case "mcp":
	default:
		http.Error(cw, "not found: /cap/<server> serves only /cap/<server>/mcp", http.StatusNotFound)
		return
	}
	in := r.Clone(r.Context())
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxCapRequestBytes+1))
		if err != nil {
			http.Error(cw, "reading the request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body) > maxCapRequestBytes {
			http.Error(cw, fmt.Sprintf("request body exceeds %d bytes", maxCapRequestBytes), http.StatusRequestEntityTooLarge)
			return
		}
		calls, err := capToolCalls(body)
		if err != nil {
			http.Error(cw, "the request is not a JSON-RPC message: "+err.Error(), http.StatusBadRequest)
			return
		}
		for _, tool := range calls {
			if listed, modelOnly := srv.tool(tool); !listed {
				http.Error(cw, fmt.Sprintf("forbidden: capability server %q lists no tool %q for this run (run `grain cap %s` for its sandbox tools)", name, tool, name), http.StatusForbidden)
				return
			} else if modelOnly {
				http.Error(cw, fmt.Sprintf("forbidden: tool %q of capability server %q is model-only: only the agent's own MCP channel may call it, never the sandbox (docs/capabilities-spec.md §5.2.6)", tool, name), http.StatusForbidden)
				return
			}
		}
		if len(calls) == 1 {
			act = RouteActivity{Tool: "cap__" + name + "__" + calls[0], Phrase: fmt.Sprintf("calling %s on capability %s", calls[0], name)}
		}
		in.Body = io.NopCloser(bytes.NewReader(body))
		in.ContentLength = int64(len(body))
	}
	p.noteActivity(r.Context(), taskID, act.Phrase)
	// The proxy token authenticates the sandbox to this proxy and to
	// nothing else, and the X-Grain-* headers are the proxy's to set:
	// neither is passed on to a capability server.
	for k := range in.Header {
		if strings.EqualFold(k, "Authorization") || strings.HasPrefix(strings.ToLower(k), "x-grain-") {
			in.Header.Del(k)
		}
	}
	srv.Handler.ServeHTTP(cw, in)
}

// capToolCalls is the tool name of every tools/call in body, a JSON-RPC
// message or batch. A body with two keys in one object that differ at
// most in case is refused outright: encoding/json matches keys without
// regard to case and keeps the last duplicate, so such a body could read
// as one tool here and as another to the capability server's parser --
// {"method":"tools/call","params":{"name":"secret"},"Params":{"name":"ping"}}
// is ping to this check and secret to a case-sensitive server.
func capToolCalls(body []byte) ([]string, error) {
	if err := rejectAmbiguousKeys(body); err != nil {
		return nil, err
	}
	type message struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	var msgs []message
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &msgs); err != nil {
			return nil, err
		}
	} else {
		var m message
		if err := json.Unmarshal(trimmed, &m); err != nil {
			return nil, err
		}
		msgs = []message{m}
	}
	var out []string
	for _, m := range msgs {
		if m.Method != "tools/call" {
			continue
		}
		var params struct {
			Name string `json:"name"`
		}
		if len(m.Params) > 0 {
			if err := json.Unmarshal(m.Params, &params); err != nil {
				return nil, fmt.Errorf("tools/call params: %w", err)
			}
		}
		out = append(out, params.Name)
	}
	return out, nil
}

// rejectAmbiguousKeys refuses a JSON document any of whose objects holds
// two keys equal under Unicode case folding (exact duplicates included):
// the keys parsers disagree on, so the ones no tools/call check can read
// the way the server will.
func rejectAmbiguousKeys(body []byte) error {
	type frame struct {
		object    bool
		expectKey bool
		keys      map[string]bool
	}
	var stack []*frame
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, &frame{object: true, expectKey: true, keys: map[string]bool{}})
			case '[':
				stack = append(stack, &frame{})
			default:
				stack = stack[:len(stack)-1]
				valueDone()
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].expectKey {
				top := stack[n-1]
				k := foldKey(v)
				if top.keys[k] {
					return fmt.Errorf("key %q appears twice in one object (keys are compared without regard to case)", v)
				}
				top.keys[k] = true
				top.expectKey = false
				continue
			}
			valueDone()
		default:
			valueDone()
		}
	}
}

// foldKey maps s to one spelling per case-folding class: each rune to the
// least rune of its unicode.SimpleFold orbit, which is how
// strings.EqualFold (and encoding/json's key matching) compares runes.
func foldKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < least {
				least = f
			}
		}
		b.WriteRune(least)
	}
	return b.String()
}

func writeCapJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
