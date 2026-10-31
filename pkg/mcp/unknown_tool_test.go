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

// What a run is told when it calls a tool this server does not serve.
// For most names that is nothing but the name; for a tool grain only ever
// serves behind a capability grant it is the capability, because "unknown
// tool run_host_command" reads as a tool grain does not have and sends
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

	_, err := client.CallTool(context.Background(), "run_host_command", map[string]any{"command": "uptime"})
	if err == nil {
		t.Fatal("calling a tool this server does not serve succeeded")
	}
	if !strings.Contains(err.Error(), "self-repair") {
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
