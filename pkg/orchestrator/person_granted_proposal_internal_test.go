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

package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A run whose task a person gave a person-granted gcp set cannot pass
// that set on: its proposal asking for it is filed for a person, and the
// note says it is a person's grant -- not, as the 2026-11-08 gcp
// token-mode walk's proposal said, that the proposer "does not hold
// itself" a grant it held. A set any agent holding it may pass on, written
// as an agent writes it (no primary), is queued on the proposer's
// permission: the walk held that one too, as an escalation of the
// parent's own grant recorded with primary: true.
func TestRelayedProposalOfAHeldPersonGrantedSetSaysWhyItWaits(t *testing.T) {
	const id = "https://example.test/capabilities/gcp-walk"
	schema := func(reserved ...any) map[string]*protocol.MethodInfo {
		set := map[string]any{"type": "string", "enum": []any{"ext-deploy", "sandbox"}, "default": "sandbox"}
		if len(reserved) > 0 {
			set[protocol.PersonGrantedArgKey] = reserved
		}
		return map[string]*protocol.MethodInfo{"gcp-key": {ArgsSchema: map[string]any{
			"type": "object", "properties": map[string]any{"permission_set": set, "primary": map[string]any{"type": "boolean", "default": true}},
		}}}
	}
	protocol.NotePersonGrantedArgs(id, schema("ext-deploy"))
	protocol.NoteArgDefaults(id, schema())
	t.Cleanup(func() {
		protocol.NotePersonGrantedArgs(id, schema())
		protocol.NoteArgDefaults(id, map[string]*protocol.MethodInfo{"gcp-key": {ArgsSchema: map[string]any{}}})
	})

	now := time.Date(2026, 11, 8, 2, 20, 0, 0, time.UTC)
	store, ctx := openTreeStore(t)
	source := externalSource("5")
	source.Base = ""
	// What a person's grant is recorded as: the default argument filled in.
	source.Grants = []model.Grant{
		{Capability: id + `#gcp-key={"permission_set":"ext-deploy","primary":true}`, Via: model.GrantByLabel},
		{Capability: id + `#gcp-key={"permission_set":"sandbox","primary":true}`, Via: model.GrantByLabel},
	}
	if err := store.PutTask(ctx, source); err != nil {
		t.Fatal(err)
	}
	result := &agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "propose_task", Arguments: map[string]any{"title": "Pass the person's set on", "body": "Deploy.",
			"capabilities": []any{id + `#gcp-key={"permission_set":"ext-deploy"}`}}},
		{Name: "propose_task", Arguments: map[string]any{"title": "Pass the other set on", "body": "Read logs.",
			"capabilities": []any{id + `#gcp-key={"permission_set":"sandbox"}`}}},
	}}
	if err := relayProposedTasks(ctx, store, nil, nil, source, result, now); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]model.Task{}
	for _, task := range tasks {
		if task.Parent() == source.ID {
			byTitle[task.Title] = task
		}
	}
	other, ok := byTitle["Pass the other set on"]
	if !ok || other.Approval == nil {
		t.Fatalf("the proposal of a set any agent holding it may pass on, written without its defaults, was not queued on its proposer's permission: %+v", other)
	}
	got, ok := byTitle["Pass the person's set on"]
	if !ok {
		t.Fatal("the proposal asking for the person-granted set was not filed")
	}
	if got.Approval != nil {
		t.Fatalf("a proposal passing on a person-granted set was approved by %+v", got.Approval.Actor)
	}
	if !strings.Contains(got.Body, "only a person may grant") {
		t.Errorf("body %q does not say the set is a person's to grant", got.Body)
	}
	if strings.Contains(got.Body, "does not hold itself") {
		t.Errorf("body %q says task 5 does not hold a set a person gave it", got.Body)
	}
}
