package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
)

// LogSource is one named stream of recent log lines the UI's System
// pane can show, oldest first -- journalctl for the daemon's own unit, a plain
// file for the git proxy's audit log, or anything else a deployment
// wants to surface, each behind whatever mechanism actually fits it
// (see pkg/systemlog).
//
// lines is always positive: handleGetLogLines rejects a non-positive
// ?lines= with a 400 before it gets here, and defaults it when the
// caller says nothing, so no implementation has to invent an answer for
// "the last zero lines". The ones in pkg/systemlog nonetheless refuse a
// non-positive count themselves rather than trusting this handler to be
// their only caller.
type LogSource interface {
	Tail(ctx context.Context, lines int) ([]string, error)
}

// defaultLogLines is how many lines GET /api/logs/{source} returns when
// the caller does not say -- enough to see recent activity without
// pulling an unbounded journal through a JSON response by default.
const defaultLogLines = 500

// maxLogLines caps what a caller can ask for in one request, regardless
// of ?lines=.
const maxLogLines = 5000

// logSourcesResponse is GET /api/logs' whole body. Enabled is false,
// with no sources listed, when this deployment's UI has no log sources
// configured -- the frontend uses that to hide the System pane's own
// Logs tab entirely, the same convention upgradeStatusResponse's own Enabled
// already establishes.
type logSourcesResponse struct {
	Enabled bool     `json:"enabled"`
	Sources []string `json:"sources,omitempty"`
}

// handleListLogSources answers GET /api/logs. The daemon's logs are a
// system administrator's to read, and so is the list of them.
func (s *Server) handleListLogSources(w http.ResponseWriter, r *http.Request) {
	if len(s.tasks.Config.Logs) == 0 {
		writeJSON(w, http.StatusOK, logSourcesResponse{Enabled: false})
		return
	}
	names := make([]string, 0, len(s.tasks.Config.Logs))
	for name := range s.tasks.Config.Logs {
		names = append(names, name)
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, logSourcesResponse{Enabled: true, Sources: names})
}

// logLinesResponse is GET /api/logs/{source}'s whole body.
type logLinesResponse struct {
	Lines []string `json:"lines"`
}

func (s *Server) handleGetLogLines(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("source")
	source, ok := s.tasks.Config.Logs[name]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such log source: %q", name))
		return
	}
	n := defaultLogLines
	if raw := r.URL.Query().Get("lines"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("lines must be a positive integer"))
			return
		}
		n = parsed
	}
	if n > maxLogLines {
		n = maxLogLines
	}
	lines, err := source.Tail(r.Context(), n)
	if err != nil {
		// This route's own answer is the daemon's log, so why the log
		// could not be read discloses nothing more.
		writeError(w, http.StatusInternalServerError, apierror.Expose(err))
		return
	}
	redactor := grainlog.NewRedactor(nil)
	if s.tasks.Config.LogRedactor != nil {
		redactor, err = s.tasks.Config.LogRedactor()
		if err != nil || redactor == nil {
			if err == nil {
				err = errors.New("no redactor")
			}
			writeError(w, http.StatusInternalServerError, apierror.Expose(fmt.Errorf(
				"this log cannot be shown: the credentials that would have to be redacted out of it "+
					"could not be read, and it is not served unredacted instead: %w", err)))
			return
		}
	}
	lines, _ = redactor.Redact(lines)
	lines = redactDatabaseConfirmTokens(lines)
	writeJSON(w, http.StatusOK, logLinesResponse{Lines: lines})
}
