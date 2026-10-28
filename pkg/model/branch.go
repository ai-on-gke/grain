package model

import (
	"errors"
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
