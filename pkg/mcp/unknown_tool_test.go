package mcp

// What a run is told when it calls a tool this server does not serve.
// For most names that is nothing but the name; for a tool grain only ever
// serves behind a capability grant it is the capability, because "unknown
// tool upgrade_deployment" reads as a tool grain does not have and sends
// an agent looking for another way to do the job it was given.

import (
	"context"
	"strings"
	"testing"
)

func TestAnUnknownToolNamesTheCapabilityThatWouldServeIt(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	_, err := client.CallTool(context.Background(), "upgrade_deployment", map[string]any{"branch": "main"})
	if err == nil {
		t.Fatal("calling a tool this server does not serve succeeded")
	}
	if !strings.Contains(err.Error(), "self-upgrade") {
		t.Errorf("the refusal does not name the capability that serves it: %v", err)
	}
	if !strings.Contains(err.Error(), "a human attaches it") {
		t.Errorf("the refusal does not say who can add it: %v", err)
	}
}

func TestAnUnknownToolThatIsNobodysCapabilityIsJustUnknown(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	_, err := client.CallTool(context.Background(), "invent_a_tool", nil)
	if err == nil {
		t.Fatal("calling a tool this server does not serve succeeded")
	}
	if !strings.Contains(err.Error(), "unknown tool invent_a_tool") {
		t.Errorf("the refusal does not name the tool: %v", err)
	}
	if strings.Contains(err.Error(), "capability") {
		t.Errorf("a tool no grant serves was blamed on a capability: %v", err)
	}
}
