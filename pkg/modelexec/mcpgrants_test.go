package modelexec

import (
	"strconv"
	"testing"
)

func TestMCPGrantsAreExactAndPerConnection(t *testing.T) {
	var g, other mcpGrants
	issued := []string{"mcpserver", "-sandbox-root", "/tmp/run-1", "-task", "7"}
	g.grant(issued)

	// The executor forwards argv without the subcommand name.
	if !g.allowed(issued[1:]) {
		t.Fatal("the issued argv, as the executor forwards it, was refused")
	}
	if !g.allowed(issued) {
		t.Fatal("the issued argv, with its subcommand name, was refused")
	}
	for name, args := range map[string][]string{
		"another root":  {"-sandbox-root", "/", "-task", "7"},
		"extra flag":    {"-sandbox-root", "/tmp/run-1", "-task", "7", "-data-dir", "/"},
		"a prefix":      {"-sandbox-root", "/tmp/run-1"},
		"joined values": {"-sandbox-root", "/tmp/run-1\x00-task", "7"},
		"nothing":       nil,
	} {
		if g.allowed(args) {
			t.Errorf("%s: %q was allowed", name, args)
		}
	}
	if other.allowed(issued[1:]) {
		t.Fatal("an argv issued to one connection was allowed on another")
	}
}

func TestMCPGrantsForgetTheOldestBeyondTheBound(t *testing.T) {
	var g mcpGrants
	for i := 0; i <= maxMCPGrantsPerHost; i++ {
		g.grant([]string{"-task", strconv.Itoa(i)})
	}
	if g.allowed([]string{"-task", "0"}) {
		t.Fatal("the oldest grant survived past the bound")
	}
	if !g.allowed([]string{"-task", strconv.Itoa(maxMCPGrantsPerHost)}) {
		t.Fatal("the newest grant was forgotten")
	}
}
