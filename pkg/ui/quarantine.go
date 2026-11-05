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
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"gopkg.in/yaml.v3"
)

// QuarantineTestRequest is POST /api/tests/quarantine's body: record a flaky
// or failing test quarantine with its expiration TTL and optionally file a
// held follow-up repair task.
type QuarantineTestRequest struct {
	Repo         string `json:"repo,omitempty"`
	Branch       string `json:"branch,omitempty"`
	TestName     string `json:"testName"`
	Reason       string `json:"reason"`
	TTL          string `json:"ttl,omitempty"`
	FileFollowUp *bool  `json:"fileFollowUp,omitempty"`
}

// QuarantineTestResponse is POST /api/tests/quarantine's reply, including the
// filed follow-up task ID (when requested) and the rendered .grain/quarantine.yml
// entry for the caller to commit in its workspace.
type QuarantineTestResponse struct {
	TaskID         string     `json:"taskId,omitempty"`
	FollowUpTaskID string     `json:"followUpTaskId,omitempty"`
	TestName       string     `json:"testName"`
	Reason         string     `json:"reason"`
	TTL            string     `json:"ttl"`
	CreatedAt      time.Time  `json:"createdAt"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
	QuarantineYAML string     `json:"quarantineYaml"`
}

func (s *Server) handleQuarantineTest(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil || s.tasks.Store == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("task store not configured"))
		return
	}
	var req QuarantineTestRequest
	if !readJSON(w, r, &req) {
		return
	}
	testName := strings.TrimSpace(req.TestName)
	if testName == "" {
		writeError(w, http.StatusBadRequest, errors.New("testName is required"))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		writeError(w, http.StatusBadRequest, errors.New("reason is required"))
		return
	}
	ttl := strings.TrimSpace(req.TTL)
	if ttl == "" {
		ttl = "30d"
	}
	dur, err := parseQuarantineDuration(ttl)
	if err != nil || dur <= 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid ttl %q: expected positive duration such as 30d or 72h", ttl))
		return
	}

	ctx := r.Context()
	var callerTaskID, callerRunID string
	if ac, ok := agentCallFromContext(ctx); ok {
		callerTaskID = ac.taskID
		callerRunID = ac.runID
	}

	repo := strings.TrimSpace(req.Repo)
	base := strings.TrimSpace(req.Branch)
	if repo != "" {
		parsed, err := s.tasks.parseRepo(repo)
		if err != nil {
			writeClientError(w, err)
			return
		}
		repo = parsed.String()
	}
	if callerTaskID != "" {
		if parent, err := s.tasks.Store.GetTask(ctx, callerTaskID); err == nil && parent != nil {
			if repo == "" && parent.Target != nil {
				repo = parent.Target.String()
			}
			if base == "" && repo != "" {
				for _, wr := range parent.Writes {
					if strings.EqualFold(wr.Repo.String(), repo) {
						base = wr.Base
						break
					}
				}
			}
		}
	}

	fileFollowUp := true
	if req.FileFollowUp != nil {
		fileFollowUp = *req.FileFollowUp
	}

	var followUpID string
	if fileFollowUp {
		localID := "quarantine-fix-" + strings.ToLower(strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(testName))
		title := fmt.Sprintf("fix(test): unquarantine and fix flaky test %s", testName)
		body := fmt.Sprintf("Test `%s` was quarantined.\n\nReason: %s\n\nPlease investigate the root cause, fix the flake, and remove the entry from .grain/quarantine.yml.", testName, reason)
		var proposalKey string
		if callerTaskID != "" {
			body = fmt.Sprintf("Test `%s` was quarantined by [Task #%s](/tasks/%s).\n\nReason: %s\n\nPlease investigate the root cause, fix the flake, and remove the entry from .grain/quarantine.yml.",
				testName, callerTaskID, callerTaskID, reason)
			proposalKey = sandboxProposalKey(callerTaskID, callerRunID, localID, title)
		}
		created, err := s.tasks.CreateTask(ctx, CreateTaskRequest{
			Title:           title,
			Description:     body,
			Repo:            repo,
			NoRepo:          repo == "",
			Base:            base,
			AutoMerge:       false,
			HoldForApproval: true,
			ProposedBy:      callerTaskID,
			ProposalKey:     proposalKey,
		})
		if err != nil {
			writeClientError(w, err)
			return
		}
		followUpID = created.ID
	}

	now := time.Now().UTC()
	exp := now.Add(dur)
	entry := ci.QuarantineEntry{
		TestName:       testName,
		Reason:         reason,
		TaskID:         callerTaskID,
		FollowUpTaskID: followUpID,
		CreatedAt:      &now,
		ExpiresAt:      &exp,
		TTL:            ttl,
	}
	yamlBytes, err := yaml.Marshal(ci.QuarantineConfig{Tests: []ci.QuarantineEntry{entry}})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, QuarantineTestResponse{
		TaskID:         callerTaskID,
		FollowUpTaskID: followUpID,
		TestName:       testName,
		Reason:         reason,
		TTL:            ttl,
		CreatedAt:      now,
		ExpiresAt:      &exp,
		QuarantineYAML: string(yamlBytes),
	})
}

func parseQuarantineDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid day duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
