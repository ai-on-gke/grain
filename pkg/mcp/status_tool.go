package mcp

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxStatusLength is the longest synopsis update_status will send, in
// runes -- the same limit the daemon enforces on the write
// (ui.MaxActivityLength), stated here so a call that is too long is
// refused where the agent can still shorten it in the same turn rather
// than after a round trip.
//
// It is a display constraint: the phrase lands on a task row beside the
// title, and one that runs past a line stops being the glance it exists
// to be.
const MaxStatusLength = 120

// StatusReport is what one update_status call did: whether the run's task
// was still live to record it against.
//
// Live false is not an error. A run's status can only be shown while the
// run is happening, so a call that lands after grain has already finished
// the run has nothing to attach to -- the run has done nothing wrong, and
// the answer says so rather than failing.
type StatusReport struct {
	Live bool
}

// StatusReporter records a one-line synopsis of what the run this server
// serves is doing, against that run's own task.
//
// An interface, and one that names no task, for the reason
// PullRequestOpener and SandboxRecreator next door name none: which task
// a synopsis lands on is fixed at process start (`grain mcpserver
// -task`), never by anything in a tool call, so an agent can only ever
// describe its own run. The real implementation
// (pkg/cli/mcpserver.go's daemonStatus) asks the daemon over its REST
// API, since the row being written lives there.
type StatusReporter interface {
	ReportStatus(ctx context.Context, note string) (StatusReport, error)
}

// NewStatusTools returns the one tool a run gets when its dispatch can
// carry a synopsis back to the daemon: update_status.
//
// It exists because "running" is all a task said for as long as it ran.
// A run is dispatched, its row reads 'running', and half an hour later it
// still does -- with nothing on it to say whether that half hour went on
// a slow test suite, a wait for CI, or an agent going in circles, until
// somebody opens the transcript after the fact. The tool lets the run
// answer that question itself, in a phrase, while there is still time for
// the answer to be worth something.
//
// It is no longer the only writer of that row: the registry narrates
// every call it answers, from the tool's own name and from run_command's
// own "description", on the call the run was making anyway
// (tool_activity.go). This tool is what is left over -- what a tool name
// cannot say, which is why this file it is reading matters or what the
// last approach got wrong. Both write the same field and the last writer
// wins, which is the whole of the coordination between them; the
// registry's own narration steps around update_status rather than
// writing a generic phrase over a considered one.
//
// Like open_pull_request and recreate_sandbox, and unlike NewMockTools'
// escape hatches, this takes effect immediately rather than when the run
// ends: a synopsis relayed after the fact would describe a run nobody is
// still watching. Unlike either of those, it changes nothing about the
// run -- grain never reads the note back, dispatches nothing differently
// for it, and a run that never calls it is treated exactly as one that
// does. It is a line of text for a person, which is also why it is the
// one tool here that is safe to call as often as the run has something
// new to say.
//
// reporter nil returns the tool anyway, refusing every call -- what lets
// each agent framework's allowedTools enumerate the names this package
// registers without holding a live reporter, exactly as
// NewOpenPullRequestTools and NewRecreateSandboxTools do.
func NewStatusTools(reporter StatusReporter) []Tool {
	return []Tool{updateStatusTool(reporter)}
}

func updateStatusTool(reporter StatusReporter) Tool {
	return Tool{
		Name: "update_status",
		Description: "Tell the human watching what you are doing right now, in one " +
			"short phrase -- \"waiting for CI on the second push\", \"reading " +
			"pkg/orchestrator\", \"working out why the merge conflicts\". It is " +
			"shown on this " +
			"task's row in grain's UI for as long as your run lasts, and replaced " +
			"each time you call it, so a person can see what the run is spending " +
			"its time on without opening anything. Grain already writes that row " +
			"for every tool call you make, from the tool you called and from " +
			"run_command's own \"description\", so this tool is for what the tool " +
			"you are calling cannot say for itself: which package you are reading " +
			"your way around, why you have changed approach, what you are waiting " +
			"for. Do not spend a turn here repeating what the command you are " +
			"about to run will say for you -- it costs a turn like any other call. " +
			"Write it for somebody who has not read your task: name the thing, " +
			"not the tool. It changes nothing about your run: grain never reads " +
			"it back, it is not a substitute for comment_on_issue or " +
			"ask_question, and nothing you put here reaches the human as a " +
			"question or as a result. Keep it under " + fmt.Sprint(MaxStatusLength) +
			" characters and on one line.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"status": map[string]any{
					"type": "string",
					"description": "The one-line synopsis, at most " +
						fmt.Sprint(MaxStatusLength) + " characters. It replaces " +
						"whatever you last said, so write what is true now rather " +
						"than adding to a list.",
				},
			},
			"required": []string{"status"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if reporter == nil {
				return Result{
					Text: "This run cannot report its status (no route back to the grain " +
						"daemon that dispatched it). Carry on with your work: nothing " +
						"about the task depends on it.",
					IsError: true,
				}
			}
			note, _ := argString(args, "status")
			note = strings.Join(strings.Fields(note), " ")
			switch {
			case note == "":
				return Result{Text: "status is required: one short phrase saying what you are doing now",
					IsError: true}
			case utf8.RuneCountInString(note) > MaxStatusLength:
				return Result{
					Text: fmt.Sprintf("That status is %d characters; keep it under %d so it fits on "+
						"the task's row. Say what you are doing, not how you are doing it.",
						utf8.RuneCountInString(note), MaxStatusLength),
					IsError: true,
				}
			}
			report, err := reporter.ReportStatus(ctx, note)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			if !report.Live {
				return Result{Text: "Recorded nothing: grain no longer has this run in flight, so there " +
					"is no task row showing your status. Nothing is wrong with your work -- " +
					"finish what you are doing."}
			}
			return Result{Text: fmt.Sprintf("Status is now %q on this task's row, until you replace it "+
				"or your run ends.", note)}
		},
	}
}
