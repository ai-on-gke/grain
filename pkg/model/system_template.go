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
	"fmt"
	"strings"
	"time"
)

// SystemTemplatePrefix marks a template id that names one of grain's
// own templates rather than a row in this deployment's `template` table
// -- "system:memory-agent" and, in time, whatever else grain ships a
// standing job for.
//
// Store.NewTemplateID hands out "template-N", so the two namespaces
// cannot collide, and nothing writes a `template` row with an id in this
// one: a system template is code (SystemTemplate below), resolved from
// this build every time it fires.
//
// The point of the prefix is that a schedule can name a template that is
// not in the store. Everything that resolves a template id therefore has
// to ask this build first and the store second -- Store.resolveTemplate
// is where that happens for a firing, and it is the only place that
// matters, because a template id only ever means anything at the moment
// something fires from it.
const SystemTemplatePrefix = "system:"

// MemoryAgentTemplateID names the memory agent: the nightly run that
// reads what this deployment has been doing and writes its memory
// (Memory, memory.go) -- the deployment's own layer and each repo's.
//
// It was retired once (grain/task-3544) in favour of one memory agent per
// team, the default team's included, each writing its team's layers.
// Teams are gone (single-tenant reset, stage 4d): the team layers folded
// into the deployment's and the repos', the
// per-team agents' schedules are retired (RetiredSystemScheduleID), and
// this one is seeded again (SystemTemplates).
//
// The id is also the id of the schedule grain seeds for it: one
// well-known id for the standing job, rather than a `sched-N` from the
// sequence that would differ between deployments and could not be
// seeded idempotently -- the reason every system schedule has one.
const MemoryAgentTemplateID = SystemTemplatePrefix + "memory-agent"

// retiredTeamMemoryAgentPrefix is the id family of the per-team memory
// agents an earlier build seeded, one per team
// ("system:memory-agent:<team>"). SystemTemplate resolves every one of
// them to the deployment's memory agent, so a row that fires before the
// reconciler deletes it -- or a task already filed from one -- runs
// rather than failing on a template that is gone.
const retiredTeamMemoryAgentPrefix = MemoryAgentTemplateID + ":"

// RetiredSystemScheduleID reports whether id names a system schedule an
// earlier build seeded and this one no longer ships.
// orchestrator.reconcileSystemSchedules deletes each on every cycle that
// finds it, so a row that comes back in a state-repository import is gone
// again before the schedule reconciler can fire it, and the export after
// that writes its removal out.
//
// Deleted rather than disabled: ui.Client.DeleteSchedule refuses a
// system row, so a disabled one would sit in every deployment's
// schedules list for good, switchable back on and firing a job nobody
// maintains.
func RetiredSystemScheduleID(id string) bool {
	return strings.HasPrefix(id, retiredTeamMemoryAgentPrefix)
}

// ReleaseNotesTemplateID names the release-notes writer: the weekly run
// that reads how much work this deployment actually did and writes the
// week up for a person (ReleaseNote, release_note.go).
//
// It is also the id of the schedule grain seeds for it
// (orchestrator.reconcileSystemSchedules), for the reason every system
// schedule has one: one well-known id for the standing job, rather than a
// `sched-N` from the sequence that would differ between deployments and
// could not be seeded idempotently.
const ReleaseNotesTemplateID = SystemTemplatePrefix + "release-notes"

// InfraReviewTemplateID names the infrastructure review: the nightly run
// that reads how this deployment's own machinery actually performed and
// suggests which of its parameters to move -- and never moves one
// (infraReviewTemplate).
//
// It is also the id of the schedule grain seeds for it
// (orchestrator.reconcileSystemSchedules), for the reason every system
// schedule has one: one well-known id for the standing job, rather than a
// `sched-N` from the sequence that would differ between deployments and
// could not be seeded idempotently.
const InfraReviewTemplateID = SystemTemplatePrefix + "infra-review"

// PipelineStewardTemplateID names the pipeline steward: the standing job
// that keeps the merge pipeline moving -- files the repair for a red
// base branch, holds the work that keeps failing against it, retriggers
// flakes and unsticks what is stalled (pipelineStewardTemplate).
//
// Unlike the other system jobs it is one task rather than one per
// firing (IsStandingTemplateID): every wake re-queues the same task, so
// what it filed and who it told to wait is in its own handover rather
// than scattered across a task per half hour. It is also the id of the
// schedule grain seeds for it, for the reason every system schedule has
// one.
const PipelineStewardTemplateID = SystemTemplatePrefix + "pipeline-steward"

// BaseRepairTemplateID names the default built-in system template for base branch repairs and release triages.
const BaseRepairTemplateID = SystemTemplatePrefix + "base-repair"

// IsSystemTemplateID reports whether id names one of grain's own
// templates -- what pkg/ui checks before letting a human edit or delete
// the schedule that fires one.
func IsSystemTemplateID(id string) bool { return strings.HasPrefix(id, SystemTemplatePrefix) }

// SystemTemplate is this build's template for id, or nil for an id that
// is not one of grain's own.
//
// stateRepo is this deployment's own state repository
// (orchestrator.Config.StateRepo, ui.Config.StateRepoRef): the one thing
// about a system template that is not the same on every deployment, and
// the reason this is a function of the deployment rather than a package
// variable. A deployment whose state is local-only passes the zero
// RepoRef and gets a template that reads nothing -- an honest answer
// rather than a template pointing at a repository that does not exist.
//
// Why these live in code at all, rather than as a `template` row grain
// seeds on first run the way it seeds the schedule: what a system
// template says is grain's, not the deployment's. The memory agent's
// prompt decides what every later run of this deployment is told it has
// learned, which makes it the one piece of prompt text a deployment must
// not be able to quietly rewrite for itself -- a run holding the memory
// capability could otherwise widen its own standing instructions by
// editing the template that writes them. Keeping it here means the
// prompt is reviewed where the rest of grain's prompts are reviewed, in
// a pull request against this repository, and that upgrading grain
// upgrades it.
//
// What a deployment does get to decide is on the schedule row, not here:
// how often it runs, whether firings may overlap, and whether it runs at
// all (ui.refuseSystemScheduleContentEdit).
func SystemTemplate(id string, stateRepo RepoRef) *Template {
	switch id {
	case MemoryAgentTemplateID:
		return memoryAgentTemplate(stateRepo)
	case ReleaseNotesTemplateID:
		return releaseNotesTemplate(stateRepo)
	case InfraReviewTemplateID:
		return infraReviewTemplate(stateRepo)
	case PipelineStewardTemplateID:
		return pipelineStewardTemplate(stateRepo)
	case StewardAuditTemplateID:
		return stewardAuditTemplate(stateRepo)
	case BaseRepairTemplateID:
		return baseRepairTemplate(stateRepo)
	case SecurityReviewTemplateID:
		return securityReviewTemplate()
	case InvestigateNotificationTemplateID:
		return investigateNotificationTemplate()
	case GKESandboxClusterTemplateID:
		return gkeSandboxClusterTemplate()
	}
	if RetiredSystemScheduleID(id) {
		return memoryAgentTemplate(stateRepo)
	}
	// The one system template that is a family rather than a single job:
	// one per scheduled workflow, named in the id
	// (WorkflowReviewTemplatePrefix).
	if stem, ok := WorkflowReviewStem(id); ok {
		return workflowReviewTemplate(stem)
	}
	return nil
}

// SystemTemplates is every template this build ships that grain seeds a
// schedule for, resolved against stateRepo -- what the schedule seeder
// walks, and what a test that wants to assert something about all of them
// iterates.
//
// The workflow-review family (WorkflowReviewTemplatePrefix) is deliberately
// not in it and cannot be: there is one per scheduled workflow of every
// repository this deployment reads declarations from, and nothing seeds a
// schedule for one -- the repository's own workflow file is what creates
// it. SystemTemplate resolves them all the same.
func SystemTemplates(stateRepo RepoRef) []Template {
	return []Template{
		*memoryAgentTemplate(stateRepo),
		*releaseNotesTemplate(stateRepo),
		*infraReviewTemplate(stateRepo),
		*pipelineStewardTemplate(stateRepo),
		*stewardAuditTemplate(stateRepo),
	}
}

// SystemScheduleRecurrence is the cadence grain seeds the schedule for
// one of its own templates with -- and only the first time, since the
// cadence is one of the three columns a deployment owns from then on
// (orchestrator.seedSystemSchedule, ui.refuseSystemScheduleContentEdit).
//
// It is here rather than in the seeder because it is a property of the
// job and not of the seeding: the memory agent reads a day that has
// finished, so it runs every night; release notes are about a week, so
// they are written once a week and there is no sensible daily version of
// them. A template this build does not ship a cadence for gets the
// nightly default, which is what every system template had before this
// function existed -- and which the infrastructure review takes
// deliberately rather than by omission (infraReviewTemplate's own doc
// comment has the argument for nightly over weekly, which is not the
// same argument the memory agent makes).
//
// Any kind Recurrence.Validate accepts may be returned, everyNMinutes
// included: a job that has to wake every half hour (a release manager
// watching its queue) is seeded with that cadence the same way, and its
// first firing is that many minutes after the cycle that seeds it.
//
// timeOfDay is the deployment's own "middle of the night" in minutes
// since midnight, passed in by the seeder (orchestrator's
// systemScheduleTimeOfDay) so that grain's standing jobs all start at
// the same quiet hour without this package having an opinion about which
// hour that is.
func SystemScheduleRecurrence(id string, timeOfDay int) Recurrence {
	switch id {
	case ReleaseNotesTemplateID, StewardAuditTemplateID:
		// Monday, because a week's notes are written about a week that
		// has finished: fired at the quiet hour on a Monday, the run is
		// looking back over a Monday-to-Sunday ISO week that closed a
		// few hours earlier, which is exactly the period the notes are
		// filed under (ReleaseNotePeriod).
		return Recurrence{Kind: RecurrenceWeekly, Weekday: time.Monday, TimeOfDay: timeOfDay}
	case PipelineStewardTemplateID:
		// Every six hours, at no particular hour: a safety wake. The
		// steward is woken by the pipeline.* notifications it subscribes
		// to, and a confirmed red does not wait for a firing at all
		// (orchestrator.wakePipelineSteward). Most of these firings cost
		// nothing -- the daemon skips one when there is nothing wrong to
		// look at (orchestrator.pipelineStewardQuiet).
		return Recurrence{Kind: RecurrenceEveryNMinutes, EveryNMinutes: PipelineStewardEveryNMinutes}
	}
	return Recurrence{Kind: RecurrenceDaily, TimeOfDay: timeOfDay}
}

// memoryAgentTemplate is the memory agent itself.
//
// The shape of the task is the whole of what this feature is, so each
// piece of it is a decision -- the same accounting
// orchestrator.fileUpgradeCheck makes for the other task grain files on
// its own behalf:
//
//   - No Target. The memory agent has no change to push. What it
//     produces is rows in the `memory` table, written through the
//     update_memory tool, and giving it a repository to write to would
//     invite a pull request nobody asked for. A schedule fired from an
//     unbound template with no target of its own files a task with no
//     target at all (fireTaskSchedule).
//   - Reads: the state repository, when this deployment has one. That is
//     grain's editable configuration and memory as text (pkg/staterepo,
//     whose ExportConfig writes those tables and no others) -- what this
//     deployment is already configured to tell every run, and what this
//     job wrote the last time it ran. It is the background, not the
//     record: tasks, runs and comments are not exported at all, and the
//     prompt says so, because a run told to read its evidence there
//     spends its turns discovering that tables/task.json is not a file.
//   - The memory grant, which is the whole point: update_memory, and
//     the two tools onto the long notes beside it,
//     list_memory_documents and read_memory_document
//     (pkg/capability/memory, pkg/mcp/memory_tool.go). Reading is on
//     that grant so that a run finding a stale note can replace it in
//     the same turn, and this is the run that exists to go looking.
//   - The self-debug grant, which is where the evidence actually comes
//     from. It is two sets of read-only tools: pkg/mcp's task readers
//     (list_grain_tasks, read_grain_task, read_grain_task_prompt,
//     read_grain_task_transcript), which read this deployment's own
//     tasks and runs through the daemon -- with read_grain_metrics
//     beside them, which is those same runs counted rather than read,
//     and the difference between a memory that says "this keeps
//     happening" and one that says how often -- and
//     pkg/capability/selfdebug's
//     reads of grain's own source checkout. A pattern in the runs is
//     often only legible against the code that produced it -- what a
//     reconciler actually does on a failure, what a prompt actually
//     said -- and a memory written from a misreading of grain's own
//     behaviour is repeated to every run afterwards.
//   - NetworkOpen, because this job is enabled on the shipped
//     host-directory backend, which cannot honour NetworkNone: a run
//     there uses the host's own network stack, and the backend correctly
//     refuses to pretend otherwise. Closing the memory agent belongs to
//     deployments whose sandbox backend can actually enforce it.
//
// Deliberately not granted: grain-log and self-repair. The memory agent
// reads the record and writes prose; it has no reason to read every line
// this host has ever logged, and none whatsoever to change anything on
// it.
func memoryAgentTemplate(stateRepo RepoRef) *Template {
	t := &Template{
		ID:            MemoryAgentTemplateID,
		Name:          "Memory agent",
		Title:         "Write down what grain has learned about working here",
		Body:          memoryAgentBody(stateRepo),
		NetworkAccess: NetworkOpen,
		Grants: []Grant{
			// Named as strings rather than through
			// pkg/capability/memory and pkg/capability/selfdebug's own
			// CapabilityName constants: those packages import this one,
			// so this one cannot import them.
			{Capability: "memory", Via: GrantByGrain},
			{Capability: "self-debug", Via: GrantByGrain},
		},
	}
	if stateRepo.Owner != "" {
		t.Reads = []RepoRef{stateRepo}
	}
	return t
}

// memoryAgentBody is what the run is actually told.
//
// It is written against what the run can actually reach -- the task
// readers, the state repository checkout and grain's own source -- and
// it spends most of its length on what does *not* belong in a memory,
// for the reason
// mcp.NewMemoryTools' own description does: an agent asked what it
// noticed will write down everything it noticed, and every word of that
// is then repeated in every prompt this deployment issues until somebody
// replaces it. The cost of a memory is paid by every later run; the
// benefit is only ever paid by a run that would otherwise have gone
// wrong.
//
// It asks for replacement rather than accumulation, and says where the
// current text is (this run's own prompt), because that is the only way
// a memory can ever shrink. A memory that could only grow could not be
// corrected, and a wrong note repeated on every dispatch is worse than
// no memory at all.
//
// It says the same of the long notes beside it (model.MemoryDocument),
// which this run holds all three tools for and which nothing else in
// this deployment is scheduled to look at. A document is not carried in
// anybody's prompt, so nothing goes wrong the night it stops being true
// -- it goes wrong later, in the one run that opens it and believes it.
// Auditing them is therefore work only a run that goes looking will ever
// do, and this is the run that exists to go looking: the prompt sends it
// to list_memory_documents rather than to the index above it, because a
// task with no target is shown only the deployment's own scope
// (orchestrator.resolveMemory) while the tool lists every repository's
// too.
//
// What it says about a document a person wrote is deliberately not
// repeated about the memory itself. MemoryHandNote already puts that
// instruction in band, in the memory's own text -- "it is somebody's own
// correction, so keep it unless what it says has actually stopped being
// true" -- and this run reads its own memory like any other run, so a
// line here saying it again would be prompt nobody needs. A document
// carries only the bare fact: ", written by hand" on its index line
// (MemoryIndex), and the same clause in what list_memory_documents and
// read_memory_document answer, with the reasoning in Go doc comments no
// agent reads. That left the one run told to correct or delete whatever
// has stopped being true as the one run seeing that mark with no
// instruction attached to it.
func memoryAgentBody(stateRepo RepoRef) string {
	var b strings.Builder
	b.WriteString("You are grain's memory. Once a night you read what this deployment has " +
		"actually been doing and write down what the runs after you ought to know before " +
		"they start.\n\n")

	b.WriteString("Your evidence is the record of the runs themselves, and you read it with the " +
		"self-debug capability's `grain` commands in your sandbox: `grain task list` for what " +
		"this deployment has been doing (pipe it through `grep` for failed tasks), " +
		"`grain task read <id>` for one task's whole record, every attempt and how each " +
		"one ended and everything said on the way through, `grain task prompt <id>` for what " +
		"an agent was actually told, and `grain task transcript <id> [--attempt N]` for one " +
		"attempt's own story. `grain metrics [<section>]` is the same evidence counted rather than read: the " +
		"deployment's own report over a window you choose, including the tool-use census " +
		"and the prompt sizes. Reach for it when what you think you are seeing is a rate -- " +
		"\"runs keep timing out on this tool\" is a claim the census either supports or does " +
		"not, and a memory is worth much more with the number in it. " +
		"Read the recent end of it -- roughly the last week or two of tasks and runs. " +
		"You are looking for what repeats. One run that went wrong is an anecdote; the third " +
		"run that went wrong the same way is a memory. A transcript is long, so open one when " +
		"a task's record leaves you guessing about how a run actually went wrong, rather than " +
		"on principle.\n\n")

	b.WriteString("If those tools are not there, or refuse because this run was dispatched " +
		"without a route back to the grain daemon, then the record is not available to you " +
		"this time. Say exactly that with comment_on_issue and change nothing: a memory " +
		"inferred from what this deployment is configured to do, rather than from what its " +
		"runs actually did, is the failure this job exists to avoid, and a run that invents " +
		"one teaches it to every run afterwards.\n\n")

	if stateRepo.Owner != "" {
		b.WriteString("You also have read access to " + stateRepo.String() + ", this " +
			"deployment's state repository: its editable configuration and memory exported as " +
			"text, one file per table under `tables/`. Two things there are worth your time:\n\n" +
			"- `tables/memory.json` and `tables/memory_document.json` -- what you wrote last " +
			"time, and when: the short memory every prompt carries, and the long notes filed " +
			"beside it.\n" +
			"- `tables/repo_config.json` and `tables/grain_config.json`, with the templates, " +
			"suites and schedules beside them -- what this deployment is already configured to " +
			"tell every run, so you do not spend a memory repeating it.\n\n" +
			"What is not there is the record: grain exports its configuration and memory and " +
			"nothing else, and keeps tasks, runs, comments and observations in its own " +
			"database. There is no `tables/task.json`, `tables/task_run.json`, " +
			"`tables/task_comment.json` or `tables/task_observation.json` to read, so do not " +
			"go looking for one, and do not read a repository without them as a deployment " +
			"that has done nothing. The task tools above are the only way to that half.\n\n")
	} else {
		b.WriteString("This deployment keeps its configuration locally rather than in a state " +
			"repository, so you have no checkout of its settings to read this time -- which " +
			"costs you the background, not the record. What you are replacing is in this " +
			"prompt already, and the runs are in the task tools above.\n\n")
	}

	b.WriteString("Self-debug also reads grain's own source: inspect the `grain-src/` checkout " +
		"directly with standard sandbox tools (`run_command` with grep/find/git log, or `read_file`) to check " +
		"what grain itself does before you write down a belief about it -- a " +
		"memory that misreads grain's own behaviour is repeated to every run afterwards.\n\n")

	b.WriteString("Write what you found with `grain memory write --file <f>`. Leave `--repo` out for the memory " +
		"every run gets, or set `--repo owner/name` for the one only runs targeting that " +
		"repository get; write both if both have something to say. Each one replaces the " +
		"whole memory for that scope rather than adding to it, and what you are replacing is " +
		"already in the prompt above: send back what is still true, drop what is not, and " +
		"write an empty file to forget a scope entirely. That is the only way a memory ever " +
		"gets smaller, so re-reading what is there and cutting what has stopped being true " +
		"is as much of this job as adding anything.\n\n")

	b.WriteString("What earns a place in a memory: a suite or a check that fails for its own " +
		"reasons rather than for the change under it; a build step a repository needs before " +
		"its tests mean anything; a review comment that keeps being made; a path where work " +
		"keeps going wrong; a convention this deployment holds to that nothing in the " +
		"repository writes down.\n\n")

	b.WriteString("What does not: anything the repository already says (its README, its " +
		"CLAUDE.md, its own docs -- a run can read those for itself); anything one task alone " +
		"needed; a narrative of what happened last night; praise, tallies, or a summary of " +
		"how busy the week was. A memory is not a report. It is read by every run forever, so " +
		"it earns its place only by changing what a run would otherwise do.\n\n")

	b.WriteString("Be brief. A couple of paragraphs of prose or a short list per scope is " +
		"plenty, and the limit is a hard 8 KiB per scope. If you find nothing worth saying " +
		"that is not already there, change nothing and say so -- a night with no new memory " +
		"is the ordinary outcome, and rewriting a good memory into a longer one is a loss.\n\n")

	b.WriteString("grain also keeps long notes -- documents: a named note per scope that no " +
		"prompt carries, listed one line each in the memory block above and read in full with " +
		"`grain memory read <key>`. You have those commands because you hold the memory capability, and " +
		"nothing else here is scheduled to look at them, so keeping them true is as much this " +
		"job as the short memory is. Run `grain memory list` rather than working from the " +
		"index in your own prompt: that index is this deployment's own scope, and the command lists " +
		"every repository's as well. Read the ones that touch what you have just been reading in " +
		"the record, and read the old ones on principle. A document goes stale quietly -- nothing " +
		"carries it, so nothing goes wrong the night it stops being true; it goes wrong later, in " +
		"the one run that opens it mid-task and believes it.\n\n")

	b.WriteString("You write one with `grain memory doc-write <name> --summary <s> --file <f>` " +
		"(and `--repo owner/name` for a repository's): say in one line of `--summary` what it is " +
		"about, and put the note in the file. That summary is the " +
		"part every prompt pays for, so it says what the note is about rather than what it says. " +
		"Writing the same name again replaces the document whole and an empty text deletes it, so " +
		"a note you have found to have gone stale is yours to correct on the spot -- rewrite it " +
		"against what the record and grain's own source say now, or delete it if the thing it " +
		"described is gone. Write one when the short memory can only gesture at something -- why " +
		"a suite fails and what to do about it, how a subsystem actually fits together, what four " +
		"runs in a row got wrong in the same place -- and keep the short memory pointing at it, " +
		"since a document nobody is pointed at is a document nobody opens. A document is a hard " +
		"64 KiB, its summary 200 characters, and a scope may hold 24 of them at most: that last " +
		"bound is on the index every prompt carries, so filing a note per observation would " +
		"rebuild out of summaries exactly the diary the 8 KiB limit exists to prevent. Longer is " +
		"not laxer -- what does not earn a place in a memory does not earn a document either.\n\n")

	b.WriteString("A document marked as written by hand is not an earlier run's observation: it " +
		"is somebody's own correction, typed by a person who had decided the note in front of " +
		"them was wrong. Replace or delete one only when the record says what it claims has " +
		"actually stopped being true -- not because it is not how you would have put it, and not " +
		"because it is old. When you do overwrite one, say so in your closing note and say what " +
		"made it wrong, because that note is the only place the person who wrote it finds " +
		"out.\n\n")

	b.WriteString("Finish with comment_on_issue saying what you changed and why -- what you " +
		"added, what you cut, which documents you read and what you did about any that had " +
		"stopped being true, and what you looked at to decide. Nobody is watching this run " +
		"while it happens, and that note is the only account of it there will be.")
	return b.String()
}

// releaseNotesTemplate is the weekly release-notes writer.
//
// The shape of the task is the whole of what this feature is, so each
// piece of it is a decision, the same accounting memoryAgentTemplate
// makes for the job next door:
//
//   - No Target. Like the memory agent, this run has no change to push:
//     what it produces is a `release_note` row, written through the
//     write_release_notes tool, which the next export turns into
//     `release-notes/<week>.md` in the state repository. Giving it a
//     repository to write to would mean a pull request against this
//     deployment's own settings every Monday -- and a sandbox holding a
//     credential to push to them, which is exactly what MemoryDocument's
//     own doc comment argues nothing here should need.
//   - The release-notes grant, which is the point: read_cluster_stats
//     for what this deployment actually did over the week, and
//     write_release_notes to publish it (pkg/capability/releasenotes,
//     pkg/mcp/release_notes_tool.go).
//   - The self-debug grant, which is where the story comes from. The
//     numbers say a week was busy; only the record says what it was busy
//     *with* -- which tasks landed, which came back red, what somebody
//     asked a question about. The same read-only task tools the memory
//     agent reads the record with (list_grain_tasks, read_grain_task and
//     the two below them), for the same reason: a week written up from
//     what this deployment is configured to do, rather than from what it
//     did, is the failure this job exists to avoid.
//   - Reads: the state repository, when this deployment has one -- which
//     is where the weeks already written are (`release-notes/`), so a
//     run can see the house style and can tell a thread it has already
//     reported on from a new one, without a read tool of its own.
//   - NetworkOpen, for memoryAgentTemplate's reason: this job is enabled
//     on the shipped host-directory backend, which cannot honour
//     NetworkNone and correctly refuses to pretend otherwise.
//
// Deliberately not granted: memory. These are two different audiences
// and it matters that one run cannot quietly serve both. A memory is
// read by every later run and is bounded to the notes that change what a
// run does; release notes are read by a person and are the narrative a
// memory is forbidden to be. A job that could write both would be one
// tool call away from filing the week's report as a standing
// instruction to every agent this deployment dispatches.
func releaseNotesTemplate(stateRepo RepoRef) *Template {
	t := &Template{
		ID:            ReleaseNotesTemplateID,
		Name:          "Release notes",
		Title:         "Write up what the cluster shipped this week",
		Body:          releaseNotesBody(stateRepo),
		NetworkAccess: NetworkOpen,
		Grants: []Grant{
			// Named as strings rather than through
			// pkg/capability/releasenotes and pkg/capability/selfdebug's
			// own CapabilityName constants: those packages import this
			// one, so this one cannot import them.
			{Capability: "release-notes", Via: GrantByGrain},
			{Capability: "self-debug", Via: GrantByGrain},
		},
	}
	if stateRepo.Owner != "" {
		t.Reads = []RepoRef{stateRepo}
	}
	return t
}

// releaseNotesBody is what the run is actually told.
//
// It spends its length on two things, and they are the two ways this
// job goes wrong.
//
// The first is invention. A run asked how much a cluster has done, with
// the whole record in front of it and a page to fill, will write the
// impressive version: rounded-up numbers, a figure for something grain
// does not measure, a fortnight of work described as a week's. So the
// numbers come from one tool call rather than from counting
// (read_cluster_stats), the prompt says to use them as they are, and it
// says plainly what to do with a figure that covers only part of the
// week -- the lines-changed total is summed over the merges grain has a
// diff size on file for, so a window reaching back before it started
// recording one is partial, and a partial figure has to be quoted as
// what it covers rather than rounded up into the week's total.
//
// The second is that a quiet week reads as a failure to whoever is
// writing it up. It is not, and an agent that believes it is will pad.
// The prompt asks for the week that happened, says a short note is a
// good note, and asks for what went wrong beside what landed -- a
// week's notes that only ever say things went well are a week's notes
// nobody keeps reading.
//
// What it does not do is prescribe a format beyond a headline and a
// first paragraph. The pane renders markdown and the file is markdown;
// beyond that, a week with one big change and a week with forty small
// ones do not want the same shape, and a template that insisted would
// be filled in rather than written.
func releaseNotesBody(stateRepo RepoRef) string {
	var b strings.Builder
	b.WriteString("You are grain's release notes. Once a week you read what this deployment " +
		"actually did over the week that just ended and write it up for a person -- the " +
		"operator, and anybody they show it to. This is the account of how much work the " +
		"cluster has done, across every repository it works in.\n\n")

	b.WriteString("Start with `grain release-notes stats` in your sandbox. With no arguments it answers for the week " +
		"that just ended -- the same week `grain release-notes publish` files under by default -- and " +
		"it is grain's own count, not an estimate: pull requests merged, tasks completed, " +
		"tasks filed, runs and how many of them succeeded, how long the agents spent running, " +
		"how many lines those merges added and removed, and the same counts per repository. " +
		"Those are the hero numbers, and they go near the top. Use them as they are: do not " +
		"round them up, do not add them together into a bigger-sounding total, and do not " +
		"quote a figure for something that is not in the answer. The lines-changed figure is " +
		"GitHub's own count of each merged pull request's diff, recorded as grain saw it " +
		"merge -- so if the answer says it covers only some of the week's merges, say that " +
		"too, and never quote a partial figure as the week's total.\n\n")

	b.WriteString("Then find out what the week was actually about, with the self-debug task " +
		"commands: `grain task list` for what has been going on, `grain task read <id>` for one " +
		"task's whole record, `grain task prompt <id>` for what an agent was told and " +
		"`grain task transcript <id>` for one attempt's story. " +
		"Read the week the stats cover. You are looking for the handful of things a person " +
		"would want to know: what landed and what it changes, what took several attempts and " +
		"why, what is still open or still failing, what somebody had to answer a question " +
		"about. A list of every task is not release notes -- the record already has that. " +
		"Open a transcript only when a task's record leaves you guessing.\n\n")

	if stateRepo.Owner != "" {
		b.WriteString("You have read access to " + stateRepo.String() + ", this deployment's " +
			"state repository. The weeks already written are in `release-notes/`, one markdown " +
			"file per ISO week -- read the last two or three before you start: they are where " +
			"the house style is, and they are how you tell a thread you have already reported " +
			"on from something new. `tables/repo_config.json` is which repositories this " +
			"deployment works in at all, which is worth a look before you describe one as " +
			"quiet.\n\n")
	}

	b.WriteString("Write the week with `grain release-notes publish --title <t> --file <f>`. Leave " +
		"`--period` out and it files the week that just ended, the one the stats are for. `--title` " +
		"is a headline a person reads in a list -- what the week was about, not \"Weekly release " +
		"notes\". The file is " +
		"markdown: lead with a short paragraph saying what the week amounted to, put the " +
		"numbers where they can be seen, then say what happened in each repository that had " +
		"something happen in it, then what is still outstanding. Writing the same week again " +
		"replaces it, so if you find you got something wrong you can correct it in the same " +
		"run.\n\n")

	b.WriteString("Be honest and be short. A quiet week is a quiet week: say so in a paragraph " +
		"and stop, rather than padding it out -- nobody is served by a report that reads the " +
		"same whatever happened. Say what went wrong as plainly as what went right: a suite " +
		"that has been failing all week, a pull request that has been open for six days, a " +
		"task that has been retried four times, are the most useful sentences in the whole " +
		"note. Do not praise the cluster, and do not write about yourself.\n\n")

	b.WriteString("Finish with `comment_on_issue` saying which week you wrote, what you led " +
		"with and anything you noticed that the notes themselves are not the place for. " +
		"Nobody is watching this run while it happens, and that note is the only account of " +
		"it there will be.")
	return b.String()
}

// infraReviewCooldownDays is how long the infrastructure review leaves a
// parameter alone once it has moved, before it will recommend moving
// that same parameter again.
//
// A tuner with no cooldown is not a tuner. It moves a number every
// night, every night's measurement then covers a span in which
// something else also changed, and no improvement can ever be
// attributed to anything -- which is the one thing the whole job exists
// to be able to do. So the rule is stated in the prompt and in every
// report, as a rule rather than as a judgement the run makes fresh each
// night, because a judgement made fresh each night is exactly what an
// agent looking at a bad number will talk itself out of.
//
// Seven days because that is what makes the comparison legible rather
// than because a week is a round number: it is this deployment's own
// default metrics window, so a parameter that has been still for the
// cooldown has a full window after the change and a full window of
// equal length before it, which is the two-call shape docs/tuning-stats.md
// describes and the `changes` section exists to place the boundary for.
// A shorter cooldown would have the run comparing a part-window against
// a whole one and reading the difference as an effect.
const infraReviewCooldownDays = 7

// infraReviewTemplate is the nightly infrastructure review: the run that
// reads how this deployment's own machinery actually performed, says
// which parameter it believes should move and why, and moves nothing.
//
// **It suggests and never applies**, and that is the feature rather than
// a caution about it. Every number it might want to change is a column
// of `grain_config`, which this deployment exports to its state
// repository as `tables/grain_config.json` and imports back from it, so
// a change to one is a pull request a person reads. The reasoning is the
// whole of what a tuner is worth: the same slow CI supports "raise
// max_ci_jobs" and "lower max_workers" depending on which limit is
// believed to be binding, and those have opposite effects, so the
// sentence saying which it believes is the part that has to be reviewed.
// A job that could write the setting would move this deployment's
// behaviour overnight with nobody having read that sentence. What it
// produces instead is a report -- its closing note -- and, where the
// evidence is strong enough, a proposed task carrying the change: a task
// somebody approves before it runs, and a pull request somebody merges
// after it does. Two people-shaped gates in front of every number that
// moves, and this run is neither of them.
//
// The shape of the task is the whole of what this feature is, so each
// piece of it is a decision, the same accounting memoryAgentTemplate
// makes for the job next door:
//
//   - No Target, for the memory agent's reason and one of its own. It
//     has no change to push: what it produces is prose and proposals.
//     And a target would be the state repository, which is the one
//     repository a run recommending changes to this deployment's own
//     settings must not be able to push to -- the gap between "suggest"
//     and "apply" would then be one `git push` wide.
//   - Reads: the state repository, when this deployment has one. That is
//     where every parameter in scope actually lives
//     (`tables/grain_config.json`), and a recommendation to change a
//     number whose current value the run never read is not a
//     recommendation. Its git history is also the only place a settings
//     change has a timestamp, which is what the cooldown is measured
//     against and what `read_grain_metrics`' own `changes` section is
//     derived from (staterepo.SettingsChanges).
//   - The self-debug grant, which is where all three kinds of evidence
//     are. read_grain_metrics is the numbers -- `ci_queueing` for what
//     each job ran and waited, `host` for what the machine underneath
//     was doing over the same slices, `changes` for when a parameter
//     last moved, `capacity`, `cycles`, `checks` and `backlog` for
//     whether the deployment was full and what that cost
//     (docs/tuning-stats.md is the companion change that built them, and
//     says what each one's retention actually is). pkg/mcp's task
//     readers are this job's own history: what it recommended last
//     night, whether anybody took it, and what happened next -- without
//     which it re-files the same recommendation indefinitely and learns
//     nothing. And selfdebug's source readers are the mechanism: a
//     recommendation about max_ci_jobs made without reading what the
//     admission code does with it is a guess with a number attached.
//   - The grain-ci grant, which is not about running CI -- this task has
//     no branch for CI to run on, and the grant decides nothing about
//     whether a repository's workflows fire (pkg/capability/grainci's
//     own doc comment: that is read off the repo and deployment config,
//     never off a task). What it does here is register list_ci_runs and
//     get_ci_log, and self-debug widens their scope to every repository
//     this deployment remembers (mcp.CIWideReadGrant). That is the only
//     route to half the reliability brief: a job cancelled when the
//     daemon restarted, and a flake identified by the same job failing
//     the same way across unrelated branches, are facts about a night's
//     runs that no aggregate over job durations contains.
//   - NetworkOpen, for memoryAgentTemplate's reason: this job is enabled
//     on the shipped host-directory backend, which cannot honour
//     NetworkNone and correctly refuses to pretend otherwise.
//
// Deliberately not granted: memory, release-notes, self-repair and
// deployment-health. The first two are other jobs' audiences -- a tuner
// that could write the memory would be filing tonight's hypothesis as a
// standing instruction to every run this deployment dispatches, which is
// the same argument releaseNotesTemplate makes for not holding the
// memory grant. self-repair is the point of the whole design: the
// counterpart capability that can actually change things is exactly what
// this job must not have. deployment-health is a near miss rather than
// an oversight -- read_deployment_health is an excellent instantaneous
// reading and this job is about series, and the instantaneous reading
// taken at 03:00 describes the quietest hour of the day, which is the
// one hour nothing here should be tuned against.
//
// # Why nightly, when most settings should not move most weeks
//
// The cadence is how often it looks, not how often it changes something:
// infraReviewCooldownDays already fixes the second at "not more than
// once a week per parameter", and most nights the honest output is that
// nothing is recommended. Three things make looking nightly worth a
// sandbox anyway.
//
// The evidence is perishable, and asymmetrically so. The host series is
// hourly rollups in memory, bounded at a fortnight and lost on every
// daemon restart (sysstat.DefaultHistoryHours, and `host.reaches` is the
// field that says how far back it really goes); CI job rows are swept
// with the CI history. A weekly run on a deployment whose daemon
// restarted on Wednesday has no host series at all for the first half of
// the week it is reporting on -- and the nights a daemon restarts are
// disproportionately the nights worth reading. A nightly run has already
// read Monday before the restart happens.
//
// The reliability half is perishable in a different way. "This job
// failed the same way on six branches tonight" is a fact about tonight:
// by the following Monday those branches have merged or been abandoned,
// their logs are swept, and the finding is a history lesson rather than
// something anybody can act on. Noticing it the next morning is most of
// its value.
//
// And a nightly job that usually says "within the usual range, nothing
// recommended" is cheap to read and therefore actually read, which is
// the property that decides whether a standing job is worth anything. A
// weekly one arrives carrying a week of everything and gets skimmed --
// and a tuner whose reports are skimmed is a tuner whose one strong
// recommendation is skimmed with them.
//
// The cost is one sandbox a night, at the quiet hour every standing job
// grain ships runs at (orchestrator's systemScheduleTimeOfDay), reading
// a day that has finished and competing with workers that are idle --
// the memory agent's argument for the same slot. What it buys is
// visible in what the evening this was filed cost without it: an
// operator asking whether throughput was healthy, about fifteen minutes
// of a Director session hand-assembling numbers out of `list_ci_runs`
// output and one instantaneous health reading, one parameter changed on
// that evidence, and no way afterwards to tell whether the change helped
// (docs/tuning-stats.md). One sandbox against that, most nights spent
// concluding nothing needs to happen, is the trade -- and a deployment
// that disagrees owns the cadence and the switch from the first seeding
// onward (ui.refuseSystemScheduleContentEdit), so finding it noisy is
// one edit rather than a pull request against grain.
func infraReviewTemplate(stateRepo RepoRef) *Template {
	t := &Template{
		ID:            InfraReviewTemplateID,
		Name:          "Infrastructure review",
		Title:         "Suggest what to tune for throughput and reliability",
		Body:          infraReviewBody(stateRepo),
		NetworkAccess: NetworkOpen,
		Grants: []Grant{
			// Named as strings rather than through
			// pkg/capability/selfdebug and pkg/capability/grainci's own
			// CapabilityName constants: those packages import this one,
			// so this one cannot import them.
			{Capability: "self-debug", Via: GrantByGrain},
			{Capability: "grain-ci", Via: GrantByGrain},
		},
	}
	if stateRepo.Owner != "" {
		t.Reads = []RepoRef{stateRepo}
	}
	return t
}

// infraReviewBody is what the run is actually told.
//
// It spends its length on the four ways this job goes wrong, and they
// are all one failure wearing different clothes: a nightly run with a
// page to fill will fill it.
//
// It finds something every night. So the prompt says in as many words
// that "throughput and reliability are within their usual range,
// nothing recommended" is a good night's output, and it makes a
// suggestion expensive to make rather than cheap: three things it has
// to carry, and a suggestion missing any of them is not made. The
// three are not a format, they are the three ways a tuning
// recommendation is wrong -- a number with no history is noise, a
// number with no mechanism is a guess, and a change with no decision
// rule for reading the result can never be evaluated, which means the
// next night's run cannot learn from it either.
//
// It thrashes. Hence the cooldown (infraReviewCooldownDays), stated as
// a rule and repeated in the report rather than left to a judgement the
// run makes about whether this particular number is special. Every
// agent looking at a bad number believes this particular number is
// special.
//
// It forgets. A run that does not read what it recommended last night
// re-files it, every night, until somebody switches the schedule off --
// which is a real failure mode for a standing job nobody watches, and
// the reason the prompt sends it to the task readers for its own
// history before it sends it to the metrics at all. "Was it taken?" is
// answerable two ways and it is told to use both: the proposal's own
// task record, and whether the parameter actually moved in `changes`.
//
// It tunes only throughput. Reliability is half the brief and the half
// with no single number on a dashboard, so the prompt names the signals
// one at a time -- jobs cancelled by a daemon restart, tasks stuck where
// nothing will move them, a job failing the same way across unrelated
// branches, repairs the merge queue had to file -- because a run asked
// for "reliability" in the abstract will report the failure rate and
// call it done.
//
// What it does not do is prescribe the report's shape beyond what a
// suggestion must contain. A night with one recommendation and a night
// with an outage to describe do not want the same note, and a template
// that insisted would be filled in rather than written.
func infraReviewBody(stateRepo RepoRef) string {
	var b strings.Builder
	b.WriteString("You are grain's infrastructure review. Once a night you read how this " +
		"deployment's own machinery actually performed -- how much work got through it, how " +
		"long things waited, what broke and what broke repeatedly -- and you say which of its " +
		"parameters you think should move, and why.\n\n")

	b.WriteString("**You suggest. You never apply.** Every parameter you might want to change " +
		"is a setting of this deployment, and changing one is a pull request somebody reads. " +
		"You have no tool that writes a setting and you must not go looking for one: the " +
		"entire value of this job is that a person sees the reasoning before the deployment's " +
		"behaviour moves. Your output is the report you finish with, plus -- only where the " +
		"evidence is strong enough -- a proposed task carrying the change.\n\n")

	b.WriteString("## Start with what you said last night\n\n")
	b.WriteString("Before you look at a single number, find out what this job already " +
		"recommended and what came of it. The self-debug `grain` commands are how: `grain task list` " +
		"for what this deployment has been doing, " +
		"`grain task read <id>` for one task's whole record including every comment on it, " +
		"`grain task prompt <id>` for what an agent was told and `grain task transcript <id>` " +
		"for one attempt's own story. Look for the earlier firings of this same schedule -- " +
		"their closing notes are your previous reports -- and for any task you proposed from " +
		"one. For each open recommendation, answer three questions and carry the answers into " +
		"tonight's report -- was it taken, did the parameter actually move, and did the thing " +
		"you said would happen happen? A recommendation still waiting for somebody is not " +
		"re-filed: say it is still open and leave it alone. A recommendation that was taken is " +
		"the most valuable thing you will look at all night, because it is the only evidence " +
		"you will ever get about whether your own reasoning works. If it was taken and the " +
		"decision rule you wrote says it was the wrong lever, say that first and plainly -- " +
		"including recommending it be put back.\n\n")

	b.WriteString("Without this you are not a tuner, you are a random walk: a run that does not " +
		"read its own history files the same recommendation every night until somebody " +
		"switches the schedule off.\n\n")

	b.WriteString("## The numbers\n\n")
	b.WriteString("`grain metrics` is this deployment measuring itself, and it is the " +
		"answer to every question of the form \"is this worse than it was\". Ask for one " +
		"section at a time (`grain metrics <section>`) rather than the whole page. The ones this job is about:\n\n" +
		"- `ci_queueing`: per job, how long it **ran** and how long it **waited**, slice by " +
		"slice, plus what the waiting was blamed on. This is the most important series here, " +
		"because those two numbers have opposite fixes and a single \"CI is slow\" figure " +
		"cannot tell them apart.\n" +
		"- `host`: load, CPU and memory over the same slices, so a job that took twice as long " +
		"can be read against the machine that gave it half a core.\n" +
		"- `merges`: merge queue contention telemetry, batch size distributions, merge gate " +
		"evaluation latency, queue wait time per PR, and rebase/re-test contention.\n" +
		"- `changes`: when one of this deployment's own parameters last moved, and which. Read " +
		"this early -- it is what tells you where to put the boundary of every comparison you " +
		"make, and it is how you check whether last night's recommendation was acted on.\n" +
		"- `capacity` and `cycles`: whether the deployment was full, and what the scheduling " +
		"tick itself cost.\n" +
		"- `checks`: how long runs sat blocked on CI, and how many pushes it took to go green.\n" +
		"- `backlog`: what is queued right now and the oldest thing waiting.\n\n")

	b.WriteString("Every series in the report is bucketed on the same slices, so ask each " +
		"section over the same window and read them against each other as one picture. A " +
		"series carries a fixed number of points however long the window is, so the window is " +
		"what sets the resolution: ask over half a day to see hours, over a season to see " +
		"weeks. The shape that actually answers a tuning question is two calls, not one: the " +
		"window since the last change, and a window of equal length before it. Read each " +
		"section's own header before " +
		"you quote a share out of it -- it says how many samples it had, and a window reaching " +
		"back before a measurement existed reports a share of a smaller population than its " +
		"dates suggest. The host series in particular is held in memory and starts again when " +
		"the daemon restarts; it says how far back it really reaches, and a short history must " +
		"not be read as a quiet fortnight.\n\n")

	b.WriteString("## What a suggestion has to carry\n\n")
	b.WriteString("Three things. A suggestion missing any one of them is not a suggestion and " +
		"you do not make it -- say what you saw as an observation instead.\n\n")

	b.WriteString("1. **The number, with its history.** Not \"CI looks contended\" but \"the " +
		"`go` job ran in 31-34 minutes tonight against 15-26 minutes last week, while queue " +
		"time also rose\". A reading with nothing to compare it against says nothing at all.\n" +
		"2. **The mechanism.** Which limit you believe is binding, and why: contention *inside* " +
		"jobs that are already running, or admission holding them *outside*. Those have " +
		"opposite fixes, so a recommendation that does not say which it believes is a guess. " +
		"Check it against the code rather than against your intuition -- standard sandbox tools " +
		"(`read_file`, `run_command` with grep/find/git log) in the `grain-src/` checkout are grain's own source, and a recommendation about " +
		"`max_ci_jobs` made without reading what the admission path does with it is worth " +
		"nothing. If the source is not readable from this run, say so and file what you found " +
		"as an observation rather than as a proposal.\n" +
		"3. **A decision rule for reading the result.** State in advance what would show the " +
		"change worked and what would show it was the wrong lever. Worked example: \"If `go` " +
		"run time drops toward 20-25 min and suite wall-clock holds or improves, 7 slots was " +
		"right. If run time drops but wall-clock gets worse, the bottleneck is demand, not " +
		"concurrency, and the lever is `max_workers` rather than `max_ci_jobs`.\" You are " +
		"writing this for the run that reads it in a week -- which is you -- so make it " +
		"something a later run can actually check against a number.\n\n")

	b.WriteString(fmt.Sprintf("## The cooldown\n\nA parameter that has moved within the last "+
		"%d days is left alone, whatever tonight's numbers look like. Not because the number "+
		"is right, but because its effect is not measurable yet: a tuner that moves something "+
		"every night can never attribute an improvement to anything it did, and that "+
		"attribution is the only thing separating this job from guessing. %d days is this "+
		"deployment's own default measurement window, so a parameter that has been still that "+
		"long has a full window after the last change to compare against a full window "+
		"before it.\n\n", infraReviewCooldownDays, infraReviewCooldownDays))

	b.WriteString("State the cooldown in your report -- which parameters are inside it, when " +
		"each comes out, and what you would have said about them otherwise. That last part " +
		"matters: it is how a person sees that you noticed, and it is what the run after the " +
		"cooldown expires has to read. The cooldown is a rule, not a judgement you make fresh " +
		"each night. Do not argue yourself past it because this particular number looks " +
		"especially bad; that is what it is for.\n\n")

	b.WriteString("## Reliability, which is half of this\n\n")
	b.WriteString("Throughput is the obvious reading and the easy one. Reliability has no " +
		"single figure, so go and look for these by name:\n\n" +
		"- **Jobs cancelled rather than judged**, particularly by a daemon restart. A " +
		"cancellation is not a verdict, and one can leave a cache cold or a branch unjudged " +
		"for hours without anything saying so. `grain ci log` is how you tell a job that was " +
		"killed from one that failed.\n" +
		"- **Tasks stuck in a state nothing will move them out of** -- waiting on a check that " +
		"cannot arrive, on a merge that keeps being retried, on a person. `backlog` and " +
		"`grain task list` between them will show you the oldest of these.\n" +
		"- **Flaky jobs**, identified as the same job failing the same way across unrelated " +
		"branches. `grain ci list` annotates a red job with the other branches failing " +
		"identically, and your self-debug grant lets you read every repository's runs rather " +
		"than one's. \"This flaked on six branches tonight\" is worth more than any number you " +
		"will tune.\n" +
		"- **Repairs the merge queue had to file**, and anything it gave up on and handed back " +
		"to a person.\n\n")

	b.WriteString("Report these even when no parameter would fix them. A reliability problem " +
		"named early is the most useful thing a nightly job produces, and several of these " +
		"have no dial behind them at all -- the answer is a bug report, not a setting.\n\n")

	b.WriteString("Make that bug report one somebody can act on without asking you anything. A " +
		"fault with an answer you can stand behind -- a bug, a missing test, docs that no " +
		"longer match the code -- is proposed as a task that fixes it, with the fix spelled " +
		"out, not as an observation for somebody else to work out. Keep the question form for " +
		"what genuinely needs the owner: a design decision, a trade-off, a behaviour change " +
		"nothing settles -- and write each with the answer you recommend. Settings stay " +
		"suggestions either way: they are the owner's to apply.\n\n")

	b.WriteString("## What is in scope, and what is emphatically not\n\n")
	b.WriteString("In scope: this deployment's concurrency and sizing dials. `max_ci_jobs`, " +
		"`max_workers`, `max_mergers`, `max_merges_per_repo` and `merge_batch_size`; " +
		"`sandbox_cpus`, `sandbox_memory_mb`, `sandbox_disk_gb` and the overcommit factors " +
		"beside them; the cache ceilings (`package_cache_size_gb`, `actions_cache_size_gb`) " +
		"and the retention bounds (`ci_run_history`, `ci_step_output_mb`, " +
		"`ci_step_output_days`, `ci_run_output_kb`). The general rule is a numeric " +
		"concurrency, sizing or retention knob that belongs to the whole deployment.\n\n")

	b.WriteString("Out of scope, and not on the grounds of taste:\n\n" +
		"- **Anything that changes what counts as a verdict.** Not what makes a check pass, " +
		"not whether a red one blocks a merge, not which checks are required, not whether a " +
		"pull request may merge unreviewed. An operator has ruled that a fixed merge predicate " +
		"must not be reachable by strategy, and a nightly tuner reaching into the gate to make " +
		"the numbers look better would be precisely that. If the gate is what is slow, say so " +
		"in the report and stop there.\n" +
		"- **Per-repository policy.** Which capabilities a repository grants, what it is " +
		"gated on, what it may merge. Those are somebody's decisions about that repository, " +
		"not dials.\n" +
		"- **Anything you cannot name a mechanism for.** If you cannot say what the number " +
		"does, you cannot say what moving it would do.\n\n")

	if stateRepo.Owner != "" {
		b.WriteString("## Where the parameters are\n\n")
		b.WriteString("You have read access to " + stateRepo.String() + ", this deployment's " +
			"state repository: its settings as text. `tables/grain_config.json` holds every " +
			"parameter above, and it is where you read the **current** value of anything you " +
			"are about to recommend changing -- a recommendation to raise a number you never " +
			"read is not a recommendation. `git log -p tables/grain_config.json` in that " +
			"checkout is the only place a settings change carries a timestamp, so it is how " +
			"you check the cooldown and how you corroborate what the `changes` section says. " +
			"`tables/repo_config.json` is which repositories this deployment works in at all, " +
			"which is worth reading before you describe one as idle.\n\n")
		b.WriteString("Note what is **not** there: this deployment's tasks, runs, comments and " +
			"observations are not exported, so there is no tables/task.json and no " +
			"tables/task_run.json to read. The record lives behind the self-debug task tools " +
			"above.\n\n")
		b.WriteString("You cannot push to that repository and you should not try. The way a " +
			"change reaches it is a proposal.\n\n")
	} else {
		b.WriteString("## Where the parameters are\n\n")
		b.WriteString("This deployment exports no state repository, so you cannot read the " +
			"current value of any parameter, and the `changes` section has nothing behind it " +
			"-- you cannot tell when anything last moved, which means you cannot honour the " +
			"cooldown either. Report what the measurements say, say plainly that you could not " +
			"read the settings or their history, and do not propose a change to a number you " +
			"have not seen.\n\n")
	}

	b.WriteString("## Filing a change\n\n")
	b.WriteString("Where the evidence clears the bar, file it with `propose_task`")
	if stateRepo.Owner != "" {
		b.WriteString(", with `repo` set to " + stateRepo.String() + " -- that is where the " +
			"setting lives and therefore where the change has to land")
	}
	b.WriteString(". Put the whole case in the body: the number with its history, the " +
		"mechanism, the decision rule, the current value and the value you are proposing, and " +
		"what you expect to see a week later. The person approving it and the run doing it " +
		"have nothing else to go on. One proposal a night at most, and usually none -- a " +
		"night that proposes three changes is a night whose results cannot be read.\n\n")

	b.WriteString("## Saying nothing is a good night\n\n")
	b.WriteString("\"Throughput and reliability are within their usual range; nothing " +
		"recommended\" is a complete and successful report, and on most nights it is the " +
		"correct one -- most settings should not move most weeks. A nightly job that always " +
		"finds something is a nightly job nobody reads, and by the time it has something " +
		"urgent to say nobody is reading it. Do not pad, do not manufacture a marginal " +
		"suggestion to justify the run, and do not recommend a change you would not defend to " +
		"the person who has to merge it.\n\n")

	b.WriteString("## Finish\n\n")
	b.WriteString("Finish with `comment_on_issue`, and make it the report: what the night " +
		"looked like, what you recommended and the three things behind it, what is inside the " +
		"cooldown and until when, what you said last night and what came of it, and anything " +
		"about reliability that has no dial behind it. Nobody is watching this run while it " +
		"happens and that note is the only account of it there will be.\n\n")

	b.WriteString("Finish that way even if you are unsure and would rather ask: this task is " +
		"a scheduled firing, and while it stays open the next night's firing is skipped and " +
		"nothing announces that it was. A run that parks on a question stops this schedule " +
		"silently and indefinitely. Put the question in the report instead.")
	return b.String()
}

func baseRepairTemplate(stateRepo RepoRef) *Template {
	return &Template{
		ID:    BaseRepairTemplateID,
		Name:  "Base Branch Repair",
		Title: "Fix grain CI on {{repo}} {{branch}}",
		Body:  baseRepairTemplateBody(),
		Fields: []TemplateField{
			{Name: "team", Label: "Responsible team for the breakage", Required: false},
			{Name: "repo", Label: "Repository where CI failed", Required: true},
			{Name: "branch", Label: "Base branch where CI failed", Required: true},
			{Name: "commit", Label: "Commit SHA that failed", Required: true},
			{Name: "detail", Label: "Failure details and failing job summary", Required: false},
			{Name: "confirm_detail", Label: "Confirmation run details", Required: false},
			{Name: "merged_task_id", Label: "ID of merged task that caused breakage", Required: false},
			{Name: "merged_task_title", Label: "Title of merged task that caused breakage", Required: false},
		},
		AutoMerge: true,
		Grants: []Grant{
			{Capability: "grain-ci", Via: GrantByGrain},
		},
	}
}

// RedCIProcedure names the standard procedure for a red build that every
// run with grain CI is given in its prompt (orchestrator's
// redCIProcedureSection). The bodies of grain's own repair tasks refer
// to it by this name, through RedCIProcedureReminder, rather than spelling
// the steps out a second time in a task the same prompt already carries.
const RedCIProcedure = "red-CI procedure"

// RedCIProcedureReminder is the paragraph a grain-filed CI or release
// repair body ends its instructions with.
const RedCIProcedureReminder = "Work it with the " + RedCIProcedure + " in your prompt: find the " +
	"failing job and the exact failing tests, rule out inherited, flaky and infrastructure " +
	"failures, reproduce the failure locally with the narrowest test command before changing " +
	"anything, fix and re-run it locally, then re-run only the failed jobs before the full suite."

func baseRepairTemplateBody() string {
	var b strings.Builder
	b.WriteString("grain CI is failing on `{{branch}}`, the branch changes merge into on {{repo}}, at commit `{{commit}}`.\n\n")
	b.WriteString("Failure details:\n{{detail}}\n\n")
	b.WriteString("Confirmation:\n{{confirm_detail}}\n\n")
	b.WriteString("Caused by merged task: {{merged_task_id}} ({{merged_task_title}})\n\n")
	b.WriteString("Fix it on a branch of your own and open a pull request against `{{branch}}`.\n\n")
	b.WriteString("You are repairing the root base branch itself. Take a holistic approach: " +
		"inspect and resolve all failing jobs in the suite, rather than stopping after the first fix. " +
		"Disregard hints about \"shared base faults\" or failures occurring on other branches—this repair " +
		"task is the source of truth for `{{branch}}`, and no other repair will fix them for you. Revert the " +
		"merge above if that is the right answer, or repair the broken suites directly. Iterate and " +
		"re-run CI until all required jobs in the suite pass.\n\n")
	b.WriteString(RedCIProcedureReminder + "\n\n")
	b.WriteString("The merge queue is still merging into `{{branch}}` -- a red base branch holds nothing, because a gate whose own failure blocks the only route to fixing it is a deadlock rather than a control. So this is not a queue everyone is waiting in, it is a tree every other change for {{repo}} is landing on while it is broken, and each one that lands makes the next verdict harder to read. That is the hurry.\n")
	return b.String()
}
