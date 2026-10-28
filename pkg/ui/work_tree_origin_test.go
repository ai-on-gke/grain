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

package ui_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A task from before origins existed, filed part way down a tree, is
// stored with no origin of its own. Its page draws the tree it is part
// of, rooted where the tree is -- not a tree of its own rooted at it,
// which is what treating "no origin" as "a root" drew.
func TestAnOriginlessTaskPartWayDownATreeIsDrawnInItsFilersTree(t *testing.T) {
	client, store, ctx := testClient(t)
	put := func(id string, links ...model.Link) {
		t.Helper()
		task := model.Task{
			ID: id, Intent: model.IntentImplement, Title: "task " + id,
			Origin:  model.Origin{Attribution: model.Attribution{Actor: model.TriggerPrincipal}, Reason: model.ReasonDirect},
			Binding: model.BindingDirective,
			Links:   links,
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	put("1")
	// Stored before its proposed-by link was, so with no origin: the
	// shape a task filed before origins existed has.
	put("2")
	put("2", model.Link{Kind: model.LinkProposedBy, Target: "1"})
	put("3", model.Link{Kind: model.LinkProposedBy, Target: "2"})
	if b, _ := store.GetTask(ctx, "2"); b.WorkOrigin != "" || b.Parent() != "1" {
		t.Fatalf("task 2 = origin %q parent %q, want none and 1", b.WorkOrigin, b.Parent())
	}

	for _, id := range []string{"1", "2", "3"} {
		tree, err := client.WorkTree(ctx, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tree.Origin != "task:1" || tree.RootTask != "1" || tree.Total != 2 {
			t.Errorf("task %s's tree = origin %q root %q total %d, want task:1, 1, 2",
				id, tree.Origin, tree.RootTask, tree.Total)
		}
		if len(tree.Nodes) != 1 || tree.Nodes[0].ID != "1" ||
			len(tree.Nodes[0].Children) != 1 || tree.Nodes[0].Children[0].ID != "2" ||
			len(tree.Nodes[0].Children[0].Children) != 1 || tree.Nodes[0].Children[0].Children[0].ID != "3" {
			t.Errorf("task %s's tree is not 1 > 2 > 3: %+v", id, tree.Nodes)
		}
	}
}
