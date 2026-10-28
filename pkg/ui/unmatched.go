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

// GET /api/unmatched-failures: what runs said when they failed for a
// reason grain could not name. The store holds the rows
// (model.UnmatchedFailure); this is the wire shape, the window parsing
// it shares with /api/metrics, and the one summary worth computing here
// -- how many of the window's failures look like a provider refusing a
// run.
//
// It is served rather than left to the database because reading it must
// not require reaching the machine. An instrument an operator can only
// consult over SSH, on the host, with a sqlite client, is one that is
// consulted after the damage rather than before it -- and the damage
// here is tasks retiring quietly over a fortnight while a marker grain
// matches on has stopped existing.

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-on-gke/grain/pkg/kubectlgate"
	"github.com/ai-on-gke/grain/pkg/model"
)

// UnmatchedFailure is one failed run whose text matched none of grain's
// markers for a provider refusal, as the API renders it.
//
// This package's own type rather than model's with tags on it, the same
// choice MetricsReport makes: the field an operator acts on is Suspect,
// and an API is worth stating in words that say what they mean.
type UnmatchedFailure struct {
	RunID     string    `json:"runId"`
	TaskID    string    `json:"taskId"`
	Framework string    `json:"framework"`
	At        time.Time `json:"at"`
	// Text is the tail of what the run said, bounded when it was
	// recorded (agent.MaxUnmatchedFailureText) -- a kilobyte at most,
	// because it is model output and model output has no length of its
	// own.
	Text string `json:"text"`
	// Suspect is grain's loose read of that text: it looks like a
	// provider refusing the run rather than the run failing. A hint for
	// where to start reading, not a claim -- see agent.ResemblesRefusal.
	Suspect bool `json:"suspect"`
}

// UnmatchedFailureReport is GET /api/unmatched-failures' whole body: the
// window asked for, what was found in it, and the two counts that make
// the list worth scanning.
//
// Suspected is the number worth a human's eye, and Truncated says
// outright that the list is not the whole window -- a report that
// silently stopped at its own limit would answer "has this got worse?"
// with a flat line at the limit.
type UnmatchedFailureReport struct {
	Since         time.Time          `json:"since"`
	Until         time.Time          `json:"until"`
	WindowSeconds float64            `json:"windowSeconds"`
	Total         int                `json:"total"`
	Suspected     int                `json:"suspected"`
	Truncated     bool               `json:"truncated"`
	Failures      []UnmatchedFailure `json:"failures"`
	// Infrastructure is the window's failures that were the cluster's
	// rather than any agent's -- not in Failures or Total, since grain
	// did classify them, but read beside them because a failure count
	// that climbs while the cluster is failing is not a marker that
	// stopped matching.
	Infrastructure InfrastructureFailures `json:"infrastructure"`
}

// InfrastructureFailures counts what the cluster's exec tunnel -- the
// API server's path to a node's kubelet -- failed over the window: the
// same failures the cluster.exec-tunnel alert counts over minutes
// (orchestrator.noteExecTunnelRate), over the report's window instead.
type InfrastructureFailures struct {
	// ExecTunnelSetups is runs that never reached their agent because
	// their sandbox's setup failed on the exec transport, whether grain
	// retried them or they ended setup-failed
	// (kubectlgate.IsTransientTransport on the run's detail).
	ExecTunnelSetups int `json:"execTunnelSetups"`
	// ExecTunnelToolRuns is runs that reached their agent and then had
	// sandbox tool calls fail on the same transport past the exec
	// runner's own retries (model.ExecTransportFailure), and
	// ExecTunnelToolCalls how many calls that was in all.
	ExecTunnelToolRuns  int `json:"execTunnelToolRuns"`
	ExecTunnelToolCalls int `json:"execTunnelToolCalls"`
}

// maxUnmatchedFailureLimit bounds ?limit=, so one request cannot ask for
// every failure a deployment has ever had in one body.
const maxUnmatchedFailureLimit = 2000

// UnmatchedFailures is what runs said when they failed for a reason
// grain has no marker for, over the last window, newest first.
//
// The question it answers is not "why did this task fail" -- the task's
// own attempts say that -- but "is grain still able to recognise a
// provider refusing us?". A week of these with the same unfamiliar
// sentence in them is a marker that has been reworded
// (pkg/agent/refusal.go), and reading them is the only way that becomes
// visible before the tasks it retires do.
func (c *Client) UnmatchedFailures(ctx context.Context, window time.Duration, limit int) (UnmatchedFailureReport, error) {
	if window <= 0 {
		window = DefaultMetricsWindow
	}
	if limit <= 0 {
		limit = model.DefaultUnmatchedFailureLimit
	}
	until := c.now()
	since := until.Add(-window)
	// One more than asked for, so that "there are more of these than fit"
	// is a fact this read establishes rather than a guess from the list
	// being exactly full.
	rows, err := c.Store.UnmatchedFailures(ctx, since, limit+1)
	if err != nil {
		return UnmatchedFailureReport{}, err
	}
	report := UnmatchedFailureReport{
		Since: since.UTC(), Until: until.UTC(),
		WindowSeconds: window.Seconds(),
		Failures:      []UnmatchedFailure{},
	}
	if len(rows) > limit {
		rows, report.Truncated = rows[:limit], true
	}
	for _, r := range rows {
		if r.Suspect {
			report.Suspected++
		}
		report.Failures = append(report.Failures, UnmatchedFailure{
			RunID: r.RunID, TaskID: r.TaskID, Framework: r.Framework,
			At: r.At.UTC(), Text: r.Text, Suspect: r.Suspect,
		})
	}
	report.Total = len(report.Failures)
	if report.Infrastructure, err = c.infrastructureFailures(ctx, since); err != nil {
		return UnmatchedFailureReport{}, err
	}
	return report, nil
}

// infrastructureFailures counts the exec-tunnel failures recorded since
// since, of every task.
func (c *Client) infrastructureFailures(ctx context.Context, since time.Time) (InfrastructureFailures, error) {
	var out InfrastructureFailures
	details, err := c.Store.SetupFailureDetailsSince(ctx, since)
	if err != nil {
		return out, err
	}
	for _, d := range details {
		if kubectlgate.IsTransientTransport(d) {
			out.ExecTunnelSetups++
		}
	}
	midRun, err := c.Store.ExecTransportFailuresSince(ctx, since)
	if err != nil {
		return out, err
	}
	out.ExecTunnelToolRuns = len(midRun)
	for _, f := range midRun {
		out.ExecTunnelToolCalls += f.Calls
	}
	return out, nil
}

// handleListUnmatchedFailures answers GET /api/unmatched-failures. The
// window is spelled the way /api/metrics spells it ("7d", "36h"), since
// an operator asking both questions in one sitting should not have to
// remember two vocabularies.
func (s *Server) handleListUnmatchedFailures(w http.ResponseWriter, r *http.Request) {
	window, err := ParseMetricsWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > maxUnmatchedFailureLimit {
			writeError(w, http.StatusBadRequest, validationErrorf(
				"limit must be a positive integer no greater than %d", maxUnmatchedFailureLimit))
			return
		}
		limit = parsed
	}
	report, err := s.tasks.UnmatchedFailures(r.Context(), window, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
