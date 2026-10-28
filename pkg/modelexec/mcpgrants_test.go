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
