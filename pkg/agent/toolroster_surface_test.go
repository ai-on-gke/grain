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

import (
	"slices"
	"testing"
)

// The two surfaces partition PublishedToolNames: every published tool is
// on exactly one of them, and the ones the design names are where it
// puts them (docs/design/sandbox-first-agent-surface.md §1, §6).
func TestPublishedSurfacesPartitionTheRoster(t *testing.T) {
	control := PublishedControlToolNames()
	cli := PublishedCLICommandNames()
	for _, name := range PublishedToolNames() {
		in := slices.Contains(control, name)
		if in == slices.Contains(cli, name) {
			t.Errorf("%q is on both surfaces or neither (control %v)", name, in)
		}
	}
	for _, name := range []string{"run_command", "edit_file", "ask_question", "comment_on_issue",
		"nothing_needed", "propose_task", "wait_for_task", "resume_later", "recreate_sandbox", "chat",
		"execute_grain_db"} {
		if ToolSurface(name) != SurfaceControl {
			t.Errorf("ToolSurface(%q) = %q, want %q", name, ToolSurface(name), SurfaceControl)
		}
	}
	for _, name := range []string{"wait_for_checks", "get_ci_log", "grain_read", "update_status",
		"write_handover", "add_review_comment", "query_grain_db", "cap__widgets__ping"} {
		if ToolSurface(name) != SurfaceCLI {
			t.Errorf("ToolSurface(%q) = %q, want %q", name, ToolSurface(name), SurfaceCLI)
		}
	}
	if got := ToolSurface("Bash"); got != "" {
		t.Errorf("ToolSurface(Bash) = %q, want empty for a name grain does not publish", got)
	}
}
