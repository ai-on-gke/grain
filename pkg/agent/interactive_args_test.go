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

// The one fact a forked mcpserver needs before it serves the chat tool:
// that somebody is sitting with this task. Nothing else travels with it
// -- how long a chat turn waits is the tool's own business, and how long
// the run lives is the orchestrator's.
func TestInteractiveArgsCarriesTheFlagForASessionAndNothingForARun(t *testing.T) {
	args := agent.InteractiveArgs(agent.RunConfig{Interactive: true})
	if !slices.Equal(args, []string{"-interactive"}) {
		t.Errorf("InteractiveArgs = %v, want just the flag", args)
	}
	// A background run's mcpserver is not given a tool that would block
	// for an hour waiting for somebody who is not there.
	if args := agent.InteractiveArgs(agent.RunConfig{}); len(args) != 0 {
		t.Errorf("InteractiveArgs = %v, want nothing for an ordinary run", args)
	}
}
