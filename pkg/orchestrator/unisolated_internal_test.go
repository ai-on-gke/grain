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

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/policy"
)

// On a deployment whose sandboxes isolate nothing from this host, only
// this machine's own tasks run, and only they get the operator's
// -host-sandbox-env (GSA1-05). Somebody else's task is never one of
// them, and neither is anything proposed under it, however far down: a
// proposal names no requester of its own, which alone would read as
// grain's.
func TestUnisolatedSandboxesRunOnlyTheMachinesOwnTasks(t *testing.T) {
	store, ctx := openTestStore(t)
	put := func(task model.Task) model.Task {
		t.Helper()
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		return task
	}
	proposedBy := func(id string) []model.Link { return []model.Link{{Kind: model.LinkProposedBy, Target: id}} }

	root := put(model.Task{ID: "1", Title: "external root", Creator: "friend"})
	child := put(model.Task{ID: "2", Title: "child", Links: proposedBy(root.ID)})
	grandchild := put(model.Task{ID: "3", Title: "grandchild", Links: proposedBy(child.ID)})
	stranger := put(model.Task{ID: "4", Title: "stranger", Creator: "mallory"})
	strangers := put(model.Task{ID: "5", Title: "stranger's follow-up", Links: proposedBy(stranger.ID)})
	owners := put(model.Task{ID: "6", Title: "owner's", Creator: "owner"})
	ownersChild := put(model.Task{ID: "7", Title: "owner's follow-up", Links: proposedBy(owners.ID)})
	grains := put(model.Task{ID: "8", Title: "a schedule's"})
	orphan := put(model.Task{ID: "9", Title: "orphan", Links: proposedBy("404")})
	loopA := put(model.Task{ID: "10", Title: "loop a", Links: proposedBy("11")})
	put(model.Task{ID: "11", Title: "loop b", Links: proposedBy("10")})

	p, err := policy.Parse([]byte("repos: [acme/*]\nlocal_requesters: [owner]\n"))
	if err != nil {
		t.Fatal(err)
	}
	host := NewHostSandboxes(t.TempDir())
	for _, c := range []struct {
		task  model.Task
		local bool
	}{
		{root, false}, {child, false}, {grandchild, false},
		{stranger, false}, {strangers, false},
		{owners, true}, {ownersChild, true}, {grains, true},
		{orphan, false}, {loopA, false},
	} {
		local, err := localTask(ctx, store, p, c.task)
		if err != nil {
			t.Fatalf("localTask(%s): %v", c.task.Title, err)
		}
		if local != c.local {
			t.Errorf("localTask(%s) = %v, want %v", c.task.Title, local, c.local)
		}

		deps := Deps{Store: store, Sandboxes: host, Policy: p}
		err = checkPolicy(ctx, deps, c.task, model.NetworkOpen)
		if refused := err != nil && strings.Contains(err.Error(), "unisolated host sandboxes"); refused == c.local {
			t.Errorf("checkPolicy(%s) on host sandboxes = %v; local=%v", c.task.Title, err, c.local)
		}

		// Not this backend's concern elsewhere.
		deps.Sandboxes = NewDisabledSandboxes()
		if err := checkUnisolated(ctx, deps, c.task); err != nil {
			t.Errorf("checkUnisolated(%s) on an isolating backend = %v", c.task.Title, err)
		}
	}

	// With no -policy at all, a task whose chain cannot be resolved is
	// still refused, and every other requester is the daemon's own.
	noPolicy := Deps{Store: store, Sandboxes: host}
	if err := checkPolicy(ctx, noPolicy, orphan, model.NetworkOpen); err == nil {
		t.Error("an unresolved task passed with no -policy on host sandboxes")
	}
	if err := checkPolicy(ctx, noPolicy, strangers, model.NetworkOpen); err != nil {
		t.Errorf("with no -policy, a resolved task was refused: %v", err)
	}

	// The owner can say otherwise.
	lax, err := policy.Parse([]byte("repos: [acme/*]\nlocal_requesters: [owner]\nallow_external_on_host_sandboxes: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkUnisolated(ctx, Deps{Store: store, Sandboxes: host, Policy: lax}, grandchild); err != nil {
		t.Errorf("allow_external_on_host_sandboxes: %v", err)
	}
}
