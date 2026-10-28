package mcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/procgroup"
)

// ManagedHTTP owns one run's stdio MCP child and its authenticated HTTP
// endpoint. The agent CLI is only an HTTP client: it needs no local
// execution environment to start or stop this server.
type ManagedHTTP struct {
	URL   string
	Token string
	Tools []ToolInfo
	close func()
}

func (s *ManagedHTTP) Close() {
	if s.close != nil {
		s.close()
	}
}

// StartCommandHTTP adapts Grain's existing stdio server without changing
// Claude/AGY's transport or duplicating the sandbox/grant configuration.
// Tools are snapshotted at startup and calls are serialized by Client,
// exactly as they are on the underlying stdio connection.
//
// The endpoint is on loopback, named by the address it bound to, and
// reachable only from this machine: the CLI it serves is a child of this
// same process.
func StartCommandHTTP(ctx context.Context, binary string, args []string) (*ManagedHTTP, error) {
	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, binary, args...)
	procgroup.Prepare(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		cancel()
		return nil, err
	}
	// Do not capture unbounded output or relay controller credentials from
	// a failing child into an agent-visible error.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		cancel()
		return nil, fmt.Errorf("mcp: starting Grain server: %w", err)
	}
	client := NewClient(stdin, stdout, stdin)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); cancel(); close(done) }()
	var once sync.Once
	var server *http.Server
	stop := func() {
		once.Do(func() {
			cancel()
			if server != nil {
				_ = server.Close()
			}
			_ = client.Close()
			_ = stdout.Close()
			<-done
		})
	}
	// A server that never answers startup must not hang a dispatch.
	timer := time.AfterFunc(30*time.Second, cancel)
	tools, err := client.ListTools(runCtx)
	timer.Stop()
	if err != nil {
		stop()
		return nil, fmt.Errorf("mcp: Grain server startup failed: %w", err)
	}
	registry := NewRegistry()
	// Relayed verbatim, and bounded twice as a consequence: the child is
	// itself a Registry, so its answer arrives already cut at
	// maxResultTextBytes with its own notices on top, and an answer that
	// large is cut a second time here. That costs the middle of a notice
	// in the one case where a tool answered with more than a run can
	// carry, which is better than the alternative of exempting a whole
	// transport from the bound -- what a relay carries is whatever the
	// server on the other end said, and here that server happens to be
	// grain's own rather than by construction.
	for _, info := range tools {
		registry.Register(Tool{Name: info.Name, Description: info.Description, InputSchema: info.InputSchema,
			Handler: func(ctx context.Context, args map[string]any) Result {
				result, err := client.CallTool(ctx, info.Name, args)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				return Result{Text: result.Text(), IsError: result.IsError}
			},
		})
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		stop()
		return nil, err
	}
	authority := listener.Addr().String()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		listener.Close()
		stop()
		return nil, err
	}
	token := hex.EncodeToString(secret[:])
	server = &http.Server{
		Handler:           streamableHTTPHandler(registry, token, authority),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return runCtx },
	}
	go func() { _ = server.Serve(listener); cancel() }()
	go func() { <-runCtx.Done(); stop() }()
	return &ManagedHTTP{URL: "http://" + authority + "/mcp", Token: token, Tools: tools, close: stop}, nil
}

// Stateless Streamable HTTP: JSON POST responses, no optional GET/SSE
// stream or shared sessions. Each endpoint already represents one run.
// Reject browser origins and unexpected Host headers as defense in depth
// against cross-origin requests and DNS rebinding; every request also
// requires a cryptographically random per-run bearer token.
//
// host is the authority the endpoint bound to: a request has to name the
// address this run's CLI was actually given.
func streamableHTTPHandler(registry *Registry, token, host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != host || r.Header.Get("Origin") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
			http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		if err != nil {
			http.Error(w, "invalid request body", http.StatusRequestEntityTooLarge)
			return
		}
		var req rpcRequest
		if json.Unmarshal(body, &req) != nil || req.JSONRPC != "2.0" || req.Method == "" {
			http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
			return
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		response := registry.Handle(r.Context(), body)
		if req.Method == "initialize" {
			// HTTP transport was introduced after the stdio server's
			// original version. Our tools need no newer capabilities.
			response = []byte(strings.Replace(string(response), `"protocolVersion":"2024-11-05"`, `"protocolVersion":"2025-03-26"`, 1))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response)
	})
}
