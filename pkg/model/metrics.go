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

// The store's read surface for measurement: every moment a task and its
// attempts passed through, in the flattest shape a report can be computed
// from. Nothing here is a new record -- every column read below is
// already written by the paths that own it (Client.CreateTask,
// Store.Approve, Store.StartRun/SetRunAgentStarted/FinishRun,
// orchestrator's own Observe) -- which is the whole point: throughput and
// latency are derivations of what already happened, so measuring them
// adds nothing to store and nothing to keep in sync.
//
// pkg/metrics turns these two slices into a report. The split is
// deliberate: reading rows is this package's job, and deciding what a
// percentile means is not.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// TaskTiming is one task's own timeline, flattened to the moments a
// report measures between: filed, queued, completed, closed, and -- for a
// task whose work went through a pull request -- opened and landed. It is
// not a Task -- nothing here needs a title, a target or a grant -- and it is
// deliberately not a state either: StateOf's answer is a snapshot of now,
// where these are the moments that snapshot moved.
//
// CreatedAt and ApprovedAt are both nilable because both genuinely are
// (Task.CreatedAt, Task.ApprovedAt): a task filed before either column
// existed has no such moment to report, which a report has to skip rather
// than guess at.
//
// Targeted and FixTaskFiled are the two facts here that are not moments,
// and they are here because the run-level measurements in pkg/metrics
// cannot be read without them. A run's own row says nothing about the
// task it was for: whether that task had a repo to push to at all --
// which is what decides whether its prompt even carried the
// push/check/repair paragraph, and so who belongs in the denominator of
// any measurement of that loop -- or whether the merge queue later had to
// repair it, which is the recorded form of "a red build was left behind"
// (Observation.MergeQueueRepairAt, or the older LinkFixTask). Both are
// derived, not stored: one reads a column the task already has, the other
// an observation column or an EXISTS over task_link.
//
// Repo and Author are the two facts that are not about measurement at
// all: they are what a report can be *scoped* by (metrics.Scope,
// grain/task-83). A report board asks "how is acme/widgets going" or
// "what is this schedule's work costing", and neither question can be
// answered from moments alone -- the rows have to say whose they are
// before a window can be taken over some of them. They are read here
// rather than by a second query for the reason everything else here is:
// one scan of `task` already happens per report, and a report scoped to
// a repo is the same scan with a smaller set kept.
type TaskTiming struct {
	TaskID      string
	CreatedAt   *time.Time
	ApprovedAt  *time.Time
	CompletedAt *time.Time
	ClosedAt    *time.Time
	// UndeferredAt is when this task was last picked back up
	// (Task.UndeferredAt), and nil for the great majority that were never
	// deferred. It is here for one measurement: the backlog's oldest wait
	// (metrics.backlogOf), which would otherwise measure a task put aside
	// for three months from the approval it went aside with, and report a
	// task that came back this morning as the thing that has been waiting
	// longest.
	UndeferredAt *time.Time
	// PrOpenedAt and PrMergedAt are the task's own tracked pull request's
	// two moments (Task.PrOpenedAt, Task.PrMergedAt), and nil for a task
	// that never opened one, one whose pull request is still open, and one
	// whose pull request closed without merging.
	//
	// They are here for the one measurement that cannot be taken from the
	// four moments above: when the work a task did actually *landed*
	// (metrics.Merges). CompletedAt is the agent finishing, which is not
	// the same event and can be hours earlier -- a review to wait for, a
	// queue to get through, a suite to pass -- and the difference is
	// precisely what somebody waiting on a change is waiting through.
	PrOpenedAt *time.Time
	PrMergedAt *time.Time
	// QueueEnteredAt is when the merge queue first took the task on
	// (Observation.MergeQueueEnteredAt), and nil for one it never did --
	// a change merged by hand, and every change that landed before the
	// stamp existed. PrMergedAt minus this is the change's time in the
	// queue, which PrOpenedAt cannot give: a pull request is opened long
	// before the agent that opened it stops working.
	QueueEnteredAt *time.Time
	// Targeted is whether the task named a repo to push to (Task.Target).
	Targeted bool
	// Repo is that target as "owner/name", or "" for a task with none --
	// Task.Target.String(), which is the spelling the UI's own `repo`
	// filter carries and so the spelling a board's stored query names a
	// repo by.
	Repo string
	// Author is who the task is attributed to
	// (Origin.Attribution.Actor.ID) -- the UI's `author` filter, again in
	// its own spelling, so that a query narrowed on screen scopes a
	// report without translation.
	Author string
	// FixTaskFiled is whether the merge queue ever had to repair this
	// task's branch -- Observation.MergeQueueRepairAt, the repair run it
	// asks the task itself for, or the LinkFixTask a database written
	// before that change records for the separate fix task the queue used
	// to file. Either spelling is the same fact, and both are counted so
	// that a report spanning the change does not show the rate falling to
	// zero on the day the recording moved.
	//
	// It is a fact about the task rather than about any one of its
	// attempts: it records that a red build outlived the run that pushed
	// it, not which attempt pushed it.
	FixTaskFiled bool
	// MergeQueueRepairAt is when the merge queue dispatched or requeued this
	// task for branch repair (Observation.MergeQueueRepairAt).
	MergeQueueRepairAt *time.Time
	// MergeQueueRepairReason is the failure or conflict note associated with
	// the repair (Observation.MergeQueueNote).
	MergeQueueRepairReason string
	// Port is how a port task was made: PortMechanical for one grain
	// cherry-picked itself (TagMechanicalPort), PortByAgent for any other
	// task carrying LinkPortOf, and "" for a task that is not a port. It
	// is what the merges report counts the ports an agent was spared by
	// (metrics.MergePorts).
	Port string
	// TemplateID is the template the task was filed from
	// (task_filed_template.template_id, or the older schedule:<id> tag),
	// and "" for a task filed without a template. It is what the agents
	// report groups wake telemetry by (metrics.Agents).
	TemplateID string
}

// The two values TaskTiming.Port takes for a port.
const (
	PortMechanical = "mechanical"
	PortByAgent    = "agent"
)

// RunTiming is one attempt's own timeline. AgentStartedAt splits it in
// two -- setup before, agent work after -- and is nil for a run still in
// setup and for one that never reached its agent at all (see schema.go on
// task_run.agent_started_at).
//
// RunID is the row's own id, carried because it is what the per-run tool
// census (RunToolUses, RunCheckWaits) is keyed by: those rows have no
// moment of their own, so a report windows them against the run they
// belong to, joining the two in memory rather than in SQL.
type RunTiming struct {
	RunID          string
	TaskID         string
	Attempt        int
	StartedAt      time.Time
	AgentStartedAt *time.Time
	FinishedAt     *time.Time
	Outcome        string
	// Detail is the short reason recorded beside the outcome. It is read
	// here for one purpose: EndingOf, which is what tells a run cancelled
	// by the wall-clock cap from one cancelled because a human closed its
	// task, and a run that exhausted its turn budget from any other
	// failure. Both pairs share an outcome string and differ only here.
	Detail string
	// ExecTransportFailures is how many of the run's sandbox tool calls
	// the cluster's exec tunnel failed after it had reached its agent
	// (ExecTransportFailure) -- zero for a run with none recorded.
	ExecTransportFailures int
}

// TaskTimings is every task's timeline, oldest id first.
//
// It reads the whole table rather than taking a window, and so does
// RunTimings, for a reason that is about correctness before it is about
// size: a report's window bounds which *measurements* it covers, not
// which rows it may look at. A task completed this morning may have been
// filed last month, and the latency worth reporting is exactly that
// distance; a run overlapping the window contributes to its occupancy
// whether or not it started inside it. Filtering rows by the window in
// SQL would drop precisely the samples that make the window interesting.
//
// The cost is one full scan of task and task_run per report, which is
// what a single-operator deployment can afford (Store.ListTasks already
// reads every task for every list) and what a much larger one would have
// to revisit -- a materialised roll-up, or a window widened at the query
// rather than in Go.
func (s *Store) TaskTimings(ctx context.Context) ([]TaskTiming, error) {
	var out []TaskTiming
	err := each(ctx, s.db,
		"SELECT `t`.`id`,`t`.`created_at`,`t`.`approved_at`,`t`.`undeferred_at`,"+
			"`o`.`completed_at`,`o`.`closed_at`,"+
			// The tracked pull request's own two moments, off the same
			// observation row the two above come from: what says when the
			// work landed rather than when the agent stopped.
			"`o`.`pr_opened_at`,`o`.`pr_merged_at`,`o`.`merge_queue_entered_at`,"+
			// Both target columns, since a scoped report needs the repo
			// itself and not just whether there was one. They are written
			// together -- a task with an owner and no name is not a shape
			// Task can take -- so the owner alone still decides Targeted.
			"`t`.`target_owner`,`t`.`target_name`,`t`.`origin_actor_id`,"+
			// EXISTS rather than a join, for the reason task_blocked's own
			// query gives: a task may carry several links and a join would
			// return the task once per link, multiplying every other row
			// here by something that has nothing to do with it.
			"(`o`.`merge_queue_repair_at` IS NOT NULL OR EXISTS (SELECT 1 FROM `task_link` AS `l` "+
			"WHERE `l`.`task_id` = `t`.`id` AND `l`.`kind` = ?)), "+
			"`o`.`merge_queue_repair_at`,`o`.`merge_queue_note`, "+
			// Whether it is a port, and whether grain made it itself.
			"EXISTS (SELECT 1 FROM `task_link` AS `pl` WHERE `pl`.`task_id` = `t`.`id` AND `pl`.`kind` = ?), "+
			"EXISTS (SELECT 1 FROM `task_tag` AS `pt` WHERE `pt`.`task_id` = `t`.`id` AND `pt`.`tag` = ?), "+
			"COALESCE((SELECT `ft`.`template_id` FROM `task_filed_template` AS `ft` WHERE `ft`.`task_id` = `t`.`id`), "+
			"(SELECT SUBSTR(`st`.`tag`, 10) FROM `task_tag` AS `st` WHERE `st`.`task_id` = `t`.`id` AND `st`.`tag` LIKE 'schedule:%' LIMIT 1), '') "+
			"FROM `task` AS `t` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id` "+
			"ORDER BY `t`.`id`", []any{string(LinkFixTask), string(LinkPortOf), TagMechanicalPort},
		func(rows *sql.Rows) error {
			var t TaskTiming
			var owner, name sql.NullString
			var created, approved, undeferred, completed, closed sql.NullTime
			var prOpened, prMerged, queueEntered sql.NullTime
			var repairAt sql.NullTime
			var repairNote sql.NullString
			var isPort, mechanical bool
			if err := rows.Scan(&t.TaskID, &created, &approved, &undeferred, &completed, &closed,
				&prOpened, &prMerged, &queueEntered, &owner, &name, &t.Author, &t.FixTaskFiled,
				&repairAt, &repairNote, &isPort, &mechanical, &t.TemplateID); err != nil {
				return err
			}
			t.Targeted = owner.Valid
			if owner.Valid {
				t.Repo = RepoRef{Owner: owner.String, Name: name.String}.String()
			}
			t.CreatedAt, t.ApprovedAt = timePtr(created), timePtr(approved)
			t.UndeferredAt = timePtr(undeferred)
			t.CompletedAt, t.ClosedAt = timePtr(completed), timePtr(closed)
			t.PrOpenedAt, t.PrMergedAt = timePtr(prOpened), timePtr(prMerged)
			t.QueueEnteredAt = timePtr(queueEntered)
			t.MergeQueueRepairAt = timePtr(repairAt)
			t.MergeQueueRepairReason = repairNote.String
			switch {
			case mechanical:
				t.Port = PortMechanical
			case isPort:
				t.Port = PortByAgent
			}
			out = append(out, t)
			return nil
		})
	return out, err
}

// RunPrompt is how big one attempt's prompt was, and whether grain had
// to cut the task's conversation down to fit it.
//
// It is the deployment-wide twin of Store.RunPromptSizes, which answers
// "is *this task's* prompt growing?" one task at a time. The same
// question about the whole deployment -- are prompts growing, and which
// tasks are the heavy ones -- needs a row per run rather than per
// attempt of one task, and needs nothing else: the prompt itself never
// leaves SQLite (Store.RunPrompts).
//
// ThreadCut is the half of this that survives the bound. From
// grain/task-268 on, the conversation a prompt carries is capped at
// orchestrator's maxConversationBytes, so every prompt recorded since is
// at most that plus grain's own sections and a distribution over them is
// censored at the cap -- see metrics.Prompts, which says how to read it.
type RunPrompt struct {
	RunID string
	Bytes int
	// ThreadCut is whether the prompt carries one of the notices grain
	// writes where it cut the conversation (PromptThreadCutNotice,
	// PromptMessageCutNotice). It is false for every prompt recorded
	// before those notices existed, which is a prompt that was never cut
	// because nothing was cutting -- Prompts.Runs is what says how many
	// prompts a count of cuts is over.
	ThreadCut bool
}

// RunPrompts is how big every recorded prompt was, oldest run first --
// the whole table, for the reason TaskTimings gives, and windowed by
// pkg/metrics against the run each row belongs to.
//
// Measured in the database, like RunPromptSizes and for the same reason
// written out there: a deployment's task_run table holds every prompt it
// has ever handed a run, hundreds of kilobytes each, and a report over
// them wants none of that text. `length(CAST(prompt AS BLOB))` because
// SQLite's length() counts characters on TEXT and a context window is
// spent in bytes; the two instr() tests are the cut notices, matched at
// the start of a line so that a prompt quoting one is not counted as one
// (PromptThreadCutNotice).
//
// A run that never reached its agent recorded no prompt and is left out
// rather than reported as a prompt of nought bytes -- "this run was
// never told anything" is not "this run was told nothing", and a zero
// here would drag every percentile drawn from it.
func (s *Store) RunPrompts(ctx context.Context) ([]RunPrompt, error) {
	var out []RunPrompt
	err := each(ctx, s.db,
		"SELECT `id`,length(CAST(`prompt` AS BLOB)),"+
			"(instr(`prompt`, ?) > 0 OR instr(`prompt`, ?) > 0) "+
			"FROM `task_run` WHERE `prompt` IS NOT NULL AND `prompt` <> '' "+
			"ORDER BY `id`",
		[]any{"\n" + PromptThreadCutNotice, "\n" + PromptMessageCutNotice},
		func(rows *sql.Rows) error {
			var p RunPrompt
			if err := rows.Scan(&p.RunID, &p.Bytes, &p.ThreadCut); err != nil {
				return err
			}
			out = append(out, p)
			return nil
		})
	return out, err
}

// RunTranscript is how big one attempt's transcript was in bytes.
type RunTranscript struct {
	RunID string
	Bytes int
}

// RunTranscripts is how big every recorded transcript was in bytes, oldest run
// first -- the whole table, windowed by pkg/metrics against the run each row
// belongs to.
func (s *Store) RunTranscripts(ctx context.Context) ([]RunTranscript, error) {
	var out []RunTranscript
	err := each(ctx, s.db,
		"SELECT `id`,length(CAST(`transcript` AS BLOB)) FROM `task_run` "+
			"WHERE `transcript` IS NOT NULL AND `transcript` <> '' ORDER BY `id`",
		nil,
		func(rows *sql.Rows) error {
			var t RunTranscript
			if err := rows.Scan(&t.RunID, &t.Bytes); err != nil {
				return err
			}
			out = append(out, t)
			return nil
		})
	return out, err
}

// CIJobTiming is one stored CI job's own timeline, flattened to what a
// report of this deployment's queueing measures between: how long the
// job waited for a machine, what held it, and how long it then worked.
//
// It is the CI half of TaskTiming and is read the same way -- columns
// that already exist, written by the paths that own them (ci.Engine
// through RunStore), joined here to the run's repository and branch
// because a wait nobody can attribute to a repository answers none of
// the questions worth asking about one (metrics.CIQueueing).
//
// StartedAt is nil for a job that never got a machine at all, which is
// the case the measurement was most needed for: its whole span is
// Queued, it did no work, and its FinishedAt is when it was given up on.
// A job still in flight has neither moment and contributes to no window.
type CIJobTiming struct {
	RunID string
	// Repo is the run's repository as "owner/name" -- ci_run.repo, which
	// is the spelling a metrics.Scope names a repo by, so a scoped report
	// needs no translation.
	Repo string
	Ref  string
	// Name is the job's display name, falling back to its workflow id for
	// a row written with neither -- what a breakdown by job is labelled
	// with, so it is never blank.
	Name string
	// Cached is whether this entry was served from the job cache. Such a
	// job took no machine and waited for nothing, and its timestamps are
	// an earlier run's (ci.JobRun.Cached), so a report counts it apart
	// rather than as a job that queued for no time at all.
	Cached     bool
	StartedAt  *time.Time
	FinishedAt *time.Time
	// Queued is how long it waited, QueuedCause the kind of thing that
	// held it longest and QueuedReason that thing's own sentence about
	// it (CIJob.Queued). The cause is empty on every job recorded before
	// the column existed and on a wait nothing attributed.
	Queued       time.Duration
	QueuedCause  string
	QueuedReason string
}

// CIJobTimings is every stored CI job, oldest run first -- the whole
// table, for the reason TaskTimings gives, and windowed by pkg/metrics
// against each job's own ending.
//
// The join is to ci_run for the repository and branch only: a job row
// says nothing about whose branch it was, and "which repositories wear
// this deployment's queueing" is half of what the report is for.
func (s *Store) CIJobTimings(ctx context.Context) ([]CIJobTiming, error) {
	var out []CIJobTiming
	err := each(ctx, s.db,
		"SELECT `j`.`run_id`,`r`.`repo`,`r`.`ref`,`j`.`name`,`j`.`job_id`,`j`.`cached`,"+
			"`j`.`started_at`,`j`.`finished_at`,`j`.`queued_ms`,`j`.`queued_cause`,`j`.`queued_reason` "+
			"FROM `ci_job` AS `j` JOIN `ci_run` AS `r` ON `r`.`id` = `j`.`run_id` "+
			"ORDER BY `j`.`run_id`,`j`.`job_index`", nil,
		func(rows *sql.Rows) error {
			var j CIJobTiming
			var jobID string
			var started, finished sql.NullTime
			var queuedMS int64
			if err := rows.Scan(&j.RunID, &j.Repo, &j.Ref, &j.Name, &jobID, &j.Cached,
				&started, &finished, &queuedMS, &j.QueuedCause, &j.QueuedReason); err != nil {
				return err
			}
			if j.Name == "" {
				j.Name = jobID
			}
			j.StartedAt, j.FinishedAt = timePtr(started), timePtr(finished)
			j.Queued = time.Duration(queuedMS) * time.Millisecond
			out = append(out, j)
			return nil
		})
	return out, err
}

// RunTimings is every attempt ever recorded, oldest first per task -- see
// TaskTimings on why this is the whole table.
func (s *Store) RunTimings(ctx context.Context) ([]RunTiming, error) {
	var out []RunTiming
	err := each(ctx, s.db,
		"SELECT `r`.`id`,`r`.`task_id`,`r`.`attempt`,`r`.`started_at`,`r`.`agent_started_at`,`r`.`finished_at`,"+
			"`r`.`outcome`,`r`.`detail`,COALESCE(`x`.`calls`, 0) FROM `task_run` `r` "+
			"LEFT JOIN `task_run_exec_failure` `x` ON `x`.`run_id` = `r`.`id` "+
			"ORDER BY `r`.`task_id`, `r`.`attempt`", nil,
		func(rows *sql.Rows) error {
			var r RunTiming
			var agentStarted, finished sql.NullTime
			var outcome, detail sql.NullString
			if err := rows.Scan(&r.RunID, &r.TaskID, &r.Attempt, &r.StartedAt,
				&agentStarted, &finished, &outcome, &detail, &r.ExecTransportFailures); err != nil {
				return err
			}
			r.AgentStartedAt, r.FinishedAt = timePtr(agentStarted), timePtr(finished)
			r.Outcome, r.Detail = outcome.String, detail.String
			out = append(out, r)
			return nil
		})
	return out, err
}

// GitHubCIOutcome is one recorded GitHub CI job as the metrics read it
// (metrics.GitHubCI): its identity, where it ran and how it concluded,
// and none of its text. A GitHubCIJob carries its output and failure
// tail, up to 32 KiB a row; a report counts conclusions and wants none
// of the words.
type GitHubCIOutcome struct {
	Repo       string
	CheckRunID int64
	Ref        string
	Commit     string
	Workflow   string
	JobName    string
	RunAttempt int
	Conclusion string
	// At is when the job concluded: its completion time, or when grain
	// recorded it for a report that did not say -- the same moment
	// ListGitHubCIJobs windows by.
	At time.Time
}

// JobKey is the job's identity across commits (GitHubCIJob.JobKey).
func (o GitHubCIOutcome) JobKey() string {
	return GitHubCIJob{Workflow: o.Workflow, JobName: o.JobName}.JobKey()
}

// GitHubCIOutcomes is every recorded GitHub CI job, oldest first -- the
// whole table (kept 14 days), for the reason TaskTimings gives, and
// windowed by pkg/metrics against each job's own conclusion. It reads
// github_ci_job only: grain's own CI (ci_run) is CIJobTimings', and the
// two never share a query.
func (s *Store) GitHubCIOutcomes(ctx context.Context) ([]GitHubCIOutcome, error) {
	var out []GitHubCIOutcome
	err := each(ctx, s.db,
		"SELECT `repo`,`check_run_id`,`ref`,`commit_sha`,`workflow`,`job_name`,`run_attempt`,`conclusion`,"+
			"`completed_at`,`recorded_at` FROM `github_ci_job` "+
			"ORDER BY COALESCE(`completed_at`, `recorded_at`), `check_run_id`", nil,
		func(rows *sql.Rows) error {
			var o GitHubCIOutcome
			var completed sql.NullTime
			var recorded time.Time
			if err := rows.Scan(&o.Repo, &o.CheckRunID, &o.Ref, &o.Commit, &o.Workflow, &o.JobName,
				&o.RunAttempt, &o.Conclusion, &completed, &recorded); err != nil {
				return err
			}
			o.At = recorded.UTC()
			if completed.Valid {
				o.At = completed.Time.UTC()
			}
			out = append(out, o)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading GitHub CI outcomes: %w", err)
	}
	return out, nil
}
