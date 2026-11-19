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

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

// GateCheckSource is an ordinary scoped internal definition reader. It has no
// Start, Cancel, dispatch, provider wait, merge, or configuration capability.
type GateCheckSource interface {
	Workflows(context.Context, string, string) ([]ci.Workflow, string, error)
}
type shadowForgeReader interface {
	PullRequestReader
	GetBranchHead(string, string, string) (*github.BranchHead, error)
	ListCheckRuns(string, string, string) ([]github.CheckRun, error)
	ListPullRequestFiles(string, string, int) ([]string, error)
}

var errGateCheckDenied = errors.New("CI observation read denied")

func (c *Client) collectGateChecks(ctx context.Context, id string, now time.Time) (fence string, facts []gates.ChecksFacts, err error) {
	defer func() {
		if recover() != nil {
			fence = ""
			facts = nil
			err = fmt.Errorf("CI source panicked")
		}
	}()
	fence, facts, err = c.collectGateChecksScoped(ctx, id, now)
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	return fence, facts, err
}
func (c *Client) collectGateChecksScoped(ctx context.Context, id string, now time.Time) (string, []gates.ChecksFacts, error) {
	task, obs, fence, err := c.Store.GateChecksInput(ctx, id)
	if err != nil {
		return "", nil, err
	}
	subjects := model.CheckSubjects(*task)
	if len(subjects) > 8 {
		return "", nil, fmt.Errorf("change collection exceeds observation bound")
	}
	allows, _ := gateObservationScope(ctx)
	for _, subject := range subjects {
		if !allows("ci:read", subject.Repository) || requirePullRequestScopes(ctx, subject.Repository, APIScope("ci:read")) != nil {
			return "", nil, errGateCheckDenied
		}
	}
	out := make([]gates.ChecksFacts, 0, len(subjects))
	for _, subject := range subjects {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		f := gates.ChecksFacts{TaskID: id, Subject: subject, ObservedAt: now, ValidUntil: now.Add(time.Minute)}
		if subject.ChangeKind == "cl" {
			c.collectProviderChecks(ctx, *task, &f)
		} else {
			c.collectPRChecks(ctx, *task, obs, &f)
		}
		out = append(out, f)
	}
	return fence, out, nil
}

func (c *Client) collectProviderChecks(ctx context.Context, task model.Task, f *gates.ChecksFacts) {
	f.Mode = "provider"
	if task.ChangeTarget == nil {
		f.Gap = "CL change-target scope is unavailable."
		return
	}
	p, ok := change.Lookup(model.LinkChangelist)
	if !ok {
		f.Gap = "CL provider is unavailable."
		return
	}
	// Use the provider's read directly. change.Observe also updates global
	// credential health/notifications; those side effects belong to its collector.
	obs, err := p.Observe(ctx, task, f.Subject.ID)
	if err != nil {
		f.Sources = []gates.CheckSource{{Kind: "provider", Error: true}}
		f.Gap = "CL provider read failed."
		return
	}
	f.Head = strconv.Itoa(obs.Snapshot)
	if obs.Snapshot <= 0 || obs.Target != f.Subject.ID {
		f.Head = ""
		f.Gap = "CL identity or current snapshot is missing."
		return
	}
	f.PolicyVersion = gates.Digest(struct {
		Target   any
		Template string
	}{task.ChangeTarget, task.ReviewTemplateID})
	f.PolicyComplete = true
	s := gates.CheckSource{Kind: "provider", Head: f.Head, Version: gates.Digest(obs), Complete: true, Verdict: obs.Presubmit}
	// PresubmitPassed is the full snapshot verdict, not inferred from health or
	// the optional display check list. SubmitBlockers owns other prerequisites.
	f.Sources = []gates.CheckSource{s}
	f.Preconditions = change.SubmitBlockers(task, obs, true)
	f.LegacyHealth = string(obs.Health)
	// The core provider contract lacks an atomic expected-reviewed-snapshot
	// receipt. This observation cannot grant Submit or attest final admission.
	f.Preconditions = append(f.Preconditions, "Submit must revalidate the expected reviewed snapshot; provider review, reasons and Grain review remain separate")
}

func (c *Client) collectPRChecks(ctx context.Context, task model.Task, obs *model.Observation, f *gates.ChecksFacts) {
	reader, ok := c.Config.GitHubPullRequests.(shadowForgeReader)
	if !ok {
		f.Gap = "Scoped forge reader is unavailable."
		return
	}
	repo, err := model.ParseRepo(f.Subject.Repository)
	if err != nil {
		return
	}
	number, err := strconv.Atoi(f.Subject.ID)
	if err != nil {
		return
	}
	detail, err := reader.GetPullRequest(repo.StoredOwner(), repo.Name, number)
	if err != nil {
		f.Gap = "Forge head read failed or was denied."
		f.Sources = []gates.CheckSource{{Kind: "github", Error: !github.IsPermissionDenied(err)}}
		return
	}
	f.Head = detail.HeadSHA
	if f.Head == "" {
		f.Gap = "Forge has not reported a current head."
		return
	}
	if ctx.Err() != nil {
		return
	}
	base, err := reader.GetBranchHead(repo.StoredOwner(), repo.Name, detail.BaseRef)
	if err != nil || base == nil || base.SHA == "" {
		f.Gap = "Exact current base is unavailable."
		return
	}
	cfg, err := c.Store.EffectiveRepoConfig(ctx, repo)
	if err != nil {
		f.Gap = "Effective repository CI policy is unavailable."
		return
	}
	// Match the authoritative queue's repository mode. Default capabilities seed
	// new task grants; changing defaults is not a retroactive provider switch.
	mode := model.CIModeGitHub
	// Explicit repository source selection takes precedence over historical task grants.
	repoCfg, err := c.Store.GetRepoConfig(ctx, repo)
	if err != nil {
		return
	}
	if repoCfg != nil {
		mode = repoCfg.EffectiveCIMode()
	}
	explicitOff := repoCfg != nil && (repoCfg.CIMode == model.CIModeGitHub || repoCfg.CIMode == model.CIModeNone)
	if !explicitOff && mode == model.CIModeGitHub && grainci.GrantedExplicitly(task.Grants) {
		mode = model.CIModeGrain
	}
	policy := model.EffectiveMergePolicy{Head: f.Head, Mode: model.CIGateMode(mode), GrainFullSuite: true, GitHubAllChecks: true}
	storedRules, err := c.Store.MergeRulesOf(ctx, repo)
	if err != nil {
		f.Gap = "Accepted merge policy is unavailable."
		return
	}
	rules := storedRules
	if (repoCfg != nil && repoCfg.Declarations) || !storedRules.Empty() {
		rules, err = readShadowMergeRules(ctx, reader, repo, base.SHA)
		if err != nil {
			f.Gap = "Base-owned merge rules are unreadable, invalid or exceed observation bounds."
			return
		}
	}
	policyCurrent := true
	if repoCfg != nil && repoCfg.Declarations {
		policyCurrent = equalShadowRules(rules, storedRules)
		if !policyCurrent {
			f.Gap = "Current base-owned rules differ from the declaration collector's accepted policy."
		}
	} else if !rules.Empty() && storedRules.Empty() {
		f.Preconditions = append(f.Preconditions, "base-owned merge-rules file is inactive until accepted by the existing declaration collector")
	}
	rules = storedRules
	if !rules.Empty() {
		// Classification is read/pure: never persist a policy or queue comment.
		files, e := reader.ListPullRequestFiles(repo.StoredOwner(), repo.Name, number)
		if e != nil || len(files) >= github.PullRequestFilesCap {
			f.Gap = "Changed-file collection is incomplete; policy applicability is unknown."
			return
		}
		policy = rules.Effective(files, true, mode)
		policy.Head = f.Head
	}
	f.Mode = string(policy.Mode)
	if obs != nil {
		f.LegacyHealth = obs.MergeQueueState
		f.Preconditions = append(f.Preconditions, "legacy queue state: "+obs.MergeQueueState+"; reason: "+boundedQueueReason(obs.MergeQueueReason)+"; queue observations lack an exact head/base pin and remain advisory")
	}
	f.ExplicitNone = f.Mode == "none" && (mode == model.CIModeNone || !rules.Empty()) && len(policy.GrainJobs) == 0 && len(policy.GitHubChecks) == 0
	f.PolicyVersion = gates.Digest(struct {
		Config any
		Rules  any
		Policy any
		Base   string
		Grants any
	}{cfg, rules, policy, base.SHA, task.Grants})
	f.PolicyComplete = policyCurrent
	if detail.Mergeable == nil {
		f.Preconditions = append(f.Preconditions, "mergeability is unknown")
	} else if !*detail.Mergeable {
		f.Preconditions = append(f.Preconditions, "merge conflicts require repair")
	}
	if detail.MergeableState == "behind" {
		f.Preconditions = append(f.Preconditions, "branch is behind its base; legacy up-to-date/overlap policy decides")
	}
	if detail.State != "open" {
		f.Preconditions = append(f.Preconditions, "pull request is closed; CI is not an integration receipt")
	}
	f.Preconditions = append(f.Preconditions, "review, approval, lane ordering and GitHub merge-group state are not proved by CI; no shadow enqueue")
	f.Preconditions = append(f.Preconditions, "base-red repair eligibility is separate; base red alone is not a CI failure for this change")
	if c.Config.GateRequiredCheck != nil {
		if required := c.Config.GateRequiredCheck(repo); required != "" {
			if f.Mode == "none" {
				f.ExplicitNone = false
				f.Gap = "No-CI selection conflicts with the deployment-owned required check."
			}
			if !slices.Contains(policy.GitHubChecks, required) {
				policy.GitHubChecks = append(policy.GitHubChecks, required)
			}
		}
	}
	f.PolicyVersion = gates.Digest(struct {
		Version  string
		Required []string
	}{f.PolicyVersion, policy.GitHubChecks})
	if policy.Mode.GitHub() {
		known, err := c.Store.RecordedGateChecks(ctx, repo, f.Head)
		if err != nil {
			f.Sources = append(f.Sources, gates.CheckSource{Kind: "github", Head: f.Head, Error: true})
			f.Gap = "Recorded CI evidence is unavailable."
			return
		}
		f.RecordedChecksVersion = known.Version
		current, legacy := collectGitHubChecks(ctx, reader, repo, number, f, policy, known)
		f.Sources = append(f.Sources, current)
		f.LegacySources = append(f.LegacySources, legacy)
	}

	if policy.Mode.Grain() {
		s := c.collectGrainChecks(ctx, repo, detail, base.SHA, obs, policy)
		if !s.Complete {
			f.Gap += " Grain full-run, base-definition or recheck evidence is missing, stale or incomplete."
		}
		f.Sources = append(f.Sources, s)
		f.LegacySources = append(f.LegacySources, s)
		legacy := &f.LegacySources[len(f.LegacySources)-1]
		if obs != nil && obs.CI != nil && obs.CI.Commit == f.Head {
			legacy.LegacyAggregate = true
			legacy.Complete = true
			legacy.Verdict = obs.CI.Conclusion
		}
		legacy.Required = slices.Clone(policy.GrainJobs)
		legacy.All = policy.GrainFullSuite
		if s.Complete && !policy.GrainFullSuite && len(policy.GrainJobs) > 0 && legacy.Verdict == "failure" {
			passed := true
			for _, name := range policy.GrainJobs {
				found := false
				for _, a := range legacy.Attempts {
					if a.Identity == name || slices.Contains(a.Names, name) {
						found = true
						if a.Conclusion != "success" {
							passed = false
						}
					}
				}
				if !found {
					passed = false
				}
			}
			if passed {
				legacy.Verdict = "success"
			}
		}
		legacy.Version = gates.Digest(*legacy)
	}
	// Preserve the forge's narrow budget exception only as an explicit legacy
	// import, conditional on that same head's recorded Grain success.
	grainPassed := false
	for _, source := range f.LegacySources {
		if source.Kind == "grain" && source.Head == f.Head && source.Verdict == "success" {
			grainPassed = true
		}
	}
	if grainPassed {
		for i := range f.LegacySources {
			source := &f.LegacySources[i]
			if source.Kind == "github" {
				for j := range source.Attempts {
					if source.Attempts[j].LegacyBudget {
						source.Attempts[j].Conclusion = "success"
					}
				}
				source.Version = gates.Digest(*source)
			}
		}
	}
	c.collectMergeRecheck(ctx, repo, detail, base.SHA, task.ID, f)
	c.collectBaseCIContext(ctx, repo, detail.BaseRef, base.SHA, f)
	if known, stalled, deadline := forge.InspectPendingChecks(forge.Ref{Owner: repo.StoredOwner(), Repo: repo.Name, Number: number}, f.Head, policy.StallTimeout, f.ObservedAt); known {
		f.Preconditions = append(f.Preconditions, "legacy CI stall deadline: "+deadline.UTC().Format(time.RFC3339)+fmt.Sprintf("; reached: %t; escalation remains owned by the legacy collector", stalled))
		if deadline.After(f.ObservedAt) && deadline.Before(f.ValidUntil) {
			f.ValidUntil = deadline
		}
	}
	// Read the resource again after collecting. A push/base change during a read
	// is a stale collection, never a mixed-head accepted positive result.
	if ctx.Err() != nil {
		f.PolicyComplete = false
		return
	}
	latest, e := reader.GetPullRequest(repo.StoredOwner(), repo.Name, number)
	if ctx.Err() != nil {
		f.PolicyComplete = false
		return
	}
	tip, be := reader.GetBranchHead(repo.StoredOwner(), repo.Name, detail.BaseRef)
	if e != nil || be != nil || tip == nil || latest.HeadSHA != f.Head || latest.BaseRef != detail.BaseRef || tip.SHA != base.SHA {
		f.PolicyComplete = false
		f.Gap = "Head or base changed during CI collection."
	}
}

func (c *Client) collectGrainChecks(ctx context.Context, repo model.RepoRef, detail github.PullRequestDetail, base string, obs *model.Observation, p model.EffectiveMergePolicy) gates.CheckSource {
	s := gates.CheckSource{Kind: "grain", Head: detail.HeadSHA, Required: slices.Clone(p.GrainJobs), All: p.GrainFullSuite}
	if c.Config.GateCheckSource == nil {
		return s
	}
	definitions, commit, err := c.readGateDefinitions(ctx, repo, detail.BaseRef, base)
	if err != nil || commit != base {
		s.Error = err != nil
		return s
	}
	req := ci.Request{Repo: repo.String(), Ref: detail.HeadRef, Event: ci.EventPush}
	// The same base-owned event/branch planner as checkBaseGate; a branch cannot
	// remove inherited jobs. A base definition error stays unavailable here,
	// without blocking the authoritative repair path.
	for _, w := range definitions {
		if _, matches := workflows.WorkflowEvent(w, req.Normalize()); !matches {
			continue
		}
		for _, j := range w.Jobs {
			id := workflows.PlanID(w.Path, j.ID)
			if !slices.Contains(s.Required, id) {
				s.Required = append(s.Required, id)
			}
		}
	}
	if obs == nil || obs.CI == nil || obs.CI.Commit != detail.HeadSHA {
		s.Complete = true
		s.Version = gates.Digest(struct {
			Definitions any
			Base        string
		}{definitions, base})
		return s
	}
	status := obs.CI
	if c.Config.CI == nil {
		return s
	}
	run, ok := c.Config.CI.Get(status.RunID)
	if !ok || run.Commit != detail.HeadSHA || run.Repo != repo.String() || string(run.Conclusion) != status.Conclusion || run.JobsTruncated || len(run.Jobs) > 512 {
		return s
	}
	if status.RecheckOf != "" {
		first, found := c.Config.CI.Get(status.RecheckOf)
		if !found || first.Commit != detail.HeadSHA || first.Repo != repo.String() || first.JobsTruncated || len(first.Jobs) > 512 {
			return s
		}
		byID := map[string]ci.JobRun{}
		for _, j := range first.Jobs {
			byID[workflows.PlanID(j.WorkflowPath, j.JobID)] = j
		}
		for _, j := range run.Jobs {
			byID[workflows.PlanID(j.WorkflowPath, j.JobID)] = j
		}
		run.Jobs = nil
		for _, j := range byID {
			run.Jobs = append(run.Jobs, j)
		}
		slices.SortFunc(run.Jobs, func(a, b ci.JobRun) int {
			return strings.Compare(workflows.PlanID(a.WorkflowPath, a.JobID), workflows.PlanID(b.WorkflowPath, b.JobID))
		})
	}
	s.Complete = true
	s.Verdict = status.Conclusion
	if s.Verdict == "" {
		s.Verdict = "cancelled"
	} // pending, never a full-suite pass
	for _, j := range run.Jobs {
		state := "completed"
		if j.FinishedAt == nil {
			state = "running"
		}
		s.Attempts = append(s.Attempts, gates.CheckAttempt{Identity: workflows.PlanID(j.WorkflowPath, j.JobID), Names: []string{j.JobID, j.Name, j.Workflow, j.WorkflowPath, model.DeclarationStem(j.WorkflowPath)}, Head: detail.HeadSHA, Version: gates.Digest(j), Order: 1, State: state, Conclusion: string(j.Conclusion)})
	}
	s.Version = gates.Digest(struct {
		Source      any
		Definitions any
		Status      any
	}{s, definitions, status})
	return s
}

func (c *Client) collectMergeRecheck(ctx context.Context, repo model.RepoRef, d github.PullRequestDetail, base, id string, f *gates.ChecksFacts) {
	b, err := c.Store.GetMergeBatch(ctx, repo, d.BaseRef, id)
	if err != nil {
		f.Preconditions = append(f.Preconditions, "overlap/recheck source is unavailable")
		return
	}
	if b == nil {
		f.Preconditions = append(f.Preconditions, "overlap/recheck applicability has not been established by this observer")
		return
	}
	f.ReadinessVersion = gates.Digest(b)
	current := b.BaseCommit == base
	member := false
	for _, m := range b.Members {
		if m.TaskID == id && m.HeadSHA == d.HeadSHA {
			member = true
		}
	}
	if !current || !member {
		f.Preconditions = append(f.Preconditions, "overlap recheck is stale for current head/base")
		return
	}
	f.Preconditions = append(f.Preconditions, "overlap recheck revision "+gates.Digest(b)+"; current overlap recheck: "+b.Conclusion+" (executor prerequisite, not own-head CI)")
}

// readShadowMergeRules reads all accepted rule-file identities at the exact
// current base. A stale declaration cache is not current-policy evidence.
func readShadowMergeRules(ctx context.Context, reader shadowForgeReader, repo model.RepoRef, base string) (*model.MergeRules, error) {
	files, ok := reader.(github.FileReader)
	if !ok {
		return nil, fmt.Errorf("exact-base rules reader unavailable")
	}
	tree := shadowRulesFS{}
	for _, path := range model.MergeRulesPaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, found, err := files.ReadFile(repo.StoredOwner(), repo.Name, base, path)
		if err != nil {
			return nil, err
		}
		if len(body) > 256*1024 {
			return nil, fmt.Errorf("merge rules exceed bound")
		}
		if found {
			tree[path] = body
		}
	}
	rules, err := repodecl.LoadMergeRules(tree)
	if rules != nil {
		rules.Commit = base
	}
	return rules, err
}

type shadowRulesFS map[string][]byte

func (s shadowRulesFS) Open(path string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
}
func (s shadowRulesFS) ReadFile(path string) ([]byte, error) {
	if b, ok := s[path]; ok {
		return b, nil
	}
	return nil, &fs.PathError{Op: "read", Path: path, Err: fs.ErrNotExist}
}

func collectGitHubChecks(ctx context.Context, reader shadowForgeReader, repo model.RepoRef, number int, f *gates.ChecksFacts, p model.EffectiveMergePolicy, recorded model.RecordedGateChecks) (gates.CheckSource, gates.CheckSource) {
	s := gates.CheckSource{Kind: "github", Head: f.Head, Required: slices.Clone(p.GitHubChecks), All: p.GitHubAllChecks}
	legacy := s
	bounded, ok := reader.(github.CheckCollectionReader)
	if !ok {
		f.Gap = "Bounded complete Checks/Actions reader is unavailable."
		return s, legacy
	}
	collection, err := bounded.ReadCheckCollection(ctx, repo.StoredOwner(), repo.Name, f.Head, 512)
	s.Complete = collection.Complete
	legacy.Complete = collection.ChecksComplete
	// A complete API listing can lag a webhook/poll already committed locally.
	// Such disagreement is a source-state gap, never proof of CI success.
	byID := make(map[int64]github.CheckRun, len(collection.Checks))
	for _, check := range collection.Checks {
		byID[check.ID] = check
	}
	for _, check := range recorded.Checks {
		remote, found := byID[check.CheckRunID]
		conclusion := ""
		if remote.Conclusion != nil {
			conclusion = *remote.Conclusion
		}
		if !found || (check.App != "" && check.App != remote.App) ||
			(check.Name != "" && check.Name != remote.Name) ||
			(check.Conclusion != "" && check.Conclusion != conclusion) {
			s.Complete = false
			f.Gap = "Complete remote checks disagree with recorded exact-head CI evidence."
		}
	}
	if err != nil {
		s.Error = !github.IsPermissionDenied(err)
		f.Gap = "Checks or Actions collection failed or was denied; incomplete sources cannot pass."
	} else if collection.Gap != "" {
		f.Gap = collection.Gap
	}
	type suiteLineage struct {
		key          string
		run, attempt int64
	}
	lineages := map[int64]suiteLineage{}
	latestWorkflows := map[string]github.WorkflowRun{}
	suiteStarted := map[int64]*time.Time{}
	recordedByID := map[int64]model.RecordedGateCheck{}
	for _, check := range recorded.Checks {
		recordedByID[check.CheckRunID] = check
	}
	for _, run := range collection.Workflows {
		key := run.Path + "/" + run.Event + "/" + run.HeadBranch
		old, found := latestWorkflows[key]
		if !found || run.ID > old.ID || (run.ID == old.ID && run.RunAttempt > old.RunAttempt) {
			latestWorkflows[key] = run
		}
		if run.CheckSuiteID > 0 && run.Path != "" && run.Event != "" && run.HeadBranch != "" {
			lineage := suiteLineage{run.Path + "/" + run.Event + "/" + run.HeadBranch, run.ID, int64(run.RunAttempt)}
			if prior, ok := lineages[run.CheckSuiteID]; ok && prior != lineage {
				s.Complete = false
				f.Gap = "Workflow suite lineage is ambiguous."
			}
			lineages[run.CheckSuiteID] = lineage
			suiteStarted[run.CheckSuiteID] = run.StartedAt
		}
	}
	for _, check := range recorded.Checks {
		if check.RunID <= 0 && check.RunAttempt <= 0 {
			continue
		}
		remote, found := byID[check.CheckRunID]
		lineage, resolved := lineages[remote.CheckSuiteID]
		if !found || !resolved || (check.RunID > 0 && lineage.run != check.RunID) ||
			(check.RunAttempt > 0 && lineage.attempt < int64(check.RunAttempt)) {
			s.Complete = false
			f.Gap = "Remote workflow attempt disagrees with recorded exact-head CI evidence."
		}
	}
	type suiteCoverage struct {
		ids        map[int64]bool
		unresolved bool
	}
	suites := map[string]*suiteCoverage{}
	type generationOrder struct{ leaf, leafRun, run int64 }
	generationOrders := map[string]*generationOrder{}
	currentJobs := map[suiteLineage]bool{}
	currentIdentities := map[string]bool{}
	historicalUnknown := map[string]github.WorkflowRun{}
	for _, a := range collection.Checks {
		conclusion := ""
		if a.Conclusion != nil {
			conclusion = *a.Conclusion
		}
		legacyAttempt := gates.CheckAttempt{Identity: a.App + "/" + a.Name, Names: []string{a.Name, a.App + "/" + a.Name}, Head: a.HeadSHA, Version: gates.Digest(a), Order: a.ID, State: a.Status, Conclusion: conclusion, LegacyBudget: forge.IsActionsBudgetFailure(a)}
		legacy.Attempts = append(legacy.Attempts, legacyAttempt)
		attempt := legacyAttempt
		key := a.App + "/" + a.Name
		coverage := suites[key]
		if coverage == nil {
			coverage = &suiteCoverage{ids: map[int64]bool{}}
			suites[key] = coverage
		}
		coverage.ids[a.CheckSuiteID] = true
		lineage, known := lineages[a.CheckSuiteID]
		if a.App == "github-actions" && known {
			attempt.Identity = a.App + "/" + lineage.key + "/" + a.Name
			order := generationOrders[lineage.key]
			if order == nil {
				order = &generationOrder{}
				generationOrders[lineage.key] = order
			}
			if lineage.run > order.run {
				order.run = lineage.run
			}
			if a.ID > order.leaf {
				order.leaf = a.ID
				order.leafRun = lineage.run
			}
			attempt.Order, attempt.Attempt, attempt.LeafOrder = lineage.run, lineage.attempt, a.ID
			parent := latestWorkflows[lineage.key]
			if lineage.run != parent.ID {
				continue
			}
			// A suite can survive reruns. Its latest parent attempt is not
			// evidence that every historical leaf belongs to that attempt.
			if record, found := recordedByID[a.ID]; found && record.RunAttempt > 0 {
				start := suiteStarted[a.CheckSuiteID]
				if start != nil && a.StartedAt != nil &&
					((int64(record.RunAttempt) == lineage.attempt && a.StartedAt.Before(*start)) ||
						(int64(record.RunAttempt) < lineage.attempt && a.StartedAt.After(*start))) {
					s.Complete = false
					f.Gap = "Recorded job attempt and source start times disagree."
					continue
				}
				attempt.Attempt = int64(record.RunAttempt)
			} else if lineage.attempt > 1 {
				start := suiteStarted[a.CheckSuiteID]
				if start != nil && a.StartedAt != nil && a.StartedAt.Before(*start) {
					historicalUnknown[attempt.Identity] = parent
					continue
				}
				if start == nil || a.StartedAt == nil || !a.StartedAt.After(*start) {
					positive := parent.Status == "completed" && (parent.Conclusion == "success" || parent.Conclusion == "neutral" || parent.Conclusion == "skipped")
					if !s.All || positive {
						s.Complete = false
					}
					f.Gap = "Job membership in the latest workflow attempt is unproved."
					continue
				}
			}
			if attempt.Order != parent.ID || attempt.Attempt > int64(parent.RunAttempt) {
				continue
			}
			// A failed-job rerun retains unreplaced jobs of this same run.
			// Exact historical attempts remain candidates per job identity.
			if attempt.Attempt == int64(parent.RunAttempt) {
				currentJobs[suiteLineage{lineage.key, attempt.Order, attempt.Attempt}] = true
				currentIdentities[attempt.Identity] = true
			}
		} else {
			coverage.unresolved = true
			if a.App == "github-actions" {
				s.Complete = false
				f.Gap = "Actions job has no proved workflow lineage."
			}
		}

		if a.CheckSuiteID <= 0 {
			s.Complete = false
			f.Gap = "Check suite identity is missing."
		}
		s.Attempts = append(s.Attempts, attempt)
	}

	for identity, parent := range historicalUnknown {
		positive := parent.Status == "completed" && (parent.Conclusion == "success" || parent.Conclusion == "neutral" || parent.Conclusion == "skipped")
		if !currentIdentities[identity] && (!s.All || positive) {
			s.Complete = false
			f.Gap = "Unreplaced historical job lacks exact attempt proof."
		}
	}
	for key, run := range latestWorkflows {
		positive := run.Status == "completed" && (run.Conclusion == "success" || run.Conclusion == "neutral" || run.Conclusion == "skipped")
		if positive && !currentJobs[suiteLineage{key, run.ID, int64(run.RunAttempt)}] {
			s.Complete = false
			f.Gap = "Latest successful workflow has no proved current-attempt job checks."
		}
	}
	for _, order := range generationOrders {
		if order.leafRun < order.run {
			s.Complete = false
			f.Gap = "Check and workflow attempt ordering disagree; the current suite generation is unknown."
		}
	}
	for _, coverage := range suites {
		if len(coverage.ids) > 1 && coverage.unresolved {
			s.Complete = false
			f.Gap = "Same-named checks span suites without a proved replacement lineage."
		}
	}

	if !collection.ChecksComplete && collection.ActionsComplete && github.IsPermissionDenied(err) {
		legacy.Complete = true
		for _, a := range collection.Workflows {
			legacy.Attempts = append(legacy.Attempts, gates.CheckAttempt{Identity: fmt.Sprintf("legacy-actions/%d", a.ID), Names: []string{a.Name}, Head: a.HeadSHA, Version: gates.Digest(a), Order: int64(a.RunAttempt), State: a.Status, Conclusion: a.Conclusion})
		}
	}
	for _, a := range collection.Workflows {
		s.Attempts = append(s.Attempts, gates.CheckAttempt{Identity: "workflow/" + a.Path + "/" + a.Event + "/" + a.HeadBranch, Workflow: true, Head: a.HeadSHA, Version: gates.Digest(a), Order: a.ID, Attempt: int64(a.RunAttempt), State: a.Status, Conclusion: a.Conclusion})
	}
	known, settled, deadline := forge.InspectEmptyChecks(forge.Ref{Owner: repo.StoredOwner(), Repo: repo.Name, Number: number}, f.Head, f.ObservedAt)
	f.LegacyEmptyKnown = known
	f.LegacyEmptySettled = known && settled
	if len(collection.Checks) == 0 && !known && f.Gap == "" {
		f.Gap = "Legacy empty-check clock has no current-head sighting (including after restart); shadow reads do not start it."
	}
	if len(collection.Checks) == 0 && deadline.After(f.ObservedAt) && deadline.Before(f.ValidUntil) {
		f.ValidUntil = deadline
	}
	sortShadowAttempts(s.Attempts)
	sortShadowAttempts(legacy.Attempts)
	s.Version = gates.Digest(struct {
		Source   gates.CheckSource
		Recorded string
	}{s, recorded.Version})
	legacy.Version = gates.Digest(legacy)
	return s, legacy
}

type gateDefinitionCache struct {
	definitions []ci.Workflow
	commit      string
	until       time.Time
}

func (c *Client) readGateDefinitions(ctx context.Context, repo model.RepoRef, ref, base string) ([]ci.Workflow, string, error) {
	rows, err := c.Store.GetRepoWorkflows(ctx, repo)
	if err != nil {
		return nil, "", err
	}
	key := repo.String() + "/" + base + "/" + gates.Digest(rows)
	c.gateDefinitionMu.Lock()
	if c.gateDefinitionPermit == nil {
		c.gateDefinitionPermit = make(chan struct{}, 1)
	}
	permit := c.gateDefinitionPermit
	cached, ok := c.gateDefinitions[key]
	c.gateDefinitionMu.Unlock()
	if ok && c.now().Before(cached.until) {
		return cached.definitions, cached.commit, nil
	}
	// One definition read at a time per UI client; waiting respects the same
	// bounded collection context and never holds a legacy store/queue lock.
	select {
	case permit <- struct{}{}:
		defer func() { <-permit }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	c.gateDefinitionMu.Lock()
	cached, ok = c.gateDefinitions[key]
	c.gateDefinitionMu.Unlock()
	if ok && c.now().Before(cached.until) {
		return cached.definitions, cached.commit, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	definitions, commit, err := c.Config.GateCheckSource.Workflows(ctx, repo.String(), ref)
	if err != nil || commit != base {
		return definitions, commit, err
	}
	encoded, encodeErr := json.Marshal(definitions)
	if encodeErr != nil || len(encoded) > 1024*1024 {
		return nil, "", fmt.Errorf("workflow definition byte bound exceeded")
	}
	jobs := 0
	for _, w := range definitions {
		jobs += len(w.Jobs)
	}
	if len(definitions) > 512 || jobs > 512 {
		return nil, "", fmt.Errorf("workflow definition collection exceeds bound")
	}
	c.gateDefinitionMu.Lock()
	defer c.gateDefinitionMu.Unlock()
	now := c.now()
	if c.gateDefinitions == nil {
		c.gateDefinitions = map[string]gateDefinitionCache{}
	}
	for key, entry := range c.gateDefinitions {
		if !now.Before(entry.until) {
			delete(c.gateDefinitions, key)
		}
	}
	if len(c.gateDefinitions) >= 32 {
		for key := range c.gateDefinitions {
			delete(c.gateDefinitions, key)
			break
		}
	}
	c.gateDefinitions[key] = gateDefinitionCache{definitions, commit, now.Add(time.Minute)}
	return definitions, commit, nil
}

func equalShadowRules(a, b *model.MergeRules) bool {
	if a.Empty() && b.Empty() {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	aa, bb := *a, *b
	aa.Commit = ""
	bb.Commit = ""
	aa.Warnings = nil
	bb.Warnings = nil
	return gates.Digest(aa) == gates.Digest(bb)
}
func (c *Client) collectBaseCIContext(ctx context.Context, repo model.RepoRef, branch, base string, f *gates.ChecksFacts) {
	row, err := c.Store.GetBaseBranchCI(ctx, repo, branch)
	if err == nil {
		f.ReadinessVersion = gates.Digest(struct {
			Recheck string
			Base    any
		}{f.ReadinessVersion, row})
	}
	switch {
	case err != nil:
		f.Preconditions = append(f.Preconditions, "base CI context is unavailable; repair eligibility is unknown")
	case row == nil:
		f.Preconditions = append(f.Preconditions, "base CI has no recorded verdict; this is not positive base-health evidence")
	case row.Commit != base:
		f.Preconditions = append(f.Preconditions, "recorded base CI is for an older tip; current base-health is unknown")
	default:
		f.Preconditions = append(f.Preconditions, "current base CI: "+row.Conclusion+"; confirmation: "+string(row.Confirmation)+" (separate repair context, not this change's CI)")
	}
}

func sortShadowAttempts(attempts []gates.CheckAttempt) {
	slices.SortFunc(attempts, func(a, b gates.CheckAttempt) int {
		if d := strings.Compare(a.Identity, b.Identity); d != 0 {
			return d
		}
		if a.Order < b.Order {
			return -1
		}
		if a.Order > b.Order {
			return 1
		}
		if a.Attempt < b.Attempt {
			return -1
		}
		if a.Attempt > b.Attempt {
			return 1
		}
		if a.LeafOrder < b.LeafOrder {
			return -1
		}
		if a.LeafOrder > b.LeafOrder {
			return 1
		}
		return strings.Compare(a.Version, b.Version)
	})
}

func boundedQueueReason(reason string) string {
	if len(reason) > 256 {
		return "unavailable (oversized legacy queue reason)"
	}
	return reason
}
