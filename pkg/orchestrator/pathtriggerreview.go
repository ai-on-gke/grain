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
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The review a pull request's gating triggers file (grain/task-4093).
//
// # One review per (pull request, head, kind)
//
// Gating triggers that match the same head and name the same review
// template -- its kind -- are one review, not one each: release-machinery
// and release-promotion both matching a release workflow once filed two
// identical security reviews of it, which ran side by side and each
// opened a pull request. The one review gets the union of what they
// matched, listed by trigger, and the strictest of their settings
// (reviewSettings). Its idempotency tag is (pull request, kind), and the
// head it read is a tag of its own, so judging the same head again files
// nothing. Distinct templates are distinct reviews.
//
// # Reviews post findings
//
// A review comments: its findings go on the pull request
// (add_review_comment) and the reviewed task, and its run never opens a
// pull request (model.LinkReviewCommentsOnly). Two kinds may fix what they
// find. Grain's own security review asks for fixes and works on the
// change's own branch (putSecurityReview), so what it pushes is in the
// change's pull request and there is never a second one. A trigger may
// say `fixes: true` for any other template, and when every trigger of the
// kind says so the review is the stacked one fileReviewTask describes,
// whose own pull request merges back into the change.
//
// # Incremental, debounced re-review
//
// A new head whose last review of the kind finished -- clean, or with
// findings the push may have addressed -- is reviewed from the head that
// review read: the files the new commits touch, plus a re-check of what
// it found. It is reviewed in full instead when the new head is not a
// descendant of the old one (a rebase or force-push: the base moved
// under it), when commits from the base brought in a file the kind's
// triggers watch, or when the change's own files that moved number more
// than incrementalReviewMaxFiles. A re-review is filed deferred for
// reviewDebounce, so a burst of pushes supersedes it while it waits
// (fileCoalesced) and only the head that stayed put is read.

// incrementalReviewMaxFiles is the most files of the change a push may
// move and still be reviewed as a delta: past it, the delta is most of a
// review anyway and a full read costs little more.
const incrementalReviewMaxFiles = 20

// reviewDebounce is how long a re-review of a new head waits before it
// can be dispatched. The first review of a change is filed when its run
// finishes, which is as stable as a head gets, and is not deferred.
const reviewDebounce = 3 * time.Minute

// reviewMatch is one gating trigger that matched a head, and what of it.
type reviewMatch struct {
	rule      model.RepoPathTrigger
	paths     []string
	approvers []string
}

// reviewSettings is the strictest of what a kind's triggers say.
type reviewSettings struct {
	human       model.TriggerHuman
	sensitive   bool
	autoApprove bool
	fixes       bool
}

func strictestSettings(matches []reviewMatch) reviewSettings {
	s := reviewSettings{autoApprove: true, fixes: true}
	for _, m := range matches {
		s.human = model.StricterHuman(s.human, m.rule.Trigger.Human)
		s.sensitive = s.sensitive || m.rule.Trigger.Sensitive
		s.autoApprove = s.autoApprove && m.rule.Trigger.AutoApprove
		s.fixes = s.fixes && m.rule.Trigger.Fixes
	}
	return s
}

func unionPaths(matches []reviewMatch) []string {
	var out []string
	for _, m := range matches {
		for _, p := range m.paths {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	slices.Sort(out)
	return out
}

func triggerNames(matches []reviewMatch) []string {
	var out []string
	for _, m := range matches {
		out = append(out, m.rule.Trigger.Name)
	}
	return out
}

// reviewKindTag is the idempotency tag of a pull request's review of one
// kind: at most one is runnable at a time (fileCoalesced).
func reviewKindTag(ref model.PullRequestRef, kind string) string {
	return "path-trigger-review:" + strings.ToLower(ref.String()) + ":" + kind
}

// reviewHeadTag is the head a review was filed to read.
func reviewHeadTag(sha string) string { return "path-trigger-head:" + strings.ToLower(sha) }

// reviewSinceTag is the head an incremental review reads from.
func reviewSinceTag(sha string) string { return "path-trigger-since:" + strings.ToLower(sha) }

func reviewSince(tags []string) string {
	for _, tag := range tags {
		if sha, ok := strings.CutPrefix(tag, "path-trigger-since:"); ok {
			return sha
		}
	}
	return ""
}

// lastReviewed is the review of kind last filed for task's pull request,
// and the head it read (model.LinkPathTriggerReviewed).
func lastReviewed(task model.Task, kind string) (id, head string) {
	for _, l := range task.Links {
		if l.Kind != model.LinkPathTriggerReviewed {
			continue
		}
		k, rest, ok := strings.Cut(l.Target, "=")
		if !ok || k != kind {
			continue
		}
		id, head, _ = strings.Cut(rest, "@")
	}
	return id, head
}

// filePullRequestReview files the one review of kind that matches -- the
// gating triggers naming it that matched task's pull request at headSHA --
// demand, or returns the one already filed for that head.
func filePullRequestReview(ctx context.Context, store *model.Store, client github.Client, task model.Task,
	ref model.PullRequestRef, headSHA, kind string, matches []reviewMatch, files []string,
	now time.Time) (model.Task, error) {

	tag := reviewKindTag(ref, kind)
	prev, err := store.OpenTaskWithTag(ctx, tag)
	if err != nil {
		return model.Task{}, err
	}
	if prev != nil && headSHA != "" && hasTag(*prev, reviewHeadTag(headSHA)) {
		// This head is already being reviewed: judging it again files
		// nothing.
		return *prev, nil
	}
	settings := strictestSettings(matches)
	lastID, lastHead := lastReviewed(task, kind)
	rereview := lastHead != "" && headSHA != "" && !strings.EqualFold(lastHead, headSHA)

	filed, err := fileCoalesced(ctx, store, tag, now, func(prev *model.Task, superseding bool) (model.Task, error) {
		since := ""
		if rereview {
			since = incrementalBase(ctx, store, prev, superseding, lastID, lastHead)
		}
		var delta []string
		if since != "" {
			delta, since = reviewDelta(client, ref, since, headSHA, files, matches)
		}
		review, err := buildPullRequestReview(ctx, store, task, ref, headSHA, kind, matches, settings, since, delta, now)
		if err != nil {
			return model.Task{}, err
		}
		review.Tags = []string{model.PathTriggerMarkerTag, tag}
		if headSHA != "" {
			review.Tags = append(review.Tags, reviewHeadTag(headSHA))
		}
		if since != "" {
			review.Tags = append(review.Tags, reviewSinceTag(since))
		}
		for _, m := range matches {
			if rt := pathTriggerRuleTag(m.rule.Commit); !slices.Contains(review.Tags, rt) {
				review.Tags = append(review.Tags, rt)
			}
		}
		if rereview {
			// Debounced: a burst of pushes supersedes this while it waits.
			until := now.Add(reviewDebounce)
			review.DeferredAt, review.DeferredUntil = &now, &until
		}
		return review, nil
	})
	if err != nil {
		return model.Task{}, err
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		var kept []model.Link
		for _, l := range t.Links {
			if l.Kind == model.LinkPathTriggerReviewed && strings.HasPrefix(l.Target, kind+"=") {
				continue
			}
			kept = append(kept, l)
		}
		t.Links = append(kept, model.Link{Kind: model.LinkPathTriggerReviewed,
			Target: kind + "=" + filed.ID + "@" + headSHA})
		return nil
	}); err != nil {
		return filed, fmt.Errorf("orchestrator: recording %s's %s review: %w", task.ID, kind, err)
	}
	return filed, nil
}

// incrementalBase is the head a re-review reads from, or "" for a full
// review. An undispatched review being superseded hands on its own base
// (it never read anything); a review still running, or one that finished,
// read lastHead. One closed without finishing read nothing.
func incrementalBase(ctx context.Context, store *model.Store, prev *model.Task, superseding bool,
	lastID, lastHead string) string {

	if prev != nil && superseding {
		return reviewSince(prev.Tags)
	}
	if prev != nil {
		return lastHead
	}
	if lastID == "" {
		return ""
	}
	obs, err := store.GetObservation(ctx, lastID)
	if err != nil || obs == nil || obs.CompletedAt == nil {
		return ""
	}
	return lastHead
}

// reviewDelta is the change's files that moved between since and head,
// and since itself -- or "" with no delta when the review should be a
// full one after all (the package comment's rules).
func reviewDelta(client github.Client, ref model.PullRequestRef, since, head string,
	files []string, matches []reviewMatch) ([]string, string) {

	if client == nil {
		return nil, ""
	}
	cmp, err := client.CompareRefs(ref.Repo.Owner, ref.Repo.Name, since, head)
	if err != nil {
		log.Printf("orchestrator: comparing %s %s..%s for an incremental review: %v -- reviewing it in full",
			ref, shortCommit(since), shortCommit(head), err)
		return nil, ""
	}
	if cmp.Status != "ahead" || cmp.FilesTruncated {
		return nil, ""
	}
	ofChange := map[string]bool{}
	for _, f := range files {
		ofChange[f] = true
	}
	var delta []string
	for _, f := range cmp.Files {
		names := []string{f.Filename}
		if f.PreviousFilename != "" && f.PreviousFilename != f.Filename {
			names = append(names, f.PreviousFilename)
		}
		for _, name := range names {
			if ofChange[name] {
				if !slices.Contains(delta, name) {
					delta = append(delta, name)
				}
				continue
			}
			for _, m := range matches {
				if len(m.rule.Trigger.Match([]string{name})) > 0 {
					// The base brought in a watched file: what the change
					// does to it has to be read afresh.
					return nil, ""
				}
			}
		}
	}
	if len(delta) > incrementalReviewMaxFiles {
		return nil, ""
	}
	slices.Sort(delta)
	return delta, since
}

// buildPullRequestReview writes the review of kind, and returns it with
// the tags and links fileCoalesced adds still to come.
func buildPullRequestReview(ctx context.Context, store *model.Store, task model.Task, ref model.PullRequestRef,
	headSHA, kind string, matches []reviewMatch, settings reviewSettings, since string, delta []string,
	now time.Time) (model.Task, error) {

	paths := unionPaths(matches)
	vars := map[string]string{
		"trigger": strings.Join(triggerNames(matches), ", "),
		"paths":   strings.Join(paths, "\n"),
		"group":   "",
		"pr":      ref.String(), "head_sha": headSHA, "base_sha": matches[0].rule.Commit,
	}
	facts := reviewFacts(ref, headSHA, matches, settings) + incrementalNote(since, headSHA, delta)

	var tmpl *model.Template
	if kind == model.SecurityReviewTemplateID {
		tmpl = model.SystemTemplate(model.SecurityReviewTemplateID, model.RepoRef{})
	} else {
		var err error
		if tmpl, err = store.ResolveTemplate(ctx, kind, model.RepoRef{}); err != nil {
			return model.Task{}, err
		}
		if tmpl == nil {
			// The template went away after the sync checked it (a
			// deployment template deleted since). A gate is still a
			// gate: grain's own security review stands in, rather than
			// nothing being filed for the change to wait on.
			log.Printf("orchestrator: trigger template %s does not exist; filing grain's security review "+
				"for %s instead", kind, ref)
			tmpl = model.SystemTemplate(model.SecurityReviewTemplateID, model.RepoRef{})
		}
	}
	filled := *tmpl
	filled.Title = model.SubstituteFields(filled.Title, vars)
	filled.Body = model.SubstituteFields(filled.Body, vars)

	var review model.Task
	var err error
	switch {
	case filled.ID == model.SecurityReviewTemplateID:
		// The security review asks for fixes, on the change's own branch:
		// never a second pull request.
		body := strings.TrimSpace(filled.Body) + "\n\n---\n\n" + fmt.Sprintf(
			"You are reviewing task %s (%q), whose work is on `%s` and proposed in %s. "+
				"Your checkout is that branch itself: diff it against %s's base to see the change, and push "+
				"any fix to it.\n", task.ID, task.Title, task.BranchName(), ref, ref) + facts
		review, err = putSecurityReview(ctx, store, task, ref, filled, securityReviewTitle(task), body, now)
	case settings.fixes:
		review, err = putStackedReview(ctx, store, task, filled, reviewTaskTitle(filled, task),
			reviewTaskBody(filled, task, ref, task.BranchName())+facts, now)
	default:
		review, err = putStackedReview(ctx, store, task, filled, reviewTaskTitle(filled, task),
			reviewFindingsBody(filled, task, ref, task.BranchName())+facts, now)
		review.Links = append(review.Links, model.Link{Kind: model.LinkReviewCommentsOnly, Target: task.ID})
	}
	if err != nil {
		return model.Task{}, err
	}
	model.InheritRequester(&review, task)
	if !settings.autoApprove {
		review.Approval, review.ApprovedAt = nil, nil
	}
	return review, nil
}

// reviewFacts is what a review's body ends with: which triggers asked for
// it and what each matched, and the settings it is held under.
func reviewFacts(ref model.PullRequestRef, headSHA string, matches []reviewMatch, settings reviewSettings) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n---\n\ngrain: filed by the path trigger(s) %s for %s at %s -- one review for all of them.\n\n",
		strings.Join(quoted(triggerNames(matches)), ", "), ref, shortCommit(headSHA))
	fmt.Fprintf(&b, "- pr: `%s`\n- head_sha: `%s`\n", ref, headSHA)
	for _, m := range matches {
		fmt.Fprintf(&b, "- trigger `%s` (`%s` as of base commit %s):\n", m.rule.Trigger.Name, m.rule.Trigger.Path,
			shortCommit(m.rule.Commit))
		for _, p := range m.paths {
			fmt.Fprintf(&b, "  - `%s`\n", p)
		}
	}
	fmt.Fprintf(&b, "\nThese triggers gate %s: it does not merge until this review is done (human: `%s`", ref, settings.human)
	if settings.sensitive {
		b.WriteString(", sensitive")
	}
	b.WriteString(").\n")
	return b.String()
}

// incrementalNote tells an incremental review what to read, or nothing
// for a full one.
func incrementalNote(since, head string, delta []string) string {
	if since == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n**This is an incremental review.** An earlier review of this kind read the change at %s. "+
		"Review only what changed since: `git diff %s %s`", shortCommit(since), since, head)
	if len(delta) == 0 {
		b.WriteString(" -- none of the change's own files moved (the new commits only brought in its base). ")
	} else {
		b.WriteString(", which touches:\n\n")
		for _, f := range delta {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
		b.WriteString("\n")
	}
	b.WriteString("Also check that every finding the earlier review left on the pull request has been addressed, " +
		"and say which still stand. Do not re-review the rest of the change.\n")
	return b.String()
}

// reviewFindingsBody is reviewTaskBody for a review that fixes nothing:
// what it finds goes on the pull request, and its run opens none.
func reviewFindingsBody(tmpl model.Template, task model.Task, ref model.PullRequestRef, branch string) string {
	var b strings.Builder
	if body := strings.TrimSpace(model.SubstituteFields(tmpl.Body, tmpl.FieldValues(nil))); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n---\n\n")
	}
	fmt.Fprintf(&b,
		"You are reviewing task %s (%q), whose work is on `%s` and proposed in %s. Your checkout "+
			"starts from that branch.\n\n"+
			"**Post findings; do not fix them.** Record each finding with an add_review_comment call "+
			"(with `path` and `line` for one about a line of the diff): they are posted as a review on %s "+
			"and put to task %s, whose owner or a fix task deals with them, and any such call holds the "+
			"change. Do not commit, push or open a pull request -- a review's run never opens one, and "+
			"anything pushed is dropped. If the change reads clean, finish with comment_on_issue saying "+
			"what you checked.",
		task.ID, task.Title, branch, ref, ref, task.ID)
	b.WriteString("\n\n" + reviewHumanReviewNote)
	return b.String()
}

// reviewCommentsOnly reports whether task is a review that posts its
// findings and opens no pull request.
func reviewCommentsOnly(task model.Task) bool {
	return isReviewTask(task) && linkTarget(task, model.LinkReviewCommentsOnly) != ""
}
