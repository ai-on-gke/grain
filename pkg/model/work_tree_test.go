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

package model_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A chat's tree: the chat at the root, uncounted; what it filed nested
// under it; a task somebody filed straight into the tree (naming the
// origin, filed by nothing in it) still under the root; and a task the
// reader may not see left out, with what it filed drawn where it was.
func TestWorkTreeNestsUnderTheRootAndSkipsHiddenTasks(t *testing.T) {
	store, ctx := open(t)
	proposal(t, store, ctx, "1", "", nil)
	proposal(t, store, ctx, "2", "1", nil)
	proposal(t, store, ctx, "3", "2", nil)
	proposal(t, store, ctx, "4", "3", nil)
	stray := model.Task{
		ID: "5", Intent: model.IntentImplement, Title: "5",
		Origin:     model.Origin{Attribution: model.Attribution{Actor: bot}, Reason: model.ReasonDirect},
		Binding:    model.BindingDirective,
		WorkOrigin: model.TaskOrigin("1"),
	}
	if err := store.PutTask(ctx, stray); err != nil {
		t.Fatal(err)
	}

	tree, err := store.WorkTree(ctx, model.TaskOrigin("1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Total != 4 || tree.Done != 0 {
		t.Errorf("%d of %d done, want 0 of 4", tree.Done, tree.Total)
	}
	if got := shape(tree.Roots); got != "1*(2(3(4)) 5)" {
		t.Errorf("tree = %s", got)
	}
	for _, n := range tree.Roots[0].Children {
		if n.Status != model.TreeStatusPlanned {
			t.Errorf("task %s status = %q, want planned", n.Task.ID, n.Status)
		}
	}

	hidden, err := store.WorkTree(ctx, model.TaskOrigin("1"), func(t model.Task) bool { return t.ID != "3" })
	if err != nil {
		t.Fatal(err)
	}
	if got := shape(hidden.Roots); got != "1*(2(4) 5)" || hidden.Total != 3 {
		t.Errorf("tree without task 3 = %s, total %d", got, hidden.Total)
	}

	if none, err := store.WorkTree(ctx, model.TaskOrigin("1"), func(model.Task) bool { return false }); err != nil || none != nil {
		t.Errorf("a tree with nothing visible = %+v, %v; want nil", none, err)
	}
}

func shape(nodes []*model.WorkTreeNode) string {
	out := ""
	for i, n := range nodes {
		if i > 0 {
			out += " "
		}
		out += n.Task.ID
		if n.Root {
			out += "*"
		}
		if len(n.Children) > 0 {
			out += "(" + shape(n.Children) + ")"
		}
	}
	return out
}

// A chat that files work for an issue (propose_task's origin) gives that
// work the issue's origin, and the chat's own tree still draws it: the
// work it filed, at any depth, whatever tree each task went into.
func TestATaskRootedTreeDrawsWhatTheRootFiledIntoAnotherTree(t *testing.T) {
	store, ctx := open(t)
	proposal(t, store, ctx, "1", "", nil)
	issue := model.WorkOrigin("acme/app#7")
	for _, id := range []string{"2", "3"} {
		tk := model.Task{
			ID: id, Intent: model.IntentImplement, Title: id,
			Origin:     model.Origin{Attribution: model.Attribution{Actor: bot}, Reason: model.ReasonProposal},
			Binding:    model.BindingDirective,
			Links:      []model.Link{{Kind: model.LinkProposedBy, Target: "1"}},
			WorkOrigin: issue,
		}
		if id == "3" {
			tk.Links[0].Target = "2"
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	proposal(t, store, ctx, "4", "1", nil)

	tree, err := store.WorkTree(ctx, model.TaskOrigin("1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := shape(tree.Roots); got != "1*(2(3) 4)" || tree.Total != 3 {
		t.Errorf("chat tree = %s, total %d; want 1*(2(3) 4) of 3", got, tree.Total)
	}
	// The issue's own tree (the progress comment's) is unchanged.
	gh, err := store.WorkTree(ctx, issue, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := shape(gh.Roots); got != "2(3)" || gh.Total != 2 {
		t.Errorf("issue tree = %s, total %d; want 2(3) of 2", got, gh.Total)
	}
}
