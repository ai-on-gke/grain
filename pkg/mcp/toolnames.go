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

import "strings"

// ToolNamespace is the mcpServers key both agent frameworks write into
// their CLI's MCP configuration (agent/claude's --mcp-config,
// agent/antigravity's settings file) -- matching v1's own "grain-sandbox"
// naming (dispatch.py's _mcp_config_json) purely for continuity; neither
// CLI cares what this string is.
//
// It is not protocol.go's serverName, which is what this server calls
// itself in the initialize handshake. This is what the client filed the
// server under, and the two have never had to agree.
const ToolNamespace = "grain-sandbox"

// QualifiedToolName renders a tool this package registers the way a CLI
// that loaded it from an MCP config reports and admits it:
// "mcp__<namespace>__<tool>". Both frameworks' allowedTools build their
// --allowedTools list with it.
func QualifiedToolName(tool string) string {
	return "mcp__" + ToolNamespace + "__" + tool
}

// AgyQualifiedToolName is QualifiedToolName in the other spelling a CLI
// uses for the same tool: "mcp_<namespace>_<tool>", with single
// underscores. That is what the Antigravity CLI (agy) names an MCP tool
// it registered eagerly ("Eagerly loaded tools are registered as native
// tools under the name `mcp_<server>_<tool>`", agy 1.1.25), and it is not
// a variant of the claude spelling above -- it is a different string that
// has to be recognized on its own, or every call an agy run makes reaches
// BareToolName unchanged and matches nothing downstream.
//
// Only agent/antigravity has any use for it; it lives here beside its
// sibling so the pair that BareToolName has to undo is written down in
// one place.
func AgyQualifiedToolName(tool string) string {
	return "mcp_" + ToolNamespace + "_" + tool
}

// BareToolName undoes QualifiedToolName and AgyQualifiedToolName both:
// given whatever name a CLI reported a call under, it returns the name
// this package actually registered the tool as. A name carrying neither
// prefix -- a CLI's own native tool, or a framework whose runtime calls
// tools in-process -- comes back unchanged.
//
// Every Framework must put a call's name through this before recording it
// as an agent.ToolCall, and agent.ToolCall.Name's own doc comment says so.
// The prefix is a detail of how a CLI namespaces the MCP servers it
// loaded, not part of the tool's identity, and a consumer downstream of
// agent.Result matches on the identity: orchestrator.ProcessResult decides
// whether a run asked a question, left a closing comment, or proposed a
// task by comparing against "ask_question", "comment_on_issue" and
// "propose_task" exactly. It compared against names that arrived
// "mcp__grain-sandbox__"-prefixed and so never matched any of them, which
// silently cost every real run its question and its closing comment: the
// run was recorded no_action, and the human who was supposed to be asked
// was never asked. Every test of that path scripted the bare name, which
// is why nothing caught it.
//
// Both spellings are stripped rather than only claude's, because the same
// failure happened a second time with agy: it names an eagerly registered
// MCP tool "mcp_grain-sandbox_ask_question", which is not the string this
// function used to trim, so it came back unchanged and ProcessResult
// matched nothing -- the identical silent loss, through a different CLI.
// The two prefixes cannot be confused for one another: a name beginning
// "mcp__grain-sandbox__" does not begin "mcp_grain-sandbox_", so trimming
// both in turn removes exactly one of them.
func BareToolName(reported string) string {
	bare := strings.TrimPrefix(reported, "mcp__"+ToolNamespace+"__")
	return strings.TrimPrefix(bare, "mcp_"+ToolNamespace+"_")
}

// capabilityForTool names the capability a run has to hold for a tool
// grain only serves behind a grant to be on its roster at all
// (pkg/cli/mcpserver.go's grantedDaemonTools, and
// orchestrator.Config.GrantTools).
//
// It exists for one message: what a run is told when it calls one of
// these and has not been granted it. "unknown tool upgrade_deployment"
// reads as a tool grain does not have, which is wrong and sends the
// agent looking for another way to do the job; "this task does not hold
// self-upgrade" is the truth, and it is something the agent can put in
// its closing note for a human to act on.
//
// Only grant-gated tools belong here. A tool absent for any other reason
// -- a run with no daemon to ask, a deployment with nothing configured
// -- is on the roster and refuses the call with a sentence of its own,
// which is a better answer than this one and the reason those tools are
// always registered.
var capabilityForTool = map[string]string{
	// What self-debug opens (pkg/cli/mcpserver.go registers all of these
	// only for a run holding the grant): the task readers, and
	// read_grain_metrics for this deployment's own measurements. A run that
	// asks one of these without the grant is asking a reasonable question
	// and should be told which box to have ticked, not that grain cannot
	// answer it.
	"list_grain_tasks":           "self-debug",
	"read_grain_task":            "self-debug",
	"read_grain_task_prompt":     "self-debug",
	"read_grain_task_transcript": "self-debug",
	"read_pipeline_changes":      "self-debug",
	"read_grain_metrics":         "self-debug",
	"read_grain_log":             "grain-log",
	"list_bootstrap_playbooks":   "bootstrap-playbooks",
	"read_bootstrap_playbook":    "bootstrap-playbooks",
	"read_deployment_health":     "deployment-health",
	"read_deployment_state":      "deployment-health",
	"run_host_command":           "self-repair",
	"update_memory":              "memory",
	"read_memory_document":       "memory",
	"list_memory_documents":      "memory",
	"deployment_upgrade_status":  "self-upgrade",
	"upgrade_deployment":         "self-upgrade",
	"cancel_deployment_upgrade":  "self-upgrade",
	"pause_pipeline":             "pipeline-control",
	"resume_pipeline":            "pipeline-control",
	"run_task_now":               "dispatch-control",
	"defer_task":                 "dispatch-control",
	"list_issues":                "read-issues",
	"read_issue":                 "read-issues",
}

// CapabilityForTool names the capability that puts tool on a run's
// roster, and reports false for every tool a run gets without one.
//
// Exported so that the place a grant is actually wired up can check the
// two agree (cmd/grain's own tests): a tool renamed here and not there
// would silently go back to being answered as a tool grain does not
// have.
func CapabilityForTool(tool string) (string, bool) {
	capability, ok := capabilityForTool[tool]
	return capability, ok
}

// unknownToolMessage is what a call names no registered tool is answered
// with: the name, and -- for a tool that is only ever served behind a
// grant -- which capability this task would have to hold for it to be
// here.
func unknownToolMessage(name string) string {
	if capability, ok := capabilityForTool[name]; ok {
		return "unknown tool " + name + ": this run does not hold the \"" + capability +
			"\" capability, which is what puts that tool on a run's roster. Nothing you can do " +
			"from inside this run will add it -- a human attaches it to the task. Say so if it " +
			"matters to what you were asked to do."
	}
	return "unknown tool " + name
}
