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

package gateobserve

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/qa/resolve"
)

// These are deliberately read-only domain adapters. The observer cannot call
// any of the lifecycle, workflow dispatch, check writer or promotion methods.
type qaObservationHeads interface {
	GetBranchHead(owner, repo, branch string) (*github.BranchHead, error)
}
type releaseObservationChecks interface {
	ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error)
	CheckRunApp(owner, repo string) (string, error)
}

// CollectQualificationObservations is called only after the ordinary task read
// authorizes the subject, and only for the existing bounded observation allowlist.
// It neither assigns QA exposure nor starts/retries/judges qualification tasks.
func CollectQualificationObservations(ctx context.Context, store *model.Store, source any, id string, now time.Time) (gates.QualificationCollection, error) {
	return CollectQualificationObservationsWithCIRead(ctx, store, source, id, now, nil)
}

// CollectQualificationObservationsWithCIRead additionally checks the caller's
// ordinary CI read scope before reading qualification verdicts or readiness.
// A denied optional family is omitted, as for the checks observation family.
func CollectQualificationObservationsWithCIRead(ctx context.Context, store *model.Store, source any, id string, now time.Time, ciReadable func(string) bool) (gates.QualificationCollection, error) {
	task, err := store.GetTask(ctx, id)
	if err != nil {
		return gates.QualificationCollection{}, err
	}
	if task == nil {
		return gates.QualificationCollection{}, model.ErrNotFound
	}
	return CollectQualificationObservationsForTask(ctx, store, source, *task, now, ciReadable)
}

// CollectQualificationObservationsForTask collects qualification observations
// using an already-fetched task row from the current domain state, avoiding a
// redundant GetTask hydration on steady-state reads.
func CollectQualificationObservationsForTask(ctx context.Context, store *model.Store, source any, task model.Task, now time.Time, ciReadable func(string) bool) (gates.QualificationCollection, error) {
	out := gates.QualificationCollection{}
	id := task.ID
	repoScope := ""
	if task.Target != nil {
		repoScope = task.Target.String()
	}
	if model.HasExternalRefReadable(ctx) && !model.ExternalRefReadable(ctx, repoScope) {
		return out, model.ErrGateObservationScopeDenied
	}
	if task.Target == nil {
		return out, nil
	}
	qaActive := false
	if model.TaskEligibleForQAStaging(task) {
		res, cfg, qaErr := resolve.ActiveQAResolutionForTask(ctx, store, id)
		qaActive = qaErr != nil || cfg.Enabled || res != nil
	}
	checkRelease := ciReadable == nil || ciReadable(repoScope)
	hasSuiteRun := false
	if checkRelease {
		suiteOrigin, err := store.SuiteRunOfTask(ctx, id)
		if err != nil {
			return out, err
		}
		hasSuiteRun = suiteOrigin != nil
	}
	if !qaActive && !hasSuiteRun {
		return out, nil
	}
	var err error
	out.StoreFence, err = store.QualificationObservationFence(ctx, id)
	if err != nil {
		return out, err
	}
	if qaActive {
		res, cfg, err := resolve.ActiveQAResolutionForTask(ctx, store, id)
		if err != nil || cfg.Enabled || res != nil {
			out.QA = &gates.StageFacts{TaskID: id, ObservedAt: now, ValidUntil: now.Add(time.Minute), Reason: "QA assignment or flow evidence is unavailable."}
			if err == nil {
				collectQAObservation(ctx, store, source, task, res, cfg, out.QA)
			}
		}
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if checkRelease && hasSuiteRun {
		release, readErr := collectReleaseObservation(ctx, store, source, task, now)
		out.Release = release
		if readErr != nil {
			return out, readErr
		}
	}
	if out.QA == nil && out.Release == nil {
		return out, nil
	}
	fence, err := store.QualificationObservationFence(ctx, id)
	if err != nil {
		return out, err
	}
	if fence != out.StoreFence {
		out = out.Invalidate("Task, QA assignment or qualification policy changed during collection.")
	}
	return out, nil
}

func qaAssignmentVersion(ctx context.Context, store *model.Store, task model.Task, res *resolve.Resolution, cfg model.QAConfig) (string, error) {
	var assignment any
	root := model.ResolveQARootOrigin(task.WorkOrigin, task.ID)
	if res != nil && res.Source == "registry" {
		enrollment, err := store.GetQAEnrollment(ctx, root)
		if err != nil {
			return "", err
		}
		if enrollment == nil || enrollment.QAEngineRev <= 0 || enrollment.QAEngineRev != res.QAEngineRev {
			return "", nil
		}
		exp, err := store.GetQAExperiment(ctx, enrollment.ExperimentID)
		if err != nil {
			return "", err
		}
		assignment = struct {
			Enrollment *model.QAEnrollment
			Experiment model.QAExperiment
		}{enrollment, exp}
	} else if res != nil && res.Source == "legacy" {
		legacy, err := store.EarliestQAAssignment(ctx, root)
		if err != nil {
			return "", err
		}
		if legacy == nil {
			return "", nil
		}
		assignment = legacy
	} else {
		assignment = struct {
			Flow       string
			Resolution *resolve.Resolution
		}{task.QAFlowRef, res}
	}
	return gates.Digest(struct {
		Task       string
		Config     model.QAConfig
		Assignment any
		Resolution *resolve.Resolution
	}{task.ID, cfg, assignment, res}), nil
}

func observationQAHead(source any, task model.Task) (string, error) {
	reader, ok := source.(qaObservationHeads)
	if !ok {
		return "", nil
	}
	head, err := reader.GetBranchHead(task.Target.StoredOwner(), task.Target.Name, model.QAStageBranchName(task.ID))
	if err != nil {
		return "", err
	}
	if head == nil {
		head, err = reader.GetBranchHead(task.Target.StoredOwner(), task.Target.Name, task.BranchName())
	}
	if err != nil {
		return "", err
	}
	if head == nil || !observationCommitPattern.MatchString(head.SHA) {
		return "", nil
	}
	return head.SHA, nil
}

func collectQAObservation(ctx context.Context, store *model.Store, source any, task model.Task, res *resolve.Resolution, cfg model.QAConfig, f *gates.StageFacts) {
	if res == nil || res.Spec == nil {
		return
	}
	f.FlowRevision, f.Arm, f.Experiment, f.Engine, f.Shadow = res.FlowHash, res.ArmID, res.ExperimentID, res.QAEngineRev, res.Shadow
	f.PolicyVersion = gates.Digest(struct {
		Spec   *qa.FlowSpec
		Engine int
	}{res.Spec, res.QAEngineRev})
	var err error
	f.AssignmentVersion, err = qaAssignmentVersion(ctx, store, task, res, cfg)
	if err != nil {
		return
	}
	f.Supported = f.FlowRevision != "" && f.AssignmentVersion != "" && res.Spec.ID != model.QAFlowRepoCustom
	if !f.Supported {
		f.Reason = "Missing immutable flow/assignment/engine pin, or unsupported repo-custom overlay."
		return
	}
	if res.Source == "process" {
		repoPolicy, readErr := store.RepoConfig(ctx, *task.Target)
		if readErr != nil {
			return
		}
		if repoPolicy != nil && repoPolicy.QAFlowRef != "" {
			f.Supported = false
			f.Reason = "QA records do not retain the composed task/repository overlay policy pin."
			return
		}
	}
	spec := *res.Spec
	for _, stage := range spec.CustomStages {
		kind := strings.ToLower(strings.TrimSpace(stage.Kind))
		if !slices.Contains([]string{"", "deterministic_batch", "isolated_sandbox_check", "isolated_critic", "human_gate"}, kind) {
			f.Supported = false
			f.Reason = "Custom stage kind has no supported typed observation adapter."
			return
		}
	}
	// Use the resolved runtime flow, including the integrity floors applied by
	// the ordinary resolver. Custom checks are observed, never reexecuted.
	for _, rule := range spec.Rules {
		stage := rule.Stage
		if stage == "" {
			stage = "S3"
		}
		if rule.Enabled && (strings.EqualFold(stage, "S3") || strings.EqualFold(stage, "S3.5")) {
			f.Checks = append(f.Checks, gates.StageCheck{Stage: stage, ID: rule.ID, Severity: rule.Severity})
		}
	}
	f.S5Shadow = spec.S5MergeTriage.Mode == string(qa.TriageShadow)
	f.S5Enabled = spec.S5MergeTriage.Mode != "" && spec.S5MergeTriage.Mode != string(qa.TriageOff)
	f.RepairDescription = fmt.Sprintf("Legacy S4 repair cap: %d; attempted rounds: %d; bounded S3 salvage already used: %t.", maxS4RepairRounds(spec), countTaskLinks(task, model.LinkQAS4Attempted), hasTaskLink(task, model.LinkQASalvageRetried))
	f.Head, err = observationQAHead(source, task)
	if err != nil || f.Head == "" {
		f.Reason = "Current staged/task ref head is missing, denied or unavailable."
		return
	}
	if ctx.Err() != nil {
		return
	}
	runs, findings, complete, err := store.QAGateEvidence(ctx, task.ID, f.Head)
	if err != nil {
		return
	}
	f.Reason = ""
	f.Complete = complete
	if !complete {
		f.Reason = "QA record/finding collection exceeds the 512-record or bounded-detail limit."
	}
	byRun := map[int64][]gates.StageFinding{}
	for _, finding := range findings {
		byRun[finding.CheckRunID] = append(byRun[finding.CheckRunID], gates.StageFinding{ID: strconv.FormatInt(finding.ID, 10), Version: gates.Digest(finding), Severity: finding.Severity, Summary: finding.Summary})
	}
	tier := ""
	var changedFiles *[]string
	applicabilityVersion := ""
	tierVersion := ""

	for _, run := range runs {
		arm := run.ArmID
		if arm == "" {
			arm = run.Arm
		}
		f.Runs = append(f.Runs, gates.StageRun{ID: strconv.FormatInt(run.ID, 10), Stage: run.Stage, CheckID: run.CheckID, Head: run.CommitSHA, FlowRevision: run.FlowHash, Arm: arm, Experiment: run.ExperimentID, Engine: run.QAEngineRev, Shadow: run.Shadow, Verdict: run.Verdict, TriageDecision: run.TriageDecision, Version: gates.Digest(struct {
			Run      model.QACheckRun
			Findings []gates.StageFinding
		}{run, byRun[run.ID]}), Findings: byRun[run.ID]})
		if run.FlowHash == f.FlowRevision && arm == f.Arm && run.ExperimentID == f.Experiment && run.QAEngineRev == f.Engine && run.Shadow == f.Shadow && (strings.EqualFold(run.Stage, "S3") || strings.EqualFold(run.Stage, "S3.5")) {

			t := run.BlastTier
			var meta struct {
				BlastTier    string
				ChangedFiles *[]string
			}
			if json.Unmarshal([]byte(run.DetailJSON), &meta) == nil {
				if t == "" {
					t = meta.BlastTier
				}
				if meta.ChangedFiles != nil {
					changedFiles = meta.ChangedFiles
					applicabilityVersion = gates.Digest(run)
				}
			}
			if t != "" {
				tier = t
				tierVersion = gates.Digest(run)
			}

		}
	}

	staged := map[string]bool{}
	for _, stage := range spec.CustomStages {
		for _, id := range stage.CheckIDs {
			staged[id] = true
		}
	}
	for _, check := range spec.CustomChecks {
		if !staged[check.ID] {
			f.Checks = append(f.Checks, gates.StageCheck{Stage: "S3", ID: check.ID, Severity: check.Severity})
		}
	}
	for _, stage := range spec.CustomStages {
		phase := "s3"
		if strings.EqualFold(strings.TrimSpace(stage.InsertAfter), "s4") {
			phase = "s4"
		}
		conditional := len(stage.WhenPaths) > 0 || stage.MinTier != ""
		exemption := false
		proof := ""
		tierKnown := stage.MinTier == "" || tier == "T0" || tier == "T1" || tier == "T2" || tier == "T3"
		pathsKnown := len(stage.WhenPaths) == 0 || changedFiles != nil
		if tierKnown && pathsKnown {
			paths := []string{}
			if changedFiles != nil {
				paths = *changedFiles
			}
			exemption = !qa.BlastTierAtLeast(qa.BlastTier(tier), qa.BlastTier(stage.MinTier)) || (len(stage.WhenPaths) > 0 && !qa.MatchesAnyGlob(stage.WhenPaths, paths))
			if exemption {
				proof = applicabilityVersion
				if len(stage.WhenPaths) == 0 {
					proof = tierVersion
				}
			}
		}
		f.Checks = append(f.Checks, gates.StageCheck{Phase: phase, Stage: stage.ID, ID: stage.ID, Severity: "blocking", Conditional: conditional, Exempt: exemption, ApplicabilityVersion: proof})
		kind := strings.ToLower(strings.TrimSpace(stage.Kind))
		if !exemption && (kind == "deterministic_batch" || kind == "isolated_sandbox_check" || kind == "") {
			for _, id := range stage.CheckIDs {
				f.Checks = append(f.Checks, gates.StageCheck{Phase: phase, Stage: "S3", ID: id, Severity: "blocking", Conditional: conditional})
			}
		}
	}
	if !spec.EnableS4 {
		required := false
		f.S4Required = &required
	} else if tier == "T0" || tier == "T1" || tier == "T2" || tier == "T3" {
		required := spec.RequiresS4ForTier(qa.BlastTier(tier))
		f.S4Required = &required
	}
	if !task.AutoMerge {
		f.HumanFloors = append(f.HumanFloors, "Operator auto-merge is not enabled; existing person floor remains authoritative.")
	}
	for _, reason := range model.HumanReviewReasons(task) {
		f.HumanFloors = append(f.HumanFloors, string(reason.Source)+": "+reason.Reason)
	}
	for _, link := range task.Links {
		switch link.Kind {
		case model.LinkQAHold, model.LinkReviewFindings, model.LinkSecurityHold, model.LinkHumanOnlyHold:
			f.HumanFloors = append(f.HumanFloors, "Existing "+string(link.Kind)+" floor remains authoritative.")
		}
	}
	// Optimistic fence over ordinary reads: never combine a former assignment
	// with a new head/flow. This diagnostic does not claim action admission.
	again, cfg2, err := resolve.ActiveQAResolutionForTask(ctx, store, task.ID)
	if err != nil {
		f.Complete = false
		f.Reason = "QA assignment refresh unavailable."
		return
	}
	assignment2, err := qaAssignmentVersion(ctx, store, task, again, cfg2)
	if err != nil || assignment2 != f.AssignmentVersion {
		f.Complete = false
		f.Reason = "QA assignment/flow changed during evidence collection."
		return
	}
	head2, err := observationQAHead(source, task)
	if err != nil || head2 != f.Head {
		f.Complete = false
		f.Reason = "QA ref head changed or became unavailable during evidence collection."
	}
}

func collectReleaseObservation(ctx context.Context, store *model.Store, source any, task model.Task, now time.Time) (*gates.ReleaseFacts, error) {
	origin, err := store.SuiteRunOfTask(ctx, task.ID)
	if err != nil || origin == nil {
		return nil, err
	}
	run, err := store.SuiteRunHeader(ctx, origin.RunID)
	if err != nil || run == nil {
		return nil, err
	}
	repo, stem, declared := repoDeclaredSuite(run.SuiteID)
	if !declared || !repo.Same(*task.Target) {
		return nil, nil
	}
	cfg, err := store.SuiteQualification(ctx, run.SuiteID)
	if err != nil {
		return nil, err
	}
	if cfg == nil && stem != model.ReleaseQualificationSuiteStem {
		return nil, nil
	}
	if cfg == nil {
		cfg = &model.SuiteQualification{}
	}
	policy := cfg.WithDefaults()
	f := &gates.ReleaseFacts{TaskID: task.ID, Repository: repo.String(), PolicyVersion: gates.Digest(struct {
		Suite  string
		Config model.SuiteQualification
	}{run.SuiteID, policy}), ObservedAt: now, ValidUntil: now.Add(time.Minute)}
	var tagList []github.Tag
	tagsComplete := false
	tagVersion := ""
	prefix := model.QualificationTag(stem, repo, "")
	candidate, ok := strings.CutPrefix(run.ScheduleID, prefix)
	if !ok {
		f.Reason = "Suite membership has no qualification candidate provenance."
		return f, nil
	}
	if pos := strings.LastIndex(candidate, "#"); pos >= 0 {
		candidate = candidate[:pos]
	}
	f.Candidate = candidate
	if policy.Candidate == model.CandidateSyncedRelease {
		version, head, ok := strings.Cut(candidate, "@")
		if !ok {
			f.Reason = "Synced-release run lacks exact release@commit provenance."
			return f, nil
		}
		f.Version, f.Head = version, head
	} else {
		rc, ok := model.ParseRCTag(candidate)
		if !ok {
			f.Reason = "Qualification run does not name an RC."
			return f, nil
		}
		f.Version = rc.Version()
		tags, ok := source.(github.TagLister)
		if !ok {
			f.Reason = "Complete RC tag source unavailable."
			return f, nil
		}
		list, err := tags.ListTags(repo.StoredOwner(), repo.Name)
		if err != nil {
			f.Reason = "RC tag collection denied or unavailable."
			return f, nil
		}
		if len(list) > 2048 {
			f.Reason = "RC tag collection exceeds the 2048-tag observation limit."
			return f, nil
		}
		tagList = list
		tagsComplete = true
		tagVersion = gates.Digest(list)
		for _, tag := range list {
			if tag.Name == candidate {
				f.Head = tag.SHA
			}
		}
		// Expose known provenance conflicts; a matching tag alone does not
		// establish the ancestry proof required to promote/publish.
		for _, tag := range list {
			if tag.Name == candidate+"-discarded" {
				bad := false
				f.Provenance = &bad
			}
		}
		f.SourceVersion = gates.Digest(list)
	}
	if !observationCommitPattern.MatchString(f.Head) {
		f.Reason = "Exact RC head is missing or malformed."
		return f, nil
	}
	if ctx.Err() != nil {
		return f, ctx.Err()
	}

	f.Complete = true
	f.PredicateReasons = map[string]string{
		"suite":                 "Authenticated qualification verdict source is unavailable.",
		"provenance":            "Promotion ancestry and immutable invocation-head proof are unavailable.",
		"soak":                  "Soak-era effective workflow/environment minimum is not retained; a missing minimum is not a pass.",
		"person":                "No readable verified signed person decision for this head/version; dispatch history is not authorization.",
		"breaking_change":       "Complete commit range and exact release policy are unavailable.",
		"signing_version":       "Verified artifact/approval signature, digest and exact version evidence are unavailable; tag prose is not a signature.",
		"publication_readiness": "Complete required asset/registry target evidence is unavailable; publication health is owned by the health family.",
		"promotion_policy":      "Exact effective release veto/version/scheduling policy is unavailable.",
	}
	readReleaseReadiness(ctx, source, repo, tagList, tagsComplete, f)
	var list []github.CheckRun
	if checks, ok := source.(releaseObservationChecks); ok && ctx.Err() == nil {
		app, err := checks.CheckRunApp(repo.StoredOwner(), repo.Name)
		if err == nil && app != "" {
			f.PolicyVersion = gates.Digest([]string{f.PolicyVersion, "issuer:" + app})
			checksList, readErr := checks.ListCheckRuns(repo.StoredOwner(), repo.Name, f.Head)
			if readErr == nil && len(checksList) <= 512 {
				// Only the configured qualification scope versions this observer;
				// unrelated nightly/publication changes belong to health.
				for _, check := range checksList {
					if check.Name == policy.Check {
						list = append(list, check)
					}
				}
				base := model.QualificationTag(stem, repo, candidate)
				attempt, check := latestAttemptCheckRun(list, policy.Check, app, base)
				if check != nil {
					conclusion := ""
					if check.Conclusion != nil {
						conclusion = *check.Conclusion
					}
					f.Suite = &gates.ReleaseSuite{ID: strconv.FormatInt(check.ID, 10), Head: check.HeadSHA, App: check.App, ExpectedApp: app, ExternalID: check.ExternalID, ExpectedExternalID: model.QualificationAttemptTag(base, attempt), Status: check.Status, Conclusion: conclusion, Version: gates.Digest(check), Complete: true}
				} else {
					f.PredicateReasons["suite"] = "Complete collection has no authenticated qualification verdict for this candidate attempt."
				}
			} else {
				f.PredicateReasons["suite"] = "Qualification check collection is denied, incomplete or exceeds the 512-record limit."
			}
		} else {
			f.PredicateReasons["suite"] = "Qualification issuer provenance is missing or denied; Stage 1 alone belongs to the health family."
		}
	}
	// This decision row proves only dispatch/deduplication. It does not
	// retain a signature or the short-soak/breaking answers, and must not
	// be converted into a positive human authorization receipt.
	decision, err := store.GetReleaseApprovalDecision(ctx, repo, f.Version, f.Head)
	if err != nil {
		return f, err
	}
	if decision != nil && (decision.Failed() || decision.DecisionVerb() == "reject") {
		f.Person = &gates.ReleasePerson{Identity: decision.DecidedBy, Kind: "human", Decision: decision.Decision, Commit: decision.Commit, Version: decision.Version, IssuedAt: decision.DecidedAt, ExpiresAt: decision.DecidedAt.Add(model.ReleaseApprovalHold)}
	}
	if tagVersion != "" && ctx.Err() == nil {
		tags := source.(github.TagLister)
		current, refreshErr := tags.ListTags(repo.StoredOwner(), repo.Name)
		if refreshErr != nil || len(current) > 2048 || gates.Digest(current) != tagVersion {
			f.Complete = false
			f.Reason = "RC tag/head collection changed or became unavailable during qualification collection."
		}
	}
	f.SourceVersion = gates.Digest(struct {
		Tags     string
		Checks   []github.CheckRun
		Decision *model.ReleaseApprovalDecision
	}{f.SourceVersion, list, decision})
	return f, nil
}
