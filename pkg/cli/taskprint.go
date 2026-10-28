// taskprint.go is how the task verbs (taskcmd.go) print a task, a list
// of tasks and one task's detail.
package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/ui"
)

func (p *printer) tasks(tasks []ui.Task) {
	if p.json {
		p.encode(tasks)
		return
	}
	for _, t := range tasks {
		fmt.Println(taskLine(t))
	}
}

func (p *printer) task(t ui.Task) {
	if p.json {
		p.encode(t)
		return
	}
	fmt.Print(taskBlock(t))
}

func (p *printer) detail(d ui.TaskDetail) {
	if p.json {
		p.encode(d)
		return
	}
	fmt.Print(taskBlock(d.Task))
	if d.FailedAttempts > 0 {
		fmt.Printf("failed attempts: %d in a row", d.FailedAttempts)
		if d.LastFailureAt != nil {
			fmt.Printf(" (last at %s)", d.LastFailureAt.Format(time.RFC3339))
		}
		fmt.Println()
		if d.LastFailureReason != "" {
			fmt.Printf("last failure:    %s\n", d.LastFailureReason)
		}
	}
	// A task parked on a credential looks exactly like one parked on a
	// question from here -- awaiting_reply, with the request as the last
	// comment -- so the one thing this says is the one thing that
	// differs: what it is waiting for, and that answering it is a box in
	// the UI rather than a reply here. `grain secrets set` would store
	// the same value and leave the task parked, since nothing about a
	// write to the secret store tells this task it was answered.
	if d.PendingSecret != nil {
		fmt.Printf("waiting on secret: %s (stored as %s/%s, %s)\n",
			d.PendingSecret.Name, d.PendingSecret.Secret, d.PendingSecret.Key,
			map[bool]string{true: "already set", false: "not set"}[d.PendingSecret.Set])
		fmt.Println("                   set it in this task's pane in the UI -- that stores the value and queues the task again")
	}
	// grain's own CI, if this task's branch has been through it. The
	// verdict is printed rather than the run id alone, because the run
	// behind it is kept only for a while (model.CIStatus): "grain ci:
	// failure -- test: build" still answers the question once that run
	// has been pruned, where "see run 3f2a1c" would not.
	if d.CI != nil {
		switch {
		case d.CI.Conclusion == "":
			fmt.Printf("grain ci:        running (run %s)\n", d.CI.RunID)
		case d.CI.Detail != "":
			fmt.Printf("grain ci:        %s -- %s\n", d.CI.Conclusion, d.CI.Detail)
		default:
			fmt.Printf("grain ci:        %s\n", d.CI.Conclusion)
		}
	}
	// The conversation this task's last run left parked, which its next
	// one resumes (model.SuspendedSession). Said here as well as in the
	// UI pane because it changes what a `grain retry` or an approval is
	// about to do -- the next run carries on rather than starting over
	// -- and because the token is usable from the same shell this was
	// typed in.
	if d.SuspendedSession != nil {
		fmt.Printf("suspended session: %s (%s, left by %s at %s)\n",
			d.SuspendedSession.Token, d.SuspendedSession.Framework,
			d.SuspendedSession.RunID, d.SuspendedSession.At.Format(time.RFC3339))
		fmt.Println("                   this task's next run resumes it instead of starting from nothing")
	}
	if len(d.Transitions) > 0 {
		fmt.Println("\nhistory:")
		for _, tr := range d.Transitions {
			fmt.Printf("  -> %-*s %s\n", stateColumn, tr.State, tr.At.Format(time.RFC3339))
		}
	}
	// Each attempt, with the model it actually ran on: a task left to
	// the deployment's default list or the rotation can be given a
	// different one each time (ui.Attempt.Model).
	if len(d.Attempts) > 0 {
		fmt.Println("\nattempts:")
		for _, a := range d.Attempts {
			fmt.Println(attemptLine(a))
		}
	}
	for _, cm := range d.Comments {
		who := cm.Author
		if cm.OnBehalfOf != "" {
			// Grain relaying somebody else's words -- the distinction
			// model.Attribution exists to carry.
			who = fmt.Sprintf("%s on behalf of %s", cm.Author, cm.OnBehalfOf)
		}
		fmt.Printf("\n--- comment #%d by %s ---\n%s\n", cm.ID, who, cm.Body)
	}
}

// attemptLine is one attempt in `grain get`: its number, when it started,
// how it ended (or that it has not), and the model it ran on, where
// grain recorded one.
func attemptLine(a ui.Attempt) string {
	line := fmt.Sprintf("  #%d started %s", a.Number, a.StartedAt.Format(time.RFC3339))
	if a.FinishedAt != nil {
		line += ", finished " + a.FinishedAt.Format(time.RFC3339)
	} else {
		line += ", still running"
	}
	if a.Outcome != "" {
		line += ", " + a.Outcome
	}
	switch {
	case a.Model != "" && a.AgentFramework != "":
		line += fmt.Sprintf(", on %s (%s)", a.Model, a.AgentFramework)
	case a.Model != "":
		line += ", on " + a.Model
	}
	return line
}

// stateColumn is how much room a state is given in the two places one is
// printed in a column of its own -- `grain list`'s rows and the history
// in `grain get`. It is the width of the longest state a task can read
// as, "awaiting_submit", plus nothing: a shorter column does not
// truncate the state, it pushes every column after it one place right on
// exactly the rows that hold the long one, so a listing with an
// unsubmitted pull request in it is ragged from that row down.
// TestTaskLineColumnsLineUpForEveryState pins it against the vocabulary.
const stateColumn = 15

func taskLine(t ui.Task) string {
	repo := t.Repo
	if repo == "" {
		repo = "-"
	}
	return fmt.Sprintf("%-6s %-*s %-24s %s", t.ID, stateColumn, t.State, repo, t.Title)
}

func taskBlock(t ui.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", t.ID, t.Title)
	fmt.Fprintf(&b, "state:      %s\n", t.State)
	if t.Paused != nil {
		paused := fmt.Sprintf("by %s at %s (was %s)", t.Paused.By, t.Paused.At.Format(time.RFC3339), t.Paused.FromState)
		switch {
		case t.Paused.Immediate:
			paused += ", immediate"
		case t.Paused.GraceUntil != nil:
			paused += ", grace until " + t.Paused.GraceUntil.Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "paused:     %s\n", paused)
	}
	if t.Activity != "" {
		fmt.Fprintf(&b, "activity:   %s\n", t.Activity)
	}
	if t.Repo != "" {
		fmt.Fprintf(&b, "repo:       %s\n", t.Repo)
	}
	if t.Base != "" {
		fmt.Fprintf(&b, "base:       %s\n", t.Base)
	}
	if len(t.Reads) > 0 {
		fmt.Fprintf(&b, "reads:      %s\n", strings.Join(t.Reads, ", "))
	}
	if len(t.Writes) > 0 {
		// The branch beside each repo, since it is half of what the
		// declaration says: a repo listed here with no branch shown would
		// read as one whose base nobody had decided.
		names := make([]string, len(t.Writes))
		for i, w := range t.Writes {
			names[i] = w.Repo
			if w.Base != "" {
				names[i] += "@" + w.Base
			}
		}
		fmt.Fprintf(&b, "writes:     %s\n", strings.Join(names, ", "))
	}
	// When a deferred task picks itself back up (grain/task-313's wake
	// time, `grain defer -until`). Printed here because the flag that
	// sets it is the only place the CLI ever mentioned it: `grain get`
	// showed a bare "deferred", so the one thing an operator wants to
	// know about a task they put aside on a date -- which date -- was
	// readable only with -json.
	if t.DeferredUntil != nil {
		fmt.Fprintf(&b, "deferred until: %s\n", t.DeferredUntil.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "auto-merge: %t\n", t.AutoMerge)
	if len(t.Capabilities) > 0 {
		fmt.Fprintf(&b, "capabilities: %s\n", strings.Join(t.Capabilities, ", "))
	}
	if t.ReviewTemplateID != "" {
		// The name when there is one, since that is what a person picked
		// it by; the bare id when the template it names has gone, which
		// is worth seeing rather than hiding (ui.Task.ReviewTemplateName).
		review := t.ReviewTemplateName
		if review == "" {
			review = t.ReviewTemplateID + " (no such template)"
		}
		if t.ReviewTask != "" {
			review += ", filed as task " + t.ReviewTask
		}
		fmt.Fprintf(&b, "review:     %s\n", review)
	}
	// The deployment's own fields, by name and in name order: there is
	// no declaration to hand here (taskBlock is a function of the task
	// alone), so it prints what the task carries rather than a row per
	// field the way the detail pane does -- which is also what makes a
	// value whose declaration has since been deleted visible from here,
	// and so clearable (`grain update <id> -field <name>=`).
	if len(t.CustomFields) > 0 {
		values := make([]string, 0, len(t.CustomFields))
		for _, name := range slices.Sorted(maps.Keys(t.CustomFields)) {
			values = append(values, name+"="+strings.Join(t.CustomFields[name], ","))
		}
		fmt.Fprintf(&b, "fields:     %s\n", strings.Join(values, " "))
	}
	if t.Issue > 0 {
		fmt.Fprintf(&b, "issue:      #%d\n", t.Issue)
	}
	if t.PullRequest != "" {
		fmt.Fprintf(&b, "pull request: %s\n", t.PullRequest)
	}
	if t.MergeQueueBlockedAt != nil {
		fmt.Fprintf(&b, "merge queue blocked since: %s\n", t.MergeQueueBlockedAt.Format(time.RFC3339))
	}
	if t.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", t.Description)
	}
	return b.String()
}
