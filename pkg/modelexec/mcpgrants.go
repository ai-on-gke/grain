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

package modelexec

import (
	"encoding/json"
	"sync"
)

// An MCP tunnel stream (StreamTypeMCP) asks this process to run `grain
// mcpserver` with the arguments in its header, and those arguments decide
// everything that server can reach: -sandbox-root names a directory on the
// controller that run_command will execute bash in, -container and
// -granule-vm name sandboxes that are not the executor's own, and -data-dir,
// -pr-repo, -server and -task decide whose credentials and whose task its
// tools act on. The header is written by the executor, which is a separate
// machine grain does not trust with any of that -- so its arguments are never
// taken on its word. A remote executor only ever starts an mcpserver because
// a Framework on this side told it to, through an agent.Host whose Run and
// WriteFile carry an argv that Framework built; mcpGrants is the record of
// exactly those argvs, per executor connection, and a stream whose header names anything
// else is refused rather than served.

// maxMCPGrantsPerHost bounds how many issued argvs are remembered for one
// executor connection. Every run's argv differs (it carries the run's
// deadline), so the set would otherwise grow for as long as a machine stays
// connected; the oldest are forgotten first, and only a run that has been
// superseded by this many newer ones on the same connection could lose its
// grant.
const maxMCPGrantsPerHost = 1024

// mcpGrants is the argvs issued to one executor connection. It lives on
// that connection's hostConn rather than in a table keyed by the executor's
// instance id: the id is the executor's own word in its hello, and anybody
// holding an executor token -- another team's included -- can claim one
// already in use. Grants keyed by it followed the id to whoever claimed it
// last, and replaying a run's argv got that run's mcpserver. A run's
// commands, and so its mcpserver's tunnel, only ever travel on the
// connection it was dispatched over, so nothing needs a grant to outlive
// that connection either, and they are dropped with it.
type mcpGrants struct {
	mu    sync.Mutex
	set   map[string]struct{}
	order []string
}

// mcpGrantKey is argv as the executor forwards it: the arguments after the
// "mcpserver" subcommand name (cmd/grain/main.go hands mcpserver args[1:],
// and tunnelMCPServer sends those on unchanged). It is encoded as JSON
// rather than joined on a separator: the header is JSON the executor wrote,
// so an element of it can hold any byte a separator could be, and two
// different argvs must never share a key.
func mcpGrantKey(args []string) string {
	if len(args) > 0 && args[0] == "mcpserver" {
		args = args[1:]
	}
	if args == nil {
		args = []string{}
	}
	key, _ := json.Marshal(args)
	return string(key)
}

func (g *mcpGrants) grant(args []string) {
	key := mcpGrantKey(args)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.set == nil {
		g.set = make(map[string]struct{})
	}
	if _, ok := g.set[key]; ok {
		return
	}
	g.set[key] = struct{}{}
	g.order = append(g.order, key)
	for len(g.order) > maxMCPGrantsPerHost {
		delete(g.set, g.order[0])
		g.order = g.order[1:]
	}
}

func (g *mcpGrants) allowed(args []string) bool {
	if len(args) == 0 {
		return false
	}
	key := mcpGrantKey(args)
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.set[key]
	return ok
}
