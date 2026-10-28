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

package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// RefusalKind is what a merge GitHub would not perform was refused for,
// sorted by what the caller should do about it -- which is the only
// distinction that matters to the merge queue, and the one GitHub's own
// answer does not make for it: every rule a repository sets on GitHub
// (CODEOWNERS with "Require review from Code Owners", a protection that
// wants approving reviews, a ruleset's required checks or file-path
// reviewers, signed commits, linear history) comes back as the same 405.
type RefusalKind string

const (
	// RefusalApproval is a merge waiting on a review GitHub requires:
	// "Review required", "Waiting on code owner review", "At least N
	// approving reviews", a ruleset's required reviewers. Nothing grain
	// can do resolves it and retrying cannot either; somebody approving
	// the pull request on GitHub does.
	RefusalApproval RefusalKind = "approval"
	// RefusalRequiredChecks is a merge GitHub holds for status checks its
	// rules require and that are failing or have never reported -- which,
	// in a repository whose merge gate is grain CI (ci_mode: grain), are
	// checks grain is not running and does not read.
	RefusalRequiredChecks RefusalKind = "required_checks"
	// RefusalRule is every other rule a repository can set on GitHub:
	// signed commits, linear history, a merge method it does not allow,
	// restricted file paths, a credential without permission to merge.
	// Each is a setting somebody changes, never something a retry fixes.
	RefusalRule RefusalKind = "rule"
	// RefusalTransient is everything this cannot place: a head or base
	// that moved under the call, a mergeability GitHub had not finished
	// computing, a 5xx. Worth a few more tries, and only a few.
	RefusalTransient RefusalKind = "transient"
)

// ClassifyMergeRefusal sorts a MergePullRequest error by GitHub's own
// words. It is the first opinion, and for most refusals the whole of it;
// MergeRequirements is the second, read from the rules themselves, which
// is what names who must approve and which checks are wanted.
//
// Only 405 (and 403 and 422, which GitHub uses for the same rule
// violations on some routes) is ever a rule: a 409 is a head that moved,
// and a 5xx or a transport error is GitHub having a bad minute, both of
// which resolve by themselves. Checks come before approval when GitHub
// names both, because an approval given to a pull request whose required
// checks are red still does not merge it.
func ClassifyMergeRefusal(err error) RefusalKind {
	var e *Error
	if !errors.As(err, &e) {
		return RefusalTransient
	}
	switch e.Status {
	case http.StatusMethodNotAllowed, http.StatusForbidden, http.StatusUnprocessableEntity:
	default:
		return RefusalTransient
	}
	text := strings.ToLower(refusalText(e.Body))
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(text, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("head branch was modified", "base branch was modified", "merge already in progress",
		"pull request is not mergeable", "is not mergeable", "try again"):
		return RefusalTransient
	case has("status check", "required check", "checks are failing", "check is failing",
		"checks are expected", "is expected"):
		return RefusalRequiredChecks
	case has("approving review", "review required", "code owner", "required reviewer",
		"changes requested", "review is required", "reviews are required", "approval"):
		return RefusalApproval
	case has("rule violation", "repository rule", "protected branch", "signed", "signature",
		"linear history", "merge commits are not allowed", "merge method", "not allowed",
		"file path", "restricted", "resource not accessible", "must not contain",
		"deployment", "merge queue", "permission"):
		return RefusalRule
	}
	// Words this cannot place: still a refusal, but not one worth naming
	// a rule for. The bounded retry is what decides whether it was one.
	return RefusalTransient
}

// IsMergeRuleStatus reports whether err is GitHub refusing a merge with a
// status it uses for its rules (405, 403, 422) rather than for a race
// (409) or an outage (5xx) -- the refusals the rules themselves can be
// asked about.
func IsMergeRuleStatus(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Status {
	case http.StatusMethodNotAllowed, http.StatusForbidden, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// refusalText is GitHub's message out of a refusal body, with whatever
// detail it lists beside it (`errors`), or the body itself when it is not
// the JSON GitHub usually sends.
func refusalText(body []byte) string {
	var parsed struct {
		Message string            `json:"message"`
		Errors  []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Message == "" {
		return string(body)
	}
	parts := []string{parsed.Message}
	for _, raw := range parsed.Errors {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			parts = append(parts, s)
			continue
		}
		var o struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &o) == nil && o.Message != "" {
			parts = append(parts, o.Message)
		}
	}
	return strings.Join(parts, "\n")
}

// RefusalMessage is GitHub's own sentence out of a refused merge, whole --
// not cut at 200 bytes the way Error renders it, since a ruleset refusal
// lists every rule it violated and the one that matters is often last.
func RefusalMessage(err error) string {
	var e *Error
	if errors.As(err, &e) {
		if text := strings.TrimSpace(refusalText(e.Body)); text != "" {
			return text
		}
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// MergeRequirements is what GitHub's rules ask of one pull request before
// it may merge, and how much of it has been given: read from the
// repository's rulesets for the base branch, its classic branch
// protection, and the pull request's own reviews and review requests.
//
// Every field is best effort. A credential that may read the pull request
// is not always allowed to read the rules that protect it, and a
// requirement nobody could read is left empty rather than guessed -- the
// refusal's own words are still there to quote.
type MergeRequirements struct {
	// ApprovalsRequired is the largest number of approving reviews any
	// rule asks for; zero where none could be read.
	ApprovalsRequired int
	// CodeOwnerReview is a rule requiring review from the code owners of
	// the files the pull request touches (CODEOWNERS).
	CodeOwnerReview bool
	// Rulesets names the rulesets whose rules ask for a review.
	Rulesets []string
	// Approvers is who has an approving review standing on the pull
	// request (the latest review by each person, as GitHub counts them),
	// and ChangesRequestedBy who has asked for changes instead.
	Approvers          []string
	ChangesRequestedBy []string
	// RequestedUsers and RequestedTeams are the review requests still
	// outstanding -- the code owners GitHub requested on its own, and
	// anybody a ruleset's required reviewers names. "@org/team" for a
	// team.
	RequestedUsers []string
	RequestedTeams []string
	// RequiredChecks is every status check a rule requires for the base
	// branch, by the name it reports under.
	RequiredChecks []string
	// OtherRules is every other rule type a ruleset applies to the base
	// branch, in GitHub's spelling ("required_signatures",
	// "required_linear_history", "file_path_restriction", ...).
	OtherRules []string
}

// ApprovalSignature is a short, stable statement of the review state:
// who has approved and who has asked for changes. The merge queue keeps
// the one it saw when GitHub refused for want of an approval and asks for
// the merge again when it changes, which is how an approval arriving
// re-arms the merge without the queue retrying blind in between.
func (r MergeRequirements) ApprovalSignature() string {
	return "approved:" + strings.Join(r.Approvers, ",") +
		";changes:" + strings.Join(r.ChangesRequestedBy, ",")
}

// MergeRulesReader is the optional half of Client that reads
// MergeRequirements. Optional on purpose: every fake Client in this
// repository would otherwise need a method only the merge queue's
// refusal handling calls, and a client without it still has the
// refusal's own words to classify by.
type MergeRulesReader interface {
	MergeRequirements(owner, repo string, number int, base string) (MergeRequirements, error)
}

var _ MergeRulesReader = (*RESTClient)(nil)

// MergeRequirements reads what GitHub's rules ask of pull request number
// before it may merge into base. Each of the four reads is independent and
// best effort; the error is the first that failed, returned beside
// whatever the others answered, so a caller that can use half an answer
// does.
func (c *RESTClient) MergeRequirements(owner, repo string, number int, base string) (MergeRequirements, error) {
	var req MergeRequirements
	var errs []error
	errs = append(errs, c.readBranchRules(owner, repo, base, &req))
	errs = append(errs, c.readBranchProtection(owner, repo, base, &req))
	errs = append(errs, c.readReviews(owner, repo, number, &req))
	errs = append(errs, c.readRequestedReviewers(owner, repo, number, &req))
	req.RequiredChecks = uniqueSorted(req.RequiredChecks)
	req.OtherRules = uniqueSorted(req.OtherRules)
	req.Rulesets = uniqueSorted(req.Rulesets)
	for _, err := range errs {
		if err != nil {
			return req, err
		}
	}
	return req, nil
}

// readBranchRules reads every ruleset rule that applies to branch
// (GET /repos/{owner}/{repo}/rules/branches/{branch}), which needs only
// read access, and names the rulesets that ask for a review.
func (c *RESTClient) readBranchRules(owner, repo, branch string, req *MergeRequirements) error {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/rules/branches/%s?per_page=100", owner, repo, quoteAll(branch)))
	if err != nil {
		return err
	}
	if resp.Status == http.StatusNotFound {
		return nil
	}
	if resp.Status != http.StatusOK {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var rules []struct {
		Type       string `json:"type"`
		RulesetID  int    `json:"ruleset_id"`
		Parameters struct {
			RequiredApprovingReviewCount int  `json:"required_approving_review_count"`
			RequireCodeOwnerReview       bool `json:"require_code_owner_review"`
			RequiredReviewers            []struct {
				MinimumApprovals int `json:"minimum_approvals"`
			} `json:"required_reviewers"`
			RequiredStatusChecks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(resp.Body, &rules); err != nil {
		return err
	}
	var reviewRulesets []int
	for _, rule := range rules {
		switch rule.Type {
		case "pull_request":
			p := rule.Parameters
			req.ApprovalsRequired = max(req.ApprovalsRequired, p.RequiredApprovingReviewCount)
			req.CodeOwnerReview = req.CodeOwnerReview || p.RequireCodeOwnerReview
			for _, r := range p.RequiredReviewers {
				req.ApprovalsRequired = max(req.ApprovalsRequired, r.MinimumApprovals)
			}
			if p.RequiredApprovingReviewCount > 0 || p.RequireCodeOwnerReview || len(p.RequiredReviewers) > 0 {
				reviewRulesets = append(reviewRulesets, rule.RulesetID)
			}
		case "required_status_checks":
			for _, check := range rule.Parameters.RequiredStatusChecks {
				req.RequiredChecks = append(req.RequiredChecks, check.Context)
			}
		case "creation", "update", "deletion", "non_fast_forward":
			// About pushes, not about merging a pull request.
		default:
			req.OtherRules = append(req.OtherRules, rule.Type)
		}
	}
	for _, id := range reviewRulesets {
		if id == 0 {
			continue
		}
		name := fmt.Sprintf("ruleset %d", id)
		if got, err := c.rulesetName(owner, repo, id); err == nil && got != "" {
			name = got
		}
		req.Rulesets = append(req.Rulesets, name)
	}
	return nil
}

func (c *RESTClient) rulesetName(owner, repo string, id int) (string, error) {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/rulesets/%d", owner, repo, id))
	if err != nil {
		return "", err
	}
	if resp.Status != http.StatusOK {
		return "", &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return "", err
	}
	return data.Name, nil
}

// readBranchProtection reads the classic protection's required checks off
// the branch itself, which -- unlike /protection -- does not need admin.
func (c *RESTClient) readBranchProtection(owner, repo, branch string, req *MergeRequirements) error {
	resp, err := c.getBranch(owner, repo, branch)
	if err != nil {
		return err
	}
	if resp.Status == http.StatusNotFound {
		return nil
	}
	if resp.Status != http.StatusOK {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		Protection struct {
			RequiredStatusChecks struct {
				Contexts []string `json:"contexts"`
				Checks   []struct {
					Context string `json:"context"`
				} `json:"checks"`
			} `json:"required_status_checks"`
		} `json:"protection"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return err
	}
	req.RequiredChecks = append(req.RequiredChecks, data.Protection.RequiredStatusChecks.Contexts...)
	for _, check := range data.Protection.RequiredStatusChecks.Checks {
		req.RequiredChecks = append(req.RequiredChecks, check.Context)
	}
	return nil
}

// readReviews keeps the latest review by each reviewer, the way GitHub
// counts them toward a requirement: a comment after an approval does not
// withdraw it, and a dismissal does.
func (c *RESTClient) readReviews(owner, repo string, number int, req *MergeRequirements) error {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100", owner, repo, number))
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var reviews []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(resp.Body, &reviews); err != nil {
		return err
	}
	latest := map[string]string{}
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			latest[r.User.Login] = r.State
		}
	}
	for login, state := range latest {
		switch state {
		case "APPROVED":
			req.Approvers = append(req.Approvers, login)
		case "CHANGES_REQUESTED":
			req.ChangesRequestedBy = append(req.ChangesRequestedBy, login)
		}
	}
	sort.Strings(req.Approvers)
	sort.Strings(req.ChangesRequestedBy)
	return nil
}

func (c *RESTClient) readRequestedReviewers(owner, repo string, number int, req *MergeRequirements) error {
	resp, err := c.get(owner, repo, fmt.Sprintf("/repos/%s/%s/pulls/%d/requested_reviewers", owner, repo, number))
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var data struct {
		Users []struct {
			Login string `json:"login"`
		} `json:"users"`
		Teams []struct {
			Slug string `json:"slug"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return err
	}
	for _, u := range data.Users {
		req.RequestedUsers = append(req.RequestedUsers, "@"+u.Login)
	}
	for _, t := range data.Teams {
		req.RequestedTeams = append(req.RequestedTeams, "@"+owner+"/"+t.Slug)
	}
	sort.Strings(req.RequestedUsers)
	sort.Strings(req.RequestedTeams)
	return nil
}

func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
