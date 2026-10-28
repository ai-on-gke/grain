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
	"net/http"
	"time"
)

// WaitCredit is what one report of blocked time bought, as
// POST /api/tasks/{id}/wait-credit answers: the total time the run is now
// recorded as having spent waiting on CI, how much of it has been
// credited back to its wall-clock deadline, and where that deadline now
// stands.
//
// Milliseconds on the wire because the caller is a program rather than a
// page -- the forked mcpserver serving one run's wait_for_checks
// (mcp.WaitCreditReporter) -- and a duration is the one thing JSON has no
// spelling for that both ends would read the same way.
//
// Live is false when the task had no run in flight to credit, which is a
// 200 rather than an error for the reason Activity.Live is: a report that
// crossed with grain finishing the run has done nothing wrong, there is
// simply no deadline left to move, and the tool that asked needs to know
// that rather than to read a failure into it.
type WaitCredit struct {
	BlockedMS  int64      `json:"blockedMs"`
	CreditedMS int64      `json:"creditedMs"`
	Deadline   *time.Time `json:"deadline,omitempty"`
	Live       bool       `json:"live"`
}

// waitCreditRequest is POST /api/tasks/{id}/wait-credit's body: how long
// this run has spent blocked on CI in total, over its whole life.
//
// Cumulative rather than a delta, so that a report lost to a dropped
// request costs nothing but its own lateness -- the next one carries the
// same total again -- and so that a caller repeating itself cannot
// inflate its own credit. See orchestrator.RunWaitCredits.Credit, which
// keeps the largest total it has been told rather than a sum.
type waitCreditRequest struct {
	BlockedMS int64 `json:"blockedMs"`
}

// WaitCreditor is implemented by whatever actually holds a live run's
// wall-clock timer -- pkg/cli/daemon.go's own adapter over
// orchestrator.RunWaitCredits, in a real deployment. See
// Config.WaitCredit's doc comment for the nil-means-unavailable contract
// this interface's zero value satisfies.
type WaitCreditor interface {
	CreditWaitForTask(ctx context.Context, taskID string, blocked time.Duration) (WaitCredit, error)
}

// errWaitCreditUnavailable is what handleCreditWait reports when
// Config.WaitCredit is nil -- see that field's own doc comment.
var errWaitCreditUnavailable = errors.New(
	"crediting a run for time spent waiting on CI is not available: this deployment's UI is not " +
		"wired to the daemon that owns its runs")

// errWaitCreditNegative refuses a report of negative blocked time. A
// ValidationError, so it is a 400 rather than a 500: it is the caller's
// own mistake, and the only thing crediting it could do is bring a run's
// deadline forward -- taking back time the agent has already been told it
// has, on the say-so of a bad number.
var errWaitCreditNegative = validationErrorf("blocked time cannot be negative")

// CreditWait records that id's live run has now spent blocked on CI for
// the given total, and moves its wall-clock deadline out by whatever that
// earns (orchestrator.RunWaitCreditCap).
//
// This exists for the agent, the same way RecreateSandbox and
// SetTaskActivity do: a dispatched run reaches it from inside its own
// wait_for_checks call, through the MCP server its framework forked
// (pkg/mcp's wait_credit.go, wired in pkg/cli/mcpserver.go). It is the
// one route between the process that can measure the waiting and the
// process that decides when the run dies for it.
//
// Unlike those two it is called *during* a single tool call rather than
// between calls, several times over one wait, which is what makes the
// credit arrive while it can still stop the wait being cut short. That
// makes it cheap by necessity: it moves a timer in memory and touches no
// store at all beyond reading the task it names.
//
// A task with no live run is not an error, for the reason WaitCredit.Live
// gives.
func (c *Client) CreditWait(ctx context.Context, id string, blocked time.Duration) (WaitCredit, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return WaitCredit{}, err
	}
	if task == nil {
		return WaitCredit{}, &NotFoundError{ID: id}
	}
	if blocked < 0 {
		return WaitCredit{}, errWaitCreditNegative
	}
	if c.Config.WaitCredit == nil {
		return WaitCredit{}, errWaitCreditUnavailable
	}
	return c.Config.WaitCredit.CreditWaitForTask(ctx, id, blocked)
}

// handleCreditWait answers POST /api/tasks/{id}/wait-credit.
//
// The nil check is here as well as in CreditWait so an unwired
// deployment answers 404 -- "this deployment does not offer that" --
// exactly as handleRecreateSandbox does, rather than turning a missing
// feature into a 500 that reads like a failure. The tool on the other end
// treats both the same way in any case: no credit granted, and a wait
// bounded by the deadline as it stands.
func (s *Server) handleCreditWait(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.WaitCredit == nil {
		writeError(w, http.StatusNotFound, errWaitCreditUnavailable)
		return
	}
	var req waitCreditRequest
	if !readJSON(w, r, &req) {
		return
	}
	credit, err := s.tasks.CreditWait(r.Context(), r.PathValue("id"),
		time.Duration(req.BlockedMS)*time.Millisecond)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, credit)
}
