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
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Clearing many notifications at once: POST /api/notifications/clear,
// the Inbox's "Clear all" and "Clear these", and `grain notifications
// clear`. Each notification it matches is cleared exactly as POST
// /api/notifications/{id}/clear clears it -- the same store call, the
// same notification.cleared event -- so a level-triggered one
// (model.LevelTriggeredFamily) comes back on the reconciler's next cycle
// while its condition holds, just as it would after a one-at-a-time
// clear.

// maxBulkClear is at most how many notifications one call clears.
const maxBulkClear = 5000

// ClearNotificationsRequest is POST /api/notifications/clear's body. Every
// filter given narrows the open notifications; none matches every open
// one the Inbox shows as an alert (a pending release is left alone unless
// Subject or IDs name it: it is an approval, not an alert).
type ClearNotificationsRequest struct {
	// Subject keeps the notifications whose subject starts with it.
	Subject string `json:"subject,omitempty"`
	// Severity keeps one severity: info, warning or critical.
	Severity string `json:"severity,omitempty"`
	// OlderThan keeps those last raised longer ago than it: a Go
	// duration ("90m", "12h") or a number of days ("7d").
	OlderThan string `json:"olderThan,omitempty"`
	// IDs keeps the notifications with these ids.
	IDs []int64 `json:"ids,omitempty"`
	// Reason is why, recorded in the audit entry. Required unless DryRun.
	Reason string `json:"reason,omitempty"`
	// DryRun counts what would be cleared and clears nothing: the
	// confirm dialog's count.
	DryRun bool `json:"dryRun,omitempty"`
}

// ClearNotificationsResponse is what a bulk clear answers.
type ClearNotificationsResponse struct {
	// Matched is how many open notifications the filter matched.
	Matched int `json:"matched"`
	// Cleared is how many this call cleared: Matched, less any another
	// caller cleared first. Zero on a dry run.
	Cleared int `json:"cleared"`
	// IDs are the notifications cleared (on a dry run, matched).
	IDs []int64 `json:"ids"`
	// ExpectedBack is how many of them are level-triggered and will be
	// raised again on the next cycle if their condition still holds.
	ExpectedBack int `json:"expectedBack"`
	// ExpectedBackFamilies are those notifications' families, sorted.
	ExpectedBackFamilies []string `json:"expectedBackFamilies,omitempty"`
	DryRun               bool     `json:"dryRun,omitempty"`
}

// parseOlderThan reads ClearNotificationsRequest.OlderThan.
func parseOlderThan(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	} else if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, nil
	}
	return 0, validationErrorf("olderThan %q: want a positive duration such as 90m, 12h or 7d", s)
}

// ClearNotifications clears every open notification req matches, as
// ctx's actor, and records one config-audit entry for the call.
//
// A run's agent principal may clear only the notifications its own task
// raised (its agent.<task>: namespace) -- even with api:task-admin, which
// lets it clear another's one at a time: a bulk clear is a person's tool
// for a noisy Inbox, as pipeline-control lifts only its own pauses. An id
// naming another's notification is refused rather than skipped.
func (c *Client) ClearNotifications(ctx context.Context, req ClearNotificationsRequest) (ClearNotificationsResponse, error) {
	actor := c.requestActor(ctx).Actor
	reason := strings.TrimSpace(req.Reason)
	if reason == "" && !req.DryRun {
		return ClearNotificationsResponse{}, validationErrorf("clearing notifications in bulk needs a reason: say why they no longer need anyone")
	}
	severity := model.NotificationSeverity("")
	if s := strings.TrimSpace(req.Severity); s != "" {
		parsed, err := model.ParseNotificationSeverity(s)
		if err != nil {
			return ClearNotificationsResponse{}, validationErrorf("%v", err)
		}
		severity = parsed
	}
	olderThan, err := parseOlderThan(req.OlderThan)
	if err != nil {
		return ClearNotificationsResponse{}, err
	}
	subject := strings.TrimSpace(req.Subject)
	ids := map[int64]bool{}
	for _, id := range req.IDs {
		if id <= 0 {
			return ClearNotificationsResponse{}, validationErrorf("notification id %d: ids are positive numbers", id)
		}
		ids[id] = true
	}
	agent := actor.Kind == model.PrincipalAgent
	own := ""
	if agent {
		own = AgentNamespace(actor.ID)
	}
	open, err := c.Store.ListNotifications(ctx, model.NotificationFilter{Open: true, Limit: maxBulkClear})
	if err != nil {
		return ClearNotificationsResponse{}, err
	}
	now := c.now()
	var matched []model.Notification
	for _, n := range open {
		if len(ids) > 0 && !ids[n.ID] {
			continue
		}
		if subject != "" && !strings.HasPrefix(n.Subject, subject) {
			continue
		}
		if subject == "" && len(ids) == 0 && n.Family == pendingReleaseFamily {
			continue
		}
		if severity != "" && n.Severity != severity {
			continue
		}
		if olderThan > 0 {
			at := n.LastRaisedAt
			if at.IsZero() {
				at = n.RaisedAt
			}
			if now.Sub(at) <= olderThan {
				continue
			}
		}
		if agent && !strings.HasPrefix(n.Subject, own) {
			if len(ids) > 0 {
				return ClearNotificationsResponse{}, forbiddenErrorf("task %s may only clear its own notifications (%s...) in bulk, "+
					"and notification %d is %s", actor.ID, own, n.ID, n.Subject)
			}
			continue
		}
		matched = append(matched, n)
	}
	resp := ClearNotificationsResponse{Matched: len(matched), IDs: []int64{}, DryRun: req.DryRun}
	families := map[string]bool{}
	by := model.PrincipalString(actor)
	for _, n := range matched {
		if !req.DryRun {
			cleared, err := c.Store.ClearNotificationByID(ctx, n.ID, by)
			if err != nil {
				return resp, err
			}
			if !cleared {
				continue
			}
			resp.Cleared++
		}
		resp.IDs = append(resp.IDs, n.ID)
		family := n.Family
		if family == "" {
			family = model.NotificationFamily(n.Subject)
		}
		if model.LevelTriggeredFamily(family) {
			resp.ExpectedBack++
			families[family] = true
		}
	}
	for f := range families {
		resp.ExpectedBackFamilies = append(resp.ExpectedBackFamilies, f)
	}
	slices.Sort(resp.ExpectedBackFamilies)
	if !req.DryRun {
		c.auditNotificationClear(ctx, req, resp, reason)
	}
	return resp, nil
}

// auditNotificationClear records one bulk clear in config_audit: what
// filter, how many, and why.
func (c *Client) auditNotificationClear(ctx context.Context, req ClearNotificationsRequest, resp ClearNotificationsResponse, reason string) {
	var filter []string
	if s := strings.TrimSpace(req.Subject); s != "" {
		filter = append(filter, "subject "+s+"*")
	}
	if s := strings.TrimSpace(req.Severity); s != "" {
		filter = append(filter, "severity "+s)
	}
	if s := strings.TrimSpace(req.OlderThan); s != "" {
		filter = append(filter, "older than "+s)
	}
	if len(req.IDs) > 0 {
		filter = append(filter, fmt.Sprintf("%d named ids", len(req.IDs)))
	}
	what := "every open notification"
	if len(filter) > 0 {
		what = "open notifications matching " + strings.Join(filter, ", ")
	}
	summary := fmt.Sprintf("cleared %d of %s (%d expected back while their condition holds): %s",
		resp.Cleared, what, resp.ExpectedBack, reason)
	by := c.requestActor(ctx)
	if _, err := c.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At: c.now(), By: by.Actor, OnBehalfOf: by.OnBehalfOf, Surface: "notifications", Summary: summary,
	}); err != nil {
		log.Printf("grain notifications: recording %q in the config audit: %v", summary, err)
	}
}

// handleClearNotifications answers POST /api/notifications/clear.
func (s *Server) handleClearNotifications(w http.ResponseWriter, r *http.Request) {
	var req ClearNotificationsRequest
	if !readJSON(w, r, &req) {
		return
	}
	resp, err := s.tasks.ClearNotifications(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ClearNotifications is POST /api/notifications/clear, for `grain
// notifications clear`.
func (c *HTTPClient) ClearNotifications(ctx context.Context, req ClearNotificationsRequest) (ClearNotificationsResponse, error) {
	var resp ClearNotificationsResponse
	err := c.do(ctx, http.MethodPost, "/api/notifications/clear", req, &resp)
	return resp, err
}
