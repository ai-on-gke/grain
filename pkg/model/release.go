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

// Release is one named release branch set -- bwsalmon/agents#571's
// replacement for bwsalmon/agents#398's single prod/rc ReleaseConfig per
// repo. Name is whatever a human chose when creating it ("myfeat", a
// feature branch's own name, or "2.1", a system version) and is what
// LatestBranch and ProdBranch are both derived from, so a release named
// "myfeat" always means exactly the branches "myfeat.latest" and
// "myfeat"; a release named "2.1" means "2.1.latest" and "2.1". Unlike
// ReleaseConfig, which was one singleton row per repo, a repo may have
// any number of Releases at once, each with its own name and its own
// independent sequence of candidates -- a feature release and a system
// release can be in flight side by side.
//
// Status walks provisioning -> active -> merge_requested -> merged,
// monotonically. A repo may have at most one Release by a given Name that
// has not yet reached ReleaseMerged (Store.CreateRelease's own
// ErrReleaseNameInUse) -- once merged, that Name is free again for
// "start a new release" (the issue's own "you merge it back to default
// and create a new release branch") to reuse, most often for the next
// version in the same line.
type Release struct {
	ID        int64
	Repo      RepoRef
	Name      string
	Status    ReleaseStatus
	CreatedAt time.Time
	MergedAt  *time.Time
	// PullRequestURL is the merge-back pull request RequestReleaseMerge's
	// own reconciler opened -- empty until Status reaches ReleaseMerged,
	// and empty at ReleaseMerged too for a release that had nothing to
	// merge back (MergeNote).
	PullRequestURL string
	// MergeNote is why this release reached ReleaseMerged with no pull
	// request of its own: its ProdBranch carried no commits the default
	// branch did not already have, so GitHub had nothing to open one
	// from. Empty on the ordinary path, where PullRequestURL names the
	// pull request instead -- exactly one of the two is ever set, and
	// which one is what a release page shows under "merge back to
	// default" (Store.MarkReleaseNothingToMerge).
	//
	// Not LastError: a release in this shape has nothing wrong with it
	// and nothing left to retry, and a 422 quoted onto LastError is both
	// (see orchestrator's own requestMergeOnGitHub).
	MergeNote string
	// LastError is the releases reconciler's own account of why
	// provisioning or a requested merge has not landed yet, cleared the
	// instant that step succeeds -- Candidate's own LastError, applied to
	// a release rather than one of its candidates.
	LastError string
}

// LatestBranch is the moving branch every agent commit against this
// release lands on -- the issue's own "All agent commits go here."
func (r Release) LatestBranch() string { return r.Name + ".latest" }

// ProdBranch is the release's own permanent branch, promoted to from a
// candidate once it is sufficiently tested, and eventually merged back
// into the repo's own default branch -- the issue's own "A prod branch:
// myfeat." Name, unadorned: a release named "2.1" promotes to a branch
// called exactly "2.1", the same as the issue's own "system release 2.1"
// example.
func (r Release) ProdBranch() string { return r.Name }

// ReleaseStatus is where one release's own branch set is in its
// lifecycle.
type ReleaseStatus string

const (
	// ReleaseProvisioning is a freshly created release, before the
	// releases reconciler has created LatestBranch on GitHub.
	ReleaseProvisioning ReleaseStatus = "provisioning"
	// ReleaseActive is a provisioned release: LatestBranch exists, and its
	// candidates can be cut and promoted.
	ReleaseActive ReleaseStatus = "active"
	// ReleaseMergeRequested is a requested merge of ProdBranch back into
	// the repo's own default branch, before the reconciler has opened the
	// pull request.
	ReleaseMergeRequested ReleaseStatus = "merge_requested"
	// ReleaseMerged is a release whose merge-back pull request has been
	// opened -- PullRequestURL names it -- or one that turned out to need
	// no such pull request at all, its ProdBranch carrying nothing the
	// default branch did not already have (MergeNote). Terminal: nothing
	// here polls GitHub for whether that pull request has since been merged,
	// the same way nothing here polls a task's own PR once one is open --
	// a human merges it (or doesn't) on GitHub itself, and this Name is
	// free again the moment this status is reached.
	ReleaseMerged ReleaseStatus = "merged"
)

// CandidateStatus is where one release candidate is in its own
// lifecycle.
type CandidateStatus string

const (
	// CandidateCutting is a freshly requested cut, before the reconciler
	// has created its branch on GitHub.
	CandidateCutting CandidateStatus = "cutting"
	// CandidateActive is a cut candidate with its branch live on GitHub,
	// not yet promoted -- the issue's own "current rc branch."
	CandidateActive CandidateStatus = "active"
	// CandidatePromoting is a requested promotion, before the reconciler
	// has moved the prod branch.
	CandidatePromoting CandidateStatus = "promoting"
	// CandidatePromoted is a retired candidate: promoted once, and --
	// mirroring bwsalmon/agents#398's own "it cannot be promoted twice"
	// -- never again. A release's own candidates promote one after
	// another as testing turns up fixes that land on LatestBranch and get
	// cut into a fresh rc; only the most recently cut one can ever be
	// active at once (Store.CutCandidate's own ErrCandidateActive).
	CandidatePromoted CandidateStatus = "promoted"
)

// Candidate is one rc cut for a Release.
//
// Number is an incrementing count scoped to ReleaseID alone, starting
// back at 1 for every fresh Release -- unlike bwsalmon/agents#398's own
// Candidate.Number, which never reset for the life of a repo's single
// ReleaseConfig. Branch is RCBranch(release name, Number), cut from that
// release's own LatestBranch -- the issue's own "myfeat.rc.X" -- and
// never reassigned afterwards. There is no separate "release branch" the
// way bwsalmon/agents#398 cut one at promotion time: promoting moves the
// release's own ProdBranch forward, and that is the only permanent branch
// a promotion touches.
type Candidate struct {
	ID         int64
	Repo       RepoRef
	ReleaseID  int64
	Number     int
	Branch     string
	Status     CandidateStatus
	CreatedAt  time.Time
	PromotedAt *time.Time
	LastError  string
}

// RCBranch renders one candidate's own branch name -- "myfeat.rc.3" --
// from its release's own Name and its Number within that release.
func RCBranch(releaseName string, number int) string {
	return fmt.Sprintf("%s.rc.%d", releaseName, number)
}

// releaseNameSafe is what a release Name has to match: the same
// alphanumeric-plus-punctuation shape a git ref component already
// requires (orchestrator's own gitSafe, minus '/', since a release name
// is one path segment, never several), starting with a letter or digit
// so ".latest" itself can never be mistaken for one.
var releaseNameSafe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// rcSuffix matches the tail RCBranch appends -- a name ending in it, or
// in ".latest", could never be told apart from a branch this package
// itself derives from some other release's own Name.
var rcSuffix = regexp.MustCompile(`\.rc\.[0-9]+$`)

// ValidReleaseName reports whether name is safe to derive LatestBranch,
// ProdBranch and every RCBranch from.
//
// The charset alone is not enough: git check-ref-format also refuses a
// ".." anywhere, a ref ending in "." and a component ending in ".lock"
// -- and the bare name is ProdBranch itself -- so "v3.0..x", "v3." and
// "v3.lock" would each be stored as a release whose branches GitHub can
// never create (2026-10-06 walkthrough; ValidBranchName refuses ".." the
// same way). git check-ref-format --branch refuses a branch named
// exactly "HEAD" too, so a release by that name is refused for the same
// reason (TestValidReleaseNameAgreesWithGit checks all of it against git
// itself).
func ValidReleaseName(name string) bool {
	if !releaseNameSafe.MatchString(name) || name == "HEAD" {
		return false
	}
	if strings.Contains(name, "..") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return false
	}
	return !strings.HasSuffix(name, ".latest") && !rcSuffix.MatchString(name)
}

// ErrInvalidReleaseName is returned by CreateRelease when name is empty,
// contains something unsafe to put in a branch name, or collides with the
// ".latest"/".rc.N" suffixes this package itself derives branch names
// with.
var ErrInvalidReleaseName = errors.New("release: invalid release name")

// ErrReleaseNameInUse is returned by CreateRelease when repo already has
// a Release by that name which has not yet merged.
var ErrReleaseNameInUse = errors.New("release: repo already has an unmerged release with this name")

// ErrNoRelease is returned by CutCandidate, PromoteCandidate and
// RequestReleaseMerge when repo has no release by the given name.
var ErrNoRelease = errors.New("release: no such release")

// ErrReleaseNotActive is returned by CutCandidate and PromoteCandidate
// when the named release has not finished provisioning yet, or has
// already had its merge back requested.
var ErrReleaseNotActive = errors.New("release: release is not active")

// ErrReleaseAlreadyMergeRequested is returned by RequestReleaseMerge for
// a release that is not ReleaseActive -- either its merge was already
// requested, or it has not been provisioned yet.
var ErrReleaseAlreadyMergeRequested = errors.New("release: merge back to the default branch was already requested")

// ErrCandidateActive is returned by CutCandidate when the named release
// already has a candidate that has not been promoted -- "the current rc"
// is singular within one release, so a fresh cut has to wait for that one
// to promote first.
var ErrCandidateActive = errors.New("release: release already has an unpromoted candidate")

// ErrNoCandidate is returned by PromoteCandidate when the named release
// has never had a candidate cut.
var ErrNoCandidate = errors.New("release: release has no candidate")

// ErrCandidateNotReady is returned by PromoteCandidate when the current
// candidate's branch has not finished cutting yet.
var ErrCandidateNotReady = errors.New("release: current candidate has not finished cutting yet")

// ErrCandidateSuperseded is returned by PromoteCandidateIfCurrent when
// the candidate it was asked to promote is no longer its release's
// current one -- a newer candidate has been cut since the caller read it.
var ErrCandidateSuperseded = errors.New("release: candidate is no longer the release's current one")

// ErrAlreadyPromoted is returned by PromoteCandidate for the case
// bwsalmon/agents#398 first called out by name: "it cannot be promoted
// twice."
var ErrAlreadyPromoted = errors.New("release: current candidate was already promoted")
