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

package ui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/console"
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
	Enabled    bool             `json:"enabled"`
	Sessions   []ConsoleSession `json:"sessions"`
	Total      int              `json:"total,omitempty"`
	Truncated  bool             `json:"truncated,omitempty"`
	NextCursor string           `json:"nextCursor,omitempty"`
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
	list, err := parseListQuery(r, listPageLarge)
	if err != nil {
		writeClientError(w, err)
		return
	}
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
	resp := ConsoleSessionsResponse{Enabled: true, Sessions: sessions}
	if list.Paged {
		matched := filterList(sessions, func(cs ConsoleSession) bool {
			return list.matches(cs.TaskID, cs.Title, cs.Repo)
		})
		page := pageOfBy(matched, list, func(cs ConsoleSession) string { return cs.TaskID })
		resp.Sessions = page.Items
		resp.Total = page.Total
		resp.Truncated = page.Truncated
		resp.NextCursor = page.NextCursor
	}
	writeJSON(w, http.StatusOK, resp)
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
	// A console is a run's terminal, and sessions:read -- grain/viewer's
	// -- reads anybody's: what it printed goes through the same redaction
	// the Logs pane's lines do, so a credential the run cat-ed or echoed
	// is not handed to a reader who holds no grant of it. Offsets stay the
	// session's own, so a client asking for what follows is unaffected.
	//
	// The offset is the caller's to choose, so the redaction reads from
	// the start of the line it falls in (consoleRedactContext at most
	// before it): asked for what follows a point inside a credential, the
	// route cuts the whole credential rather than serve the rest of it.
	from := max(offset-consoleRedactContext, 0)
	window, next, _ := session.Output(from)
	start := next - int64(len(window))
	var dropped bool
	switch {
	case offset < start:
		dropped = offset > 0 || start > 0
		offset = start
	case offset > next:
		// What console.Session.Output does too: a client asking about
		// output that does not exist yet is given all there is.
		dropped = true
		window, next, _ = session.Output(0)
		start = next - int64(len(window))
		offset = start
	}
	// Redaction recognises a credential only whole, and a PTY hands a
	// write over in as many reads as it likes: a poll landing between two
	// of them would serve the first half of a credential that the next
	// poll, reading from the start of its line, cuts too late. So while
	// the run is still printing, the token it has not finished is left
	// for the next poll (consoleHeldTail). LastOutput is read after
	// Output, so output arriving between the two only holds back more.
	live := session.Info().Live
	fresh := live && time.Since(session.LastOutput()) < consoleHoldQuiet
	if held := consoleHeldTail(window, int(offset-start), fresh); held > 0 {
		window = window[:len(window)-held]
		next -= int64(held)
	}
	data := s.redactConsoleOutput(window, int(offset-start))
	writeJSON(w, http.StatusOK, ConsoleOutputResponse{
		Output:  base64.StdEncoding.EncodeToString(data),
		Offset:  next,
		Dropped: dropped,
		Live:    live,
	})
}

// consoleHoldQuiet is how long a live console must have printed nothing
// before handleConsoleOutput serves a trailing token it has not seen the
// end of. A run cat-ing a credential writes it in one go, so its pieces
// arrive microseconds apart; somebody typing sees an unfinished long
// word echoed once they pause or press space.
var consoleHoldQuiet = 500 * time.Millisecond

const (
	// consoleHoldMin is the shortest trailing token held back: shorter
	// than any credential shape or stored secret worth cutting, and long
	// enough that a prompt such as "$ " or "[y/N]" is never held.
	consoleHoldMin = 8
	// consoleHoldMax is the longest: a run streaming one unbroken token
	// past this is not printing a credential, and holding it would stall
	// the terminal for as long as the stream lasts.
	consoleHoldMax = 8 << 10
)

// consoleHeldTail is how many bytes at the end of window[at:]
// handleConsoleOutput leaves for the next poll: the trailing run with no
// whitespace after it, when the console printed within consoleHoldQuiet
// (fresh) and the run is between consoleHoldMin and consoleHoldMax long.
// It may be the first part of a credential still arriving, which the log
// redactor can only recognise once it is whole. Never more than
// window[at:], so a poll is answered with nothing rather than going back.
func consoleHeldTail(window []byte, at int, fresh bool) int {
	if !fresh {
		return 0
	}
	tail := len(window) - (bytes.LastIndexAny(window, " \t\r\n") + 1)
	if tail < consoleHoldMin || tail > consoleHoldMax {
		return 0
	}
	return min(tail, len(window)-at)
}

// consoleRedactContext is how far before a requested offset
// handleConsoleOutput reads for the start of its line.
const consoleRedactContext = 64 << 10

// redactConsoleOutput is window[at:] with every credential the log
// redactor knows, and every credential shape, cut out -- cut with the
// line at falls in read whole, so one that starts before at is cut too.
// Without a redactor of the daemon's own it still cuts the shapes; one
// that cannot be built withholds the output rather than serve it
// unredacted.
func (s *Server) redactConsoleOutput(window []byte, at int) []byte {
	data := window[at:]
	if len(data) == 0 {
		return data
	}
	redactor := grainlog.NewRedactor(nil)
	if s.tasks.Config.LogRedactor != nil {
		var err error
		redactor, err = s.tasks.Config.LogRedactor()
		if err != nil || redactor == nil {
			return []byte("\r\n[grain: this output is withheld: the credentials that would have to be redacted out of it could not be read]\r\n")
		}
	}
	line := bytes.LastIndexByte(window[:at], '\n') + 1
	before, _ := redactor.Text(string(window[line:at]))
	out, n := redactor.Text(string(window[line:]))
	if n == 0 {
		return data
	}
	if strings.HasPrefix(out, before) {
		return []byte(out[len(before):])
	}
	// A credential straddles at: serve from where the two diverge, which
	// repeats a little of what the client has rather than any of it.
	common := 0
	for common < len(before) && common < len(out) && before[common] == out[common] {
		common++
	}
	return []byte(out[common:])
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
		if a := accessRoleFromContext(r.Context()); a != nil && !a.Full {
			allowedRepo := func(need APIScope, targetRepo string) bool {
				return targetRepo == "" || a.allows(need, targetRepo)
			}
			if err := s.tasks.roleTaskWithin(r.Context(), a, *task, "access console of", allowedRepo, s.normalizeCapFn()); err != nil {
				writeError(w, http.StatusForbidden, err)
				return nil, false
			}
		}
	}
	session := s.tasks.Config.Consoles.Get(id)
	if session == nil {
		writeError(w, http.StatusNotFound, console.ErrNoSession)
		return nil, false
	}
	return session, true
}
