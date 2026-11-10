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

package staterepo

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
)

// require_verified mode (task 3973, insider-risk finding 1d).
//
// With an external config repository, whatever lands on its branch is
// imported on the next tick: host scripts in mcp_servers, scope sets,
// secret bindings. Nobody's grain Submit is involved, so anybody who may
// push to that branch on the forge is an administrator of the deployment.
// A Verifier narrows that to commits grain can vouch for: one that the
// pull request it merged was approved on the forge, merged by grain's own
// forge account, signed by an allowed key, or accepted by an operator
// here. PullExternal moves the working tree only across commits that
// pass, and holds the first one that does not.

// verifiedHeadRef is the commit PullExternal last chose to check out in
// require_verified mode.
const verifiedHeadRef = "refs/grain/verified-head"

// ErrHeld is PullExternal's answer when there is no verified commit to
// apply at all: the repository was cloned at, or first named at, a commit
// that does not verify. Nothing is imported; Held names the commit.
var ErrHeld = errors.New("staterepo: the external config repository's commit is held: it is not verified")

// ForgeReview is what the forge says about the pull request whose merge
// put a commit on the branch.
type ForgeReview struct {
	Number int
	URL    string
	// Author opened the pull request; MergedBy merged it.
	Author   string
	MergedBy string
	// Approvers are the reviewers, other than Author, whose latest
	// review approves the pull request's final head commit.
	Approvers []string
}

// ForgeReviews looks up the pull request a commit on the branch is the
// merge of. A commit that is no pull request's merge is (nil, nil).
type ForgeReviews interface {
	MergedReview(ctx context.Context, sha string) (*ForgeReview, error)
}

// Verifier is require_verified mode's configuration (Settings), in the
// form PullExternal checks it.
type Verifier struct {
	// AllowedSignersFile is an ssh allowed_signers file (git's
	// gpg.ssh.allowedSignersFile); a commit with a good signature by a key
	// in it verifies. Empty checks no signatures.
	AllowedSignersFile string
	// Reviews reads forge reviews; nil when the remote is not on a forge
	// grain can ask.
	Reviews ForgeReviews
	// RequiredApprovals is how many approvals a pull request needs; less
	// than one means one.
	RequiredApprovals int
	// Mergers are the forge accounts grain merges pull requests as: a
	// pull request one of them merged went through grain's own merge
	// queue, which a person's Submit releases.
	Mergers []string
	// Accepted are commits an operator holding an unlimited role accepted
	// by name (StateRepoRemoteRequest.AcceptCommit).
	Accepted []string
}

// HeldCommit is the commit require_verified mode is holding, and why.
type HeldCommit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject,omitempty"`
	Reason  string    `json:"reason"`
	Since   time.Time `json:"since"`
}

// SetVerifier turns require_verified mode on (v non-nil) or off.
func (r *Repo) SetVerifier(v *Verifier) {
	r.verifier = v
	if v == nil {
		r.held = nil
	}
}

// Held is the commit require_verified mode is holding, or nil.
func (r *Repo) Held() *HeldCommit {
	if r.held == nil {
		return nil
	}
	h := *r.held
	return &h
}

// verifiedTarget is the commit PullExternal may check out with tip
// fetched: the newest commit on tip's first-parent line, from the last
// one applied, up to which every commit verifies. The first that does not
// is recorded in r.held. "" is no commit at all.
func (r *Repo) verifiedTarget(ctx context.Context, tip string) (string, error) {
	base, err := r.loadedHead(ctx)
	if err != nil {
		return "", err
	}
	if base != "" {
		if _, err := r.git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", base+"^{commit}"); err != nil {
			base = ""
		}
	}
	if base == tip {
		r.held = nil
		return tip, nil
	}
	var commits []string
	switch {
	case base == "":
		// Nothing applied from this repository yet: its tip is checked on
		// its own, and an operator accepts it by name if it does not pass.
		commits = []string{tip}
	default:
		if _, err := r.git(ctx, "merge-base", "--is-ancestor", "--end-of-options", base, tip); err != nil {
			if slices.Contains(r.verifier.Accepted, tip) {
				// An operator accepted the rewritten tip by name: the
				// Accept a rewrite's hold offers.
				r.held = nil
				return tip, nil
			}
			r.hold(ctx, tip, fmt.Sprintf("the branch no longer contains %s, the last commit applied from it: "+
				"its history was rewritten, so what changed since cannot be checked commit by commit", shortSHA(base)))
			return base, nil
		}
		out, err := r.git(ctx, "rev-list", "--first-parent", "--reverse", "--end-of-options", base+".."+tip)
		if err != nil {
			return "", err
		}
		commits = strings.Fields(out)
	}
	target := base
	for _, c := range commits {
		how, reason := r.verifyCommit(ctx, c)
		if how == "" {
			r.hold(ctx, c, reason)
			return target, nil
		}
		target = c
	}
	r.held = nil
	return target, nil
}

// hold records sha as held, logging it once.
func (r *Repo) hold(ctx context.Context, sha, reason string) {
	if r.held != nil && r.held.SHA == sha && r.held.Reason == reason {
		return
	}
	subject, _ := r.git(ctx, "log", "-1", "--format=%s", "--end-of-options", sha)
	r.held = &HeldCommit{SHA: sha, Subject: strings.TrimSpace(subject), Reason: reason, Since: r.now()}
	log.Printf("staterepo: require_verified: holding %s (%s) on %s: %s; nothing from it on is applied until it verifies "+
		"(docs/external-config-repo.md)", shortSHA(sha), r.held.Subject, r.branch, reason)
}

// verifyCommit says how sha verifies, or "" and why it does not.
func (r *Repo) verifyCommit(ctx context.Context, sha string) (how, reason string) {
	v := r.verifier
	if slices.Contains(v.Accepted, sha) {
		return "accepted by an operator", ""
	}
	var why []string
	if v.AllowedSignersFile != "" {
		// git picks the verifier by the signature's own armor, not by
		// gpg.format, so an OpenPGP or X.509 signature would be checked
		// against whatever keyring this host's user has and could report
		// G too. Only an SSH signature by an allowed_signers key is meant
		// to verify; the other two programs are made to fail.
		out, err := r.git(ctx, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+v.AllowedSignersFile,
			"-c", "gpg.openpgp.program=false", "-c", "gpg.x509.program=false",
			"log", "-1", "--format=%G?", "--end-of-options", sha)
		if err == nil && strings.TrimSpace(out) == "G" {
			return "signed by an allowed key", ""
		}
		why = append(why, "it is not signed by an allowed key")
	} else {
		why = append(why, "no allowed signing keys are configured")
	}
	if v.Reviews == nil {
		why = append(why, "grain cannot read this remote's pull requests")
		return "", strings.Join(why, ", and ")
	}
	review, err := v.Reviews.MergedReview(ctx, sha)
	switch {
	case err != nil:
		why = append(why, fmt.Sprintf("reading its pull request failed (%v)", err))
	case review == nil:
		why = append(why, "it is not the merge of a pull request into "+r.branch)
	case review.MergedBy != "" && slices.Contains(v.Mergers, review.MergedBy):
		return fmt.Sprintf("pull request #%d merged by grain (%s)", review.Number, review.MergedBy), ""
	default:
		need := max(v.RequiredApprovals, 1)
		if len(review.Approvers) >= need {
			return fmt.Sprintf("pull request #%d approved by %s", review.Number, strings.Join(review.Approvers, ", ")), ""
		}
		why = append(why, fmt.Sprintf("pull request #%d has %d of the %d approvals required on its final commit",
			review.Number, len(review.Approvers), need))
	}
	return "", strings.Join(why, ", and ")
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
