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
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A warm session sandbox is reused only under the key of the run that
// built it, so every change that should cost a fresh sandbox -- a grant
// withdrawn or added, a different target, a narrower network -- must
// change the key, and grant order must not.
func TestSessionSandboxKeyChangesWithWhatTheSandboxWasBuiltFor(t *testing.T) {
	t.Parallel()
	base := model.Task{
		Target: &model.RepoRef{Owner: "acme", Name: "app"},
		Base:   "main",
		Reads:  []model.RepoRef{{Owner: "acme", Name: "docs"}, {Owner: "acme", Name: "shared"}},
		Writes: []model.WriteTarget{
			{Repo: model.RepoRef{Owner: "acme", Name: "infra"}, Base: "prod"},
			{Repo: model.RepoRef{Owner: "acme", Name: "tools"}, Base: "main"},
		},
		Grants:        []model.Grant{{Capability: "gcp-key"}, {Capability: "durable", Via: "grain"}},
		NetworkAccess: model.NetworkOpen,
	}
	shape := Shape{NetworkAccess: model.NetworkOpen, Class: "interactive"}
	prepared := []model.Materialized{{Grant: model.Grant{Capability: "gcp-key"}}}
	key := sessionSandboxKey(shape, base, prepared)
	grantsKey := sessionGrantsKey(base)

	reordered := base
	reordered.Grants = []model.Grant{base.Grants[1], base.Grants[0]}
	reordered.Reads = []model.RepoRef{base.Reads[1], base.Reads[0]}
	reordered.Writes = []model.WriteTarget{base.Writes[1], base.Writes[0]}
	if got := sessionSandboxKey(shape, reordered, prepared); got != key {
		t.Errorf("reordering the same grants/reads/writes changed the key:\n%s\n%s", key, got)
	}
	if got := sessionGrantsKey(reordered); got != grantsKey {
		t.Errorf("reordering the same grants/reads/writes changed sessionGrantsKey:\n%s\n%s", grantsKey, got)
	}

	withdrawn := base
	withdrawn.Grants = base.Grants[1:]
	retarget := base
	retarget.Target = &model.RepoRef{Owner: "acme", Name: "other"}
	rebased := base
	rebased.Base = "release"
	readWithdrawn := base
	readWithdrawn.Reads = base.Reads[:1]
	writeWithdrawn := base
	writeWithdrawn.Writes = base.Writes[:1]
	writeRebased := base
	writeRebased.Writes = []model.WriteTarget{
		{Repo: model.RepoRef{Owner: "acme", Name: "infra"}, Base: "staging"},
		base.Writes[1],
	}
	narrowed := shape
	narrowed.NetworkAccess = model.NetworkNone
	for name, got := range map[string]string{
		"a task grant withdrawn":     sessionSandboxKey(shape, withdrawn, prepared),
		"a run grant no longer made": sessionSandboxKey(shape, base, nil),
		"another target":             sessionSandboxKey(shape, retarget, prepared),
		"another base branch":        sessionSandboxKey(shape, rebased, prepared),
		"a read repo withdrawn":      sessionSandboxKey(shape, readWithdrawn, prepared),
		"a write repo withdrawn":     sessionSandboxKey(shape, writeWithdrawn, prepared),
		"a write repo base changed":  sessionSandboxKey(shape, writeRebased, prepared),
		"no network":                 sessionSandboxKey(narrowed, base, prepared),
	} {
		if got == key {
			t.Errorf("%s left the key unchanged: %s", name, key)
		}
	}

	netNarrowedTask := base
	netNarrowedTask.NetworkAccess = model.NetworkNone
	for name, got := range map[string]string{
		"a task grant withdrawn":    sessionGrantsKey(withdrawn),
		"another target":            sessionGrantsKey(retarget),
		"another base branch":       sessionGrantsKey(rebased),
		"a read repo withdrawn":     sessionGrantsKey(readWithdrawn),
		"a write repo withdrawn":    sessionGrantsKey(writeWithdrawn),
		"a write repo base changed": sessionGrantsKey(writeRebased),
		"task network narrowed":     sessionGrantsKey(netNarrowedTask),
	} {
		if got == grantsKey {
			t.Errorf("%s left sessionGrantsKey unchanged: %s", name, grantsKey)
		}
	}
}

// An agent cannot hide a call from the orchestrator by naming the key the
// in-process relay used to mark relayed calls with: only the orchestrator's
// own ToolCall.Relayed does.
func TestRelayedMarkIsNotAnAgentArgument(t *testing.T) {
	t.Parallel()
	result := &agent.Result{ToolCalls: []agent.ToolCall{{
		Name:      "comment_on_issue",
		Arguments: map[string]any{"comment": "closing note", "_relayed": true},
	}}}
	if got, ok := firstToolCallArg(result, "comment_on_issue", "comment"); !ok || got != "closing note" {
		t.Fatalf("a call carrying its own _relayed argument was skipped: %q, %v", got, ok)
	}
	markToolCallsRelayed(result, "comment_on_issue")
	if _, ok := firstToolCallArg(result, "comment_on_issue", "comment"); ok {
		t.Error("a call the orchestrator marked relayed is still read")
	}
}
