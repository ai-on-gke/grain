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

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// externalcmd.go is the external-agent workflow's polling and answering
// verbs (design/spec/external-cli/external-cli.spec.md): grain reply
// (#XC-20), grain inbox (#XC-21), grain wait (#XC-22) and grain logs
// (#XC-23), plus the --body-file reading grain create shares (#XC-19).

// externalCommands are this file's verbs, as dispatchCLI runs them.
var externalCommands = map[string]func(context.Context, *ui.HTTPClient, *printer, []string) error{
	"reply": cmdReply, "inbox": cmdInbox, "wait": cmdWait, "logs": cmdLogs,
	"compact": cmdCompact, "brief": cmdBrief, "rollover": cmdRollover,
}

// cliStdin is what --body-file - reads; a test swaps it.
var cliStdin io.Reader = os.Stdin

// bodyFromFlags is a body given as -body text or as -body-file (a path,
// or - for stdin); naming both is a usage error.
func bodyFromFlags(fs *flag.FlagSet, body, bodyFile string) (string, error) {
	bodySet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "body" {
			bodySet = true
		}
	})
	if bodyFile == "" {
		return body, nil
	}
	if bodySet {
		return "", usageErrorf("-body and -body-file both name the body: give one of them")
	}
	return readBodyFile(bodyFile)
}

// readBodyFile reads path, or stdin for "-", as UTF-8 markdown.
func readBodyFile(path string) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(cliStdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", usageErrorf("-body-file %s: %v", path, err)
	}
	return string(data), nil
}

// --- grain reply ------------------------------------------------------

// cmdReply answers the question task <id> is parked on. Unlike grain
// comment it is refused -- by the daemon, with a 409 this exits 2 on,
// posting nothing -- when no question is parked, and when what is
// parked is a secret request, whose value belongs in the encrypted
// input on the task's page and never in its conversation.
func cmdReply(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain reply", flag.ContinueOnError)
	bodyFile := fs.String("body-file", "", "read the reply from this path (- for stdin) instead of the words after the id")
	var attach stringList
	fs.Var(&attach, "attach", "path to a local file to attach to the reply (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return usageErrorf("usage: grain reply [-body-file path|-] [-attach path]... <id> [body...]")
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	if name, ok := misplacedFlag(fs, fs.Args()[1:]); ok {
		return usageErrorf("-%s has to come before the task id: grain reply -%s <value> %s <body>", name, name, fs.Arg(0))
	}
	body := strings.Join(fs.Args()[1:], " ")
	if *bodyFile != "" {
		if body != "" {
			return usageErrorf("the reply is given both as words and as -body-file: give one of them")
		}
		if body, err = readBodyFile(*bodyFile); err != nil {
			return err
		}
	}
	attachments, err := loadAttachments(attach)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" && len(attachments) == 0 {
		return usageErrorf("usage: grain reply [-attach path]... <id> [body...] -- a body, an attachment, or both is required")
	}
	if err := c.Reply(ctx, id, body, attachments); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// --- grain inbox ------------------------------------------------------

func cmdInbox(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain inbox", flag.ContinueOnError)
	repo := fs.String("repo", "", "only this repository's (owner/name) items")
	kind := fs.String("kind", "", "only items of this kind: "+strings.Join(ui.InboxKinds, ", "))
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	if *kind != "" && !slices.Contains(ui.InboxKinds, *kind) {
		return usageErrorf("-kind %q is not one of %s", *kind, strings.Join(ui.InboxKinds, ", "))
	}
	resp, err := c.Inbox(ctx, strings.TrimSpace(*repo), *kind)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(resp)
		return nil
	}
	if len(resp.Items) == 0 {
		fmt.Fprintln(os.Stderr, "grain: nothing in the inbox")
		return nil
	}
	for _, it := range resp.Items {
		fmt.Println(inboxLine(it))
	}
	return nil
}

// inboxLine is one item, on one line: its kind, what it is about, what
// it says and where it is answered.
func inboxLine(it ui.InboxItem) string {
	var what, says string
	switch it.Kind {
	case ui.InboxNotification:
		what = it.Severity + " " + it.Subject
		says = it.Title
	case ui.InboxMergeHeld:
		what = "#" + it.Task + " " + it.PR
		says = it.Reason
	case ui.InboxProposal:
		what = "#" + it.Task + " " + it.Title
		says = "proposed by " + cmpOr(it.Proposer, "unknown")
		if it.Reason != "" {
			says += ": " + it.Reason
		}
	default:
		what = "#" + it.Task + " " + it.Title
		says = it.Summary
	}
	line := fmt.Sprintf("%-12s %s", it.Kind, what)
	if says != "" {
		line += " -- " + oneLine(says)
	}
	return line + "  " + it.Path
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

// --- grain wait -------------------------------------------------------

// defaultWaitTimeout is how long grain wait waits with no -timeout.
const defaultWaitTimeout = 30 * time.Minute

// waitSlice is the longest one GET /api/tasks/wait call is asked to hold;
// a test shortens it.
var waitSlice = ui.MaxTasksWaitHold

// cmdWait blocks until the tasks named reach one of -until's conditions,
// re-issuing GET /api/tasks/wait slices of at most a minute up to the
// global -timeout (30m by default). It exits 0 when a condition is met
// cleanly, 1 when the one met is a terminal failure (a task closed
// unmerged under until=merged, or failed under closed), and 5 when the
// timeout runs out first, printing the command that carries on.
func cmdWait(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain wait", flag.ContinueOnError)
	untilFlag := fs.String("until", "closed", "what to wait for, comma-separated for the first of several: "+strings.Join(ui.WaitUntilConditions, ", "))
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return usageErrorf("usage: grain wait <id...> [-until=closed|merged|awaiting-reply|ci-done|any[,...]] [-timeout=30m]")
	}
	var ids []string
	for _, a := range rest {
		for _, id := range strings.Split(a, ",") {
			if id = strings.TrimPrefix(strings.TrimSpace(id), "#"); id != "" && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	var until []string
	for _, u := range strings.Split(*untilFlag, ",") {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !slices.Contains(ui.WaitUntilConditions, u) {
			return usageErrorf("-until %q is not one of %s", u, strings.Join(ui.WaitUntilConditions, ", "))
		}
		until = append(until, u)
	}
	if len(until) == 0 {
		return usageErrorf("-until names no condition")
	}
	timeout := defaultWaitTimeout
	if d, ok := ctx.Value(waitLimitKey{}).(time.Duration); ok && d > 0 {
		timeout = d
	}
	deadline := time.Now().Add(timeout)
	since := ""
	var last ui.TasksWaitResponse
	for {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		slice := min(left, waitSlice)
		callCtx, cancel := context.WithTimeout(ctx, slice+30*time.Second)
		resp, err := c.WaitTasks(callCtx, ids, until, slice, since)
		cancel()
		if err != nil {
			return err
		}
		last = resp
		if since == "" {
			since = resp.Revision
		}
		if resp.Met {
			printWait(out, resp)
			if resp.Failed && failureCounts(until, resp.Condition) {
				return targetFailed(fmt.Errorf("%s: %s", resp.Condition, waitFailureWhy(resp)))
			}
			return nil
		}
	}
	printWait(out, last)
	resume := "grain wait " + strings.Join(ids, " ") + " --until=" + strings.Join(until, ",") + " --timeout=" + timeout.String()
	if out.json {
		fmt.Fprintln(os.Stderr, "grain: to carry on waiting: "+resume)
	}
	return &cliError{exit: exitWaitTimedOut, code: "wait_timeout", fix: resume,
		err: fmt.Errorf("waited %s and no task reached %s", timeout, strings.Join(until, " or "))}
}

// failureCounts is whether a failed answer to condition fails the
// command: until=merged without closed (a closed task was a possible
// answer to closed), and closed itself.
func failureCounts(until []string, condition string) bool {
	if condition == ui.WaitUntilMerged {
		return !slices.Contains(until, ui.WaitUntilClosed)
	}
	return condition == ui.WaitUntilClosed
}

func waitFailureWhy(resp ui.TasksWaitResponse) string {
	var parts []string
	for _, t := range resp.Tasks {
		if !t.Merged && t.Outcome != "" {
			parts = append(parts, "task "+t.ID+" "+t.Outcome)
		}
	}
	if len(parts) == 0 {
		return "a task ended without what was waited for"
	}
	return strings.Join(parts, ", ")
}

func printWait(out *printer, resp ui.TasksWaitResponse) {
	if out.json {
		out.encode(resp)
		return
	}
	for _, t := range resp.Tasks {
		line := fmt.Sprintf("%s\t%s", t.ID, t.State)
		if t.Outcome != "" {
			line += "\t" + t.Outcome
		}
		if t.PRUrl != "" {
			line += "\t" + t.PRUrl
		}
		if t.CI != nil && t.CI.Conclusion != "" {
			line += "\tci " + t.CI.Conclusion
		}
		fmt.Println(line)
		if t.Question != "" {
			fmt.Println("  question: " + oneLine(t.Question))
		}
		if t.PendingSecret != "" {
			fmt.Println("  secret requested: " + t.PendingSecret + " (set it on /tasks/" + t.ID + ")")
		}
		// A parked task's closing note is the question it parked on,
		// already printed just above; say it once.
		if t.ClosingNote != "" && oneLine(t.ClosingNote) != oneLine(t.Question) {
			fmt.Println("  " + oneLine(t.ClosingNote))
		}
	}
}

// --- grain logs -------------------------------------------------------

// logsPollInterval is how often grain logs -follow reads the attempt
// again; a test shortens it.
var logsPollInterval = 2 * time.Second

// logsView is grain logs -json's document.
type logsView struct {
	Task       string     `json:"task"`
	Attempt    int        `json:"attempt"`
	Activity   string     `json:"activity,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Outcome    string     `json:"outcome,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	Lines      []string   `json:"lines"`
}

func cmdLogs(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain logs", flag.ContinueOnError)
	attemptN := fs.Int("attempt", 0, "the attempt to read, 1 for the first (default the latest)")
	tail := fs.Int("tail", 0, "only the last N lines of the transcript")
	follow := fs.Bool("follow", false, "stream new lines until the attempt finishes (exit 0 when it ends normally, 1 when it errored)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return err
	}
	id, err := taskID(first(rest))
	if err != nil {
		return err
	}
	read := func() (ui.TaskDetail, ui.Attempt, []string, error) {
		detail, err := c.GetTask(ctx, id)
		if err != nil {
			return detail, ui.Attempt{}, nil, err
		}
		a, ok := pickAttempt(detail.Attempts, *attemptN)
		if !ok {
			if *attemptN > 0 {
				return detail, a, nil, usageErrorf("task %s has no attempt %d", id, *attemptN)
			}
			return detail, a, nil, nil
		}
		transcript, err := c.AttemptTranscript(ctx, id, a.Number)
		if err != nil {
			var nf *ui.NotFoundError
			if !errors.As(err, &nf) {
				return detail, a, nil, err
			}
		}
		return detail, a, transcriptLines(transcript), nil
	}
	detail, a, lines, err := read()
	if err != nil {
		return err
	}
	if a.Number == 0 && !*follow {
		return usageErrorf("task %s has not run yet", id)
	}
	shown := lines
	if *tail > 0 && len(shown) > *tail {
		shown = shown[len(shown)-*tail:]
	}
	if out.json && !*follow {
		out.encode(logsView{Task: id, Attempt: a.Number, Activity: detail.Activity, StartedAt: a.StartedAt,
			FinishedAt: a.FinishedAt, Outcome: a.Outcome, Detail: a.Detail, Lines: nonNil(shown)})
		return nil
	}
	printed, activity := len(lines), ""
	emit := func(text string) {
		if out.json {
			out.encode(map[string]string{"task": id, "line": text})
			return
		}
		fmt.Println(text)
	}
	status := func(d ui.TaskDetail, a ui.Attempt) {
		if d.Activity != "" && d.Activity != activity {
			activity = d.Activity
			if out.json {
				out.encode(map[string]any{"task": id, "attempt": a.Number, "activity": activity})
			} else {
				fmt.Fprintf(os.Stderr, "grain: #%s attempt %d: %s\n", id, a.Number, activity)
			}
		}
	}
	status(detail, a)
	for _, l := range shown {
		emit(l)
	}
	if !*follow {
		if a.FinishedAt != nil && !out.json {
			fmt.Fprintf(os.Stderr, "grain: #%s attempt %d finished: %s\n", id, a.Number, cmpOr(a.Outcome, "done"))
		}
		return nil
	}
	number := a.Number
	for a.Number == 0 || a.FinishedAt == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(logsPollInterval):
		}
		detail, a, lines, err = read()
		if err != nil {
			return err
		}
		if a.Number != number {
			number, printed = a.Number, 0
		}
		status(detail, a)
		if len(lines) < printed {
			printed = 0
		}
		for _, l := range lines[printed:] {
			emit(l)
		}
		printed = len(lines)
	}
	if out.json {
		out.encode(map[string]any{"task": id, "attempt": a.Number, "finishedAt": a.FinishedAt, "outcome": a.Outcome})
	} else {
		fmt.Fprintf(os.Stderr, "grain: #%s attempt %d finished: %s\n", id, a.Number, cmpOr(a.Outcome, "done"))
	}
	if attemptErrored(a.Outcome) {
		return targetFailed(fmt.Errorf("task %s attempt %d %s: %s", id, a.Number, a.Outcome, a.Detail))
	}
	return nil
}

// pickAttempt is attempt n (1-based), or the latest for n == 0.
func pickAttempt(attempts []ui.Attempt, n int) (ui.Attempt, bool) {
	var best ui.Attempt
	found := false
	for _, a := range attempts {
		if n > 0 && a.Number == n {
			return a, true
		}
		if n == 0 && (!found || a.Number > best.Number) {
			best, found = a, true
		}
	}
	return best, found && n == 0
}

func transcriptLines(transcript string) []string {
	transcript = strings.TrimRight(transcript, "\n")
	if transcript == "" {
		return nil
	}
	return strings.Split(transcript, "\n")
}

func nonNil(lines []string) []string {
	if lines == nil {
		return []string{}
	}
	return lines
}

// attemptErrored is whether an attempt's outcome is a run that went
// wrong rather than one that ended normally (succeeded, parked, asked).
func attemptErrored(outcome string) bool {
	o := strings.ToLower(outcome)
	for _, bad := range []string{"fail", "error", "timed", "timeout", "crash", "oom", "cancel"} {
		if strings.Contains(o, bad) {
			return true
		}
	}
	return false
}
