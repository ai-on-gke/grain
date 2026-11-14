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

// Package selfrepair builds the tool that the
// https://grain.dev/capabilities/grain#host-exec grant (once the bare
// "self-repair" capability; design/design/unified-grants.design.md §4) adds to a
// run: run_host_command, to run shell commands directly on the host
// grain's own controller runs on -- restart a systemd unit, poke at
// grain's own config, call the grain CLI itself, anything a person at
// that host's own terminal could do -- bwsalmon/agents#540's
// "configuration mode." The grant itself is a dangerous method on the
// in-process grain server (pkg/capability/grainserver), which only a
// person may give; this package holds no provider of its own.
//
// Unlike selfdebug's read-only tools, everything here is capable of
// changing grain's own running state, so every call is gated on a live
// human reply in the task's own chat before it runs -- this package's
// first real instance of a tool that pauses mid-run for a person to
// approve or refuse it, rather than a grant a human decided about once,
// before the run ever started. See Confirm's own doc comment for how
// that pause works, and orchestrator/cycle.go's runOne for why this
// capability's tools are only ever added to an Interactive task's run:
// nobody is necessarily watching a non-interactive task's chat closely
// enough to answer within Confirm's own timeout.
package selfrepair

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/procgroup"
)

// CapabilityName is the retired bare grant name this package's tool
// was once offered under; schema migration 84 rewrote every stored one,
// and it grants nothing any longer.
const CapabilityName = "self-repair"

// DefaultPollInterval is how often Confirm re-reads a task's comments
// while waiting on a reply, absent a caller-supplied interval.
const DefaultPollInterval = 5 * time.Second

// DefaultConfirmTimeout is how long Confirm waits for a reply before
// giving up and refusing, absent a caller-supplied timeout -- long
// enough for a person to notice a chat notification and type a few
// words, short enough that a run nobody is watching does not tie up its
// dispatch slot indefinitely.
const DefaultConfirmTimeout = 15 * time.Minute

// HostCommandTools returns the one tool a self-repair grant adds to a
// run: run_host_command. store and taskID let it post its confirmation
// question into, and poll for a reply from, that task's own
// conversation; pollInterval and timeout are DefaultPollInterval/
// DefaultConfirmTimeout with 0.
func HostCommandTools(store *model.Store, taskID string, pollInterval, timeout time.Duration) []mcp.Tool {
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}
	if timeout <= 0 {
		timeout = DefaultConfirmTimeout
	}
	return ConfirmedHostCommandTools(func(ctx context.Context, question string) (bool, string, error) {
		return Confirm(ctx, store, taskID, question, pollInterval, timeout)
	})
}

// Confirmer asks a person on the task's own chat whether to go ahead,
// and reports what they said: Confirm's own contract -- approved is
// false, with reply saying why, for a refusal, a timeout or a cancelled
// ctx -- whatever route it takes to that chat. Confirm closes over a
// store; a forked "grain mcpserver" holding none asks the daemon over
// its REST API instead (pkg/daemon's daemonHostExecConfirm).
type Confirmer func(ctx context.Context, question string) (approved bool, reply string, err error)

// ConfirmSummary is the one line a confirmation question is shown by in
// the inbox, the task list and notifications (model.Comment.Summary).
const ConfirmSummary = "Run a command on grain's own host?"

// ConfirmQuestion is what run_host_command asks before running command.
func ConfirmQuestion(command string) string {
	return fmt.Sprintf(
		"This task wants to run this command directly on grain's own host:\n\n```\n%s\n```\n\n"+
			"Reply **approve** to run it, or **deny** (optionally with a reason) to refuse.", command)
}

var runHostCommandInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"command": map[string]any{
			"type":        "string",
			"description": "shell command to run on grain's own host, as the same user grain's controller runs as",
		},
	},
	"required": []string{"command"},
}

// ConfirmedHostCommandTools is run_host_command, asking confirm before
// every command it runs and running nothing confirm does not approve. A
// nil confirm builds the tool only for its name and schema
// (agent.PublishedToolNames); every call to it is refused.
func ConfirmedHostCommandTools(confirm Confirmer) []mcp.Tool {
	return []mcp.Tool{{
		Name: "run_host_command",
		Description: "Run a shell command directly on the host grain's own controller runs on -- not a sandbox, " +
			"the real machine. Before it runs, this posts the command into this task's own chat and waits for a " +
			"human to reply there with approve or deny (optionally with a reason); it does not run until they do.",
		InputSchema: runHostCommandInputSchema,
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			command, _ := args["command"].(string)
			if command == "" {
				return mcp.Result{Text: "command is required", IsError: true}
			}
			if confirm == nil {
				return mcp.Result{Text: "run_host_command has no way to ask for confirmation in this run, so it runs nothing", IsError: true}
			}

			approved, reply, err := confirm(ctx, ConfirmQuestion(command))
			if err != nil {
				return mcp.Result{Text: fmt.Sprintf("could not get confirmation: %v", err), IsError: true}
			}
			if !approved {
				return mcp.Result{Text: fmt.Sprintf("denied: %s", reply), IsError: true}
			}

			cmd := exec.CommandContext(ctx, "bash", "-c", command)
			procgroup.Prepare(cmd)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			runErr := cmd.Run()

			exitCode := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				if errors.As(runErr, &exitErr) {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = -1
					stderr.WriteString(runErr.Error())
				}
			}
			text := fmt.Sprintf("exit=%d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
			return mcp.Result{Text: text, IsError: exitCode != 0}
		},
	}}
}

// Confirm posts question as a comment on taskID's own conversation,
// marks the task awaiting that reply (model.Observation.
// PendingQuestionCommentID, the same field ask_question's own post-run
// handling sets -- so a human watching the task list sees "awaiting
// reply" the instant this run pauses, not only once it eventually
// finishes), and blocks -- polling Comments every pollInterval -- until
// a human replies or timeout elapses.
//
// This is deliberately a synchronous wait inside one tool call, not
// grain's usual "park the whole run, pick the reply up on the next
// dispatch" pattern (mcp's own ask_question) -- the point of an
// Interactive task is that someone is on the other end right now, so
// there is a turn boundary worth waiting across here that an unattended
// run would not have. It only works at all because the caller already
// holds *model.Store directly and is running in the same OS process as
// the reply it is waiting for.
//
// That is the in-process caller's half, and it is the one no agent
// reaches: both frameworks that remain (agent/antigravity and
// agent/claude) drive a real CLI as a subprocess, whose tools are served
// by a forked "grain mcpserver" holding no store. That process asks the
// same question through the daemon instead -- the REST hop the chat
// tool's daemonChat makes (pkg/ui's /api/tasks/{id}/messages) -- with
// pkg/daemon's daemonHostExecConfirm as its Confirmer, and reads the
// reply with the same Decide. This function is still what an
// in-process caller holding the store uses.
//
// approved is false, with reply explaining why, whenever a human
// answers no, the wait times out, or ctx is cancelled (the task closed,
// or the run was otherwise stopped) -- refusing, not running, is always
// the safe default for a call this function cannot resolve cleanly.
func Confirm(ctx context.Context, store *model.Store, taskID, question string, pollInterval, timeout time.Duration) (approved bool, reply string, err error) {
	agentPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: taskID}
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: taskID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &agentPrincipal,
		},
		Body:      question,
		CreatedAt: time.Now(),
	})
	if err != nil {
		return false, "", fmt.Errorf("selfrepair: posting confirmation question: %w", err)
	}
	markPending := func(now time.Time) {
		_ = store.ObserveField(context.Background(), taskID, now, func(o *model.Observation) {
			o.PendingQuestionCommentID = &commentID
		})
	}
	clearPending := func(now time.Time) {
		_ = store.ObserveField(context.Background(), taskID, now, func(o *model.Observation) {
			o.PendingQuestionCommentID = nil
		})
	}
	markPending(time.Now())

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			clearPending(time.Now())
			return false, "the run was cancelled while waiting for a reply", nil
		case now := <-ticker.C:
			comments, err := store.Comments(ctx, taskID)
			if err != nil {
				// Transient read failure: the next tick gets another
				// chance at the same comments, the same "costs one
				// interval of latency, not the ability to notice it
				// later" reasoning orchestrator's own addendaPoller
				// applies to the identical read.
				continue
			}
			for _, c := range comments {
				if c.ID <= commentID || c.Author.Actor.Kind != model.PrincipalHuman {
					continue
				}
				clearPending(now)
				return Decide(c.Body), c.Body, nil
			}
			if now.After(deadline) {
				clearPending(now)
				return false, "no reply within the timeout", nil
			}
		}
	}
}

// Decide reads a human's reply as approve or deny -- deliberately
// biased toward refusing: an unrecognised reply (a question back, a
// typo, small talk) must never be read as approval just because it
// wasn't a clear "no".
func Decide(reply string) bool {
	lower := strings.ToLower(strings.TrimSpace(reply))
	if strings.Contains(lower, "deny") || strings.Contains(lower, "denied") || strings.Contains(lower, "refuse") {
		return false
	}
	return strings.HasPrefix(lower, "approve") || strings.HasPrefix(lower, "yes")
}
