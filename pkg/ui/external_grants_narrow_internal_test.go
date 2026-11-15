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

package ui

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A gma_ credential presented inside a limited verified session reaches no
// further than that session's role: held, the grant strings
// agentGrantRefusal and externalTemplateWithin read, is narrowed with set,
// not left at the credential's own (post-merge review of PR #987,
// finding 2.2).
func TestNarrowExternalGrantsHoldsHeldToTheSessionsRole(t *testing.T) {
	held, set, _, err := model.ResolveExternalAgentGrants("", []string{"tasks:read", "tasks:write", "dispatch:write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	roleGrants := []string{model.APIGrantString("tasks:read", ""), model.APIGrantString("tasks:write", "acme/widgets")}
	role := &accessRole{Who: "wade@example.com", grants: roleGrants, set: APIGrantsOf(roleGrants)}

	gotHeld, gotSet := narrowExternalGrants(held, set, role)
	if gotSet.Allows("tasks:write", "acme/gadgets") || !gotSet.Allows("tasks:write", "acme/widgets") || gotSet.Has("dispatch:write") {
		t.Errorf("narrowed set = %v, want tasks:write on acme/widgets alone and no dispatch:write", gotSet.Scopes())
	}
	for _, c := range []struct {
		grant string
		want  bool
	}{
		{model.APIGrantString("tasks:read", ""), true},
		{model.APIGrantString("tasks:write", "acme/widgets"), true},
		{model.APIGrantString("tasks:write", ""), false},
		{model.APIGrantString("tasks:write", "acme/gadgets"), false},
		{model.APIGrantString("dispatch:write", ""), false},
	} {
		if got := agentGrantRefusal(gotHeld, c.grant, nil) == nil; got != c.want {
			t.Errorf("narrowed held %v grants %s = %v, want %v", gotHeld, c.grant, got, c.want)
		}
		if got := agentGrantRefusal(held, c.grant, nil) == nil; !got && c.want {
			t.Errorf("the credential's own held %v does not grant %s", held, c.grant)
		}
	}

	// No role, or an unlimited one, narrows nothing.
	for _, r := range []*accessRole{nil, {Full: true}} {
		if h, _ := narrowExternalGrants(held, set, r); len(h) != len(held) {
			t.Errorf("narrowExternalGrants under %+v = %v, want %v", r, h, held)
		}
	}
}
