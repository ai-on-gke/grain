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
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Branch is one branch a human has asked grain to know about on a repo --
// bwsalmon/agents#638's own "Add the ability to create a new branch on a
// repo from the repo page. This should just create the given branch in
// github," plus grain/task-176's own "if a branch already exists on a
// repo, we should be able to add it to grain." Name is exactly the ref a
// human typed; Status walks pending -> created (grain cut the ref) or
// pending -> adopted (the ref was already there), the same declarative-
// intent-then-reconcile shape Release already holds to.
type Branch struct {
	ID   int64
	Repo RepoRef
	Name string
	// From is the branch Name is to be cut from, when grain is the one
	// to create it -- grain/task-2317's own "create a new branch, based
	// on the selected (main by default)". "" means the repo's default
	// branch, which is what every request made before From existed
	// meant. It says nothing about a Name already on GitHub: that one is
	// adopted as it stands, wherever it was cut from.
	From      string
	Status    BranchStatus
	CreatedAt time.Time
	// LastError is the branches reconciler's own account of why Name has
	// not been created on GitHub yet, cleared the instant that succeeds.
	LastError string
}

// BranchStatus is where one requested branch is in its own lifecycle.
type BranchStatus string

const (
	// BranchPending is a freshly requested branch, before the branches
	// reconciler has looked for it on GitHub.
	BranchPending BranchStatus = "pending"
	// BranchCreated is a branch confirmed live on GitHub, cut by the
	// branches reconciler itself.
	BranchCreated BranchStatus = "created"
	// BranchAdopted is a branch that was already live on GitHub when the
	// reconciler went to create it -- grain/task-176's own "if a branch
	// already exists on a repo, we should be able to add it to grain."
	//
	// It is as terminal as BranchCreated, and means the same thing to
	// everything downstream: the name is on GitHub and grain has a row
	// for it. The two are kept apart because only one of them says grain
	// made the ref, and somebody who typed a name expecting a fresh
	// branch and got a colleague's existing one would otherwise never be
	// told which they now have.
	BranchAdopted BranchStatus = "adopted"
)

// branchNameSafe is what a branch Name has to match to be safe to hand
// GitHub's own git-database API -- the same charset orchestrator's own
// gitSafe (checkout.go) already requires of a task's target branch,
// including '/' for a name like "feature/foo", unlike
// releaseNameSafe, which excludes it since a release name is always one
// path segment.
var branchNameSafe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// ValidBranchName reports whether name is safe to create on GitHub as a
// ref -- non-empty, drawn from branchNameSafe's own charset, and free of
// the "..", leading/trailing or doubled "/" that a git ref itself
// refuses.
func ValidBranchName(name string) bool {
	if !branchNameSafe.MatchString(name) {
		return false
	}
	return !strings.Contains(name, "..") && !strings.Contains(name, "//") && !strings.HasSuffix(name, "/")
}

// ErrInvalidBranchName is returned by CreateBranch when name is empty or
// contains something unsafe to create on GitHub as a ref.
var ErrInvalidBranchName = errors.New("branch: invalid branch name")

// ForkHead is GitHub's head label for a pull request from a fork,
// "<fork-owner>:<branch>" -- what a pull request's page shows as its
// head, and what people copy into a task's branch field. It is not a
// branch name (':' is not legal in a git ref); it names Branch on
// Owner's fork of the task's repository, and grain resolves it to the
// open pull request it heads.
type ForkHead struct {
	Owner  string
	Branch string
}

func (h ForkHead) String() string { return h.Owner + ":" + h.Branch }

// githubLogin is a GitHub user or organisation login: up to 39
// alphanumerics or single hyphens, neither leading nor trailing.
var githubLogin = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// forkHeadShape is what reads as an attempt at a fork head label: a ':'
// among nothing but the characters of a login and a branch name.
var forkHeadShape = regexp.MustCompile(`^[A-Za-z0-9._/:-]*:[A-Za-z0-9._/:-]*$`)

// ParseForkHead reads s as a fork head label. ok is false, with no
// error, for anything without a ':' -- a plain branch name, which the
// caller validates as it always has -- and for a string with characters
// no label or branch has ("bad ~^:"), which the same validation refuses
// as the bad ref it is. A string shaped like a label that is not exactly
// one GitHub login, one ':' and one valid branch name is an error saying
// what is wrong with it.
func ParseForkHead(s string) (head ForkHead, ok bool, err error) {
	if !forkHeadShape.MatchString(s) {
		return ForkHead{}, false, nil
	}
	owner, branch, _ := strings.Cut(s, ":")
	switch {
	case strings.Contains(branch, ":"):
		return ForkHead{}, true, fmt.Errorf("%q has more than one ':': a fork head is <owner>:<branch>", s)
	case owner == "" || branch == "":
		return ForkHead{}, true, fmt.Errorf("%q is not a fork head: it needs both an owner and a branch, as <owner>:<branch>", s)
	case !githubLogin.MatchString(owner) || strings.Contains(owner, "--"):
		return ForkHead{}, true, fmt.Errorf("%q is not a fork head: %q is not a GitHub login", s, owner)
	case !ValidBranchName(branch):
		return ForkHead{}, true, fmt.Errorf("%q is not a fork head: %q is not a usable branch name", s, branch)
	}
	return ForkHead{Owner: owner, Branch: branch}, true, nil
}
