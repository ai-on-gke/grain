package orchestrator

import (
	"context"
	"fmt"
	"log"
	"path"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// The guard grain keeps over a repository's own rules (grain/task-1714).
//
// A repository's `.grain/triggers` decide what its pull requests are held
// for and who may release them, so the declarations have to guard
// themselves, and in a way no declaration can configure away. Three
// pieces, all built in:
//
//   - `.grain/triggers/**` is a trust-boundary category of its own
//     (trustboundary's path-triggers): every repository, held with
//     `human: required`, released only by the repository's owners.
//   - The templates the base's gating triggers file are guarded the same
//     way (guardTriggerTemplates). Which templates they are is the
//     repository's to say, so they cannot be on trustboundary's fixed
//     list; they are added to it here, per repository, from the base.
//   - A base whose trigger files do not parse holds every self-merging
//     change in the repository for its owners, saying why
//     (guardBrokenTriggers): the last good triggers still run, but a
//     repository that has broken its own rules has no rules grain can
//     vouch for.
//
// What this adds is matched alongside trustboundary.Classify's own list
// (holdForTrustBoundary), so the effective list is grain's categories
// plus the repository's: a repository can add a category or tighten a
// review, never remove one. The identity of the rules it was decided
// under (model.RepoRules) is recorded on the hold beside
// trustboundary.Version.

// Categories this file adds, by ID.
const (
	guardTriggerTemplatesID = "trigger-templates"
	guardBrokenTriggersID   = "unparseable-triggers"
)

// repoGuard is the repository's half of the effective list for files:
// the rules' identity, and the categories they add.
func repoGuard(ctx context.Context, store *model.Store, repo model.RepoRef, files []string,
	known bool) (model.RepoRules, []trustboundary.Match) {

	triggers, rules := repoRules(ctx, store, repo)
	// grain's own guard on its state repository comes first, and is not
	// the repository's to declare: see stateRepoGuard.
	out := stateRepoGuard(store, repo, files, known)
	if rules.Broken != "" {
		out = append(out, trustboundary.Match{
			Category: trustboundary.Category{
				ID: guardBrokenTriggersID,
				Description: fmt.Sprintf("this repository's base has trigger or merge-rule declarations grain cannot use, "+
					"so every change to it waits for its owners until they are fixed: %s", rules.Broken),
				RequiresHuman: true,
				OwnerApproval: true,
			},
			Paths: everyPath(files, known),
		})
	}
	if m, ok := guardTriggerTemplates(repo, triggers, files); ok {
		out = append(out, m)
	}
	if !known && !rules.Empty() {
		// The unread part of the list is exactly where a trigger or one
		// of its templates could be.
		out = append(out, trustboundary.Match{
			Category: trustboundary.Category{
				ID:            guardTriggerTemplatesID,
				Description:   "the repository's trigger declarations and the templates they file",
				RequiresHuman: true,
				OwnerApproval: true,
			},
			Paths: []string{"(the pull request's file list could not be read in full)"},
		})
	}
	return rules, out
}

// repoRulesFor is repoRules' identity alone, for a caller only comparing.
func repoRulesFor(ctx context.Context, store *model.Store, repo model.RepoRef) model.RepoRules {
	_, rules := repoRules(ctx, store, repo)
	return rules
}

// repoRules reads repo's triggers as the base declares them, and their
// identity. A store that cannot say is read as broken rules: the guard
// fails closed.
func repoRules(ctx context.Context, store *model.Store, repo model.RepoRef) ([]model.RepoPathTrigger, model.RepoRules) {
	triggers, err := store.PathTriggersOf(ctx, repo)
	if err != nil {
		log.Printf("orchestrator: reading %s's triggers to guard them: %v -- holding as if they were broken", repo, err)
		return nil, model.RepoRules{Broken: "grain could not read them: " + err.Error()}
	}
	rules := model.RepoRulesOf(triggers)
	sync, err := store.GetRepoDeclarationSync(ctx, repo)
	if err != nil {
		log.Printf("orchestrator: reading %s's declaration sync to guard its triggers: %v -- holding as if "+
			"they were broken", repo, err)
		rules.Broken = "grain could not read whether they parse: " + err.Error()
		return triggers, rules
	}
	if sync != nil && model.TriggerDeclarationsBroken(sync.LastError) {
		rules.Broken = sync.LastError
		if rules.Commit == "" {
			rules.Commit = sync.Commit
		}
	}
	return triggers, rules
}

// guardTriggerTemplates matches files that are a template one of the
// base's gating triggers files: `.grain/templates/<stem>.yml` (or .yaml)
// for a trigger whose template is that repository-declared stem.
func guardTriggerTemplates(repo model.RepoRef, triggers []model.RepoPathTrigger,
	files []string) (trustboundary.Match, bool) {

	prefix := model.RepoDeclaredID(model.DeclaredTemplate, repo, "")
	stems := map[string][]string{}
	for _, r := range triggers {
		if !r.Trigger.Gate {
			continue
		}
		if stem, ok := strings.CutPrefix(r.Trigger.Template, prefix); ok && stem != "" {
			stems[stem] = append(stems[stem], r.Trigger.Name)
		}
	}
	if len(stems) == 0 {
		return trustboundary.Match{}, false
	}
	var matched, by []string
	seen := map[string]bool{}
	for _, f := range files {
		f = strings.TrimPrefix(path.Clean("/"+f), "/")
		dir, base := path.Split(f)
		if strings.TrimSuffix(dir, "/") != model.TemplateDeclarationDir {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(base, ".yml"), ".yaml")
		names, ok := stems[stem]
		if !ok || stem == base {
			continue
		}
		matched = append(matched, f)
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				by = append(by, "`"+n+"`")
			}
		}
	}
	if len(matched) == 0 {
		return trustboundary.Match{}, false
	}
	return trustboundary.Match{
		Category: trustboundary.Category{
			ID: guardTriggerTemplatesID,
			Description: fmt.Sprintf("templates the repository's gating triggers file (%s), which decide "+
				"what reviews those triggers' holds wait on", strings.Join(by, ", ")),
			RequiresHuman: true,
			OwnerApproval: true,
		},
		Paths: matched,
	}, true
}

func everyPath(files []string, known bool) []string {
	if !known || len(files) == 0 {
		return []string{"(every path)"}
	}
	return files
}

// ownerApprovers is who may release a category only a repository's
// owners may: the deployment's owner, which is any person
// (model.ApproverOwner). The default team's token is kept beside it, as
// holds already recorded carry both.
func ownerApprovers(context.Context, *model.Store, model.RepoRef) []string {
	return []string{model.ApproverTeamAdmins(model.DefaultTeamID), model.ApproverOwner}
}

// categorySource says where a category on a hold came from.
func categorySource(c trustboundary.Category) string {
	switch c.ID {
	case guardTriggerTemplatesID:
		return "built in, from this repository's `" + model.TriggerDeclarationDir + "`"
	case guardBrokenTriggersID:
		// Its own description quotes the error naming the file; the
		// merge rules break it as surely as the triggers do.
		return "built in, from this repository's `" + model.TriggerDeclarationDir + "` and `" +
			model.MergeRulesPath + "`"
	default:
		return "built in"
	}
}

// releaseNote is the part of a security hold's note that says who may
// release it, and under which rules it was decided -- empty for a hold
// that says nothing new about either, which is every hold in a
// repository with no triggers and no owner-only category.
func releaseNote(matches []trustboundary.Match, approvers []string, rules model.RepoRules) string {
	var b strings.Builder
	if trustboundary.OwnerApproval(matches) {
		var ids []string
		for _, m := range matches {
			if m.Category.OwnerApproval {
				ids = append(ids, "**"+m.Category.ID+"**")
			}
		}
		fmt.Fprintf(&b, "\n\nWho can release it: changes to %s are released only by %s. An agent's "+
			"Submit is refused.", strings.Join(uniq(ids), " and "), model.DescribeApprovers(approvers))
	}
	if !rules.Empty() {
		fmt.Fprintf(&b, "\n\nDecided under %s and this repository's rules `%s`.", trustboundary.Version, rules)
	}
	return b.String()
}

func uniq(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// withdrawForPendingApprovals takes auto-merge back from a change that
// still owes an approver's Submit, and reports whether it did. Every
// release path already refuses such a change; this is the merge queue's
// own last look, so a way of turning auto-merge on that skipped
// ui.Client.Submit's check still cannot merge it.
func withdrawForPendingApprovals(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, now time.Time) (bool, error) {
	if !task.AutoMerge || len(model.PendingApprovals(task)) == 0 {
		return false, nil
	}
	var pending []model.ApprovalRequirement
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		pending = model.PendingApprovals(*t)
		if len(pending) > 0 {
			t.AutoMerge = false
		}
		return nil
	}); err != nil {
		return false, fmt.Errorf("orchestrator: withdrawing %s's auto-merge for its outstanding approvals: %w", task.ID, err)
	}
	if len(pending) == 0 {
		return false, nil
	}
	log.Printf("orchestrator: %s had auto-merge on with approvals outstanding; withdrawn", ref)
	return true, pathTriggerNote(ctx, store, client, task.ID, &ref,
		"This change was set to merge itself, but it is still owed a Submit from a named approver:\n\n"+
			DescribeApprovals(pending), now)
}

// DescribeApprovals lists requirements and who may satisfy each, for a
// note or a refusal.
func DescribeApprovals(reqs []model.ApprovalRequirement) string {
	var b strings.Builder
	for _, r := range reqs {
		fmt.Fprintf(&b, "- `%s`: ", r.Rule)
		if r.ApprovedBy != "" {
			fmt.Fprintf(&b, "approved by %s\n", r.ApprovedBy)
			continue
		}
		fmt.Fprintf(&b, "waiting for %s\n", model.DescribeApprovers(r.Approvers))
	}
	return b.String()
}

// heldOnlyForBrokenRules reports whether the base's broken rules are the
// only thing in matches an owner has to release, about a head and a file
// list grain read in full -- the hold a fix of the base may lift by
// itself (releaseFixedRulesHold). Anything else an owner must release
// leaves the whole decision theirs.
func heldOnlyForBrokenRules(matches []trustboundary.Match, known bool, headSHA string) bool {
	if !known || headSHA == "" {
		return false
	}
	broken := false
	for _, m := range matches {
		if m.Category.ID == guardBrokenTriggersID {
			broken = true
			continue
		}
		if m.Category.RequiresHuman || m.Category.OwnerApproval {
			return false
		}
	}
	return broken
}

// releaseFixedRulesHold judges again a change held only because its
// base's rules did not parse (model.LinkBrokenRulesHold), once they do.
//
// The hold says the change waits "until they are fixed", and that is
// what happens: the head it was held at is classified again under the
// fixed rules (judgePullRequestTriggers, holdForTrustBoundary), exactly as a change finishing
// under them would be. One that touches nothing they guard gets its
// auto-merge back; one that touches something is held for that, with the
// review and the approvers it asks for. The fix itself reached the base
// either past the owners' own hold on `.grain/triggers` and
// `.grain/merge-rules.yml` or by somebody who can push there, so the
// rules it restores are ones the repository's owners stand behind.
//
// Nothing is released on anyone else's behalf. The hold is left to an
// owner, and its marker dropped, when the requirement is no longer
// pending (an owner has already decided), when auto-merge is already
// back on, or when the head has moved since it was held: commits nobody
// classified under broken rules are not let through by fixing them. A
// review's findings drop the marker too (dropSecurityHoldOnFindings).
func releaseFixedRulesHold(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref *model.PullRequestRef, now time.Time) error {

	heldAt := linkTarget(task, model.LinkBrokenRulesHold)
	if task.AutoMerge || !brokenRulesPending(task) || task.Target == nil {
		return dropBrokenRulesHold(ctx, store, task.ID, heldAt)
	}
	rules := repoRulesFor(ctx, store, *task.Target)
	if rules.Broken != "" || ref == nil {
		return nil
	}
	detail, err := client.GetPullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		log.Printf("orchestrator: reading %s's head to judge it under its base's fixed rules: %v -- "+
			"trying again next cycle", ref, err)
		return nil
	}
	if detail.HeadSHA != heldAt {
		if err := dropBrokenRulesHold(ctx, store, task.ID, heldAt); err != nil {
			return err
		}
		body := fmt.Sprintf("This repository's rules parse again, but this change has moved from `%s` to `%s` "+
			"since it was held for them, and those commits were never judged by rules grain could use. "+
			"It still waits for an owner's Submit.", shortSHA(heldAt), shortSHA(detail.HeadSHA))
		return securityHoldNote(ctx, store, client, task.ID, ref, body, body, now)
	}
	log.Printf("orchestrator: %s's base rules parse again; classifying %s (held for them at %s) under them",
		task.Target, ref, shortSHA(heldAt))
	// The repository's triggers judge a head once (LinkPathTriggersRead),
	// and this head was judged by the last good ones: forget that, so the
	// fixed ones judge it as the self-merging change it was when it was
	// held. Before the trust-boundary classification, which drops the
	// marker -- a failure here is tried again next cycle.
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = withoutLinks(t.Links, model.LinkPathTriggersRead)
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: forgetting %s's trigger judgement under broken rules: %w", task.ID, err)
	}
	selfMerging := task
	selfMerging.AutoMerge = true
	if err := judgePullRequestTriggers(ctx, store, client, selfMerging, *ref, heldAt, now); err != nil {
		return err
	}
	if err := holdForTrustBoundary(ctx, store, client, task, *ref, heldAt, now); err != nil {
		return err
	}

	cleared := clearedTarget(heldAt, rules)
	released, waiting := false, false
	reviewID := ""
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		released, waiting, reviewID = false, false, ""
		if t.AutoMerge || linkTarget(*t, model.LinkTrustBoundaryCleared) != cleared || hasSecurityHold(*t) {
			// Held again under the fixed rules, which said so itself, or
			// already decided by somebody else.
			return nil
		}
		if hasPathTriggerHold(*t) || heldForHuman(*t) || len(model.PendingApprovals(*t)) > 0 {
			waiting = true
			return nil
		}
		t.AutoMerge, released = true, true
		// The review the broken-rules hold filed is about a hold that is
		// no longer there; closeObsoleteReview ends it.
		reviewID = linkTarget(*t, model.LinkSecurityReviewTask)
		t.Links = withoutLinks(t.Links, model.LinkSecurityReviewTask)
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: releasing %s from its base's fixed rules: %w", task.ID, err)
	}
	if released && reviewID != "" {
		if err := closeLiftedHoldReview(ctx, store, reviewID, task.ID, now); err != nil {
			// The change is released either way; at worst the review
			// runs for nothing, and its findings still reach the change.
			log.Printf("orchestrator: %v", err)
		}
	}
	var body string
	switch {
	case released:
		body = fmt.Sprintf("This repository's rules parse again, and under them this change (`%s`) touches "+
			"nothing they hold, so the hold for its broken rules is lifted: it merges automatically "+
			"again once it reads clean.", shortSHA(heldAt))
	case waiting:
		body = fmt.Sprintf("This repository's rules parse again, so the hold for its broken rules is lifted "+
			"from this change (`%s`). It still waits for the other approvals it needs.", shortSHA(heldAt))
	default:
		return nil
	}
	return securityHoldNote(ctx, store, client, task.ID, ref, body, body, now)
}

// brokenRulesPending reports whether task still waits on an owner's
// release of its base's broken rules.
func brokenRulesPending(task model.Task) bool {
	for _, r := range model.PendingApprovals(task) {
		if r.Rule == model.ApprovalRuleBuiltin+guardBrokenTriggersID {
			return true
		}
	}
	return false
}

func dropBrokenRulesHold(ctx context.Context, store *model.Store, taskID, heldAt string) error {
	if err := store.UpdateTask(ctx, taskID, func(t *model.Task) error {
		if linkTarget(*t, model.LinkBrokenRulesHold) == heldAt {
			t.Links = withoutLinks(t.Links, model.LinkBrokenRulesHold)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: dropping %s's broken-rules hold: %w", taskID, err)
	}
	return nil
}

// closeLiftedHoldReview closes reviewID, the security review a
// broken-rules hold on taskID filed, once a fix of the base has lifted
// that hold -- unless it is already running, or has a pull request of its
// own, in which case it is left to finish: findings it leaves still reach
// the change (noteFindingsOnReviewedTask).
func closeLiftedHoldReview(ctx context.Context, store *model.Store, reviewID, taskID string, now time.Time) error {
	review, err := store.GetTask(ctx, reviewID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading security review %s: %w", reviewID, err)
	}
	if review == nil || hasPullRequest(*review) {
		return nil
	}
	state, err := store.State(ctx, reviewID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading security review %s's state: %w", reviewID, err)
	}
	if state == "" || state.Settled() || state == model.StateRunning {
		return nil
	}
	if err := observeField(ctx, store, reviewID, now, func(o *model.Observation) {
		o.ClosedAt = &now
	}); err != nil {
		return fmt.Errorf("orchestrator: closing security review %s: %w", reviewID, err)
	}
	return reviewComment(ctx, store, reviewID, fmt.Sprintf(
		"Closing this review: the change it was filed for (task %s) was held only because its "+
			"repository's rules did not parse. They parse again, and under them the change touches "+
			"nothing that needs a security review.", taskID), now)
}
