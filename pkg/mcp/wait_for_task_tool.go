package mcp

// wait_for_task: a running agent waiting on another task.
//
// depends_on only ever gated dispatch -- a queued task does not start
// until what it names has closed -- so a run that found, half-way
// through, that it had to wait for somebody else's work had no good move:
// end and file a successor, ask a human to say when, or poll with tools
// most runs do not hold. This is the move.
//
// Two shapes, because two different waits hide behind the one question.
// A target already running and minutes from done is worth blocking on,
// exactly as wait_for_checks blocks on CI, and the blocked time is
// credited back to the run's deadline through the same ledger and under
// the same per-run cap (wait_credit.go). A target hours away is not: the
// run parks instead, adding the targets to its own task's depends-on
// links and ending, and the scheduler sends the task back out when they
// close -- the dependency machinery that already exists, rather than a
// second one (ui.Client.WaitOn, orchestrator.processResult).
//
// A third shape waits on nothing but the clock: resume_at parks the run's
// own task until a time ("hold off for 1 hour while we do repairs"). It is
// a deferral with a wake time -- the one propose_task's not_before files
// and orchestrator.reconcileWake undefers -- so, again, no second
// mechanism.
//
// Both parks are forms of resume_later (resume_later_tool.go,
// docs/design/durable-tasks.md#DT-9): one TaskWaiter.ResumeLater call,
// and a run recorded as a requested suspension when it ends.
//
// The sandbox does not survive a park, which is why parking with work not
// pushed is refused unless the call says it knows.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// TaskWaiter is the daemon, as wait_for_task sees it. Which task is
// waiting is fixed at process start (`grain mcpserver -task`), never by
// anything in a tool call, so a run can only ever park itself.
type TaskWaiter interface {
	// WaitOn checks that this run's task may wait on ids, describes each
	// of them, and with park adds them to the task's depends-on links.
	// A refusal (itself, an unknown id, a standing chat session, a
	// cycle) is an error whose text says which.
	WaitOn(ctx context.Context, ids []string, park bool) ([]model.WaitTarget, error)
	// ParkUntil defers this run's task until until, which reconcileWake
	// picks back up, and returns the wake time as stored. A refusal (a
	// time not in the future, or further off than a park may last) is an
	// error whose text says which.
	ParkUntil(ctx context.Context, until time.Time) (time.Time, error)
	// ResumeLater stops this run's task until when is met -- resume_later,
	// and wait_for_task's two parks, which are forms of it
	// (ui.Client.ResumeLater) -- and returns the condition as it was
	// taken. A refusal is an error whose text says why.
	ResumeLater(ctx context.Context, when model.ResumeOn) (model.ResumeOn, error)
}

// UnpushedWorkReader reports what in the run's checkout would be lost
// with the sandbox: commits on no remote, and uncommitted changes to
// tracked files, one line each. Empty is nothing to lose.
type UnpushedWorkReader func(ctx context.Context) (string, error)

const (
	defaultWaitForTaskTimeout = 15 * time.Minute
	maxWaitForTaskTimeout     = time.Hour
	waitForTaskPoll           = 20 * time.Second
	// parkedMarker opens a park's result. orchestrator.processResult
	// reads it (with the park argument) to tell a run that parked from
	// one that only looked.
	parkedMarker = "Parked."
	// parkedUntilPhrase follows parkedMarker in a timed park's result,
	// with the wake time after it in RFC 3339, so
	// WaitForTaskParkedUntil can read it back.
	parkedUntilPhrase = " This task is deferred until "
)

// WaitForTaskParked reports whether a wait_for_task call with these
// arguments and this result parked its task. Shared with
// orchestrator.processResult so the two cannot disagree about the
// marker.
func WaitForTaskParked(args map[string]any, text string) bool {
	if strings.HasPrefix(text, parkedMarker) {
		return true
	}
	park, _ := args["park"].(bool)
	return park && !strings.HasPrefix(text, "Already")
}

// WaitForTaskParkedUntil is the wake time a wait_for_task call with these
// arguments and this result parked its task until, or false for a call
// that did not park on a time.
func WaitForTaskParkedUntil(args map[string]any, text string) (time.Time, bool) {
	if raw, _ := args["resume_at"].(string); strings.TrimSpace(raw) == "" {
		return time.Time{}, false
	}
	rest, ok := strings.CutPrefix(text, parkedMarker+parkedUntilPhrase)
	if !ok {
		return time.Time{}, false
	}
	stamp, _, _ := strings.Cut(rest, " ")
	until, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return time.Time{}, false
	}
	return until.UTC(), true
}

// ParseResumeAt reads wait_for_task's resume_at: a Go duration from now
// ("1h", "90m", "1h30m"), or an RFC 3339 time.
func ParseResumeAt(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("resume_at %q: a duration has to be positive", raw)
		}
		return now.Add(d).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("resume_at %q: want a duration (1h, 90m) or an RFC 3339 time (2026-10-14T09:00:00Z)", raw)
	}
	return t.UTC(), nil
}

// NewWaitForTaskTools returns wait_for_task. A nil waiter is a server
// with no daemon to ask, and the tool says so rather than vanishing, so
// the roster does not depend on how the server was started. A nil
// unpushed reader skips the unpushed-work guard -- a sandbox this process
// cannot run git in.
func NewWaitForTaskTools(waiter TaskWaiter, unpushed UnpushedWorkReader) []Tool {
	return []Tool{waitForTaskTool(waiter, unpushed, waitForTaskPoll)}
}

func waitForTaskTool(waiter TaskWaiter, unpushed UnpushedWorkReader, poll time.Duration) Tool {
	return Tool{
		Name: "wait_for_task",
		Description: "Wait for another task in this deployment to close (or merge) before you carry " +
			"on. Two modes. A short wait blocks this call until the task reaches the state asked " +
			"for or timeout_seconds passes, and returns its state, outcome and closing note; time " +
			"spent blocked is credited back to your run's deadline the way wait_for_checks' is, " +
			"under the same per-run cap. Use it when the task is already running and should finish " +
			"within minutes. A long wait parks: set park: true -- or ask for a timeout longer than " +
			"your run has room for, and it parks by itself -- and grain adds the task(s) to this " +
			"task's own depends_on and ends your run; when every one of them has closed the task " +
			"is queued again, and the next attempt's prompt says what it waited on and how that " +
			"ended. THE SANDBOX DOES NOT SURVIVE A PARK: commit and push your branch first. A park " +
			"with unpushed commits or uncommitted changes is refused unless you set force: true. " +
			"After a park, take no further actions. until: \"closed\" (the default) is met however " +
			"the task ended -- merged, closed unmerged, cancelled, or stopped as failed -- and the " +
			"result says which; \"merged\" is met only by a merge. A park resumes on closure " +
			"either way, so check the outcome when you resume. You cannot wait on this task " +
			"itself, on a standing chat or director session, or on a task that already waits on " +
			"this one. To wait out a hold that is only a time (\"hold off for 1 hour\"), set " +
			"resume_at instead of a task: grain defers this task until then and ends your run, " +
			"and the task is queued again once that time has passed -- the same rules about " +
			"unpushed work apply. To make work you are *filing* wait for something, use " +
			"propose_task's depends_on instead.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "The task to wait on (\"4944\", \"#4944\", or its URL).",
				},
				"task_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "More tasks to wait on: the wait ends when every one has reached the state.",
				},
				"until": map[string]any{
					"type":        "string",
					"enum":        []string{"closed", "merged"},
					"description": "\"closed\" (default): the task closed, however it ended. \"merged\": closed with its change merged.",
				},
				"timeout_seconds": map[string]any{
					"type":        "number",
					"description": "How long a blocking wait may last (default 900, at most 3600). One longer than this run has room for parks instead.",
				},
				"park": map[string]any{
					"type":        "boolean",
					"description": "Park now rather than block: end this run and resume the task when the targets close.",
				},
				"force": map[string]any{
					"type":        "boolean",
					"description": "Park even though the checkout has unpushed commits or uncommitted changes, which are lost with the sandbox.",
				},
				"resume_at": map[string]any{
					"type": "string",
					"description": "Park this task until a time rather than on a task: a duration from now (\"1h\", \"90m\") " +
						"or an RFC 3339 time (2026-10-14T09:00:00Z), at most a week away. Takes no task_id. The task " +
						"is queued again once the time has passed -- not at that instant, but on grain's next cycle " +
						"with room to dispatch it.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			return waitForTask(ctx, waiter, unpushed, poll, args)
		},
	}
}

func waitForTask(ctx context.Context, waiter TaskWaiter, unpushed UnpushedWorkReader,
	poll time.Duration, args map[string]any) Result {
	if waiter == nil {
		return Result{Text: "wait_for_task is not available here: this server has no grain daemon " +
			"to ask about other tasks.", IsError: true}
	}
	ids := WaitForTaskIDs(args)
	force, _ := args["force"].(bool)
	if raw, _ := args["resume_at"].(string); strings.TrimSpace(raw) != "" {
		if len(ids) > 0 {
			return Result{Text: "resume_at parks this task until a time and takes no task_id: wait on " +
				"the task, or until the time, not both.", IsError: true}
		}
		until, err := ParseResumeAt(raw, time.Now())
		if err != nil {
			return Result{Text: err.Error(), IsError: true}
		}
		return parkUntil(ctx, waiter, unpushed, until, force)
	}
	if len(ids) == 0 {
		return Result{Text: "task_id is required: name the task to wait on -- or set resume_at to " +
			"park until a time instead.", IsError: true}
	}
	until, _ := args["until"].(string)
	switch until {
	case "", "closed":
		until = "closed"
	case "merged":
	default:
		return Result{Text: fmt.Sprintf("until must be \"closed\" or \"merged\", not %q.", until), IsError: true}
	}
	untilMerged := until == "merged"
	timeout := defaultWaitForTaskTimeout
	if v, ok := args["timeout_seconds"].(float64); ok && v > 0 {
		timeout = min(time.Duration(v)*time.Second, maxWaitForTaskTimeout)
	}
	park, _ := args["park"].(bool)

	targets, err := waiter.WaitOn(ctx, ids, false)
	if err != nil {
		return Result{Text: "Cannot wait on " + strings.Join(ids, ", ") + ": " + err.Error(), IsError: true}
	}
	if allSettled(targets, untilMerged) {
		return Result{Text: "Already settled -- no wait needed.\n\n" + describeTargets(targets, untilMerged)}
	}

	var autoNote string
	if !park {
		if deadline, ok := runDeadlineFromContext(ctx); ok {
			room := deadline.remaining() + deadline.creditLeft() - waitForChecksDeadlineSlack
			if room < timeout {
				park = true
				autoNote = fmt.Sprintf(" A %s wait does not fit in the %s this run has room for "+
					"(time left plus wait credit still to earn), so this parked rather than blocking.",
					roundDuration(timeout), roundDuration(max(room, 0)))
			}
		}
	}
	if park {
		return parkOnTasks(ctx, waiter, unpushed, ids, force, autoNote)
	}
	return blockOnTasks(ctx, waiter, ids, untilMerged, timeout, poll)
}

// WaitForTaskIDs is task_id and task_ids together, blanks and repeats
// dropped, in the order given.
func WaitForTaskIDs(args map[string]any) []string {
	var raw []string
	if id, ok := args["task_id"].(string); ok {
		raw = append(raw, id)
	}
	if list, ok := args["task_ids"].([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok {
				raw = append(raw, s)
			}
		}
	}
	var ids []string
	seen := map[string]bool{}
	for _, r := range raw {
		id := model.NormalizeTaskID(strings.TrimPrefix(strings.TrimSpace(r), "#"))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// unpushedRefusal is the refusal a park gets while the checkout holds
// work the sandbox would take with it, or nil when there is none (or the
// call said force).
func unpushedRefusal(ctx context.Context, unpushed UnpushedWorkReader, force bool, autoNote string) *Result {
	if unpushed == nil || force {
		return nil
	}
	lost, err := unpushed(ctx)
	if err != nil {
		return &Result{Text: "Not parked: could not check the checkout for unpushed work (" +
			err.Error() + "). The sandbox does not survive a park; push your branch, then call " +
			"again -- or set force: true if there is nothing in it to keep.", IsError: true}
	}
	if lost != "" {
		return &Result{Text: "Not parked: the sandbox does not survive a park, and this would be " +
			"lost with it:\n" + lost + "\nCommit and push it first (git push origin <your " +
			"branch>), then call again -- or set force: true to park and lose it." + autoNote, IsError: true}
	}
	return nil
}

// parkUntil parks the run's task until a time: a deferral with a wake
// time, which reconcileWake undefers.
func parkUntil(ctx context.Context, waiter TaskWaiter, unpushed UnpushedWorkReader,
	until time.Time, force bool) Result {
	if refusal := unpushedRefusal(ctx, unpushed, force, ""); refusal != nil {
		return *refusal
	}
	// resume_later(when: {at}).
	taken, err := waiter.ResumeLater(ctx, model.ResumeOn{At: &until})
	if err != nil {
		return Result{Text: "Not parked: " + err.Error(), IsError: true}
	}
	if taken.At != nil {
		until = *taken.At
	}
	return Result{Text: parkedMarker + parkedUntilPhrase + until.UTC().Format(time.RFC3339) +
		" and is queued again once that time has passed; the next attempt's prompt says it " +
		"resumed from this park. This ends your run: stop here and take no further actions."}
}

func parkOnTasks(ctx context.Context, waiter TaskWaiter, unpushed UnpushedWorkReader,
	ids []string, force bool, autoNote string) Result {
	if refusal := unpushedRefusal(ctx, unpushed, force, autoNote); refusal != nil {
		return *refusal
	}
	// resume_later(when: {task}).
	if _, err := waiter.ResumeLater(ctx, model.ResumeOn{Tasks: ids}); err != nil {
		return Result{Text: "Not parked: " + err.Error(), IsError: true}
	}
	targets, err := waiter.WaitOn(ctx, ids, false)
	if err != nil {
		return Result{Text: "Not parked: " + err.Error(), IsError: true}
	}
	return Result{Text: parkedMarker + " This task now depends on " + joinIDs(ids) + " and is " +
		"queued again once every one of them has closed; the next attempt's prompt says how " +
		"each ended." + autoNote + " This ends your run: stop here and take no further " +
		"actions.\n\n" + describeTargets(targets, false)}
}

func blockOnTasks(ctx context.Context, waiter TaskWaiter, ids []string, untilMerged bool,
	timeout, poll time.Duration) Result {
	credit := startWaitCredit(ctx)
	start := time.Now()
	for {
		blocked := time.Since(start)
		if blocked >= timeout {
			break
		}
		wait := min(poll, timeout-blocked)
		if remaining, ok := credit(ctx, blocked); ok && remaining-waitForChecksDeadlineSlack < wait {
			// The deadline did not move as far as the wait needed --
			// credit used up, or a report that did not land. Stop while
			// there is still time to act on the answer.
			if remaining-waitForChecksDeadlineSlack <= 0 {
				break
			}
			wait = remaining - waitForChecksDeadlineSlack
		}
		if err := sleepUntilCancelled(ctx, wait); err != nil {
			return Result{Text: "wait_for_task was cancelled: " + err.Error(), IsError: true}
		}
		credit(ctx, time.Since(start))
		targets, err := waiter.WaitOn(ctx, ids, false)
		if err != nil {
			return Result{Text: "Stopped waiting: " + err.Error(), IsError: true}
		}
		if allSettled(targets, untilMerged) {
			return Result{Text: fmt.Sprintf("Done waiting after %s.\n\n", roundDuration(time.Since(start))) +
				describeTargets(targets, untilMerged)}
		}
	}
	targets, err := waiter.WaitOn(ctx, ids, false)
	if err != nil {
		return Result{Text: "Stopped waiting: " + err.Error(), IsError: true}
	}
	if allSettled(targets, untilMerged) {
		return Result{Text: fmt.Sprintf("Done waiting after %s.\n\n", roundDuration(time.Since(start))) +
			describeTargets(targets, untilMerged)}
	}
	return Result{Text: fmt.Sprintf("Timed out after %s, still waiting. Wait again, park "+
		"(park: true) to resume when it closes, or carry on without it.\n\n",
		roundDuration(time.Since(start))) + describeTargets(targets, untilMerged)}
}

func allSettled(targets []model.WaitTarget, untilMerged bool) bool {
	for _, t := range targets {
		if settled, _ := t.Settled(untilMerged); !settled {
			return false
		}
	}
	return true
}

// describeTargets is each target's state, and for a settled one whether
// it met what was asked -- "closed but never merged" is the line a run
// waiting for a merge most needs to read.
func describeTargets(targets []model.WaitTarget, untilMerged bool) string {
	var b strings.Builder
	for _, t := range targets {
		settled, met := t.Settled(untilMerged)
		switch {
		case settled && met:
			b.WriteString("[reached] ")
		case settled:
			b.WriteString("[will not reach it: closed without merging] ")
		default:
			b.WriteString("[still waiting] ")
		}
		b.WriteString(t.Describe())
	}
	return b.String()
}

func joinIDs(ids []string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = "#" + id
	}
	return strings.Join(out, ", ")
}

// unpushedWorkScript prints, between two markers, what in the checkout a
// park would lose: commits reachable from HEAD that are on no remote, and
// tracked files with uncommitted changes. The checkout is ./work beside
// where run_command starts (orchestrator.CheckoutDir); a sandbox with no
// checkout there has nothing of grain's to lose and prints nothing.
const unpushedWorkScript = `cd work 2>/dev/null && git rev-parse --git-dir >/dev/null 2>&1 || exit 0
echo GRAIN-UNPUSHED-BEGIN
git log --oneline HEAD --not --remotes -n 20 2>/dev/null | sed 's/^/unpushed commit: /'
git status --porcelain --untracked-files=no 2>/dev/null | sed 's/^/uncommitted: /'
echo GRAIN-UNPUSHED-END`

// SandboxUnpushedWork is an UnpushedWorkReader over tools' own
// run_command, so the check runs where the work is -- a local root, a
// container or a VM alike -- or nil when tools has no run_command.
func SandboxUnpushedWork(tools []Tool) UnpushedWorkReader {
	var run *Tool
	for i := range tools {
		if tools[i].Name == "run_command" {
			run = &tools[i]
		}
	}
	if run == nil {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		res := run.Handler(ctx, map[string]any{
			"command":     unpushedWorkScript,
			"description": "checking for unpushed work before parking",
		})
		if res.IsError {
			return "", fmt.Errorf("%s", strings.TrimSpace(res.Text))
		}
		return unpushedFromOutput(res.Text), nil
	}
}

// unpushedFromOutput is the lines between unpushedWorkScript's markers.
func unpushedFromOutput(text string) string {
	_, rest, ok := strings.Cut(text, "GRAIN-UNPUSHED-BEGIN\n")
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(rest, "GRAIN-UNPUSHED-END")
	return strings.TrimSpace(body)
}
