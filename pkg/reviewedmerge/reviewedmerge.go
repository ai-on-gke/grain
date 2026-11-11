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

// Package reviewedmerge answers whether a commit reached a release branch
// through a reviewed merge (docs/security/audits/2026-11-10-insider-risk.md,
// finding 3).
//
// Every hold grain has -- the trust-boundary review, a `human: required`
// trigger -- acts on grain's own pull-request tasks. Somebody with write
// access to the repository and no grain access can merge on the forge, or
// push where branch protection allows it, and skip all of them; the
// release workflow then builds, signs and publishes that commit, and a
// release-key signature on the image shows only that CI built it. This is
// the question that signature does not answer, asked of the forge's own
// records, which nobody who can merge can rewrite:
//
//   - the commit is the merge commit of a pull request merged into one of
//     the policy's branches (a commit pushed straight onto the branch has
//     none), and
//   - either the merge was grain's -- merged by one of the forge logins
//     grain merges as, which only grain's credential can be, and which
//     only happens once grain's own holds have let it go -- or the pull
//     request carries the forge's review record: an approval by somebody
//     other than its author, of the head that was merged, with no change
//     request outstanding. Only reviews by the repository's owner, its
//     organization's members and its collaborators count, as the forge's
//     own required reviews do: any account at all can leave an approving
//     review on a public repository's pull request.
//
// A commit is checked with the first-parent chain that brought it, back to
// the last commit already checked (Policy.CheckChain): a reviewed merge on
// top of a self-merged one would otherwise carry the unreviewed change into
// the release with it.
//
// Two callers ask: scripts/release-images.sh, through cmd/reviewed-merge,
// before it publishes a build, and pkg/upgrade before it runs one. The
// first stops an unreviewed commit being published by an unmodified
// workflow; the second is the one that holds against a commit that also
// rewrote the workflow, because it does not run anything from that commit
// to decide.
//
// Rebase merges are not recognised: only the last rebased commit is a pull
// request's merge commit. The repositories this guards merge with merge
// commits or squash.
package reviewedmerge

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// RecordType is the in-toto predicate type and the first field of every
// Record: what it is and its format.
const RecordType = "https://github.com/ai-on-gke/grain/reviewed-merge/v1"

// Policy is what counts as a reviewed merge for one repository.
type Policy struct {
	// Repo is owner/name on github.com, or host/owner/name on a GitHub
	// Enterprise Server (SplitRepo).
	Repo string
	// Branches are the branches a release is built from, as path.Match
	// patterns: "main", "release/v*". A pull request merged anywhere else
	// did not reach a release branch.
	Branches []string
	// Mergers are the forge logins grain merges as. A merge by one of
	// them went through grain's Submit or auto-merge, and so through
	// every hold grain puts in front of those. Empty, only the forge's
	// review record counts.
	Mergers []string
}

// Pull is the part of a pull request this reads.
type Pull struct {
	Number         int
	State          string
	Merged         bool
	MergeCommitSHA string
	Base           string
	HeadSHA        string
	Author         string
	MergedBy       string
}

// Review is one pull-request review.
type Review struct {
	Login    string
	State    string // APPROVED, CHANGES_REQUESTED, DISMISSED, COMMENTED
	CommitID string
	// Association is the reviewer's relation to the repository, the
	// forge's author_association: OWNER, MEMBER, COLLABORATOR,
	// CONTRIBUTOR, NONE and so on.
	Association string
}

// countsAsReviewer is whether a review's author is one whose review the
// forge would count toward a required review: anybody else's approval
// is any account's, and so is nobody's.
func countsAsReviewer(association string) bool {
	switch strings.ToUpper(association) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return false
}

// Forge reads the records the check is made from.
type Forge interface {
	// PullsForCommit lists the pull requests a commit is associated with.
	PullsForCommit(ctx context.Context, repo, sha string) ([]Pull, error)
	// Reviews lists a pull request's reviews, oldest first.
	Reviews(ctx context.Context, repo string, number int) ([]Review, error)
	// FirstParent is a commit's first parent, "" for a root commit.
	FirstParent(ctx context.Context, repo, sha string) (string, error)
}

// Merge is how one commit reached its branch.
type Merge struct {
	Commit string `json:"commit"`
	Pull   int    `json:"pull"`
	Base   string `json:"base"`
	// Via is "grain" for a merge grain made, "review" for one the forge's
	// review record covers.
	Via string `json:"via"`
	// By is the login that merged (Via "grain") or approved (Via "review").
	By string `json:"by"`
}

// Record is what a release build writes into its image's attestation: the
// commit, the commit before it that was already checked, and how every
// commit between reached the branch.
type Record struct {
	Type   string  `json:"type"`
	Repo   string  `json:"repo"`
	Commit string  `json:"commit"`
	Since  string  `json:"since,omitempty"`
	Merges []Merge `json:"merges"`
}

func (p Policy) branchAllowed(base string) bool {
	for _, pattern := range p.Branches {
		if ok, _ := path.Match(pattern, base); ok {
			return true
		}
	}
	return false
}

func (p Policy) isMerger(login string) bool {
	for _, m := range p.Mergers {
		if login != "" && strings.EqualFold(m, login) {
			return true
		}
	}
	return false
}

// Check answers for one commit: how it reached a release branch, or why it
// did not reach one through a reviewed merge.
func (p Policy) Check(ctx context.Context, f Forge, sha string) (Merge, error) {
	pulls, err := f.PullsForCommit(ctx, p.Repo, sha)
	if err != nil {
		return Merge{}, fmt.Errorf("reading the pull requests for %s: %w", short(sha), err)
	}
	var delivered []Pull
	for _, pr := range pulls {
		if pr.Merged && pr.MergeCommitSHA == sha && p.branchAllowed(pr.Base) {
			delivered = append(delivered, pr)
		}
	}
	if len(delivered) == 0 {
		return Merge{}, fmt.Errorf("%s is not the merge commit of any pull request merged into %s: it was pushed onto the branch, not merged through a pull request",
			short(sha), strings.Join(p.Branches, " or "))
	}
	var why []string
	for _, pr := range delivered {
		if p.isMerger(pr.MergedBy) {
			return Merge{Commit: sha, Pull: pr.Number, Base: pr.Base, Via: "grain", By: pr.MergedBy}, nil
		}
		approver, reason, err := p.approvedBy(ctx, f, pr)
		if err != nil {
			return Merge{}, err
		}
		if approver != "" {
			return Merge{Commit: sha, Pull: pr.Number, Base: pr.Base, Via: "review", By: approver}, nil
		}
		why = append(why, fmt.Sprintf("pull request #%d was merged by %s, %s", pr.Number, orNobody(pr.MergedBy), reason))
	}
	return Merge{}, fmt.Errorf("%s did not reach %s through a reviewed merge: %s", short(sha), delivered[0].Base, strings.Join(why, "; "))
}

// approvedBy is the forge's review record for pr: who approved its merged
// head, if anybody but its author did and nobody's change request is
// outstanding.
func (p Policy) approvedBy(ctx context.Context, f Forge, pr Pull) (string, string, error) {
	reviews, err := f.Reviews(ctx, p.Repo, pr.Number)
	if err != nil {
		return "", "", fmt.Errorf("reading the reviews of pull request #%d: %w", pr.Number, err)
	}
	// The latest of each reviewer's deciding reviews, the way the forge
	// counts them toward a requirement: a comment after an approval does
	// not withdraw it, and a dismissal does. A review by somebody with no
	// standing in the repository counts for nothing either way.
	latest := map[string]Review{}
	var order []string
	for _, r := range reviews {
		if !countsAsReviewer(r.Association) {
			continue
		}
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			key := strings.ToLower(r.Login)
			if _, seen := latest[key]; !seen {
				order = append(order, key)
			}
			latest[key] = r
		}
	}
	approver, stale := "", ""
	for _, key := range order {
		r := latest[key]
		switch {
		case r.State == "CHANGES_REQUESTED":
			return "", fmt.Sprintf("and %s's request for changes was still outstanding", r.Login), nil
		case r.State != "APPROVED" || strings.EqualFold(r.Login, pr.Author):
		case pr.HeadSHA != "" && r.CommitID != pr.HeadSHA:
			stale = r.Login
		case approver == "":
			approver = r.Login
		}
	}
	if approver != "" {
		return approver, "", nil
	}
	if stale != "" {
		return "", fmt.Sprintf("and %s's approval was of an earlier head than the %s that was merged", stale, short(pr.HeadSHA)), nil
	}
	return "", "with no approval from an owner, member or collaborator other than its author, and not by grain", nil
}

// CheckChain checks sha and every first-parent ancestor of it back to, and
// not including, since -- the commit already checked, such as the one the
// branch's published image was built from. An empty since checks sha
// alone. At most max commits are walked; a chain longer than that, or one
// that never reaches since, is refused rather than checked in part.
func (p Policy) CheckChain(ctx context.Context, f Forge, sha, since string, max int) (Record, error) {
	rec := Record{Type: RecordType, Repo: p.Repo, Commit: sha, Since: since}
	at := sha
	for {
		if at == since {
			return rec, nil
		}
		if len(rec.Merges) >= max {
			return Record{}, fmt.Errorf("%s is more than %d first-parent commits past %s, so the chain between them was not checked", short(sha), max, short(since))
		}
		m, err := p.Check(ctx, f, at)
		if err != nil {
			if at != sha {
				return Record{}, fmt.Errorf("%s carries %s, which it was built on: %w", short(sha), short(at), err)
			}
			return Record{}, err
		}
		rec.Merges = append(rec.Merges, m)
		if since == "" {
			return rec, nil
		}
		parent, err := f.FirstParent(ctx, p.Repo, at)
		if err != nil {
			return Record{}, fmt.Errorf("reading %s's first parent: %w", short(at), err)
		}
		if parent == "" {
			return Record{}, fmt.Errorf("%s's first-parent chain never reaches %s", short(sha), short(since))
		}
		at = parent
	}
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func orNobody(login string) string {
	if login == "" {
		return "an unknown account"
	}
	return login
}
