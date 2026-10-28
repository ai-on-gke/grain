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

package mcp

import (
	"bufio"
	"context"
	"io"
)

// NewInProcess connects a Client to registry over a pair of in-memory
// pipes, with Serve running in its own goroutine -- the same protocol a
// real subprocess would speak over its stdin/stdout, just without paying
// for a fork/exec. This is what the scripted agent seam uses (see
// agent/antigravity's testing.go): the
// model's only route to any tool is through this Client, and the Client's
// only route to anything is through registry, which is what makes "Gemini
// can only take actions within the sandbox using the MCP tools" true by
// construction rather than by convention.
//
// ctx is Serve's own ctx (see its doc comment) -- a caller that builds one
// NewInProcess per agent.Framework.Run call
// should pass that Run's own ctx, so cancelling it reaches every tool call
// this Client ever drives against registry, including one already in
// flight.
//
// A real subprocess remains available for cases that want an actual process
// boundary (see mcp/cmd/mcpserver) -- NewClient works identically over its
// Stdin/Stdout pipes.
func NewInProcess(ctx context.Context, registry *Registry) *Client {
	clientReadsFromServer, serverWritesToClient := io.Pipe()
	serverReadsFromClient, clientWritesToServer := io.Pipe()

	go func() {
		_ = Serve(ctx, registry, bufio.NewReader(serverReadsFromClient), bufio.NewWriter(serverWritesToClient))
		serverWritesToClient.Close()
	}()

	return NewClient(clientWritesToServer, clientReadsFromServer, clientWritesToServer)
}
