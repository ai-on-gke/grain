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

// pipeline_tools.go is read_pipeline_changes: the delta feed a judgement
// agent starts each wake from (design/design/durable-tasks.design.md#DT-13). One
// line per thing that changed since a cursor -- a task's transition, a
// merge-queue move, a stall, a base-branch verdict, a merge batch's
// result -- and the cursor to read on from, so a wake reads what moved
// rather than sweeping every task to find it.
//
// It sits behind self-debug beside the task readers (task_tools.go) and
// for their reason: everything it says is on a page anyone with the UI
// open can read, and nothing here changes anything. The feed is built by
// the daemon (pkg/ui's GET /api/pipeline/changes) from the records it
// already keeps; this side only renders it.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PipelineChange is one entry of the feed (ui.PipelineChange).
type PipelineChange struct {
	At     time.Time
	ID     string
	Kind   string
	From   string
	To     string
	Detail string
}

// PipelineChangesPage is one page of it: the entries, the cursor to pass
// as after next time, and whether the limit cut the page.
type PipelineChangesPage struct {
	Changes []PipelineChange
	Next    string
	More    bool
	Since   time.Time
}

// PipelineReader is the one read these tools need.
type PipelineReader interface {
	PipelineChanges(ctx context.Context, after string, limit int) (PipelineChangesPage, error)
}

// NewPipelineTools returns read_pipeline_changes. reader nil returns the
// tool anyway, refusing every call with a sentence -- NewTaskTools'
// contract.
func NewPipelineTools(reader PipelineReader) []Tool {
	return []Tool{readPipelineChangesTool(reader)}
}

// defaultPipelineChangeLimit is the page a call with no limit gets: a
// half-hour wake on a busy deployment is a few dozen entries.
const defaultPipelineChangeLimit = 100

// maxPipelineDetailBytes bounds the one free-text field an entry carries.
const maxPipelineDetailBytes = 80

func readPipelineChangesTool(reader PipelineReader) Tool {
	return Tool{
		Name:          "read_pipeline_changes",
		ElisionAdvice: "pass a smaller \"limit\" and page on with the cursor each answer ends with",
		Description: "Read what changed in this grain deployment's pipeline since a cursor, one line per change, " +
			"oldest first: task transitions (filed, running, ended, closed, merged), merge-queue moves, " +
			"stalls (a task whose state outlived its patience, a merge-queue entry stalled or blocked), " +
			"base-branch verdicts and merge-batch results. Each line is the id, the kind, from→to and when. " +
			"The answer ends with the cursor to pass as \"after\" next time -- write it into your handover. " +
			"With no \"after\" it reads the last 24 hours. Read-only. Start a wake here and read nothing " +
			"else unless a line names it.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"after": map[string]any{"type": "string",
					"description": "the cursor the last read ended with (<time>~<seq>), or an RFC 3339 time; omit for the last 24 hours"},
				"limit": map[string]any{"type": "number",
					"description": fmt.Sprintf("at most this many lines (default %d, at most 500)", defaultPipelineChangeLimit)},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if reader == nil {
				return Result{Text: noTaskReader, IsError: true}
			}
			after, _ := argString(args, "after")
			limit := 0
			if f, ok := argFloat(args, "limit"); ok && int(f) > 0 {
				limit = int(f)
			}
			text, err := ReadPipelineChangesText(ctx, reader, after, limit, nil)
			if err != nil {
				return Result{Text: fmt.Sprintf("Error reading pipeline changes: %v", err), IsError: true}
			}
			return Result{Text: text}
		},
	}
}

// ReadPipelineChangesText is one page of the feed after the cursor after
// ("" for the last 24 hours), at most limit entries (0 for the default
// page), as read_pipeline_changes prints it. next words the last line
// from the page's cursor -- `grain read`'s own command to run next, say;
// nil prints the bare cursor, which is what the tool's "after" takes.
func ReadPipelineChangesText(ctx context.Context, reader PipelineReader, after string, limit int, next func(cursor string) string) (string, error) {
	after = strings.TrimSpace(after)
	if limit <= 0 {
		limit = defaultPipelineChangeLimit
	}
	page, err := reader.PipelineChanges(ctx, after, limit)
	if err != nil {
		return "", err
	}
	if next == nil {
		next = func(cursor string) string { return cursor }
	}
	return renderPipelineChanges(page, after == "", next), nil
}

// renderPipelineChanges is one line per entry and the cursor last, which
// is the one line a reader must not lose.
func renderPipelineChanges(page PipelineChangesPage, fromLookback bool, next func(cursor string) string) string {
	var b strings.Builder
	if len(page.Changes) == 0 {
		if fromLookback {
			fmt.Fprintf(&b, "Nothing has changed since %s.\n", page.Since.UTC().Format(time.RFC3339))
		} else {
			b.WriteString("Nothing has changed since that cursor.\n")
		}
	}
	for _, c := range page.Changes {
		b.WriteString(PipelineChangeLine(c))
		b.WriteByte('\n')
	}
	if page.More {
		b.WriteString("(more changes follow: read again with this cursor)\n")
	}
	fmt.Fprintf(&b, "next: %s", next(page.Next))
	return b.String()
}

// PipelineChangeLine is one entry as the feed prints it: id, kind,
// from→to, at, and the detail when there is one.
func PipelineChangeLine(c PipelineChange) string {
	id := c.ID
	if c.Kind == "task" || c.Kind == "queue" || c.Kind == "stall" {
		id = taskRef(c.ID)
	}
	line := fmt.Sprintf("%s %s %s→%s %s", id, c.Kind, c.From, c.To, c.At.UTC().Format(time.RFC3339))
	if d := singleLine(c.Detail); d != "" {
		line += " (" + clipWords(d, maxPipelineDetailBytes) + ")"
	}
	return line
}

// clipWords cuts s to about limit bytes on a rune boundary, marking the cut.
func clipWords(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := []rune(s)
	for len(string(cut)) > limit {
		cut = cut[:len(cut)-1]
	}
	return string(cut) + "…"
}

// taskRef is a task id the way a report cites it: "task 832" for a bare
// number, and the id itself for one that already says what it is.
func taskRef(id string) string {
	if _, err := strconv.Atoi(id); err == nil {
		return "task " + id
	}
	return id
}
