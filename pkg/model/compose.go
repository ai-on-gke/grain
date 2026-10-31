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

package model

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// A task grain files for itself is never written out field by field the
// way a human writes one at the new-task form: it is composed, out of
// two or three declarations that each get a say. A schedule fires a
// template; a suite run fires several of them against one repo and
// branch; a qualification run fires a plan's against one candidate's
// branch. Each of those points used to resolve "what does the task
// actually carry" for itself, in its own function, with its own
// precedence -- which is how the same template came to auto-merge when a
// schedule fired it and not when a suite did, without either place
// saying so.
//
// This file is that resolution, once. Every firing point calls it, and
// so does every preview of one (ui.Client.ScheduleComposition,
// ui.Client.SuiteComposition), so what a UI shows a human before they
// press anything is not a second implementation that can drift from the
// first: it is the same call, against the same declarations, made
// without writing a task.
//
// # The rule
//
// A composed task has three kinds of field, and each kind has exactly
// one layer that decides it:
//
//   - Content -- title, body, reads, writes, capabilities, custom
//     fields, network access. The template's, whenever a firing has a
//     template; the schedule's own inline fields when it has none.
//     Never merged: a template-backed schedule carries no per-field
//     override, deliberately (ui.CreateScheduleRequest's own doc
//     comment on why), so the answer to "where did this body come
//     from" is one layer and not two.
//   - Placement -- the repo a task is filed against and the branch it
//     bases off. The template's binding (Template.Target) when it has
//     one, because a binding is the template saying its content only
//     makes sense against that repo; otherwise whatever fired it -- a
//     schedule's own repo, a suite run's own repo. A qualification run
//     is the exception and says so out loud: its tasks exist to test
//     one candidate's own branch, so the candidate's placement wins
//     over everything, and a bound template is refused entry to a plan
//     for another repo rather than silently retargeted
//     (CreateQualificationRun).
//   - Disposition -- whether the task lands approved, and whether its
//     pull request auto-merges. The batch decides when there is a batch:
//     a suite run's own RequireApproval/AutoMerge are the policy for
//     every task in every pass of it, since "run these templates
//     together, this way" is the whole of what a suite is. With no batch
//     policy to apply, the template's own AutoMerge stands (a
//     qualification plan has an approval switch and no auto-merge one,
//     so its tasks take the template's), and a schedule's firing is
//     always approved -- creating the schedule was the standing
//     approval, docs/data-model.md's dissolved SCHEDULED special case.
//
// Composition records which of those layers answered for each field
// (CompositionSources), which is what makes the preview worth showing:
// "auto-merge: on, from the suite" is an answer a human can act on where
// a bare "on" is not.

// CompositionLayer names one of the declarations a composed task's
// fields can come from -- what CompositionSources reports per field, and
// what a UI renders as "from the template" beside the value.
type CompositionLayer string

const (
	// LayerTemplate is the Template a firing fires (template.go).
	LayerTemplate CompositionLayer = "template"
	// LayerSchedule is the Schedule itself (schedule.go) -- its inline
	// content, when it fires no template, and its repo and branch either
	// way.
	LayerSchedule CompositionLayer = "schedule"
	// LayerSuite is the suite run (suite.go): its own repo and branch,
	// and the approval and auto-merge policy it applies to every task of
	// every pass.
	LayerSuite CompositionLayer = "suite"
	// LayerPlan is the QualificationPlan (qualification.go) -- its
	// RequireApproval, and nothing else: a plan carries no content and
	// no placement of its own.
	LayerPlan CompositionLayer = "plan"
	// LayerCandidate is the Candidate a qualification run is testing,
	// whose repo and branch every task in that run is pinned to.
	LayerCandidate CompositionLayer = "candidate"
	// LayerFiling is the person or agent filing one task straight from a
	// template (ComposeTemplateTask, grain/task-58): the repo and branch
	// they chose at the form, and whether what they filed lands
	// approved. There is no standing declaration behind this layer the
	// way there is behind a schedule or a suite -- it is one filing,
	// decided once, by whoever was looking at it.
	LayerFiling CompositionLayer = "filing"
	// LayerDeployment is "nothing said anything, so the deployment's own
	// default decides" -- today only NetworkAccess, whose empty value is
	// not a value at all but a deferral: whatever the firing was going
	// to carry it from (a template, a schedule's own inline content)
	// left it unset, so Config.NetworkAccess answers, read when the run
	// is dispatched rather than when the task is filed. networkSourceOf
	// is where that is decided, for every firing point alike.
	LayerDeployment CompositionLayer = "deployment"
	// LayerTeam is "nothing chose a model, so the team's own default
	// does" (Team.DefaultModel, docs/connections-and-models.md) -- the
	// model's counterpart of LayerDeployment, and decided the same way:
	// an empty Model is a deferral, resolved when the run is dispatched.
	LayerTeam CompositionLayer = "team"
	// LayerRepo is the target repository's own configuration
	// (RepoConfig) -- today the default grain-ci grant that
	// adjustGrainCIGrant attaches to every task targeting a repository
	// whose CI Mode is "Use Grain CI" (RepoUsesGrainCI).
	LayerRepo CompositionLayer = "repo"
)

// modelSourceOf is networkSourceOf for the model: said when the layer
// asked chose one, and LayerTeam when it left the choice to the team's
// default.
func modelSourceOf(chosen string, said CompositionLayer) CompositionLayer {
	if chosen == "" {
		return LayerTeam
	}
	return said
}

// ModelSourceOf is Task.ModelSource for a model chosen by the layer by:
// that layer when it chose one, and empty when it chose none -- the
// team's default decides then, and a task records no source for it. It
// is what a filing point that builds its Task without a Composition (the
// orchestrator's review and base-branch filings from a template) stamps,
// so it says what Composition.Task would have.
func ModelSourceOf(chosen string, by CompositionLayer) CompositionLayer {
	if chosen == "" {
		return ""
	}
	return by
}

// networkSourceOf is which layer a composed NetworkAccess actually came
// from: said, the declaration that was asked, when it carries a value --
// and LayerDeployment when it does not, whoever was asked.
//
// Network access is the one composed field whose empty value names
// another decider rather than an absence. An empty Base is still the
// schedule's own choice of "the repo's default branch" and empty Reads
// are still the template saying "none", so both report the layer that
// carries them; an empty NetworkAccess is Config.NetworkAccess's answer
// and nobody else's (NetworkAccess's own "zero means unset" contract), so
// a preview that said "from the template" over it would name a layer
// that decided nothing.
func networkSourceOf(access NetworkAccess, said CompositionLayer) CompositionLayer {
	if access == "" {
		return LayerDeployment
	}
	return said
}

// CompositionSources is which layer decided each field of a Composition
// -- one CompositionLayer per field of it, in the same order, so a
// reader can put "from the suite" beside "auto-merge: on".
type CompositionSources struct {
	Title                CompositionLayer
	Body                 CompositionLayer
	Target               CompositionLayer
	Base                 CompositionLayer
	Reads                CompositionLayer
	Writes               CompositionLayer
	Grants               CompositionLayer
	CustomFields         CompositionLayer
	NetworkAccess        CompositionLayer
	NothingNeededAction  CompositionLayer
	Model                CompositionLayer
	AutoMerge            CompositionLayer
	AutoApproveProposals CompositionLayer
	Approved             CompositionLayer
	Interactive          CompositionLayer
	KeepRunning          CompositionLayer
}

// Composition is one task a firing will file, resolved: every value it
// carries, and which layer decided each one. It is deliberately not a
// Task -- it has no id, no origin and no links, because those are the
// filing's own and a composition can be computed without filing
// anything, which is what a preview is.
//
// Task below turns one into the task itself, so the two can never
// disagree about what a composition meant.
type Composition struct {
	Title string
	Body  string

	// Target is the repo this task is filed against, nil for a firing
	// with no repo at all -- Task.Target's own nil, and the state only a
	// system template's firing is ever in (its work is a comment or a
	// store write, and "" / "" is not a repository).
	Target *RepoRef
	// ChangeTarget is the non-git change target (a registered kind) this task is
	// filed against, nil for git and no-repo tasks.
	ChangeTarget *ChangeTarget
	// Reviewers is the person-set reviewer list from the template (Task.Reviewers).
	Reviewers []string
	// Base is the branch it bases off, empty for the target repo's own
	// default branch.
	Base string

	Reads []RepoRef
	// Writes is the extra write targets, already minus any naming this
	// task's own target -- WritesBesides, applied here so that what a
	// preview shows and what a firing files are the same list.
	Writes []WriteTarget
	Grants []Grant
	// GrantSources, when non-empty, gives the layer that contributed
	// each entry in Grants (same length and order). Every grant comes
	// from Sources.Grants when a single layer decided the whole list;
	// GrantSources is populated alongside Grants so a preview can still
	// credit each grant to its own layer when a template-backed
	// schedule adds a grant of its own or adjustGrainCIGrant attaches
	// grain-ci for the target repo.
	GrantSources []CompositionLayer
	CustomFields map[string][]string
	// NetworkAccess is empty for "the deployment's own answer"
	// (Config.NetworkAccess), exactly as it is on a Task.
	NetworkAccess NetworkAccess
	// NothingNeededAction is empty for "close", exactly as it is on a
	// Task.
	NothingNeededAction NothingNeededAction
	// Model is the model the task asks for (Task.Model), empty for its
	// team's default.
	Model string

	AutoMerge            bool
	AutoApproveProposals bool
	// Approved is whether the filed task lands already approved, with no
	// human in the loop -- Task.Approval set by the firing's own actor.
	Approved bool
	// Interactive is whether the task is a live chat (Task.Interactive).
	// Only a template says so: an inline schedule's firing never is one.
	Interactive bool
	// KeepRunning is whether the task immediately restarts when its run
	// exits (Task.KeepRunning).
	KeepRunning bool
	// Priority is the resolved numeric priority for the task (DefaultTaskPriority = 5).
	Priority int

	// TemplateID and TemplateName name the template this task's content
	// came from, both empty for a schedule firing its own inline
	// content. They are how a preview says which row a value came out of,
	// and TemplateID is also carried onto the filed task (Task.TemplateID),
	// which is how its runs find the template's declared push set.
	TemplateID   string
	TemplateName string

	Sources CompositionSources
}

// Task is the task this composition files: everything the composition
// decided, plus the identity and attribution the filing itself supplies
// -- id, the automation actor it is attributed to, and why it exists.
// Callers add whatever else is theirs alone (a schedule's idempotency
// tag, a qualification instance's dependency links, an order key) and
// write it.
//
// The approval an approved composition lands with is attributed to actor
// too: the mechanism that filed the task is also the thing carrying the
// standing human approval that let it land queued -- a schedule somebody
// created, a suite somebody started -- and Task.Approval names an actor,
// not a moment of judgement (LandsQueued's own reading of it).
func (c Composition) Task(id string, actor Principal, reason OriginReason, now time.Time) Task {
	task := Task{
		ID:     id,
		Intent: IntentImplement,
		Title:  c.Title,
		Body:   c.Body,
		Origin: Origin{
			Attribution: Attribution{Actor: actor},
			Reason:      reason,
		},
		Target:               c.Target,
		ChangeTarget:         c.ChangeTarget.Clone(),
		Reviewers:            slices.Clone(c.Reviewers),
		Binding:              BindingDirective,
		Base:                 c.Base,
		Reads:                c.Reads,
		Writes:               c.Writes,
		Grants:               c.Grants,
		CustomFields:         c.CustomFields,
		AutoMerge:            c.AutoMerge,
		AutoApproveProposals: c.AutoApproveProposals,
		NetworkAccess:        c.NetworkAccess,
		NothingNeededAction:  c.NothingNeededAction,
		Model:                c.Model,
		TemplateID:           c.TemplateID,
		Interactive:          c.Interactive,
		KeepRunning:          c.KeepRunning,
		CreatedAt:            &now,
	}
	if c.Model != "" {
		task.ModelSource = c.Sources.Model
	}
	if c.Priority != 0 {
		task.Priority = c.Priority
	} else {
		task.Priority = DefaultTaskPriority
	}
	if c.Approved {
		task.Approval = &Attribution{Actor: actor}
		task.ApprovedAt = &now
	}
	return task
}

// placement is one composition's repo and branch, with the layer that
// decided them -- worked out once here so the three firing points cannot
// disagree about what a binding beats.
func placementOf(tmpl *Template, target RepoRef, base string, from CompositionLayer) (RepoRef, string, CompositionLayer) {
	if tmpl == nil || tmpl.Target == nil {
		return target, base, from
	}
	t, b := tmpl.FiringTarget(target, base)
	return t, b, LayerTemplate
}

// targetPointer is Composition.Target's nil-for-no-repo rule, kept in
// one place: a firing with no repo of its own files a task with no
// target at all rather than one pointing at the empty repo the zero
// RepoRef spells.
func targetPointer(target RepoRef) *RepoRef {
	if target == (RepoRef{}) {
		return nil
	}
	return &target
}

func repeatLayer(layer CompositionLayer, n int) []CompositionLayer {
	if n == 0 {
		return nil
	}
	out := make([]CompositionLayer, n)
	for i := range out {
		out[i] = layer
	}
	return out
}

// composeTemplate is the shared body: tmpl's content, placed against
// target/base unless tmpl is bound, disposed of the way the batch asks.
//
// autoMerge is the batch's own answer where it has one and nil where it
// does not, which is the whole of the difference between a suite run
// (its own switch, for every task in it) and a schedule or a
// qualification plan (neither has one, so the template's stands).
//
// values is what the template's input fields are filled in with
// (TemplateField, grain/task-58) -- nil from every unattended firing,
// which is how they get each field's default. Substituting here rather
// than at each caller is what stops a `{{name}}` reaching an agent's
// prompt from whichever firing point somebody forgot.
func composeTemplate(tmpl Template, values map[string]string, target RepoRef, base string, placedBy CompositionLayer,
	autoMerge *bool, autoMergeFrom CompositionLayer,
	autoApproveProposals *bool, autoApproveProposalsFrom CompositionLayer,
	approved bool, approvedBy CompositionLayer) Composition {

	filled := tmpl.FieldValues(values)
	repo, branch, placement := placementOf(&tmpl, target, base, placedBy)
	var changeTarget *ChangeTarget
	if tmpl.ChangeTarget != nil {
		changeTarget = tmpl.ChangeTarget.Clone()
		repo, branch, placement = RepoRef{}, "", LayerTemplate
	}
	merge, mergeFrom := tmpl.AutoMerge, LayerTemplate
	if autoMerge != nil {
		merge, mergeFrom = *autoMerge, autoMergeFrom
	}
	if changeTarget != nil {
		merge, mergeFrom = false, LayerTemplate
	}
	var approveProp bool
	var approvePropFrom CompositionLayer
	if autoApproveProposals != nil {
		approveProp, approvePropFrom = *autoApproveProposals, autoApproveProposalsFrom
	}
	grants := tmpl.Grants
	if changeTarget != nil {
		if reg, ok := LookupChangeTarget(changeTarget.Kind); ok && reg.EnsureGrants != nil {
			if g, err := reg.EnsureGrants(changeTarget.Spec, grants); err == nil {
				grants = g
			}
		}
	}
	priority := DefaultTaskPriority
	if tmpl.Priority != nil && *tmpl.Priority != 0 {
		priority = *tmpl.Priority
	}
	return Composition{
		Title:                SubstituteFields(tmpl.Title, filled),
		Body:                 SubstituteFields(tmpl.Body, filled),
		Target:               targetPointer(repo),
		ChangeTarget:         changeTarget,
		Reviewers:            slices.Clone(tmpl.Reviewers),
		Base:                 branch,
		Reads:                tmpl.Reads,
		Writes:               WritesBesides(tmpl.Writes, repo, branch),
		Grants:               grants,
		GrantSources:         repeatLayer(LayerTemplate, len(grants)),
		CustomFields:         tmpl.CustomFields,
		NetworkAccess:        tmpl.NetworkAccess,
		NothingNeededAction:  tmpl.NothingNeededAction,
		Model:                tmpl.Model,
		AutoMerge:            merge,
		AutoApproveProposals: approveProp,
		Approved:             approved,
		Interactive:          tmpl.Interactive,
		KeepRunning:          tmpl.KeepRunning,
		Priority:             priority,
		TemplateID:           tmpl.ID,
		TemplateName:         tmpl.Name,
		Sources: CompositionSources{
			Title:                LayerTemplate,
			Body:                 LayerTemplate,
			Target:               placement,
			Base:                 placement,
			Reads:                LayerTemplate,
			Writes:               LayerTemplate,
			Grants:               LayerTemplate,
			CustomFields:         LayerTemplate,
			NetworkAccess:        networkSourceOf(tmpl.NetworkAccess, LayerTemplate),
			NothingNeededAction:  LayerTemplate,
			Model:                modelSourceOf(tmpl.Model, LayerTemplate),
			AutoMerge:            mergeFrom,
			AutoApproveProposals: approvePropFrom,
			Approved:             approvedBy,
			Interactive:          LayerTemplate,
			KeepRunning:          LayerTemplate,
		},
	}
}

// ComposeScheduleTask is the task one firing of a task-backed schedule
// files -- tmpl is the template it fires, or nil for a schedule firing
// its own inline content.
//
// Every firing is approved, whichever of the two it is: creating the
// schedule was the human's standing approval (fireTaskSchedule's own doc
// comment), and there is no per-firing choice about it.
func ComposeScheduleTask(sched Schedule, tmpl *Template) Composition {
	if tmpl != nil {
		comp := composeTemplate(*tmpl, nil, sched.Target, sched.Base, LayerSchedule,
			nil, "", &sched.AutoApproveProposals, LayerSchedule, true, LayerSchedule)
		// A review of a workflow run says which branch the run was on
		// when the schedule chose one (WorkflowReviewTitle).
		if _, ok := WorkflowReviewStem(tmpl.ID); ok {
			comp.Title = WorkflowReviewTitle(comp.Title, sched.Base)
		}
		// Unlike the template's content, a model the schedule chose
		// beats the template's (Schedule.Model's own doc comment): which
		// model runs a job is a choice about this firing's cadence, and
		// a declared schedule -- which carries no content of its own --
		// has no other way to make it.
		if sched.Model != "" {
			comp.Model, comp.Sources.Model = sched.Model, LayerSchedule
		}
		// A template-backed schedule stores a copy of its template's
		// grants (ui.scheduleContentFromTemplate), so only a capability
		// the template does not already grant is the schedule's own to
		// credit: crediting the copy would have the preview say a
		// template's capabilities come "from the schedule".
		if len(sched.Grants) > 0 {
			combined := make([]Grant, len(comp.Grants), len(comp.Grants)+len(sched.Grants))
			copy(combined, comp.Grants)
			sources := repeatLayer(LayerTemplate, len(comp.Grants))
			for _, sg := range sched.Grants {
				found := false
				for _, tg := range comp.Grants {
					if tg.Capability == sg.Capability {
						found = true
						break
					}
				}
				if !found {
					combined = append(combined, sg)
					sources = append(sources, LayerSchedule)
				}
			}
			if len(combined) > len(comp.Grants) {
				comp.Sources.Grants = LayerSchedule
			}
			comp.Grants = combined
			comp.GrantSources = sources
		}
		return comp
	}
	return Composition{
		Title:                sched.Title,
		Body:                 sched.Body,
		Target:               targetPointer(sched.Target),
		Base:                 sched.Base,
		Reads:                sched.Reads,
		Writes:               WritesBesides(sched.Writes, sched.Target, sched.Base),
		Grants:               sched.Grants,
		GrantSources:         repeatLayer(LayerSchedule, len(sched.Grants)),
		CustomFields:         sched.CustomFields,
		NetworkAccess:        sched.NetworkAccess,
		NothingNeededAction:  sched.NothingNeededAction,
		Model:                sched.Model,
		AutoMerge:            sched.AutoMerge,
		AutoApproveProposals: sched.AutoApproveProposals,
		Approved:             true,
		KeepRunning:          sched.KeepRunning,
		Priority:             DefaultTaskPriority,
		Sources: CompositionSources{
			Title:                LayerSchedule,
			Body:                 LayerSchedule,
			Target:               LayerSchedule,
			Base:                 LayerSchedule,
			Reads:                LayerSchedule,
			Writes:               LayerSchedule,
			Grants:               LayerSchedule,
			CustomFields:         LayerSchedule,
			NetworkAccess:        networkSourceOf(sched.NetworkAccess, LayerSchedule),
			NothingNeededAction:  LayerSchedule,
			Model:                modelSourceOf(sched.Model, LayerSchedule),
			AutoMerge:            LayerSchedule,
			AutoApproveProposals: LayerSchedule,
			Approved:             LayerSchedule,
			Interactive:          LayerSchedule,
			KeepRunning:          LayerSchedule,
		},
	}
}

// ComposeSuiteTask is one item of one pass of a suite run: the
// template's content, against the run's own repo and branch unless the
// template is bound to one, landing the way the run says every one of
// its tasks lands.
//
// requireApproval and autoMerge are the run's own snapshot of its
// suite's two switches (SuiteRun's doc comment on why a run holds a
// snapshot), and they beat whatever the template says: "run these
// templates together, against this branch, and let them land" is the
// whole of what a suite is, so a template that auto-merges on its own
// does not auto-merge inside a suite that says not to.
func ComposeSuiteTask(tmpl Template, target RepoRef, base string, requireApproval, autoMerge, autoApproveProposals bool) Composition {
	return composeTemplate(tmpl, nil, target, base, LayerSuite,
		&autoMerge, LayerSuite, &autoApproveProposals, LayerSuite, !requireApproval, LayerSuite)
}

// ComposeTemplateTask is the task somebody files straight from a
// template (grain/task-58): its content, with its input fields filled in
// as values says, against the repo and branch they picked at the form --
// or the template's own, when it is bound to one, since a binding beats
// the caller here exactly as it does at every other firing point.
//
// values is keyed by TemplateField.Name and is what the person or agent
// instantiating the template supplied; a field they left out takes its
// default (Template.FieldValues), so a template can be filed with
// nothing filled in at all and still produce prose rather than braces.
//
// There is no batch: a filing is one task, disposed of the way the
// person filing it asked (approved), and auto-merging the way the
// template says, which is the same "with no batch policy to apply, the
// template's own AutoMerge stands" rule a qualification run gets.
//
// ui.Client.CreateTask files exactly this and
// ui.Client.TemplateComposition shows exactly this, so the preview
// beside the form is the filing's own answer rather than a second guess
// at it.
func ComposeTemplateTask(tmpl Template, values map[string]string, target RepoRef, base string, approved bool) Composition {
	return composeTemplate(tmpl, values, target, base, LayerFiling,
		nil, "", nil, "", approved, LayerFiling)
}

// ComposeQualificationTask is one instance of one item of a
// qualification run: the template's content, pinned to the candidate's
// own repo and branch whatever the template is bound to (the plan
// refuses a template bound elsewhere rather than retargeting it), and
// landing the way the template says, because a plan has an approval
// switch and no auto-merge one.
func ComposeQualificationTask(tmpl Template, repo RepoRef, branch string, requireApproval bool) Composition {
	comp := composeTemplate(tmpl, nil, repo, branch, LayerCandidate,
		nil, "", nil, "", !requireApproval, LayerPlan)
	// Not placementOf's answer: a qualification run is the one caller a
	// binding does not move (Template.Target's own doc comment), so the
	// candidate's repo and branch are restated here over whatever the
	// binding said. CreateQualificationRun has already refused any
	// template bound to another repo, so this only ever re-asserts a
	// branch.
	comp.Target, comp.Base = targetPointer(repo), branch
	comp.Sources.Target, comp.Sources.Base = LayerCandidate, LayerCandidate
	return comp
}

// adjustGrainCIGrant adds the grain-ci grant to grants for a target repo
// that uses grain CI, and strips it from one that does not.
//
// grants is usually a template's or a schedule's own slice, shared with
// every other composition made from the same declaration, so it is never
// appended to in place: an append into spare capacity would write into
// the declaration's backing array.
func adjustGrainCIGrant(grants []Grant, usesGrainCI bool) []Grant {
	if usesGrainCI {
		for _, g := range grants {
			if g.Capability == "grain-ci" {
				return grants
			}
		}
		return append(slices.Clip(grants), Grant{Capability: "grain-ci", Via: GrantByDefault})
	}
	filtered := make([]Grant, 0, len(grants))
	for _, g := range grants {
		if g.Capability != "grain-ci" {
			filtered = append(filtered, g)
		}
	}
	return filtered
}

// applyGrainCIGrant adjusts comp.Grants with adjustGrainCIGrant and records
// LayerRepo as the source of an added grain-ci grant (and as Sources.Grants
// when the declaration itself carried no grants of its own).
func applyGrainCIGrant(comp *Composition, usesGrainCI bool) {
	sources := make([]CompositionLayer, len(comp.Grants))
	for i := range comp.Grants {
		if i < len(comp.GrantSources) && comp.GrantSources[i] != "" {
			sources[i] = comp.GrantSources[i]
		} else if comp.Grants[i].Via == GrantByDefault {
			sources[i] = LayerRepo
		} else {
			sources[i] = comp.Sources.Grants
		}
	}
	if usesGrainCI {
		for _, g := range comp.Grants {
			if g.Capability == "grain-ci" {
				comp.GrantSources = sources
				return
			}
		}
		hadGrants := len(comp.Grants) > 0
		comp.Grants = adjustGrainCIGrant(comp.Grants, true)
		comp.GrantSources = append(sources, LayerRepo)
		if !hadGrants {
			comp.Sources.Grants = LayerRepo
		}
		return
	}
	filteredGrants := make([]Grant, 0, len(comp.Grants))
	filteredSources := make([]CompositionLayer, 0, len(comp.Grants))
	for i, g := range comp.Grants {
		if g.Capability != "grain-ci" {
			filteredGrants = append(filteredGrants, g)
			filteredSources = append(filteredSources, sources[i])
		}
	}
	comp.Grants = filteredGrants
	if len(filteredSources) == 0 {
		comp.GrantSources = nil
	} else {
		comp.GrantSources = filteredSources
	}
}

// composeScheduleTask resolves what one firing of a task-backed schedule
// files: its template, fresh (fireTaskSchedule's own doc comment on why
// never a stale copy), and the values it carries for this deployment's
// declared fields, sifted against those declarations as they stand now.
//
// Store.FireSchedule fires exactly this, and
// ui.Client.ScheduleComposition shows exactly this: one function, so a
// preview cannot promise something a firing would not do.
// The template it fired from comes back alongside, nil for an inline
// schedule: fireTaskSchedule keeps the schedule's own display-cache
// columns in sync with it, and wants the template's values as written
// rather than the sifted ones the task carries.
func (s *Store) composeScheduleTask(ctx context.Context, sched Schedule, stateRepo RepoRef) (Composition, *Template, error) {
	var tmpl *Template
	if sched.TemplateID != nil {
		resolved, err := s.resolveTemplate(ctx, *sched.TemplateID, stateRepo)
		if err != nil {
			return Composition{}, nil, fmt.Errorf("resolving template %s: %w", *sched.TemplateID, err)
		}
		if resolved == nil {
			return Composition{}, nil, fmt.Errorf("template %s no longer exists", *sched.TemplateID)
		}
		tmpl = resolved
	}
	comp := ComposeScheduleTask(sched, tmpl)
	declarations := customFieldDeclarations{q: s.db}
	fields, err := declarations.apply(ctx, comp.CustomFields)
	if err != nil {
		return Composition{}, nil, err
	}
	comp.CustomFields = fields
	if comp.Target != nil {
		usesGrainCI, err := querierRepoUsesGrainCI(ctx, s.db, *comp.Target)
		if err != nil {
			return Composition{}, nil, err
		}
		applyGrainCIGrant(&comp, usesGrainCI)
	}
	return comp, tmpl, nil
}

// composeSuitePass resolves every task one pass of a suite run files, in
// item order -- fireSuitePass files exactly these, and
// ui.Client.SuiteComposition shows exactly these. Every pass of a run
// composes the same way, so one pass is the whole answer for the run.
func composeSuitePass(ctx context.Context, q querier, items []SuiteItem, templates map[string]Template,
	target RepoRef, base string, requireApproval, autoMerge, autoApproveProposals bool) ([]Composition, error) {

	// Read at most once for the whole pass, and not at all for a suite
	// whose templates say nothing about any declared field.
	declarations := customFieldDeclarations{q: q}
	out := make([]Composition, 0, len(items))
	for _, it := range items {
		comp := ComposeSuiteTask(templates[it.TemplateID], target, base, requireApproval, autoMerge, autoApproveProposals)
		fields, err := declarations.apply(ctx, comp.CustomFields)
		if err != nil {
			return nil, err
		}
		comp.CustomFields = fields
		if comp.Target != nil {
			usesGrainCI, err := querierRepoUsesGrainCI(ctx, q, *comp.Target)
			if err != nil {
				return nil, err
			}
			applyGrainCIGrant(&comp, usesGrainCI)
		}
		out = append(out, comp)
	}
	return out, nil
}

// ScheduleComposition is every task sched's next firing files, composed
// -- one task for a schedule that files one, and one per item of the
// suite's next pass for a suite-backed schedule (every pass of the run
// composes identically, so the first pass is the answer for all of
// them).
//
// This writes nothing. It is what a UI asks for to show a human what a
// schedule will actually do before its cadence comes round, and it is
// resolved the same way the firing itself resolves it: templates read
// fresh, custom fields sifted against today's declarations, bindings and
// batch policy applied by the same code.
//
// stateRepo is Store.FireSchedule's own -- only ever read to resolve one
// of grain's own "system:" templates, and ignorable by every deployment
// whose schedules are its operator's.
func (s *Store) ScheduleComposition(ctx context.Context, sched Schedule, stateRepo RepoRef) ([]Composition, error) {
	if sched.SuiteID != nil {
		suite, err := s.GetSuite(ctx, *sched.SuiteID)
		if err != nil {
			return nil, fmt.Errorf("resolving suite %s: %w", *sched.SuiteID, err)
		}
		if suite == nil {
			return nil, fmt.Errorf("suite %s no longer exists", *sched.SuiteID)
		}
		return s.SuiteComposition(ctx, *suite, sched.Target, sched.Base)
	}
	comp, _, err := s.composeScheduleTask(ctx, sched, stateRepo)
	if err != nil {
		return nil, err
	}
	return []Composition{comp}, nil
}

// SuiteComposition is every task one pass of suite files when it is run
// against target and base -- what CreateSuiteRun would file, composed
// without starting a run, so a human can see it before pressing "Run".
func (s *Store) SuiteComposition(ctx context.Context, suite Suite, target RepoRef, base string) ([]Composition, error) {
	templates, err := resolveSuiteTemplates(ctx, s, suite.Items)
	if err != nil {
		return nil, err
	}
	return composeSuitePass(ctx, s.db, suite.Items, templates, target, base,
		suite.RequireApproval, suite.AutoMerge, suite.AutoApproveProposals)
}

// TemplateComposition is the task filing tmpl with values against
// target and base produces -- what ui.Client.CreateTask files, composed
// without filing it so the form can preview it first.
func (s *Store) TemplateComposition(ctx context.Context, tmpl Template, values map[string]string, target RepoRef, base string, approved bool) (Composition, error) {
	comp := ComposeTemplateTask(tmpl, values, target, base, approved || tmpl.Interactive)
	if tmpl.Interactive {
		comp.Sources.Approved = LayerTemplate
	}
	declarations := customFieldDeclarations{q: s.db}
	fields, err := declarations.apply(ctx, comp.CustomFields)
	if err != nil {
		return Composition{}, err
	}
	comp.CustomFields = fields
	if comp.Target != nil {
		usesGrainCI, err := querierRepoUsesGrainCI(ctx, s.db, *comp.Target)
		if err != nil {
			return Composition{}, err
		}
		applyGrainCIGrant(&comp, usesGrainCI)
	}
	return comp, nil
}
