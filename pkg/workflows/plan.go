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

package workflows

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Request is what a CI run is asked for: one repository, one branch,
// one event. Commit is optional on the way in -- a caller that already
// knows which commit the branch pointed at when it asked should say so,
// because that is what makes a result mean something later (and what a
// cache is keyed by); a caller that does not leaves it empty and the
// Source fills it in as it resolves the branch.
type Request struct {
	// Capabilities is filled by Service from trusted repo settings, never caller input.
	Capabilities []string `json:"-"`
	// TrustedRelease is set only by Grain's merge reconciler or its dedicated
	// operator-facing release endpoint. It is deliberately absent from JSON so
	// the generic CI API cannot turn an arbitrary run into a release.
	TrustedRelease bool `json:"-"`
	// TrustedSchedule is set only by grain's own scheduled-workflow
	// starter (orchestrator.startScheduledWorkflowRun), for the run a
	// workflow's `on: schedule` cron fired. A caller's `-event schedule`
	// can never carry it -- it is absent from JSON for TrustedRelease's
	// reason -- so a capability that accepts a trusted schedule
	// (model.TrustedScheduleCapability) is still refused to anybody who
	// merely asks for a schedule-shaped run.
	TrustedSchedule bool `json:"-"`
	// ReleaseSecrets is filled from trusted repository settings by Service,
	// never caller input. It carries names only; values are resolved directly
	// into each eligible job sandbox.
	ReleaseSecrets []string `json:"-"`
	// WithholdReleaseSecrets keeps Service from attaching the repository's
	// release secrets to this run even though its event would otherwise
	// carry them. The guest-cache warmer sets it: a warm run is a
	// workflow_dispatch against whatever branch first needed the guest --
	// an untrusted task branch as often as not -- and filling a build
	// cache never needs a signing key or a publish token. Set only by
	// grain's own callers, never from JSON.
	WithholdReleaseSecrets bool `json:"-"`
	// WithholdReason is the clause a job that needed a withheld release
	// secret is told, finishing "and ..." -- empty for the guest-cache
	// warmer, which has a sentence of its own. pkg/ui sets it for a by-hand
	// manual run from somebody who is not one of the repository's admins.
	WithholdReason string `json:"-"`
	// ReleaseRefVouched exempts a TrustedRelease request from
	// Service.ReleaseRefRefusal: a person -- the deployment's owner --
	// starting a release of a ref by hand, which is how a deployment
	// republishes an arbitrary branch on purpose. Set only by grain's own
	// callers, never from JSON.
	ReleaseRefVouched bool `json:"-"`
	// untrustedCommit is set by Service.Start, never by a caller, when
	// Commit is neither the tip Ref resolved to nor on Ref's history:
	// a tree nothing vouches for, which is handed no capabilities and
	// no release secrets. It holds the sentence a job that wanted one is
	// told.
	untrustedCommit string
	// LifecycleImage is the `image:` from the repository's
	// .grain/lifecycle.yml, handed to every job in a repository that
	// declares a lifecycle as GRAIN_LIFECYCLE_IMAGE.
	//
	// Filled by Service from the declaration grain read, never caller
	// input and never from a workflow file: it is the one namespace a
	// release job here may publish into, and a job that could name its
	// own would be a job that could publish another repository's
	// lifecycle image. It is also the only new thing the lifecycle build
	// needs from grain -- everything else about publishing one is an
	// ordinary release job (docs/task-lifecycle-sandbox.md, "The build is
	// a release job").
	LifecycleImage string `json:"-"`
	// DeployedImages are the grain images this deployment is running --
	// the build's own stamped image and the one its config repository's
	// check pulls, when an operator has set that -- handed to a release
	// job as GRAIN_DEPLOYED_IMAGES (DeployedImagesEnv).
	//
	// A release job is what prunes the registry (cmd/ghcr-prune), and a
	// deployment's own build is a sha- tag the registry cannot tell from
	// any other superseded build: once its branch tag has moved on it is
	// held by the age window alone, and pruning it fails every check of
	// grain/config the moment the window closes (grain:sha-adea62d,
	// grain:sha-7781f61). Filled by Service, never from JSON, and only
	// for a release event, since it is part of every job's fingerprint
	// and would otherwise invalidate the whole cache on each upgrade.
	DeployedImages []string `json:"-"`
	// SandboxImage is the container or VM guest image this deployment runs
	// sandbox jobs in right now -- the stock guest or k8s-sandbox image, or
	// the custom image configured in Settings. Filled by Service from the
	// deployment's live sandbox configuration, never from JSON, and folded
	// into a sandbox job's fingerprint in its digest-canonical form so a
	// daemon upgrade that leaves the sandbox image unchanged keeps the job
	// cache intact.
	SandboxImage string `json:"-"`
	// Priority is the least this run's jobs rank at the deployment's job
	// gate (GateRequest.Priority), whatever Engine.Priority says: grain's
	// own trunk release-gate runs ask for PriorityReleaseGate
	// (orchestrator.releaseGatePriority). Set only by grain's own callers,
	// never from JSON, for TrustedRelease's reason: a rank at or above
	// PriorityReleaseGate spends the repair reserve, and a caller that
	// could name one could take it.
	Priority Priority `json:"-"`
	// Repo is "owner/name", the same spelling model.Task.Repo uses.
	Repo string
	// Ref is the branch, with or without a refs/heads/ prefix.
	Ref string
	// Commit is the full SHA the run is against.
	Commit string
	// Event decides which workflows answer. Empty means EventManual --
	// somebody asked by hand, which is what an API call with no event of
	// its own is -- for choosing workflows only: see eventDefaulted.
	Event Event
	// eventDefaulted records that the caller left Event empty and
	// Normalize filled EventManual in. Such a run answers like a manual
	// dispatch but is never handed release secrets (Service.Start): the
	// absent case is the least-privileged one, and an incomplete caller --
	// an internal one that forgot the field, an API client that left it
	// out -- must not inherit the release secret set by default. Asking
	// for them takes an explicit EventManual or EventRelease.
	eventDefaulted bool
	// RequestedBy is recorded on the Run; see Run.RequestedBy.
	RequestedBy string
	// RequestedByKind is the kind of principal RequestedBy names
	// ("human", "agent"), handed to every step as
	// GRAIN_CI_REQUESTED_BY_KIND (with RequestedBy as
	// GRAIN_CI_REQUESTED_BY) when it is set. A workflow only a person may
	// run -- .grain/workflows/promote-release.yml, which makes a release
	// official -- refuses to start without "human" there. Set only by
	// grain's own callers that have established who is asking
	// (pkg/ui's release approval), never from JSON: a caller that could
	// name its own kind could name itself a person.
	RequestedByKind string `json:"-"`
	// ReleaseVersion is the release a person-only release workflow acts
	// on (vX.Y.0), handed to every step as GRAIN_CI_RELEASE_VERSION when
	// set. Set only by grain's own callers, never from JSON, for
	// RequestedByKind's reason.
	ReleaseVersion string `json:"-"`
	// GateOnBase asks for this run to be refused when its head would run
	// fewer jobs than BaseRef's workflows require for the same branch and
	// event (basegate.go). BaseRef is the branch the change is going
	// into, "" meaning the repository's default branch. Set by grain's
	// own callers for a verdict the merge queue reads, never from JSON: a
	// caller that could clear it could switch its own gate off again.
	GateOnBase bool   `json:"-"`
	BaseRef    string `json:"-"`
	// BaseCommit is the commit SHA of the base branch this run was planned
	// against, used for base-transition cache invalidation.
	BaseCommit string `json:"baseCommit,omitempty"`
	// Workflow optionally narrows the run to one workflow path. It is used
	// by an operator choosing a workflow for a manual dispatch; empty keeps
	// the event-driven behaviour of running every matching workflow.
	Workflow string
	// Jobs optionally narrows the run to named jobs. A bare job id is
	// accepted when it is unique across the matching workflows; the
	// plan-wide "<workflow path>:<job id>" spelling resolves ambiguity.
	// Dependencies of a selected job are included automatically. Empty
	// means the complete matching workflow set and is the only form that
	// may serve as a required task verdict.
	Jobs []string
	// JobFilter optionally narrows the run to a single named job.
	// When set (e.g. "go" or "lint"), it is equivalent to narrowing Jobs
	// to that job.
	JobFilter string
	// Recheck asks for work rather than for an answer: no job of this run
	// is served from the cache. Every other run is content-addressed --
	// the same commit asked about twice is one answer (jobFingerprint) --
	// which is exactly what a caller wanting an *independent sample* of a
	// job must be able to opt out of. The merge queue's confirmation of a
	// red base branch (orchestrator.startBaseCIConfirmation), a scheduled
	// workflow, and retrigger_ci are the callers: a re-run that replayed
	// the cached failure would confirm nothing.
	//
	// What it learns is written back, though, replacing the entry it
	// declined to read. The newest verdict for a key is the one every
	// later reader gets. A recheck is asked for because the first answer
	// is in doubt, almost always a red suspected of being a flake. If the
	// recheck passed but left the cache as it found it, wait_for_checks
	// and the merge-queue gate would go on reading the red they were told
	// to doubt until the entry aged out, and the only way past it would
	// be to change the tree so the job got a new key. A recheck that
	// fails again just writes the same answer back. A job's key carries
	// its event (GRAIN_CI_EVENT), so a scheduled run's recheck can only
	// replace a scheduled run's entry, never a push's.
	//
	// For a narrowed run it applies to the jobs that were named, not to
	// the dependencies selectPlanJobs pulls in behind them
	// (PlannedJob.DependencyOnly). A dependency is not what is in doubt:
	// it is there because the named job cannot start without it. So it
	// reuses a cached pass for its own fingerprint when one exists, and
	// runs only when there is none or the cached entry is not a pass -- a
	// red dependency has to be run, since a job waiting behind it would
	// otherwise be skipped and the recheck would learn nothing.
	//
	// Absent from JSON with TrustedRelease and Capabilities, and for the
	// same reason: spending the deployment's sandboxes on work it has
	// already answered is grain's decision to make, not a caller's.
	Recheck bool `json:"-"`
}

// Normalize fills in the defaults a caller is allowed to leave out, so
// everything downstream reads one shape.
// UntrustedCommit is the sentence a job that wanted a capability or a
// release secret is told when the run's commit is a tree nothing vouches
// for (see the field), or "" for a trusted one.
func (r Request) UntrustedCommit() string { return r.untrustedCommit }

// WithUntrustedCommit is r marked as a run of an untrusted commit, with
// the sentence its jobs are told. Only grain's Service.Start calls it.
func (r Request) WithUntrustedCommit(reason string) Request {
	r.untrustedCommit = reason
	return r
}

// EventDefaulted reports whether the caller left Event empty and
// Normalize filled EventManual in (see the field).
func (r Request) EventDefaulted() bool { return r.eventDefaulted }

func (r Request) Normalize() Request {
	r.Repo = strings.TrimSpace(r.Repo)
	r.Ref = strings.TrimPrefix(strings.TrimSpace(r.Ref), "refs/heads/")
	r.BaseRef = strings.TrimPrefix(strings.TrimSpace(r.BaseRef), "refs/heads/")
	r.Commit = strings.TrimSpace(r.Commit)
	r.SandboxImage = strings.TrimSpace(r.SandboxImage)
	r.Capabilities = slices.Clone(r.Capabilities)
	slices.Sort(r.Capabilities)
	r.Capabilities = slices.Compact(r.Capabilities)
	r.ReleaseSecrets = slices.Clone(r.ReleaseSecrets)
	slices.Sort(r.ReleaseSecrets)
	r.ReleaseSecrets = slices.Compact(r.ReleaseSecrets)
	r.Jobs = append([]string(nil), r.Jobs...)
	r.JobFilter = strings.TrimSpace(r.JobFilter)
	if r.JobFilter != "" && !slices.Contains(r.Jobs, r.JobFilter) {
		r.Jobs = append(r.Jobs, r.JobFilter)
	}
	r.Workflow = strings.TrimSpace(r.Workflow)
	for i := range r.Jobs {
		r.Jobs[i] = strings.TrimSpace(r.Jobs[i])
	}
	if r.Event == "" {
		r.Event = EventManual
		r.eventDefaulted = true
	}
	return r
}

// Validate is what an API handler checks before anything else happens:
// a run needs a repository and a branch, and an event grain knows.
func (r Request) Validate() error {
	n := r.Normalize()
	if n.Repo == "" {
		return fmt.Errorf("ci: a run names the repository to run against")
	}
	if !strings.Contains(n.Repo, "/") {
		return fmt.Errorf("ci: repo %q is not owner/name", r.Repo)
	}
	if n.Ref == "" {
		return fmt.Errorf("ci: a run names the branch to run against")
	}
	for _, name := range n.ReleaseSecrets {
		if err := ValidateReleaseSecretName(name); err != nil {
			return fmt.Errorf("ci: %w", err)
		}
	}
	for _, job := range n.Jobs {
		if job == "" {
			return fmt.Errorf("ci: a selected job name cannot be empty")
		}
	}
	switch n.Event {
	case EventPush, EventPullRequest, EventRelease, EventManual, EventSchedule:
	default:
		return fmt.Errorf("ci: %q is not an event grain runs workflows for (%s, %s, %s, %s, %s)",
			r.Event, EventPush, EventPullRequest, EventRelease, EventManual, EventSchedule)
	}
	if n.Event == EventRelease && !n.TrustedRelease {
		return fmt.Errorf("ci: release runs are started by Grain after a merge, not by callers")
	}
	if n.TrustedSchedule && n.Event != EventSchedule {
		return fmt.Errorf("ci: only a %s run can be a trusted schedule, not a %s run", EventSchedule, n.Event)
	}
	return nil
}

// ReleaseSecretsEligible reports whether this run may be handed the
// repository's release secrets: a release, or a manual dispatch its
// caller asked for by name -- and not one grain itself asked to withhold
// them from (WithholdReleaseSecrets).
func (r Request) ReleaseSecretsEligible() bool {
	n := r.Normalize()
	if n.eventDefaulted || n.WithholdReleaseSecrets {
		return false
	}
	return n.Event == EventRelease || n.Event == EventManual
}

// Plan is every job one Request will run, with the `needs` edges
// resolved into plan-wide ids -- the whole decision about *what* runs,
// made before anything is acquired, started or spent.
//
// Separating it from the engine is what makes the interesting parts
// testable with no sandbox in sight: which workflows a branch matches,
// which jobs wait for which, and what each one's cache key is are all
// answered here, by a pure function of a Request and a set of parsed
// files.
type Plan struct {
	Request Request
	// Key is the run-level fingerprint -- see Fingerprint.
	Key  Key
	Jobs []PlannedJob
	// Quarantine carries the repository's quarantine rules when loaded.
	Quarantine *QuarantineConfig
	// Unmatched is why a plan with no jobs in it has none, and is empty
	// on every plan that has some -- and on the one empty plan that is
	// harmless (see PlanRun: a repository declaring no workflows at all,
	// asked for no job in particular).
	//
	// It exists because "no job ran" and "every job passed" are the same
	// verdict to anything that only reads a conclusion, and the second
	// is what the first was reported as: a run that matched no workflow
	// finished green in fifteen seconds, which a merge queue and an
	// agent waiting on wait_for_checks both read as a tested tree
	// (grain/task-254, on a kontur branch whose runs all did exactly
	// that). A run carrying this is concluded errored by the engine,
	// with this as its Run.Error -- nothing of the tree was executed, so
	// it is not a verdict on the commit at all (see ConclusionErrored).
	//
	// It is prose rather than a code because there is nothing to branch
	// on: the only thing a reader can do about it is look at which
	// workflows the repository has and what they answer to, so that is
	// what it says -- and it says which of the two empty plans this is,
	// since "no workflow answers this event" and "a workflow answered
	// and its jobs are not here" are different failures with different
	// people to fix them (emptyPlanReason).
	Unmatched string
	// NothingToPublish marks the one Unmatched plan the engine concludes
	// skipped rather than errored: an automatic release (EventRelease, no
	// job asked for by name) of a repository whose workflows all planned
	// and none of which answers `release` on this branch. For a release
	// that is the whole answer -- the repository publishes nothing, and
	// nothing failed -- so a red run for it on every merge was a false
	// alarm (orchestrator's collectBaseBranchRelease already read it as
	// skipped). Unmatched still says why, so NoWorkflowAnswered still
	// recognises the run.
	//
	// A release that named jobs asked for work by name and got none, and
	// a workflow file that could not be planned may be the very `release`
	// workflow that would have answered: both stay errored.
	NothingToPublish bool
}

// PlannedJob is one job of a plan: the job as declared, plus everything
// the engine needs that the declaration alone does not carry.
type PlannedJob struct {
	// ID is unique across the plan, which a job's own ID is not: two
	// workflows may each declare a job called "test". It is
	// "<workflow path>:<job id>", the pair that is unique by
	// construction, and it is what Needs below names.
	ID string
	// Workflow and WorkflowPath are the file this came from.
	Workflow     string
	WorkflowPath string
	// Job is the declaration itself.
	Job Job
	// WorkflowEnv is the `env:` block of the file this job came from --
	// carried on the planned job rather than looked up again, so the
	// engine needs no map from a job back to its workflow to know what
	// environment it inherits.
	WorkflowEnv map[string]string
	// Needs are plan ids, resolved from the job's own workflow-local
	// `needs` -- always jobs of the same workflow, since there is no
	// cross-workflow needs (Job.Needs' own doc comment).
	Needs []string
	// Key is this job's own fingerprint.
	Key Key
	// KeyedOnFiles is how many of the tree's files this job's key was
	// computed from, and is zero for a job keyed on the commit -- every
	// job that declares no `paths:`, every job whose declaration matched
	// nothing, and every job of a run whose source could not say what the
	// commit holds (paths.go).
	//
	// Carried on the plan rather than worked out again by whatever wants
	// to report it, because it is the one thing about a key a person
	// actually asks: not what the hash is, but what it was taken over.
	KeyedOnFiles int
	// GuestKey identifies the sandbox guest this job will resolve, by the
	// content of the files it declared as `guest-cache:` -- the key grain
	// warms the cache under, and the key it single-flights on
	// (guestcache.go). Empty for every job that declares none, for a
	// declaration that matched nothing in this tree, and for a run whose
	// source could not say what the commit holds; all three mean "grain
	// warms nothing for this job", which is what every job did before this
	// existed.
	GuestKey Key
	// GuestFiles is how many of the tree's files GuestKey was taken over,
	// for the same reason KeyedOnFiles is carried: the question anybody
	// asks of a key is what went into it.
	GuestFiles int
	// DependencyOnly marks a job a narrowed run includes only because a
	// selected job `needs` it (selectPlanJobs), never because it was
	// asked for. A recheck reuses such a job's cached pass rather than
	// spending a sandbox on it again (Request.Recheck).
	DependencyOnly bool
	// BaseCommit is the base commit SHA this job was planned against, used
	// to invalidate cached failures when the base branch advances.
	BaseCommit string
}

// KeyedOnPaths reports whether this job's key is the content of the files
// it declared rather than the commit -- so a commit that changed nothing
// it reads will hit the cache instead of running it.
func (p PlannedJob) KeyedOnPaths() bool { return p.KeyedOnFiles > 0 }

// PlanRun decides what req runs against workflows: every job of every
// workflow whose `on:` answers to this event and branch, in a stable
// order (workflow path, then job id), with `needs` resolved and a
// fingerprint on each.
//
// A repository that declares no workflows at all is not an error: it is
// a plan with no jobs, and a run of no jobs is a success. That is
// deliberate -- CI that fails a branch because the repository declares
// no workflow for it would make adding `.grain/workflows` to one repo a
// change to every other.
//
// A repository that *does* declare workflows and matched none of them is
// a different animal, and is not a success. It looks identical from the
// outside -- a run with no jobs -- but somebody has asked for CI here and
// this commit got none of it, which is the one shape of green that can
// merge an untested tree. Those plans come back with Unmatched set and
// are concluded errored by the engine.
func PlanRun(req Request, workflows []Workflow) (Plan, error) {
	return PlanRunIn(req, workflows, nil)
}

// PlanRunIn is PlanRun against a tree whose content is known, which is
// what lets a job that declares `paths:` be keyed on the files it reads
// rather than on the commit (paths.go). A nil tree plans exactly what
// PlanRun does, and is what every Source that cannot read a checkout
// gives.
func PlanRunIn(req Request, workflows []Workflow, tree *Tree) (Plan, error) {
	req = req.Normalize()
	if err := req.Validate(); err != nil {
		return Plan{}, err
	}
	plan := Plan{Request: req}

	sorted := append([]Workflow(nil), workflows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	seen := map[string]bool{}
	selectedWorkflow := false
	// The workflows that answered this event, kept because an empty plan
	// means something different depending on whether this list is empty
	// too (emptyPlanReason).
	var answered []Workflow
	for _, w := range sorted {
		if seen[w.Path] {
			return Plan{}, fmt.Errorf("ci: %s: two workflows with the same path", w.Path)
		}
		seen[w.Path] = true
		if req.Workflow != "" && w.Path != req.Workflow {
			continue
		}
		event, matches := WorkflowEvent(w, req)
		if !matches {
			continue
		}
		selectedWorkflow = true
		answered = append(answered, w)
		if w.planningError != nil {
			return Plan{}, w.planningError
		}
		for _, job := range w.Jobs {
			if job.Actions != nil {
				job.Actions = &ActionsJob{
					WorkflowPath: job.Actions.WorkflowPath,
					Event:        event,
					WorkflowText: job.Actions.WorkflowText,
				}
				job.Steps[0].Run = actionsJobScript(job.Actions.WorkflowPath, job.ID, event, job.Actions.WorkflowText)
			}
			pj := PlannedJob{
				ID:           PlanID(w.Path, job.ID),
				Workflow:     w.Name,
				WorkflowPath: w.Path,
				Job:          job,
				WorkflowEnv:  w.Env,
				Key:          FingerprintIn(req, w, job, tree),
				BaseCommit:   req.BaseCommit,
			}
			if files, ok := tree.Match(job.Paths, job.PathsIgnore); ok {
				pj.KeyedOnFiles = len(files)
			}
			if files, ok := tree.Match(job.GuestCache, nil); ok {
				pj.GuestKey, pj.GuestFiles = GuestCacheKey(req.Repo, files), len(files)
			}
			for _, need := range job.Needs {
				pj.Needs = append(pj.Needs, PlanID(w.Path, need))
			}
			plan.Jobs = append(plan.Jobs, pj)
		}
	}
	if req.Workflow != "" && !selectedWorkflow {
		return Plan{}, fmt.Errorf("ci: workflow %q does not exist or does not match %s", req.Workflow, req.Event)
	}
	if len(plan.Jobs) == 0 {
		// Nothing to run. Say why, and say it here rather than leaving
		// it to whatever reads the empty result: this is the only place
		// that still holds the workflows that were considered, what each
		// of them answers to and which of them answered, which is the
		// whole of the answer.
		//
		// The job selection below is skipped rather than run against an
		// empty plan. It could only fail -- "selected job "test" does not
		// exist in the matching workflows", which is what a caller asking
		// for one job of a branch that matched nothing was told, an error
		// that names the symptom and hides the cause -- and an errored
		// run carrying the explanation is a better answer to the same
		// question than a 500 is.
		plan.Unmatched = emptyPlanReason(req, sorted, answered)
		plan.NothingToPublish = nothingToPublish(req, sorted, answered, plan.Unmatched)
		plan.Key = planKey(req, plan.Jobs)
		return plan, nil
	}
	if len(req.Jobs) > 0 {
		var err error
		plan.Jobs, err = selectPlanJobs(plan.Jobs, req.Jobs)
		if err != nil {
			return Plan{}, err
		}
	}
	plan.Key = planKey(req, plan.Jobs)
	return plan, nil
}

// emptyPlanReason is what a plan with no jobs says for itself, and it
// begins by saying which of the two empty plans this is -- because they
// look identical from the outside and send whoever reads them to
// different places.
//
// answered is the workflows whose `on:` matched this request. Empty
// means no workflow wanted this event on this branch, which is a fact
// about the repository's triggers and is somebody's to fix in the tree
// (unmatchedReason). Non-empty means a workflow did answer and the plan
// still came out with nothing in it, which is a fact about grain: the
// job graph went missing between parsing the file and planning the run,
// and nothing in the repository's triggers explains it
// (droppedJobsReason).
//
// Telling them apart matters because the second was reported as the
// first. A `.github` workflow grain could not plan kept its triggers and
// lost its jobs, so the run it emptied was explained by listing triggers
// that had nothing to do with it (grain/task-254's last case, fixed by
// carrying the workflow's kind through the error); a reader given "no
// workflow answers this event" went looking at `on:` keys for a fault
// that was never there. This function is what keeps that answer honest
// if a job graph is ever dropped again (grain/task-388).
func emptyPlanReason(req Request, workflows, answered []Workflow) string {
	if len(answered) > 0 {
		return droppedJobsReason(req, answered)
	}
	return unmatchedReason(req, workflows)
}

// droppedJobsReason is the empty plan that no trigger explains: these
// workflows answered, and the plan holds no job of any of them.
//
// It is a fault in this deployment rather than in the branch, and says
// so, because that is the difference a reader acts on: there is nothing
// to change in the tree, and the jobs each answering workflow declares
// -- none, for the shape that has ever actually occurred -- is where to
// start looking.
func droppedJobsReason(req Request, answered []Workflow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ci: %d workflow(s) of %s answer %s of branch %s and the run planned no job from "+
		"any of them, so it had nothing to run and tested nothing. Nothing in the repository's "+
		"triggers explains this: the jobs went missing between parsing these files and planning "+
		"the run, which is a fault in this deployment rather than in the branch",
		len(answered), req.Repo, req.Event, req.Ref)
	if len(req.Jobs) > 0 {
		fmt.Fprintf(&b, " (the run asked for %s)", strings.Join(req.Jobs, ", "))
	}
	b.WriteString(". The workflows that answered, and the jobs each one declares:")
	for _, w := range answered {
		fmt.Fprintf(&b, "\n  %s: %s", w.Path, describeJobs(w))
	}
	return b.String()
}

// describeJobs is one workflow's jobs as the explanation above names
// them, and carries the file's own planning error where it has one: a
// workflow that could not be planned is the one way in the tree's
// history that a file has held triggers and no jobs at once
// (parseForSource), so it is the first thing worth ruling in or out.
func describeJobs(w Workflow) string {
	if len(w.Jobs) == 0 {
		if w.planningError != nil {
			return "no job at all, because this file could not be planned: " + w.planningError.Error()
		}
		return "no job at all"
	}
	ids := make([]string, 0, len(w.Jobs))
	for _, j := range w.Jobs {
		ids = append(ids, j.ID)
	}
	return strings.Join(ids, ", ")
}

// unmatchedReason is what a plan no workflow answered says for itself:
// which workflows this repository has at this commit, what each of them
// answers to, and what was asked for -- because the question anybody
// asks of a run that tested nothing is "then what would have had to be
// true for it to test something", and the workflows' own triggers are
// the answer.
//
// It returns "" for the one empty plan that is not a problem: a
// repository that declares no workflows at all, asked for no job in
// particular. That run really does have nothing to report and stays a
// success (PlanRun's own doc comment, and pkg/forge/checks.go's "A
// repository that declares no workflows"). A caller that named jobs is
// told even then, because it asked for work by name and got none.
func unmatchedReason(req Request, workflows []Workflow) string {
	if len(workflows) == 0 {
		if len(req.Jobs) == 0 {
			return ""
		}
		return fmt.Sprintf("ci: %s declares no workflows at %s, so the selected job(s) %s could not run",
			req.Repo, req.Ref, strings.Join(req.Jobs, ", "))
	}
	var b strings.Builder
	fmt.Fprintf(&b, NoWorkflowAnswersPrefix+"%s answers %s of branch %s, so this run had nothing to run and tested nothing",
		req.Repo, req.Event, req.Ref)
	if len(req.Jobs) > 0 {
		fmt.Fprintf(&b, " (the run asked for %s)", strings.Join(req.Jobs, ", "))
	}
	b.WriteString(". The workflows this commit declares, and what each one answers to:")
	for _, w := range workflows {
		fmt.Fprintf(&b, "\n  %s: %s", w.Path, w.On.Describe())
		if w.planningError != nil {
			// A file grain could not plan keeps its triggers and loses
			// its jobs (parseForSource), so it is listed here with an
			// `on:` it would never have run anything for. Saying so is
			// the difference between a reader comparing triggers that
			// were never the problem and a reader fixing the file.
			fmt.Fprintf(&b, " -- though this file could not be planned, so it declares "+
				"nothing grain could have run: %s", w.planningError)
		}
	}
	return b.String()
}

// nothingToPublish reports whether an empty plan is Plan.NothingToPublish's
// case: an automatic release that no workflow answered, with every
// workflow of the repository planned cleanly.
func nothingToPublish(req Request, workflows, answered []Workflow, unmatched string) bool {
	if req.Event != EventRelease || len(req.Jobs) > 0 || len(answered) > 0 ||
		!strings.HasPrefix(unmatched, NoWorkflowAnswersPrefix) {
		return false
	}
	for _, w := range workflows {
		if w.planningError != nil {
			return false
		}
	}
	return true
}

// NoWorkflowAnswersPrefix is how unmatchedReason's explanation starts,
// and what NoWorkflowAnswered recognises it by.
const NoWorkflowAnswersPrefix = "ci: no workflow of "

// WorkflowEvent translates Grain events into the event an Actions workflow
// understands for the same work. Native Grain workflows keep Grain's event.
//
// A trusted post-merge release is a push to the destination branch from an
// Actions workflow's perspective. Similarly, Grain pushes a task branch before
// opening its pull request, but an Actions workflow sees that work as a pull
// request. These translations follow the workflow kind, not its directory:
// Actions-compatible workflows may live under either .grain or .github.
//
// The plan retains the original event as its security context while act
// receives the translated event, so release-only capabilities cannot leak into
// an ordinary caller-created push run.
func WorkflowEvent(w Workflow, req Request) (Event, bool) {
	if w.On.Matches(req.Event, req.Ref) {
		return req.Event, true
	}
	actions := w.isActions()
	if req.Event == EventRelease && actions && w.On.Push.Matches(req.Ref) {
		return EventPush, true
	}
	if req.Event == EventPush && actions && w.On.PullRequest != nil {
		return EventPullRequest, true
	}
	return req.Event, false
}

// selectPlanJobs narrows a complete plan without making `needs` lie. A
// selected job brings every transitive dependency with it, and the result
// keeps the complete plan's stable order.
func selectPlanJobs(jobs []PlannedJob, selectors []string) ([]PlannedJob, error) {
	byPlanID := make(map[string]PlannedJob, len(jobs))
	byJobID := make(map[string][]PlannedJob, len(jobs))
	for _, job := range jobs {
		byPlanID[job.ID] = job
		byJobID[job.Job.ID] = append(byJobID[job.Job.ID], job)
	}

	// A job's display name ("ui e2e") is accepted as well as its id
	// ("ui-e2e"): it is what every report of a run prints, so it is what a
	// caller copying a failing job out of one has in hand.
	byName := make(map[string][]PlannedJob, len(jobs))
	for _, job := range jobs {
		if name := job.Job.DisplayName(); name != job.Job.ID {
			byName[name] = append(byName[name], job)
		}
	}

	selected := map[string]bool{}
	named := map[string]bool{}
	var include func(string)
	include = func(id string) {
		if selected[id] {
			return
		}
		selected[id] = true
		for _, need := range byPlanID[id].Needs {
			include(need)
		}
	}
	for _, selector := range selectors {
		if job, ok := byPlanID[selector]; ok {
			named[job.ID] = true
			include(job.ID)
			continue
		}
		matches := byJobID[selector]
		if len(matches) == 0 {
			matches = byName[selector]
		}
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("ci: selected job %q does not exist in the matching workflows; the jobs there are: %s",
				selector, strings.Join(selectableJobNames(jobs), ", "))
		case 1:
			named[matches[0].ID] = true
			include(matches[0].ID)
		default:
			return nil, fmt.Errorf("ci: selected job %q is ambiguous; use <workflow path>:%s", selector, matches[0].Job.ID)
		}
	}

	result := make([]PlannedJob, 0, len(selected))
	for _, job := range jobs {
		if selected[job.ID] {
			job.DependencyOnly = !named[job.ID]
			result = append(result, job)
		}
	}
	return result, nil
}

// selectableJobNames is every spelling of jobs a selection accepts
// without ambiguity, for the sentence that refuses one it does not: the
// job id where it is unique across the plan, and the plan-wide
// "<workflow path>:<job id>" where it is not.
func selectableJobNames(jobs []PlannedJob) []string {
	count := map[string]int{}
	for _, job := range jobs {
		count[job.Job.ID]++
	}
	names := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if count[job.Job.ID] == 1 {
			names = append(names, job.Job.ID)
		} else {
			names = append(names, job.ID)
		}
	}
	return names
}

// PlanID is PlannedJob.ID's own construction, in one place because both
// a job and everything that needs it have to agree on it.
func PlanID(workflowPath, jobID string) string { return workflowPath + ":" + jobID }

// NoWorkflowAnswered reports whether run is the empty run of a
// repository that declares workflows and has none answering the run's
// event on its branch -- unmatchedReason's case, as against
// droppedJobsReason's, which is a fault in this deployment.
//
// For most callers it is an errored run: a push that matched
// nothing tested nothing, and saying so is the point of
// Plan.Unmatched. It is a question of its own for the one caller for
// which no workflow answering is an answer in itself: a release of a
// repository that publishes nothing (orchestrator's
// collectBaseBranchRelease), where it means there was nothing to
// publish rather than a release that broke. It reads the run's Error
// rather than a field of its own so that it gives the same answer for a
// run read back out of a durable store.
//
// Such a release run is concluded skipped (Plan.NothingToPublish); one
// stored before that was concluded errored, and both are recognised.
func NoWorkflowAnswered(run Run) bool {
	return (run.Conclusion == ConclusionSkipped || run.Conclusion == ConclusionErrored) && len(run.Jobs) == 0 &&
		strings.HasPrefix(run.Error, NoWorkflowAnswersPrefix)
}
