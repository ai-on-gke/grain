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

package forge

import "strings"

// unfixable.go is the difference between a pull request that is red and a
// pull request that is red for a reason nobody pushing to its branch can
// reach.
//
// The merge queue used to have one word for both. A head that is failing
// earns the cheap half of a repair (merge the base in, refreshStaleHead)
// and then the expensive half (an agent run on the branch, one full CI
// suite for whatever it pushes), and that is exactly right for a branch
// whose own tests do not pass. It is spending an agent and two suites on
// nothing for a branch whose checks failed before a job of them ran a
// step, or whose red check is a CLA bot that will never pass for the
// author grain files pull requests as. Both are facts about the
// repository -- true of every pull request open against it, unchanged by
// any commit -- and grain has been able to *say* so for a while
// (orchestrator's checkstartups.go) without that conclusion reaching the
// one decision it should have governed: whether to spend the repair at
// all.
//
// So the split lives here, in the package that already holds "what is
// true about this branch" and cannot see a task (docs/three-pieces.md).
// What is on the other side of the boundary is where the conclusion comes
// from -- a GitHub read of what the repository has been running lately,
// or an operator's declaration about a check that never passes here --
// and it arrives as an Unfixable.

// Unfixable is what grain has concluded about one repository's checks:
// which of them nothing pushed to a branch turns green, and why in words.
//
// The zero value says nothing, and saying nothing is the common case --
// every repository whose CI works is one. A caller that could not find
// out (a credential that cannot read Actions, a provider that is not
// Actions at all) passes the zero value too: absence of an answer must
// leave every decision exactly where it stood before anybody asked, which
// is what makes asking safe.
type Unfixable struct {
	// ChecksNeverStart is the repository-wide finding: of the runs grain
	// looked at, not one got a job as far as running a step
	// (github.RunStarts.ChecksNeverStarted). Every cause of that is a
	// setting on the repository or the account behind it -- Actions
	// disabled or restricted, no runner it may use, an account out of
	// minutes -- so it covers every check the repository reports, named
	// or not, and Checks below need not list them.
	//
	// The one exception is a workflow file this very branch made
	// unparseable, which is a startup failure whose cause is in the tree.
	// It is not carved out here, because this type cannot see a diff:
	// the caller that can (orchestrator's touchedWorkflowFiles) asks
	// before it consults this.
	ChecksNeverStart bool
	// Checks names individual checks that will not pass here whatever is
	// pushed, for the case the finding above cannot express: a check that
	// is working exactly as intended and is never going to go green for
	// this author. A CLA bot is the standing example -- restoring a
	// repository's Actions budget clears every run-level failure and
	// leaves `cla/google` red on every agent-authored pull request in it,
	// permanently, because the thing it is asserting is about who signed
	// what rather than about the tree.
	//
	// Matched case-insensitively and by exact name, not by substring: the
	// mistake this package already made once was reading a check's *name*
	// for a phrase and letting a substring take a repository's whole gate
	// away (checks.go's note on the Actions-budget sniffing that used to
	// live there). A name is a name.
	Checks []string
	// Why is the finding in the words a person reads on the row and in
	// the comment the queue leaves -- "not one of the last 20 finished
	// Actions runs in gke-labs/kontur got a job as far as running a
	// step". Empty where the caller had nothing to add beyond the fields
	// above.
	Why string
}

// Any reports whether this says anything at all.
func (u Unfixable) Any() bool { return u.ChecksNeverStart || len(u.Checks) > 0 }

// Covers reports whether check is one nothing pushed to a branch clears.
//
// grain CI's own verdict is never covered, whatever a declaration says,
// and that is deliberate rather than an oversight to be configured away:
// a grain CI run judges one exact commit on this branch (GrainCI's own
// doc comment), so it is the branch's own answer by construction, and a
// deployment that could declare its own runner unfixable would have built
// a way to merge with no verdict at all.
func (u Unfixable) Covers(check string) bool {
	if strings.EqualFold(check, GrainCICheckName) {
		return false
	}
	if u.ChecksNeverStart {
		return true
	}
	for _, name := range u.Checks {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(check)) {
			return true
		}
	}
	return false
}

// Reds is one pull request's failing checks, split by whether a commit on
// its branch could clear them. The two halves are read for two different
// decisions and that is the whole reason they are separated:
//
//   - Inherited decides whether there is a path to green at all. While
//     one of these is red the pull request does not merge however green
//     everything else goes, so the moves that exist to *reach* green --
//     merging the base branch in, dispatching a repair to fix the tree --
//     cannot land it and are not worth their cost.
//   - Own decides whether an agent has anything to do. It is the work
//     that is genuinely there, and it does not stop being real work
//     because something inherited is red beside it: grain/task-219 was
//     seven checks nobody could fix and one grain CI failure its own base
//     merge had caused, and the seven are exactly why the one went
//     unnoticed.
type Reds struct {
	// Own is what a commit on this branch could turn green, in the order
	// the checks were reported, with grain CI's own verdict appended
	// where that is what failed.
	Own []string
	// Inherited is the rest: red that is true of this repository rather
	// than of this branch.
	Inherited []string
}

// PathToGreen reports whether anything pushed to this branch could make
// the pull request mergeable. False means the queue should stop spending
// on it -- no base merge, no repair -- and tell a person, because every
// move it could make ends at the same red check.
func (r Reds) PathToGreen() bool { return len(r.Inherited) == 0 }

// BranchOwns reports whether any of the red is this branch's, which is
// what earns the repair it would have earned before any of this existed.
func (r Reds) BranchOwns() bool { return len(r.Own) > 0 }

// SplitReds sorts failing -- the names of the checks that did not pass,
// as FailingChecks reports them -- against what grain has concluded about
// the repository.
//
// grainFailed is grain's own CI having failed for this exact commit, and
// it is always the branch's own red: see Unfixable.Covers. It is named
// here rather than left to the caller to append, so that "the branch owns
// something" and "grain CI is what it owns" cannot drift apart.
//
// A caller whose repository is not judged by GitHub's checks at all --
// forge.Input.IgnoreGitHubCI, or a task whose gate is grain CI -- passes
// no names, because those checks are not what is holding the merge and
// classifying them either way would put a sentence about them in front of
// a person who has been told, correctly, that they are not read here.
func SplitReds(failing []string, grainFailed bool, u Unfixable) Reds {
	var r Reds
	for _, name := range failing {
		if u.Covers(name) {
			r.Inherited = append(r.Inherited, name)
			continue
		}
		r.Own = append(r.Own, name)
	}
	if grainFailed {
		r.Own = append(r.Own, GrainCICheckName)
	}
	return r
}
