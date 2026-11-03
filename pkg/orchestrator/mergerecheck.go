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
	"errors"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The overlap re-check (grain/task-985, 1023, docs/merge-queue-behind.md "The
// overlap re-check").
//
// A change green on its own head merges as it stands (grain/task-198):
// the tree grain CI passed is the change on top of the commit it forked
// from, and the tree it lands as is the change on top of the branch's tip
// now. Re-checking every merge would double the suites to catch something
// that happens about once a week.
//
// So the re-check is asked for only where the two trees can differ in a
// way the change's own run could not see: where what the change touches
// and what has landed since it forked overlap. Let F be where the change
// last met the branch, T the branch's tip, P the files the change touches
// (F...head) and L the files that landed (F..T). F == T merges at once.
// Otherwise the change is re-checked on top of T if
//
//  1. P and L share a file;
//  2. some Go package directory has a changed .go file in both; or
//  3. L touches a global input -- go.mod, go.sum, a package manifest or
//     lockfile, .grain/workflows, a Makefile or build script;
//
// and merges on its own green if none of them hold.
//
// # Why F rather than the run's recorded base
//
// grain CI runs a task's suite on the pull request's head commit as it
// stands, not on head merged into its base. The base commit a run
// records (ci.Request.BaseCommit, JobRun.BaseCommit) is the branch's tip
// when the run was planned, kept to invalidate cached failures when the
// base moves -- it is not in the tree that was tested unless the branch
// had already merged it. The base the tested tree actually contains is
// the merge base of head and the branch, which is what GitHub's compare
// endpoint reports. Taking the recorded base instead would leave out
// everything that landed between the fork and the run, which is exactly
// the window a stale branch's conflicts live in.
//
// # Modes
//
// The rule is applied per repository (model.RepoConfig.MergeRecheck):
// off turns the rule off, observe decides as off does and records what
// the rule would have said on every merge, and enforce -- the default once
// merge batching is removed (grain/task-1023) -- re-checks what triggers
// by testing that single change merged onto the current tip on a ref of
// the queue's own for that change (model.MergeRecheckRef) and merging on
// green. Each change has its own ref and row, so overlapping changes on
// one branch are re-checked side by side rather than one at a time; a
// green whose base the branch has moved past since is re-validated against
// the live tip before it merges (stillHoldsOn, grain/task-2390). On red, it
// takes the normal repair path: one automatic repair (or a second when
// secondRepairReason holds), then merge_failed to the Inbox.

// maxMergeBatchAge bounds how long an overlap re-check run may wait for
// grain CI before the queue gives up on that run.
const maxMergeBatchAge = model.MaxMergeBatchAge

// recheckClauseOrder is the order clauses are listed in a record.
var recheckClauseOrder = []string{
	model.MergeRecheckClauseSameFile,
	model.MergeRecheckClauseSamePackage,
	model.MergeRecheckClauseGlobalInput,
	model.MergeRecheckClauseUnknown,
}

// overlapVerdict is the rule's answer about one change.
type overlapVerdict struct {
	// fork, tip and head are the three commits the rule was asked about.
	// fork == tip is "nothing has landed since", which never triggers.
	fork, tip, head string
	// hasFiles is whether the change names at least one changed file.
	hasFiles bool
	// trigger is whether the change has to be re-checked on top of tip.
	trigger bool
	// clauses is every clause that fired, in recheckClauseOrder; detail
	// names the first thing that fired each, for a person reading the
	// record.
	clauses []string
	detail  string
}

// clauseList is clauses as a record stores them.
func (v overlapVerdict) clauseList() string { return strings.Join(v.clauses, ",") }

// why is the sentence a row says about a triggered verdict.
func (v overlapVerdict) why() string {
	if !v.trigger {
		return "nothing it changes overlaps what has landed since it was tested"
	}
	return fmt.Sprintf("what it changes overlaps what has landed since %s, the commit its own run "+
		"was tested on (%s: %s)", shortSHA(v.fork), v.clauseList(), v.detail)
}

// overlapRule is the rule itself, over two file lists: changed is P, the
// paths the change touches, and landed is L, the paths that landed on the
// branch since the change forked. Either list incomplete -- GitHub caps a
// comparison's files at 300 -- is complete=false, and a rule that cannot
// see the whole of both sides cannot say they do not overlap.
//
// The fork == tip case is the caller's (recheckFor): with nothing landed
// there are no lists to compare.
func overlapRule(changed, landed []string, complete bool) overlapVerdict {
	var v overlapVerdict
	details := map[string]string{}
	fire := func(clause, what string) {
		if _, ok := details[clause]; ok {
			return
		}
		details[clause] = what
	}

	changedSet := map[string]bool{}
	changedPkgs := map[string]bool{}
	for _, p := range changed {
		changedSet[p] = true
		if goPackageFile(p) {
			changedPkgs[path.Dir(p)] = true
		}
	}
	landedSorted := append([]string(nil), landed...)
	sort.Strings(landedSorted)
	for _, p := range landedSorted {
		if changedSet[p] {
			fire(model.MergeRecheckClauseSameFile, p)
		}
		if goPackageFile(p) && changedPkgs[path.Dir(p)] {
			fire(model.MergeRecheckClauseSamePackage, path.Dir(p))
		}
		if globalBuildInput(p) {
			fire(model.MergeRecheckClauseGlobalInput, p)
		}
	}
	if !complete {
		fire(model.MergeRecheckClauseUnknown, "the comparison's file list was cut short")
	}
	var parts []string
	for _, c := range recheckClauseOrder {
		if what, ok := details[c]; ok {
			v.clauses = append(v.clauses, c)
			parts = append(parts, c+" "+what)
		}
	}
	v.trigger = len(v.clauses) > 0
	v.detail = strings.Join(parts, "; ")
	return v
}

// goPackageFile is whether p is Go source that belongs to the package in
// its directory -- its tests included, since a test in the package is
// compiled with it.
func goPackageFile(p string) bool { return strings.HasSuffix(p, ".go") }

// globalBuildInputNames are files every build reads wherever they sit in
// the tree: Go's module files, the JavaScript package manifests and
// lockfiles, and a Makefile.
var globalBuildInputNames = map[string]bool{
	"go.mod": true, "go.sum": true, "go.work": true, "go.work.sum": true,
	"package.json": true, "package-lock.json": true, "npm-shrinkwrap.json": true,
	"yarn.lock": true, "pnpm-lock.yaml": true, "pnpm-workspace.yaml": true, "bun.lockb": true,
	"Makefile": true, "GNUmakefile": true, "makefile": true,
}

// globalBuildInput is clause 3's list: a change to one of these changes
// what every package is built or tested with, so no file or package
// overlap is needed for it to break a change that touches neither.
func globalBuildInput(p string) bool {
	base := path.Base(p)
	switch {
	case globalBuildInputNames[base]:
		return true
	case strings.HasSuffix(base, ".mk"):
		return true
	case strings.HasPrefix(p, ".grain/workflows/"):
		return true
	case strings.HasPrefix(base, "Dockerfile.build"):
		return true
	case strings.HasPrefix(p, "scripts/") && strings.HasPrefix(base, "build"):
		return true
	}
	return false
}

// comparedPaths is every path a comparison names, a rename's old path as
// well as its new one: a file renamed on one side and edited on the other
// is an overlap under either name.
func comparedPaths(files []github.ComparedFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Filename)
		if f.PreviousFilename != "" {
			out = append(out, f.PreviousFilename)
		}
	}
	return out
}

// recheckFor asks GitHub for the three commits and two file lists the
// rule needs, and answers the rule. tip may be given by a caller that
// already holds the branch's tip; empty reads it.
//
// Two comparisons: tip...head, whose merge base is F and whose files are
// P (GitHub's three-dot diff is from the merge base), and F...tip, whose
// files are L. A comparison GitHub will not answer is an error rather
// than a guess: the caller decides what an unanswerable rule means.
func recheckFor(client github.Client, repo model.RepoRef, branch, tip, head string) (overlapVerdict, error) {
	if tip == "" {
		h, err := client.GetBranchHead(repo.Owner, repo.Name, branch)
		if err != nil {
			return overlapVerdict{}, fmt.Errorf("reading %s's tip on %s: %w", branch, repo, err)
		}
		if h == nil || h.SHA == "" {
			return overlapVerdict{}, fmt.Errorf("%s has no tip on %s", branch, repo)
		}
		tip = h.SHA
	}
	change, err := client.CompareRefs(repo.Owner, repo.Name, tip, head)
	if err != nil {
		return overlapVerdict{}, fmt.Errorf("comparing %s with %s on %s: %w", shortSHA(head), branch, repo, err)
	}
	fork := change.MergeBase
	if fork == "" {
		return overlapVerdict{}, fmt.Errorf("GitHub named no merge base for %s and %s on %s",
			shortSHA(head), branch, repo)
	}
	if fork == tip {
		return overlapVerdict{fork: fork, tip: tip, head: head, hasFiles: len(change.Files) > 0}, nil
	}
	landed, err := client.CompareRefs(repo.Owner, repo.Name, fork, tip)
	if err != nil {
		return overlapVerdict{}, fmt.Errorf("comparing %s with %s's tip on %s: %w", shortSHA(fork), branch, repo, err)
	}
	v := overlapRule(comparedPaths(change.Files), comparedPaths(landed.Files),
		!change.FilesTruncated && !landed.FilesTruncated)
	v.fork, v.tip, v.head = fork, tip, head
	v.hasFiles = len(change.Files) > 0
	return v, nil
}

// recheckModeFor is one repository's mode, the default filled in, along
// with whether the repository explicitly configured a mode.
func recheckModeFor(ctx context.Context, store *model.Store, repo model.RepoRef) (model.MergeRecheckMode, bool) {
	if store == nil {
		return model.MergeRecheckOff, false
	}
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		log.Printf("orchestrator: reading %s's merge re-check mode: %v", repo, err)
		return model.MergeRecheckEnforce, false
	}
	explicit := cfg != nil && cfg.MergeRecheck != ""
	return model.MergeRecheckModeOf(cfg), explicit
}

// recheckOutcome is what happened to the change besides the rule's
// answer: whether it was tested on top of the branch, and whether that
// test caught it.
type recheckOutcome struct {
	rechecked, caught bool
}

// recordRecheck writes one record of the rule's answer and what came of
// it. Best-effort: the record is a measurement, and a merge that has
// already happened is not undone because it could not be written down.
func recordRecheck(ctx context.Context, store *model.Store, repo model.RepoRef, branch string,
	number int, taskID string, mode model.MergeRecheckMode, v overlapVerdict, o recheckOutcome, now time.Time) {

	if store == nil || mode == model.MergeRecheckOff {
		return
	}
	if err := store.RecordMergeRecheck(ctx, model.MergeRecheckRecord{
		Repo: repo, Branch: branch, Number: number, TaskID: taskID, Mode: mode,
		Head: v.head, Fork: v.fork, Tip: v.tip,
		Triggered: v.trigger, Clauses: v.clauseList(), Detail: v.detail,
		Rechecked: o.rechecked, Caught: o.caught, DecidedAt: now,
	}); err != nil {
		log.Printf("orchestrator: recording the merge re-check decision for %s: %v", taskID, err)
	}
}

// observeRecheck asks the rule about a change that is merging anyway and
// records the answer -- used when a re-check has passed and merges.
func observeRecheck(ctx context.Context, store *model.Store, client github.Client, repo model.RepoRef,
	branch, tip, head string, number int, taskID string, o recheckOutcome, now time.Time) {

	mode, _ := recheckModeFor(ctx, store, repo)
	if mode == model.MergeRecheckOff || client == nil || head == "" {
		return
	}
	v, err := recheckFor(client, repo, branch, tip, head)
	if err != nil {
		log.Printf("orchestrator: asking the overlap rule about %s: %v", taskID, err)
		return
	}
	recordRecheck(ctx, store, repo, branch, number, taskID, mode, v, o, now)
}

// singleMergeRecheck is what the merge path does with a change the
// predicate has just found eligible on its own head, before merging it:
// in enforce mode, a change whose verdict triggers is not merged but
// tested on top of the branch, on a ref of its own (model.MergeRecheckRef)
// and beside any other change being re-checked on the same branch.
//
// It answers whether the merge may go ahead now, and the verdict and
// outcome to record if it does. held is the note for the row when it may
// not. A rule GitHub would not answer holds the change in enforce mode --
// the rule exists because merging unchecked is the risk, so not knowing
// is not a pass -- and is merged unrecorded otherwise.
type singleRecheck struct {
	mode    model.MergeRecheckMode
	verdict *overlapVerdict
	outcome recheckOutcome
	held    string
}

func singleMergeRecheck(ctx context.Context, store *model.Store, client github.Client, ciRunner ci.Runner,
	e queueEntry, detail github.PullRequestDetail, now time.Time) (singleRecheck, error) {

	repo, branch := e.ref.Repo, detail.BaseRef
	mode, explicit := recheckModeFor(ctx, store, repo)
	out := singleRecheck{mode: mode}
	if out.mode == model.MergeRecheckOff || detail.HeadSHA == "" || branch == "" {
		return out, nil
	}
	// A pull request gated by GitHub's checks rather than grain CI has no
	// grain CI suite to re-check it with unless the repository explicitly
	// asked for enforce mode.
	if !e.grain.Required && !explicit {
		return singleRecheck{}, nil
	}
	if ciRunner == nil && !explicit {
		out.mode = model.MergeRecheckObserve
	}
	v, err := recheckFor(client, repo, branch, "", detail.HeadSHA)
	if err != nil {
		log.Printf("orchestrator: asking the overlap rule about %s: %v", e.task.ID, err)
		if out.mode == model.MergeRecheckEnforce {
			out.held = "grain could not tell whether this change overlaps what has landed on " +
				branch + " since it was tested, so it is not merging on its own green: " + err.Error()
		}
		return out, nil
	}
	out.verdict = &v
	if out.mode != model.MergeRecheckEnforce || !v.trigger {
		return out, nil
	}
	if e.obs != nil && e.obs.MergeQueueBlockedAt != nil {
		out.held = "blocked"
		return out, nil
	}
	if requiresGitHubQueue(repo, branch) {
		return out, nil
	}
	if ciRunner == nil {
		out.held = v.why() + ", and this deployment runs no grain CI to re-check it with"
		return out, nil
	}
	// Only this change's own re-check can hold it: every change gets a
	// row and a ref of its own (model.MergeRecheckRef), and a re-check
	// another change has running on the same branch is re-validated
	// against the live tip when it concludes (advanceMergeRecheck), so the
	// two never need to wait on each other (grain/task-2390). A row for
	// a head this change has since moved off tests a commit that is not
	// going to merge, and gives way to the one that is.
	if row, err := store.GetMergeBatch(ctx, repo, branch, e.task.ID); err != nil {
		return out, fmt.Errorf("orchestrator: reading the overlap re-check of %s on %s %s: %w",
			e.task.ID, repo, branch, err)
	} else if row != nil && row.Running() {
		if len(row.Members) > 0 && row.Members[0].HeadSHA == detail.HeadSHA {
			out.held = v.why() + fmt.Sprintf(", and its overlap re-check on `%s` is still running", row.Ref)
			return out, nil
		}
		ciRunner.Cancel(row.RunID, "the change this overlap re-check was testing has a new head", ci.CancelBySystem)
		deps := Deps{Store: store, Client: client, CI: ciRunner}
		if err := discardMergeRecheck(ctx, deps, *row, e.task.ID, now); err != nil {
			return out, err
		}
	}
	started, err := startMergeRecheck(ctx, Deps{Store: store, Client: client, CI: ciRunner}, repo, branch,
		model.MergeBatchMember{TaskID: e.task.ID, Number: e.ref.Number, HeadSHA: detail.HeadSHA}, now)
	if err != nil {
		return out, err
	}
	if !started {
		out.held = v.why() + ", and the re-check on top of " + branch + " could not be started: " +
			"it is asked again next cycle"
		return out, nil
	}
	log.Printf("orchestrator: %s overlaps what landed on %s %s since %s (%s): re-checking it on top of %s",
		e.task.ID, repo, branch, shortSHA(v.fork), v.clauseList(), shortSHA(v.tip))
	out.held = "started"
	return out, nil
}

// advanceMergeRechecks collects the verdict of any in-flight overlap
// re-check before SyncPullRequests walks the queue, so a re-check that
// passed merges on the same tick its verdict arrives and the sync walk
// closes out the merged pull request immediately.
func advanceMergeRechecks(ctx context.Context, deps Deps, now time.Time) error {
	if deps.CI == nil || deps.Store == nil || deps.Client == nil {
		return nil
	}
	rows, err := deps.Store.ListMergeBatches(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading overlap re-checks: %w", err)
	}
	var errs []error
	for _, row := range rows {
		if err := advanceMergeRecheck(ctx, deps, row, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func advanceMergeRecheck(ctx context.Context, deps Deps, row model.MergeBatch, now time.Time) error {
	if !row.Running() || len(row.Members) == 0 {
		var errs []error
		errs = append(errs, dropRecheckRef(deps, row))
		for _, m := range row.Members {
			errs = append(errs, clearRecheckState(ctx, deps, m.TaskID, now))
		}
		errs = append(errs, deps.Store.DeleteMergeBatch(ctx, row.Repo, row.Branch, row.TaskID))
		return errors.Join(errs...)
	}
	m := row.Members[0]
	age := now.Sub(row.StartedAt)
	if age > maxMergeBatchAge {
		deps.CI.Cancel(row.RunID, "the merge queue gave up waiting for this overlap re-check", ci.CancelBySystem)
		return discardMergeRecheck(ctx, deps, row, m.TaskID, now)
	}
	run, ok := deps.CI.Get(row.RunID)
	if !ok || run.Interrupted {
		return discardMergeRecheck(ctx, deps, row, m.TaskID, now)
	}
	if !run.Conclusion.Done() {
		return nil
	}
	head, err := deps.Client.GetBranchHead(row.Repo.Owner, row.Repo.Name, row.Branch)
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's head on %s: %w", row.Branch, row.Repo, err)
	}
	moved := head != nil && head.SHA != "" && head.SHA != row.BaseCommit
	if moved && (run.Conclusion != ci.ConclusionSuccess || !stillHoldsOn(deps.Client, row, m, head.SHA)) {
		return discardMergeRecheck(ctx, deps, row, m.TaskID, now)
	}
	row.Conclusion, row.Detail = string(run.Conclusion), ciDetail(run)
	finished := run.FinishedAt
	if finished == nil {
		finished = &now
	}
	row.FinishedAt = finished
	switch {
	case row.Conclusion == string(ci.ConclusionSuccess):
		return mergeRecheckedMember(ctx, deps, row, m, now)
	case row.Failed():
		var errs []error
		errs = append(errs, dropRecheckRef(deps, row))
		errs = append(errs, clearRecheckState(ctx, deps, m.TaskID, now))
		errs = append(errs, sendRecheckFailureBack(ctx, deps, row, m, now))
		errs = append(errs, deps.Store.DeleteMergeBatch(ctx, row.Repo, row.Branch, row.TaskID))
		return errors.Join(errs...)
	case row.Conclusion == string(ci.ConclusionErrored):
		var errs []error
		errs = append(errs, dropRecheckRef(deps, row))
		errs = append(errs, clearRecheckState(ctx, deps, m.TaskID, now))
		errs = append(errs, blockErroredMergeRecheck(ctx, deps, row, m, now))
		errs = append(errs, deps.Store.DeleteMergeBatch(ctx, row.Repo, row.Branch, row.TaskID))
		return errors.Join(errs...)
	default:
		return discardMergeRecheck(ctx, deps, row, m.TaskID, now)
	}
}

// stillHoldsOn is whether a re-check that passed on top of row.BaseCommit
// still speaks for the merge now that the branch has moved on to tip --
// because another change merged while this one's re-check ran, which with
// a re-check per change (grain/task-2390) is the ordinary case rather than
// the exception.
//
// It is the overlap rule again, asked about what has landed since the
// re-check's base rather than since the change forked: the re-check
// tested the change on top of row.BaseCommit, so if nothing that landed
// after that overlaps what the change touches, the tree the merge
// produces differs from the tested one only where the change's own run
// and the re-check could not have been affected, and the green stands.
// Anything else -- the branch rewritten rather than moved forward, a
// comparison GitHub will not answer or cuts short, a file or package in
// common, a global build input -- is a tree the re-check never saw, and
// the change is re-checked on top of tip instead.
func stillHoldsOn(client github.Client, row model.MergeBatch, m model.MergeBatchMember, tip string) bool {
	if m.HeadSHA == "" || row.BaseCommit == "" {
		return false
	}
	landed, err := client.CompareRefs(row.Repo.Owner, row.Repo.Name, row.BaseCommit, tip)
	if err != nil {
		log.Printf("orchestrator: comparing %s's re-check base %s with %s on %s: %v",
			m.TaskID, shortSHA(row.BaseCommit), shortSHA(tip), row.Repo, err)
		return false
	}
	if landed.MergeBase != row.BaseCommit {
		return false
	}
	change, err := client.CompareRefs(row.Repo.Owner, row.Repo.Name, row.BaseCommit, m.HeadSHA)
	if err != nil {
		log.Printf("orchestrator: comparing %s with its re-check base %s on %s: %v",
			m.TaskID, shortSHA(row.BaseCommit), row.Repo, err)
		return false
	}
	v := overlapRule(comparedPaths(change.Files), comparedPaths(landed.Files),
		!change.FilesTruncated && !landed.FilesTruncated)
	if v.trigger {
		log.Printf("orchestrator: %s's re-check on %s passed on %s, but what landed since overlaps it (%s): "+
			"re-checking it on top of %s", m.TaskID, row.Repo, shortSHA(row.BaseCommit), v.detail, shortSHA(tip))
		return false
	}
	log.Printf("orchestrator: %s's re-check on %s passed on %s, and nothing landed since overlaps it: "+
		"merging on top of %s", m.TaskID, row.Repo, shortSHA(row.BaseCommit), shortSHA(tip))
	return true
}

func discardMergeRecheck(ctx context.Context, deps Deps, row model.MergeBatch, taskID string, now time.Time) error {
	var errs []error
	errs = append(errs, dropRecheckRef(deps, row))
	errs = append(errs, clearRecheckState(ctx, deps, taskID, now))
	errs = append(errs, deps.Store.DeleteMergeBatch(ctx, row.Repo, row.Branch, row.TaskID))
	return errors.Join(errs...)
}

func mergeRecheckedMember(ctx context.Context, deps Deps, row model.MergeBatch,
	m model.MergeBatchMember, now time.Time) error {

	task, err := deps.Store.GetTask(ctx, m.TaskID)
	if err != nil {
		return err
	}
	if task == nil || !task.AutoMerge || len(model.PendingApprovals(*task)) > 0 || taskPaused(ctx, deps.Store, m.TaskID) {
		return discardMergeRecheck(ctx, deps, row, m.TaskID, now)
	}
	obs, err := deps.Store.GetObservation(ctx, m.TaskID)
	if err != nil {
		return err
	}
	if obs != nil && (obs.MergeQueueBlockedAt != nil || obs.RepairInFlight()) {
		return discardMergeRecheck(ctx, deps, row, m.TaskID, now)
	}
	runs, err := deps.Store.LiveRuns(ctx)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.TaskID == m.TaskID {
			return discardMergeRecheck(ctx, deps, row, m.TaskID, now)
		}
	}
	review, err := reviewStateOf(ctx, deps.Store, *task, obs, now)
	if err != nil {
		return err
	}
	if review.pending || review.overdue {
		return discardMergeRecheck(ctx, deps, row, m.TaskID, now)
	}
	decision := mayMerge(mergeRequest{
		taskID: m.TaskID, branch: row.Branch, clean: true, greenHead: true,
	})
	if !decision.ok {
		return discardMergeRecheck(ctx, deps, row, m.TaskID, now)
	}
	if err := mergeTaskPullRequest(ctx, deps.Store, deps.Client, row.Repo,
		m.Number, m.HeadSHA, m.TaskID, ""); err != nil {
		if github.IsMergeQueueRequired(err) {
			noteGitHubQueueBranch(row.Repo, row.Branch)
		}
		return discardMergeRecheck(ctx, deps, row, m.TaskID, now)
	}
	observeRecheck(ctx, deps.Store, deps.Client, row.Repo, row.Branch, row.BaseCommit, m.HeadSHA,
		m.Number, m.TaskID, recheckOutcome{rechecked: true}, now)
	var errs []error
	errs = append(errs, clearRecheckState(ctx, deps, m.TaskID, now))
	errs = append(errs, noteRecheckMerged(ctx, deps, row, m.TaskID, now))
	errs = append(errs, dropRecheckRef(deps, row))
	errs = append(errs, deps.Store.DeleteMergeBatch(ctx, row.Repo, row.Branch, row.TaskID))
	return errors.Join(errs...)
}

func noteRecheckMerged(ctx context.Context, deps Deps, row model.MergeBatch, taskID string, now time.Time) error {
	body := fmt.Sprintf("Merged after an overlap re-check: grain CI tested `%s` at %s "+
		"(`%s` with this change merged into it), and the suite passed, so the change "+
		"landed on that run. `%s` is still tested on its own after the merge, and a failure "+
		"there earns a repair as it always has -- it holds no merges, so the queue carries "+
		"on either way.",
		row.Ref, shortSHA(row.Commit), row.Branch, row.Branch)
	return queueComment(ctx, deps.Store, taskID, body, now)
}

func sendRecheckFailureBack(ctx context.Context, deps Deps, row model.MergeBatch,
	m model.MergeBatchMember, now time.Time) error {

	obs, err := deps.Store.GetObservation(ctx, m.TaskID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading observation for %s: %w", m.TaskID, err)
	}
	body := fmt.Sprintf("The merge queue tested this change on top of `%s` at %s -- grain CI "+
		"against `%s`, which is `%s` with this change merged into it and nothing else -- and the "+
		"suite failed: %s\n\nThis branch is green on its own head. What it is not is green on the "+
		"tree it would land on, which is the failure a post-merge `%s` would otherwise have "+
		"discovered with the change already in it.",
		row.Branch, shortSHA(row.BaseCommit), row.Ref, row.Branch, row.Detail, row.Branch)
	observeRecheck(ctx, deps.Store, deps.Client, row.Repo, row.Branch, row.BaseCommit, m.HeadSHA,
		m.Number, m.TaskID, recheckOutcome{rechecked: true, caught: true}, now)
	failures := signatureOf(failedJobsOf(row.Detail))
	rc := repairCap{max: maxAutomaticRepairs}
	if task, err := deps.Store.GetTask(ctx, m.TaskID); err == nil && task != nil {
		rc = repairCapOf(recordedPolicy(*task))
	}
	leave := func(spent int) error {
		if err := observeField(ctx, deps.Store, m.TaskID, now, func(o *model.Observation) {
			if o.CI != nil && o.CI.Commit == m.HeadSHA {
				o.CI.Conclusion = row.Conclusion
				o.CI.Detail = row.Detail
				o.CI.FinishedAt = row.FinishedAt
			}
		}); err != nil {
			return err
		}
		return blockMergeQueue(ctx, deps.Store, m.TaskID,
			body+"\n\n"+repairsSpentSentence(spent, "`"+row.Branch+"`", rc)+fmt.Sprintf(" The queue is leaving "+
				"it to a person: push a fix and it merges as soon as it is clean and green; the "+
				"queue has moved on to the rest of %s.", row.Repo),
			"it broke grain CI when tested on top of "+row.Branch+", and "+repairsSpentReason(spent, rc), now)
	}
	why := ""
	spent := obs.RepairsSpent()
	if spent >= rc.max {
		return leave(spent)
	}
	if spent > 0 {
		why = secondRepairReason(obs, row.Branch, row.BaseCommit, failures)
		if why == "" {
			return leave(spent)
		}
	}
	body += fmt.Sprintf("\n\nWork on the branch this pull request is already open from -- no "+
		"separate task, no approval -- and it merges as soon as a full grain CI suite passes for "+
		"the commit you push. Everything else queued for %s has carried on merging without it. %s",
		row.Repo, repairAttemptSentence(spent+1, rc.max, why))
	if err := queueComment(ctx, deps.Store, m.TaskID, body, now); err != nil {
		return err
	}
	return observeField(ctx, deps.Store, m.TaskID, now, func(o *model.Observation) {
		markRepairAsked(o, now, row.BaseCommit, failures, why)
	})
}

// blockErroredMergeRecheck hands m's pull request to a person when its
// overlap re-check run concludes errored without judging the tree -- for
// instance when no workflow in the repository answers pull_request on the
// merge-queue ref, or when no sandbox could be acquired. Discarding the row
// silently would fall straight back into startMergeRecheck on the same and
// every subsequent sync cycle, spinning in an unbounded CI loop; sending it
// to sendRecheckFailureBack would spend an automatic agent repair on a
// verdict that says nothing about the branch's code.
func blockErroredMergeRecheck(ctx context.Context, deps Deps, row model.MergeBatch,
	m model.MergeBatchMember, now time.Time) error {

	detail := row.Detail
	if detail == "" {
		detail = "grain CI could not run"
	}
	body := fmt.Sprintf("The merge queue tried to re-check this change on top of `%s` at %s -- "+
		"grain CI against `%s`, which is `%s` with this change merged into it and nothing else -- "+
		"and the run errored without reaching a verdict about the tree: %s\n\n"+
		"Nothing here says the branch is broken, so no automatic repair was spent. The queue has "+
		"handed this pull request to a person and moved on to the rest of %s: once what stopped "+
		"grain CI from running has been resolved, press \"Retry merge\" to run the check again.",
		row.Branch, shortSHA(row.BaseCommit), row.Ref, row.Branch, detail, row.Repo)
	if err := observeField(ctx, deps.Store, m.TaskID, now, func(o *model.Observation) {
		if o.CI != nil && o.CI.Commit == m.HeadSHA {
			o.CI.Conclusion = row.Conclusion
			o.CI.Detail = detail
			o.CI.FinishedAt = row.FinishedAt
			if o.CI.Attempts < ciMaximumAttempts {
				o.CI.Attempts = ciMaximumAttempts
			}
		}
	}); err != nil {
		return err
	}
	reason := fmt.Sprintf("grain CI could not run its overlap re-check on top of %s (%s)",
		row.Branch, detail)
	return blockMergeQueueAgainst(ctx, deps.Store, m.TaskID, body, reason, row.BaseCommit, now)
}

func startMergeRecheck(ctx context.Context, deps Deps, repo model.RepoRef, branch string,
	m model.MergeBatchMember, now time.Time) (bool, error) {

	head, err := deps.Client.GetBranchHead(repo.Owner, repo.Name, branch)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s's head on %s: %w", branch, repo, err)
	}
	if head == nil || head.SHA == "" {
		return false, nil
	}
	deploymentID, err := deps.Store.DeploymentID(ctx)
	if err != nil {
		return false, fmt.Errorf("orchestrator: naming the re-check ref for %s on %s: %w", branch, repo, err)
	}
	ref := model.MergeRecheckRef(deploymentID, m.TaskID, branch)
	if err := resetRecheckRef(deps, repo, ref, head.SHA); err != nil {
		return false, err
	}
	detail, err := deps.Client.GetPullRequest(repo.Owner, repo.Name, m.Number)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s#%d for the overlap re-check on %s: %w",
			repo, m.Number, branch, err)
	}
	if detail.Merged || detail.State != "open" || detail.HeadRef == "" || detail.HeadSHA == "" {
		return false, nil
	}
	// m.HeadSHA is the commit the caller cleared -- security hold, pending
	// approvals, merge rules, its own green -- and a passing re-check merges
	// on its word alone (mergeRecheckedMember). A head pushed since that
	// read is none of those things, so it is not adopted: the next cycle
	// judges it like any other push.
	if m.HeadSHA == "" || detail.HeadSHA != m.HeadSHA {
		return false, nil
	}
	m.HeadRef = detail.HeadRef
	result, err := deps.Client.MergeBranch(repo.Owner, repo.Name, ref, m.HeadRef,
		fmt.Sprintf("grain merge queue: %s (%s) onto %s", m.TaskID, shortSHA(m.HeadSHA), branch))
	switch {
	case err == nil:
	case github.IsMergeConflict(err):
		return false, nil
	default:
		return false, fmt.Errorf("orchestrator: building the overlap re-check for %s %s (%s): %w",
			repo, branch, m.TaskID, err)
	}
	if !result.Merged {
		return false, nil
	}
	tip := result.SHA
	run, err := deps.CI.Start(ctx, ci.Request{
		Repo: repo.String(), Ref: ref, Commit: tip, Event: ci.EventPullRequest,
		RequestedBy: "merge-recheck:" + repo.String() + ":" + branch,
		GateOnBase:  true, BaseRef: branch, BaseCommit: head.SHA,
	})
	if err != nil {
		return false, fmt.Errorf("orchestrator: starting grain CI for the overlap re-check on %s %s: %w",
			repo, branch, err)
	}
	started := run.StartedAt
	if started == nil {
		started = &now
	}
	batch := model.MergeBatch{
		Repo: repo, Branch: branch, TaskID: m.TaskID, Ref: ref, BaseCommit: head.SHA, Commit: tip,
		Members: []model.MergeBatchMember{m}, RunID: run.ID, StartedAt: *started, Queued: 1,
	}
	if err := deps.Store.PutMergeBatch(ctx, batch); err != nil {
		return false, err
	}
	log.Printf("orchestrator: re-checking %s on %s %s on %s at %s (run %s)",
		m.TaskID, repo, branch, ref, shortSHA(tip), run.ID)
	if err := setRecheckState(ctx, deps, m.TaskID, batch, now); err != nil {
		return true, err
	}
	return true, nil
}

func resetRecheckRef(deps Deps, repo model.RepoRef, ref, sha string) error {
	exists, err := deps.Client.BranchExists(repo.Owner, repo.Name, ref)
	if err != nil {
		return fmt.Errorf("orchestrator: looking for %s on %s: %w", ref, repo, err)
	}
	if !exists {
		if err := deps.Client.CreateBranch(repo.Owner, repo.Name, ref, sha); err != nil {
			return fmt.Errorf("orchestrator: creating %s on %s at %s: %w", ref, repo, shortSHA(sha), err)
		}
		return nil
	}
	if err := deps.Client.UpdateBranch(repo.Owner, repo.Name, ref, sha, true); err != nil {
		return fmt.Errorf("orchestrator: resetting %s on %s to %s: %w", ref, repo, shortSHA(sha), err)
	}
	return nil
}

func dropRecheckRef(deps Deps, row model.MergeBatch) error {
	if deps.Client == nil || row.Ref == "" {
		return nil
	}
	if err := deps.Client.DeleteBranch(row.Repo.Owner, row.Repo.Name, row.Ref); err != nil {
		return fmt.Errorf("orchestrator: destroying %s on %s: %w", row.Ref, row.Repo, err)
	}
	return nil
}

func setRecheckState(ctx context.Context, deps Deps, taskID string,
	batch model.MergeBatch, now time.Time) error {

	obs, err := deps.Store.GetObservation(ctx, taskID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading observation for %s: %w", taskID, err)
	}
	if obs == nil {
		return nil
	}
	note := fmt.Sprintf("grain CI is re-checking this change on top of %s before it merges, on `%s` "+
		"at %s: what it changes overlaps what has landed on %s since the commit its own run "+
		"tested, and it merges if this passes", batch.Branch, batch.Ref, shortSHA(batch.Commit),
		batch.Branch)
	return setMergeQueueState(ctx, deps.Store, obs, taskID,
		queueStatus{state: mergeStateWaitingCI, reason: mergeReasonCIRunning, note: note}, now)
}

func clearRecheckState(ctx context.Context, deps Deps, taskID string, now time.Time) error {
	obs, err := deps.Store.GetObservation(ctx, taskID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading observation for %s: %w", taskID, err)
	}
	if obs == nil || !strings.HasPrefix(obs.MergeQueueNote, "grain CI is re-checking this change on top of ") {
		return nil
	}
	return setMergeQueueState(ctx, deps.Store, obs, taskID, queueStatus{}, now)
}

// recheckingTaskHeads returns the head SHA of each task currently undergoing
// an overlap re-check on its own re-check ref.
func recheckingTaskHeads(ctx context.Context, store *model.Store) (map[string]string, error) {
	if store == nil {
		return nil, nil
	}
	rows, err := store.ListMergeBatches(ctx)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: reading overlap re-checks: %w", err)
	}
	out := map[string]string{}
	for i := range rows {
		if !rows[i].Running() {
			continue
		}
		for _, m := range rows[i].Members {
			out[m.TaskID] = m.HeadSHA
		}
	}
	return out, nil
}

// baseVerdictOf is a base-branch run's conclusion as the overlap rule's
// record spells it, or "" for one that judged nothing.
func baseVerdictOf(conclusion string) string {
	switch conclusion {
	case "success":
		return model.MergeRecheckBaseGreen
	case "failure":
		return model.MergeRecheckBaseRed
	}
	return ""
}
