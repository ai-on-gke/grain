package agent_test

import (
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
)

// The repositories a proposal this run makes may be filed against, on
// their way to the forked mcpserver that describes them
// (pkg/cli/mcpserver.go's -propose-repo). One flag each, in the order
// the caller wrote them: that order is "most recently worked in first",
// and a run reading a capped list reads the front of it.
func TestProposeRepoArgsCarriesEveryRepoInOrder(t *testing.T) {
	args := agent.ProposeRepoArgs(agent.RunConfig{
		ProposeRepos: []string{"acme/widgets", "acme/docs"},
	})
	want := []string{"-propose-repo", "acme/widgets", "-propose-repo", "acme/docs"}
	if !slices.Equal(args, want) {
		t.Fatalf("ProposeRepoArgs = %v, want %v", args, want)
	}
}

// A run that was told none -- a caller with no store to ask, which is
// every test fake -- passes nothing at all, leaving propose_task's
// description exactly as it reads without this.
func TestProposeRepoArgsPassesNothingWithoutRepos(t *testing.T) {
	if args := agent.ProposeRepoArgs(agent.RunConfig{}); len(args) != 0 {
		t.Fatalf("ProposeRepoArgs = %v, want nothing for a run given no repos", args)
	}
	if args := agent.ProposeRepoArgs(agent.RunConfig{ProposeRepos: []string{""}}); len(args) != 0 {
		t.Fatalf("ProposeRepoArgs = %v, want a blank entry dropped rather than passed empty", args)
	}
}
