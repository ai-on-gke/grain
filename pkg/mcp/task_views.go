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

package mcp

// task_views.go is read_grain_task's two compact views -- summary and
// conversation -- and the kind vocabulary list_grain_tasks filters by
// (docs/design/durable-tasks.md#DT-13). The full record is
// renderTaskRecord's, in task_tools.go.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// taskKindVocabulary is ui.TaskKinds restated, since pkg/ui imports this
// package: the tags list_grain_tasks' kind filter accepts.
var taskKindVocabulary = []string{"port", "repair", "session", "proposal", "durable", "imported"}

// summaryEventCount is how many of a task's latest events the summary
// carries, and the byte bounds keep each of them, and the handover's head,
// to a line or so.
const (
	summaryEventCount        = 3
	maxSummaryEventBytes     = 140
	maxSummaryHandoverBytes  = 300
	maxSummaryHandoverLines  = 4
	maxConversationComments  = 20
	maxConversationBodyBytes = 2 << 10
)

// renderTaskSummary is the summary view: what the task is and what last
// happened to it, in about a kilobyte.
func renderTaskSummary(t TaskRecord, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s: %s\n", t.ID, singleLine(t.Title))
	fmt.Fprintf(&b, "State: %s\n", t.State)
	if t.Stall != "" {
		fmt.Fprintf(&b, "  Stalled: %s\n", clipWords(singleLine(t.Stall), maxSummaryEventBytes*2))
	}
	priority := t.Priority
	if priority <= 0 {
		priority = 5
	}
	fmt.Fprintf(&b, "Priority: %d\n", priority)
	fmt.Fprintf(&b, "Kind: %s\n", kindWords(t.Kinds))
	if t.Repo != "" {
		fmt.Fprintf(&b, "Repo: %s (base %s)\n", t.Repo, blankAsNone(t.Base, "default branch"))
	}
	if len(t.DependsOn) > 0 {
		fmt.Fprintf(&b, "Depends on: %s", strings.Join(t.DependsOn, ", "))
		if len(t.BlockedBy) > 0 {
			fmt.Fprintf(&b, " (still open: %s)", strings.Join(t.BlockedBy, ", "))
		}
		b.WriteByte('\n')
	}
	if q := t.MergeQueue; q.State != "" && !strings.EqualFold(t.State, "closed") {
		fmt.Fprintf(&b, "Merge queue: %s (%s)%s", mergeQueuePhrase(q.State), q.State, sinceWords(q.At, now, ", since "))
		if q.StalledAt != nil {
			b.WriteString(", stalled")
		}
		if q.BlockedAt != nil {
			b.WriteString(", blocked until a person retries the merge")
		}
		b.WriteByte('\n')
	}
	if t.PullRequest != "" {
		fmt.Fprintf(&b, "Pull request: %s\n", t.PullRequest)
	}
	if events := lastTaskEvents(t, summaryEventCount); len(events) > 0 {
		b.WriteString("Last events:\n")
		for _, e := range events {
			fmt.Fprintf(&b, "  %s %s\n", e.at.UTC().Format(time.RFC3339), clipWords(e.text, maxSummaryEventBytes))
		}
	}
	if head := handoverHead(t.Comments); head != "" {
		b.WriteString("Handover (head):\n")
		b.WriteString(indent(head))
	}
	b.WriteString("(view \"full\" for the whole record, \"conversation\" to page the thread)")
	return b.String()
}

// kindWords is the kind tags as the summary prints them: "none" for a task
// with none, so a reader never has to infer a tag from an absence.
func kindWords(kinds []string) string {
	if len(kinds) == 0 {
		return "none"
	}
	return strings.Join(kinds, ", ")
}

type taskEvent struct {
	at   time.Time
	text string
}

// lastTaskEvents is the n latest things the record says happened to the
// task: attempts starting and ending, recorded closes and reopens, and
// comments.
func lastTaskEvents(t TaskRecord, n int) []taskEvent {
	var events []taskEvent
	for _, a := range t.Attempts {
		if a.FinishedAt != nil {
			text := fmt.Sprintf("attempt #%d ended %s", a.Number, blankAsNone(a.Outcome, "without an outcome"))
			if d := singleLine(a.Detail); d != "" {
				text += ": " + d
			}
			events = append(events, taskEvent{at: *a.FinishedAt, text: text})
		} else {
			events = append(events, taskEvent{at: a.StartedAt, text: fmt.Sprintf("attempt #%d started, still running", a.Number)})
		}
	}
	for _, tr := range t.Transitions {
		if (tr.State != "closed" && tr.State != "reopened") || (tr.By == "" && tr.Reason == "" && tr.Detail == "") {
			continue
		}
		text := tr.State
		if tr.By != "" {
			text += " by " + tr.By
		}
		if tr.Reason != "" {
			text += " (" + singleLine(tr.Reason) + ")"
		}
		if tr.Detail != "" {
			text += "; " + singleLine(tr.Detail)
		}
		events = append(events, taskEvent{at: tr.At, text: text})
	}
	for _, c := range t.Comments {
		text := fmt.Sprintf("comment #%d by %s (%s): %s", c.ID, c.Author, c.AuthorKind, singleLine(c.Body))
		if c.Handover {
			// Its head is quoted below the events, line by line.
			text = fmt.Sprintf("handover #%d written by %s (%s)", c.ID, c.Author, c.AuthorKind)
		}
		events = append(events, taskEvent{at: c.CreatedAt, text: text})
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	if len(events) > n {
		events = events[len(events)-n:]
	}
	return events
}

// handoverHead is the first lines of the task's latest handover comment,
// or "" for a task that has written none.
func handoverHead(comments []TaskComment) string {
	for i := len(comments) - 1; i >= 0; i-- {
		if !comments[i].Handover {
			continue
		}
		lines := strings.Split(strings.TrimSpace(comments[i].Body), "\n")
		if len(lines) > maxSummaryHandoverLines {
			lines = append(lines[:maxSummaryHandoverLines], "...")
		}
		return clipWords(strings.Join(lines, "\n"), maxSummaryHandoverBytes)
	}
	return ""
}

// renderTaskConversation is the conversation view: the comments after the
// given id, a page at a time, each body bounded, ending with where to read
// on from.
func renderTaskConversation(t TaskRecord, after int64) string {
	var page []TaskComment
	for _, c := range t.Comments {
		if c.ID > after {
			page = append(page, c)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s conversation", t.ID)
	if after >= 0 {
		fmt.Fprintf(&b, " after comment #%d", after)
	}
	fmt.Fprintf(&b, " (%d comment(s)):\n", len(page))
	more := len(page) > maxConversationComments
	if more {
		page = page[:maxConversationComments]
	}
	for _, c := range page {
		kind := c.AuthorKind
		if c.Handover {
			kind += ", handover"
		}
		fmt.Fprintf(&b, "  #%d [%s] %s (%s):\n", c.ID, c.CreatedAt.UTC().Format(time.RFC3339), c.Author, kind)
		b.WriteString(indent(clipNote(c.Body, maxConversationBodyBytes)))
	}
	if more {
		fmt.Fprintf(&b, "More follow: read again with after %d.", page[len(page)-1].ID)
	} else {
		b.WriteString("That is the end of the thread.")
	}
	return b.String()
}
