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
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/systemlog"
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
	// Unit is the systemd unit these lines were read from, for a source
	// that reads one (UnitLogSource): the daemon's own, or the one
	// ?unit= named. Absent for a source that is not a journal.
	Unit *LogUnit `json:"unit,omitempty"`
	// Units is every unit ?unit= may name, the running one first --
	// present only when there is more than one, which is the host
	// carrying both grain's system unit and its user unit, where a
	// reader has to be told which it got.
	Units []LogUnit `json:"units,omitempty"`
}

// LogUnit is one systemd unit a journal source can read, as the route
// names it.
type LogUnit struct {
	// Unit is the unit's name, and what ?unit= takes.
	Unit string `json:"unit"`
	// Scope is "system" or "user": which manager runs it.
	Scope string `json:"scope"`
	// Running is true for the unit this process runs under -- the one
	// read when ?unit= says nothing.
	Running bool `json:"running,omitempty"`
}

// UnitLogSource is a LogSource over a systemd unit's journal on a host
// where another unit could have been meant (systemlog.DaemonJournal,
// through DaemonJournal below): GET /api/logs/{source} says which unit
// it read and takes ?unit= to pick another of Units. A LogSource that
// is not one of these reads what it reads, and ?unit= on it is refused.
type UnitLogSource interface {
	LogSource
	// Units is every unit this source may read, the running one first.
	Units() []systemlog.Unit
	// ForUnit is the source over one of Units, by name, or an error
	// naming the units there are.
	ForUnit(unit string) (LogSource, error)
}

// DaemonJournal is systemlog.DaemonJournal as a UnitLogSource: what a
// host deployment wires as its "daemon" source.
type DaemonJournal struct{ systemlog.DaemonJournal }

// ForUnit implements UnitLogSource.
func (d DaemonJournal) ForUnit(unit string) (LogSource, error) {
	journal, err := d.Journal(unit)
	if err != nil {
		return nil, err
	}
	return journal, nil
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
	var unit *LogUnit
	var units []LogUnit
	if selectable, ok := source.(UnitLogSource); ok {
		all := selectable.Units()
		chosen := all[0]
		if want := r.URL.Query().Get("unit"); want != "" {
			picked, err := selectable.ForUnit(want)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			source = picked
			for _, u := range all {
				if u.Name == want {
					chosen = u
				}
			}
		}
		unit = &LogUnit{Unit: chosen.Name, Scope: chosen.Scope(), Running: chosen == all[0]}
		if len(all) > 1 {
			for _, u := range all {
				units = append(units, LogUnit{Unit: u.Name, Scope: u.Scope(), Running: u == all[0]})
			}
		}
	} else if r.URL.Query().Get("unit") != "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("log source %q is not a systemd journal: unit= does not apply to it", name))
		return
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
	writeJSON(w, http.StatusOK, logLinesResponse{Lines: lines, Unit: unit, Units: units})
}
