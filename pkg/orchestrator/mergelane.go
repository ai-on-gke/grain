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

import "strings"

// A merge queue lane is one (repository, base branch) pair: the unit the
// queue keeps its order in and spends its merges on.
//
// The queue used to be one line per repository, and that was wrong for
// any repository with more than one branch being merged into. A change
// for `main` still waiting on its CI held every green change for
// `candidate` behind it -- no merge and no batch for either branch, with
// every row saying "behind task N" about a change on the other branch
// (grain/task-3340: five green candidate changes sat behind three
// freshly filed ports to main for twenty minutes). Two branches do not
// share a base: merging into one never changes what a change for the
// other is tested against, so there is no order between them for the
// queue to keep.
//
// What is still per repository is everything about the repository itself
// rather than its order -- its merge rules, its CI mode, and the
// conclusions about its checks (checkstartups.go) -- because those are
// the same for every branch in it.

// laneKey is the map key of one lane. The NUL keeps "o/r" + "x" apart from
// "o/rx" + "", which no branch or repository name can contain.
func laneKey(repo, base string) string {
	return repo + "\x00" + base
}

// laneOf is the lane e is queued in.
//
// The base branch is the pull request's own where this cycle has read it
// (e.detail, set by observe), since that is where it will actually merge,
// and before then the base the task itself was filed against. Empty is a
// task that named none and a pull request nothing has read: the
// repository's default branch, before anything has said which that is.
//
// Only reconcileSync's walk decides anything on the read lane -- who is
// head, who merges (mergeWalk) -- because only it has read every entry by
// the time it decides. MergeQueueHeads, which reads nothing from GitHub,
// works on the declared base alone.
func laneOf(e queueEntry) string {
	return laneKey(e.ref.Repo.String(), laneBase(e))
}

// laneBase is the base-branch half of laneOf.
func laneBase(e queueEntry) string {
	if e.detail.BaseRef != "" {
		return e.detail.BaseRef
	}
	return e.task.Base
}

// laneName is a lane said in words for a row: "o/r candidate" reads as
// the queue for that branch, and a lane whose branch nothing has named
// yet is said as the repository alone, as it always was.
func laneName(e queueEntry) string {
	if base := laneBase(e); base != "" {
		return e.ref.Repo.String() + " " + base
	}
	return e.ref.Repo.String()
}

// laneRepo is the repository half of a lane key.
func laneRepo(key string) string {
	repo, _, _ := strings.Cut(key, "\x00")
	return repo
}
