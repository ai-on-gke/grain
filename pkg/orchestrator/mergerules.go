package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A repository's path-scoped merge rules at the merge gate
// (grain/task-1659; model's merge_rules.go has what they say and how they
// combine).
//
// The rules are the base's, as the declaration sync last stored them:
// a pull request editing `.grain/merge-rules.yml` is judged by the copy
// it is trying to change, and the file's own trust-boundary category
// (merge-rules) sends that edit to the repository's owners.
//
// A pull request is classified against them once per head commit, and
// the effective policy is recorded on its task (model.LinkMergePolicy)
// with a note saying which rule caused each requirement. A file list
// that cannot be read in full is classified as touching every rule: the
// gate fails closed.
//
// A repository with no rules is not classified at all and is gated
// exactly as before.

// mergePolicyFor is e's effective policy for headSHA, or nil where its
// repository declares no rules.
func mergePolicyFor(ctx context.Context, store *model.Store, client github.Client, e *queueEntry,
	headSHA string, now time.Time) *model.EffectiveMergePolicy {

	if e.rules.Empty() {
		return nil
	}
	if p, ok := model.MergePolicyOf(e.task); ok && p.Head == headSHA && headSHA != "" && p.Rules == e.rules.Commit {
		return &p
	}
	files, err := client.ListPullRequestFiles(e.ref.Repo.Owner, e.ref.Repo.Name, e.ref.Number)
	known := err == nil && len(files) < github.PullRequestFilesCap
	if err != nil {
		log.Printf("orchestrator: reading which files %s touches to apply its merge rules: %v -- "+
			"applying every rule", e.ref, err)
	}
	p := e.rules.Effective(files, known, e.ciMode)
	p.Head = headSHA
	if store == nil {
		return &p
	}
	if err := store.UpdateTask(ctx, e.task.ID, func(t *model.Task) error {
		t.Links = append(withoutLinks(t.Links, model.LinkMergePolicy), model.MergePolicyLink(p))
		return nil
	}); err != nil {
		// The policy still gates this cycle; the next one classifies again.
		log.Printf("orchestrator: recording %s's merge policy: %v", e.task.ID, err)
		return &p
	}
	e.task.Links = append(withoutLinks(e.task.Links, model.LinkMergePolicy), model.MergePolicyLink(p))
	if err := queueComment(ctx, store, e.task.ID, mergePolicyNote(e.rules, p, known), now); err != nil {
		log.Printf("orchestrator: noting %s's merge policy: %v", e.task.ID, err)
	}
	return &p
}

// MergePolicyNote is the comment the queue leaves on a change it has
// classified, from a file list it read in full -- exported for grain
// demo's -merge-rules seed, so the demo shows the queue's own words.
func MergePolicyNote(rules *model.MergeRules, p model.EffectiveMergePolicy) string {
	return mergePolicyNote(rules, p, true)
}

// mergePolicyNote is what the task is told when it is classified.
func mergePolicyNote(rules *model.MergeRules, p model.EffectiveMergePolicy, known bool) string {
	head := shortSHA(p.Head)
	matched := "no rule (the defaults apply)"
	if len(p.Matched) > 0 {
		matched = "rule(s) " + joinQuoted(p.Matched)
	}
	unread := ""
	if !known {
		unread = " The pull request's file list could not be read in full, so every rule applies."
	}
	return fmt.Sprintf("Merge rules: %s at %s matched %s for this change's head %s.%s It needs:\n\n%s",
		rules.Path, shortSHA(rules.Commit), matched, head, unread, p.Describe())
}

func joinQuoted(list []string) string {
	out := ""
	for i, s := range list {
		if i > 0 {
			out += ", "
		}
		out += "`" + s + "`"
	}
	return out
}

// missingChecksNote names the GitHub checks e's merge rules require that
// are not on its head at all -- the wait a person can act on (a workflow
// that does not run on this branch, a misspelt name) -- with the rules
// that asked for them.
func missingChecksNote(e queueEntry) string {
	if len(e.missingChecks) == 0 || e.policy == nil {
		return ""
	}
	var parts []string
	for _, n := range e.missingChecks {
		from := ""
		for _, r := range e.policy.Requirements {
			if r.Field == "ci.github" && r.Value == n {
				from = " (required by " + joinQuoted(r.From) + ")"
			}
		}
		parts = append(parts, "`"+n+"`"+from)
	}
	return "; required GitHub check(s) not reported on this commit at all: " + strings.Join(parts, ", ")
}

// forgePolicy is p as far as the forge needs it.
func forgePolicy(p *model.EffectiveMergePolicy) *forge.Policy {
	if p == nil {
		return nil
	}
	return &forge.Policy{
		Mode:           string(p.Mode),
		RequiredChecks: p.GitHubChecks,
		AllChecks:      p.GitHubAllChecks,
		StallTimeout:   p.StallTimeout,
	}
}

// narrowedGrainCI is v under a policy that narrows grain CI to named jobs.
//
// The full suite still has to have run for this exact commit -- that is
// the run v is about -- and a run that failed only outside the named jobs
// reads as passed. Every named job has to be in the run and have passed:
// a name the run does not have is not a pass. A run the runner no longer
// remembers cannot be read job by job, so it stays failed.
func narrowedGrainCI(v grainCIVerdict, p *model.EffectiveMergePolicy, obs *model.Observation,
	runner ci.Runner) grainCIVerdict {

	if p == nil || !p.Mode.Grain() || p.GrainFullSuite || len(p.GrainJobs) == 0 || !v.Failed ||
		runner == nil || obs == nil || obs.CI == nil {
		return v
	}
	run, ok := verdictRunOf(runner, obs.CI)
	if !ok {
		return v
	}
	if narrowedJobsPassed(run, p.GrainJobs) {
		return grainCIVerdict{Required: true, Passed: true}
	}
	return v
}

// verdictRunOf is the run status's verdict is about, job by job. For a
// verdict a flake recheck reached, that is the run it rechecked with the
// recheck's jobs laid over it: a recheck re-runs only the jobs that
// failed, so on its own it is missing every job that passed the first
// time -- which, under a narrowed suite, is exactly the jobs that count.
func verdictRunOf(runner ci.Runner, status *model.CIStatus) (ci.Run, bool) {
	run, ok := runner.Get(status.RunID)
	if !ok || status.RecheckOf == "" {
		return run, ok
	}
	first, ok := runner.Get(status.RecheckOf)
	if !ok {
		return ci.Run{}, false
	}
	return overlayRecheck(first, run), true
}

// overlayRecheck is first with each job recheck also ran replaced by the
// recheck's answer for it.
func overlayRecheck(first, recheck ci.Run) ci.Run {
	out := first
	out.Jobs = append([]ci.JobRun(nil), first.Jobs...)
	for _, j := range recheck.Jobs {
		replaced := false
		for i, f := range out.Jobs {
			if f.WorkflowPath == j.WorkflowPath && f.JobID == j.JobID {
				out.Jobs[i], replaced = j, true
			}
		}
		if !replaced {
			out.Jobs = append(out.Jobs, j)
		}
	}
	return out
}

// narrowedJobsPassed reports whether every name -- a job id, a job's
// name, or a workflow -- matched at least one job in run and every job it
// matched succeeded.
func narrowedJobsPassed(run ci.Run, names []string) bool {
	for _, n := range names {
		hit := false
		for _, j := range run.Jobs {
			if j.JobID != n && j.Name != n && j.Workflow != n && model.DeclarationStem(j.WorkflowPath) != n {
				continue
			}
			hit = true
			if j.Conclusion != ci.ConclusionSuccess {
				return false
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// withdrawForMergePolicy takes auto-merge back from a change its merge
// rules say may never merge itself (`auto_merge: never`), once per head:
// a person's Submit after that turns it on again, and is what merges it.
// A new head is withdrawn again, since nobody has submitted that one.
func withdrawForMergePolicy(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, p *model.EffectiveMergePolicy, now time.Time) (bool, error) {

	if p == nil || !p.AutoMergeNever || !task.AutoMerge || p.Head == "" ||
		linkTarget(task, model.LinkMergePolicyWithdrawn) == p.Head {
		return false, nil
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.AutoMerge = false
		t.Links = append(withoutLinks(t.Links, model.LinkMergePolicyWithdrawn),
			model.Link{Kind: model.LinkMergePolicyWithdrawn, Target: p.Head})
		return nil
	}); err != nil {
		return false, fmt.Errorf("orchestrator: withdrawing %s's auto-merge for its merge rules: %w", task.ID, err)
	}
	var from []string
	for _, r := range p.Requirements {
		if r.Field == "auto_merge" {
			from = r.From
		}
	}
	log.Printf("orchestrator: %s may not merge itself under its merge rules; auto-merge withdrawn", ref)
	return true, securityHoldNote(ctx, store, client, task.ID, &ref, fmt.Sprintf(
		"This change will not merge itself: its merge rules say `auto_merge: never` (from %s) for "+
			"commit %s. A person has to press Submit for it to merge.", joinQuoted(from), shortSHA(p.Head)),
		fmt.Sprintf("grain will not merge this pull request by itself: the repository's merge rules say "+
			"`auto_merge: never` for the paths it touches (%s). A person has to approve it in grain.",
			joinQuoted(from)), now)
}

// --- grain/task-1660: repairs, flake rechecks, porting -------

// recordedPolicy is the policy task was last classified under, nil where
// it has none. Wherever the queue acts on a task without a queue entry in
// hand -- a CI verdict, a merge that has landed -- this is what
// it goes by: the classification syncEntry made for the head it last saw.
func recordedPolicy(task model.Task) *model.EffectiveMergePolicy {
	p, ok := model.MergePolicyOf(task)
	if !ok {
		return nil
	}
	return &p
}

// entryPolicy is e's policy for this cycle: the one forge.Observe had it
// classified under, or the one recorded on its task.
func entryPolicy(e queueEntry) *model.EffectiveMergePolicy {
	if e.policy != nil {
		return e.policy
	}
	if e.rules.Empty() {
		return nil
	}
	return recordedPolicy(e.task)
}

// repairCap is how many automatic repairs a pull request may have: the
// queue's own most (maxAutomaticRepairs), or fewer where its merge rules
// say `repairs:`, with the rules that said it.
type repairCap struct {
	max  int
	from []string
}

func repairCapOf(p *model.EffectiveMergePolicy) repairCap {
	n, from := p.RepairLimit(maxAutomaticRepairs)
	return repairCap{max: n, from: from}
}

// describeRuleSources is a policy's sources in a sentence.
func describeRuleSources(from []string) string {
	if len(from) == 0 {
		return "the merge rules"
	}
	out := make([]string, len(from))
	for i, f := range from {
		switch f {
		case model.PolicySourceDefaults, model.PolicySourceRepo:
			out[i] = f
		default:
			out[i] = "rule `" + f + "`"
		}
	}
	return strings.Join(out, ", ")
}

// escalateNoRepair hands a broken pull request straight to a person
// because its merge rules say `repairs: 0`: no agent is sent to it.
func escalateNoRepair(ctx context.Context, store *model.Store, task model.Task, ref model.PullRequestRef,
	health model.PrHealth, rc repairCap, now time.Time) error {

	comment := fmt.Sprintf("%s %s, and the merge queue has not sent it to an agent. %s Push a fix by "+
		"hand (or resolve it on GitHub) and it merges as soon as it reads clean. The queue has moved "+
		"on to the next task in %s.", ref, healthReasonSuffix(health), repairsSpentSentence(0, "", rc), ref.Repo)
	log.Printf("orchestrator: %s is %s and its merge rules allow no automatic repair; blocked for a person",
		ref, healthReasonSuffix(health))
	return blockMergeQueue(ctx, store, task.ID, comment,
		healthReasonSuffix(health)+", and "+repairsSpentReason(0, rc), now)
}

// flakeRecheckRefusal is why task's failing jobs are not rechecked before
// it is blamed -- its merge rules say `flake_recheck: false` -- or "".
func flakeRecheckRefusal(task model.Task) string {
	p := recordedPolicy(task)
	if p == nil || !p.NoFlakeRecheck {
		return ""
	}
	return "its merge rules say `flake_recheck: false` (from " + describeRuleSources(p.SourcesOf("flake_recheck")) + ")"
}

// portRefusal is why a merge of task is not ported -- its merge rules say
// `port: false` -- or "".
func portRefusal(task model.Task) string {
	p := recordedPolicy(task)
	if p == nil || !p.NoPort {
		return ""
	}
	return "its merge rules say `port: false` (from " + describeRuleSources(p.SourcesOf("port")) + ")"
}
