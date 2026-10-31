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

// tool_activity.go is what keeps a task's row saying something true while
// its run is going, and the half of it that costs the run nothing.
//
// update_status (status_tool.go) is a turn spent saying something instead
// of doing something, and the agent is told as much, so a run spends one
// rarely and by choice. run_command's own "description" fixed that for
// the calls a run makes most -- but only for those. A run that spends
// twenty minutes in read_grain_source, read_grain_task, get_ci_log or a
// blocked chat wrote nothing at all, so the row kept showing whatever was
// last said, which might be half an hour old and about a piece of work
// that finished long ago. The line was not merely missing; it was
// confidently wrong, and nothing on the row told a person reading it
// which of the two it was.
//
// So the registry narrates every call itself. Every tool a run can
// possibly reach goes through Registry.handleCall, which is one place
// rather than one per tool, and each tool's name is enough to say what
// the run is doing in the terms a person watching cares about: "reading
// grain's source", "filing a task", "waiting for a reply". The phrase is
// fixed per tool (toolActivityNotes) -- a single honest sentence beats an
// agent-written one that is only written when the agent remembers to
// write it -- with two exceptions, both of which are a better phrase
// rather than a different mechanism:
//
//   - run_command keeps its "description": the model has already composed
//     a phrase for exactly this, on the call that is about to become the
//     next few minutes of the run's life.
//   - update_status writes its own note from its handler, so the registry
//     stays out of its way rather than overwriting it a moment before.
//
// The other half of "the line is never stale" is not here and needs
// nothing here: a run that ends, is cancelled or is orphaned stops
// having a line at all
// rather than leaving its last one standing, and that is the store's
// doing -- model.Store.TaskActivity reads live runs only, and every
// ending path stamps finished_at (model.Store.FinishRun, including
// orchestrator.RecoverOrphanedRuns' "orphaned").

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// toolActivityTimeout bounds the hop to the daemon a narrated call makes
// before it runs.
//
// It exists because this hop is now on the path of every single tool call
// a run makes, unlike update_status', which is on the path of a call the
// agent chose to spend. A daemon that has stopped answering must cost
// each call a bounded pause and nothing more -- the call is the work, the
// row is a courtesy, and the courtesy never gets to hold up the work for
// longer than this.
const toolActivityTimeout = 5 * time.Second

// toolActivityRepeatWindow is how long a phrase just written is trusted
// to still be on the row, so that writing it again is skipped.
//
// The row is not the registry's alone: a run's capability servers write
// it too, from the daemon (capability_activity.go), and nothing tells
// this process when they have. A repeat skipped forever could leave the
// row naming a workspace edit for the whole of the test run the agent started
// after it, with the same description as the run before. Bounded, a
// burst of identical calls still costs one write, and a call that
// repeats one from longer ago than this writes its phrase again.
const toolActivityRepeatWindow = 30 * time.Second

// toolActivityNotes is what each tool says the run is doing while it
// runs: one phrase per tool, written for somebody who has not read the
// task and does not know what an MCP tool is.
//
// Present tense and no tool names, deliberately. The row is read by a
// person scanning a list of tasks to see which ones are moving, so "going
// through grain's own source" is the fact they want and
// "read_grain_source" is not.
//
// A name that is not here gets a machine-shaped phrase from
// toolActivityNote below rather than nothing -- an honest "calling
// whatever_it_was" is still a live line, where silence is what this file
// exists to remove. Tools grain itself serves should be listed, including
// the ones defined in pkg/capability (their names appear here rather than
// beside their definitions because the mapping is a property of the row,
// which is this package's, and because keeping it in one table is what
// makes it reviewable at a glance).
//
// "" is a meaningful entry: it means the tool writes the row itself and
// the registry must not write over it.
var toolActivityNotes = map[string]string{
	// Written by its own handler, from the agent's own phrase.
	"update_status": "",

	// The sandbox, both transports. run_command is narrated from its
	// "description" and only falls back to this when a call omits one.
	"run_command": "running a command in the sandbox",
	"read_file":   "reading a file in the sandbox",
	"write_file":  "writing a file in the sandbox",
	"edit_file":   "editing a file in the sandbox",

	// Reading CI, and waiting on it.
	"wait_for_checks":               "waiting for CI",
	"pull_request_status":           "checking CI on its own branch",
	"base_branch_status":            "checking CI on the base branch",
	"reply_to_pull_request_comment": "answering a review comment on its pull request",
	"resolve_review_thread":         "resolving a review thread on its pull request",
	"get_ci_log":                    "reading a CI log",
	"get_ci_artifact":               "downloading a CI artifact",
	"list_ci_runs":                  "looking through recent CI runs",
	"run_ci_job":                    "running a targeted CI job",
	"retrigger_ci":                  "retriggering grain CI",
	"query_test_history":            "querying test history",
	"grain_ls":                      "listing the grain namespace",
	"grain_read":                    "reading from the grain namespace",
	"grain_grep":                    "searching the grain namespace",
	"grain_copy":                    "copying from the grain namespace into the sandbox",

	// Acting on the run's own task, through the daemon.
	"open_pull_request": "opening the pull request",
	"recreate_sandbox":  "rebuilding the sandbox",
	"chat":              "waiting for a reply",
	"wait_for_task":     "waiting on another task",

	// The escape hatches: written now, relayed when the run ends.
	"ask_question":       "writing a question for a human",
	"request_secret":     "asking for a credential",
	"comment_on_issue":   "writing its closing note",
	"nothing_needed":     "closing the task as needing no work",
	"write_handover":     "writing a handover for the next run",
	"notify_operator":    "notifying the operator",
	"message_task":       "leaving a note for another task",
	"propose_task":       "filing a follow-up task",
	"add_review_comment": "writing review comments",

	// grain's memory.
	"update_memory":         "writing down what it learned",
	"read_memory_document":  "reading grain's memory",
	"list_memory_documents": "looking through grain's memory",

	// grain's account of itself, written once a week.
	"read_cluster_stats":  "counting what the cluster has done",
	"write_release_notes": "writing the week's release notes",

	// The self-debug grant: grain's own record.
	"list_grain_tasks":           "looking through grain's tasks",
	"read_grain_task":            "reading a grain task",
	"read_grain_task_prompt":     "reading a grain task's prompt",
	"read_grain_task_transcript": "reading a grain task's transcript",
	"read_grain_metrics":         "reading grain's own measurements",
	"read_pipeline_changes":      "reading grain's pipeline changes",
	"query_merge_queue":          "checking the merge queue",

	// The remaining grant-gated tools (pkg/capability).
	"read_grain_log":           "reading grain's log",
	"list_bootstrap_playbooks": "looking through the bootstrap playbooks",
	"read_bootstrap_playbook":  "reading a bootstrap playbook",
	"read_deployment_health":   "checking this deployment's health",
	"read_deployment_state":    "reading this deployment's state",
	"run_host_command":         "waiting to be allowed to run a command on the host",
	"list_issues":              "looking through GitHub issues",
	"read_issue":               "reading a GitHub issue",

	// The db-read and db-write grants (pkg/capability/dbaccess).
	"query_grain_db":   "querying grain database",
	"execute_grain_db": "executing statement in grain database",

	// The self-upgrade grant: this deployment's own upgrade.
	"deployment_upgrade_status": "reading this deployment's upgrade status",
	"upgrade_deployment":        "upgrading this deployment",
	"cancel_deployment_upgrade": "calling off this deployment's upgrade",
}

// capabilityActivityNotes is toolActivityNotes for the tools of the
// capability servers grain ships, keyed by the server's serverInfo.name
// (builtinCapabilityServer) and then by tool: what a call on one says the
// run is doing, after the server's name -- "workspace: editing
// repo/foo/bar.go". These are not grain's tools and never reach a
// Registry; capability_activity.go narrates them from the proxy they are
// reached through, and phrases a server not listed here from what its
// tools say of themselves.
//
// A phrase names the one argument a reader would ask about, where there
// is one, and falls back to a phrase without it when the call left it
// out or it is not fit to show (activityArg).
//
// A server built in somewhere other than core grain adds its own table
// with RegisterCapabilityToolActivity.
var capabilityActivityNotes = map[string]map[string]CapabilityToolNote{
	// pkg/capability/intree's gchat: one space per grant.
	"gchat": {
		"post_message": {Fallback: "posting to Google Chat"},
	},
}

// capabilityActivityMu guards capabilityActivityNotes against a
// registration racing a lookup.
var capabilityActivityMu sync.RWMutex

// RegisterCapabilityToolActivity sets the phrases for the tools of the
// built-in capability server whose serverInfo.name is server, replacing
// any registered before. It is for a package that ships a capability
// server, to call from init().
func RegisterCapabilityToolActivity(server string, notes map[string]CapabilityToolNote) {
	capabilityActivityMu.Lock()
	defer capabilityActivityMu.Unlock()
	table := make(map[string]CapabilityToolNote, len(notes))
	for tool, note := range notes {
		table[tool] = note
	}
	capabilityActivityNotes[server] = table
}

// capabilityToolNote is the registered phrase for tool on server, if any.
func capabilityToolNote(server, tool string) (CapabilityToolNote, bool) {
	capabilityActivityMu.RLock()
	defer capabilityActivityMu.RUnlock()
	note, ok := capabilityActivityNotes[server][tool]
	return note, ok
}

// declaredWaitTools are the tools that block on something outside the
// run -- CI, another task, a person -- for as long as that takes. A
// durable run inside one is not idle however quiet it is
// (orchestrator's idle watchdog, docs/design/durable-tasks.md#DT-1).
var declaredWaitTools = []string{"wait_for_checks", "run_ci_job", "wait_for_task", "chat", "run_host_command"}

// IsDeclaredWait reports whether note, a run's activity phrase as
// reportActivity writes it, says the run is inside a declared wait.
func IsDeclaredWait(note string) bool {
	if note == "" {
		return false
	}
	for _, tool := range declaredWaitTools {
		if toolActivityNote(tool, nil) == note {
			return true
		}
	}
	return false
}

// toolActivityNote is the phrase to put on the row for one call: the
// tool's own description where it wrote one, its fixed phrase otherwise,
// and "" for a tool that writes the row itself.
func toolActivityNote(tool string, args map[string]any) string {
	if tool == "run_command" {
		if note := describedActivityNote(args); note != "" {
			return note
		}
	}
	if note, ok := toolActivityNotes[tool]; ok {
		return note
	}
	// A tool this table has never heard of -- one added since, or one
	// proxied from somewhere else (managed_http.go). Naming it is
	// clumsier than a phrase but it is the truth, and it keeps the line
	// moving with the run.
	return "calling " + tool
}

// ToolActivityNote is the phrase a call on tool puts on its run's row,
// exported for the sandbox proxy (pkg/gitproxy/activity.go), which
// narrates the in-sandbox grain CLI's reads in the same words the MCP
// tools they replace use.
func ToolActivityNote(tool string) string {
	return toolActivityNote(tool, nil)
}

// toolActivity is where a Registry reports what it is about to run, and
// the phrase it last reported.
//
// last is remembered so a run that repeats itself does not repeat the
// hop: an agent reading twenty files in a row, or re-running the same
// test command with the same description, would otherwise write the same
// row twenty times. Guarded by a mutex because one registry can serve
// concurrent tools/call handlers, and a torn read here would only ever
// cost a redundant write, but a race detector's opinion of it is worth
// more than that.
type toolActivity struct {
	reporter StatusReporter
	now      func() time.Time

	mu     sync.Mutex
	last   string
	lastAt time.Time
}

// AnnounceActivity tells r where to report what the run it serves is
// doing, so that every tool call it answers puts a line on that run's
// task row for as long as the call lasts.
//
// reporter nil is "nobody to tell", which is every dispatch with no
// daemon behind it -- a bare `grain mcpserver -sandbox-root`, pkg/mcp's
// own tests, tests/e2e -- and leaves every call exactly as it was.
//
// Announced on the registry rather than wrapped around the tools for the
// same reason AnnounceDeadline is: the registry is the one place every
// call already passes through, and a wrapper would have to be applied at
// each of the dozen places pkg/cli/mcpserver.go registers a roster,
// where forgetting one is silent and looks exactly like the bug this
// fixes.
func (r *Registry) AnnounceActivity(reporter StatusReporter) {
	if reporter == nil {
		return
	}
	r.activity = &toolActivity{reporter: reporter, now: time.Now}
}

// reportActivity records what r is about to run, before it runs it: a row
// that only catches up once a ten-minute wait has finished is describing
// the one stretch nobody needed it for.
//
// Synchronous, ahead of the handler, so the ordering is the obvious one
// -- the row shows the call that is running, never the one before it --
// and the cost is a round trip against a call that is about to take
// anywhere from milliseconds to a quarter of an hour.
//
// Whatever the daemon answers is dropped. Nothing an agent could do with
// it justifies putting it in the result: the agent asked to read a file,
// not to write a row, and a paragraph about the daemon on the end of the
// one result it is actually reading is noise. The two things that can
// come back are already handled elsewhere -- a run whose task is no
// longer live has nobody watching the row anyway, and a daemon that
// cannot be reached at all still says so, out loud, the next time the run
// calls update_status itself.
func (r *Registry) reportActivity(ctx context.Context, tool string, args map[string]any) {
	if r.activity == nil {
		return
	}
	note := toolActivityNote(tool, args)
	if note == "" {
		return
	}
	// A declared wait always stamps its own start: the idle watchdog compares
	// that stamp against the transcript's modification time to tell a wait in
	// flight from one that has returned, so a second wait_for_checks in a row
	// must move the stamp past the first one's return.
	r.activity.mu.Lock()
	now := r.activity.now()
	skip := note == r.activity.last && now.Sub(r.activity.lastAt) < toolActivityRepeatWindow && !IsDeclaredWait(note)
	if !skip {
		r.activity.last, r.activity.lastAt = note, now
	}
	r.activity.mu.Unlock()
	if skip {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, toolActivityTimeout)
	defer cancel()
	_, _ = r.activity.reporter.ReportStatus(ctx, note)
}

// describedActivityNote shapes run_command's "description" into something
// the row can hold: whitespace collapsed onto the one line it is rendered
// on, and cut to MaxStatusLength.
//
// Cut rather than refused, which is the one place this parts company with
// update_status. There the length is the agent's mistake to fix, in the
// same turn, and saying so is the useful answer. Here the agent is not
// asking for anything and there is a command waiting to run behind the
// question, so an over-long description costs it a truncated row and
// nothing else -- and the daemon would reject the whole write otherwise
// (ui.MaxActivityLength), which would lose the phrase entirely rather
// than its tail.
func describedActivityNote(args map[string]any) string {
	note, _ := argString(args, "description")
	return clipActivity(strings.Join(strings.Fields(note), " "), MaxStatusLength)
}

// clipActivity is note cut to max runes, marked as cut.
func clipActivity(note string, max int) string {
	if utf8.RuneCountInString(note) <= max {
		return note
	}
	return strings.TrimRight(string([]rune(note)[:max-1]), " ") + "…"
}
