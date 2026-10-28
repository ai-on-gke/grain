package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
)

// MergeQueueReader is what query_merge_queue calls to inspect the merge
// queue, in-flight batches, and held gates.
type MergeQueueReader interface {
	QueryMergeQueue(ctx context.Context, repo, branch string) (MergeQueueReport, error)
}

const noMergeQueueReader = "This run cannot inspect the merge queue: it was dispatched with no route back " +
	"to the grain daemon that owns the merge queue state. Say so if it matters to what you were asked to do; " +
	"nothing you can do from here will change it."

// MergeQueueReport captures the complete state of the merge queue for a
// repo and branch.
type MergeQueueReport struct {
	Repo      string
	Branch    string
	Paused    bool
	PauseInfo string
	HeldGates []HeldGateReport
	Batches   []MergeBatchReport
	QueuedPRs []QueuedPRReport
}

// HeldGateReport is one gate holding a branch or queue back.
type HeldGateReport struct {
	Repo      string
	Branch    string
	Gate      string
	Status    string
	Reason    string
	Broken    bool
	Confirmed bool
}

// MergeBatchReport is one speculative merge batch in flight.
type MergeBatchReport struct {
	Repo       string
	Branch     string
	Ref        string
	BaseCommit string
	Commit     string
	RunID      string
	Conclusion string
	Detail     string
	QueuedAt   time.Time
	Members    []MergeBatchMemberReport
}

// MergeBatchMemberReport is one task/PR within a merge batch.
type MergeBatchMemberReport struct {
	TaskID string
	Number int
	Title  string
	Commit string
}

// QueuedPRReport is one task/PR waiting in the merge queue.
type QueuedPRReport struct {
	TaskID  string
	Number  int
	Title   string
	Repo    string
	Branch  string
	State   string
	Note    string
	At      *time.Time
	Stalled bool
	Blocked bool
	// MergePolicy is what the repository's merge rules require of this
	// change and which rule each requirement came from, one line each
	// (model.EffectiveMergePolicy.Describe); empty where it has none.
	MergePolicy string
}

var queryMergeQueueInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"repo": map[string]any{
			"type":        "string",
			"description": "Optional repository (owner/name) to inspect the merge queue for. Defaults to all repositories.",
		},
		"branch": map[string]any{
			"type":        "string",
			"description": "Optional branch to inspect the merge queue for. Defaults to all branches.",
		},
		"view": map[string]any{
			"type":        "string",
			"enum":        []any{mergeQueueViewCompact, mergeQueueViewFull},
			"description": "compact (the default: one line per gate, batch and queued change) or full (notes and merge rules too).",
		},
		"changed_since": map[string]any{
			"type": "string",
			"description": "Only batches and queued changes whose state moved since then: a span (30m, 24h) or a date " +
				"or RFC 3339 time. Held gates and the pause are always shown.",
		},
	},
}

const (
	mergeQueueViewCompact = "compact"
	mergeQueueViewFull    = "full"
)

// NewMergeQueueTools returns the query_merge_queue tool.
func NewMergeQueueTools(reader MergeQueueReader) []Tool {
	return []Tool{
		queryMergeQueueTool(reader),
	}
}

func queryMergeQueueTool(reader MergeQueueReader) Tool {
	return Tool{
		Name:        "query_merge_queue",
		Description: "Inspects current state of the merge queue: in-flight batches, queued pull requests, and held gates.",
		InputSchema: queryMergeQueueInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			if reader == nil {
				return Result{Text: noMergeQueueReader, IsError: true}
			}
			repo, _ := args["repo"].(string)
			branch, _ := args["branch"].(string)
			repo = strings.TrimSpace(repo)
			branch = strings.TrimSpace(branch)

			view, _ := args["view"].(string)
			view = strings.ToLower(strings.TrimSpace(view))
			if view == "" {
				view = mergeQueueViewCompact
			}
			if view != mergeQueueViewCompact && view != mergeQueueViewFull {
				return Result{Text: fmt.Sprintf("view %q is not compact or full", view), IsError: true}
			}
			var since time.Time
			if text, _ := args["changed_since"].(string); strings.TrimSpace(text) != "" {
				at, err := ci.ParseSince(strings.TrimSpace(text), time.Now())
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				since = at
			}

			report, err := reader.QueryMergeQueue(ctx, repo, branch)
			if err != nil {
				return Result{Text: fmt.Sprintf("Failed to query merge queue: %v", err), IsError: true}
			}
			hidden := 0
			if !since.IsZero() {
				report, hidden = mergeQueueChangedSince(report, since)
			}
			if view == mergeQueueViewFull {
				text := formatMergeQueueReport(report)
				if hidden > 0 {
					text += fmt.Sprintf("(%d batch(es) and queued change(s) that have not moved since %s are not shown.)\n",
						hidden, since.UTC().Format(time.RFC3339))
				}
				return Result{Text: text}
			}
			return Result{Text: formatMergeQueueCompact(report, since, hidden)}
		},
	}
}

func formatMergeQueueReport(r MergeQueueReport) string {
	var b strings.Builder
	title := "Merge Queue Status"
	if r.Repo != "" {
		title += fmt.Sprintf(" for %s", r.Repo)
		if r.Branch != "" {
			title += fmt.Sprintf(" (%s)", r.Branch)
		}
	} else if r.Branch != "" {
		title += fmt.Sprintf(" (branch: %s)", r.Branch)
	}
	b.WriteString("# " + title + "\n\n")

	// Pipeline pause / gates
	if r.Paused {
		b.WriteString("⚠️ **Pipeline Status:** PAUSED\n")
		if r.PauseInfo != "" {
			b.WriteString(fmt.Sprintf("Reason: %s\n", r.PauseInfo))
		}
		b.WriteString("\n")
	} else {
		b.WriteString("✅ **Pipeline Status:** Running\n\n")
	}

	if len(r.HeldGates) > 0 {
		b.WriteString("### 🛑 Held Gates\n")
		for _, g := range r.HeldGates {
			target := g.Repo
			if g.Branch != "" {
				target += fmt.Sprintf(" (%s)", g.Branch)
			}
			status := g.Status
			if g.Broken {
				status += " [BROKEN]"
			}
			if g.Confirmed {
				status += " [CONFIRMED]"
			}
			b.WriteString(fmt.Sprintf("- **%s**: Gate `%s` is %s\n", target, g.Gate, status))
			if g.Reason != "" {
				b.WriteString(fmt.Sprintf("  Detail: %s\n", g.Reason))
			}
		}
		b.WriteString("\n")
	}

	// Batches in flight
	if len(r.Batches) > 0 {
		b.WriteString(fmt.Sprintf("### 🚀 Batches in Flight (%d)\n", len(r.Batches)))
		for _, batch := range r.Batches {
			target := batch.Repo
			if batch.Branch != "" {
				target += fmt.Sprintf(" (%s)", batch.Branch)
			}
			b.WriteString(fmt.Sprintf("- **Batch `%s`** on %s (Commit: `%s`, Base: `%s`)\n", batch.Ref, target, truncateSHA(batch.Commit), truncateSHA(batch.BaseCommit)))
			if batch.RunID != "" {
				b.WriteString(fmt.Sprintf("  CI Run ID: `%s` | Conclusion: `%s`\n", batch.RunID, batch.Conclusion))
			}
			if batch.Detail != "" {
				b.WriteString(fmt.Sprintf("  Detail: %s\n", batch.Detail))
			}
			if len(batch.Members) > 0 {
				b.WriteString("  Members:\n")
				for _, m := range batch.Members {
					prStr := ""
					if m.Number > 0 {
						prStr = fmt.Sprintf("PR #%d ", m.Number)
					}
					b.WriteString(fmt.Sprintf("  - %s(Task %s): %s [`%s`]\n", prStr, m.TaskID, m.Title, truncateSHA(m.Commit)))
				}
			}
		}
		b.WriteString("\n")
	} else {
		b.WriteString("### 🚀 Batches in Flight\nNone\n\n")
	}

	// Queued PRs
	if len(r.QueuedPRs) > 0 {
		b.WriteString(fmt.Sprintf("### 📋 Queued Pull Requests (%d)\n", len(r.QueuedPRs)))
		for i, q := range r.QueuedPRs {
			prStr := ""
			if q.Number > 0 {
				prStr = fmt.Sprintf("PR #%d ", q.Number)
			}
			flags := ""
			if q.Blocked {
				flags += " [BLOCKED]"
			}
			if q.Stalled {
				flags += " [STALLED]"
			}
			if q.State == "waiting_github_approval" {
				// Out of the queue until somebody approves it on GitHub:
				// the note says who, and nothing here retries it.
				flags += " [WAITING FOR GITHUB APPROVAL]"
			}
			b.WriteString(fmt.Sprintf("%d. %s(Task %s)%s: %s\n", i+1, prStr, q.TaskID, flags, q.Title))
			b.WriteString(fmt.Sprintf("   Repo: %s | Branch: %s | State: `%s`\n", q.Repo, q.Branch, q.State))
			if q.Note != "" {
				b.WriteString(fmt.Sprintf("   Note: %s\n", q.Note))
			}
			if q.MergePolicy != "" {
				b.WriteString("   Merge rules:\n")
				for _, line := range strings.Split(strings.TrimRight(q.MergePolicy, "\n"), "\n") {
					b.WriteString("     " + line + "\n")
				}
			}
		}
		b.WriteString("\n")
	} else {
		b.WriteString("### 📋 Queued Pull Requests\nNone\n\n")
	}

	if !r.Paused && len(r.HeldGates) == 0 && len(r.Batches) == 0 && len(r.QueuedPRs) == 0 {
		b.WriteString("The merge queue is currently idle and clear.\n")
	}

	return b.String()
}

func truncateSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// mergeQueueChangedSince keeps the batches and queued changes whose state
// moved at or after since, and counts the rest.
func mergeQueueChangedSince(r MergeQueueReport, since time.Time) (MergeQueueReport, int) {
	hidden := 0
	var batches []MergeBatchReport
	for _, b := range r.Batches {
		if b.QueuedAt.Before(since) {
			hidden++
			continue
		}
		batches = append(batches, b)
	}
	var queued []QueuedPRReport
	for _, q := range r.QueuedPRs {
		if q.At == nil || q.At.Before(since) {
			hidden++
			continue
		}
		queued = append(queued, q)
	}
	r.Batches, r.QueuedPRs = batches, queued
	return r, hidden
}

// formatMergeQueueCompact is the compact view: one line per gate, batch
// and queued change, ids first, so a wake can cite what it read.
func formatMergeQueueCompact(r MergeQueueReport, since time.Time, hidden int) string {
	var b strings.Builder
	scope := "all repositories"
	if r.Repo != "" {
		scope = r.Repo
	}
	if r.Branch != "" {
		scope += "@" + r.Branch
	}
	if r.Paused {
		fmt.Fprintf(&b, "merge queue %s: dispatch PAUSED", scope)
		if r.PauseInfo != "" {
			fmt.Fprintf(&b, " (%s)", singleLine(r.PauseInfo))
		}
	} else {
		fmt.Fprintf(&b, "merge queue %s: running", scope)
	}
	fmt.Fprintf(&b, "; %d gate(s) held, %d batch(es), %d queued\n", len(r.HeldGates), len(r.Batches), len(r.QueuedPRs))
	for _, g := range r.HeldGates {
		line := fmt.Sprintf("gate %s@%s %s %s", g.Repo, g.Branch, g.Gate, g.Status)
		if g.Broken {
			line += " broken"
		}
		if g.Confirmed {
			line += " confirmed"
		}
		b.WriteString(line + "\n")
	}
	for _, batch := range r.Batches {
		verdict := batch.Conclusion
		if verdict == "" {
			verdict = "running"
		}
		members := make([]string, 0, len(batch.Members))
		for _, m := range batch.Members {
			members = append(members, taskRef(m.TaskID))
		}
		fmt.Fprintf(&b, "batch %s@%s run %s %s since %s: %s\n", batch.Repo, batch.Branch,
			blankAsNone(batch.RunID, "-"), verdict, batch.QueuedAt.UTC().Format(time.RFC3339), strings.Join(members, ", "))
	}
	for i, q := range r.QueuedPRs {
		line := fmt.Sprintf("%d. %s", i+1, taskRef(q.TaskID))
		if q.Number > 0 {
			line += fmt.Sprintf(" PR #%d", q.Number)
		}
		line += fmt.Sprintf(" %s@%s %s", q.Repo, q.Branch, q.State)
		if q.Stalled {
			line += " stalled"
		}
		if q.Blocked {
			line += " blocked"
		}
		if q.At != nil {
			line += " since " + q.At.UTC().Format(time.RFC3339)
		}
		b.WriteString(line + "\n")
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "(%d batch(es) and queued change(s) unchanged since %s not shown)\n", hidden, since.UTC().Format(time.RFC3339))
	}
	b.WriteString("(view \"full\" adds notes and merge rules)")
	return b.String()
}
