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

// This file asks the one question about the tool roster that only a real
// server can answer: does `grain mcpserver` advertise anything the
// frameworks are not allowed to call?
//
// agent.PublishedToolNames is what both CLI frameworks filter their tool
// roster with -- claude passes it to --allowedTools, where
// --strict-mcp-config admits a tool only if that list names it, and agy
// registers eagerly from it -- so a name this process offers and that
// list omits is a tool the run dies on at its first call to it. The unit
// tests next door check that the list calls every constructor the server
// does (pkg/agent/toolroster_drift_test.go) and that it holds the names
// a reader expects (toolroster_test.go); neither of them can see what a
// started server actually puts on the wire, which is what this does.
package e2e

import (
	"context"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/bootstrap"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/readissues"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
)

// Every grant this binary serves tools for, so the server started below
// registers as much of its vocabulary as a process with no daemon can.
// Append a capability's name here when it grows tools of its own; there
// is no count to keep in step with it.
var everyToolGrant = []string{
	selfdebug.CapabilityName,
	"https://grain.dev/capabilities/grain#journal",
	"https://grain.dev/capabilities/grain#health",
	bootstrap.CapabilityName,
	grainci.CapabilityName,
	readissues.CapabilityName,
	`https://grain.dev/capabilities/grain#sql={"write":false}`,
	`https://grain.dev/capabilities/grain#sql={"write":true}`,
}

func TestMCPServerAdvertisesNothingUnpublished(t *testing.T) {
	bin := buildGrainBinary(t)

	args := []string{"-sandbox-root", t.TempDir()}
	for _, grant := range everyToolGrant {
		args = append(args, "-grant", grant)
	}
	p := startMCPServer(t, bin, args...)

	advertised := toolNames(t, p)
	published := agent.PublishedToolNames()
	for name := range advertised {
		if !slices.Contains(published, name) {
			t.Errorf("grain mcpserver advertises %q, which agent.PublishedToolNames omits: "+
				"a run offered that tool is refused at its first call to it. "+
				"Add the constructor that returns it to pkg/agent/toolroster.go.", name)
		}
	}

	// Not vacuous: a server that advertised nothing at all, or that
	// stopped registering its granted tools, would satisfy the loop
	// above without saying anything. These are the three kinds of tool
	// this configuration has -- the sandbox's own, an escape hatch, and
	// one a -grant turned on -- and the point is that the check ran
	// against a real roster, not that this is the whole of it.
	for _, want := range []string{"run_command", "ask_question", "read_grain_task"} {
		if !advertised[want] {
			t.Errorf("tools/list is missing %q; got %v", want, advertised)
		}
	}
}

// The names on the wire are bare -- "run_command", not claude's
// "mcp__grain-sandbox__run_command" -- which is the spelling each
// framework qualifies for itself, and the spelling PublishedToolNames is
// in. A server that started qualifying them would make the comparison
// above compare nothing, and would be a protocol change besides.
func TestMCPServerAdvertisesBareToolNames(t *testing.T) {
	p := startMCPServer(t, buildGrainBinary(t), "-sandbox-root", t.TempDir())

	infos, err := p.ListTools(context.Background())
	if err != nil {
		t.Fatalf("tools/list against grain mcpserver: %v\nstderr:\n%s", err, p.stderr.String())
	}
	if len(infos) == 0 {
		t.Fatal("tools/list advertised nothing at all")
	}
	for _, info := range infos {
		if len(info.Name) >= 4 && info.Name[:4] == "mcp_" {
			t.Errorf("tools/list advertises %q, which is already qualified", info.Name)
		}
	}
}
