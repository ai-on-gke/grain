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
)

// WorkflowReviewTemplatePrefix marks the one system template that is a
// family rather than a single job: "run this repository's scheduled
// workflow and check the results", one per workflow (grain/task-344).
//
// A workflow declaring `on: schedule` is translated into a task schedule of
// this deployment's (pkg/ci's cron.go, pkg/repodecl's workflows.go), and
// what that schedule fires is this. Which is the whole of why the feature
// is worth having: a cron of pkg/ci's own could have started the run at
// 02:11 just as well, and would have produced the thing being replaced --
// a run nobody reads. The schedule guarantees the reader in the same act as
// the run.
//
// The workflow is named in the id because the template's title, its prompt
// and the run it reviews all differ per workflow, and a schedule has
// nowhere else to carry one: Schedule.TemplateID is the only field it has
// that says what a firing *is*. It is the file's stem rather than its
// `name:` -- a name is prose somebody will reword, and rewording it must
// not repoint the schedule, which is model.RepoDeclaredID's own reasoning
// for deriving identity from the file.
const WorkflowReviewTemplatePrefix = SystemTemplatePrefix + "workflow-review:"

// WorkflowReviewTemplateID is the template that reviews the workflow
// declared in `.grain/workflows/<stem>.yml`.
func WorkflowReviewTemplateID(stem string) string { return WorkflowReviewTemplatePrefix + stem }

// WorkflowReviewStem is the workflow stem a WorkflowReviewTemplateID names,
// and false for an id that is not one -- what the firing path asks to find
// out whether it has a workflow to start (orchestrator.startScheduledWorkflow).
func WorkflowReviewStem(id string) (string, bool) {
	stem, ok := strings.CutPrefix(id, WorkflowReviewTemplatePrefix)
	if !ok || stem == "" {
		return "", false
	}
	return stem, true
}

// WorkflowReviewTitle is what a review of one scheduled workflow run is
// called, given the template's own title and the branch the schedule runs
// the workflow on: the title as it stands for a schedule that names no
// branch (the default one, which is all a workflow's own cron can mean),
// and with the branch on the end for one that does -- a declared schedule
// with `workflow:` and `base:` (repodecl.parseSchedule). The same workflow
// runs on two branches then, and two reviews a morning with one name is
// a reader left to open both to find out which is which.
func WorkflowReviewTitle(title, base string) string {
	if base = strings.TrimSpace(base); base == "" {
		return title
	}
	return title + " on " + base
}

// workflowReviewTemplate is the standing prompt for reviewing one run of
// one scheduled workflow.
//
// It is code rather than a `template` row for SystemTemplate's own reason,
// which applies here about as sharply as it does to the memory agent: what
// this prompt says is grain's behaviour, not a deployment's configuration.
// The whole guarantee the translation makes is that a scheduled run is
// read by somebody who reports honestly on it, and a prompt an operator --
// or an agent editing a repository's own files -- could rewrite is a
// guarantee that can be edited away without anyone noticing. A declared
// schedule cannot ask for a different prompt, and this one arrives with an
// upgrade of grain, reviewed where grain's other prompts are reviewed.
//
// Two decisions about the content, both of which are about drift:
//
//   - It names no job. Which jobs a workflow runs changes week to week,
//     and a prompt carrying a list of them is a prompt that is wrong by the
//     second month -- this deployment's own hand-written nightly template
//     named six jobs where the workflow had five, and named a
//     `.github/workflows` file that could not run here at all. So the
//     prompt sends the agent to the workflow file in its own checkout, and
//     says so in as many words.
//   - It names no run id. The run this task is about, and how that run
//     ended, are told to the task as a comment
//     (orchestrator.fileWorkflowReview), because those are facts about one
//     firing rather than about the job, and a template body is resolved
//     fresh for every firing from this same code.
//
// The run is finished before this is ever filed (grain/task-351), which is
// what lets the prompt ask for a whole verdict rather than for whatever an
// agent managed to watch: the review is filed by the cycle that collected
// the conclusion, not by the one that started the run.
//
// With one exception, and the prompt has to carry it: a run nothing ever
// concluded is given up on after a deadline and reviewed unfinished
// (orchestrator.scheduledWorkflowDeadline). That is not the same job as
// reading a finished run and it is not the same job as reporting a night
// that never ran either -- the run is there, most of its jobs have ended,
// and the one that has not is where the hang is. So this prompt names
// three kinds of night rather than two, and leaves saying which to the
// comment, which is the only thing that knows (orchestrator's
// workflowReviewComment, off ScheduledWorkflowRun.Unfinished).
//
// Grants: grain-ci, and nothing else. list_ci_runs and get_ci_log are read
// paths onto this deployment's own CI, which is exactly what reviewing a
// run is; this job has no change to push, no memory to write and no reason
// to read the host's logs.
func workflowReviewTemplate(stem string) *Template {
	return &Template{
		ID:    WorkflowReviewTemplateID(stem),
		Name:  fmt.Sprintf("Review the %q workflow's scheduled run", stem),
		Title: fmt.Sprintf("Review last night's %q workflow run", stem),
		Body:  workflowReviewBody(stem),
		Grants: []Grant{
			// Named as a string rather than through
			// pkg/capability/grainci, which imports this package.
			{Capability: "grain-ci", Via: GrantByGrain},
		},
	}
}

func workflowReviewBody(stem string) string {
	var b strings.Builder
	b.WriteString("grain CI has run this repository's `" + stem + "` workflow, because a schedule " +
		"for it came due -- the `on: schedule` cron in that workflow file, or a `.grain/schedules` " +
		"file naming it with `workflow:` to run it on another branch -- and grain has stopped watching that " +
		"run -- because it finished, or because it never did. Your job is to read it and report " +
		"on it. This is triage and judgement, not a fix-everything job: finish by saying what " +
		"happened and by filing tasks for what needs doing, rather than by pushing a large " +
		"speculative change.\n\n")

	b.WriteString("**The run is on the branch this task is based on**, which the comment below also " +
		"names; your checkout is that branch. When a repository runs the same workflow on more than one " +
		"branch, say which one in your report and in any task you file, and file a fix against that branch.\n\n")

	b.WriteString("**Read the workflow file first, in your own checkout.** It is `.grain/workflows/" +
		stem + ".yml` (or `.yaml`), and it is the only trustworthy list of what this run consists " +
		"of: which jobs there are, what each one runs, how long each is allowed to take. Which jobs " +
		"a workflow has changes week to week, so do not work from a memory of them, from a previous " +
		"night's report, or from anything this prompt might have said about them -- this prompt " +
		"deliberately names none.\n\n")

	b.WriteString("**Find the run.** grain has left a comment on this task naming the run and how " +
		"it ended, and that comment says which of three nights this is. Read it before anything " +
		"else, because the three want opposite work out of you:\n\n")

	b.WriteString("- **There is no run to read** -- grain could not start one, or the run it " +
		"started is no longer known to this deployment. That is your whole report: say so, say " +
		"what the comment says about why, and stop. There is nothing to read and nothing to " +
		"diagnose from here.\n")
	b.WriteString("- **grain stopped waiting for a run that had not finished** -- it was started, " +
		"it was still going many hours later, and grain filed this review rather than hold the " +
		"schedule for ever. The run is there and it is unfinished, so read it: every job that did " +
		"finish has a log, and so does the job that did not. `grain ci log` follows a log that is " +
		"still being written, which is how you read the one that never ended. Which job it is and " +
		"what it was doing when it stopped producing output *is* the report -- a hang is the one " +
		"failure this suite cannot tell anybody about by itself, and the log of the job that never " +
		"ended is the only evidence of where it hangs. Do not report the conclusion grain recorded " +
		"as a verdict on the tree: it means grain never got a verdict out of this run.\n")
	b.WriteString("- **The run ended** -- the ordinary night, and the rest of this prompt is " +
		"about it. The conclusion in the comment is grain's record of how it ended, and the logs " +
		"are what it means. A run cut short by the daemon that was running it stopping is read " +
		"the same way, for whatever ran before it stopped, and reported as machinery rather than " +
		"as a verdict on the tree.\n\n")

	b.WriteString("Run `grain ci list` in your sandbox to find the run (narrow with `--repo` and " +
		"`--ref` to the branch the comment names) and `grain ci log <run-id> --job <j>` to read any " +
		"job's output; `grain ls` / `grain read ci://...` reach every file the run kept.\n\n")

	b.WriteString("**Nothing in this run was served from grain CI's cache.** grain asks for a " +
		"scheduled run with the cache bypassed, because the repetition is what a cadence is for: " +
		"even at a commit nothing has moved since the last firing, every job here really ran " +
		"again. So the durations are this night's own, a pass is this night's own, and a failure " +
		"is a finding rather than an older answer being shown to you a second time -- which is " +
		"also why comparing a job against the same job on earlier nights says something.\n\n")

	b.WriteString("**A night with no run, and a night whose run never ended, are each worth a task " +
		"of their own.** A suite that has silently stopped running is invisible from everywhere " +
		"else, and so is one that hangs: nothing but this review is going to notice either.\n\n")

	b.WriteString("**For each job that did not pass, read its log and distinguish the cause.** " +
		"Name the step, quote the error and the few lines around it, and classify whether the failure " +
		"is a repository code bug or an infrastructure / capability omission:\n" +
		"- **Capability or secret unavailability**: Missing deployment CI capabilities (such as " +
		"`github-sandbox`) or missing provider secrets / OAuth tokens (such as `CLAUDE_CODE_OAUTH_TOKEN` or API keys).\n" +
		"- **Infrastructure or runner preparation errors**: Jobs with duration `0s`, sandbox acquisition failures, " +
		"or jobs that errored before executing test steps.\n" +
		"- **External cloud constraints**: Cloud quota exhaustion or organization policy refusals (such as GCP private cluster org policies).\n" +
		"- **Repository code bugs**: Actual test assertion failures, build compilation errors, or runtime lint check failures within an executed step.\n\n")

	b.WriteString("**Automated Triage Guards for propose_task:**\n" +
		"- Only file code repair tasks with `propose_task` when an actual test assertion, build compilation, " +
		"or runtime lint check failed within an executed step in the repository code.\n" +
		"- For infrastructure and capability issues (missing deployment capabilities, missing secrets/tokens, " +
		"runner preparation errors, or cloud policy constraints): **Do NOT file a code modification task via `propose_task`.** " +
		"Proposing repository code changes for missing operator configuration or cloud infrastructure is wrong. " +
		"Instead, report these directly to the operator by leaving a closing note via `comment_on_issue` explaining " +
		"the missing operator configuration or capability grant.\n" +
		"- One task per distinct repository code bug, with the error output and your diagnosis in it, grouping related " +
		"failures with one cause into one task. A job that has failed once in a way that looks transient is not worth a task; " +
		"one that has failed repeatedly on repository code is worth one.\n\n")

	b.WriteString("**Only report things the user needs to address.** Scheduled workflows are most " +
		"likely to have one of three cases:\n" +
		"1) Everything is fine: Nothing needs to be fixed. File nothing and do not report to the user.\n" +
		"2) Clear repository bugs exist: File new code repair tasks with `propose_task` to fix them. Do not report to the user.\n" +
		"3) Infrastructure / capability failures or issues needing user intervention: Missing capabilities, secrets, " +
		"runner preparation errors, cloud policy refusals, or architectural changes. Report them with `comment_on_issue` " +
		"explaining what operator configuration or user intervention is required, and do not file code modification tasks.\n\n")

	b.WriteString("**Finish, rather than parking.** This task is half of one firing of the " +
		"schedule, and the firing is not over until it is closed: while this task is open the " +
		"cadence skips, so the next few nights do not run. If user intervention, missing capability/secret configuration, " +
		"or an architectural decision is required, report it with `comment_on_issue` and let this close. If everything was fine " +
		"or clear repository bugs were filed as tasks, finish cleanly without reporting to the user. Do not park on `ask_question`, " +
		"which holds up the nights behind it.")
	return b.String()
}
