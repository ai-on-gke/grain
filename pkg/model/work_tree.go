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

package model

import (
	"context"
	"sort"
	"strconv"
)

// A work tree is every task in one origin's tree (WorkOrigin), nested
// under the task that filed it. It is drawn in two places -- the progress
// comment grain keeps on a GitHub origin (pkg/orchestrator's
// renderWorkTree) and the tree on a task's own page in grain's UI
// (GET /api/tasks/{id}/tree) -- and both draw it from WorkTree here, so
// the two cannot disagree about which tasks are in it, where each sits,
// what each is called or what counts as done.

// Work tree statuses: the word a tree gives a task (WorkTreeStatus).
// StatusPROpen and StatusMerged are the two that come with the task's
// pull request, which a reader of either tree can follow.
const (
	TreeStatusPlanned  = "planned"
	TreeStatusQueued   = "queued"
	TreeStatusDeferred = "deferred"
	TreeStatusRunning  = "running"
	TreeStatusWaiting  = "waiting on a reply"
	TreeStatusFailed   = "failed"
	TreeStatusPROpen   = "PR open"
	TreeStatusMerged   = "merged"
	TreeStatusClosed   = "closed"
	TreeStatusDone     = "done"
)

// WorkTreeNode is one task as a tree draws it.
type WorkTreeNode struct {
	Task Task
	// Status is one of the TreeStatus words above (or the bare state, for
	// one this build has no word for).
	Status string
	// PullRequest is the pull request the task's work was opened as, nil
	// for none (TaskPullRequest).
	PullRequest *PullRequestRef
	// Done is whether the task counts toward the tree's "n of m done".
	Done bool
	// Root marks the grain task a task origin names: the tree's root,
	// drawn at the top but not counted in Done/Total -- a chat that files
	// work is where the work came from, not a piece of it, and it may
	// never finish at all.
	Root     bool
	Children []*WorkTreeNode
}

// WorkTree is origin's whole tree.
type WorkTree struct {
	Origin WorkOrigin
	// Roots are the tasks nothing else in the tree filed, in id order:
	// the origin task alone, for a task origin, and for a GitHub origin
	// every task filed straight from the pull request or issue.
	Roots []*WorkTreeNode
	// Done of Total tasks are done, the origin task itself not counted.
	Done, Total int
}

// TaskPullRequest is the pull request task's work was opened as, nil if
// none.
func TaskPullRequest(task Task) *PullRequestRef {
	var out *PullRequestRef
	for _, l := range task.Links {
		if l.Kind != LinkFixes {
			continue
		}
		if ref, err := ParsePullRequestRef(l.Target); err == nil {
			out = &ref
		}
	}
	return out
}

// WorkTreeStatus is how a tree names where task stands -- planned,
// queued, running, PR open, merged, closed -- and whether it counts
// toward "n of m done".
func (s *Store) WorkTreeStatus(ctx context.Context, task Task) (string, bool, error) {
	state, err := s.State(ctx, task.ID)
	if err != nil {
		return "", false, err
	}
	obs, err := s.GetObservation(ctx, task.ID)
	if err != nil {
		return "", false, err
	}
	pr := TaskPullRequest(task)
	switch {
	case obs != nil && obs.PrMergedAt != nil:
		return TreeStatusMerged, true, nil
	case state == StateClosed:
		return TreeStatusClosed, true, nil
	case state == StateCompleted && pr == nil:
		return TreeStatusDone, true, nil
	case pr != nil && (obs == nil || obs.PrClosedAt == nil):
		return TreeStatusPROpen, false, nil
	}
	switch state {
	case StateProposed:
		return TreeStatusPlanned, false, nil
	case StateQueued:
		return TreeStatusQueued, false, nil
	case StateDeferred:
		return TreeStatusDeferred, false, nil
	case StateRunning:
		return TreeStatusRunning, false, nil
	case StateAwaitingReply:
		return TreeStatusWaiting, false, nil
	case StateFailed:
		return TreeStatusFailed, false, nil
	}
	return string(state), false, nil
}

// WorkTree builds origin's tree: every task carrying it, plus -- for a
// task origin -- the task it names, each nested under the task that filed
// it (Task.Parent) when that task is in the tree too, and under the
// origin task when nothing in the tree filed it. include, when not
// nil, says which tasks the reader may see; one it refuses is left out
// and the tasks it filed are drawn where it would have been. A tree with
// nothing in it is nil.
func (s *Store) WorkTree(ctx context.Context, origin WorkOrigin, include func(Task) bool) (*WorkTree, error) {
	tasks, err := s.TasksWithOrigin(ctx, origin)
	if err != nil {
		return nil, err
	}
	rootID := origin.Task()
	if rootID != "" {
		root, err := s.GetTask(ctx, rootID)
		if err != nil {
			return nil, err
		}
		// The root normally carries no origin of its own (or another
		// one); one that names itself is already in the list.
		if root != nil && root.WorkOrigin != origin {
			tasks = append([]Task{*root}, tasks...)
		}
		// What the root filed belongs under it even when it went into
		// another tree: a chat that files work for an issue
		// (propose_task's origin) gives that work the issue's origin, and
		// the chat's own page would otherwise show nothing it filed.
		if root != nil {
			if tasks, err = s.withDescendants(ctx, tasks); err != nil {
				return nil, err
			}
		}
	}
	all := map[string]Task{}
	for _, t := range tasks {
		all[t.ID] = t
	}
	visible := map[string]bool{}
	for _, t := range tasks {
		if include == nil || include(t) {
			visible[t.ID] = true
		}
	}
	if len(visible) == 0 {
		return nil, nil
	}
	// parentOf is the nearest visible ancestor in the tree, "" for a root.
	parentOf := func(t Task) string {
		seen := map[string]bool{t.ID: true}
		for p := t.Parent(); p != "" && !seen[p]; {
			seen[p] = true
			pt, ok := all[p]
			if !ok {
				return ""
			}
			if visible[p] {
				return p
			}
			p = pt.Parent()
		}
		return ""
	}
	tree := &WorkTree{Origin: origin}
	nodes := map[string]*WorkTreeNode{}
	for _, t := range tasks {
		if !visible[t.ID] {
			continue
		}
		status, done, err := s.WorkTreeStatus(ctx, t)
		if err != nil {
			return nil, err
		}
		n := &WorkTreeNode{Task: t, Status: status, PullRequest: TaskPullRequest(t), Done: done, Root: t.ID == rootID}
		nodes[t.ID] = n
		if n.Root {
			continue
		}
		tree.Total++
		if done {
			tree.Done++
		}
	}
	for _, t := range tasks {
		n := nodes[t.ID]
		if n == nil {
			continue
		}
		p := parentOf(t)
		if p == "" && !n.Root && nodes[rootID] != nil {
			// Filed into the tree without being filed by anything in it
			// (an origin somebody named): it still grew out of the root.
			p = rootID
		}
		if p != "" && !n.Root {
			nodes[p].Children = append(nodes[p].Children, n)
		} else {
			tree.Roots = append(tree.Roots, n)
		}
	}
	var order func([]*WorkTreeNode)
	order = func(ns []*WorkTreeNode) {
		sort.SliceStable(ns, func(i, j int) bool { return taskIDLess(ns[i].Task.ID, ns[j].Task.ID) })
		for _, n := range ns {
			order(n.Children)
		}
	}
	order(tree.Roots)
	return tree, nil
}

// withDescendants is tasks with every task any of them filed, at any
// depth, added once each.
func (s *Store) withDescendants(ctx context.Context, tasks []Task) ([]Task, error) {
	seen := map[string]bool{}
	for _, t := range tasks {
		seen[t.ID] = true
	}
	for i := 0; i < len(tasks); i++ {
		kids, err := s.ChildTasks(ctx, tasks[i].ID)
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			if !seen[k.ID] {
				seen[k.ID] = true
				tasks = append(tasks, k)
			}
		}
	}
	return tasks, nil
}

// taskIDLess orders task ids numerically, the order they were filed in.
func taskIDLess(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	if aerr != nil || berr != nil {
		return a < b
	}
	return ai < bi
}
