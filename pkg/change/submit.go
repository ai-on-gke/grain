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

package change

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Stages S5 and S6 (changelists.md §6-§7, G3-6/G3-7): a person's Submit
// of a task's change, what each outcome does to the task, and the CL a
// poll finds submitted or abandoned outside grain. Like the earlier
// stages these speak through Provider and optional interfaces; core never
// names the edition answering.

// The outcomes a Submit or Sync that did not succeed reports through its
// error's ChangeOutcome.
const (
	OutcomeNeedsSync       = "needs_sync"
	OutcomeConflicts       = "conflicts"
	OutcomePresubmitFailed = "presubmit_failed"
	OutcomeError           = "error"
)

// Outcome is a Submit or Sync error that says which outcome it is.
type Outcome interface {
	ChangeOutcome() string
}

// ConflictReport is an Outcome naming the files that conflict.
type ConflictReport interface {
	ConflictingFiles() []string
}

// CheckReport is an Outcome naming the checks that failed.
type CheckReport interface {
	FailingChecks() []string
}

// UntrustedReport is an Outcome carrying the forge's own words, already
// wrapped as untrusted input by the provider.
type UntrustedReport interface {
	UntrustedDetail() string
}

// Failure is a Submit or Sync error read through the interfaces above.
type Failure struct {
	Outcome string
	Files   []string
	Checks  []string
	Detail  string
}

// FailureOf reads err. An error saying nothing of its outcome is
// OutcomeError.
func FailureOf(err error) Failure {
	f := Failure{Outcome: OutcomeError}
	if err == nil {
		return Failure{}
	}
	var o Outcome
	if errors.As(err, &o) && o.ChangeOutcome() != "" {
		f.Outcome = o.ChangeOutcome()
	}
	var c ConflictReport
	if errors.As(err, &c) {
		f.Files = c.ConflictingFiles()
	}
	var k CheckReport
	if errors.As(err, &k) {
		f.Checks = k.FailingChecks()
	}
	var u UntrustedReport
	if errors.As(err, &u) {
		f.Detail = u.UntrustedDetail()
	}
	return f
}

// SubmitGranter is a Provider that names the capability grant a person's
// Submit of target records on the task: the method and its arguments.
type SubmitGranter interface {
	SubmitGrant(target string) (capability string, with map[string]any)
}

// SubmitBlockers is why task's change, as obs last saw it, cannot be
// submitted now; none means the Submit button is live. There is no
// "submit anyway": every reason here has to clear first. observed is
// whether grain has an observation of the change at all.
func SubmitBlockers(task model.Task, obs Observation, observed bool) []string {
	var out []string
	add := func(s string) {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if !observed {
		return []string{"grain has not read this CL yet"}
	}
	switch obs.Status {
	case StatusSubmitted:
		return []string{"already submitted"}
	case StatusAbandoned:
		return []string{"abandoned"}
	}
	if obs.ReviewState != ReviewStateApproved {
		state := obs.ReviewState
		if state == "" {
			state = "unknown"
		}
		add("review is not approved (" + strings.ReplaceAll(state, "_", " ") + ")")
	}
	if obs.Presubmit != PresubmitPassed {
		p := obs.Presubmit
		if p == "" {
			p = PresubmitUnknown
		}
		if obs.Snapshot > 0 {
			add(fmt.Sprintf("required presubmit has not passed on snapshot %d (%s)", obs.Snapshot, strings.ReplaceAll(p, "_", " ")))
		} else {
			add("required presubmit has not passed (" + strings.ReplaceAll(p, "_", " ") + ")")
		}
	}
	switch obs.Health {
	case HealthConflicted:
		add("conflicts with head: sync it")
	case HealthBehind:
		add("behind head: sync it")
	}
	for _, r := range obs.Reasons {
		add(strings.TrimSpace(r))
	}
	for _, r := range GrainReviewBlockers(task) {
		add(r)
	}
	return out
}

// GrainReviewBlockers is grain's own review gate on task: the provider's
// ReviewGate (GateSubmit), and the holds and approvals that a pull
// request's merge waits on, which a CL's Submit waits on the same way.
func GrainReviewBlockers(task model.Task) []string {
	var out []string
	if ok, reason, err := GateSubmit(context.Background(), task); err != nil {
		out = append(out, err.Error())
	} else if !ok {
		if reason == "" {
			reason = "its required reviews have not passed on the current snapshot"
		}
		out = append(out, reason)
	}
	for _, l := range task.Links {
		switch l.Kind {
		case model.LinkSecurityHold:
			out = append(out, "a security review has not cleared it")
		case model.LinkHumanOnlyHold:
			out = append(out, "it is held for a person's review")
		case model.LinkReviewQuestions:
			out = append(out, "a grain review left questions open")
		}
	}
	for _, a := range model.PendingApprovals(task) {
		out = append(out, "approval pending: "+a.Rule)
	}
	return out
}

// Store is what recording a change's outcome writes: the daemon's store.
type Store interface {
	UpdateTask(ctx context.Context, id string, mutate func(*model.Task) error) error
	ObserveField(ctx context.Context, taskID string, now time.Time, set func(*model.Observation)) error
	AddComment(ctx context.Context, c model.Comment) (int64, error)
}

// SubmitPrincipal is who writes the notes a Submit's outcome leaves.
var SubmitPrincipal = model.Principal{Kind: model.PrincipalAutomation, ID: "cl-submit"}

// SubmittedTarget is the LinkChangelistSubmitted target for from
// submitted as to: to itself, or "to (was from)" when the forge
// renumbered it.
func SubmittedTarget(from, to string) string {
	if to == "" || to == from {
		return from
	}
	return to + " (was " + from + ")"
}

// submittedFrom is the CL a LinkChangelistSubmitted target was.
func submittedFrom(target string) string {
	if i := strings.Index(target, " (was "); i >= 0 {
		return strings.TrimSuffix(target[i+len(" (was "):], ")")
	}
	return target
}

// Settled reports whether task's CL target is submitted or abandoned.
func Settled(task model.Task, target string) bool {
	for _, l := range task.Links {
		switch l.Kind {
		case model.LinkChangelistSubmitted:
			if submittedFrom(l.Target) == target {
				return true
			}
		case model.LinkChangelistAbandoned:
			if l.Target == target {
				return true
			}
		}
	}
	return false
}

// LiveChanges is task's CLs that are neither submitted nor abandoned.
func LiveChanges(task model.Task) []string {
	var out []string
	for _, t := range model.TaskChangelists(task) {
		if !Settled(task, t) {
			out = append(out, t)
		}
	}
	return out
}

// RecordSubmitted records task's CL from as submitted as to (the number
// the forge reports, renumbered or not), posts message, and -- once no
// live CL remains -- closes the task as merged: ClosedAt and PrMergedAt,
// which is what merge metrics, pr-merged goals and tasks-closed until
// merged all read. outside is a submit grain did not make.
func RecordSubmitted(ctx context.Context, st Store, task model.Task, from, to, message string, outside bool, now time.Time) error {
	target := SubmittedTarget(from, to)
	last := false
	if err := st.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		if !slices.ContainsFunc(t.Links, func(l model.Link) bool {
			return l.Kind == model.LinkChangelistSubmitted && submittedFrom(l.Target) == from
		}) {
			t.Links = append(t.Links, model.Link{Kind: model.LinkChangelistSubmitted, Target: target})
		}
		last = len(LiveChanges(*t)) == 0
		return nil
	}); err != nil {
		return fmt.Errorf("recording %s submitted: %w", from, err)
	}
	if last {
		if err := st.ObserveField(ctx, task.ID, now, func(o *model.Observation) {
			if o.ClosedAt == nil {
				o.ClosedAt = &now
			}
			if o.PrMergedAt == nil {
				o.PrMergedAt = &now
			}
			if o.PrOpenedAt == nil {
				o.PrOpenedAt = &now
			}
		}); err != nil {
			return fmt.Errorf("closing %s as submitted: %w", task.ID, err)
		}
	}
	body := strings.TrimSpace(message)
	if outside {
		body = fmt.Sprintf("%s was submitted outside grain", from)
		if to != "" && to != from {
			body += " as " + to
		}
		body += "."
	} else if body == "" {
		body = fmt.Sprintf("Submitted %s", from)
		if to != "" && to != from {
			body += " as " + to
		}
		body += "."
	}
	if !last {
		body += fmt.Sprintf("\n\nThis task still has %s to submit: %s.", plural(len(LiveChanges(withLink(task, target))), "live CL"),
			strings.Join(LiveChanges(withLink(task, target)), ", "))
	}
	_, err := st.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: SubmitPrincipal}, Body: body, CreatedAt: now})
	return err
}

func withLink(task model.Task, submitted string) model.Task {
	task.Links = append(slices.Clone(task.Links), model.Link{Kind: model.LinkChangelistSubmitted, Target: submitted})
	return task
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// RecordAbandoned marks task's CL target abandoned. A task left with no
// live CL goes back to awaiting_reply (model.AwaitsAck reads a task with
// no change link as waiting on a person), with a comment saying why.
func RecordAbandoned(ctx context.Context, st Store, task model.Task, target string, now time.Time) error {
	if Settled(task, target) {
		return nil
	}
	live := 0
	if err := st.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkChangelistAbandoned, Target: target})
		live = len(LiveChanges(*t))
		if live == 0 {
			// The CL links stay for the record; with none live the task
			// no longer awaits a Submit.
			for i, l := range t.Links {
				if l.Kind == model.LinkChangelist {
					t.Links[i].Kind = model.LinkChangelistAbandoned
				}
			}
			t.Links = dedupeLinks(t.Links)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("recording %s abandoned: %w", target, err)
	}
	body := fmt.Sprintf("%s was abandoned outside grain.", target)
	if live == 0 {
		body += " This task has no live CL left, so it is waiting on a person: reopen it with a comment to have a run upload a new CL, or close it."
	}
	_, err := st.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: SubmitPrincipal}, Body: body, CreatedAt: now})
	return err
}

func dedupeLinks(links []model.Link) []model.Link {
	out := links[:0]
	seen := map[model.Link]bool{}
	for _, l := range links {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// Wake sends task back to the queue with note as the first thing its
// next run reads: a Submit that came back conflicted or with failing
// presubmit is an agent's to fix.
func Wake(ctx context.Context, st Store, task model.Task, note string, now time.Time) error {
	if err := st.ObserveField(ctx, task.ID, now, func(o *model.Observation) {
		o.CompletedAt = nil
		o.RetryRequestedAt = &now
	}); err != nil {
		return fmt.Errorf("queueing %s: %w", task.ID, err)
	}
	_, err := st.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: SubmitPrincipal}, Body: note, CreatedAt: now})
	return err
}

// ConflictNote is what a run woken for a conflicted Submit or Sync reads.
func ConflictNote(target string, files []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s conflicts with head", target)
	if len(files) > 0 {
		b.WriteString(" in:\n\n")
		for _, f := range files {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	} else {
		b.WriteString(". ")
	}
	b.WriteString("Resolve the conflicts with citc_resolve, then upload the result with open_pull_request. " +
		"A person presses Submit again once presubmit has passed on the new snapshot.")
	return b.String()
}

// PresubmitNote is what a run woken for a Submit refused on failing
// presubmit reads: the same thing a red wait_for_checks would say.
func PresubmitNote(target string, checks []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Submit of %s was refused: required presubmit failed", target)
	if len(checks) > 0 {
		b.WriteString(":\n\n")
		for _, c := range checks {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("\n")
	} else {
		b.WriteString(". ")
	}
	b.WriteString("Read the failures with pull_request_status, fix them, upload the fix with open_pull_request " +
		"and wait_for_checks until presubmit passes.")
	return b.String()
}

// ErrorNote is the comment a Submit that failed for any other reason
// leaves on a task that stays awaiting_submit.
func ErrorNote(target string, f Failure, err error) string {
	body := fmt.Sprintf("Submit of %s did not go through; the task is still waiting on Submit.", target)
	if d := strings.TrimSpace(f.Detail); d != "" {
		body += "\n\n" + d
	} else if err != nil {
		body += "\n\n" + err.Error()
	}
	return body
}
