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

package daemon

import (
	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The two rosters `grain mcpserver` can serve (-mcp-surface;
// docs/design/sandbox-first-agent-surface.md §6).
const (
	// MCPSurfaceControl is the sandbox tools, the tools that end, park or
	// escalate a run, and the tools of the grants a sandbox process must
	// never reach. Everything else is a `grain` command inside the
	// sandbox, through the sandbox proxy.
	MCPSurfaceControl = "control"
	// MCPSurfaceFull is every tool the run's grants reach, as MCP tools:
	// for a client that cannot shell out, and for tests.
	MCPSurfaceFull = "full"
)

// MCPSurfaceEnv names the environment variable a server reads when it is
// given no -mcp-surface: how a test harness that drives runs end to end,
// with mcpserver started many layers below it, asks for the full roster
// its scripted agents call.
const MCPSurfaceEnv = "GRAIN_MCP_SURFACE"

// ResolveMCPSurface is the roster a server serves: the one -mcp-surface
// names, or, left empty, the control set when the run has a sandbox for
// `grain` to run in and the full roster when it has none.
func ResolveMCPSurface(flag string, hasSandbox bool) string {
	if flag != "" {
		return flag
	}
	if hasSandbox {
		return MCPSurfaceControl
	}
	return MCPSurfaceFull
}

// controlRoster is the names a control-surface server keeps out of
// everything it registered. Registration is already gated on grants and
// on a daemon to ask, so this only names what may stay; a name here that
// was never registered costs nothing.
//
// Local scripts stay: they are the deployment's own tools, run on the
// controller, with no sandbox command of their own. A changelist task
// keeps pull_request_status and wait_for_checks, which answer from the
// CL's presubmit rather than from grain CI, which is all `grain ci wait`
// and `grain pr status` read. It keeps open_pull_request,
// reply_to_pull_request_comment and resolve_review_thread too, which its
// thread wakes tell it to call: `grain pr open` cannot carry the CL's
// summary and body, and `grain pr reply`/`resolve` take a GitHub comment
// number, never a CL thread's cl/<n>:<id>. So does a run without
// grain-ci keep the checks tools, for the same reason: its checks are
// GitHub's, which only the MCP tools read.
func controlRoster(sandbox, local []mcp.Tool, grants GrantNames, changelist bool) map[string]bool {
	keep := map[string]bool{}
	for _, t := range sandbox {
		keep[t.Name] = true
	}
	for _, t := range local {
		keep[t.Name] = true
	}
	for _, name := range agent.PublishedToolNames() {
		if agent.IsControlTool(name) {
			keep[name] = true
		}
	}
	for _, name := range agent.ModelOnlyGrantToolNames() {
		keep[name] = true
	}
	for _, name := range ui.ModelOnlyAgentAPIToolNames(grants) {
		keep[name] = true
	}
	if changelist || !grants.Has(grainci.CapabilityName) {
		keep["pull_request_status"] = true
		keep["wait_for_checks"] = true
	}
	if changelist {
		keep["open_pull_request"] = true
		keep["reply_to_pull_request_comment"] = true
		keep["resolve_review_thread"] = true
	}
	if !changelist && !grants.Has(grainci.CapabilityName) {
		keep["base_branch_status"] = true
	}
	return keep
}
