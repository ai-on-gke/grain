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

// task_tools.go is the read-only window onto grain's *other* tasks that
// the self-debug capability opens: list_grain_tasks, read_grain_task,
// read_grain_task_prompt and read_grain_task_transcript.
//
// Reading grain's own source (pkg/capability/selfdebug's own
// read_grain_source) explains what grain is built to do. It cannot
// explain what this deployment actually did -- why that task failed
// three times in a row, what its agent was really told, which tool call
// it was on when it gave up. That evidence lives in the task store, and
// until now a run had no way to reach any of it: the only task a run
// could see anything of was its own, and only through the prompt it was
// handed at dispatch. So debugging grain from inside grain meant asking
// a human to copy the interesting parts of another task's record into
// the chat by hand.
//
// These tools are served by the `grain mcpserver` process, which runs on
// the controller, and they reach the store the way every other write
// there does -- by asking the daemon over its own REST API
// (pkg/cli/mcpserver.go's daemonTasks), never by opening the store
// from a second process. docs/design.md's split surface is untouched:
// what crosses into the sandbox is rendered text, not a database handle
// and not a credential.
//
// They are registered only for a run whose task holds the self-debug
// grant (mcpserver's own -self-debug, passed by a Framework only when
// agent.RunConfig.SelfDebug says so). Everything they expose is
// everything anyone with the UI open can already read, so the gate is
// the grant a human attached rather than a confirmation per call --
// exactly the line pkg/capability/selfdebug draws for source reading,
// and the opposite of selfrepair's, which can change things.

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// TaskSummary is one task as a list entry: enough to pick the task worth
// looking at, and no more. It is deliberately not ui.Task -- pkg/ui
// imports pkg/capability/selfdebug, and this package is imported by both
// halves of that, so the wire shape stays out of here and
// pkg/cli/mcpserver.go's adapter projects one into the other.
type TaskSummary struct {
	ID            string
	Title         string
	State         string
	Repo          string
	Interactive   bool
	Configuration bool
	CreatedAt     *time.Time
	// BlockedBy is every still-open task this one is waiting on
	// (ui.Task.BlockedBy, model.IsBlocked). It is on the list shape,
	// unlike almost everything else that is not the state itself,
	// because without it the list cannot say the one thing a reader of
	// "queued" most needs: blocked and waiting its turn read identically
	// as `[queued]`, and a run that had quietly made eight of its own
	// tasks undispatchable went on reporting them as queued and
	// dispatching for five hours (grain/task-487). The ids alone answer
	// it -- what those tasks are is read_grain_task's job, one row at a
	// time.
	BlockedBy []string
	// MergeQueue is what grain's merge queue is doing about landing the
	// change this task pushed, which is a different question from State:
	// State goes on saying "completed" for as long as a change sits
	// unmerged, and every wait that matters after a run is over lives
	// here. The zero value is "the queue is not driving this task", which
	// is the answer for nearly every task there is.
	MergeQueue TaskMergeQueue
	// Stall is MergeQueue.Note's counterpart for the lane State itself
	// is about: what this task's own state has to say for itself once it
	// has stood for longer than that state should take -- which
	// component ought to have ended the wait, and what to go and look at
	// (orchestrator's taskLane).
	//
	// It is here for the reason MergeQueue is: "why has this been
	// queued for four days" is the question a self-debug run is sent to
	// answer, and State alone answers it with the word that is the
	// problem. Empty for the overwhelming majority of tasks -- a state
	// inside its patience, a state that is waiting on a person by design,
	// or one whose record cannot say when it began -- so empty is the
	// ordinary reading and never means nothing was checked.
	Stall string
	// Kinds is the task's kind tags (ui.TaskKindTags: port, repair,
	// session, proposal, durable, imported) -- the fixed vocabulary a report about
	// the task is held to, so a reader never has to infer "this is a port"
	// from a title.
	Kinds []string
	// DependsOn is every task this one names in depends_on, open or not;
	// BlockedBy above is the still-open part of it.
	DependsOn []string
	// ChangedAt is the latest moment the record stamps a change of this
	// task's -- filed, a run starting or ending, closed, merged, or a move
	// of the merge queue's -- which is what list_grain_tasks'
	// changed_since compares. nil for a record that stamps none.
	ChangedAt *time.Time
}

// TaskMergeQueue is the merge queue's own account of one task, as the
// task row in grain's UI reads it: model.Observation's MergeQueueState,
// MergeQueueNote, MergeQueueStateAt, MergeQueueStalledAt and
// MergeQueueBlockedAt.
//
// It is here because it was the one part of a task's record a self-debug
// run could not reach, and the part an operator asks about most: "why has
// this been sitting at completed for three hours" is answered by State
// and At together, and by nothing else in the record.
type TaskMergeQueue struct {
	// State is orchestrator's own vocabulary -- "waiting_ci",
	// "waiting_queue", "waiting_review", "merging", "updating_branch",
	// "batching", "gathering", "checks_unfixable", "waiting_github_approval", "github_merge_queue", "repair_needed",
	// "repair_queued", "repairing", "agent_working", "merge_failed",
	// "unreadable" -- and "" for a task the queue is not driving.
	State string
	// Note is that state said in words: which checks are unfinished,
	// which task is ahead of this one, what GitHub said when it refused
	// the merge. Empty for a state that needs no elaboration.
	Note string
	// At is when the task entered State, which is half of what the state
	// means: "Merging" ten seconds old and "Merging" three hours old are
	// a queue working and a queue stuck.
	At *time.Time
	// StalledAt is grain's own answer to which of those two this is:
	// non-nil once the entry has been in State for longer than the
	// component driving it should plausibly take
	// (model.Observation.MergeQueueStalledAt, orchestrator's liveness.go),
	// stamped at the moment that patience ran out.
	//
	// It is carried rather than left to Note because an agent asked "why
	// has this not merged" should not have to read a judgement out of a
	// sentence grain has already made: the daemon knows, and Note is the
	// same finding written for a person.
	StalledAt *time.Time
	// BlockedAt is set once the queue has given up driving this pull
	// request and is waiting for a person (a repair that did not take,
	// checks that never finished). Nothing clears it but a human's own
	// "Retry merge".
	BlockedAt *time.Time
}

// TaskActivity is the one line a task's live run says about itself
// (model.Run.Activity, mcp's own update_status) -- the same phrase the
// row in grain's UI shows, with the moment it was written.
//
// BySetup marks the phrase as grain's own rather than the agent's: what
// the dispatch path is doing to get the run started, over the stretch
// before the agent's first turn. The two voices are told apart here for
// the reason the UI tells them apart -- a reader who has learnt to read
// the line as the agent's should not have to guess which of the two is
// speaking.
//
// The zero value is "this task is saying nothing", which is every task
// that is not running and every running task whose agent has not
// narrated itself yet. That is an ordinary state and never a fault.
type TaskActivity struct {
	Note    string
	At      *time.Time
	BySetup bool
}

// TaskAttempt is one run of a task: when it started, how it ended, and
// the one sentence grain recorded about how it got there
// (model.Run.Detail -- a tool error, the framework's own error text, or
// "finished without pushing, asking, or commenting"). This is where the
// errors are.
//
// PromptBytes and PromptTokens are how big the prompt this attempt was
// handed came out, and the token estimate of it (model.PromptSize). They
// are here because a task's prompt is the one part of a dispatch that
// grows without bound -- its conversation is replayed into every later
// attempt -- and the growth is invisible from anywhere else: attempt 10
// of a week-old chat reads exactly like attempt 1 in this list until the
// sizes are beside it. Both are 0 for an attempt that never reached its
// agent and for one recorded before prompts were kept.
type TaskAttempt struct {
	Number       int
	StartedAt    time.Time
	FinishedAt   *time.Time
	Outcome      string
	Detail       string
	PromptBytes  int
	PromptTokens int
	// Model and AgentFramework are what the attempt actually ran on
	// (ui.Attempt.Model), "" for one recorded before grain kept them.
	Model          string
	AgentFramework string
}

// TaskComment is one entry in a task's conversation, author included:
// who said a thing matters as much as what was said when the question is
// "why did this run do that", since a comment from a human is an
// instruction and one from grain is a relay.
type TaskComment struct {
	// ID is the comment's own id, which read_grain_task's conversation
	// view pages by ("after").
	ID         int64
	Author     string
	AuthorKind string
	Body       string
	CreatedAt  time.Time
	// Handover marks the comment a run wrote as the task's standing
	// summary of its own older conversation (model.Comment.Handover). A
	// run reading another task's record to work out why it is behaving
	// badly is often reading it *because* that conversation outgrew a
	// prompt, and which comment grain carries into every later prompt of
	// it is part of that answer.
	Handover bool
}

// TaskTransition is one step in a task's timeline (ui.Transition): when
// it entered a state, or when somebody closed or reopened it
// (model.TaskEvent), with who asked, the reason they gave and what a
// close cancelled.
type TaskTransition struct {
	State    string
	At       time.Time
	Inferred bool
	By       string
	Reason   string
	Detail   string
}

// TaskRecord is one task in full: the summary above, plus what it asked
// for, what happened on every attempt, and everything said about it.
type TaskRecord struct {
	TaskSummary
	Description       string
	Author            string
	AuthorKind        string
	Base              string
	Capabilities      []string
	PullRequest       string
	FailedAttempts    int
	LastFailureAt     *time.Time
	LastFailureReason string
	Attempts          []TaskAttempt
	Transitions       []TaskTransition
	Comments          []TaskComment
	// Activity is what this task's live run is saying it is doing, for
	// the reason the merge queue's own state is on the summary above: it
	// is a question asked of a task that looks stuck, and until now the
	// only way to answer it was to ask the operator to read their own
	// screen. Unlike MergeQueue it is not on the list shape -- see
	// renderTaskList on why one task's sentence does not belong on fifty
	// rows.
	Activity TaskActivity
}

// TaskPromptRecord is the prompt one attempt was actually handed, and
// which attempt that was -- a redispatched task's prompt grows with its
// conversation, so attempt 3's is not attempt 1's, and a reader needs to
// know which one it is holding.
//
// Bytes, Tokens and ContextTokens are that prompt measured -- its size,
// the estimated tokens, and the context window the estimate is judged
// against (model.PromptSize, restated here because pkg/mcp does not
// import pkg/model). A run reading another task's prompt to work out why
// it is behaving badly needs the size first: a prompt that is half the
// window is itself the answer, and it reads the same as any other until
// somebody measures it.
type TaskPromptRecord struct {
	Prompt        string
	Attempt       int
	Bytes         int
	Tokens        int
	ContextTokens int
}

// TaskReader is the read-only slice of grain's own task store these
// tools need. Four reads, no writes: what an agent can reach through
// here is legible at a glance, the same reason PullRequestReader next
// door is narrowed to six methods of github.Client rather than being
// that whole interface.
//
// Every method takes a task id, and none of them is this run's own task
// -- that is the point of these tools. There is no scope to pin the way
// PullRequestScope pins a branch: a debugging question is about whichever
// task went wrong, which is never known when the process starts.
type TaskReader interface {
	ListTasks(ctx context.Context) ([]TaskSummary, error)
	Task(ctx context.Context, id string) (TaskRecord, error)
	TaskPrompt(ctx context.Context, id string) (TaskPromptRecord, error)
	AttemptTranscript(ctx context.Context, id string, attempt int) (string, error)
}

// NewTaskTools returns the tools a self-debug or director run gets for
// reading grain's own tasks over the daemon's REST API.
//
// reader nil returns all tools anyway, refusing every call with a
// sentence saying why -- what lets each agent framework's allowedTools
// enumerate the names this package registers without holding a live
// reader, exactly as NewPullRequestTools and NewRecreateSandboxTools
// already do, and what a `grain mcpserver` given -self-debug but no
// -server (no daemon to ask) serves.
func NewTaskTools(reader TaskReader) []Tool {
	return []Tool{
		listTasksTool(reader),
		readTaskTool(reader),
		readTaskPromptTool(reader),
		readTaskTranscriptTool(reader),
	}
}

// noTaskReader is the answer every one of these tools gives when it has
// no route to the store -- a sentence about this run rather than "unknown
// tool", which would read like a bug in grain rather than a fact about
// how this one was dispatched.
const noTaskReader = "This run cannot read grain's other tasks: it holds the self-debug grant but was " +
	"dispatched with no route back to the grain daemon that owns the task store. Say so if it " +
	"matters to what you were asked to do; nothing you can do from here will change it."

// defaultTaskListLimit is how many tasks list_grain_tasks answers with
// when the call names no limit of its own. A deployment's store holds
// every task it has ever run, and a debugging question is nearly always
// about a recent one, so an unbounded list would spend a run's context
// on rows nobody asked for -- the same reasoning result_size.go's own
// cap follows for command output.
const defaultTaskListLimit = 50

var listTasksInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"state": map[string]any{
			"type": "string",
			"description": "only list tasks in this state: proposed, queued, running, awaiting_reply, " +
				"failed, awaiting_submit, completed or closed. Omit for every state.",
		},
		"limit": map[string]any{
			"type":        "number",
			"description": fmt.Sprintf("how many tasks to return at most (default %d)", defaultTaskListLimit),
		},
		"changed_since": map[string]any{
			"type": "string",
			"description": "only tasks whose record changed since then -- filed, a run starting or ending, closed, " +
				"merged, or a merge-queue move: a span (30m, 24h, 7d) or a date or RFC 3339 time",
		},
		"stalled": map[string]any{
			"type":        "boolean",
			"description": "true: only tasks that are stalled, in their own state or on the merge queue",
		},
		"kind": map[string]any{
			"type":        "string",
			"description": "only tasks carrying this kind tag: port, repair, session, proposal, durable or imported",
		},
	},
}

// taskListFilter is list_grain_tasks' narrowing beyond state: the filters
// that let a judgement agent ask for what moved instead of sweeping the
// whole list (docs/design/durable-tasks.md#DT-13).
//
// State may name several states, comma-separated ("proposed,queued"),
// and a task in any of them matches. Repo and Search are grain task
// list's own (/cli/tasks): the repository a task works in, compared as
// grant names are, case-insensitively; and a substring of its title.
type taskListFilter struct {
	State        string
	ChangedSince time.Time
	Stalled      bool
	Kind         string
	Repo         string
	Search       string
}

func (f taskListFilter) matches(t TaskSummary) bool {
	if f.State != "" && !stateListHas(f.State, t.State) {
		return false
	}
	if f.Repo != "" && !strings.EqualFold(t.Repo, f.Repo) {
		return false
	}
	if f.Search != "" && !strings.Contains(strings.ToLower(t.Title), strings.ToLower(f.Search)) {
		return false
	}
	if !f.ChangedSince.IsZero() && (t.ChangedAt == nil || t.ChangedAt.Before(f.ChangedSince)) {
		return false
	}
	if f.Stalled && !taskStalled(t) {
		return false
	}
	if f.Kind != "" && !slices.Contains(t.Kinds, strings.ToLower(f.Kind)) {
		return false
	}
	return true
}

// stateListHas reports whether state is one of the comma-separated states
// in list.
func stateListHas(list, state string) bool {
	for _, s := range strings.Split(list, ",") {
		if s = strings.TrimSpace(s); s != "" && strings.EqualFold(s, state) {
			return true
		}
	}
	return false
}

func (f taskListFilter) narrowed() bool {
	return f.State != "" || !f.ChangedSince.IsZero() || f.Stalled || f.Kind != "" || f.Repo != "" || f.Search != ""
}

// describe is the filter in words, for an empty answer.
func (f taskListFilter) describe() string {
	var parts []string
	if f.State != "" {
		parts = append(parts, "in state "+strconv.Quote(f.State))
	}
	if !f.ChangedSince.IsZero() {
		parts = append(parts, "changed since "+f.ChangedSince.UTC().Format(time.RFC3339))
	}
	if f.Stalled {
		parts = append(parts, "stalled")
	}
	if f.Kind != "" {
		parts = append(parts, "of kind "+f.Kind)
	}
	if f.Repo != "" {
		parts = append(parts, "in "+f.Repo)
	}
	if f.Search != "" {
		parts = append(parts, "with "+strconv.Quote(f.Search)+" in the title")
	}
	return strings.Join(parts, ", ")
}

// taskStalled is whether either lane has flagged the task: its own state
// outliving its patience, or the merge queue stalled or blocked on it.
func taskStalled(t TaskSummary) bool {
	if t.Stall != "" {
		return true
	}
	if strings.EqualFold(t.State, "closed") {
		return false
	}
	return (t.MergeQueue.State != "" && t.MergeQueue.StalledAt != nil) || t.MergeQueue.BlockedAt != nil
}

func listTasksTool(reader TaskReader) Tool {
	return Tool{
		Name: "list_grain_tasks",
		ElisionAdvice: "narrow it -- \"state\" reports one state's tasks and \"limit\" how many -- " +
			"and read one task on its own with read_grain_task",
		Description: "List the other tasks in this grain deployment -- id, title, state, repo, which " +
			"tasks each one is blocked on, and what " +
			"grain's merge queue is doing about each one it is driving -- so you can find the one " +
			"behind whatever you are debugging or explaining, or see at a glance which changes are " +
			"stuck on their way to being merged. A row marked \"(stalled)\" is one whose own state " +
			"has stood for longer than that state should take, which is how a task nothing is " +
			"working on any more is told from one that is simply waiting its turn. A queued task " +
			"marked \"blocked on\" is neither: nothing dispatches it until every task named there " +
			"is closed. Read-only, and " +
			"it needs no permission. Narrow it " +
			"with \"state\" (e.g. failed, running) and \"limit\"; then use read_grain_task for one " +
			"task's full record, which has the queue's own account of why that one is waiting.",
		InputSchema: listTasksInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			if reader == nil {
				return Result{Text: noTaskReader, IsError: true}
			}
			tasks, err := reader.ListTasks(ctx)
			if err != nil {
				return Result{Text: fmt.Sprintf("Error listing grain's tasks: %v", err), IsError: true}
			}
			var filter taskListFilter
			filter.State, _ = argString(args, "state")
			if since, ok := argString(args, "changed_since"); ok && strings.TrimSpace(since) != "" {
				at, err := ci.ParseSince(strings.TrimSpace(since), time.Now())
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				filter.ChangedSince = at
			}
			filter.Stalled, _ = args["stalled"].(bool)
			if kind, ok := argString(args, "kind"); ok && strings.TrimSpace(kind) != "" {
				filter.Kind = strings.ToLower(strings.TrimSpace(kind))
				if !slices.Contains(taskKindVocabulary, filter.Kind) {
					return Result{Text: fmt.Sprintf("kind %q is not one of %s", kind, strings.Join(taskKindVocabulary, ", ")), IsError: true}
				}
			}
			limit := defaultTaskListLimit
			if f, ok := argFloat(args, "limit"); ok && int(f) > 0 {
				limit = int(f)
			}
			return Result{Text: renderFilteredTaskList(tasks, filter, limit)}
		},
	}
}

// renderTaskList is one line per task, oldest first and newest last by
// filing time, with a count and any elision stated outright: a list that
// silently stopped at 50 would have the reader concluding there are 50
// tasks. A limit keeps the newest rows, because a debugging question is
// nearly always about something recent.
//
// The order is this function's own, not the caller's: the store hands
// tasks over in its backlog order (order_key, then id), which on a
// deployment with a long history is newest-first, and trimming that from
// the front kept the oldest month of tasks under a notice calling the
// newest ones "older" (grain task 4702).
//
// The merge queue's state rides on the row; its note does not. "Which of
// these is stuck?" is a question about the whole lane at once, and
// answering it a task at a time is one read_grain_task per row, which is
// how three separate operator conversations went. The state is a phrase
// of a few words, it appears only on the tasks the queue is actually
// driving -- a handful of a deployment's tasks at any moment -- and it is
// the whole of the answer to that question. The note is a sentence per
// row and answers the next question, which is about one row: it belongs
// on the record, where a reader has already chosen which task they are
// asking about. So does the age: "since when" is what a person asks
// after they have found the row that is stuck, and fifty timestamps to
// find one is the wrong trade for a list.
func renderTaskList(tasks []TaskSummary, state string, limit int) string {
	return renderFilteredTaskList(tasks, taskListFilter{State: state}, limit)
}

func renderFilteredTaskList(tasks []TaskSummary, filter taskListFilter, limit int) string {
	shown, matched := selectTasks(tasks, filter, limit)
	if matched == 0 {
		if filter == (taskListFilter{State: filter.State}) && filter.State != "" {
			return fmt.Sprintf("No tasks in state %q.", filter.State)
		}
		if filter.narrowed() {
			return fmt.Sprintf("No tasks %s.", filter.describe())
		}
		return "This deployment has no tasks at all."
	}

	dropped := matched - len(shown)

	var b strings.Builder
	for _, t := range shown {
		fmt.Fprintf(&b, "%s  [%s]", t.ID, t.State)
		if t.Repo != "" {
			fmt.Fprintf(&b, "  %s", t.Repo)
		}
		if t.Configuration {
			b.WriteString("  (configuration agent)")
		} else if t.Interactive {
			b.WriteString("  (interactive)")
		}
		if len(t.Kinds) > 0 {
			fmt.Fprintf(&b, "  kind %s", strings.Join(t.Kinds, ","))
		}
		if t.CreatedAt != nil {
			fmt.Fprintf(&b, "  filed %s", t.CreatedAt.UTC().Format(time.RFC3339))
		}
		// Whether a queued task is actually dispatchable, which its state
		// does not say: 'queued' is what grain calls both a task at the
		// head of the backlog and one that cannot run at all until
		// something else closes (model.StateOf never reads a link, by
		// design -- blocked is an annotation on queued, not a state).
		// The row in grain's UI carries the same fact as a chip
		// (ui/src/state.js's blockedDescription).
		if len(t.BlockedBy) > 0 {
			fmt.Fprintf(&b, "  blocked on %s", strings.Join(t.BlockedBy, ", "))
		}
		// A closed task's row shows no merge activity in the UI, whatever
		// a merge-queue observation that outlived its pull request still
		// says (ui/src/state.js's mergeQueueActive), and a list is the
		// shape read as a picture of the screen. read_grain_task is
		// where that stale row is reported, and it says outright that it
		// is stale.
		if t.MergeQueue.State != "" && !strings.EqualFold(t.State, "closed") {
			fmt.Fprintf(&b, "  merge queue: %s", mergeQueuePhrase(t.MergeQueue.State))
			// And the one thing about that phrase a list has to carry,
			// for the reason this list exists: the reader is scanning
			// for the row that needs looking at, and a wait nothing is
			// getting anywhere with reads exactly like a wait that is
			// fine. The row on screen is marked the same way
			// (ui/src/state.js's mergeQueueStalled); read_grain_task
			// says since when, and what to look at.
			if t.MergeQueue.StalledAt != nil {
				b.WriteString(" (stalled)")
			}
		}
		// The word and not the sentence: a list is for picking the task
		// worth opening, and the sentence -- which component ought to
		// have ended this wait and what to go and look at -- is what
		// read_grain_task is for. A reader scanning for "why is nothing
		// happening" needs to see which rows to open, on rows that are
		// otherwise one line each.
		if t.Stall != "" {
			b.WriteString("  (stalled)")
		}
		fmt.Fprintf(&b, "\n    %s\n", singleLine(t.Title))
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "\n%d older matching tasks are not shown -- raise \"limit\" to see them.\n", dropped)
	}
	fmt.Fprintf(&b, "\n%d task(s) shown of %d matching.", len(shown), matched)
	return b.String()
}

// selectTasks is the tasks filter matches, oldest first by filing, cut to
// the newest limit of them, and how many matched before the cut.
func selectTasks(tasks []TaskSummary, filter taskListFilter, limit int) (shown []TaskSummary, matched int) {
	all := make([]TaskSummary, 0, len(tasks))
	for _, t := range tasks {
		if filter.matches(t) {
			all = append(all, t)
		}
	}
	sortTasksByFiling(all)
	if limit > 0 && len(all) > limit {
		return all[len(all)-limit:], len(all)
	}
	return all, len(all)
}

// sortTasksByFiling puts tasks oldest first by CreatedAt, then by task
// number. A task with no filing time sorts after every task that has one:
// an unknown age is not evidence of being old.
func sortTasksByFiling(tasks []TaskSummary) {
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		switch {
		case a.CreatedAt != nil && b.CreatedAt != nil:
			if !a.CreatedAt.Equal(*b.CreatedAt) {
				return a.CreatedAt.Before(*b.CreatedAt)
			}
		case a.CreatedAt != nil:
			return true
		case b.CreatedAt != nil:
			return false
		}
		return taskIDLess(a.ID, b.ID)
	})
}

// taskIDLess orders "task-9" before "task-10": ids end in a number that
// grain hands out in filing order, and comparing them as strings would
// not.
func taskIDLess(a, b string) bool {
	na, oka := taskIDNumber(a)
	nb, okb := taskIDNumber(b)
	if oka && okb && na != nb {
		return na < nb
	}
	return a < b
}

func taskIDNumber(id string) (int, bool) {
	i := len(id)
	for i > 0 && id[i-1] >= '0' && id[i-1] <= '9' {
		i--
	}
	n, err := strconv.Atoi(id[i:])
	return n, err == nil
}

var taskIDInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"task_id": map[string]any{
			"type":        "string",
			"description": "id of the task to read, as list_grain_tasks reports it",
		},
	},
	"required": []string{"task_id"},
}

// The views read_grain_task answers in. Summary is the default: about a
// kilobyte that says what the task is and what last happened to it, which
// is what a judgement agent re-reads before acting on or naming a task
// (docs/design/durable-tasks.md#DT-13). Full is the whole record, and
// conversation pages the thread by comment id.
const (
	TaskViewSummary      = "summary"
	TaskViewFull         = "full"
	TaskViewConversation = "conversation"
)

var readTaskInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"task_id": map[string]any{
			"type":        "string",
			"description": "id of the task to read, as list_grain_tasks reports it",
		},
		"view": map[string]any{
			"type": "string",
			"enum": []any{TaskViewSummary, TaskViewFull, TaskViewConversation},
			"description": "summary (the default, about 1 KB: state, title, kind tags, base, depends_on, stall, " +
				"the last three events and the head of its handover), full (the whole record), or " +
				"conversation (the thread, paged with \"after\")",
		},
		"after": map[string]any{
			"type":        "number",
			"description": "for view conversation: only comments after this comment id, as the last page ended with",
		},
	},
	"required": []string{"task_id"},
}

func readTaskTool(reader TaskReader) Tool {
	return Tool{
		Name:          "read_grain_task",
		ElisionAdvice: elisionAdviceTaskRecord,
		Description: "Read one grain task. The default view, summary, is about a kilobyte: state, title, kind tags " +
			"(port, repair, session, proposal, durable, imported), base, depends_on, stall, the last three events and the " +
			"head of its handover -- re-read it immediately before acting on a task or naming it in a report. " +
			"view \"full\" is the whole record: what it asked for, who filed it, which " +
			"capabilities it holds, every attempt it has had with how each one ended (the error " +
			"grain recorded for a failed run), what grain's merge queue is doing about landing its " +
			"change and why, what its live run says it is doing, and its full conversation. " +
			"Where the task's own state has stood for longer than that state should take, the line " +
			"under the state says which part of grain should have moved it on and what to look at. " +
			"Read-only, and it needs no permission. This is where you look to explain why another " +
			"task behaved the way it did, or why its change has not merged yet; " +
			"read_grain_task_prompt and read_grain_task_transcript give you the two big pieces " +
			"this deliberately leaves out. view \"conversation\" pages the thread by comment id (\"after\").",
		InputSchema: readTaskInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			if reader == nil {
				return Result{Text: noTaskReader, IsError: true}
			}
			id, _ := argString(args, "task_id")
			if id == "" {
				return Result{Text: "task_id is required", IsError: true}
			}
			view, _ := argString(args, "view")
			view = strings.ToLower(strings.TrimSpace(view))
			switch view {
			case "":
				view = TaskViewSummary
			case TaskViewSummary, TaskViewFull, TaskViewConversation:
			default:
				return Result{Text: fmt.Sprintf("view %q is not one of summary, full or conversation", view), IsError: true}
			}
			task, err := reader.Task(ctx, id)
			if err != nil {
				return Result{Text: fmt.Sprintf("Error reading task %s: %v", id, err), IsError: true}
			}
			var text string
			switch view {
			case TaskViewSummary:
				text = renderTaskSummary(task, time.Now())
			case TaskViewConversation:
				after := int64(-1)
				if f, ok := argFloat(args, "after"); ok {
					after = int64(f)
				}
				text = renderTaskConversation(task, after)
			default:
				text = renderTaskRecord(task, time.Now())
			}
			return Result{Text: elideMiddle(text, int(maxToolResultBytes.Load()), elisionAdviceTaskRecord)}
		},
	}
}

// elisionAdviceTaskRecord is what to do about a task record too long to
// carry back whole -- name the narrower call rather than saying
// "truncated" and leaving the run to re-read the same thing, the rule
// result_size.go's own advice constants follow.
const elisionAdviceTaskRecord = "read the pieces on their own instead -- read_grain_task_prompt for what " +
	"the agent was told, and read_grain_task_transcript for one attempt's own story"

func renderTaskRecord(t TaskRecord, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s: %s\n", t.ID, singleLine(t.Title))
	fmt.Fprintf(&b, "State: %s\n", t.State)
	// Immediately under the state, because it is about that word and
	// nothing else: the state says what this task is waiting for and
	// this says that the wait has gone on longer than it should have.
	// Absent for a task that is inside its patience, which is nearly all
	// of them (TaskSummary.Stall).
	if t.Stall != "" {
		fmt.Fprintf(&b, "  %s\n", singleLine(t.Stall))
	}
	if t.Author != "" {
		fmt.Fprintf(&b, "Filed by: %s (%s)\n", t.Author, t.AuthorKind)
	}
	if t.CreatedAt != nil {
		fmt.Fprintf(&b, "Filed at: %s\n", t.CreatedAt.UTC().Format(time.RFC3339))
	}
	if t.Repo != "" {
		fmt.Fprintf(&b, "Repo: %s", t.Repo)
		if t.Base != "" {
			fmt.Fprintf(&b, " (base %s)", t.Base)
		}
		b.WriteByte('\n')
	}
	if t.Configuration {
		b.WriteString("Kind: configuration agent\n")
	} else if t.Interactive {
		b.WriteString("Kind: interactive\n")
	}
	if len(t.Capabilities) > 0 {
		fmt.Fprintf(&b, "Capabilities: %s\n", strings.Join(t.Capabilities, ", "))
	}
	if t.PullRequest != "" {
		fmt.Fprintf(&b, "Pull request: %s\n", t.PullRequest)
	}
	writeMergeQueue(&b, t.MergeQueue, t.State, now)
	writeActivity(&b, t.Activity, now)
	if t.FailedAttempts > 0 {
		fmt.Fprintf(&b, "Consecutive failed attempts: %d\n", t.FailedAttempts)
		if t.LastFailureReason != "" {
			fmt.Fprintf(&b, "Last failure: %s\n", singleLine(t.LastFailureReason))
		}
	}

	b.WriteString("\nDescription:\n")
	b.WriteString(indent(blankAsNone(t.Description, "(none)")))

	b.WriteString("\nAttempts:\n")
	if len(t.Attempts) == 0 {
		b.WriteString("  (none -- nothing has been dispatched for this task)\n")
	}
	for _, a := range t.Attempts {
		fmt.Fprintf(&b, "  #%d  started %s", a.Number, a.StartedAt.UTC().Format(time.RFC3339))
		if a.FinishedAt != nil {
			fmt.Fprintf(&b, ", finished %s", a.FinishedAt.UTC().Format(time.RFC3339))
		} else {
			b.WriteString(", still running")
		}
		if a.Outcome != "" {
			fmt.Fprintf(&b, ", outcome %s", a.Outcome)
		}
		if on := attemptRanOn(a); on != "" {
			fmt.Fprintf(&b, ", on %s", on)
		}
		// The prompt's own size, on the attempt that was given it: this
		// list read down the page is how a reader sees a task's prompt
		// growing, which is the only warning a conversation-fed prompt
		// gives before it starts costing the run (model.PromptSize).
		if a.PromptBytes > 0 {
			fmt.Fprintf(&b, ", prompt %s (~%d tokens)", humanBytes(a.PromptBytes), a.PromptTokens)
		}
		b.WriteByte('\n')
		if a.Detail != "" {
			fmt.Fprintf(&b, "      %s\n", singleLine(a.Detail))
		}
	}

	if len(t.Transitions) > 0 {
		b.WriteString("\nTransitions:\n")
		for _, tr := range t.Transitions {
			line := fmt.Sprintf("  %s  %s", tr.At.UTC().Format(time.RFC3339), tr.State)
			if tr.Inferred {
				line += " (inferred)"
			}
			if tr.By != "" {
				line += " by " + tr.By
			}
			if tr.Reason != "" {
				line += fmt.Sprintf(" (%s)", singleLine(tr.Reason))
			}
			if tr.Detail != "" {
				line += "; " + singleLine(tr.Detail)
			}
			b.WriteString(line + "\n")
		}
	}

	fmt.Fprintf(&b, "\nConversation (%d):\n", len(t.Comments))
	for _, c := range t.Comments {
		kind := c.AuthorKind
		if c.Handover {
			kind += ", this task's handover: the summary grain carries into its later " +
				"prompts when this thread is too long to quote whole"
		}
		fmt.Fprintf(&b, "  [%s] %s (%s):\n", c.CreatedAt.UTC().Format(time.RFC3339), c.Author, kind)
		b.WriteString(indent(c.Body))
	}
	return strings.TrimRight(b.String(), "\n")
}

// attemptRanOn names the model an attempt ran on, with its framework
// beside it where both are known -- "gemini-3.1-pro (antigravity)" --
// and "" where neither is.
func attemptRanOn(a TaskAttempt) string {
	switch {
	case a.Model != "" && a.AgentFramework != "":
		return fmt.Sprintf("%s (%s)", a.Model, a.AgentFramework)
	case a.Model != "":
		return a.Model
	default:
		return a.AgentFramework
	}
}

// writeMergeQueue says what grain's own merge queue is doing about
// landing the change this task pushed, and writes nothing at all for a
// task the queue has never touched.
//
// Absent rather than empty for that task, which is nearly every task
// there is: a section reading "Merge queue: none" on a task that never
// asked to be merged automatically is a line a reader has to rule out on
// every record they read, and it is the same thing the record already
// does with a repo, a base or a pull request there is none of.
//
// It is written in the words the UI's own row uses (mergeQueuePhrases)
// with the raw state beside them, so that an agent reading this and an
// operator reading their screen are describing the same row in the same
// words -- and so that the operator can still search grain's logs for the
// raw state, which is what those are written in.
func writeMergeQueue(b *strings.Builder, q TaskMergeQueue, taskState string, now time.Time) {
	if q.State == "" && q.BlockedAt == nil {
		return
	}
	if q.State == "" {
		// Blocked with no live state: the queue gave up, and the cycle
		// that noticed the pull request had closed or merged cleared the
		// state out from under the stamp. Say the stamp rather than
		// inventing a state for it.
		b.WriteString("Merge queue: no live state -- grain is driving nothing for this task now\n")
	} else {
		fmt.Fprintf(b, "Merge queue: %s (%s)%s\n",
			mergeQueuePhrase(q.State), q.State, sinceWords(q.At, now, ", since "))
	}
	if q.State != "" && strings.EqualFold(taskState, "closed") {
		// A merge-queue observation can outlive the pull request it
		// describes by a cycle or two, and the UI lets the task's own
		// terminal state win over it (ui/src/state.js's
		// mergeQueueActive). So this is one of the few rows where the
		// record and the screen do not match, and the difference is
		// worth a sentence: an agent quoting "Merging" about a row that
		// shows nothing at all is the confusion this section exists to
		// end, not one to add to.
		b.WriteString("  This task is closed, so grain's UI shows no merge activity for it: " +
			"what the queue last wrote is stale, and nobody looking at that row can see it.\n")
	}
	if q.StalledAt != nil && q.State != "" && !strings.EqualFold(taskState, "closed") {
		// The row and the record agree here, unlike the clause above:
		// grain's UI takes the live mark off a flagged row rather than
		// drawing it as progress (ui/src/state.js's mergeQueueStalled),
		// so an agent saying this and an operator looking at the screen
		// are reading the same thing. The note below is where the
		// classification says what to look at.
		fmt.Fprintf(b, "  Stalled%s: this has stood in that state longer than whatever drives it "+
			"should take, so read it as stuck rather than as waiting -- grain's UI marks the row "+
			"that way too, and the note says where to look.\n",
			sinceWords(q.StalledAt, now, " since "))
	}
	if note := singleLine(q.Note); note != "" {
		fmt.Fprintf(b, "  Note: %s\n", clipNote(note, maxMergeQueueNoteBytes))
	}
	if q.BlockedAt != nil {
		fmt.Fprintf(b, "  Blocked%s: the queue has given up driving this pull request and is waiting "+
			"for a person -- it files no further repair and makes no further attempt of its own "+
			"until somebody presses \"Retry merge\".\n", sinceWords(q.BlockedAt, now, " since "))
	}
}

// mergeQueuePhrases is what each of the merge queue's states is called on
// the row in grain's UI, word for word -- ui/src/state.js's own
// MERGE_QUEUE_PHRASES, over orchestrator queuestatus.go's vocabulary. The
// three are edited together -- merge_queue_phrases_test.go reads the
// other two files and fails when they have drifted apart -- because a
// state added there and not here still answers, but in the raw name
// below rather than in a person's words, which is a degradation nothing
// would otherwise report.
//
// Where this deliberately differs from the browser is the state it does
// not know. state.js renders one as no activity at all, so that an old
// build never shows anybody "waiting_thing"; here the raw state is
// printed instead, because the reader is an agent explaining a row to an
// operator and "grain says waiting_thing, which this build has no words
// for" is a true answer where silence would be a wrong one.
var mergeQueuePhrases = map[string]string{
	"waiting_ci":              "Waiting for CI",
	"waiting_queue":           "Waiting on Queue",
	"waiting_review":          "Waiting for its review",
	"merging":                 "Merging",
	"updating_branch":         "Bringing the branch up to date",
	"batching":                "Testing Batch",
	"gathering":               "Held to be tested with the next change",
	"checks_unfixable":        "Waiting: its checks can't pass from a branch",
	"waiting_github_approval": "Waiting for an approval GitHub requires",
	"github_merge_queue":      "In GitHub's merge queue",
	"repair_needed":           "Needs repairing",
	"repair_queued":           "Queued for repair",
	"repairing":               "Resolving Conflicts",
	"agent_working":           "An agent is on this branch",
	"merge_failed":            "Merge failed",
	"unreadable":              "Can't read the pull request",
}

// MergeQueuePhrase returns the human-readable description of a merge queue state,
// matching the words used on the UI's own row (ui/src/state.js MERGE_QUEUE_PHRASES).
func MergeQueuePhrase(state string) string {
	if phrase, ok := mergeQueuePhrases[state]; ok {
		return phrase
	}
	return "a merge queue state this build has no words for"
}

func mergeQueuePhrase(state string) string {
	return MergeQueuePhrase(state)
}

// writeActivity says what this task's live run is doing, in its own
// words, and nothing at all for the ordinary task that is saying nothing
// -- writeMergeQueue's rule, for the same reason.
//
// Whose voice it is comes with it, the way the UI's own tooltip carries
// it (ui/src/state.js's activityTitle): a phrase grain's dispatch path
// wrote about building a sandbox and a phrase an agent wrote about its
// own work read alike and mean different things.
func writeActivity(b *strings.Builder, a TaskActivity, now time.Time) {
	note := singleLine(a.Note)
	if note == "" {
		return
	}
	whose := "what this run says it is doing"
	if a.BySetup {
		whose = "what grain is doing to get this run started"
	}
	fmt.Fprintf(b, "Activity: %s -- %s%s\n",
		clipNote(note, maxActivityNoteBytes), whose, sinceWords(a.At, now, ", as of "))
}

// maxMergeQueueNoteBytes and maxActivityNoteBytes bound the two free-text
// fields this section carries. Both are written elsewhere in grain and
// are a sentence in practice -- the activity line is capped at
// ui.MaxActivityLength runes where it is written, and the queue's note is
// composed by orchestrator's queuestatus.go -- but neither is bounded by
// anything this package can see, and a record is read for its attempts
// and its conversation as much as for this. The whole-record cap
// (maxToolResultBytes) keeps the answer deliverable either way; these
// keep one long field near the top of it from pushing everything the
// reader came for out of the head.
const (
	maxMergeQueueNoteBytes = 500
	maxActivityNoteBytes   = 300
)

// clipNote cuts a one-line field to a byte budget and says how much went,
// rather than stopping mid-sentence and reading like the whole of it.
//
// The head is kept, not the head and the tail: these are sentences
// somebody or something wrote for a person, and a sentence says what it
// is about first. The cut lands on a rune boundary, since the budget is
// in bytes and half a multi-byte character is invalid UTF-8 in a JSON-RPC
// result -- truncateAddendaNotice's own reasoning, for the same reason.
func clipNote(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return fmt.Sprintf("%s [... %d bytes cut; the whole of it is on that task's row in grain's UI]",
		cut, len(s)-len(cut))
}

// sinceWords renders one of these timestamps the way the row that carries
// it does: the instant, and how long ago it was. Both, because they
// answer different questions -- the age is what says whether a wait is a
// wait or a stall, and the instant is what an operator lines up against
// their own logs.
//
// "" for a nil timestamp, which is a row an older build wrote: no age is
// better than a made-up one.
func sinceWords(at *time.Time, now time.Time, prefix string) string {
	if at == nil {
		return ""
	}
	return fmt.Sprintf("%s%s (%s)", prefix, at.UTC().Format(time.RFC3339), relativeAge(now.Sub(*at)))
}

// relativeAge is ui/src/state.js's relativeAge in Go: as short as the age
// can be said, and deliberately coarse, since the question it answers is
// "is this current?" rather than "how current?".
//
// A negative age is a clock that disagrees with itself -- the daemon
// stamped the row, this process renders it -- and reads as "just now"
// rather than as a time in the future, which is what the browser does
// with the same skew.
func relativeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func readTaskPromptTool(reader TaskReader) Tool {
	return Tool{
		Name:          "read_grain_task_prompt",
		ElisionAdvice: elisionAdviceTaskText,
		Description: "Read the prompt another grain task's agent was actually handed on its most recent " +
			"attempt -- the whole assembled text, not the task's own description. Read-only, and it " +
			"needs no permission. This is the answer to \"why did that run think it was supposed to " +
			"do that\": what a task asks for and what its agent was told are not the same string.",
		InputSchema: taskIDInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			if reader == nil {
				return Result{Text: noTaskReader, IsError: true}
			}
			id, _ := argString(args, "task_id")
			if id == "" {
				return Result{Text: "task_id is required", IsError: true}
			}
			prompt, err := reader.TaskPrompt(ctx, id)
			if err != nil {
				return Result{Text: fmt.Sprintf("Error reading task %s's prompt: %v", id, err), IsError: true}
			}
			if prompt.Prompt == "" {
				return Result{Text: fmt.Sprintf("Task %s has no recorded prompt: nothing has been "+
					"dispatched for it yet, or every attempt failed before its agent got a turn.", id)}
			}
			// The size before the text, because the size is a finding of
			// its own: the tool result below is capped at 16 KB whatever
			// the prompt actually was, so without this line a reader
			// cannot tell a 20 KB prompt from a 400 KB one -- and the
			// difference between those two is most of what "why is this
			// task behaving badly?" has to work with.
			header := fmt.Sprintf("Prompt given to task %s, attempt %d%s:\n\n",
				id, prompt.Attempt, promptSizeNote(prompt))
			return Result{Text: header + elideMiddle(prompt.Prompt, int(maxToolResultBytes.Load()), elisionAdviceTaskText)}
		},
	}
}

// promptSizeNote is how big the prompt was, said in the header above the
// prompt itself -- "" for a record with no measurement, which is every
// answer from a daemon older than the measurement and nothing else.
//
// The window is quoted beside the share for model.PromptSize.String's
// own reason: a bare percentage is a number the reader cannot check, and
// the window it is a share of is an assumption grain states rather than
// a fact it holds.
func promptSizeNote(p TaskPromptRecord) string {
	if p.Bytes <= 0 {
		return ""
	}
	s := fmt.Sprintf(" -- %s, ~%d tokens", humanBytes(p.Bytes), p.Tokens)
	if p.ContextTokens > 0 {
		s += fmt.Sprintf(", %.0f%% of the %d-token window grain measures against",
			float64(p.Tokens)/float64(p.ContextTokens)*100, p.ContextTokens)
	}
	return s
}

// elisionAdviceTaskText is the advice for the two tools whose answer is
// one long recorded string. Unlike a task record there is no narrower
// call to make -- the head and the tail are what there is -- so it says
// where the rest can be read instead of naming a tool that would return
// the same thing again.
const elisionAdviceTaskText = "the whole of it is in grain's own UI, on that task's page -- ask whoever " +
	"you are working with to look at the part you need if the middle turns out to matter"

var transcriptInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"task_id": map[string]any{
			"type":        "string",
			"description": "id of the task whose transcript to read, as list_grain_tasks reports it",
		},
		"attempt": map[string]any{
			"type": "number",
			"description": "which attempt's transcript to read, as read_grain_task numbers them; " +
				"omit for that task's most recent attempt",
		},
	},
	"required": []string{"task_id"},
}

func readTaskTranscriptTool(reader TaskReader) Tool {
	return Tool{
		Name:          "read_grain_task_transcript",
		ElisionAdvice: elisionAdviceTaskText,
		Description: "Read another grain task's session transcript -- one attempt's whole story, its " +
			"agent's output interleaved with every tool call it made and what came back. Read-only, " +
			"and it needs no permission. Omit \"attempt\" for the most recent one. A run still in " +
			"flight has a transcript too, as far as it has got.",
		InputSchema: transcriptInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			if reader == nil {
				return Result{Text: noTaskReader, IsError: true}
			}
			id, _ := argString(args, "task_id")
			if id == "" {
				return Result{Text: "task_id is required", IsError: true}
			}
			attempt := 0
			if f, ok := argFloat(args, "attempt"); ok {
				attempt = int(f)
			}
			if attempt <= 0 {
				latest, problem := latestAttempt(ctx, reader, id)
				if problem != "" {
					return Result{Text: problem, IsError: true}
				}
				attempt = latest
			}
			transcript, err := reader.AttemptTranscript(ctx, id, attempt)
			if err != nil {
				return Result{Text: fmt.Sprintf("Error reading task %s attempt %d's transcript: %v",
					id, attempt, err), IsError: true}
			}
			if transcript == "" {
				return Result{Text: fmt.Sprintf("Task %s attempt %d recorded no transcript. Some agent "+
					"frameworks do not write one; read_grain_task still has that attempt's outcome "+
					"and the error grain recorded for it.", id, attempt)}
			}
			header := fmt.Sprintf("Transcript of task %s, attempt %d:\n\n", id, attempt)
			return Result{Text: header + elideMiddle(transcript, int(maxToolResultBytes.Load()), elisionAdviceTaskText)}
		},
	}
}

// latestAttempt resolves "omit attempt for the most recent one" -- the
// task's own record is the only thing that knows how many attempts there
// have been, so this costs a second read rather than being guessed at
// from a number the caller supplied.
//
// It answers with a sentence for the agent rather than an error, since
// both ways of failing here (the task cannot be read, or it has never
// run) are facts about that task rather than something the caller got
// wrong, and both are what the tool result should say verbatim.
func latestAttempt(ctx context.Context, reader TaskReader, id string) (attempt int, problem string) {
	task, err := reader.Task(ctx, id)
	if err != nil {
		return 0, fmt.Sprintf("Error finding task %s's most recent attempt: %v", id, err)
	}
	if len(task.Attempts) == 0 {
		return 0, fmt.Sprintf("Task %s has had no attempts, so there is no transcript to read.", id)
	}
	return task.Attempts[len(task.Attempts)-1].Number, ""
}

// singleLine flattens a string onto one line, for the places that print
// one field per line: a title or an error detail with a newline in it
// would otherwise break the shape of the whole listing.
func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func blankAsNone(s, none string) string {
	if strings.TrimSpace(s) == "" {
		return none
	}
	return s
}

// indent shifts a block of prose in by two spaces, so a description or a
// comment body cannot be mistaken for the record's own fields.
func indent(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	return b.String()
}
