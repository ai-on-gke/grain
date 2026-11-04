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

// Stage 2 of release-candidate qualification, and the bridge that takes
// its verdict to GitHub (design/playbook/releases.playbook.md §2, §3,
// §4, §12 and §14; implementation gap 6).
//
// For every repository that declares a `release-qualification` suite,
// this reads the repository's tags for its newest `vX.Y.Z-rc.N` that has
// been neither discarded nor promoted. Once that tag's Stage 1 run
// (`nightly.yml`) is green on rc_sha, it starts the suite against it at
// model.PriorityReleaseGate, with the candidate's rc_tag and rc_sha
// filled into every item's template. It then judges each item
// objectively (model.JudgeQualificationAttempt), re-runs a first product
// failure once and an infrastructure casualty up to three times, files
// the suite's last item only once every other item has passed, and posts
// the outcome as check run `grain/release-qualification` on rc_sha.
//
// Everything here is outbound: grain asks GitHub, through the
// repository's own forge connection, and nothing on a GitHub runner has
// to reach grain. That is what lets the release train drop
// GRAIN_API_URL / GRAIN_API_TOKEN (§14).
//
// # State
//
// There is no table of its own. Every attempt is a suite run tagged
// model.ReleaseQualificationTag(repo, rc_tag), so the attempts of one
// candidate are Store.SuiteRunsTagged's answer; and the verdict, once
// reached, is the completed check run itself -- grain's own, posted by
// the GitHub App its forge connection is (latestCheckRun) -- so a
// candidate whose check run is completed is never looked at again: a candidate is
// qualified again by cutting rc.(N+1), the ordinary path after a red one.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ReleaseQualificationForge is every GitHub call the reconciler makes,
// all of them on the repository's own forge connection. github.RESTClient
// is one; a client lacking any of it is a deployment this does nothing
// for.
type ReleaseQualificationForge interface {
	github.TagLister
	github.CheckRunWriter
	ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error)
	// CheckRunApp is the GitHub App grain posts check runs on owner/repo
	// as (github.RESTClient.CheckRunApp), or "" when it cannot tell.
	CheckRunApp(owner, repo string) (string, error)
	ListWorkflowRuns(owner, repo, headSHA string) ([]github.CheckRun, error)
	BranchExists(owner, repo, branch string) (bool, error)
	DefaultBranch(owner, repo string) (string, error)
}

// releaseQualificationInterval is how often the cycle step asks GitHub.
// A candidate qualifies for an hour or more, so a minute of lag costs
// nothing, and a cycle every few seconds would spend two reads per
// repository each time for no answer that could have changed.
const releaseQualificationInterval = time.Minute

var releaseQualificationPolled sync.Map // *model.Store -> time.Time

// reconcileReleaseQualification is SyncReleaseQualification, at most once
// per releaseQualificationInterval.
func reconcileReleaseQualification(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Client == nil {
		return nil
	}
	forge, ok := deps.Client.(ReleaseQualificationForge)
	if !ok {
		return nil
	}
	if last, ok := releaseQualificationPolled.Load(deps.Store); ok && now.Sub(last.(time.Time)) < releaseQualificationInterval {
		return nil
	}
	releaseQualificationPolled.Store(deps.Store, now)
	return SyncReleaseQualification(ctx, deps.Store, forge, now)
}

// SyncReleaseQualification advances Stage 2 for every repository that
// declares a release-qualification suite. One repository's error does
// not stop the others.
func SyncReleaseQualification(ctx context.Context, store *model.Store, forge ReleaseQualificationForge, now time.Time) error {
	suites, err := store.ListSuites(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, suite := range suites {
		repo, ok := releaseQualificationRepo(suite.ID)
		if !ok {
			continue
		}
		if err := syncRepoReleaseQualification(ctx, store, forge, repo, suite, now); err != nil {
			errs = append(errs, fmt.Errorf("release qualification of %s: %w", repo, err))
		}
	}
	return errors.Join(errs...)
}

// releaseQualificationRepo is the repository a repo-declared
// release-qualification suite belongs to, read back out of its id
// (model.RepoDeclaredID).
func releaseQualificationRepo(id string) (model.RepoRef, bool) {
	prefix := model.RepoDeclaredPrefix + string(model.DeclaredSuite) + ":"
	suffix := ":" + model.ReleaseQualificationSuiteStem
	if !strings.HasPrefix(id, prefix) || !strings.HasSuffix(id, suffix) {
		return model.RepoRef{}, false
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(id, prefix), suffix), ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return model.RepoRef{}, false
	}
	return model.RepoRef{Owner: parts[0], Name: parts[1]}, true
}

// newestCandidate is the newest `vX.Y.Z-rc.N` among tags that has no
// `-discarded` marker and whose version has not been promoted -- the one
// candidate qualifying at a time (playbook, "Lifecycle").
func newestCandidate(tags []github.Tag) (model.RCTag, string, bool) {
	names := make(map[string]bool, len(tags))
	for _, t := range tags {
		names[t.Name] = true
	}
	var best model.RCTag
	var sha string
	found := false
	for _, t := range tags {
		rc, ok := model.ParseRCTag(t.Name)
		if !ok || t.SHA == "" || names[t.Name+"-discarded"] || names[rc.Version()] {
			continue
		}
		if !found || best.Less(rc) {
			best, sha, found = rc, t.SHA, true
		}
	}
	return best, sha, found
}

// latestCheckRun is the newest check run named name that app posted for
// externalID, or nil. One of the same name from anything else -- an
// Actions workflow with checks: write, another installed app, or an
// earlier discarded candidate on the same commit -- is not this
// candidate's verdict, and neither ends qualification nor is updated as
// if it were.
func latestCheckRun(runs []github.CheckRun, name, app, externalID string) *github.CheckRun {
	var out *github.CheckRun
	for i := range runs {
		if runs[i].Name == name && runs[i].App == app && runs[i].ExternalID == externalID &&
			(out == nil || runs[i].ID > out.ID) {
			out = &runs[i]
		}
	}
	return out
}

// stage1Green reports whether the newest Stage 1 workflow run on sha has
// succeeded. GitHub lists runs newest first.
func stage1Green(forge ReleaseQualificationForge, repo model.RepoRef, sha string) (bool, error) {
	runs, err := forge.ListWorkflowRuns(repo.Owner, repo.Name, sha)
	if err != nil {
		return false, err
	}
	for _, r := range runs {
		if r.Name != model.ReleaseQualificationStage1Workflow {
			continue
		}
		return r.Status == "completed" && r.Conclusion != nil && *r.Conclusion == "success", nil
	}
	return false, nil
}

func syncRepoReleaseQualification(ctx context.Context, store *model.Store, forge ReleaseQualificationForge,
	repo model.RepoRef, suite model.Suite, now time.Time) error {

	tags, err := forge.ListTags(repo.Owner, repo.Name)
	if err != nil {
		return fmt.Errorf("listing tags: %w", err)
	}
	rc, sha, ok := newestCandidate(tags)
	if !ok {
		return nil
	}
	rcTag := rc.String()
	app, err := forge.CheckRunApp(repo.Owner, repo.Name)
	if err != nil {
		return fmt.Errorf("reading which GitHub App posts %s on %s: %w", model.ReleaseQualificationCheckName, rcTag, err)
	}
	if app == "" {
		return fmt.Errorf("not qualifying %s: its forge connection is not a GitHub App grain can name, so its own %s could not be told from one anything else posted",
			rcTag, model.ReleaseQualificationCheckName)
	}
	checks, err := forge.ListCheckRuns(repo.Owner, repo.Name, sha)
	if err != nil {
		return fmt.Errorf("reading checks on %s: %w", rcTag, err)
	}
	tag := model.ReleaseQualificationTag(repo, rcTag)
	check := latestCheckRun(checks, model.ReleaseQualificationCheckName, app, tag)
	if check != nil && check.Status == "completed" {
		return nil
	}

	items := make([]string, 0, len(suite.Items))
	for _, it := range suite.Items {
		items = append(items, it.TemplateID)
	}
	runs, err := store.SuiteRunsTagged(ctx, tag)
	if err != nil {
		return err
	}
	q := rcQualification{store: store, forge: forge, repo: repo, suite: suite, rcTag: rcTag, sha: sha, tag: tag, now: now}
	if check != nil {
		q.checkID = check.ID
	}

	if len(runs) == 0 {
		green, err := stage1Green(forge, repo, sha)
		if err != nil {
			return fmt.Errorf("reading Stage 1 on %s: %w", rcTag, err)
		}
		if !green {
			return nil
		}
		gates := items
		if len(items) > 1 {
			gates = items[:len(items)-1]
		}
		if err := q.start(ctx, gates); err != nil {
			return err
		}
		return q.post("in_progress", "", fmt.Sprintf("Qualifying %s", rcTag),
			fmt.Sprintf("Stage 1 (%s) is green on %s; Stage 2 started: %s.",
				model.ReleaseQualificationStage1Workflow, sha, strings.Join(gates, ", ")))
	}

	attempts, err := q.judge(ctx, runs)
	if err != nil {
		return err
	}
	d := model.DecideReleaseQualification(items, attempts, model.DefaultQualificationPolicy)
	summary := strings.Join(d.Lines, "\n")
	if d.Conclusion != "" {
		return q.post("completed", d.Conclusion,
			fmt.Sprintf("Release qualification of %s: %s", rcTag, d.Conclusion), summary)
	}
	for _, item := range d.Rerun {
		if err := q.start(ctx, []string{item}); err != nil {
			return err
		}
	}
	if d.StartFinal {
		if err := q.start(ctx, items[len(items)-1:]); err != nil {
			return err
		}
	}
	if q.checkID == 0 {
		return q.post("in_progress", "", fmt.Sprintf("Qualifying %s", rcTag), summary)
	}
	return nil
}

// rcQualification is one candidate's Stage 2, for the length of one
// reconcile.
type rcQualification struct {
	store   *model.Store
	forge   ReleaseQualificationForge
	repo    model.RepoRef
	suite   model.Suite
	rcTag   string
	sha     string
	tag     string
	checkID int64
	now     time.Time
}

// start files items as one suite run of their own. The suite is run as
// the playbook requires whatever its declaration says -- until_clean,
// one pass, no auto-merge -- so a copy of it that drifted from
// .grain/suites/release-qualification.yml still cannot turn a pull
// request into a pass.
func (q *rcQualification) start(ctx context.Context, items []string) error {
	suite := q.suite
	suite.Items = make([]model.SuiteItem, 0, len(items))
	for _, item := range items {
		suite.Items = append(suite.Items, model.SuiteItem{TemplateID: item})
	}
	suite.Mode, suite.MaxPasses, suite.Count, suite.AutoMerge = model.SuiteUntilClean, 1, 0, false
	base, err := q.forge.DefaultBranch(q.repo.Owner, q.repo.Name)
	if err != nil || base == "" {
		base = "main"
	}
	_, err = q.store.CreateSuiteRunWith(ctx, suite, q.repo, base, model.SuiteRunOptions{
		Tag:      q.tag,
		Fields:   map[string]string{"rc_tag": q.rcTag, "rc_sha": q.sha},
		Priority: model.PriorityReleaseGate,
	}, q.now)
	if err != nil {
		return fmt.Errorf("starting %s for %s: %w", strings.Join(items, ", "), q.rcTag, err)
	}
	return nil
}

// judge reads every attempt of every item, oldest first.
func (q *rcQualification) judge(ctx context.Context, runs []model.SuiteRun) (map[string][]model.ItemVerdict, error) {
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	out := map[string][]model.ItemVerdict{}
	for _, run := range runs {
		for _, st := range run.Tasks {
			a, err := q.attempt(ctx, st)
			if err != nil {
				return nil, err
			}
			out[st.TemplateID] = append(out[st.TemplateID], model.JudgeQualificationAttempt(a, q.rcTag, q.sha))
		}
	}
	return out, nil
}

// attempt gathers what one task's verdict is read from. Only a task that
// has finished is asked about its closing note, its scratch branch and
// its children: a running task's are not its answer yet.
func (q *rcQualification) attempt(ctx context.Context, st model.SuiteTaskStatus) (model.QualificationAttempt, error) {
	a := model.QualificationAttempt{
		TaskID: st.TaskID, TemplateID: st.TemplateID, State: st.State,
		OpenedPullRequest: st.OpenedPullRequest, Proposed: st.Proposed,
	}
	switch st.State {
	case model.StateCompleted, model.StateAwaitingReply, model.StateAwaitingSubmit, model.StateFailed:
	default:
		return a, nil
	}
	runs, err := q.store.Runs(ctx, st.TaskID)
	if err != nil {
		return a, err
	}
	if len(runs) > 0 {
		a.LastOutcome = runs[len(runs)-1].Outcome
	}
	if st.State == model.StateFailed {
		return a, nil
	}
	comments, err := q.store.Comments(ctx, st.TaskID)
	if err != nil {
		return a, err
	}
	a.Note = closingNote(comments, st.TaskID)
	leaked, err := q.forge.BranchExists(q.repo.Owner, q.repo.Name, model.QualificationScratchBranch(st.TaskID))
	if err != nil {
		return a, fmt.Errorf("checking %s's scratch branch: %w", st.TaskID, err)
	}
	a.LeakedScratchRef = leaked
	if err := q.children(ctx, &a); err != nil {
		return a, err
	}
	return a, nil
}

// children sorts what a's task proposed into scratch fixtures and
// defects.
//
// Exercising the merge queue or base-branch triage takes pull requests,
// and a qualification task gets them the only way a task can: by
// proposing tasks whose branch is its own qualification/<task-id>
// scratch branch, so they merge there and never on the default branch.
// Those are the experiment, not a finding, and are not the follow-up
// SuiteTaskStatus.Proposed counts -- but every one of them has to have
// finished by the time the task does: one still proposed, queued or
// running, or failed and waiting to be dispatched again, is scratch
// state left behind (playbook §2, condition 3). Anything proposed on any other
// branch is a defect the run found, and fails the item whatever state it
// is in.
func (q *rcQualification) children(ctx context.Context, a *model.QualificationAttempt) error {
	if !a.Proposed {
		return nil
	}
	a.Proposed = false
	scratch := model.QualificationScratchBranch(a.TaskID)
	ids, err := q.store.TasksWithLinkTarget(ctx, model.LinkProposedBy, a.TaskID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		child, err := q.store.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if child == nil {
			continue
		}
		if child.Base != scratch && !strings.HasPrefix(child.Base, scratch+"/") {
			a.Proposed = true
			continue
		}
		state, err := q.store.State(ctx, id)
		if err != nil {
			return err
		}
		switch state {
		case model.StateProposed, model.StateQueued, model.StateRunning, model.StateFailed:
			a.OpenChildren = append(a.OpenChildren, id)
		}
	}
	return nil
}

// closingNote is the newest comment taskID's own runs wrote that carries
// a verdict footer, or failing that the newest one they wrote -- which
// then fails for having none.
//
// Only the task's own runs count. Anybody else in the thread -- a person,
// a scratch fixture or any api:task-admin task writing in with
// `grain task message` (ui.Client.PostTaskNote) -- could otherwise post
// a PASS footer after the run's own FAIL and have it read as the verdict.
func closingNote(comments []model.Comment, taskID string) string {
	var newest string
	found := false
	for i := len(comments) - 1; i >= 0; i-- {
		if !ownRunComment(comments[i], taskID) {
			continue
		}
		if strings.Contains(comments[i].Body, "Qualification-Verdict:") {
			return comments[i].Body
		}
		if !found {
			newest, found = comments[i].Body, true
		}
	}
	return newest
}

// ownRunComment reports whether c is something taskID's own run said:
// grain relaying it on the run's behalf (finish.go's relayComment), the
// way a closing note is stored.
func ownRunComment(c model.Comment, taskID string) bool {
	by := c.Author.OnBehalfOf
	return c.Author.Actor.Kind == model.PrincipalAutomation && by != nil &&
		by.Kind == model.PrincipalAgent && by.ID == taskID
}

// post writes the check run on rc_sha: updating the one already there,
// or creating it.
func (q *rcQualification) post(status, conclusion, title, summary string) error {
	run := github.CheckRunWrite{
		Name:       model.ReleaseQualificationCheckName,
		HeadSHA:    q.sha,
		Status:     status,
		Conclusion: conclusion,
		ExternalID: q.tag,
		Output:     &github.CheckRunOutput{Title: title, Summary: summary},
	}
	now := q.now.UTC()
	if status == "completed" {
		run.CompletedAt = &now
	} else {
		run.StartedAt = &now
	}
	if q.checkID != 0 {
		if err := q.forge.UpdateCheckRun(q.repo.Owner, q.repo.Name, q.checkID, run); err != nil {
			return fmt.Errorf("updating %s on %s: %w", model.ReleaseQualificationCheckName, q.rcTag, err)
		}
		return nil
	}
	id, err := q.forge.CreateCheckRun(q.repo.Owner, q.repo.Name, run)
	if err != nil {
		return fmt.Errorf("posting %s on %s: %w", model.ReleaseQualificationCheckName, q.rcTag, err)
	}
	q.checkID = id
	return nil
}
