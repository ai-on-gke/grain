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
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// CapabilityServer serves the control plane of a capability written in Go:
// initialize, tools/list and tools/call for its methods' mint_ and release_
// tools, over stdio (ServeStdio) or Streamable HTTP (ServeHTTP).
//
// It is deliberately not pkg/mcp's Registry, whose answers are bounded for
// a model's context: a mint result may carry up to 8 MiB of placements and
// must reach Grain whole.
type CapabilityServer struct {
	name string

	mu    sync.RWMutex
	order []string
	tools map[string]servedTool
	// configure is the configure tool, when HandleConfigure set one.
	configure *servedTool
	// plain are the tools HandleTool added, which SetMethods keeps.
	plain      map[string]servedTool
	plainOrder []string
	// Token is the bearer token ServeHTTP requires on every request
	// (§3.1.3); a request without it is answered 401. ServeHTTP refuses
	// everything while it is shorter than MinHTTPTokenBytes. ServeStdio
	// does not read it.
	Token string
}

// MinHTTPTokenBytes is the shortest Token ServeHTTP will serve under: the
// spec asks for at least 32 bytes of random material (§3.1.3).
const MinHTTPTokenBytes = 32

// NewCapabilityServer serves methods under serverInfo.name name.
func NewCapabilityServer(name string, methods ...*Method) (*CapabilityServer, error) {
	MakeDumpable()
	s := &CapabilityServer{name: name}
	if err := s.SetMethods(methods...); err != nil {
		return nil, err
	}
	return s, nil
}

// SetMethods replaces the methods the server advertises -- what a server
// whose methods depend on its configuration does from its configure
// handler. Grain re-lists the tools after every configure (§4.5.4).
func (s *CapabilityServer) SetMethods(methods ...*Method) error {
	tools := map[string]servedTool{}
	var order []string
	for _, m := range methods {
		for _, t := range m.tools() {
			if _, dup := tools[t.Name]; dup || t.Name == ConfigureTool || s.isPlain(t.Name) {
				return fmt.Errorf("tool %s is served twice", t.Name)
			}
			tools[t.Name] = t
			order = append(order, t.Name)
		}
	}
	s.mu.Lock()
	s.tools, s.order = tools, order
	s.mu.Unlock()
	return nil
}

// ConfigureFunc applies a server's configuration (§4.5.4). It must be
// idempotent: Grain calls it after every initialize and whenever the
// configuration changes. config has been validated against the schema
// by Grain and again here, and its secret fields hold their values: never
// log it. An error marks the server unhealthy in Grain, with the error's
// text as the reason.
type ConfigureFunc func(ctx context.Context, config map[string]any) error

// HandleConfigure makes the server advertise the configure tool, with
// schema as inputSchema.properties.config, and answer it with apply.
// schema must use only the keywords ValidateArgs enforces.
func (s *CapabilityServer) HandleConfigure(schema map[string]any, apply ConfigureFunc) error {
	norm, err := NormalizeArgsSchema(schema)
	if err != nil {
		return fmt.Errorf("configure: %v", err)
	}
	if err := checkSecretFields(norm); err != nil {
		return fmt.Errorf("configure: %v", err)
	}
	t := servedTool{
		Tool: Tool{
			Name:        ConfigureTool,
			Description: "Apply this server's configuration. Grain calls it after every initialize and whenever the configuration changes.",
			InputSchema: map[string]any{
				"type":       "object",
				"required":   []any{"config"},
				"properties": map[string]any{"config": norm},
			},
		},
		handle: func(ctx context.Context, in map[string]any) ([]byte, error) {
			raw, _ := in["config"].(map[string]any)
			cfg, err := validateObject("config", norm, raw)
			if err != nil {
				return nil, err
			}
			if err := apply(ctx, cfg); err != nil {
				return nil, err
			}
			return []byte(`{"status":"ok"}`), nil
		},
	}
	s.mu.Lock()
	s.configure = &t
	s.mu.Unlock()
	return nil
}

// ToolFunc answers one HandleTool tool: args are the call's arguments,
// checked against the tool's schema, and the result is a JSON object.
type ToolFunc func(ctx context.Context, args map[string]any) ([]byte, error)

// HandleTool makes the server advertise a tool that is not a capability
// method's mint_ or release_: a server Grain itself calls for something
// other than a grant (the host-git server, pkg/gitproxy). schema is the
// tool's arguments, in the keywords ValidateArgs enforces; a call whose
// arguments it refuses is answered as an error without reaching h.
func (s *CapabilityServer) HandleTool(name, description string, schema map[string]any, h ToolFunc) error {
	norm, err := NormalizeArgsSchema(schema)
	if err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.tools[name]; dup || name == ConfigureTool || s.plain[name].handle != nil {
		return fmt.Errorf("tool %s is served twice", name)
	}
	if s.plain == nil {
		s.plain = map[string]servedTool{}
	}
	s.plain[name] = servedTool{
		Tool: Tool{Name: name, Description: description, InputSchema: norm},
		handle: func(ctx context.Context, in map[string]any) ([]byte, error) {
			args, err := ValidateArgs(norm, in)
			if err != nil {
				return nil, err
			}
			return h(ctx, args)
		},
	}
	s.plainOrder = append(s.plainOrder, name)
	return nil
}

// isPlain reports whether HandleTool added name. Called with s.mu not
// held by the caller's goroutine for writing.
func (s *CapabilityServer) isPlain(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.plain[name]
	return ok
}

// listTools is what tools/list answers: configure first, then every
// method's tools, then HandleTool's.
func (s *CapabilityServer) listTools() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]Tool, 0, len(s.order)+len(s.plainOrder)+1)
	if s.configure != nil {
		list = append(list, s.configure.Tool)
	}
	for _, name := range s.order {
		list = append(list, s.tools[name].Tool)
	}
	for _, name := range s.plainOrder {
		list = append(list, s.plain[name].Tool)
	}
	return list
}

func (s *CapabilityServer) lookupTool(name string) (servedTool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if name == ConfigureTool && s.configure != nil {
		return *s.configure, true
	}
	if t, ok := s.plain[name]; ok {
		return t, true
	}
	t, ok := s.tools[name]
	return t, ok
}

// InProcessCaller returns a Caller that invokes s directly in-process with no
// stdio subprocess or HTTP listener: for grain's own in-process capability
// servers (design/design/unified-grants.design.md §1, §4).
func (s *CapabilityServer) InProcessCaller() Caller {
	return inProcessCaller{s: s}
}

type inProcessCaller struct{ s *CapabilityServer }

func (c inProcessCaller) ListTools(_ context.Context) ([]Tool, error) {
	return c.s.listTools(), nil
}

func (c inProcessCaller) CallTool(ctx context.Context, name string, arguments map[string]any) ([]byte, error) {
	t, ok := c.s.lookupTool(name)
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	return t.handle(ctx, arguments)
}

func (c inProcessCaller) Close() error { return nil }

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// handle answers one JSON-RPC message, or returns nil for a notification.
func (s *CapabilityServer) handle(ctx context.Context, line []byte) []byte {
	var req rpcMessage
	if err := json.Unmarshal(line, &req); err != nil {
		return rpcReply(json.RawMessage("null"), nil, &RPCError{Code: -32700, Message: "parse error"})
	}
	if len(req.ID) == 0 {
		return nil
	}
	switch req.Method {
	case "initialize":
		return rpcReply(req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.name, "version": "1"},
		}, nil)
	case "ping":
		return rpcReply(req.ID, map[string]any{}, nil)
	case "tools/list":
		return rpcReply(req.ID, map[string]any{"tools": s.listTools()}, nil)
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcReply(req.ID, nil, &RPCError{Code: -32602, Message: "invalid params"})
		}
		t, ok := s.lookupTool(p.Name)
		if !ok {
			return rpcReply(req.ID, nil, &RPCError{Code: -32602, Message: "unknown tool " + p.Name})
		}
		out, err := t.handle(ctx, p.Arguments)
		if err != nil {
			return rpcReply(req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
				"isError": true,
			}, nil)
		}
		return rpcReply(req.ID, map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(out)}},
			"structuredContent": json.RawMessage(out),
			"isError":           false,
		}, nil)
	}
	return rpcReply(req.ID, nil, &RPCError{Code: -32601, Message: "method not found: " + req.Method})
}

func rpcReply(id json.RawMessage, result any, rpcErr *RPCError) []byte {
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		msg["error"] = rpcErr
	} else {
		msg["result"] = result
	}
	b, err := json.Marshal(msg)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": &RPCError{Code: -32603, Message: err.Error()}})
	}
	return b
}

// ServeStdio answers newline-delimited JSON-RPC on r, writing to w, until r
// ends or ctx is done -- the stdio transport Grain spawns an in-tree or
// grain/config capability over.
func (s *CapabilityServer) ServeStdio(ctx context.Context, r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 1<<20)
	bw := bufio.NewWriter(w)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if out := s.handle(ctx, line); out != nil {
				bw.Write(out)
				bw.WriteByte('\n')
				if ferr := bw.Flush(); ferr != nil {
					return ferr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// ServeHTTP is a stateless Streamable HTTP endpoint: each POST carries one
// message and is answered with JSON. Every request must carry the bearer
// Token, and is answered 401 otherwise (§3.1.3). A server whose Token is
// unset, or shorter than MinHTTPTokenBytes, refuses every request: an HTTP
// capability server is never unauthenticated, loopback included (§1.3.2).
func (s *CapabilityServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if len(s.Token) < MinHTTPTokenBytes {
		http.Error(w, "unauthorized: this capability server has no token configured", http.StatusUnauthorized)
		return
	}
	want := "Bearer " + s.Token
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
		return
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, "reading request", http.StatusBadRequest)
		return
	}
	out := s.handle(r.Context(), body)
	if out == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}
