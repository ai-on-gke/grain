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
	"fmt"
	"strconv"
	"strings"
)

// PullRequestRef identifies a pull request in the *target* repo, never the
// task repo — RepoRef alone is ambiguous between the two, which is exactly
// the split docs/data-model.md's "task repo vs target repo" section exists
// to keep straight.
type PullRequestRef struct {
	Repo   RepoRef
	Number int
}

// String is PullRequestRef's own representation as a task_link row's
// target: "owner/name#123". '#' cannot appear in either half of a GitHub
// repo name, so this round-trips through ParsePullRequestRef unambiguously
// with no escaping.
func (r PullRequestRef) String() string { return fmt.Sprintf("%s#%d", r.Repo, r.Number) }

// ParsePullRequestRef reverses PullRequestRef.String.
func ParsePullRequestRef(s string) (PullRequestRef, error) {
	repoPart, numPart, ok := strings.Cut(s, "#")
	if !ok {
		return PullRequestRef{}, fmt.Errorf("pull request ref must be owner/name#number, got %q", s)
	}
	repo, err := ParseRepo(repoPart)
	if err != nil {
		return PullRequestRef{}, fmt.Errorf("pull request ref %q: %w", s, err)
	}
	number, err := strconv.Atoi(numPart)
	if err != nil {
		return PullRequestRef{}, fmt.Errorf("pull request ref %q: bad number: %w", s, err)
	}
	return PullRequestRef{Repo: repo, Number: number}, nil
}

// PrHealth is GitHub's own view of one pull request, as far as grain has
// last observed it — docs/data-model.md's PrHealth enum.
//
// UNKNOWN keeps the distinction that matters and that a close-out decision
// depends on: GitHub computes mergeability asynchronously, so a fresh push
// reading UNKNOWN means "check again next cycle," never "conflicted."
//
// PrPending is CI still running: at least one check has not reached a
// conclusion yet, so neither "clean" nor "failing" is known. It also
// covers the case before that one, where nothing has been reported at
// all -- GitHub creates a workflow run's check runs asynchronously after
// it processes a push, and an empty check list on a commit pushed
// seconds ago is CI that has not registered yet at least as often as it
// is a repo with no CI. orchestrator.healthFrom holds an empty list
// PENDING until enough time has passed to tell those apart (its own
// defaultCheckRegistrationWindow).
//
// It is distinct from UNKNOWN, which is "grain could not find out"
// (GitHub has not computed mergeability, or the credential cannot read
// checks at all) -- PENDING is a definite answer that resolves by
// itself, given time. Nothing merges on it: a PR whose tests have not
// finished, or have not started, is exactly the PR the merge queue must
// wait on rather than merge.
//
// "Given time" is bounded, because some checks never finish: a workflow
// waiting on an approval nobody gives, a runner that never picks the job
// up, a provider that reported "queued" and went away. A queue head that
// has read PENDING for longer than orchestrator's own
// defaultCheckStallDeadline is given up on -- said so on the task, and
// passed over so the tasks behind it can move -- rather than held
// forever. It still merges if its checks ever do finish clean; what it
// loses is the queue position, not the merge.
//
// PrMerged is orchestrator.healthFrom's answer for a closed PR GitHub's
// own Merged field says merged, as opposed to PrClosed for one that
// closed without merging -- the distinction bwsalmon/agents#493's task
// timeline needs to tell "PR merged" and "PR closed unmerged" apart, even
// though every other caller here still treats the two identically
// (neither one takes any more commits).
type PrHealth string

const (
	PrUnknown    PrHealth = "unknown"
	PrClean      PrHealth = "clean"
	PrPending    PrHealth = "pending"
	PrConflicted PrHealth = "conflicted"
	PrFailing    PrHealth = "failing"
	PrMerged     PrHealth = "merged"
	PrClosed     PrHealth = "closed"
)

// PullRequestOffTarget reports whether ref, read off one of task's
// LinkFixes links, is in a repository task no longer pushes to: neither
// its Target nor one of its Writes. That is a pull request left behind
// by a repo retarget (an edit moving the task to another repository
// after a run had already opened one in the old), and it is no longer
// this task's change: it is not what the task submits, not what the
// merge queue reconciles the task's one observation row against, and
// closing it on GitHub says nothing about the task. A task with no git
// target (a changelist task, or one not yet given a repo) has nothing to
// compare against, so none of its links is off target.
func PullRequestOffTarget(task Task, ref PullRequestRef) bool {
	if task.Target == nil || task.Target.Owner == "" {
		return false
	}
	if ref.Repo.Same(*task.Target) {
		return false
	}
	for _, w := range task.Writes {
		if ref.Repo.Same(w.Repo) {
			return false
		}
	}
	return true
}

// LinkOffTarget is PullRequestOffTarget for one of task's links: true
// only for a LinkFixes link whose pull request parses and is off target.
// A link that will not parse is left for its readers to report, as they
// already do.
func LinkOffTarget(task Task, l Link) bool {
	if l.Kind != LinkFixes {
		return false
	}
	ref, err := ParsePullRequestRef(l.Target)
	return err == nil && PullRequestOffTarget(task, ref)
}

// WithoutOffTargetPullRequests is task's links with every off-target
// LinkFixes link (LinkOffTarget) dropped, and whether any was.
func WithoutOffTargetPullRequests(task Task) ([]Link, bool) {
	var out []Link
	dropped := false
	for _, l := range task.Links {
		if LinkOffTarget(task, l) {
			dropped = true
			continue
		}
		out = append(out, l)
	}
	if !dropped {
		return task.Links, false
	}
	return out, true
}
