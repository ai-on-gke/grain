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

package agent

// The one list of every tool name `grain mcpserver` can advertise.
//
// Both CLI-driven Frameworks need it, and each needs it in its own
// spelling: agent/claude passes it to `--allowedTools`, where
// --strict-mcp-config admits a tool only if this list names it *and* the
// server offers it, and agent/antigravity writes it into the MCP config
// agy registers eagerly from, where a tool left off is reachable only
// through the call_mcp_tool fallback. Either way the list is a filter
// over what the server actually registers, never an addition to it.
//
// It lives here rather than in each Framework because it was in each
// Framework, twice, and the two copies drifted -- which is not a
// hypothetical: grain CI's own get_ci_log and list_ci_runs and the
// deployment-health pair were in neither, so a run that held those
// grants was refused its own tools at the first call, and the four
// self-debug task readers were in antigravity's copy under the wrong
// spelling, which is the same refusal by another route. A list two
// packages maintain separately is a list that disagrees with itself
// eventually; this one is computed from the constructors, so the only
// way to be missing from it is to be registered by something this file
// does not call.

import (
	"sync"

	"github.com/ai-on-gke/grain/pkg/capability/bootstrap"
	"github.com/ai-on-gke/grain/pkg/capability/dbaccess"
	"github.com/ai-on-gke/grain/pkg/capability/deploymenthealth"
	"github.com/ai-on-gke/grain/pkg/capability/dispatchcontrol"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/capability/pipelinecontrol"
	"github.com/ai-on-gke/grain/pkg/capability/selfupgrade"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// PublishedToolNames names the exact tools pkg/cli/mcpserver.go may
// register, bare, in the order it registers them and with no name
// repeated -- computed from those constructors directly rather than
// hand-copied, so it can never drift from what that subcommand actually
// advertises the way v1's hand-maintained _ALLOWED_TOOLS constant could
// (dispatch.py).
//
// Every name is listed unconditionally, including tools this run's own
// mcpserver will not register: the ones that need a daemon to talk to
// (-server/-task), the ones that need a person sitting with the task
// (-interactive), and the ones that need a capability grant (-grant).
// This list is a property of the tool vocabulary, not of one run's
// configuration -- naming a tool that is not there costs nothing, while
// a run that does have a tool and cannot name it here loses it.
//
// The nil arguments below are collaborators no run ever gets -- a nil
// reader, a nil store, an empty source directory. Nothing here is
// called; this only wants the names.
func PublishedToolNames() []string {
	var names []string
	add := func(tools []mcp.Tool) {
		for _, t := range tools {
			names = append(names, t.Name)
		}
	}

	// The sandbox roster and the escape hatches: what every run has.
	// The SSH roster is the same four names run against a remote host
	// instead of a local directory -- what a granule VM or a container
	// sandbox is given (pkg/cli/mcpserver.go's -granule-vm and
	// -container) -- and it is called here rather than assumed identical
	// to the local four, so that a tool added to one of the two rosters
	// and not the other is still published.
	add(mcp.NewHostSandboxTools("", nil))
	add(mcp.NewSSHSandboxTools(nil, ""))
	add(mcp.NewMockTools(&mcp.MockSink{}, nil))

	// The CI loop. pull_request_status, base_branch_status and
	// wait_for_checks come from the GitHub-backed set; grain CI's own
	// set re-registers wait_for_checks under the same name for a run
	// holding the grain-ci grant and adds the two tools that read what
	// it produced, which is why get_ci_log and list_ci_runs have to be
	// named here too.
	add(mcp.NewPullRequestTools(nil, nil, mcp.PullRequestScope{}))
	add(mcp.NewGrainCIWaitTools(nil, mcp.CIRepoScope{}))
	add(mcp.NewRunCIJobTools(nil, mcp.CIRepoScope{}))
	add(mcp.NewGrainCIArtifactTools(nil, mcp.CIRepoScope{}, ""))
	add(mcp.NewNamespaceTools(mcp.NamespaceToolsConfig{}))

	// The tools that need the daemon this run's mcpserver was given.
	add(mcp.NewOpenPullRequestTools(nil))
	add(mcp.NewRecreateSandboxTools(nil))
	add(mcp.NewStatusTools(nil))
	// rename_session is registered only for a task that is a standing
	// chat session, and listed unconditionally for the reason every
	// daemon-gated tool above is.
	add(mcp.NewRenameSessionTools(nil))
	add(mcp.NewWaitForTaskTools(nil, nil))
	add(mcp.NewResumeLaterTools(nil, nil))
	add(mcp.NewNotifyTools(nil))
	add(mcp.NewBroadcastTools(nil))
	add(mcp.NewMergeQueueTools(nil))
	add(mcp.NewRevertTools(nil))
	add(mcp.NewMemoryTools(nil))
	add(mcp.NewReleaseNotesTools(nil))
	add(mcp.NewQuarantineTools(mcp.QuarantineToolConfig{}))

	// chat needs that daemon and a task somebody is sitting with; the
	// live proposal tools need the daemon and register propose_task over
	// the escape hatch of that name, adding nothing new today.
	// Called anyway, for the same reason the SSH roster is: what this
	// list owes is every name a constructor the server registers can
	// produce, not every name somebody remembered was new.
	add(mcp.NewChatTools(nil, nil))
	add(mcp.NewLiveProposalTools(nil, nil, "", nil))

	// The capability grants' own tools.
	add(mcp.NewIssueTools(nil, mcp.IssueScope{}))
	add(mcp.NewTaskTools(nil))
	add(mcp.NewPipelineTools(nil))
	add(mcp.NewMetricsTools(nil))
	// The zero Configs are tools that would refuse every call -- no
	// redactor, no sources, no state directory -- which is all this
	// wants: the names.
	add(grainlog.Tools(grainlog.Config{}))
	add(deploymenthealth.Tools(deploymenthealth.Config{}))
	add(selfupgrade.Tools(selfupgrade.Config{}))
	add(pipelinecontrol.Tools(pipelinecontrol.Config{}))
	add(dispatchcontrol.Tools(dispatchcontrol.Config{}))
	add(bootstrap.PlaybookTools())
	add(dbaccess.Tools(dbaccess.Config{AllowWrite: true}))

	// The agent API's generated tools, one per scoped route, which the
	// package that owns the route table registers (pkg/ui imports this
	// one, so it cannot be called from here).
	publishedMu.RLock()
	for _, more := range publishedToolNames {
		names = append(names, more()...)
	}
	publishedMu.RUnlock()

	return dedupeToolNames(names)
}

var (
	publishedMu        sync.RWMutex
	publishedToolNames []func() []string
)

// RegisterPublishedToolNames adds names to PublishedToolNames: for tools
// a package this one cannot import registers -- the agent API's, which
// are generated from pkg/ui's route table (ui.AgentAPIToolNames). It is
// meant to be called from init().
func RegisterPublishedToolNames(names func() []string) {
	publishedMu.Lock()
	defer publishedMu.Unlock()
	publishedToolNames = append(publishedToolNames, names)
}

// dedupeToolNames keeps the first appearance of each name and drops the
// rest. One name can be registered twice on purpose -- grain CI's
// wait_for_checks replaces the GitHub-backed one in place for a run that
// holds the grant (mcp.Registry.Register), and the live propose_task
// replaces the escape hatch the same way -- and a filter naming a tool
// twice is at best noise in a command line and at worst a config key
// written over itself.
func dedupeToolNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := names[:0]
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}
