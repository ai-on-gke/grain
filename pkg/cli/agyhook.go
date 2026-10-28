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

package cli

import (
	"os"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
)

// agyToolHook is the "agy-tool-hook" subcommand: agy's own PreToolUse
// hook, which is to say the thing agy runs before every tool call a run
// makes and asks whether that call may proceed. It reads the call on
// stdin and writes the decision on stdout, which is the contract agy
// documents (pkg/agent/antigravity's hookConfigJSON quotes it).
//
// It is grain forking itself rather than a script agy's config points at,
// for the reason mcpserver is: the hook must be some executable named in
// a config file written per run, and the one executable a deployment is
// guaranteed to have is the grain binary already running. A script would
// have to be installed, found and kept in step with the tool names in
// pkg/agent/antigravity; a subcommand is compiled from them.
//
// Nothing here decides anything. antigravity.HookDecision holds the
// policy and the tests that cover it, because that package owns the list
// of agy tools grain withholds and is where a reader looking for the
// policy would go.
//
// It never exits non-zero, and it writes nothing at all for a call it has
// no opinion about. Both are load-bearing rather than tidy: measured
// against agy 1.1.26, a hook that exits non-zero fails the tool call
// ("JSON hook ... failed: command failed: exit status N") and so does one
// whose stdout is not JSON, while an empty stdout is the one reply that
// leaves the call exactly as agy would have handled it. An empty JSON
// object is not that reply -- agy reads a decision-less object as a deny
// -- which is why nothing here invents a payload. See
// antigravity.noOpinion for the whole measured contract.
//
// So a payload this binary cannot parse, or a shape agy has not promised,
// costs a run nothing: the write below is zero bytes and the tool runs.
//
// Its arguments are what antigravity's hookConfigJSON wrote for this run:
// none at all for an ordinary one, and for a run holding a host-tool
// grant (antigravity.HostToolGrant) the grant, where to record the
// host-tool calls it admits, and the task and run to record them against
// (antigravity.ParseHookArgs). They can only switch on an allowlist
// compiled into whichever package registered that grant, never name a
// tool into it. The grants themselves are registered from init() by the
// packages this binary links, which is why the hook is this binary and
// not another: it holds the same registrations as the daemon that wrote
// its arguments.
func agyToolHook(args []string) {
	antigravity.ServeHook(args, os.Stdin, os.Stdout, os.Stderr)
}
