package ui

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwsalmon/grain/pkg/mcp"
)

// MaxActivityLength is how long a run's synopsis of itself may be, in
// runes. It is a display constraint rather than a storage one: the phrase
// is rendered on a task row beside the title, and one that runs past a
// line stops being a glance and starts being a paragraph nobody reads.
//
// Enforced here, at the daemon, rather than only in the tool that calls
// it (mcp.MaxStatusLength names the same number to the agent): this is
// the write, and a limit checked only where the polite caller happens to
// be is not a limit.
const MaxActivityLength = 120

// Activity is what one task's live run says it is doing right now, as
// POST /api/tasks/{id}/activity answers -- the phrase as it was stored,
// and when it was stored.
//
// Live is false when the task had no run in flight to record anything
// against, which is a 200 rather than an error: a run whose call lands
// just after grain has already finished it has done nothing wrong, and
// the honest answer is "nobody is listening any more", which the tool
// then passes on to the agent.
//
// IsDeclaredWait is whether Note puts the run inside a declared wait
// (mcp.IsDeclaredWait: wait_for_checks, run_ci_job, wait_for_task, chat
// -- and their sandbox CLI forms, grain ci wait and grain ci run-job,
// which the sandbox proxy stamps with the same phrases), the state a
// durable run's idle watchdog leaves alone however quiet the run is.
type Activity struct {
	Note           string     `json:"note,omitempty"`
	At             *time.Time `json:"at,omitempty"`
	Live           bool       `json:"live"`
	IsDeclaredWait bool       `json:"isDeclaredWait,omitempty"`
}

// activityRequest is POST /api/tasks/{id}/activity's body: the one short
// phrase to record.
type activityRequest struct {
	Note string `json:"note"`
}

// errActivityEmpty and errActivityLong are the two ways a synopsis is
// refused -- ValidationErrors, so each is a 400 rather than a 500. Both
// are the caller's own mistake and both say what to send instead, since
// the caller is a model reading the error as its next instruction.
var errActivityEmpty = validationErrorf("a status note cannot be empty")

var errActivityLong = validationErrorf(
	"a status note must be at most %d characters -- it is shown on one line beside the task's title",
	MaxActivityLength)

// SetTaskActivity records what id's live run is doing, for the task list
// to show while it runs (model.Store.SetTaskActivity).
//
// This exists for the agent, the same way RecreateSandbox next door does:
// a dispatched run reaches it through the update_status tool its own MCP
// server exposes (pkg/mcp, wired in pkg/cli/mcpserver.go), which is the
// only route a run has back to the daemon that dispatched it. Nothing
// about it is agent-specific, though, and it is deliberately not a state
// change: the note is a phrase to read, never anything grain acts on, so
// the worst a wrong one can do is mislead a person for as long as it
// stands.
//
// The note is normalized before it is stored -- whitespace collapsed onto
// one line -- because it is rendered as a single line and a caller that
// sends two is better served by having them joined than by having the
// second silently cut off.
//
// A task with no live run is not an error. The run may have been
// cancelled or finished between the agent writing the call and the daemon
// reading it, and there is nothing for either side to repair; the answer
// says so in Live, and the tool tells the agent that its status is no
// longer being shown.
func (c *Client) SetTaskActivity(ctx context.Context, id, note string) (Activity, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return Activity{}, err
	}
	if task == nil {
		return Activity{}, &NotFoundError{ID: id}
	}
	note = normalizeActivity(note)
	switch {
	case note == "":
		return Activity{}, errActivityEmpty
	case utf8.RuneCountInString(note) > MaxActivityLength:
		return Activity{}, errActivityLong
	}
	at := time.Now().UTC()
	live, err := c.Store.SetTaskActivity(ctx, id, note, at)
	if err != nil {
		return Activity{}, err
	}
	if !live {
		return Activity{Live: false}, nil
	}
	return Activity{Note: note, At: &at, Live: true, IsDeclaredWait: mcp.IsDeclaredWait(note)}, nil
}

// normalizeActivity flattens a note onto one line: every run of
// whitespace, newlines included, becomes a single space, and the ends are
// trimmed. A caller that sends "" or nothing but whitespace gets "" back,
// which SetTaskActivity above refuses.
func normalizeActivity(note string) string {
	return strings.Join(strings.Fields(note), " ")
}

// handleSetTaskActivity answers POST /api/tasks/{id}/activity.
//
// Unlike handleRecreateSandbox and handleOpenPullRequest there is no
// "this deployment is not wired for it" case to answer 404 for: the write
// lands in the store this server is already holding, so any deployment
// with a task to name can record one.
func (s *Server) handleSetTaskActivity(w http.ResponseWriter, r *http.Request) {
	var req activityRequest
	if !readJSON(w, r, &req) {
		return
	}
	activity, err := s.tasks.SetTaskActivity(r.Context(), r.PathValue("id"), req.Note)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, activity)
}
