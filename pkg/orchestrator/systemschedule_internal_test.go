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

// The schedule grain ships for itself: that a deployment gets one
// without anybody creating it, that the deployment's own answers about
// it survive every cycle afterwards, that what the row *says* is this
// build's template however it got edited, and that what it eventually
// fires is a task with the state repository to read and nowhere to push.

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

var stateRepo = model.RepoRef{Owner: "bwsalmon", Name: "grain-home"}

// memoryAgentID is the memory agent these tests seed and fire: the
// deployment's own, since memory stopped having teams.
var memoryAgentID = model.MemoryAgentTemplateID

func systemScheduleDeps(store *model.Store) Deps {
	return Deps{Store: store, Config: Config{
		StateRepo: func() model.RepoRef { return stateRepo },
	}}
}

func TestSystemSchedulesSeedsTheMemoryAgentOnAFreshDeployment(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)

	if err := reconcileSystemSchedules(ctx, systemScheduleDeps(store), now); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}

	sched, err := store.GetSchedule(ctx, memoryAgentID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if sched == nil {
		t.Fatal("a fresh deployment has no memory agent schedule; grain ships one every deployment gets")
	}
	if sched.TemplateID == nil || *sched.TemplateID != memoryAgentID {
		t.Errorf("the seeded schedule fires %v, want the system template it was seeded for", sched.TemplateID)
	}
	if !sched.Enabled {
		t.Error("the seeded schedule is disabled; a standing job nobody switches on is one no deployment benefits from")
	}
	want := model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: systemScheduleTimeOfDay}
	if sched.Recurrence != want {
		t.Errorf("cadence = %+v, want %+v", sched.Recurrence, want)
	}
	// Its own next occurrence, never the cycle that seeded it: seeding a
	// schedule at four in the afternoon is not a request to run it then.
	if !sched.NextRunAt.After(now) {
		t.Errorf("first firing at %s, on or before the cycle that seeded it at %s", sched.NextRunAt, now)
	}
	if h := sched.NextRunAt.UTC().Hour(); h != systemScheduleTimeOfDay/60 {
		t.Errorf("first firing at %02d:00, want the cadence's own %02d:00", h, systemScheduleTimeOfDay/60)
	}
	if sched.Target != (model.RepoRef{}) {
		t.Errorf("target = %s, want none: the memory agent writes memory, not a branch", sched.Target)
	}
}

func TestSystemSchedulesSeedsOnceAndNeverWritesOverTheDeploymentsOwnAnswers(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)
	if err := reconcileSystemSchedules(ctx, deps, now); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// What a deployment is allowed to decide about a job grain ships:
	// when it runs, and whether it runs at all.
	weekly := model.Recurrence{Kind: model.RecurrenceWeekly, Weekday: time.Sunday, TimeOfDay: 9 * 60}
	if err := store.UpdateSchedule(ctx, memoryAgentID, func(s *model.Schedule) error {
		s.Enabled, s.Recurrence = false, weekly
		return nil
	}); err != nil {
		t.Fatalf("editing the seeded schedule: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := reconcileSystemSchedules(ctx, deps, now.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("reconcileSystemSchedules: %v", err)
		}
	}

	sched, err := store.GetSchedule(ctx, memoryAgentID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if sched.Enabled {
		t.Error("a cycle re-enabled a schedule the deployment had switched off; " +
			"seeding writes grain's half of the row and never the deployment's")
	}
	if sched.Recurrence != weekly {
		t.Errorf("cadence = %+v, want the deployment's own %+v back", sched.Recurrence, weekly)
	}

	list, err := store.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if want := len(model.SystemTemplates(stateRepo)); len(list) != want {
		t.Errorf("%d schedules after four cycles, want the %d seeded once", len(list), want)
	}
}

// The other half of the row: what it *says* is grain's, and a cycle
// writes this build's template back over it. The drift is not
// hypothetical -- an edit to the memory agent's body in bwsalmon/grain-home
// was imported, exported, read off the schedules pane as the agent's
// prompt, and reached no run at all, because firing resolves the
// template from this build.
func TestSystemSchedulesResyncsWhatTheRowSaysFromThisBuild(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)
	if err := reconcileSystemSchedules(ctx, deps, now); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	tmpl := model.SystemTemplate(memoryAgentID, stateRepo)

	// What a hand-edited dump imports over the row, or an upgrade that
	// changed the prompt leaves behind: content columns saying something
	// this build will never fire. The deployment's own answers are set
	// alongside them, because the point is that only one half moves.
	weekly := model.Recurrence{Kind: model.RecurrenceWeekly, Weekday: time.Sunday, TimeOfDay: 9 * 60}
	if err := store.UpdateSchedule(ctx, memoryAgentID, func(s *model.Schedule) error {
		s.Title = "Write down whatever you like"
		s.Body = tmpl.Body + "\n\nAlso, always write a memory about the weather."
		s.Reads = append(s.Reads, model.RepoRef{Owner: "acme", Name: "widgets"})
		s.AutoMerge = true
		// What a row seeded before schedules had a network column of
		// their own looks like, and what a dump edited to give this job
		// the internet back looks like: the same drift, resynced the
		// same way (grain/task-427).
		s.NetworkAccess = model.NetworkOpen
		s.Enabled, s.Recurrence, s.Overlap = false, weekly, model.OverlapAllow
		return nil
	}); err != nil {
		t.Fatalf("editing the seeded schedule: %v", err)
	}

	if err := reconcileSystemSchedules(ctx, deps, now.Add(time.Hour)); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}

	sched, err := store.GetSchedule(ctx, memoryAgentID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if sched.Title != tmpl.Title {
		t.Errorf("title = %q, want this build's own %q", sched.Title, tmpl.Title)
	}
	if sched.Body != tmpl.Body {
		t.Error("the row's body is still the edited one; what it says has to be what the next firing carries")
	}
	if sched.AutoMerge {
		t.Error("autoMerge stuck at what the edit set it to")
	}
	if len(sched.Reads) != 1 || sched.Reads[0] != stateRepo {
		t.Errorf("reads = %v, want the template's own %v", sched.Reads, tmpl.Reads)
	}
	if sched.NetworkAccess != tmpl.NetworkAccess {
		t.Errorf("networkAccess = %q, want this build's own %q: the row must not say this job "+
			"reaches the internet when the firing gives it no network",
			sched.NetworkAccess, tmpl.NetworkAccess)
	}
	// And the deployment's half of the row is exactly where it was left.
	if sched.Enabled {
		t.Error("the resync re-enabled a schedule the deployment had switched off")
	}
	if sched.Recurrence != weekly {
		t.Errorf("cadence = %+v, want the deployment's own %+v back", sched.Recurrence, weekly)
	}
	if sched.Overlap != model.OverlapAllow {
		t.Errorf("overlap = %q, want the deployment's own %q back", sched.Overlap, model.OverlapAllow)
	}
}

// A row already in step is not written at all: this runs on every cycle
// forever, and a reconciler that rewrote a settings row each time would
// churn the state repository's export with a commit a minute.
func TestSystemSchedulesLeavesARowInStepAlone(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)
	if err := reconcileSystemSchedules(ctx, deps, now); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	seeded, err := store.GetSchedule(ctx, memoryAgentID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	tmpl := model.SystemTemplate(memoryAgentID, stateRepo)
	if drift := systemContentDrift(*seeded, *tmpl); len(drift) != 0 {
		t.Errorf("a freshly seeded row already differs from the template it was seeded from: %v", drift)
	}

	for i := 0; i < 3; i++ {
		if err := reconcileSystemSchedules(ctx, deps, now.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("reconcileSystemSchedules: %v", err)
		}
	}
	after, err := store.GetSchedule(ctx, memoryAgentID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if drift := systemContentDrift(*after, *tmpl); len(drift) != 0 {
		t.Errorf("the row drifted from the template over three cycles: %v", drift)
	}
	if after.NextRunAt != seeded.NextRunAt || after.CreatedAt != seeded.CreatedAt {
		t.Error("a cycle moved the row's own timing while touching nothing else")
	}
}

// The firing itself, through the same store call the reconciler makes:
// what the memory agent's task actually is, resolved from this build
// rather than from any row.
func TestTheMemoryAgentScheduleFilesATaskWithNoTargetAndTheStateRepoToRead(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)
	if err := reconcileSystemSchedules(ctx, deps, now); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	sched, err := store.GetSchedule(ctx, memoryAgentID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}

	firing, err := store.FireSchedule(ctx, *sched, sched.NextRunAt, time.UTC, stateRepo)
	if err != nil {
		t.Fatalf("FireSchedule: %v", err)
	}
	if firing.TaskID == "" {
		t.Fatal("the memory agent's schedule fired nothing")
	}
	task, err := store.GetTask(ctx, firing.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Target != nil {
		t.Errorf("the filed task targets %s; a firing with no repo of its own files a task with none", task.Target)
	}
	if len(task.Reads) != 1 || task.Reads[0] != stateRepo {
		t.Errorf("reads = %v, want this deployment's own state repository %s", task.Reads, stateRepo)
	}
	held := map[string]bool{}
	for _, g := range task.Grants {
		held[g.Capability] = true
	}
	if !held["memory"] || !held["self-debug"] {
		t.Errorf("grants = %v, want memory (to write anything at all) and self-debug", task.Grants)
	}
	if task.Body == "" || task.Title == "" {
		t.Error("the filed task carries no prompt; the whole of the memory agent is what it is told")
	}
}

// The memory agent asks for open networking because it is enabled on the
// shipped host sandbox, which cannot enforce NetworkNone. Asserted on the
// filed task rather than only on the template because the task is what a dispatch actually
// reads: the template saying it buys nothing if the firing drops it on
// the floor, and every other field a template fixes is copied by a line
// of its own in fireTaskSchedule.
func TestTheMemoryAgentScheduleFilesATaskWithOpenNetworkAccess(t *testing.T) {
	t.Parallel()
	tmpl := model.SystemTemplate(memoryAgentID, stateRepo)
	if tmpl == nil {
		t.Fatal("this build ships no memory agent template")
	}
	if tmpl.NetworkAccess != model.NetworkOpen {
		t.Errorf("the template says networkAccess %q, want %q -- the shipped host sandbox cannot enforce none",
			tmpl.NetworkAccess, model.NetworkOpen)
	}

	store, ctx := recreateStore(t)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)
	if err := reconcileSystemSchedules(ctx, systemScheduleDeps(store), now); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	sched, err := store.GetSchedule(ctx, memoryAgentID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	firing, err := store.FireSchedule(ctx, *sched, sched.NextRunAt, time.UTC, stateRepo)
	if err != nil {
		t.Fatalf("FireSchedule: %v", err)
	}
	task, err := store.GetTask(ctx, firing.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.NetworkAccess != model.NetworkOpen {
		t.Errorf("the filed task says networkAccess %q, want %q from the template it fired from",
			task.NetworkAccess, model.NetworkOpen)
	}
}

// A deployment with no state repository is seeded nothing: the memory
// agent's whole evidence base is that repository, and a nightly run that
// can only report it had nothing to read is a queue slot spent on
// nothing. That is also what keeps a test, `grain demo` and anything
// embedding this package from growing a standing job none of them asked
// for.
func TestNothingIsSeededWithoutAStateRepositoryToRead(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)

	for name, deps := range map[string]Deps{
		"nobody wired one":    {Store: store},
		"state is local-only": {Store: store, Config: Config{StateRepo: func() model.RepoRef { return model.RepoRef{} }}},
	} {
		if err := reconcileSystemSchedules(ctx, deps, now); err != nil {
			t.Fatalf("%s: reconcileSystemSchedules: %v", name, err)
		}
		list, err := store.ListSchedules(ctx)
		if err != nil {
			t.Fatalf("ListSchedules: %v", err)
		}
		if len(list) != 0 {
			t.Fatalf("%s: seeded %d schedules, want none", name, len(list))
		}
	}

	// And the moment one is adopted, the next cycle seeds it -- this is a
	// wait, not a decision taken once.
	if err := reconcileSystemSchedules(ctx, systemScheduleDeps(store), now); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}
	if sched, err := store.GetSchedule(ctx, memoryAgentID); err != nil || sched == nil {
		t.Errorf("nothing seeded after a state repository was adopted (err %v)", err)
	}
}

// The template itself still answers for a deployment that has adopted a
// state repository and then gone local-only, whose row is already there:
// it reads nothing, and says so, rather than naming a repository that
// does not exist.
func TestTheMemoryAgentTemplateReadsNothingWithoutAStateRepository(t *testing.T) {
	t.Parallel()
	tmpl := model.SystemTemplate(memoryAgentID, model.RepoRef{})
	if tmpl == nil {
		t.Fatal("this build ships no memory agent template")
	}
	if len(tmpl.Reads) != 0 {
		t.Errorf("reads = %v, want none for a deployment with no state repository", tmpl.Reads)
	}
	if tmpl.Body == "" {
		t.Fatal("the template carries no prompt at all")
	}
}

// The memory agent's prompt names every tool the memory grant actually
// gives it -- which is the whole failure grain/task-345 was filed for:
// the long notes (model.MemoryDocument) shipped while the agent's prompt
// still said update_memory and nothing else, so the one run scheduled to
// keep this deployment's memory true was the one run not told the long
// form existed. A fourth tool on that grant should fail here until
// somebody has decided what the nightly run is to do with it, because a
// tool nothing tells this agent about is a tool this agent will not use.
func TestTheMemoryAgentIsToldAboutEveryMemoryTool(t *testing.T) {
	t.Parallel()
	tmpl := model.SystemTemplate(memoryAgentID, stateRepo)
	if tmpl == nil {
		t.Fatal("this build ships no memory agent template")
	}
	// A nil store returns the tools anyway, refusing every call: this
	// wants their names, not a route to the daemon.
	for _, tool := range []struct{ Name string }{{"grain memory write"}, {"grain memory list"}, {"grain memory read"}} { // the memory tools, as `grain` commands
		if !strings.Contains(tmpl.Body, tool.Name) {
			t.Errorf("the memory agent's prompt never mentions %s, a tool its memory grant hands it", tool.Name)
		}
	}
}

// The per-team memory agents (system:memory-agent:<team>) are retired:
// the deployment's own memory agent replaced them when memory stopped
// having teams. A row an earlier build seeded -- or a state-repository
// import brings back -- is deleted by the next cycle, before the schedule
// reconciler that runs after it could fire it, and the default team's
// row hands its cadence and switch to the deployment's memory agent.
func TestSystemSchedulesRetiresTheTeamMemoryAgents(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)
	deps := systemScheduleDeps(store)

	teamID := model.MemoryAgentTemplateID + ":" + model.DefaultTeamID
	tmpl := model.SystemTemplate(teamID, stateRepo)
	if tmpl == nil {
		t.Fatal("the retired template no longer resolves; a row that fires before the reconciler deletes it would fail")
	}
	weekly := model.Recurrence{Kind: model.RecurrenceWeekly, Weekday: time.Friday, TimeOfDay: 60}
	old := model.Schedule{
		ID:         teamID,
		Recurrence: weekly,
		Enabled:    false,
		NextRunAt:  now.Add(-time.Minute),
		CreatedAt:  now.Add(-24 * time.Hour),
	}
	applySystemContent(&old, *tmpl)
	if err := store.PutSchedule(ctx, old); err != nil {
		t.Fatalf("PutSchedule: %v", err)
	}
	if err := reconcileSystemSchedules(ctx, deps, now); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}
	if sched, err := store.GetSchedule(ctx, teamID); err != nil || sched != nil {
		t.Fatalf("%s is still there after a cycle (err %v)", teamID, err)
	}
	sched, err := store.GetSchedule(ctx, model.MemoryAgentTemplateID)
	if err != nil || sched == nil {
		t.Fatalf("%s was not seeded (err %v)", model.MemoryAgentTemplateID, err)
	}
	if sched.Enabled || sched.Recurrence != weekly {
		t.Errorf("%s seeded enabled=%v cadence=%+v, want the default team's agent's switch and cadence carried over",
			model.MemoryAgentTemplateID, sched.Enabled, sched.Recurrence)
	}

	// Another team's row comes back from an import and goes again.
	other := old
	other.ID = model.MemoryAgentTemplateID + ":alpha"
	if err := store.PutSchedule(ctx, other); err != nil {
		t.Fatalf("PutSchedule: %v", err)
	}
	if err := reconcileSystemSchedules(ctx, deps, now); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}
	if sched, err := store.GetSchedule(ctx, other.ID); err != nil || sched != nil {
		t.Fatalf("%s is still there after a cycle (err %v)", other.ID, err)
	}
	if sched, err := store.GetSchedule(ctx, model.MemoryAgentTemplateID); err != nil || sched == nil {
		t.Errorf("%s is gone too (err %v)", model.MemoryAgentTemplateID, err)
	}
}

// Seeding is what a cycle does, not what a test calls directly, so this
// is the one that would catch it being left out of the order.
func TestSystemSchedulesIsInTheCycleBeforeSchedule(t *testing.T) {
	t.Parallel()
	var system, schedule = -1, -1
	for i, r := range Reconcilers() {
		switch r.Name {
		case "system-schedules":
			system = i
		case "schedule":
			schedule = i
		}
	}
	if system < 0 {
		t.Fatal("no cycle seeds the schedules grain ships, so no deployment ever gets one")
	}
	if schedule < 0 || system > schedule {
		t.Errorf("system-schedules runs at %d and schedule at %d; a row seeded this cycle should be considered this cycle", system, schedule)
	}
}

// The second standing job grain ships, and the one whose cadence is not
// the memory agent's: release notes are about a week, so the schedule
// seeded for them fires weekly, on the Monday after the week it reports
// on (model.SystemScheduleRecurrence).
func TestSystemSchedulesSeedsTheReleaseNotesJobWeekly(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)

	if err := reconcileSystemSchedules(ctx, systemScheduleDeps(store), now); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}

	sched, err := store.GetSchedule(ctx, model.ReleaseNotesTemplateID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if sched == nil {
		t.Fatal("a fresh deployment has no release-notes schedule; grain ships one every deployment gets")
	}
	want := model.Recurrence{
		Kind: model.RecurrenceWeekly, Weekday: time.Monday, TimeOfDay: systemScheduleTimeOfDay,
	}
	if sched.Recurrence != want {
		t.Errorf("cadence = %+v, want %+v", sched.Recurrence, want)
	}
	if !sched.Enabled {
		t.Error("the seeded schedule is disabled; a standing job nobody switches on is one no deployment benefits from")
	}
	if sched.NextRunAt.Weekday() != time.Monday || !sched.NextRunAt.After(now) {
		t.Errorf("first firing %s, want the next Monday after the cycle that seeded it", sched.NextRunAt)
	}
	if sched.Target != (model.RepoRef{}) {
		t.Errorf("target = %s, want none: this job publishes a row, not a branch", sched.Target)
	}
	// What it fires has to be able to count and to publish, and to read
	// the record it is writing about.
	tmpl := model.SystemTemplate(model.ReleaseNotesTemplateID, stateRepo)
	if tmpl == nil {
		t.Fatal("this build ships no release-notes template for the schedule it seeds")
	}
	granted := map[string]bool{}
	for _, g := range tmpl.Grants {
		granted[g.Capability] = true
	}
	for _, want := range []string{"release-notes", "self-debug"} {
		if !granted[want] {
			t.Errorf("the release-notes template does not hold %q; its grants are %+v", want, tmpl.Grants)
		}
	}
	if granted["memory"] {
		t.Error("the release-notes job holds the memory grant: a week's report must not be " +
			"filable as a standing instruction to every run this deployment dispatches")
	}
	if len(tmpl.Reads) != 1 || tmpl.Reads[0] != stateRepo {
		t.Errorf("reads = %+v, want the state repository, where the weeks already written are", tmpl.Reads)
	}
	// The tools it is told to call have to be the ones it is given.
	for _, name := range []string{"grain release-notes stats", "grain release-notes publish"} {
		if !strings.Contains(tmpl.Body, name) {
			t.Errorf("the prompt never mentions %s, which is the tool this job exists to call", name)
		}
	}
}

// The third standing job grain ships, and the one whose cadence is the
// memory agent's rather than the release notes': the infrastructure
// review reads a night that has finished, and it reads series that are
// swept and, for the host, lost on every daemon restart -- so it looks
// every night even though the cooldown in its own prompt means it will
// rarely recommend anything two nights running
// (model.infraReviewTemplate's "why nightly").
func TestSystemSchedulesSeedsTheInfraReviewNightly(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)

	if err := reconcileSystemSchedules(ctx, systemScheduleDeps(store), now); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}

	sched, err := store.GetSchedule(ctx, model.InfraReviewTemplateID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if sched == nil {
		t.Fatal("a fresh deployment has no infrastructure review schedule; grain ships one every deployment gets")
	}
	want := model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: systemScheduleTimeOfDay}
	if sched.Recurrence != want {
		t.Errorf("cadence = %+v, want %+v", sched.Recurrence, want)
	}
	if !sched.Enabled {
		t.Error("the seeded schedule is disabled; a standing job nobody switches on is one no deployment benefits from")
	}
	// Skip rather than allow, and it matters more here than for the
	// other two: two of these running at once would each read the
	// other's open proposal as un-acted-on and re-file it.
	if sched.Overlap != model.OverlapSkip {
		t.Errorf("overlap = %q, want %q", sched.Overlap, model.OverlapSkip)
	}
	if sched.Target != (model.RepoRef{}) {
		t.Errorf("target = %s, want none: this job writes a report and files proposals, and "+
			"the repository it would otherwise push to is the one holding the settings it "+
			"reviews", sched.Target)
	}

	tmpl := model.SystemTemplate(model.InfraReviewTemplateID, stateRepo)
	if tmpl == nil {
		t.Fatal("this build ships no infrastructure review template for the schedule it seeds")
	}
	granted := map[string]bool{}
	for _, g := range tmpl.Grants {
		granted[g.Capability] = true
	}
	// It reads the record, the measurements and grain's own source
	// through one grant, and the deployment's CI runs through the other.
	for _, want := range []string{"self-debug", "grain-ci"} {
		if !granted[want] {
			t.Errorf("the infrastructure review template does not hold %q; its grants are %+v",
				want, tmpl.Grants)
		}
	}
	// And nothing that can change anything. self-repair is the exact
	// counterpart capability, and a job whose whole value is that a
	// person sees the reasoning first must not hold it.
	if granted["self-repair"] {
		t.Error("the infrastructure review holds self-repair: the one job that exists to " +
			"suggest changes to this deployment must not be able to make one")
	}
	if len(tmpl.Reads) != 1 || tmpl.Reads[0] != stateRepo {
		t.Errorf("reads = %+v, want the state repository, where every parameter it may "+
			"suggest changing is exported", tmpl.Reads)
	}
	// The tool it is told to read the numbers with has to be one this
	// grant actually hands it. A nil reader returns it anyway, refusing
	// every call: this wants the name.
	for _, tool := range []struct{ Name string }{{"grain metrics"}} { // read_grain_metrics, as a `grain` command
		if !strings.Contains(tmpl.Body, tool.Name) {
			t.Errorf("the prompt never mentions %s, which is the whole of what this job "+
				"reads before it recommends anything", tool.Name)
		}
	}
}

// A system job may be seeded with a minutes cadence
// (model.SystemScheduleRecurrence can return everyNMinutes): the row
// keeps it, its first firing is that many minutes after the seeding
// cycle rather than at the quiet hour, and the reconciler fires it on
// that cadence from then on.
func TestSystemScheduleSeedsAnEveryNMinutesCadence(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	now := time.Date(2026, 3, 1, 16, 7, 0, 0, time.UTC)
	tmpl := model.SystemTemplates(stateRepo)[0]
	every := model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 30}
	if err := every.Validate(); err != nil {
		t.Fatalf("a thirty-minute cadence does not validate: %v", err)
	}

	if err := seedSystemSchedule(ctx, deps, tmpl, every, now, time.UTC); err != nil {
		t.Fatalf("seedSystemSchedule: %v", err)
	}
	sched, err := store.GetSchedule(ctx, tmpl.ID)
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule: %v %v", sched, err)
	}
	if sched.Recurrence != every {
		t.Errorf("cadence = %+v, want %+v", sched.Recurrence, every)
	}
	if want := now.Add(30 * time.Minute); !sched.NextRunAt.Equal(want) {
		t.Errorf("first firing %s, want %s: thirty minutes after the seeding cycle", sched.NextRunAt, want)
	}

	// A later cycle's resync leaves the cadence alone, whatever the
	// seeder would have chosen.
	if err := seedSystemSchedule(ctx, deps, tmpl, model.SystemScheduleRecurrence(tmpl.ID, systemScheduleTimeOfDay),
		now.Add(time.Minute), time.UTC); err != nil {
		t.Fatalf("resyncing: %v", err)
	}
	if again, err := store.GetSchedule(ctx, tmpl.ID); err != nil || again.Recurrence != every {
		t.Errorf("after a resync the cadence is %+v (%v), want still %+v", again.Recurrence, err, every)
	}
}
