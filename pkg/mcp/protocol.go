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

// Package mcp is a small, self-contained port of grain/automation/mcp_server.py's
// hand-rolled MCP server: newline-delimited JSON-RPC 2.0 over a pair of
// io.Reader/io.Writer, with no dependency on an MCP SDK, mirroring v1's own
// choice not to take one on. It has two halves: Registry/Serve (the server)
// and Client (a minimal client for whatever process or in-process pipe is
// speaking the server side back at it).
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "grain-sandbox"
	serverVersion   = "0.1.0"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// nullID is the id of an answer to a request whose own id could not be
// read -- a line that did not parse, or one too long to keep. JSON-RPC 2.0
// says that id MUST be null, not absent: a client that matches answers to
// requests by id has nothing to match an answer with no id at all.
var nullID = json.RawMessage("null")

func mustMarshalLine(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// Every value passed to this ever came from our own handlers, never
		// from the wire, so a marshal failure here means a bug in this
		// package -- not something a caller can recover from meaningfully.
		panic(err)
	}
	return append(b, '\n')
}

// Serve reads newline-delimited JSON-RPC requests from r, dispatches each to
// registry, and writes any response to w -- the same framing v1's Python
// server uses (a bare readline()/write() loop, no Content-Length headers),
// which also happens to be real MCP stdio framing.
//
// ctx is handed to every registry.Handle call unchanged for the life of
// this Serve loop, not derived fresh per request -- callers that want a
// tool's own exec.CommandContext to die when a run is cancelled (see
// Tool.Handler's own doc comment) pass a per-run cancellable ctx here,
// exactly once, rather than Serve trying to guess one per line. A ctx
// whose only job is "until this process exits" (mcpserver.go's stdio
// server) can just pass context.Background(): killing that process is
// how that server is stopped, not cancelling this ctx.
func Serve(ctx context.Context, registry *Registry, r *bufio.Reader, w *bufio.Writer) error {
	return serve(ctx, registry, r, w, maxRequestLineBytes)
}

// serve is Serve with the request-line ceiling as a parameter, so a test
// can shrink it without writing a package global that every in-process
// server's Serve goroutine reads.
func serve(ctx context.Context, registry *Registry, r *bufio.Reader, w *bufio.Writer, maxLine int) error {
	for {
		line, tooLong, err := readRequestLine(r, maxLine)
		if tooLong {
			resp := mustMarshalLine(rpcResponse{
				JSONRPC: "2.0",
				ID:      nullID,
				Error:   &rpcError{Code: -32600, Message: fmt.Sprintf("invalid request: longer than %d bytes", maxLine)},
			})
			if _, werr := w.Write(resp); werr != nil {
				return werr
			}
			if ferr := w.Flush(); ferr != nil {
				return ferr
			}
		} else if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				if resp := registry.Handle(ctx, trimmed); resp != nil {
					if _, werr := w.Write(resp); werr != nil {
						return werr
					}
					if ferr := w.Flush(); ferr != nil {
						return ferr
					}
				}
			}
		}
		if err != nil {
			return err
		}
	}
}

// maxRequestLineBytes is the longest request line Serve will hold. Far
// above anything a client sends -- a write_file of a large file is the
// biggest -- and there only so that one endless line (anything that can
// reach a socket this is served on can send one) costs a bounded amount
// of memory rather than all of it.
const maxRequestLineBytes = 64 << 20

// readRequestLine is r.ReadBytes('\n') with a ceiling: a line longer than
// max is read to its end and discarded, and reported as tooLong, so the
// next line is still read as a line of its own.
func readRequestLine(r *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > max {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, tooLong, err
	}
}
