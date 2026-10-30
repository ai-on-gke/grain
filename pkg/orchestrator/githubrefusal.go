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

package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// What the merge queue does with a merge GitHub refuses, by what it was
// refused for (github.ClassifyMergeRefusal, sharpened by the rules
// themselves -- github.MergeRequirements).
//
// A repository can protect its branches on GitHub as well as in grain:
// CODEOWNERS with "Require review from Code Owners", a protection wanting
// approving reviews, rulesets with required checks or file-path
// reviewers. grain merges through the API, so GitHub enforces all of it
// on grain's merges, and each kind of refusal wants a different answer:
//
//   - An approval GitHub requires is waited for, visibly
//     (mergeStateWaitingGitHubApproval), out of the queue so the entries
//     behind it go on merging, and re-armed when the pull request's
//     reviews change (holdRefusedMerge). Nothing is retried meanwhile.
//   - Required checks GitHub wants and a rule GitHub enforces are handed
//     to a person once, with the checks or the rule named and what to
//     change. Neither is retried blind: the checks re-arm themselves when
//     GitHub reports them passing, and a rule when a person presses Retry
//     merge or pushes.
//   - Anything else is retried forge.MaxBlindMergeRetries times with
//     backoff and then handed to a person with GitHub's own words.
//
// Approvals on the two sides are separate: a grain Submit, or an approver
// a sensitive path names (.grain/sensitive.yml), does not satisfy a rule
// GitHub enforces, and an approval on GitHub does not stand in for one
// grain asks for.

// mergeRefusalPollInterval is how often a wait on GitHub -- for an
// approval, for required checks -- reads GitHub to see whether it is over.
// A few minutes: an approval is a person's act, and the reads are several.
const mergeRefusalPollInterval = 2 * time.Minute

// approvalBlindRecheck is how long an approval wait goes before asking for
// the merge again regardless, where the reviews could not be read at all
// and so could never be seen to change.
const approvalBlindRecheck = 30 * time.Minute

// awaitingGitHubApproval reports whether ref is waiting on an approval
// GitHub requires: out of its repository's queue (isQueueMember), so it
// holds nobody up while it waits. Read off the refusal record whatever its
// head, because membership is decided before the head is read; a push
// ends the wait on the entry's own turn (holdRefusedMerge).
func awaitingGitHubApproval(ref model.PullRequestRef) bool {
	r, ok := forge.PeekMergeRefusal(forgeRef(ref))
	return ok && r.Kind == github.RefusalApproval
}

// readMergeRequirements asks GitHub what its rules want of ref, where the
// client can say. ok is false where nothing could be read.
func readMergeRequirements(client github.Client, ref model.PullRequestRef, base string) (github.MergeRequirements, bool) {
	reader, ok := client.(github.MergeRulesReader)
	if !ok {
		return github.MergeRequirements{}, false
	}
	req, err := reader.MergeRequirements(ref.Repo.Owner, ref.Repo.Name, ref.Number, base)
	if err != nil {
		// Half an answer is still worth having: the reads are
		// independent, and a credential that may not read rulesets can
		// still read reviews.
		empty := len(req.Approvers) == 0 && len(req.RequestedUsers) == 0 && len(req.RequestedTeams) == 0 &&
			req.ApprovalsRequired == 0 && len(req.RequiredChecks) == 0 && len(req.OtherRules) == 0
		return req, !empty
	}
	return req, true
}

// refineRefusal sharpens what GitHub's words said with what its rules say,
// for a refusal the words alone could not place -- classic branch
// protection answers "Pull Request is not mergeable" for a missing review
// as readily as for a race.
func refineRefusal(kind github.RefusalKind, cause error, req github.MergeRequirements, read bool,
	checks []github.CheckRun) github.RefusalKind {

	if kind != github.RefusalTransient || !read || !github.IsMergeRuleStatus(cause) {
		return kind
	}
	if len(missingRequiredChecks(req.RequiredChecks, checks)) > 0 {
		return github.RefusalRequiredChecks
	}
	if req.ApprovalsRequired > len(req.Approvers) || len(req.ChangesRequestedBy) > 0 ||
		(req.CodeOwnerReview && len(req.RequestedUsers)+len(req.RequestedTeams) > 0) {
		return github.RefusalApproval
	}
	return kind
}

// missingRequiredChecks is the required checks that have not passed on
// the commit checks were read for.
func missingRequiredChecks(required []string, checks []github.CheckRun) []string {
	passed := map[string]bool{}
	for _, c := range checks {
		if c.Status == "completed" && c.Conclusion != nil &&
			(*c.Conclusion == "success" || *c.Conclusion == "neutral" || *c.Conclusion == "skipped") {
			passed[c.Name] = true
		}
	}
	var missing []string
	for _, name := range required {
		if !passed[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// githubCheckRuns reads GitHub's check runs on sha, which a repository
// whose gate is grain CI never reads otherwise. Best effort: nil on a
// failed read, which reads as "nothing has passed".
func githubCheckRuns(client github.Client, ref model.PullRequestRef, sha string, have []github.CheckRun) []github.CheckRun {
	if len(have) > 0 || sha == "" {
		return have
	}
	runs, err := client.ListCheckRuns(ref.Repo.Owner, ref.Repo.Name, sha)
	if err != nil {
		return nil
	}
	return runs
}

// holdRefusedMerge decides whether the merge arm asks GitHub again for a
// merge it has refused this head before, or holds off. It is what makes
// every kind of refusal stop being retried once a cycle.
func holdRefusedMerge(client github.Client, e queueEntry, detail github.PullRequestDetail,
	blocked bool, now time.Time) bool {

	ref := e.ref
	r, ok := lastMergeRefusal(ref, detail.HeadSHA)
	if !ok {
		// A record about another commit is a push since: the wait it
		// described is over, and the new head starts its own story.
		if _, stale := forge.PeekMergeRefusal(forgeRef(ref)); stale {
			forgetMergeRefusals(ref)
		}
		return false
	}
	if r.Escalated && !blocked {
		// The queue handed this to a person and the person has taken it
		// back (Retry merge): a fresh start, with a fresh count.
		forgetMergeRefusals(ref)
		return false
	}
	poll := func() bool {
		if !r.Polled.IsZero() && now.Before(r.Polled.Add(mergeRefusalPollInterval)) {
			return false
		}
		forge.NoteMergeRefusalPolled(forgeRef(ref), now)
		return true
	}
	switch r.Kind {
	case github.RefusalApproval:
		if !poll() {
			return true
		}
		req, read := readMergeRequirements(client, ref, detail.BaseRef)
		if !read {
			return now.Before(r.Last.Add(approvalBlindRecheck))
		}
		// Unchanged reviews: the approval GitHub wants has not arrived.
		return req.ApprovalSignature() == r.Reviews
	case github.RefusalRequiredChecks:
		if !poll() {
			return true
		}
		required := r.Requirements.RequiredChecks
		if len(required) == 0 {
			return true
		}
		runs := githubCheckRuns(client, ref, detail.HeadSHA, nil)
		return len(missingRequiredChecks(required, runs)) > 0
	case github.RefusalRule:
		return true
	}
	if r.Exhausted(now) {
		return true
	}
	return now.Before(r.RetryAt())
}

// refusalStatus is the row for a refused merge, by what it was refused
// for.
func refusalStatus(r forge.MergeRefusal, ref model.PullRequestRef, prURL string) queueStatus {
	switch r.Kind {
	case github.RefusalApproval:
		return queueStatus{state: mergeStateWaitingGitHubApproval, note: approvalNote(r, ref, prURL)}
	case github.RefusalRequiredChecks:
		return queueStatus{state: mergeStateFailed, note: requiredChecksNote(r)}
	case github.RefusalRule:
		return queueStatus{state: mergeStateFailed, note: ruleNote(r)}
	}
	return queueStatus{state: mergeStateFailed, note: refusalNote(r)}
}

// reviewPage is where a person approves the pull request.
func reviewPage(ref model.PullRequestRef, prURL string) string {
	if prURL == "" {
		prURL = ref.WebURL()
	}
	return strings.TrimSuffix(prURL, "/") + "/files"
}

// approvalWho names who GitHub says must approve, as far as it says.
func approvalWho(req github.MergeRequirements) string {
	var parts []string
	who := append(append([]string{}, req.RequestedTeams...), req.RequestedUsers...)
	if len(who) > 0 {
		label := "requested reviewers"
		if req.CodeOwnerReview {
			label = "code owners"
		}
		parts = append(parts, label+" "+strings.Join(who, ", "))
	} else if req.CodeOwnerReview {
		parts = append(parts, "the code owners of the files it changes")
	}
	if req.ApprovalsRequired > 0 {
		have := len(req.Approvers)
		need := req.ApprovalsRequired - have
		if need < 1 {
			need = 1
		}
		parts = append(parts, fmt.Sprintf("%s still needed (%d of %d given)",
			plural(need, "approval"), have, req.ApprovalsRequired))
	}
	if len(req.ChangesRequestedBy) > 0 {
		parts = append(parts, "changes requested by @"+strings.Join(req.ChangesRequestedBy, ", @"))
	}
	if len(req.Rulesets) > 0 {
		parts = append(parts, "ruleset "+quotedNames(req.Rulesets))
	}
	return strings.Join(parts, "; ")
}

func quotedNames(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}

// approvalNote is the row for an approval wait: who must approve, where,
// and that grain's own approvals are a separate matter.
func approvalNote(r forge.MergeRefusal, ref model.PullRequestRef, prURL string) string {
	what := "GitHub requires an approving review before this merges"
	if who := approvalWho(r.Requirements); who != "" {
		what += " (" + who + ")"
	}
	return fmt.Sprintf("%s: %s. Approve it at %s and grain merges it on its own; it has left the queue "+
		"so nothing behind it waits. A Submit or approval in grain does not count toward GitHub's rule, "+
		"nor GitHub's toward grain's.", what, refusalLine(r.Reason, 120), reviewPage(ref, prURL))
}

// requiredChecksNote is the row for checks GitHub requires and grain does
// not run here.
func requiredChecksNote(r forge.MergeRefusal) string {
	checks := "checks"
	if len(r.Requirements.RequiredChecks) > 0 {
		checks = "checks " + strings.Join(r.Requirements.RequiredChecks, ", ")
	}
	return fmt.Sprintf("GitHub requires %s that grain does not run here: either make them pass, or remove "+
		"them from the branch's ruleset or protection. It merges on its own once GitHub reports them "+
		"passing. GitHub said: %s", checks, refusalLine(r.Reason, 160))
}

// ruleNote is the row for any other rule GitHub enforces.
func ruleNote(r forge.MergeRefusal) string {
	rules := ""
	if len(r.Requirements.OtherRules) > 0 {
		rules = " (rules on the branch: " + strings.Join(r.Requirements.OtherRules, ", ") + ")"
	}
	return fmt.Sprintf("A GitHub rule refuses this merge%s: %s. Change the rule (or the branch to meet it), "+
		"then press Retry merge; grain does not retry a rule on its own.", rules, refusalLine(r.Reason, 160))
}

// refusalComment is what the task's thread is told when the queue hands a
// refusal to a person.
func refusalComment(ref model.PullRequestRef, r forge.MergeRefusal, now time.Time) string {
	said := refusalLine(r.Reason, 400)
	switch r.Kind {
	case github.RefusalRequiredChecks:
		checks := "status checks"
		if len(r.Requirements.RequiredChecks) > 0 {
			checks = "the status checks " + strings.Join(r.Requirements.RequiredChecks, ", ")
		}
		return fmt.Sprintf("GitHub refused to merge %s because its rules require %s:\n\n> %s\n\n"+
			"grain's own CI is this repository's gate, so grain does not run or wait for those checks. "+
			"Either make them pass on GitHub, or remove them from the branch's ruleset (or branch "+
			"protection) for %s. The queue has moved on without %s; it merges on its own once GitHub "+
			"reports the checks passing, or press Retry merge after changing the rule.",
			ref, checks, said, ref.Repo, ref)
	case github.RefusalRule:
		rules := ""
		if len(r.Requirements.OtherRules) > 0 {
			rules = " The rules GitHub applies to this branch are: " + strings.Join(r.Requirements.OtherRules, ", ") + "."
		}
		return fmt.Sprintf("GitHub refused to merge %s because of a rule on %s:\n\n> %s\n\n%s "+
			"Nothing in the branch is wrong in a way a repair could fix, so none is filed and the merge "+
			"is not retried blind. Change the rule -- or, for signed commits or linear history, the way "+
			"the branch is made -- and press Retry merge. The queue has moved on without it.",
			ref, ref.Repo, said, strings.TrimSpace(rules))
	}
	return fmt.Sprintf(
		"%s reads clean, and GitHub has refused to merge it %d times over %s:\n\n> %s\n\n"+
			"grain could not place that refusal as a review, a required check or a rule, so it "+
			"retried it %d times with backoff and has stopped. Nothing is wrong with the branch, so "+
			"there is no repair to file. Sort out whatever GitHub is refusing (or merge by hand), then "+
			"press Retry merge. The queue has moved on without %s.",
		ref, r.Count, humanDuration(now.Sub(r.First)), said, forge.MaxBlindMergeRetries, ref)
}

// approvalComment is said once, when a pull request starts waiting for an
// approval GitHub requires.
func approvalComment(ref model.PullRequestRef, r forge.MergeRefusal, prURL string) string {
	who := approvalWho(r.Requirements)
	if who == "" {
		who = "whoever the branch's rules name"
	}
	return fmt.Sprintf("GitHub will not merge %s until it has an approving review its rules require "+
		"(%s):\n\n> %s\n\nReview it at %s. grain is not retrying the merge: it watches the pull "+
		"request's reviews and merges as soon as the approval is there. Meanwhile it has left the "+
		"queue, so the changes behind it go on merging.\n\nApprovals on the two sides are separate: a "+
		"Submit or an approver's sign-off in grain does not satisfy a review GitHub requires, and an "+
		"approval on GitHub does not stand in for one grain asks for.",
		ref, who, refusalLine(r.Reason, 400), reviewPage(ref, prURL))
}

// refusalLine is GitHub's refusal on one line. Unlike oneLine it keeps the
// lines after the first: a ruleset refusal's first line is only
// "Repository rule violations found", and which rule is on the next.
func refusalLine(reason string, n int) string {
	var lines []string
	for _, line := range strings.Split(reason, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > 1 {
		return oneLine(strings.TrimSuffix(lines[0], ".")+": "+strings.Join(lines[1:], " "), n)
	}
	return oneLine(reason, n)
}
