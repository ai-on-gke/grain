package ui

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/bwsalmon/grain/pkg/console"
)

// ConsoleSessions is implemented by whatever holds this deployment's live
// interactive sessions -- *console.Manager, in a real deployment, and the
// same instance orchestrator.Config.Consoles registers them in.
//
// An interface so a deployment with no consoles hands in nil and these
// routes answer that they are not served, rather than panicking on a
// manager nobody built. It is stated in pkg/console's own types for
// pkg/ci's reason (Config.CI's, ci.go): a session is not a shape this package
// renders into something else, and pkg/console imports nothing this one
// has to keep away from.
type ConsoleSessions interface {
	List() []console.Info
	Get(taskID string) *console.Session
	Close(taskID string) error
}

// errConsolesUnavailable is what these routes report when Config.Consoles
// is nil, in errRebootUnavailable's own shape.
var errConsolesUnavailable = errors.New(
	"interactive consoles are not available: this deployment serves none")

// maxConsoleInput caps one keystroke batch. What a person types between
// two polls is a line at most; a paste is the case worth having room for,
// and this is far past the largest sensible one while still being a bound
// on a body a client sets the size of.
const maxConsoleInput = 1 << 20

// ConsoleSession is one live session as this API reports it: which task
// it belongs to, enough about that task to list it without looking the
// task up, and how it is getting on.
type ConsoleSession struct {
	TaskID    string    `json:"taskId"`
	RunID     string    `json:"runId,omitempty"`
	Title     string    `json:"title,omitempty"`
	Framework string    `json:"framework,omitempty"`
	Repo      string    `json:"repo,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	// Live is false for a session whose CLI has exited but which nobody
	// has closed yet: still readable, still on the list, and closing it
	// is still what takes it off.
	Live bool `json:"live"`
	// Bytes is everything this session has ever printed, including what
	// the ring has since dropped -- what a client shows as "how much has
	// happened here" without asking for any of it.
	Bytes int64 `json:"bytes"`
}

// ConsoleSessionsResponse is what GET /api/console answers with.
type ConsoleSessionsResponse struct {
	// Enabled is whether this deployment serves consoles at all. False
	// means Sessions is empty because there is nowhere for one to run,
	// not because nobody has opened one.
	Enabled  bool             `json:"enabled"`
	Sessions []ConsoleSession `json:"sessions"`
}

// ConsoleOutputResponse is what GET /api/console/{task}/output answers
// with: the bytes since the offset asked for, and the offset to ask for
// next time.
type ConsoleOutputResponse struct {
	// Output is base64, not a string: a terminal emits arbitrary bytes --
	// escape sequences, and whatever half of a UTF-8 rune happened to be
	// at the end of a read -- and JSON strings cannot carry those without
	// silently replacing them. The client decodes and feeds them to its
	// terminal emulator verbatim.
	Output string `json:"output"`
	Offset int64  `json:"offset"`
	// Dropped says the ring lost output between what the client last read
	// and what it is being given now (console.MaxBuffer). A terminal
	// recovers from that by redrawing, not by asking again, so this is a
	// fact to show rather than an error.
	Dropped bool `json:"dropped"`
	Live    bool `json:"live"`
}

// consoleInputRequest is the body of POST /api/console/{task}/input:
// base64 for ConsoleOutputResponse.Output's own reason -- ^C is a byte,
// not a character.
type consoleInputRequest struct {
	Input string `json:"input"`
}

// consoleResizeRequest is the body of POST /api/console/{task}/resize:
// how big the window showing this session is, in character cells.
type consoleResizeRequest struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// handleListConsoles is the Consoles section: every session running now,
// newest first.
func (s *Server) handleListConsoles(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.Consoles == nil {
		writeJSON(w, http.StatusOK, ConsoleSessionsResponse{})
		return
	}
	infos := s.tasks.Config.Consoles.List()
	sessions := make([]ConsoleSession, 0, len(infos))
	for _, info := range infos {
		if s.tasks.Store != nil {
			task, err := s.tasks.Store.GetTask(r.Context(), info.TaskID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if task == nil {
				continue
			}
		}
		sessions = append(sessions, ConsoleSession{
			TaskID:    info.TaskID,
			RunID:     info.RunID,
			Title:     info.Title,
			Framework: info.Framework,
			Repo:      info.Repo,
			StartedAt: info.StartedAt,
			Live:      info.Live,
			Bytes:     info.Bytes,
		})
	}
	writeJSON(w, http.StatusOK, ConsoleSessionsResponse{Enabled: true, Sessions: sessions})
}

// handleConsoleOutput is a client's poll: everything this session has
// printed since the offset it was given last time.
//
// Polling rather than a stream because that is the transport this API has
// (pkg/console's own doc comment): every other live pane here polls, and
// a websocket would buy a terminal fewer round trips and nothing else.
func (s *Server) handleConsoleOutput(w http.ResponseWriter, r *http.Request) {
	session, ok := s.console(w, r)
	if !ok {
		return
	}
	var offset int64
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, errors.New("offset must be a non-negative integer"))
			return
		}
		offset = parsed
	}
	data, next, dropped := session.Output(offset)
	writeJSON(w, http.StatusOK, ConsoleOutputResponse{
		Output:  base64.StdEncoding.EncodeToString(data),
		Offset:  next,
		Dropped: dropped,
		Live:    session.Info().Live,
	})
}

// handleConsoleInput is somebody typing.
func (s *Server) handleConsoleInput(w http.ResponseWriter, r *http.Request) {
	session, ok := s.console(w, r)
	if !ok {
		return
	}
	var req consoleInputRequest
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Input) > maxConsoleInput {
		writeError(w, http.StatusBadRequest, errors.New("that is more input than a session accepts at once"))
		return
	}
	input, err := base64.StdEncoding.DecodeString(req.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("input must be base64"))
		return
	}
	if err := session.Write(input); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleResize tells the session how large the window showing it
// is, so its CLI redraws to fit rather than to the 80x24 a fresh terminal
// claims.
func (s *Server) handleConsoleResize(w http.ResponseWriter, r *http.Request) {
	session, ok := s.console(w, r)
	if !ok {
		return
	}
	var req consoleResizeRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := session.Resize(req.Rows, req.Cols); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCloseConsole is the explicit close an interactive task waits for:
// the CLI ends, the run it was driving returns, and whatever that run
// pushed becomes a pull request exactly as an unattended run's would.
func (s *Server) handleCloseConsole(w http.ResponseWriter, r *http.Request) {
	_, ok := s.console(w, r)
	if !ok {
		return
	}
	if err := s.tasks.Config.Consoles.Close(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// console resolves the session these routes are about, answering the
// request itself when there is not one.
func (s *Server) console(w http.ResponseWriter, r *http.Request) (*console.Session, bool) {
	if s.tasks.Config.Consoles == nil {
		writeError(w, http.StatusNotFound, errConsolesUnavailable)
		return nil, false
	}
	id := r.PathValue("id")
	if s.tasks.Store != nil {
		task, err := s.tasks.Store.GetTask(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return nil, false
		}
		if task == nil {
			writeError(w, http.StatusNotFound, console.ErrNoSession)
			return nil, false
		}
	}
	session := s.tasks.Config.Consoles.Get(id)
	if session == nil {
		writeError(w, http.StatusNotFound, console.ErrNoSession)
		return nil, false
	}
	return session, true
}
