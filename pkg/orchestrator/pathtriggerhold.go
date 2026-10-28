package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/trustboundary"
)

// `on: pull_request` path triggers, and the gate some of them hold
// (grain/task-1712). The overview of path triggers is pathtrigger.go's.
//
// A pull request is judged when its run finishes (finishWithPullRequest,
// beside holdForTrustBoundary) and again whenever the merge queue finds
// its head somewhere it was not judged at (syncEntry), against the
// triggers the store holds for its repository -- the base's, never the
// head's. Each trigger whose paths the change touches files its template:
//
//   - without `gate`, as an ordinary task in the repository, coalesced per
//     (pull request, trigger, group) so a head that moves supersedes an
//     undispatched one instead of piling up runs;
//   - with `gate: true`, as a review stacked on the change's branch, and
//     the change's auto-merge is withdrawn -- the security hold's
//     machinery (securityhold.go), on a hold link of its own
//     (model.LinkPathTriggerHold) naming the strictest `human` setting
//     among the gating triggers that matched.
//
// SyncPathTriggerHolds releases a hold once every gating task has
// finished, the way the security hold is released: `on-findings` when
// none left findings (findings drop the hold, dropPathTriggerHoldOnFindings)
// and any fixes they pushed have merged back into the change's branch,
// `no` whatever they said, and `required` never -- that one only ends at
// a human's Submit, however clean its reviews were.

func hasPathTriggerHold(task model.Task) bool {
	return linkTarget(task, model.LinkPathTriggerHold) != ""
}

func hasTag(task model.Task, tag string) bool { return slices.Contains(task.Tags, tag) }

// pullRequestTriggersApply reports whether task's pull request is one
// pull-request triggers judge at all: a change aimed at its repository's
// base, not a fix or review merging into another task's branch, and not a
// task a trigger filed itself (which would otherwise judge its own work).
func pullRequestTriggersApply(task model.Task) bool {
	return task.Target != nil && task.Origin.Reason != model.ReasonFix && !isReviewTask(task) &&
		!hasTag(task, model.PathTriggerMarkerTag)
}

// gateFiring is one gating trigger that matched, and the task it filed.
type gateFiring struct {
	trigger string
	human   model.TriggerHuman
	taskID  string
	paths   []string
	// sensitive and approvers are the trigger's own (model's
	// sensitive_path.go); approvers is already defaulted.
	sensitive bool
	approvers []string
	// template is what taskID was filed from, for the note.
	template string
}

// judgePullRequestTriggers files what task's pull request fires, and
// holds it if a gating trigger matched. headSHA is the head the caller
// has read, or "" for this to read it. A repository with no pull-request
// triggers costs nothing beyond one store read.
func judgePullRequestTriggers(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, headSHA string, now time.Time) error {

	if !pullRequestTriggersApply(task) || client == nil {
		return nil
	}
	all, err := store.PathTriggersOf(ctx, *task.Target)
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's path triggers: %w", *task.Target, err)
	}
	var rules []model.RepoPathTrigger
	for _, r := range all {
		if r.Trigger.On == model.TriggerOnPullRequest {
			rules = append(rules, r)
		}
	}
	if len(rules) == 0 {
		return nil
	}
	current, err := store.GetTask(ctx, task.ID)
	if err != nil || current == nil {
		return err
	}
	detail, err := client.GetPullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s to judge it against its triggers: %w", ref, err)
	}
	if headSHA == "" {
		headSHA = detail.HeadSHA
	}
	if headSHA != "" && linkTarget(*current, model.LinkPathTriggersRead) == headSHA {
		return nil
	}
	def, err := client.DefaultBranch(ref.Repo.Owner, ref.Repo.Name)
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's default branch to judge %s: %w", ref.Repo, ref, err)
	}
	files, err := client.ListPullRequestFiles(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	known := err == nil && len(files) < github.PullRequestFilesCap
	if err != nil {
		log.Printf("orchestrator: reading which files %s touches for its triggers: %v -- "+
			"every gating trigger holds it rather than guessing it touches nothing", ref, err)
	}
	securityReviewID, coveredBySecurityReview := reusableSecurityReviewPaths(ctx, store, *current, files, known, headSHA)

	var gates []gateFiring
	var errs []error
	for _, rule := range rules {
		if !rule.Trigger.WatchesBranch(detail.BaseRef, def) {
			continue
		}
		matched := rule.Trigger.Match(files)
		groups := rule.Trigger.Groups(matched)
		if len(matched) == 0 {
			if known || !rule.Trigger.Gate {
				continue
			}
			// Fail closed: the unread part of the list is exactly where
			// a gated path could be.
			groups = map[string][]string{"": {"(the pull request's file list could not be read in full)"}}
		}
		firing := func(taskID, template string, paths []string) gateFiring {
			return gateFiring{trigger: rule.Trigger.Name, human: rule.Trigger.Human, taskID: taskID,
				paths: paths, sensitive: rule.Trigger.Sensitive,
				approvers: triggerApprovers(rule.Trigger, *current), template: template}
		}
		if rule.Trigger.Gate && !current.AutoMerge && !hasPathTriggerHold(*current) && !hasSecurityHold(*current) &&
			linkTarget(*current, model.LinkPathTriggersRead) != "" {
			// Already waiting for a human, who has had this gate's say
			// on an earlier head: a fresh gating review of every push
			// they make would be a review loop, not a gate. A sensitive
			// path still says *which* human, though.
			if rule.Trigger.Sensitive {
				gates = append(gates, firing("", "", flatten(groups)))
			}
			continue
		}
		if rule.Trigger.Gate && rule.Trigger.Template == model.TemplateNone {
			// Nothing to file: a person is the whole review.
			gates = append(gates, firing("", model.TemplateNone, flatten(groups)))
			continue
		}
		for _, group := range sortedKeys(groups) {
			if rule.Trigger.Gate &&
				(rule.Trigger.Template == model.SecurityReviewTemplateID || (rule.Trigger.Sensitive && rule.Trigger.Template == "")) &&
				securityReviewID != "" && allPathsCovered(groups[group], coveredBySecurityReview) {
				gates = append(gates, firing(securityReviewID, model.SecurityReviewTemplateID, groups[group]))
				continue
			}
			filed, err := filePullRequestTrigger(ctx, store, *current, ref, headSHA, rule, group, groups[group], now)
			if err != nil {
				errs = append(errs, err)
				if rule.Trigger.Sensitive {
					// The review could not be filed; the change is
					// still held, for a person.
					gates = append(gates, firing("", "", groups[group]))
				}
				continue
			}
			if rule.Trigger.Gate {
				gates = append(gates, firing(filed.ID, rule.Trigger.Template, groups[group]))
			}
		}
	}
	if len(errs) > 0 && len(gates) == 0 {
		// Nothing was held, and something that should have been filed was
		// not: leave the head unjudged so the next look tries again.
		return errors.Join(errs...)
	}
	// task is the caller's copy, read before holdForTrustBoundary ran on
	// this head: a category only a human may release withdraws auto-merge
	// without a hold link of its own, and that change was still merging
	// itself as far as this gate is concerned.
	if err := applyPathTriggerGate(ctx, store, client, *current, task.AutoMerge, ref, headSHA, gates, now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// reusableSecurityReviewPaths returns the security review task ID already
// filed by holdForTrustBoundary for task at headSHA and the set of paths that
// review covers, or ("", nil) when no reusable security review exists for
// headSHA.
func reusableSecurityReviewPaths(ctx context.Context, store *model.Store, task model.Task,
	files []string, known bool, headSHA string) (string, map[string]bool) {

	if !known || headSHA == "" || task.Target == nil {
		return "", nil
	}
	reviewID := linkTarget(task, model.LinkSecurityReviewTask)
	if reviewID == "" {
		return "", nil
	}
	matches := trustboundary.Classify(files)
	rules, guarded := repoGuard(ctx, store, *task.Target, files, known)
	matches = append(matches, guarded...)
	if linkTarget(task, model.LinkTrustBoundaryCleared) != clearedTarget(headSHA, rules) {
		return "", nil
	}
	if !securityReviewReusable(ctx, store, reviewID) {
		return "", nil
	}
	covered := map[string]bool{}
	for _, m := range matches {
		for _, p := range m.Paths {
			covered[p] = true
		}
	}
	if len(covered) == 0 {
		return "", nil
	}
	return reviewID, covered
}

func allPathsCovered(paths []string, covered map[string]bool) bool {
	if len(paths) == 0 || len(covered) == 0 {
		return false
	}
	for _, p := range paths {
		if !covered[p] {
			return false
		}
	}
	return true
}

// filePullRequestTrigger files one group of one pull-request firing:
// a stacked review for a gating trigger, an ordinary task otherwise.
func filePullRequestTrigger(ctx context.Context, store *model.Store, task model.Task,
	ref model.PullRequestRef, headSHA string, rule model.RepoPathTrigger, group string, paths []string,
	now time.Time) (model.Task, error) {

	tag := model.PathTriggerTag(strings.ToLower(ref.String()), rule.Trigger.Name, group)
	vars := triggerVars(rule, group, paths)
	vars["pr"], vars["head_sha"], vars["base_sha"] = ref.String(), headSHA, rule.Commit
	facts := triggerFacts(rule, fmt.Sprintf("%s at %s", ref, shortCommit(headSHA)), group, paths,
		[][2]string{{"pr", ref.String()}, {"head_sha", headSHA}, {"base_sha", rule.Commit}})
	if rule.Trigger.Gate {
		facts += fmt.Sprintf("\nThis trigger gates %s: it does not merge until this task is done "+
			"(human: `%s`).\n", ref, rule.Trigger.Human)
	}

	return fileCoalesced(ctx, store, tag, now, func(prev *model.Task, superseding bool) (model.Task, error) {
		tags := []string{model.PathTriggerMarkerTag, tag, pathTriggerRuleTag(rule.Commit)}
		if !rule.Trigger.Gate {
			comp, _, err := store.ComposeTriggerTask(ctx, rule.Trigger.Template, *task.Target, "", rule.Trigger.AutoApprove)
			if err != nil {
				return model.Task{}, fmt.Errorf("orchestrator: composing trigger %s's task for %s: %w", rule.Trigger.Name, ref, err)
			}
			comp.Title = model.SubstituteFields(comp.Title, vars)
			if strings.TrimSpace(comp.Title) == "" {
				comp.Title = fmt.Sprintf("Trigger %s: %s", rule.Trigger.Name, ref)
			}
			comp.Body = model.SubstituteFields(comp.Body, vars) + facts
			id, err := store.NewTaskID(ctx)
			if err != nil {
				return model.Task{}, err
			}
			filed := comp.Task(id, model.TriggerPrincipal, model.ReasonSchedule, now)
			filed.OrderKey, err = store.OrderKeyForNewTask(ctx, false)
			if err != nil {
				return model.Task{}, err
			}
			filed.Tags = tags
			filed.Links = append(filed.Links, model.Link{Kind: model.LinkProposedBy, Target: task.ID})
			model.InheritRequester(&filed, task)
			return filed, nil
		}

		tmpl, err := store.ResolveTemplate(ctx, rule.Trigger.Template, model.RepoRef{})
		if err != nil {
			return model.Task{}, err
		}
		if tmpl == nil {
			// The template went away after the sync checked it (a
			// deployment template deleted since). A gate is still a
			// gate: grain's own security review stands in, rather than
			// nothing being filed for the change to wait on.
			log.Printf("orchestrator: trigger %s's template %s does not exist; filing grain's security review "+
				"for %s instead", rule.Trigger.Name, rule.Trigger.Template, ref)
			tmpl = model.SystemTemplate(model.SecurityReviewTemplateID, model.RepoRef{})
		}
		filled := *tmpl
		filled.Title = model.SubstituteFields(filled.Title, vars)
		filled.Body = model.SubstituteFields(filled.Body, vars)
		title := reviewTaskTitle(filled, task)
		body := reviewTaskBody(filled, task, ref, task.BranchName()) + facts
		review, err := putStackedReview(ctx, store, task, filled, title, body, now)
		if err != nil {
			return model.Task{}, err
		}
		review.Tags = tags
		model.InheritRequester(&review, task)
		if !rule.Trigger.AutoApprove {
			review.Approval, review.ApprovedAt = nil, nil
		}
		return review, nil
	})
}

// applyPathTriggerGate records that task's head was judged, and holds or
// unholds it according to gates -- the gating tasks this head filed.
//
// Like the security hold, only a change that would otherwise merge
// itself is held: one with auto-merge on, or one already held (by this
// gate or by the security hold, whose release must not be the only
// thing between the change and a merge). A change
// with auto-merge off is already waiting for a human, who still gets the
// tasks and the note. selfMerging is whether the change had auto-merge on
// before this head's trust-boundary classification, which may have just
// withdrawn it.
func applyPathTriggerGate(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, selfMerging bool, ref model.PullRequestRef, headSHA string, gates []gateFiring, now time.Time) error {

	var human model.TriggerHuman
	for _, g := range gates {
		human = model.StricterHuman(human, g.human)
	}
	bypassBranch, bypass := baseRepairBypassActive(ctx, store, task, "")
	held, released := false, false
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		held, released = false, false
		t.Links = append(withoutLinks(t.Links, model.LinkPathTriggersRead),
			model.Link{Kind: model.LinkPathTriggersRead, Target: headSHA})
		wasHeld := hasPathTriggerHold(*t)
		// Who may release this head is decided afresh with it: an
		// approval given on an earlier head is not one of this.
		t.Links = model.WithoutApprovalRules(t.Links, model.ApprovalRuleTrigger)
		if !bypass {
			t.Links = append(t.Links, approvalLinks(gates)...)
		}
		if len(gates) == 0 {
			if wasHeld {
				// The change no longer touches any gated path.
				t.Links = withoutLinks(t.Links, model.LinkPathTriggerHold, model.LinkPathTriggerTask)
				if !hasSecurityHold(*t) && !heldForHuman(*t) && len(model.PendingApprovals(*t)) == 0 {
					t.AutoMerge, released = true, true
				}
			}
			return nil
		}
		t.Links = withoutLinks(t.Links, model.LinkPathTriggerHold, model.LinkPathTriggerTask)
		for _, g := range gates {
			if g.taskID == "" {
				continue
			}
			t.Links = append(t.Links, model.Link{Kind: model.LinkPathTriggerTask, Target: g.trigger + "=" + g.taskID})
		}
		if bypass {
			if wasHeld && !hasSecurityHold(*t) {
				t.AutoMerge = true
			}
			t.Links = append(withoutLinks(t.Links, model.LinkBaseRepairBypass),
				model.Link{Kind: model.LinkBaseRepairBypass, Target: headSHA})
			return nil
		}
		// A security hold that has already withdrawn auto-merge is the
		// change still on its way to merging itself: this gate holds it
		// too, or that hold's release would be the only thing left.
		if t.AutoMerge || selfMerging || wasHeld || hasSecurityHold(*t) {
			t.AutoMerge, held = false, true
			t.Links = append(t.Links, model.Link{Kind: model.LinkPathTriggerHold, Target: string(human)})
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: recording %s's path-trigger gate: %w", task.ID, err)
	}
	switch {
	case released:
		body := fmt.Sprintf("At %s this change no longer touches any path a gating trigger watches, "+
			"so it merges automatically again once it reads clean.", shortCommit(headSHA))
		return pathTriggerNote(ctx, store, client, task.ID, &ref, body, now)
	case len(gates) > 0 && bypass:
		log.Printf("orchestrator: %s touches paths gating triggers watch (%s); human gate bypassed under base_repair_bypasses_human_gates while %s is red",
			ref, gateSummary(gates), bypassBranch)
		return pathTriggerNote(ctx, store, client, task.ID, &ref, pathTriggerBypassComment(gates, bypassBranch), now)
	case len(gates) > 0:
		log.Printf("orchestrator: %s touches paths gating triggers watch (%s); held=%v", ref, gateSummary(gates), held)
		return pathTriggerNote(ctx, store, client, task.ID, &ref, pathTriggerHoldComment(gates, human, held), now)
	}
	return nil
}

func gateSummary(gates []gateFiring) string {
	var names []string
	for _, g := range gates {
		names = append(names, g.trigger)
	}
	return strings.Join(names, ", ")
}

// pathTriggerBypassComment records which triggers and paths were bypassed under
// base_repair_bypasses_human_gates while the base branch is red.
func pathTriggerBypassComment(gates []gateFiring, branch string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This change touches paths the repository's gating triggers watch, "+
		"but its human gate was bypassed under `base_repair_bypasses_human_gates` "+
		"because this task is repairing broken base branch `%s` while `%s` is red:\n\n", branch, branch)
	for _, g := range gates {
		kind := ""
		if g.sensitive {
			kind = ", sensitive"
		}
		fmt.Fprintf(&b, "- `%s` (repository file `%s/%s.yml`%s, human: `%s`) ", g.trigger,
			model.TriggerDeclarationDir, g.trigger, kind, g.human)
		switch {
		case g.taskID != "":
			fmt.Fprintf(&b, "filed task %s (%s) for %s", g.taskID, templateLabel(g.template), strings.Join(quoted(g.paths), ", "))
		case g.template == model.TemplateNone:
			fmt.Fprintf(&b, "files no review (`template: none`) for %s", strings.Join(quoted(g.paths), ", "))
		default:
			fmt.Fprintf(&b, "filed nothing new for %s", strings.Join(quoted(g.paths), ", "))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nAuto-merge remains enabled under `base_repair_bypasses_human_gates` so this repair can merge on green CI.")
	return b.String()
}

// pathTriggerHoldComment says which trigger demanded what.
func pathTriggerHoldComment(gates []gateFiring, human model.TriggerHuman, held bool) string {
	var b strings.Builder
	b.WriteString("This change touches paths the repository's gating triggers watch:\n\n")
	for _, g := range gates {
		kind := ""
		if g.sensitive {
			kind = ", sensitive"
		}
		fmt.Fprintf(&b, "- `%s` (repository file `%s/%s.yml`%s, human: `%s`) ", g.trigger,
			model.TriggerDeclarationDir, g.trigger, kind, g.human)
		switch {
		case g.taskID != "":
			fmt.Fprintf(&b, "filed task %s (%s) for %s", g.taskID, templateLabel(g.template), strings.Join(quoted(g.paths), ", "))
		case g.template == model.TemplateNone:
			fmt.Fprintf(&b, "files no review (`template: none`) for %s", strings.Join(quoted(g.paths), ", "))
		default:
			fmt.Fprintf(&b, "filed nothing new for %s", strings.Join(quoted(g.paths), ", "))
		}
		if g.sensitive && g.human == model.TriggerHumanRequired {
			fmt.Fprintf(&b, "; released only by %s", model.DescribeApprovers(g.approvers))
		}
		b.WriteString("\n")
	}
	if !held {
		b.WriteString("\nIt was not merging automatically anyway, so nothing was withdrawn: it waits for you as before.")
		return b.String()
	}
	b.WriteString("\nAuto-merge is withdrawn until ")
	switch human {
	case model.TriggerHumanRequired:
		b.WriteString("a human presses Submit: at least one of these triggers requires one, whatever its task finds.")
		if approvals := approvalLinks(gates); len(approvals) > 0 {
			b.WriteString(" Each sensitive trigger above needs a person's Submit; " +
				"an agent's is refused.")
		}
	case model.TriggerHumanNo:
		b.WriteString("every one of those tasks has finished.")
	default:
		b.WriteString("every one of those tasks has finished without findings and without commits of its own; " +
			"otherwise it waits for a human's Submit.")
	}
	return b.String()
}

func quoted(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = "`" + p + "`"
	}
	return out
}

// pathTriggerNote is securityHoldNote for this hold: on the task, and on
// the pull request where a reviewer who never opens grain is looking.
func pathTriggerNote(ctx context.Context, store *model.Store, client github.Client,
	taskID string, ref *model.PullRequestRef, body string, now time.Time) error {
	if err := reviewComment(ctx, store, taskID, body, now); err != nil {
		return err
	}
	if ref == nil || client == nil {
		return nil
	}
	prBody := fmt.Sprintf("**grain path-trigger hold** (task %s)\n\n%s", taskID, body)
	if _, err := client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, prBody); err != nil {
		log.Printf("orchestrator: noting %s's path-trigger hold on %s: %v -- it is on the task only", taskID, ref, err)
	}
	return nil
}

// pathTriggersCurrentFor is the merge queue's look before it merges
// headSHA: a head the triggers have not judged yet is judged now, and a
// change that loses its auto-merge to it is not merged this cycle.
func pathTriggersCurrentFor(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, headSHA string, now time.Time) (bool, error) {
	if hasBaseRepairBypass(task) {
		if revoked, err := revokeExpiredBaseRepairBypass(ctx, store, client, task, ref, headSHA, now); err != nil || revoked {
			return !revoked, err
		}
	}
	if headSHA == "" || !pullRequestTriggersApply(task) || linkTarget(task, model.LinkPathTriggersRead) == headSHA {
		return true, nil
	}
	if err := judgePullRequestTriggers(ctx, store, client, task, ref, headSHA, now); err != nil {
		return false, err
	}
	current, err := store.GetTask(ctx, task.ID)
	if err != nil {
		return false, err
	}
	return current != nil && current.AutoMerge == task.AutoMerge, nil
}

// gatingTaskIDs is the tasks a held change waits on.
func gatingTaskIDs(task model.Task) map[string]string {
	out := map[string]string{}
	for _, l := range task.Links {
		if l.Kind != model.LinkPathTriggerTask {
			continue
		}
		if trigger, id, ok := strings.Cut(l.Target, "="); ok {
			out[id] = trigger
		}
	}
	return out
}

// dropPathTriggerHoldOnFindings ends task's path-trigger hold when a
// review of it left findings, so the change waits for a human -- unless
// the hold is `human: no` and the findings came from one of its own
// gating tasks, which is exactly what `no` says not to wait for.
func dropPathTriggerHoldOnFindings(ctx context.Context, store *model.Store, client github.Client,
	review model.Task, now time.Time) error {
	if !isReviewTask(review) {
		return nil
	}
	reviewedID := linkTarget(review, model.LinkProposedBy)
	if reviewedID == "" {
		return nil
	}
	if reviewed, err := store.GetTask(ctx, reviewedID); err == nil && reviewed != nil && hasBaseRepairBypass(*reviewed) {
		return nil
	}
	dropped := false
	if err := store.UpdateTask(ctx, reviewedID, func(t *model.Task) error {
		dropped = false
		if !hasPathTriggerHold(*t) {
			return nil
		}
		if model.TriggerHuman(linkTarget(*t, model.LinkPathTriggerHold)) == model.TriggerHumanNo {
			if _, own := gatingTaskIDs(*t)[review.ID]; own {
				return nil
			}
		}
		t.AutoMerge, dropped = false, true
		t.Links = withoutLinks(t.Links, model.LinkPathTriggerHold)
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: ending %s's path-trigger hold on its review's findings: %w", reviewedID, err)
	}
	if !dropped {
		return nil
	}
	body := fmt.Sprintf("Review task %s left findings on this change, so its path-trigger hold will not be "+
		"released automatically: it waits for you. Press Submit once you have decided what the findings "+
		"are worth.", review.ID)
	_, ref, ok, err := reviewedPullRequest(ctx, store, review)
	if err != nil || !ok {
		return pathTriggerNote(ctx, store, client, reviewedID, nil, body, now)
	}
	return pathTriggerNote(ctx, store, client, reviewedID, &ref, body, now)
}

// SyncPathTriggerHolds settles every path-trigger hold whose gating tasks
// have all finished. Level-triggered, like SyncSecurityHolds.
func SyncPathTriggerHolds(ctx context.Context, store *model.Store, client github.Client, now time.Time) error {
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading open pull request links: %w", err)
	}
	var errs []error
	for _, link := range links {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		task, err := store.GetTask(ctx, link.TaskID)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading task %s: %w", link.TaskID, err))
			continue
		}
		if task == nil {
			continue
		}
		var ref *model.PullRequestRef
		if r, err := model.ParsePullRequestRef(link.PullRequest); err == nil {
			ref = &r
		}
		if hasBaseRepairBypass(*task) && ref != nil {
			if _, err := revokeExpiredBaseRepairBypass(ctx, store, client, *task, *ref, "", now); err != nil {
				errs = append(errs, err)
				continue
			}
			task, err = store.GetTask(ctx, link.TaskID)
			if err != nil || task == nil {
				if err != nil {
					errs = append(errs, fmt.Errorf("orchestrator: reading task %s: %w", link.TaskID, err))
				}
				continue
			}
		}
		if !hasPathTriggerHold(*task) {
			continue
		}
		if err := settlePathTriggerHold(ctx, store, client, *task, ref, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func settlePathTriggerHold(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref *model.PullRequestRef, now time.Time) error {

	gating := gatingTaskIDs(task)
	if len(gating) == 0 {
		return nil
	}
	var pushed, abandoned []string
	for id := range gating {
		gate, err := store.GetTask(ctx, id)
		if err != nil {
			return fmt.Errorf("orchestrator: reading gating task %s: %w", id, err)
		}
		if gate == nil {
			abandoned = append(abandoned, id)
			continue
		}
		obs, err := store.GetObservation(ctx, id)
		if err != nil {
			return fmt.Errorf("orchestrator: reading gating task %s's observation: %w", id, err)
		}
		gatePushed, landed, finished := reviewOutcome(*gate, obs)
		switch {
		case gatePushed && finished && !landed:
			// Fixes closed without merging back: not a clean review.
			pushed = append(pushed, id)
		case finished:
			// Read clean, or fixed what it found and its fixes merged
			// back -- either way it left no findings, which would have
			// dropped the hold already.
		case !gatePushed && obs != nil && obs.ClosedAt != nil:
			abandoned = append(abandoned, id)
		default:
			return nil // still to finish
		}
	}
	slices.Sort(pushed)
	slices.Sort(abandoned)
	human := model.TriggerHuman(linkTarget(task, model.LinkPathTriggerHold))

	settled, released := false, false
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		settled, released = false, false
		if !hasPathTriggerHold(*t) || !maps2Equal(gatingTaskIDs(*t), gating) {
			return nil
		}
		settled = true
		t.Links = withoutLinks(t.Links, model.LinkPathTriggerHold)
		releasable := human == model.TriggerHumanNo ||
			(human == model.TriggerHumanOnFindings && len(pushed) == 0 && len(abandoned) == 0)
		if releasable && !hasSecurityHold(*t) && !heldForHuman(*t) && len(model.PendingApprovals(*t)) == 0 {
			t.AutoMerge, released = true, true
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: settling %s's path-trigger hold: %w", task.ID, err)
	}
	if !settled {
		return nil
	}
	var body string
	switch {
	case released:
		body = "Every task this change's gating triggers filed has finished, so it merges automatically " +
			"again once it reads clean."
	case human == model.TriggerHumanRequired:
		body = "Every task this change's gating triggers filed has finished. At least one of those " +
			"triggers requires a human, so this change waits for you: press Submit once you have read them."
	case len(pushed) > 0:
		body = fmt.Sprintf("Gating task(s) %s pushed fixes that were closed without merging into this "+
			"branch, so this change is not merging automatically: press Submit once you have read them.",
			strings.Join(pushed, ", "))
	case len(abandoned) > 0:
		body = fmt.Sprintf("Gating task(s) %s closed without finishing, so this change waits for you: "+
			"press Submit when you are satisfied.", strings.Join(abandoned, ", "))
	default:
		// Released over by the security hold, which says so itself.
		return nil
	}
	return pathTriggerNote(ctx, store, client, task.ID, ref, body, now)
}

func maps2Equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// triggerApprovers is who may release trigger's hold on task: its own
// approvers, or the admins of task's team when it names none.
func triggerApprovers(trigger model.PathTrigger, task model.Task) []string {
	if !trigger.Sensitive {
		return nil
	}
	if len(trigger.Approvers) > 0 {
		return trigger.Approvers
	}
	return []string{model.ApproverTeamAdmins(model.DefaultTeamID)}
}

// approvalLinks is the LinkApprovalRequired each sensitive `human:
// required` trigger among gates demands, one per trigger however many
// groups it fired for.
func approvalLinks(gates []gateFiring) []model.Link {
	var out []model.Link
	seen := map[string]bool{}
	for _, g := range gates {
		if !g.sensitive || g.human != model.TriggerHumanRequired || seen[g.trigger] {
			continue
		}
		seen[g.trigger] = true
		out = append(out, model.ApprovalRequiredLink(model.ApprovalRuleTrigger+g.trigger, g.approvers))
	}
	return out
}

func templateLabel(id string) string {
	if id == model.SecurityReviewTemplateID {
		return "grain's security review"
	}
	if id == "" {
		return "template"
	}
	return "template `" + id + "`"
}

func flatten(groups map[string][]string) []string {
	var out []string
	for _, key := range sortedKeys(groups) {
		out = append(out, groups[key]...)
	}
	return out
}
