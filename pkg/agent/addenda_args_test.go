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
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// Where the run's prompt stopped quoting the task's conversation, which
// is where the forked mcpserver's own poll for what a person says next
// has to start (mcp.Registry.AnnounceAddenda).
func TestAddendaArgsCarriesTheCursorTheRunsPromptStoppedAt(t *testing.T) {
	args := agent.AddendaArgs(agent.RunConfig{AddendaAfter: 412})
	if !slices.Equal(args, []string{"-addenda-after", "412"}) {
		t.Errorf("AddendaArgs = %v, want the cursor", args)
	}
}

// A task whose prompt quotes no conversation -- every first dispatch --
// passes nothing: the flag's own default is zero, and saying so anyway
// is a line of noise on every argument list grain builds.
func TestAddendaArgsCarriesNothingForATaskWithNoConversationYet(t *testing.T) {
	if args := agent.AddendaArgs(agent.RunConfig{}); len(args) != 0 {
		t.Errorf("AddendaArgs = %v, want nothing for a first dispatch", args)
	}
}

// A negative cursor is not a cursor. It is dropped here rather than
// passed on to be parsed into "everything ever said" at the far end.
func TestAddendaArgsDropsACursorThatIsNotOne(t *testing.T) {
	if args := agent.AddendaArgs(agent.RunConfig{AddendaAfter: -1}); len(args) != 0 {
		t.Errorf("AddendaArgs = %v, want nothing for a negative cursor", args)
	}
}
