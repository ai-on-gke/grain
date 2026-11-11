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
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ParkedOutcome is the task_run.outcome of a run that ended by parking
// its task through the wait_for_task tool: on other tasks, by adding them
// to its own depends-on links and stopping, so that the scheduler sends
// the task back out once every one of them has closed; or until a time
// (resume_at), by deferring its own task with that wake time, which
// orchestrator.reconcileWake undefers.
//
// The agent's own suspension (SuspendRequested, task_run.suspend_kind),
// which resume_later generalises (design/design/durable-tasks.design.md#DT-9): the
// run did what it meant to, and nothing about it is a failure for
// MaxConsecutiveFailures to count, nor a success that ends a streak --
// kept apart from "succeeded" so the
// next attempt's prompt can say it is resuming from a park, and which
// tasks it waited on, or until when (ParkedTargets and ParkedUntil read
// them back out of the detail ParkedDetail and ParkedUntilDetail write).
const ParkedOutcome = "parked"

// parkedDetailPrefix starts every ParkedOutcome run's detail. The ids
// follow it, space-separated, so ParkedTargets can read them back.
const parkedDetailPrefix = "parked: waiting on task(s) "

// ParkedDetail is the task_run.detail of a ParkedOutcome run.
func ParkedDetail(ids []string) string {
	return parkedDetailPrefix + strings.Join(ids, " ")
}

// ParkedTargets is the ids a ParkedOutcome run's detail names, or nil for
// any other detail. An optional ": <reason>" suffix (written when the park
// came from resume_later rather than wait_for_task) is stripped first.
func ParkedTargets(detail string) []string {
	rest, ok := strings.CutPrefix(detail, parkedDetailPrefix)
	if !ok {
		return nil
	}
	ids, _, _ := strings.Cut(rest, ": ")
	return strings.Fields(ids)
}

// parkedUntilPrefix starts the detail of a run that parked until a time.
// The wake time follows it in RFC 3339.
const parkedUntilPrefix = "parked: until "

// ParkedUntilDetail is the task_run.detail of a ParkedOutcome run that
// parked until a time rather than on tasks.
func ParkedUntilDetail(until time.Time) string {
	return parkedUntilPrefix + until.UTC().Format(time.RFC3339)
}

// ParkedUntil is the wake time a ParkedOutcome run's detail names, or
// false for any other detail. An optional ": <reason>" suffix (written
// when the timed park came from resume_later) is stripped before parsing.
func ParkedUntil(detail string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(detail, parkedUntilPrefix)
	if !ok {
		return time.Time{}, false
	}
	rawTime, _, _ := strings.Cut(rest, ": ")
	until, err := time.Parse(time.RFC3339, strings.TrimSpace(rawTime))
	if err != nil {
		return time.Time{}, false
	}
	return until.UTC(), true
}

// ResumeOn is what a requested suspension (SuspendRequested) waits for
// before its task is queued again: task_suspension.resume_on, as JSON.
// The task is re-queued when any one condition is met; a ResumeOn with
// none is the next cycle (design/design/durable-tasks.design.md#DT-9).
//
// wait_for_task's two parks are forms of it: park: true is Tasks (with
// UntilMerged), resume_at is At. CI and Message are resume_later's own.
type ResumeOn struct {
	// Tasks are task ids the task now depends on.
	Tasks       []string `json:"tasks,omitempty"`
	UntilMerged bool     `json:"until_merged,omitempty"`
	// At is a wake time: the task is deferred until it.
	At *time.Time `json:"at,omitempty"`
	// CI is a CI run id (grain CI or gha:<owner>:<name>:<run-id> for GitHub
	// Actions), or ResumeOnMyBranch for the latest verdict on the task's
	// own branch: the task is queued again once it has a verdict.
	CI string `json:"ci,omitempty"`
	// Head is the commit the task's branch was at when the CI wait was
	// taken, where grain could read it: the branch moving off it is a
	// verdict of its own, since whatever was being waited for is stale.
	Head string `json:"head,omitempty"`
	// Message is "or when anybody posts to this task".
	Message bool `json:"message,omitempty"`
	// Goal is "or when the task's goal moves it" (TaskGoal): what a goal
	// task rests on when its agent ends a run.
	Goal bool `json:"goal,omitempty"`
}

// ResumeOnMyBranch is ResumeOn.CI's word for the task's own branch.
const ResumeOnMyBranch = "my-branch"

// Empty reports whether r names no condition: resume on the next cycle.
func (r ResumeOn) Empty() bool {
	return len(r.Tasks) == 0 && r.At == nil && r.CI == "" && !r.Message && !r.Goal
}

// JSON is r as task_suspension.resume_on stores it, "" for none.
func (r ResumeOn) JSON() string {
	if r.Empty() {
		return ""
	}
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return string(b)
}

// ParseResumeOn reads task_suspension.resume_on back; "" is the empty
// ResumeOn.
func ParseResumeOn(raw string) (ResumeOn, error) {
	var r ResumeOn
	if strings.TrimSpace(raw) == "" {
		return r, nil
	}
	err := json.Unmarshal([]byte(raw), &r)
	return r, err
}

// Describe is r in a sentence's tail: "task #12 closes, or anybody posts".
func (r ResumeOn) Describe() string {
	var parts []string
	if len(r.Tasks) > 0 {
		ids := make([]string, len(r.Tasks))
		for i, id := range r.Tasks {
			ids[i] = "#" + id
		}
		verb := "closed"
		if r.UntilMerged {
			verb = "merged"
		}
		parts = append(parts, "task(s) "+strings.Join(ids, ", ")+" have "+verb)
	}
	if r.At != nil {
		parts = append(parts, r.At.UTC().Format(time.RFC3339)+" has passed")
	}
	if r.CI != "" {
		parts = append(parts, "CI ("+r.CI+") has a verdict")
	}
	if r.Message {
		parts = append(parts, "anybody posts to this task")
	}
	if len(parts) == 0 {
		return "the next cycle"
	}
	return strings.Join(parts, ", or ")
}

// requestedDetailPrefix starts the detail of a resume_later run that was
// not one of wait_for_task's parks, with ResumeOn.Describe after it.
const requestedDetailPrefix = "parked: resume_later until "

// RequestedDetail is the task_run.detail of a run that ended through
// resume_later with reason, waiting for r.
func RequestedDetail(reason string, r ResumeOn) string {
	d := requestedDetailPrefix + r.Describe()
	if reason = strings.TrimSpace(reason); reason != "" {
		d += ": " + reason
	}
	return d
}

// WaitTarget is what a task waiting on another one -- through the
// wait_for_task tool, or resuming from a park -- is told about it: its
// title, where it stands, how it ended, and the note its run closed on.
//
// Deliberately no more than that. Any run may wait on any task in the
// deployment, so what is handed back is what the task's own row already
// shows anybody, never its transcript or its conversation.
type WaitTarget struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State State  `json:"state"`
	// Closed is the task's own closure: what satisfies a depends-on
	// link, and what wait_for_task's until "closed" waits for.
	Closed bool `json:"closed"`
	// Merged is a closed task whose pull request merged -- until
	// "merged".
	Merged bool `json:"merged"`
	// PullRequestOpen is a closed task whose pull request grain has no
	// settled word on yet, and is still reading for one: closed within
	// ClosedPullRequestWindow, with neither a merge nor a close recorded
	// (Store.ClosedUnsettledPullRequestLinks). A person can still merge it
	// by hand, and orchestrator.syncClosedPullRequests would record that,
	// so a wait until "merged" goes on waiting through it.
	PullRequestOpen bool `json:"pullRequestOpen,omitempty"`
	// Outcome is one word for how a task ended (WaitOutcome*), or "" for
	// one that has not.
	Outcome string `json:"outcome,omitempty"`
	// RolledOverTo is the successor of a task closed by rolling it over
	// (WaitOutcomeRolledOver): the task carrying its work on.
	RolledOverTo string `json:"rolledOverTo,omitempty"`
	// ClosingNote is the last thing the task's own runs said in its
	// conversation, bounded to maxClosingNote.
	ClosingNote string `json:"closingNote,omitempty"`
}

// The words WaitTarget.Outcome uses.
const (
	// WaitOutcomeMerged is a task closed because its pull request merged.
	WaitOutcomeMerged = "merged"
	// WaitOutcomeUnmerged is a task whose pull request closed without
	// merging.
	WaitOutcomeUnmerged = "closed_unmerged"
	// WaitOutcomeCompleted is a task closed after its run finished --
	// an answer, a report, a review -- with no pull request to merge.
	WaitOutcomeCompleted = "completed"
	// WaitOutcomeCancelled is a task closed before any run finished it:
	// a human closed it, or it was superseded.
	WaitOutcomeCancelled = "cancelled"
	// WaitOutcomeFailed is a task that is not closed but has stopped:
	// MaxConsecutiveFailures attempts in a row failed (StateFailed), and
	// nothing retries it until a human does.
	WaitOutcomeFailed = "failed"
	// WaitOutcomeRolledOver is a durable task closed by rolling it over
	// to a successor (Store.Rollover). Its RolledOverTo names the
	// successor, which a wait on it was moved onto.
	WaitOutcomeRolledOver = RolloverOutcome
)

// ClosedPullRequestWindow is how long after a task closes grain goes on
// reading a pull request of its that had not merged or closed by then
// (orchestrator.syncClosedPullRequests). Past it nothing will record a
// merge, so a wait until "merged" on such a task stops waiting too.
const ClosedPullRequestWindow = 30 * 24 * time.Hour

// maxClosingNote bounds WaitTarget.ClosingNote, so a task that closed on
// a long report cannot crowd out the tool result or prompt quoting it.
const maxClosingNote = 4000

// Settled reports whether waiting on t any longer is pointless for the
// condition asked for, and whether that condition was met.
//
// "closed" is met by any closure, however the task ended, and by a
// failed task too: it has stopped, nothing retries it without a human,
// and the waiting run is better off deciding what to do about that now
// than waiting on somebody to notice. "merged" is met only by a merge,
// and a task closed without one has settled the question the other way
// -- unless its pull request is still open and grain is still reading it
// (PullRequestOpen): a task closed while the merge queue had given up on
// its pull request can still land by a person's hand, and grain records
// that merge when it does, so the wait goes on until the pull request
// merges or closes, or grain stops reading it.
func (t WaitTarget) Settled(untilMerged bool) (settled, met bool) {
	if untilMerged {
		if t.Merged {
			return true, true
		}
		return t.Closed && !t.PullRequestOpen, false
	}
	if t.Closed || t.State == StateFailed {
		return true, true
	}
	return false, false
}

// WaitTargetOf describes task id for a run waiting on it, or nil for an
// id that names no task. It is WaitTargetAt as of now.
func (s *Store) WaitTargetOf(ctx context.Context, id string) (*WaitTarget, error) {
	return s.WaitTargetAt(ctx, id, time.Now())
}

// WaitTargetAt is WaitTargetOf as of now, which decides whether a closed
// task's unsettled pull request is still inside ClosedPullRequestWindow.
func (s *Store) WaitTargetAt(ctx context.Context, id string, now time.Time) (*WaitTarget, error) {
	task, err := s.GetTask(ctx, id)
	if err != nil || task == nil {
		return nil, err
	}
	state, err := s.State(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	obs, err := s.GetObservation(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	target := &WaitTarget{ID: task.ID, Title: task.Title, State: state, Closed: state == StateClosed}
	switch {
	case target.Closed && RolledOverTo(*task) != "":
		// Its pull request, if it had one, went with the work to the
		// successor (Store.Rollover), so a merge is the successor's to
		// report.
		target.Outcome = RolloverOutcome
		target.RolledOverTo = RolledOverTo(*task)
	case target.Closed && obs != nil && obs.PrMergedAt != nil:
		target.Merged = true
		target.Outcome = WaitOutcomeMerged
	case target.Closed && obs != nil && obs.PrClosedAt != nil:
		target.Outcome = WaitOutcomeUnmerged
	case target.Closed && obs != nil && obs.CompletedAt != nil:
		target.Outcome = WaitOutcomeCompleted
		target.PullRequestOpen = pullRequestStillRead(*task, obs, now)
	case target.Closed:
		target.Outcome = WaitOutcomeCancelled
		target.PullRequestOpen = pullRequestStillRead(*task, obs, now)
	case state == StateFailed:
		target.Outcome = WaitOutcomeFailed
	}
	if target.Closed || state == StateFailed || state == StateCompleted ||
		state == StateAwaitingSubmit || state == StateAwaitingReply {
		note, err := s.latestClosingNote(ctx, task.ID)
		if err != nil {
			return nil, err
		}
		target.ClosingNote = note
	}
	return target, nil
}

// UntilMergedParkWaitsOn is the task an until "merged" park of taskID's
// is still waiting on as of now, or "" when it waits on none.
//
// A park on tasks holds its task through depends-on links, and task_ready
// lets a link go the moment its target closes, which for until "closed"
// is the whole wait. For until "merged" it is not, when the target closed
// with its pull request still open (WaitTarget.PullRequestOpen): that
// pull request can still be merged by hand, and the park is not over
// until it merges or closes, or grain stops reading it. This names such
// a target, for orchestrator's dispatch hold to keep the task back on.
//
// Only a park still standing counts -- the latest suspension of the task,
// not yet resumed, a SuspendRequested one waiting on nothing but tasks
// until "merged" -- and only a target the task still depends on, so that
// removing the dependency lets the task go, as it does any other park.
func (s *Store) UntilMergedParkWaitsOn(ctx context.Context, taskID string, now time.Time) (string, error) {
	susps, err := s.Suspensions(ctx, taskID)
	if err != nil || len(susps) == 0 {
		return "", err
	}
	last := susps[len(susps)-1]
	if last.Kind != SuspendRequested || last.ResumedAt != nil {
		return "", nil
	}
	on, err := ParseResumeOn(last.ResumeOn)
	if err != nil || !on.UntilMerged || len(on.Tasks) == 0 ||
		on.At != nil || on.CI != "" || on.Message || on.Goal {
		return "", nil
	}
	task, err := s.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return "", err
	}
	for _, id := range on.Tasks {
		if !dependsOn(*task, id) {
			continue
		}
		target, err := s.WaitTargetAt(ctx, id, now)
		if err != nil {
			return "", err
		}
		if target != nil && target.Closed && !target.Merged && target.PullRequestOpen {
			return id, nil
		}
	}
	return "", nil
}

// dependsOn reports whether task carries a depends-on link to id.
func dependsOn(task Task, id string) bool {
	for _, l := range task.Links {
		if l.Kind == LinkDependsOn && l.Target == id {
			return true
		}
	}
	return false
}

// pullRequestStillRead reports whether task, closed with neither a merge
// nor a close of its pull request recorded, is one
// ClosedUnsettledPullRequestLinks still lists as of now: it has a
// fixes-link, and closed inside ClosedPullRequestWindow.
func pullRequestStillRead(task Task, obs *Observation, now time.Time) bool {
	if obs == nil || obs.ClosedAt == nil || obs.PrMergedAt != nil || obs.PrClosedAt != nil {
		return false
	}
	if obs.ClosedAt.Before(now.Add(-ClosedPullRequestWindow)) {
		return false
	}
	for _, l := range task.Links {
		if l.Kind == LinkFixes {
			return true
		}
	}
	return false
}

// latestClosingNote is closingNote over taskID's conversation, read a page
// at a time from the newest back until one of its runs' comments turns up.
func (s *Store) latestClosingNote(ctx context.Context, taskID string) (string, error) {
	var before int64
	for {
		page, more, err := s.CommentsBefore(ctx, taskID, before, 32)
		if err != nil {
			return "", err
		}
		if note := closingNote(page, taskID); note != "" {
			return note, nil
		}
		if !more || len(page) == 0 {
			return "", nil
		}
		before = page[0].ID
	}
}

// closingNote is the most recent comment taskID's own runs posted, bounded
// to maxClosingNote -- the agent's own words (comment_on_issue,
// nothing_needed), never a human's, which are not the task's to report.
func closingNote(comments []Comment, taskID string) string {
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		by := c.Author.OnBehalfOf
		if by == nil || by.Kind != PrincipalAgent || by.ID != taskID || c.Handover {
			continue
		}
		body := strings.TrimSpace(c.Body)
		if len(body) > maxClosingNote {
			cut := maxClosingNote
			for cut > 0 && !utf8.RuneStart(body[cut]) {
				cut--
			}
			body = body[:cut] + fmt.Sprintf("\n[... %d more bytes not shown]", len(body)-cut)
		}
		return body
	}
	return ""
}

// Describe renders t as the lines a waiting run reads: one for where it
// stands, and its closing note quoted beneath.
func (t WaitTarget) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s %q: %s", t.ID, t.Title, t.State)
	if t.Outcome != "" {
		fmt.Fprintf(&b, ", outcome %s", t.Outcome)
	}
	if t.Closed {
		if t.Merged {
			b.WriteString(" (closed, its change merged)")
		} else if t.PullRequestOpen {
			b.WriteString(" (closed, but its pull request is still open: grain records a merge " +
				"if a person merges it by hand)")
		} else {
			b.WriteString(" (closed, nothing of it merged)")
		}
	}
	b.WriteString(".\n")
	if t.ClosingNote != "" {
		b.WriteString("Its closing note:\n")
		for _, line := range strings.Split(t.ClosingNote, "\n") {
			b.WriteString("> " + line + "\n")
		}
	}
	return b.String()
}
