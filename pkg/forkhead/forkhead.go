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

// Package forkhead resolves a GitHub fork head label typed as a task's
// branch ("andrzejfudro:chore/bump-agy-1.4.9", model.ForkHead) to the
// open pull request it heads on the task's repository, and says what a
// task filed against it is based on, linked to and told. The new-task
// API (pkg/ui) and the proposals a run files (pkg/orchestrator) both
// resolve through it, so a label means the same thing either way.
package forkhead

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Finder is the slice of a GitHub client a label is resolved with.
// github.RESTClient implements it.
type Finder interface {
	FindOpenPullRequestForHead(owner, repo, head string) (*github.PullRequest, error)
	GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error)
}

// ErrNoPullRequest is Resolve's answer for a label no open pull request
// on the repository heads. Its message names the label and the
// repository, so callers can show it as it stands.
var ErrNoPullRequest = errors.New("no open pull request heads it")

// PR is a fork head label resolved against a task's repository.
type PR struct {
	Head   model.ForkHead
	Ref    model.PullRequestRef
	Detail github.PullRequestDetail
	// Base is the branch the task is based on and merges into: the pull
	// request's own head branch when that branch is in the task's
	// repository (grain can push there), and otherwise the branch the
	// pull request merges into.
	Base string
	// InRepo is whether the head branch lives in the task's repository
	// itself rather than in a fork.
	InRepo bool
}

// noPullRequestError is ErrNoPullRequest with a message of its own. It is
// not fmt.Errorf's %w because that appends the sentinel's text, and the
// message already says what the sentinel does: a refusal shown to the
// person who typed the label read "... has it as its head: no open pull
// request heads it" (docs/test-runs/2026-10-31-task-targeting-parking.md).
type noPullRequestError string

func (e noPullRequestError) Error() string { return string(e) }

func (e noPullRequestError) Is(target error) bool { return target == ErrNoPullRequest }

// Resolve finds the open pull request on repo whose head label is head
// -- GitHub's GET /repos/{o}/{r}/pulls?head=<owner>:<branch> -- and
// reads it. No open pull request is an error wrapping ErrNoPullRequest.
func Resolve(f Finder, repo model.RepoRef, head model.ForkHead) (*PR, error) {
	found, err := f.FindOpenPullRequestForHead(repo.Owner, repo.Name, head.String())
	if err != nil {
		return nil, fmt.Errorf("looking up the pull request %s heads on %s: %w", head, repo, err)
	}
	if found == nil || found.Number <= 0 {
		return nil, noPullRequestError(fmt.Sprintf("%q is a GitHub fork head (branch %s on %s's fork), but no open pull request "+
			"on %s has it as its head", head.String(), head.Branch, head.Owner, repo))
	}
	detail, err := f.GetPullRequest(repo.Owner, repo.Name, found.Number)
	if err != nil {
		return nil, fmt.Errorf("reading %s#%d, the pull request %s heads: %w", repo, found.Number, head, err)
	}
	r := &PR{Head: head, Ref: model.PullRequestRef{Repo: repo, Number: found.Number}, Detail: detail}
	r.InRepo = strings.EqualFold(head.Owner, repo.Owner) &&
		(detail.HeadRepo == "" || strings.EqualFold(detail.HeadRepo, repo.String()))
	r.Base = detail.BaseRef
	if r.InRepo {
		r.Base = head.Branch
	}
	if r.Base != "" && !model.ValidBranchName(r.Base) {
		return nil, fmt.Errorf("%q heads %s, whose branch %q is not one grain can check out", head.String(), r.Ref, r.Base)
	}
	return r, nil
}

// Link is the task link that ties a task filed against the label to the
// pull request it heads: grain's external-pull-request link, which points
// the run's pull-request tools at it until the task has its own.
func (r *PR) Link() model.Link {
	return model.Link{Kind: model.LinkExternalPR, Target: r.Ref.String()}
}

// Note is what a task filed against the label is told about it, appended
// to its body: which pull request it is, what it is based on, and -- for
// a fork -- that grain cannot push the fork's branch, so the work goes on
// a grain branch started from the pull request's head. grain's git proxy
// pushes only to the task's own repository, the same reason a takeover
// of a fork's pull request opens a replacement (orchestrator.headPushable).
func (r *PR) Note() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n---\nThis task was filed against `%s`, the head of pull request %s", r.Head, r.Ref)
	if t := strings.TrimSpace(r.Detail.Title); t != "" {
		fmt.Fprintf(&b, " (%q)", t)
	}
	if r.InRepo {
		fmt.Fprintf(&b, ". Its head branch `%s` is in %s itself, so this task is based on it: "+
			"your branch starts from it and merges into it, updating %s.\n", r.Base, r.Ref.Repo, r.Ref)
		return b.String()
	}
	fork := r.Detail.HeadRepo
	if fork == "" {
		fork = r.Head.Owner + "/" + r.Ref.Repo.Name
	}
	base := "the repository's default branch"
	if r.Base != "" {
		base = "`" + r.Base + "`"
	}
	fmt.Fprintf(&b, ", from the fork %s. grain linked this task to %s and based it on %s, the branch it merges into.\n\n", fork, r.Ref, base)
	if r.Detail.MaintainerCanModify {
		b.WriteString("Its author allows maintainers to edit it, but grain pushes only to branches of " + r.Ref.Repo.String() +
			" itself, never to a fork, ")
	} else {
		b.WriteString("grain cannot push to a fork's branch, ")
	}
	author := "its author"
	if a := strings.TrimSpace(r.Detail.AuthorLogin); a != "" {
		author = "@" + a
	}
	fmt.Fprintf(&b, "so you work on this task's own grain branch instead. Start it from the pull request's head: "+
		"`git fetch origin refs/pull/%d/head` and `git reset --hard FETCH_HEAD` before your first commit. "+
		"Then either open a follow-up pull request from your branch that names %s and credits %s, "+
		"or suggest the changes on %s itself in review comments.\n", r.Ref.Number, r.Ref, author, r.Ref)
	return b.String()
}
