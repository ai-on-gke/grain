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
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/qa/resolve"
)

// QAFlowsResponse is returned by GET /api/qa/flows.
type QAFlowsResponse struct {
	Flows []qa.FlowSpec `json:"flows"`
}

// TaskQAResponse is returned by GET /api/tasks/{id}/qa.
type TaskQAResponse struct {
	TaskID       string              `json:"taskId"`
	Enabled      bool                `json:"enabled"`
	ExperimentID string              `json:"experimentId,omitempty"`
	ArmID        string              `json:"armId,omitempty"`
	Assignment   *model.QAAssignment `json:"assignment,omitempty"`
	CheckRuns    []model.QACheckRun  `json:"checkRuns"`
	Findings     []model.QAFinding   `json:"findings"`
}

// RecordQACheckRunEntry carries one in-sandbox S3 check run and its findings from `grain mcpserver`.
type RecordQACheckRunEntry struct {
	Run      model.QACheckRun  `json:"run"`
	Findings []model.QAFinding `json:"findings,omitempty"`
}

// RecordQACheckRunsRequest is posted to POST /api/tasks/{id}/qa/check-runs (internal plumbing).
type RecordQACheckRunsRequest struct {
	Entries []RecordQACheckRunEntry `json:"entries"`
}

// VoteQAFindingRequest is posted to POST /api/tasks/{id}/qa/findings/{finding}/feedback.
type VoteQAFindingRequest struct {
	Vote int `json:"vote"`
}

// VoteQAFindingResponse is returned by POST /api/tasks/{id}/qa/findings/{finding}/feedback.
type VoteQAFindingResponse struct {
	FindingID int64  `json:"findingId"`
	TaskID    string `json:"taskId"`
	Vote      int    `json:"vote"`
	VotedBy   string `json:"votedBy,omitempty"`
}

func (s *Server) handleGetQAFlows(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, QAFlowsResponse{Flows: qa.BuiltInFlows()})
}

// QASummaryRequest specifies the span and repository scope for GET /api/qa/summary.
type QASummaryRequest struct {
	Window time.Duration
	Since  time.Time
	Until  time.Time
	Repos  []string
}

func (r QASummaryRequest) span(now time.Time) metrics.Window {
	if !r.Since.IsZero() {
		until := r.Until
		if until.IsZero() {
			until = now
		}
		return metrics.Window{Since: r.Since, Until: until}
	}
	window := r.Window
	if window <= 0 {
		window = DefaultMetricsWindow
	}
	until := r.Until
	if until.IsZero() {
		until = now
	}
	return metrics.Window{Since: until.Add(-window), Until: until}
}

// QASummary computes the single-instance A/B quality control rollout summary over req.
func (c *Client) QASummary(ctx context.Context, req QASummaryRequest) (metrics.QASummary, error) {
	if c == nil || c.Store == nil {
		return metrics.QASummary{}, errors.New("store is not available")
	}
	win := req.span(c.now())
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return metrics.QASummary{}, fmt.Errorf("reading deployment settings: %w", err)
	}
	var qaCfg model.QAConfig
	if cfg != nil {
		qaCfg = cfg.QA
	}
	assignments, err := c.Store.ListQAAssignments(ctx)
	if err != nil {
		return metrics.QASummary{}, fmt.Errorf("reading qa assignments: %w", err)
	}
	checkRuns, err := c.Store.ListAllQACheckRuns(ctx)
	if err != nil {
		return metrics.QASummary{}, fmt.Errorf("reading qa check runs: %w", err)
	}
	findings, err := c.Store.ListAllQAFindings(ctx)
	if err != nil {
		return metrics.QASummary{}, fmt.Errorf("reading qa findings: %w", err)
	}
	tasks, err := c.Store.ListTasks(ctx)
	if err != nil {
		return metrics.QASummary{}, fmt.Errorf("reading tasks: %w", err)
	}
	timings, err := c.Store.TaskTimings(ctx)
	if err != nil {
		return metrics.QASummary{}, fmt.Errorf("reading task timings: %w", err)
	}
	runs, err := c.Store.RunTimings(ctx)
	if err != nil {
		return metrics.QASummary{}, fmt.Errorf("reading run timings: %w", err)
	}
	prompts, err := c.Store.RunPrompts(ctx)
	if err != nil {
		return metrics.QASummary{}, fmt.Errorf("reading run prompts: %w", err)
	}
	usages, err := c.Store.RunUsagesSince(ctx, time.Time{})
	if err != nil {
		return metrics.QASummary{}, fmt.Errorf("reading run usages: %w", err)
	}
	ciRuns, err := c.Store.CIRunOutcomes(ctx)
	if err != nil {
		return metrics.QASummary{}, fmt.Errorf("reading ci run outcomes: %w", err)
	}
	return metrics.ComputeQASummary(metrics.QASummaryInput{
		Window:      win,
		Scope:       metrics.Scope{Repos: req.Repos},
		Config:      qaCfg,
		Assignments: assignments,
		CheckRuns:   checkRuns,
		Findings:    findings,
		Tasks:       tasks,
		Timings:     timings,
		Runs:        runs,
		Prompts:     prompts,
		Usages:      usages,
		CIRuns:      ciRuns,
	}), nil
}

func (s *Server) handleGetQASummary(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil || s.tasks.Store == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("store is not available"))
		return
	}
	query := r.URL.Query()
	window, err := ParseMetricsWindow(query.Get("window"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	since, err := ParseMetricsMoment(query.Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	until, err := ParseMetricsMoment(query.Get("until"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var repos []string
	for _, raw := range query["repo"] {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			canonical, cErr := s.agentCanonicalRepo(part)
			if cErr != nil {
				writeError(w, http.StatusBadRequest, cErr)
				return
			}
			repos = append(repos, canonical)
		}
	}
	summary, err := s.tasks.QASummary(r.Context(), QASummaryRequest{
		Window: window,
		Since:  since,
		Until:  until,
		Repos:  repos,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleGetTaskQA(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil || s.tasks.Store == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("store is not available"))
		return
	}
	ctx := r.Context()
	taskID := strings.TrimSpace(r.PathValue("id"))
	task, err := s.tasks.Store.GetTask(ctx, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, &NotFoundError{ID: taskID})
		return
	}

	res, qaCfg, err := resolve.ActiveQAResolutionForTask(ctx, s.tasks.Store, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var expID, armID string
	if res != nil {
		expID = res.ExperimentID
		armID = res.ArmID
	} else if resolve.RegistryResolver != nil {
		if regRes, rErr := resolve.RegistryResolver(ctx, s.tasks.Store, *task, model.ResolveQARootOrigin(task.WorkOrigin, task.ID), qaCfg); rErr == nil && regRes != nil {
			expID = regRes.ExperimentID
			armID = regRes.ArmID
		}
	}
	var assign *model.QAAssignment
	if res != nil || qaCfg.EpochOrDefault() >= 1 {
		assign, _ = s.tasks.Store.QAAssignment(ctx, model.ResolveQARootOrigin(task.WorkOrigin, task.ID), qaCfg.EpochOrDefault())
	}

	runs, err := s.tasks.Store.ListQACheckRunsForTask(ctx, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if runs == nil {
		runs = []model.QACheckRun{}
	}
	findings, err := s.tasks.Store.ListQAFindingsForTask(ctx, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if findings == nil {
		findings = []model.QAFinding{}
	}

	enabled := qaCfg.Enabled && model.TaskEligibleForQAStaging(*task)
	if enabled && assign == nil && len(runs) == 0 && len(findings) == 0 {
		if obs, oErr := s.tasks.Store.GetObservation(ctx, taskID); oErr == nil && obs != nil && obs.ClosedAt != nil {
			enabled = false
		}
	}

	writeJSON(w, http.StatusOK, TaskQAResponse{
		TaskID:       taskID,
		Enabled:      enabled,
		ExperimentID: expID,
		ArmID:        armID,
		Assignment:   assign,
		CheckRuns:    runs,
		Findings:     findings,
	})
}

const (
	maxRecordedCheckRunEntries = 32
	maxRecordedFindingsPerRun  = 100
)

func (s *Server) handleRecordTaskQACheckRuns(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil || s.tasks.Store == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("store is not available"))
		return
	}
	ctx := r.Context()
	if RequestActor(ctx).Actor.Kind == model.PrincipalAgent {
		writeError(w, http.StatusForbidden, errors.New("agent tokens cannot write quality check runs directly"))
		return
	}
	taskID := strings.TrimSpace(r.PathValue("id"))
	task, err := s.tasks.Store.GetTask(ctx, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, &NotFoundError{ID: taskID})
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req RecordQACheckRunsRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decoding qa check runs: %w", err))
		return
	}
	if len(req.Entries) > maxRecordedCheckRunEntries {
		writeError(w, http.StatusBadRequest, fmt.Errorf("at most %d check run entries may be recorded per request", maxRecordedCheckRunEntries))
		return
	}

	var ids []int64
	for _, entry := range req.Entries {
		if len(entry.Findings) > maxRecordedFindingsPerRun {
			writeError(w, http.StatusBadRequest, fmt.Errorf("at most %d findings may be recorded per check run", maxRecordedFindingsPerRun))
			return
		}
		switch entry.Run.Stage {
		case "S1", "S3", "S4":
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported check run stage %q", entry.Run.Stage))
			return
		}
		entry.Run.TaskID = taskID
		id, err := s.tasks.Store.RecordQACheckRun(ctx, entry.Run, entry.Findings)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ids = append(ids, id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": len(ids), "ids": ids})
}

func (s *Server) handleVoteTaskQAFinding(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil || s.tasks.Store == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("store is not available"))
		return
	}
	ctx := r.Context()
	taskID := strings.TrimSpace(r.PathValue("id"))
	findingID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("finding")), 10, 64)
	if err != nil || findingID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("finding ID must be a positive integer"))
		return
	}

	task, err := s.tasks.Store.GetTask(ctx, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, &NotFoundError{ID: taskID})
		return
	}

	findings, err := s.tasks.Store.ListQAFindingsForTask(ctx, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	belongsToTask := false
	for _, f := range findings {
		if f.ID == findingID {
			belongsToTask = true
			break
		}
	}
	if !belongsToTask {
		writeError(w, http.StatusNotFound, fmt.Errorf("finding %d does not belong to task %s", findingID, taskID))
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req VoteQAFindingRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decoding feedback vote: %w", err))
		return
	}
	if req.Vote < -1 || req.Vote > 1 {
		writeError(w, http.StatusBadRequest, errors.New("vote must be -1 (not helpful), 0 (clear), or 1 (helpful)"))
		return
	}

	attr := RequestActor(ctx)
	if attr.Actor.Kind == model.PrincipalAgent {
		writeError(w, http.StatusForbidden, errors.New("agents cannot vote on human quality findings"))
		return
	}
	voter := strings.TrimSpace(attr.Actor.ID)
	if attr.OnBehalfOf != nil && strings.TrimSpace(attr.OnBehalfOf.ID) != "" {
		voter = strings.TrimSpace(attr.OnBehalfOf.ID)
	}
	if voter == "" {
		voter = "operator"
	}
	if err := s.tasks.Store.VoteQAFinding(ctx, findingID, req.Vote, voter, time.Now().UTC()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, fmt.Errorf("finding %d not found", findingID))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if req.Vote == 0 {
		voter = ""
	}
	writeJSON(w, http.StatusOK, VoteQAFindingResponse{
		FindingID: findingID,
		TaskID:    taskID,
		Vote:      req.Vote,
		VotedBy:   voter,
	})
}
