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
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// systemScheduleTimeOfDay is when a schedule grain seeds for itself
// first comes due: 03:00 UTC (the universal default clock for schedules).
//
// The middle of the night because the memory agent is the one standing
// job that reads the *record* rather than the code: it wants a day that
// has finished, and it wants the workers it is competing with for a
// sandbox to be idle. It is a default, not a rule -- the cadence is one
// of the three things a deployment may edit on a system schedule
// (ui.refuseSystemScheduleContentEdit), so a deployment whose people
// work nights can move it.
const systemScheduleTimeOfDay = 3 * 60

// reconcileSystemSchedules makes sure a deployment with a state
// repository has a schedule row for every standing job grain ships
// (model.SystemTemplates) -- today, the nightly memory agent, the weekly
// release notes and the nightly infrastructure review -- and that none is
// left for a job it has stopped shipping (retireSystemSchedules).
//
// **It never writes over an answer the deployment gave, and always
// writes grain's own half.** That split is the whole design, and it runs
// down the middle of the row. The template is grain's
// (model.SystemTemplate's own doc comment on why the prompt is code and
// not a store row), so the content columns the row carries are a cache
// of it and are resynced from this build whenever they have drifted
// (resyncSystemSchedule). The schedule is the deployment's: how often it
// runs, whether firings may overlap, and whether it runs at all are
// answers only this deployment can give, and once it has given one
// nothing here may take it back. So those columns are written exactly
// once, on the first cycle a deployment runs without the row, and are
// settings like any other from then on -- exported to the state
// repository, editable in the UI within the bounds
// ui.refuseSystemScheduleContentEdit sets, and disabled rather than
// deleted by somebody who wants it to stop (ui.Client.DeleteSchedule
// refuses to remove one, because a deletion is exactly what this pass
// would undo on the next tick).
//
// Level-triggered like every other reconciler, and idempotent for the
// same reason: the row's id is the system template's own
// (model.InfraReviewTemplateID) rather than a `sched-N` from the
// sequence, so "does this deployment already have it" is a lookup by a
// known key rather than a search for something that looks like it. A
// deployment that has run this before finds the row and does nothing;
// one restored from a state repository dump finds the row that came back
// with it.
//
// It runs before "schedule" in Reconcilers() so a row seeded on this
// cycle is considered for firing on this same cycle -- which costs
// nothing in practice (a fresh schedule's first firing is its cadence's
// next occurrence, never now: model.Schedule.NextRunAt's own rule) and
// keeps the two in the order the reader expects.
func reconcileSystemSchedules(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	// Nothing is seeded into a deployment with no state repository to
	// name (Config.StateRepo nil, or answering with the zero RepoRef).
	// That is not a technicality about wiring: the state repository is
	// grain's own database as text, and it is the whole of what the
	// memory agent has to read. A deployment without one would get a
	// nightly run that can only report that it had nothing to look at,
	// and a queue slot spent on it every night. reconcileUpgradeCheck
	// takes the same shape for the same reason -- it files nothing for a
	// caller that wired no upgrader -- and it is what keeps `grain demo`,
	// a test and anything embedding this package from growing a standing
	// job none of them asked for.
	//
	// Level-triggered, so this is a wait rather than a decision: a
	// deployment that adopts a state repository later is seeded on the
	// next cycle after it does.
	stateRepo := deps.Config.settingsRepo()
	if stateRepo.Owner == "" {
		return nil
	}
	// Seeded before the retired rows go, so the memory agent's row can
	// take over the default team's agent's cadence (seedSystemSchedule).
	var errs []error
	for _, tmpl := range model.SystemTemplates(stateRepo) {
		recurrence := model.SystemScheduleRecurrence(tmpl.ID, systemScheduleTimeOfDay)
		if err := seedSystemSchedule(ctx, deps, tmpl, recurrence, now, time.UTC); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reconciling the schedule for %s: %w", tmpl.ID, err))
		}
	}
	if err := retireSystemSchedules(ctx, deps); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// retireSystemSchedules deletes every row an earlier build seeded for a
// standing job this one no longer ships (model.RetiredSystemScheduleID):
// today, the per-team memory agents (system:memory-agent:<team>), which
// the deployment's own memory agent replaced when memory stopped having
// teams.
//
// Every cycle rather than once, for the same reason seeding is: the row
// is a settings row, so a state repository that still carries it puts
// it back on the next import. This pass runs before "schedule" in
// Reconcilers(), so a returned row is deleted before it can fire, and
// the export after it writes the deletion back out to the dump.
//
// Whatever the row under that id says is deleted, not only a row that
// still names the retired template: the `system:` namespace is grain's
// (model.SystemTemplatePrefix), so nothing a deployment made can be
// standing on the id.
func retireSystemSchedules(ctx context.Context, deps Deps) error {
	scheds, err := deps.Store.ListSchedules(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing schedules to retire: %w", err)
	}
	var errs []error
	for _, sched := range scheds {
		if !model.RetiredSystemScheduleID(sched.ID) {
			continue
		}
		if err := deps.Store.DeleteSchedule(ctx, sched.ID); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: retiring the schedule %s: %w", sched.ID, err))
			continue
		}
		log.Printf("orchestrator: deleted schedule %s: this build no longer ships the job it fires; "+
			"if it came back from the state repository, remove it from settings/schedules.yml there "+
			"(tables/schedule.json in an export older than the settings files)", sched.ID)
	}
	return errors.Join(errs...)
}

// seedSystemSchedule writes the schedule for one system template, if it
// is not already there, and otherwise resyncs the half of the row that
// is grain's (resyncSystemSchedule). recurrence is the cadence a fresh
// row is seeded with -- the caller's model.SystemScheduleRecurrence --
// and is not read at all for a row that is already there.
//
// The content columns are seeded from the template as the same display
// cache every template-backed schedule carries
// (model.Schedule.TemplateID's own doc comment): what the schedules list
// shows without a second lookup. They are not what fires -- the template
// is re-resolved from this build on every firing
// (model.Store.resolveTemplate) -- so an edit to them, in the UI or in
// the state repository's own dump, changes what the row *says* and never
// what it *does*. That is the property that makes shipping the prompt in
// code worth anything, and it is exactly why the cache is not allowed to
// go on saying something this build will never fire.
func seedSystemSchedule(ctx context.Context, deps Deps, tmpl model.Template,
	recurrence model.Recurrence, now time.Time, loc *time.Location) error {

	existing, err := deps.Store.GetSchedule(ctx, tmpl.ID)
	if err != nil {
		return err
	}
	if existing != nil {
		return resyncSystemSchedule(ctx, deps, *existing, tmpl)
	}
	// The cadence is the job's own (model.SystemScheduleRecurrence):
	// nightly for the memory agent, which reads a day that has finished,
	// and weekly for the release notes, which are about a week. Seeded
	// once, here, and the deployment's from then on --
	// resyncSystemSchedule never touches it again.
	sched := model.Schedule{
		ID:         tmpl.ID,
		Recurrence: recurrence,
		Overlap:    model.OverlapSkip,
		// Enabled, because a job that has to be switched on by hand is
		// one no deployment ever gets the benefit of. Switching it off is
		// one edit away and is remembered from then on -- the resync that
		// runs against a row that exists writes grain's own columns and
		// never this one.
		Enabled: true,
		// Its cadence's next occurrence, never now: creating a schedule
		// is not a request to run it (ui.Client.CreateSchedule's own doc
		// comment), and a deployment that started at four in the
		// afternoon should not spend its first cycle dispatching a
		// memory agent over a record of nothing.
		NextRunAt: recurrence.Next(now, loc),
		CreatedAt: now,
	}
	if tmpl.ID == model.MemoryAgentTemplateID {
		// The default team's memory agent (system:memory-agent:default)
		// is the job this row replaces on a deployment that ran one, and
		// its cadence, overlap, switch and model were the deployment's
		// answers: they carry over rather than being asked again.
		prev, err := deps.Store.GetSchedule(ctx, model.MemoryAgentTemplateID+":"+model.DefaultTeamID)
		if err != nil {
			return err
		}
		if prev != nil {
			sched.Recurrence, sched.Overlap, sched.Enabled = prev.Recurrence, prev.Overlap, prev.Enabled
			sched.NextRunAt, sched.Model = prev.NextRunAt, prev.Model
		}
	}
	applySystemContent(&sched, tmpl)
	if err := deps.Store.PutSchedule(ctx, sched); err != nil {
		return err
	}
	log.Printf("orchestrator: seeded schedule %s (%s), first due %s",
		sched.ID, tmpl.Name, sched.NextRunAt.In(loc).Format(time.RFC3339))
	return nil
}

// resyncSystemSchedule writes this build's template back over the
// content columns of a system schedule that already exists, and leaves
// every other column exactly where the deployment left it.
//
// The row's content is a cache of a template that lives in code, and a
// cache that can say something the agent is never told is worse than no
// cache at all. Two things put it out of date, neither of them an edit
// this deployment was allowed to make:
//
//   - An upgrade. Every release that changes a system prompt leaves the
//     cached copy on every existing deployment saying what the previous
//     release said, because the row is already there and seeding only
//     ever wrote a missing one.
//   - The state repository. ui.refuseSystemScheduleContentEdit closes
//     the API against a content edit, but a `schedule` row is a settings
//     row, so a hand-edited dump imports over the columns
//     (staterepo.SettingsTables) with nothing to refuse it.
//
// The failure both produce is the same and it has happened: a paragraph
// of guidance added to the memory agent's `body` in bwsalmon/grain-home
// was imported, exported, shown on the schedules pane and read by a
// later task as the memory agent's prompt, and reached no run at all,
// because firing resolves the template from this build
// (model.Store.fireTaskSchedule). Resyncing here makes the row say what
// the next firing will actually carry -- on the pane, in the API, and in
// the dump the next export writes.
//
// It is the same set of columns fireTaskSchedule refreshes as it fires,
// on the cycle rather than on the cadence: a schedule somebody disabled
// still tells the truth about the job it is not running, and one whose
// prompt changed in an upgrade tells it from the first cycle after the
// upgrade rather than after the next firing.
func resyncSystemSchedule(ctx context.Context, deps Deps, sched model.Schedule, tmpl model.Template) error {
	drifted := systemContentDrift(sched, tmpl)
	if len(drifted) == 0 {
		return nil
	}
	if err := deps.Store.UpdateSchedule(ctx, sched.ID, func(s *model.Schedule) error {
		applySystemContent(s, tmpl)
		return nil
	}); err != nil {
		return err
	}
	// Said out loud, and naming the columns, because from the outside
	// this is an edit somebody made going away again: whoever changed
	// the body in the state repository is owed the reason in the journal
	// rather than a row that quietly reverts.
	//
	// This journal is not the only place it is said, and it is not the
	// place the person who caused it is reading. The other end of the
	// same story is written where the edit arrived: an import that
	// changes one of these rows is announced on the pull request the
	// merge came in on, with the sentence that says the change belongs
	// in a pull request against grain instead
	// (staterepo's systemschedules.go). That is the notice; this is the
	// record of the repair actually happening.
	log.Printf("orchestrator: schedule %s (%s) said something this build does not fire (%s); "+
		"resynced from the template grain ships",
		sched.ID, tmpl.Name, strings.Join(drifted, ", "))
	return nil
}

// applySystemContent copies tmpl's content onto sched, leaving the
// deployment's own columns -- cadence, overlap, the enabled switch, the
// firing times and the model -- untouched. The model is the
// deployment's choice for this row rather than a copy of the template's
// (model.Schedule.Model): left empty, a firing takes the template's own
// (model.ComposeScheduleTask), so there is nothing to copy and nothing
// to drift. The fields are the ones
// model.Store.fireTaskSchedule writes back through its own display
// cache, plus TemplateID, which is what makes the row fire the template
// at all.
func applySystemContent(sched *model.Schedule, tmpl model.Template) {
	id := tmpl.ID
	sched.TemplateID = &id
	sched.Title, sched.Body = tmpl.FilledTitleBody()
	sched.AutoMerge = tmpl.AutoMerge
	sched.Reads, sched.Writes, sched.Grants = tmpl.Reads, tmpl.Writes, tmpl.Grants
	sched.CustomFields, sched.NetworkAccess = tmpl.CustomFields, tmpl.NetworkAccess
	sched.Target, sched.Base = targetOf(tmpl), tmpl.Base
}

// systemContentDrift names the content columns of sched that say
// something other than what tmpl does -- empty for a row already in
// step, which is what every cycle on a deployment nobody has edited
// finds, so the ordinary case costs a comparison and no write.
func systemContentDrift(sched model.Schedule, tmpl model.Template) []string {
	var drifted []string
	note := func(same bool, column string) {
		if !same {
			drifted = append(drifted, column)
		}
	}
	note(sched.TemplateID != nil && *sched.TemplateID == tmpl.ID, "template")
	title, body := tmpl.FilledTitleBody()
	note(sched.Title == title, "title")
	note(sched.Body == body, "body")
	note(sched.AutoMerge == tmpl.AutoMerge, "autoMerge")
	note(sameContent(sched.Reads, tmpl.Reads), "reads")
	note(sameContent(sched.Writes, tmpl.Writes), "writes")
	note(sameGrants(sched.Grants, tmpl.Grants), "grants")
	note(sameContent(sched.CustomFields, tmpl.CustomFields), "customFields")
	// A row seeded before a schedule had a network column of its own
	// (grain/task-427) says nothing here while the job it fires runs
	// with none, so the first cycle after that upgrade resyncs it -- the
	// ordinary case this function exists for.
	note(sched.NetworkAccess == tmpl.NetworkAccess, "networkAccess")
	note(sched.Target == targetOf(tmpl), "repo")
	note(sched.Base == tmpl.Base, "base")
	return drifted
}

// sameContent compares two of the list- or map-valued content columns,
// counting an empty one and an absent one as the same thing: a template
// carrying no reads and a row whose `schedule_read` table has no lines
// for it say the same thing, and the store hands back nil where the
// template holds an empty slice often enough that a stricter comparison
// would rewrite the row on every cycle forever.
func sameContent[T any](stored, want T) bool {
	if reflect.ValueOf(stored).Len() == 0 && reflect.ValueOf(want).Len() == 0 {
		return true
	}
	return reflect.DeepEqual(stored, want)
}

func sameGrants(stored, want []model.Grant) bool {
	if len(stored) == 0 && len(want) == 0 {
		return true
	}
	if len(stored) != len(want) {
		return false
	}
	s := append([]model.Grant(nil), stored...)
	w := append([]model.Grant(nil), want...)
	sort.Slice(s, func(i, j int) bool { return s[i].Capability < s[j].Capability })
	sort.Slice(w, func(i, j int) bool { return w[i].Capability < w[j].Capability })
	return reflect.DeepEqual(s, w)
}

// targetOf is the repo a system template binds its firings to, or the
// zero RepoRef for one that binds none -- which every system template
// so far is, since what they produce is a comment or a store write
// rather than a branch. A schedule with the zero target files a task
// with no target at all (model.Store.fireTaskSchedule).
func targetOf(tmpl model.Template) model.RepoRef {
	if tmpl.Target == nil {
		return model.RepoRef{}
	}
	return *tmpl.Target
}
