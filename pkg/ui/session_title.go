package ui

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
)

// SessionTitle is what PUT /api/tasks/{id}/session-title answers: which
// standing session the task was, and the title it now carries, as it was
// stored.
type SessionTitle struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
}

// sessionTitleRequest is PUT /api/tasks/{id}/session-title's body: the
// one title to write.
type sessionTitleRequest struct {
	Title string `json:"title"`
}

// errSessionTitleEmpty and errSessionTitleLong are the two ways a title
// is refused -- ValidationErrors, so each is a 400 rather than a 500.
// Both are the caller's own mistake and both say what to send instead,
// since the caller is a model reading the error as its next instruction.
var errSessionTitleEmpty = validationErrorf("a session title cannot be empty")

var errSessionTitleLong = validationErrorf(
	"a session title must be at most %d characters -- it is shown in the rail and on one task row",
	model.ConsoleSessionTitleMaxLength)

// errSessionAlreadyNamed is the refusal an agent's rename meets once the
// session is no longer titled by the heuristic: a session is named by
// its agent once, at most, and a person's name is never overwritten.
func errSessionAlreadyNamed(session model.ConsoleSession, source model.ConsoleSessionTitleSource) error {
	who := "a person"
	if source == model.TitleSourceAgent {
		who = "an earlier run of this session"
	}
	return validationErrorf(
		"this session is already named %q, by %s, so it keeps that name: rename_session only replaces "+
			"the title a session gets from its first message, once. Do not call it again; carry on with the conversation",
		model.ConsoleSessionTitle(session), who)
}

// RenameConsoleSessionOfTask retitles the standing session whose
// conversation task id is, and nothing else about it.
//
// This exists for the agent, the way SetTaskActivity does: a session's
// own run reaches it through the rename_session tool its MCP server
// exposes (pkg/mcp, wired in pkg/daemon/mcpserver.go), which is the only
// route a run has back to the daemon that dispatched it. A session
// started from a chat is named by a heuristic on its opening line
// (model.ConsoleSessionTitleFromMessage), and the agent on the other end
// is what knows, a few turns in, what the conversation is actually
// about.
//
// It is keyed by the task rather than the session for the reason the
// tool is: which task a run serves is fixed at process start, so a run
// can only ever rename its own chat. A task that is not a console
// session (model.Task.IsConsoleSession) is refused, which is also what
// keeps an ordinary run from reaching a settings row through this door.
//
// It names a session once, at most. The title is written only while it
// is still the heuristic one (model.TitleSourceHeuristic) and is
// recorded as the agent's; after that, or once a person has named the
// session, it is refused with errSessionAlreadyNamed. A standing session
// is redispatched for every message, each time to a fresh agent, and a
// door that stayed open let each of them retitle the chat whenever the
// latest turn changed subject. A person renames freely from the UI
// (PUT /api/console-sessions/{id}), which this does not touch.
//
// The write itself is UpdateConsoleSession's, the same path PUT
// /api/console-sessions/{id} takes when a person edits the title in the
// settings pane -- so a deployment whose settings are read-only refuses
// this the same way, and the reconciler carries the new title onto the
// task on its next cycle (orchestrator.syncConsoleSessionTask) exactly as
// it does for a title typed there. Nothing here touches the task.
func (c *Client) RenameConsoleSessionOfTask(ctx context.Context, taskID, title string) (SessionTitle, error) {
	task, err := c.Store.GetTask(ctx, taskID)
	if err != nil {
		return SessionTitle{}, err
	}
	if task == nil {
		return SessionTitle{}, &NotFoundError{ID: taskID}
	}
	if !task.IsConsoleSession() {
		return SessionTitle{}, validationErrorf("task %s is not a standing chat session, so it has no session to rename", taskID)
	}
	sessionID, _ := model.ConsoleSessionIDOf(*task)
	title = normalizeActivity(title)
	switch {
	case title == "":
		return SessionTitle{}, errSessionTitleEmpty
	case utf8.RuneCountInString(title) > model.ConsoleSessionTitleMaxLength:
		return SessionTitle{}, errSessionTitleLong
	}
	comments, err := c.Store.Comments(ctx, taskID)
	if err != nil {
		return SessionTitle{}, err
	}
	sessions, err := c.UpdateConsoleSession(ctx, sessionID, UpdateConsoleSessionRequest{
		Title:         &title,
		renameByAgent: true,
		firstMessage:  model.ConsoleSessionFirstMessage(comments),
	})
	if err != nil {
		return SessionTitle{}, err
	}
	stored := title
	for _, s := range sessions {
		if s.ID == sessionID {
			stored = strings.TrimSpace(s.Title)
			break
		}
	}
	return SessionTitle{SessionID: sessionID, Title: stored}, nil
}

// handleRenameTaskSession answers PUT /api/tasks/{id}/session-title.
func (s *Server) handleRenameTaskSession(w http.ResponseWriter, r *http.Request) {
	var req sessionTitleRequest
	if !readJSON(w, r, &req) {
		return
	}
	renamed, err := s.tasks.RenameConsoleSessionOfTask(r.Context(), r.PathValue("id"), req.Title)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, renamed)
}

// RenameConsoleSessionOfTask is Client.RenameConsoleSessionOfTask over
// the wire: retitle the standing session task id is the conversation
// of. Its caller, like SetTaskActivity's, is `grain mcpserver`, serving
// the session's own run.
func (c *HTTPClient) RenameConsoleSessionOfTask(ctx context.Context, id, title string) (SessionTitle, error) {
	var renamed SessionTitle
	if err := c.do(ctx, http.MethodPut, "/api/tasks/"+id+"/session-title",
		sessionTitleRequest{Title: title}, &renamed); err != nil {
		return SessionTitle{}, err
	}
	return renamed, nil
}
