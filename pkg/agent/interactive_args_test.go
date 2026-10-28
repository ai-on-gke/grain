package agent_test

import (
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
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
