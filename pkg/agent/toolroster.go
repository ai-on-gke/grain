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
	"strings"
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
	// A changelist task's checks tools, registered in place of the
	// pull-request and grain CI tools of the same names.
	add(mcp.NewChangeChecksTools(nil))
	// And its review-thread tools, in place of the pull-request ones.
	add(mcp.NewChangeReviewTools(nil))
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

// The two surfaces of a run with a sandbox
// (docs/design/sandbox-first-agent-surface.md §4-§6).
//
// `grain mcpserver` serves the model only its control set: the four
// sandbox tools, the tools that end, park or escalate a run, and the
// dangerous grants' own tools, none of which a process inside the
// sandbox may reach. Everything else PublishedToolNames lists is a
// `grain` CLI command the agent runs inside run_command, and the
// sandbox proxy records each of those calls in the census under the
// tool name it replaced (gitproxy.RouteActivity) -- which is what lets
// read_grain_metrics tell the two surfaces apart by name.

// controlTools is the run-ending, parking and escalation tools every run
// with a daemon keeps as model tool calls. write_handover and
// add_review_comment are NewMockTools' too, and are not here: they are
// `grain handover write` and `grain pr review-comment`.
var controlTools = map[string]bool{
	"ask_question":     true,
	"request_secret":   true,
	"comment_on_issue": true,
	"nothing_needed":   true,
	"propose_task":     true,
	"wait_for_task":    true,
	"resume_later":     true,
	"recreate_sandbox": true,
	"chat":             true,
	"rename_session":   true,
}

// SandboxToolNames is the four tools that cross from the agent CLI into
// the sandbox: run_command, read_file, write_file and edit_file.
func SandboxToolNames() []string {
	var names []string
	for _, t := range mcp.NewHostSandboxTools("", nil) {
		names = append(names, t.Name)
	}
	for _, t := range mcp.NewSSHSandboxTools(nil, "") {
		names = append(names, t.Name)
	}
	return dedupeToolNames(names)
}

// IsControlTool reports whether name is one of the run-ending, parking
// or escalation tools every run keeps on its MCP roster.
func IsControlTool(name string) bool { return controlTools[name] }

// ModelOnlyGrantToolNames is the tools of the grants a sandbox process
// must never reach -- dispatch-control, pipeline-control, self-upgrade,
// db-write and team-broadcast -- which stay on the MCP roster of a run
// holding them. db-read's query_grain_db is `grain db query`, so only
// execute_grain_db is here.
func ModelOnlyGrantToolNames() []string {
	var names []string
	add := func(tools []mcp.Tool) {
		for _, t := range tools {
			names = append(names, t.Name)
		}
	}
	add(dispatchcontrol.Tools(dispatchcontrol.Config{}))
	add(pipelinecontrol.Tools(pipelinecontrol.Config{}))
	add(selfupgrade.Tools(selfupgrade.Config{}))
	add(mcp.NewBroadcastTools(nil))
	names = append(names, "execute_grain_db")
	return dedupeToolNames(names)
}

var (
	publishedControlMu    sync.RWMutex
	publishedControlNames []func() []string
)

// RegisterPublishedControlToolNames adds names to PublishedControlToolNames,
// for the model-only tools a package this one cannot import generates --
// the agent API routes the sandbox channel refuses (pkg/ui).
func RegisterPublishedControlToolNames(names func() []string) {
	publishedControlMu.Lock()
	defer publishedControlMu.Unlock()
	publishedControlNames = append(publishedControlNames, names)
}

// PublishedControlToolNames is every name `grain mcpserver` may serve on
// its control roster: the sandbox tools, the control tools, the
// model-only grants' tools and the model-only agent API routes.
func PublishedControlToolNames() []string {
	names := SandboxToolNames()
	for _, name := range PublishedToolNames() {
		if controlTools[name] {
			names = append(names, name)
		}
	}
	names = append(names, ModelOnlyGrantToolNames()...)
	publishedControlMu.RLock()
	for _, more := range publishedControlNames {
		names = append(names, more()...)
	}
	publishedControlMu.RUnlock()
	return dedupeToolNames(names)
}

// PublishedCLICommandNames is every published tool that is a `grain` CLI
// command on a run with a sandbox, named as the census records it: what
// PublishedToolNames lists and PublishedControlToolNames does not.
// Capability tools a sandbox calls through `grain cap` are recorded as
// cap__<server>__<tool> and are not listed (ToolSurface knows them).
func PublishedCLICommandNames() []string {
	control := map[string]bool{}
	for _, name := range PublishedControlToolNames() {
		control[name] = true
	}
	var names []string
	for _, name := range PublishedToolNames() {
		if !control[name] {
			names = append(names, name)
		}
	}
	return names
}

// Tool surfaces, as ToolSurface reports them.
const (
	SurfaceControl = "control"
	SurfaceCLI     = "cli"
)

var (
	surfaceOnce  sync.Once
	surfaceNames map[string]string
)

// ToolSurface is which surface a census tool name belongs to:
// SurfaceControl for the MCP control roster, SurfaceCLI for a `grain`
// command the sandbox proxy recorded, and "" for a name grain does not
// publish (a framework's own built-in tool).
func ToolSurface(name string) string {
	surfaceOnce.Do(func() {
		surfaceNames = map[string]string{}
		for _, n := range PublishedCLICommandNames() {
			surfaceNames[n] = SurfaceCLI
		}
		for _, n := range PublishedControlToolNames() {
			surfaceNames[n] = SurfaceControl
		}
	})
	if s, ok := surfaceNames[name]; ok {
		return s
	}
	if strings.HasPrefix(name, "cap__") {
		return SurfaceCLI
	}
	return ""
}
