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

package agent_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// Every tool pkg/cli/mcpserver.go can register has to be named, or the
// run that holds it is refused its own tool at the first call: claude's
// --strict-mcp-config rejects what this list omits, and agy leaves it
// out of the roster the model can see. Both halves of grain CI's own set
// and both deployment-health readers are here because they were missing
// from both frameworks' copies of this list for as long as the copies
// existed -- task 145 lost a CI triage to exactly that, with
// wait_for_checks working (the GitHub-backed set names it) and get_ci_log
// refused beside it.
func TestPublishedToolNamesNamesEveryToolTheServerCanRegister(t *testing.T) {
	names := agent.PublishedToolNames()
	for _, want := range []string{
		// The sandbox roster and the escape hatches.
		"run_command", "read_file", "edit_file", "write_file",
		"ask_question", "request_secret", "comment_on_issue", "nothing_needed", "propose_task", "add_review_comment",
		// The CI loop, GitHub's half and grain's own.
		"pull_request_status", "base_branch_status", "wait_for_checks",
		"list_ci_runs", "get_ci_log", "get_ci_artifact", "run_ci_job",
		// What the daemon connection turns on.
		"open_pull_request", "recreate_sandbox", "update_status", "rename_session", "chat",
		// What the capability grants turn on.
		"list_grain_tasks", "read_grain_task", "read_grain_task_prompt", "read_grain_task_transcript",
		"read_grain_metrics",
		"read_grain_log",
		"read_deployment_health", "read_deployment_state",
		"list_bootstrap_playbooks", "read_bootstrap_playbook",
		"deployment_upgrade_status", "upgrade_deployment", "cancel_deployment_upgrade",
		"list_issues", "read_issue",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("PublishedToolNames() = %v, want %s among them", names, want)
		}
	}
}

// Bare names, as the server registers them. Each Framework qualifies
// them in its own spelling (mcp.QualifiedToolName for claude,
// mcp.AgyQualifiedToolName for agy), and a name that arrives here
// already qualified is qualified twice downstream -- which is how the
// four self-debug task readers came to be published to agy as
// "mcp_grain-sandbox_mcp__grain-sandbox__read_grain_task", a name
// nothing offers and the hook denies.
func TestPublishedToolNamesAreBare(t *testing.T) {
	for _, name := range agent.PublishedToolNames() {
		if strings.HasPrefix(name, "mcp_") {
			t.Errorf("PublishedToolNames() contains %q, which is already qualified", name)
		}
	}
}

// No name twice. wait_for_checks is registered twice on purpose -- grain
// CI's replaces the GitHub-backed one in place for a run holding that
// grant -- and propose_task likewise for an interactive run, so a list
// built from the constructors sees both and must publish each name once.
func TestPublishedToolNamesRepeatsNothing(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range agent.PublishedToolNames() {
		if seen[name] {
			t.Errorf("PublishedToolNames() names %q twice", name)
		}
		seen[name] = true
	}
}

// The slice is the caller's to modify: agent/claude rewrites it in place
// into its own spelling, so handing out a shared backing array would let
// one call change what the next one reads.
func TestPublishedToolNamesHandsOutAFreshSlice(t *testing.T) {
	first := agent.PublishedToolNames()
	if len(first) == 0 {
		t.Fatal("PublishedToolNames() is empty")
	}
	first[0] = "clobbered"
	if second := agent.PublishedToolNames(); second[0] == "clobbered" {
		t.Errorf("PublishedToolNames() = %v, want a fresh slice per call", second)
	}
}
