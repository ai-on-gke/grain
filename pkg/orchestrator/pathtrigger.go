package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// Path triggers (grain/task-1712): a repository's `.grain/triggers/<name>.yml`
// says "file this template whenever a change to these paths merges, or is
// proposed". model's path_trigger.go has what one is; this file fires the
// `on: merge` ones and holds what every firing shares, and
// pathtriggerhold.go fires the `on: pull_request` ones and gates on them.
//
// # Rules come from the base
//
// Every trigger is read from the store, which holds what the last good
// declaration sync read off the repository's default branch
// (reconcileDeclarations) -- never from a pull request's head. A change
// cannot add, edit or delete the trigger that judges it, and the template
// a trigger names is the base's copy for the same reason. The base commit
// a rule came from is recorded on every task it files.
//
// # One runnable task per key
//
// Each firing is keyed (model.PathTriggerTag): (repo, branch, trigger,
// group) for a merge, (pull request, trigger, group) for a pull request.
// A key with a task that has never been dispatched has that task
// superseded by the new one, which keeps the earliest base commit it
// covered; a key whose task is running, parked or has a pull request
// open gets the new task filed depending on it. So however many merges
// land, a key never has two tasks that could run at once.

// pathTriggerPollInterval is how often one watched branch's head is read
// for its merge triggers: a GitHub read per branch, so not every tick.
const pathTriggerPollInterval = time.Minute

// pathTriggerPolled is when each (repo, branch) was last read. Process
// memory on purpose: forgetting it on a restart costs one read, and the
// cursor that decides what fires is in the store.
var pathTriggerPolled sync.Map

// reconcilePathTriggers fires every merge trigger whose branch has moved
// since it was last diffed.
func reconcilePathTriggers(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Client == nil {
		return nil
	}
	rules, err := deps.Store.PathTriggers(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading path triggers: %w", err)
	}
	byRepo := map[string][]model.RepoPathTrigger{}
	var repos []model.RepoRef
	for _, r := range rules {
		if r.Trigger.On != model.TriggerOnMerge {
			continue
		}
		key := strings.ToLower(r.Repo.String())
		if _, ok := byRepo[key]; !ok {
			repos = append(repos, r.Repo)
		}
		byRepo[key] = append(byRepo[key], r)
	}
	if len(repos) == 0 {
		return nil
	}
	watched, err := deps.Store.ListBaseBranchCI(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing watched base branches for path triggers: %w", err)
	}
	var errs []error
	for _, repo := range repos {
		rules := byRepo[strings.ToLower(repo.String())]
		def, err := pathTriggerDefaultBranch(deps, repo, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading %s's default branch for its triggers: %w", repo, err))
			continue
		}
		candidates := []string{def}
		for _, r := range rules {
			for _, b := range r.Trigger.Branches {
				if !strings.ContainsAny(b, "*?[\\") {
					candidates = append(candidates, b)
				}
			}
		}
		for _, row := range watched {
			if row.Repo.Same(repo) {
				candidates = append(candidates, row.Branch)
			}
		}
		seen := map[string]bool{}
		for _, branch := range candidates {
			if seen[branch] {
				continue
			}
			seen[branch] = true
			var watching []model.RepoPathTrigger
			for _, r := range rules {
				if r.Trigger.WatchesBranch(branch, def) {
					watching = append(watching, r)
				}
			}
			if len(watching) == 0 {
				continue
			}
			key := fmt.Sprintf("%d:%s@%s", deps.Store.ID(), strings.ToLower(repo.String()), branch)
			if last, ok := pathTriggerPolled.Load(key); ok && now.Sub(last.(time.Time)) < pathTriggerPollInterval {
				continue
			}
			// Past the floor, a branch nothing is merging into is read
			// less and less often (github.PollSchedule), and not at all
			// while the hour's budget is low: a merge trigger fired a few
			// minutes late costs nothing, and the merge queue's own reads
			// are what the budget is kept for.
			poll := github.PollKey{Scope: storeScope(deps.Store),
				Subject: github.BranchSubject(repo.Owner, repo.Name, branch), Aspect: "path triggers"}
			if !github.Polls().Due(poll, github.PollBackground, false, now) {
				continue
			}
			pathTriggerPolled.Store(key, now)
			if err := fireMergeTriggers(ctx, deps, repo, branch, watching, now); err != nil {
				errs = append(errs, err)
				continue
			}
			if cursor, err := deps.Store.GetPathTriggerCursor(ctx, repo, branch); err == nil {
				github.Polls().Observe(poll, cursor, now)
			}
		}
	}
	return errors.Join(errs...)
}

// storeScope keeps one store's poll schedule apart from another's in
// the one process-wide github.Polls -- parallel tests, mostly.
func storeScope(store *model.Store) string {
	if store == nil {
		return ""
	}
	return strconv.FormatUint(store.ID(), 10)
}

// pathTriggerDefaults is the default branch each repository's path
// triggers last read, per store and repository.
var pathTriggerDefaults sync.Map

// pathTriggerDefaultBranch is repo's default branch, read from GitHub
// when the poll schedule says it is due and otherwise remembered: it
// almost never changes, and reading it every cycle for every repository
// with a trigger was a call a tick for an answer that does not move.
func pathTriggerDefaultBranch(deps Deps, repo model.RepoRef, now time.Time) (string, error) {
	poll := github.PollKey{Scope: storeScope(deps.Store),
		Subject: github.BranchSubject(repo.Owner, repo.Name, ""), Aspect: "default branch"}
	cacheKey := poll.Scope + ":" + strings.ToLower(repo.String())
	if cached, ok := pathTriggerDefaults.Load(cacheKey); ok &&
		!github.Polls().Due(poll, github.PollBackground, false, now) {
		return cached.(string), nil
	}
	def, err := deps.Client.DefaultBranch(repo.Owner, repo.Name)
	if err != nil {
		return "", err
	}
	pathTriggerDefaults.Store(cacheKey, def)
	github.Polls().Observe(poll, def, now)
	return def, nil
}

// fireMergeTriggers diffs branch from where it was last diffed to where
// it is now and files what that change fires. One diff however many
// merges landed in between, and the cursor moves only once everything it
// fired is filed -- so a failure re-diffs the same range next time
// (coalescing makes a re-filed key supersede its own first try) and a
// restart neither re-fires nor skips.
//
// A branch never diffed before starts at its current head: there is no
// "before" to diff from, and firing on the whole history would be
// nobody's intent.
func fireMergeTriggers(ctx context.Context, deps Deps, repo model.RepoRef, branch string,
	rules []model.RepoPathTrigger, now time.Time) error {
	tip, err := branchTip(ctx, deps, repo, branch)
	if err != nil || tip == "" {
		return err
	}
	from, err := deps.Store.GetPathTriggerCursor(ctx, repo, branch)
	if err != nil {
		return err
	}
	if from == tip {
		return nil
	}
	if from == "" {
		log.Printf("orchestrator: %s %s: path triggers start watching at %s", repo, branch, shortCommit(tip))
		return deps.Store.PutPathTriggerCursor(ctx, repo, branch, tip, now)
	}
	files, err := comparedFiles(deps.Client, repo, from, tip)
	if err != nil {
		var gh *github.Error
		if errors.As(err, &gh) && gh.Status == 404 {
			// The commit it was diffed to is gone -- a force-push threw
			// it away. There is no range left to diff, so start again here.
			log.Printf("orchestrator: %s %s: %s is no longer there to diff from (%v); path triggers restart at %s",
				repo, branch, shortCommit(from), err, shortCommit(tip))
			return deps.Store.PutPathTriggerCursor(ctx, repo, branch, tip, now)
		}
		return fmt.Errorf("orchestrator: diffing %s %s..%s for its triggers: %w", repo, shortCommit(from), shortCommit(tip), err)
	}
	var errs []error
	for _, rule := range rules {
		matched := rule.Trigger.Match(files)
		groups := rule.Trigger.Groups(matched)
		for _, group := range sortedKeys(groups) {
			if _, err := fileMergeTrigger(ctx, deps, repo, branch, rule, group, groups[group], from, tip, now); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return deps.Store.PutPathTriggerCursor(ctx, repo, branch, tip, now)
}

// comparedFiles is every path changed between two commits.
func comparedFiles(client github.Client, repo model.RepoRef, from, to string) ([]string, error) {
	cmp, err := client.CompareRefs(repo.Owner, repo.Name, from, to)
	if err != nil {
		return nil, err
	}
	if cmp.FilesTruncated {
		log.Printf("orchestrator: %s %s..%s changes at least %d files, the most GitHub lists; "+
			"path triggers fire on those it did", repo, shortCommit(from), shortCommit(to), len(cmp.Files))
	}
	// A renamed file counts under both names: moving one out of a
	// watched folder is a change to that folder.
	var files []string
	for _, f := range cmp.Files {
		files = append(files, f.Filename)
		if f.PreviousFilename != "" && f.PreviousFilename != f.Filename {
			files = append(files, f.PreviousFilename)
		}
	}
	return files, nil
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fileMergeTrigger files one group of one merge firing, coalesced with
// whatever that key already has open.
func fileMergeTrigger(ctx context.Context, deps Deps, repo model.RepoRef, branch string,
	rule model.RepoPathTrigger, group string, paths []string, from, to string, now time.Time) (model.Task, error) {

	tag := model.PathTriggerTag(strings.ToLower(repo.String())+"@"+branch, rule.Trigger.Name, group)
	return fileCoalesced(ctx, deps.Store, tag, now, func(prev *model.Task, superseding bool) (model.Task, error) {
		if superseding {
			// Keep the earliest commit the superseded task covered, and
			// everything changed since then in this group.
			if earliest := model.PathTriggerFrom(prev.Tags); earliest != "" && earliest != from {
				files, err := comparedFiles(deps.Client, repo, earliest, to)
				if err == nil {
					from = earliest
					paths = rule.Trigger.Groups(rule.Trigger.Match(files))[group]
				} else {
					log.Printf("orchestrator: re-diffing %s %s..%s to coalesce trigger %s: %v -- "+
						"the new task covers %s on", repo, shortCommit(earliest), shortCommit(to),
						rule.Trigger.Name, err, shortCommit(from))
				}
			}
		}
		comp, _, err := deps.Store.ComposeTriggerTask(ctx, rule.Trigger.Template, repo, branch, rule.Trigger.AutoApprove)
		if err != nil {
			return model.Task{}, fmt.Errorf("orchestrator: composing trigger %s's task for %s: %w", rule.Trigger.Name, repo, err)
		}
		if comp.Base == "" {
			// A template bound to this repository with no branch of its
			// own still works on the branch that moved.
			comp.Base = branch
		}
		vars := triggerVars(rule, group, paths)
		vars["from_sha"], vars["to_sha"], vars["branch"] = from, to, branch
		comp.Title = model.SubstituteFields(comp.Title, vars)
		if strings.TrimSpace(comp.Title) == "" {
			comp.Title = "Trigger " + rule.Trigger.Name
			if group != "" {
				comp.Title += ": " + group
			}
		}
		comp.Body = model.SubstituteFields(comp.Body, vars) + triggerFacts(rule,
			fmt.Sprintf("merges to `%s` from %s to %s", branch, shortCommit(from), shortCommit(to)), group, paths,
			[][2]string{{"branch", branch}, {"from_sha", from}, {"to_sha", to}})
		id, err := deps.Store.NewTaskID(ctx)
		if err != nil {
			return model.Task{}, err
		}
		task := comp.Task(id, model.TriggerPrincipal, model.ReasonSchedule, now)
		task.WorkOrigin = mergeOrigin(deps.Client, repo, to)
		task.OrderKey, err = deps.Store.OrderKeyForNewTask(ctx, false)
		if err != nil {
			return model.Task{}, err
		}
		task.Tags = []string{model.PathTriggerMarkerTag, tag, model.PathTriggerFromTag(from),
			pathTriggerRuleTag(rule.Commit)}
		return task, nil
	})
}

// mergeOrigin is the origin of a task a merge trigger files: the pull
// request that merged the commit it fired at, so a design-doc planner and
// everything it files are followed from that pull request
// (docs/work-trees.md). "" when the commit came in some other way -- a
// direct push -- or GitHub cannot say, in which case the task is the root
// of its own tree.
func mergeOrigin(client github.Client, repo model.RepoRef, sha string) model.WorkOrigin {
	if client == nil {
		return ""
	}
	pr, err := client.MergedPullRequestForCommit(repo.Owner, repo.Name, sha)
	if err != nil || pr == nil || pr.Number <= 0 {
		return ""
	}
	return model.GitHubOrigin(repo, pr.Number)
}

// pathTriggerRuleTag records the base commit a filed task's rule (and
// template) was read at.
func pathTriggerRuleTag(commit string) string { return "path-trigger-rule:" + commit }

// triggerVars is what a trigger's template is filled in with, beyond the
// event's own: `{{trigger}}`, `{{paths}}` and `{{group}}`.
func triggerVars(rule model.RepoPathTrigger, group string, paths []string) map[string]string {
	return map[string]string{
		"trigger": rule.Trigger.Name,
		"paths":   strings.Join(paths, "\n"),
		"group":   group,
	}
}

// triggerFacts is the block appended to every triggered task's body:
// which trigger fired, on what, from which base commit's rule, and the
// files -- the same facts the template's variables carry, for a template
// that does not use them.
func triggerFacts(rule model.RepoPathTrigger, what, group string, paths []string, extra [][2]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n---\n\ngrain: filed by the path trigger `%s` (`%s` as of base commit %s) for %s.\n\n",
		rule.Trigger.Name, rule.Trigger.Path, shortCommit(rule.Commit), what)
	fmt.Fprintf(&b, "- trigger: `%s`\n", rule.Trigger.Name)
	if group != "" {
		fmt.Fprintf(&b, "- group: `%s`\n", group)
	}
	for _, kv := range extra {
		fmt.Fprintf(&b, "- %s: `%s`\n", kv[0], kv[1])
	}
	b.WriteString("- paths:\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "  - `%s`\n", p)
	}
	return b.String()
}

// fileCoalesced files the task build makes under tag, so that tag never
// has two runnable tasks: an open task under it that has never been
// dispatched is superseded by the new one, and one that has been is
// depended on by it. build is told which, and given the previous task.
func fileCoalesced(ctx context.Context, store *model.Store, tag string, now time.Time,
	build func(prev *model.Task, superseding bool) (model.Task, error)) (model.Task, error) {

	prev, err := store.OpenTaskWithTag(ctx, tag)
	if err != nil {
		return model.Task{}, err
	}
	superseding := false
	if prev != nil {
		_, refusal, err := store.SupersedeCandidate(ctx, prev.ID, prev.Target)
		if err != nil {
			return model.Task{}, err
		}
		superseding = refusal == ""
	}
	task, err := build(prev, superseding)
	if err != nil {
		return model.Task{}, err
	}
	if prev != nil {
		if superseding {
			task.Links = append(task.Links, model.Link{Kind: model.LinkSupersedes, Target: prev.ID})
			// What the superseded task was waiting on, this one waits on.
			for _, l := range prev.Links {
				if l.Kind == model.LinkDependsOn {
					task.Links = append(task.Links, l)
				}
			}
		} else {
			task.Links = append(task.Links, model.Link{Kind: model.LinkDependsOn, Target: prev.ID})
		}
	}
	if err := putFiledTask(ctx, store, task); err != nil {
		return model.Task{}, err
	}
	if superseding {
		if superseded, err := store.Supersede(ctx, prev.ID, task, "", now); err != nil {
			return task, fmt.Errorf("orchestrator: superseding %s with %s: %w", prev.ID, task.ID, err)
		} else if superseded.Refusal != "" {
			// Dispatched in the moment between the two reads: wait on it
			// instead, which is the other half of the rule.
			if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
				t.Links = append(withoutLinks(t.Links, model.LinkSupersedes),
					model.Link{Kind: model.LinkDependsOn, Target: prev.ID})
				return nil
			}); err != nil {
				return task, err
			}
		}
	}
	log.Printf("orchestrator: path trigger filed %s (%s)", task.ID, tag)
	return task, nil
}

// putFiledTask writes a task fileCoalesced's build made -- or, for one
// build already wrote (a stacked review), updates it to carry the tags
// and links build added.
func putFiledTask(ctx context.Context, store *model.Store, task model.Task) error {
	existing, err := store.GetTask(ctx, task.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return store.PutTask(ctx, task)
	}
	return store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Tags = task.Tags
		t.Links = task.Links
		t.Approval, t.ApprovedAt = task.Approval, task.ApprovedAt
		return nil
	})
}
