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
	"fmt"
)

// A release-qualification task exercises the merge queue and base-branch
// triage the only way a task can get pull requests: by proposing fixture
// tasks against its own qualification/<task-id> scratch branch
// (QualificationScratchBranch), which it cuts from the candidate and
// deletes before it exits. Those fixtures have to be queued, and to merge
// into that branch, with nobody clicking anything, or the item can never
// finish (task 2839).
//
// AutoSubmits cannot say yes to them. Its permission is parent.AutoMerge
// or parent.AutoApproveProposals, and a qualification task holds neither
// for the default branch -- the suite files it with auto-merge off,
// because nothing it does should land there. And LandsWhereGranted is a
// question about the parent's own branch and its declared Writes, and the
// scratch branch is neither.
//
// What the qualification task does hold is its template's `push:` list
// (RefPush, Store.GitDeclaredPush): the declaring repository's default
// branch saying its runs may push refs/heads/qualification/* themselves,
// unreviewed. A fixture merging into the task's own scratch branch lands
// nothing the task could not have pushed there itself, so queueing it and
// letting it merge there is not a new permission. That is the whole rule,
// and ScratchFixture is deliberately narrower than "anywhere the
// template may push":
//
//   - exactly the proposer's own scratch branch, not another task's
//     qualification/<id> (which the glob would also cover) and not any
//     other ref a template might declare, such as a release branch;
//   - the proposer's own repository, the one its push set applies to;
//   - no Writes of its own, since a fixture that auto-merges passes that
//     permission on to its own proposals through LandsWhereGranted, and a
//     Writes entry naming the proposer's default branch (one of its
//     WriteTargets) would let a fixture's proposal auto-merge there;
//   - no capability, read or write target the proposer lacks, and no
//     change target -- AutoSubmits' own conditions.
//
// AutoSubmitLimits still bound how many and how deep, as for any other
// self-approved proposal.

// ScratchFixture reports whether child, a proposal parent made, is a
// fixture on parent's own qualification scratch branch that may be
// queued and may auto-merge there. parentPushesScratch is whether
// parent's template declares a push covering that branch in parent's
// repository (Store.PushesOwnScratchBranch).
func ScratchFixture(child, parent Task, parentPushesScratch bool) bool {
	if !parentPushesScratch || parent.ID == "" || parent.Target == nil || child.Target == nil {
		return false
	}
	if !child.Target.Same(*parent.Target) || IsConfigRepo(*child.Target) {
		return false
	}
	return child.Base == QualificationScratchBranch(parent.ID) &&
		child.ChangeTarget == nil &&
		len(child.Writes) == 0 &&
		len(UngrantedReads(child, parent)) == 0 &&
		GrantsSubsetOf(GrantsBeyondCIReadDefault(child), parent.Grants)
}

// PushesOwnScratchBranch reports whether t's runs may push its own
// qualification scratch branch: t was filed from a repository-declared
// template whose `push:` list covers refs/heads/qualification/<t.ID>,
// declared in the repository t itself targets. It is ScratchFixture's
// parentPushesScratch, read the way the git proxy reads the same
// permission (GitDeclaredPush): from the declaration as the declaring
// repository's default branch has it now.
func (s *Store) PushesOwnScratchBranch(ctx context.Context, t Task) (bool, error) {
	if t.ID == "" || t.TemplateID == "" || t.Target == nil {
		return false, nil
	}
	decl, err := s.GetRepoDeclaration(ctx, t.TemplateID)
	if err != nil {
		return false, fmt.Errorf("reading the push set of task %s's template %s: %w", t.ID, t.TemplateID, err)
	}
	if decl == nil || decl.Kind != DeclaredTemplate || !decl.Repo.Same(*t.Target) {
		return false, nil
	}
	matched, _, _ := decl.Push.Allows("refs/heads/" + QualificationScratchBranch(t.ID))
	return matched, nil
}
