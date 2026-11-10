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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	// DefaultEventsLimit is the default page size for JSON responses on
	// GET /api/events when limit is omitted.
	DefaultEventsLimit = 100

	// MaxEventsLimit bounds how many events one JSON response on
	// GET /api/events returns.
	MaxEventsLimit = 500

	// MaxEventsWaitHold bounds how long GET /api/events?wait= holds an idle
	// long-poll request, staying under typical 30 s reverse-proxy timeouts
	// (matching restartWatchHold).
	MaxEventsWaitHold = 25 * time.Second

	// defaultEventsKeepalive is how often an idle SSE stream on
	// GET /api/events emits a ": keepalive" comment frame.
	defaultEventsKeepalive = 15 * time.Second
)

// IntegrationEvent is one ordered task lifecycle, question, comment, or CI
// verdict event on GET /api/events (design/design/integration-events.design.md).
// It carries identifiers, one-line summaries, external references, and actor
// attribution only -- never secret values, raw comment bodies, prompts,
// transcripts, or CI failure logs.
type IntegrationEvent struct {
	ID              string                     `json:"id"`
	Type            model.IntegrationEventType `json:"type"`
	At              time.Time                  `json:"at"`
	TaskID          string                     `json:"taskId"`
	Repo            string                     `json:"repo,omitempty"`
	PreviousState   model.State                `json:"previousState,omitempty"`
	State           model.State                `json:"state,omitempty"`
	QuestionSummary string                     `json:"questionSummary,omitempty"`
	CommentID       int64                      `json:"commentId,omitempty"`
	ExternalRef     string                     `json:"externalRef,omitempty"`
	Origin          string                     `json:"origin,omitempty"`
	PullRequest     string                     `json:"pullRequest,omitempty"`
	FiledVia        string                     `json:"filedVia,omitempty"`
	CIProvider      string                     `json:"ciProvider,omitempty"`
	CIRunID         string                     `json:"ciRunId,omitempty"`
	CICommit        string                     `json:"ciCommit,omitempty"`
	CIConclusion    string                     `json:"ciConclusion,omitempty"`
	CIDetail        string                     `json:"ciDetail,omitempty"`
	Actor           string                     `json:"actor,omitempty"`
	ActorKind       model.PrincipalKind        `json:"actorKind,omitempty"`
	ActorSource     model.AuthSource           `json:"actorSource,omitempty"`
	ActorLabel      string                     `json:"actorLabel,omitempty"`
	OnBehalfOf      string                     `json:"onBehalfOf,omitempty"`
	OnBehalfOfKind  model.PrincipalKind        `json:"onBehalfOfKind,omitempty"`
}

// EventsResponse is GET /api/events's JSON envelope for immediate and
// long-poll (?wait=) requests.
type EventsResponse struct {
	Events     []IntegrationEvent `json:"events"`
	Cursor     string             `json:"cursor"`
	NextCursor string             `json:"nextCursor,omitempty"`
	HasMore    bool               `json:"hasMore,omitempty"`
	Truncated  bool               `json:"truncated,omitempty"`
	TimedOut   bool               `json:"timedOut,omitempty"`
}

func newEventsResponse(events []IntegrationEvent, cursor int64, hasMore bool) EventsResponse {
	cur := strconv.FormatInt(cursor, 10)
	resp := EventsResponse{
		Events:  events,
		Cursor:  cur,
		HasMore: hasMore,
	}
	if hasMore {
		resp.Truncated = true
		resp.NextCursor = cur
	}
	return resp
}

type eventsQuery struct {
	since         int64
	sinceNow      bool
	explicitSince bool
	wait          time.Duration
	limit         int
	repo          string
	types         []model.IntegrationEventType
	stream        bool
}

func toWireIntegrationEvent(e model.IntegrationEvent) IntegrationEvent {
	out := IntegrationEvent{
		ID:              strconv.FormatInt(e.ID, 10),
		Type:            e.Type,
		At:              e.At.UTC(),
		TaskID:          e.TaskID,
		Repo:            e.Repo,
		PreviousState:   e.PreviousState,
		State:           e.State,
		QuestionSummary: e.QuestionSummary,
		CommentID:       e.CommentID,
		ExternalRef:     e.ExternalRef,
		Origin:          e.Origin,
		PullRequest:     e.PullRequest,
		FiledVia:        e.FiledVia,
		CIProvider:      e.CIProvider,
		CIRunID:         e.CIRunID,
		CICommit:        e.CICommit,
		CIConclusion:    e.CIConclusion,
		CIDetail:        e.CIDetail,
		Actor:           e.Actor.Actor.ID,
		ActorKind:       e.Actor.Actor.Kind,
		ActorSource:     e.Actor.Actor.Source,
		ActorLabel:      PrincipalLabel(e.Actor.Actor, e.Actor.OnBehalfOf),
	}
	if b := e.Actor.OnBehalfOf; b != nil {
		out.OnBehalfOf = b.ID
		out.OnBehalfOfKind = b.Kind
	}
	return out
}

func acceptsEventStream(header string) bool {
	for _, part := range strings.Split(header, ",") {
		mediaType, _, _ := strings.Cut(part, ";")
		if strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream") {
			return true
		}
	}
	return false
}

func (s *Server) parseEventsQuery(r *http.Request) (eventsQuery, error) {
	q := r.URL.Query()
	defaultLimit := DefaultEventsLimit
	if isAgentListCaller(r) {
		defaultLimit = listPageLarge
	}
	out := eventsQuery{
		limit:  defaultLimit,
		stream: acceptsEventStream(r.Header.Get("Accept")),
	}

	if rawStream := strings.TrimSpace(q.Get("stream")); rawStream != "" {
		switch strings.ToLower(rawStream) {
		case "1", "true":
			out.stream = true
		case "0", "false":
			out.stream = false
		default:
			return out, validationErrorf("stream must be 0, 1, true, or false")
		}
	}

	if rawWait := strings.TrimSpace(q.Get("wait")); rawWait != "" {
		n, err := strconv.Atoi(rawWait)
		if err != nil || n < 0 {
			return out, validationErrorf("wait must be a non-negative whole number of seconds")
		}
		out.wait = time.Duration(min(n, int(MaxEventsWaitHold/time.Second))) * time.Second
	}

	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n < 1 {
			return out, validationErrorf("limit must be a positive integer")
		}
		out.limit = min(n, MaxEventsLimit)
	}

	rawSince := strings.TrimSpace(q.Get("since"))
	if rawSince == "" && out.stream {
		rawSince = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	switch {
	case rawSince == "":
		if out.stream || out.wait > 0 {
			out.sinceNow = true
		}
	case strings.EqualFold(rawSince, "now"):
		out.sinceNow = true
	default:
		n, err := strconv.ParseInt(rawSince, 10, 64)
		if err != nil || n < 0 {
			return out, validationErrorf("since must be a non-negative integer cursor or \"now\"")
		}
		out.since = n
		out.explicitSince = true
	}

	if rawRepo := strings.TrimSpace(q.Get("repo")); rawRepo != "" {
		ref, err := s.tasks.parseRepo(rawRepo)
		if err != nil {
			return out, err
		}
		out.repo = ref.String()
	}

	for _, rawType := range splitList(q["types"]) {
		et := model.IntegrationEventType(rawType)
		if !et.Valid() {
			return out, validationErrorf("event type %q is not one of task.state, task.question, task.comment, task.ci", rawType)
		}
		out.types = append(out.types, et)
	}

	return out, nil
}

// eventsReadable returns a predicate reporting whether the caller on r may
// read tasks in repo ("" for a no-repository task), plus whether the caller
// has unrestricted tasks:read across the whole deployment.
func (s *Server) eventsReadable(r *http.Request) (readable func(repo string) bool, unrestricted bool) {
	ctx := r.Context()
	call, isAgent := agentCallFromContext(ctx)
	role := accessRoleFromContext(ctx)
	if role != nil && role.Full {
		role = nil
	}
	if !isAgent && role == nil {
		if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
			return func(string) bool { return false }, false
		}
		return func(string) bool { return true }, true
	}
	reach := fsReach{call: call, isAgent: isAgent, role: role}
	if !reach.has("tasks:read") {
		return func(string) bool { return false }, false
	}
	if reach.allowsAll("tasks:read") {
		return func(string) bool { return true }, true
	}
	allowed := reach.repos("tasks:read")
	return func(repo string) bool {
		if repo == "" {
			return false
		}
		return slices.ContainsFunc(allowed, func(a string) bool {
			return model.SameRepoText(a, repo)
		})
	}, false
}

// latestAuthorizedBaseline resolves the initial cursor and internal scan
// position when a caller asks to start at "now" (or opens an SSE / long-poll
// stream without an explicit cursor). Unrestricted callers observe the store's
// latest sequence id directly; repo-scoped callers receive the highest id
// among events they are authorized to read (or 0 when none exist), avoiding
// leaking cross-repository sequence advancement while still scanning only new
// store commits from the current head.
func latestAuthorizedBaseline(
	ctx context.Context,
	store *model.Store,
	q eventsQuery,
	readable func(string) bool,
	unrestricted bool,
) (clientCursor int64, scanAfter int64, err error) {
	head, err := store.LatestIntegrationEventID(ctx)
	if err != nil {
		return 0, 0, err
	}
	if unrestricted && q.repo == "" && len(q.types) == 0 {
		return head, head, nil
	}
	var lastReadable int64
	var pos int64
	const batch = 500
	for {
		rows, err := store.ListIntegrationEvents(ctx, model.IntegrationEventFilter{
			AfterID: pos,
			Repo:    q.repo,
			Types:   q.types,
			Limit:   batch,
		})
		if err != nil {
			return 0, 0, err
		}
		for _, row := range rows {
			pos = row.ID
			if readable(row.Repo) {
				lastReadable = row.ID
			}
		}
		if len(rows) < batch {
			break
		}
	}
	return lastReadable, max(head, pos), nil
}

// readAuthorizedEvents scans store events strictly after scanAfter, returning
// up to limit authorized events, the updated client cursor (unchanged from
// prevCursor when zero authorized events were found), the updated internal
// scan watermark, and whether at least one additional authorized event remains.
func readAuthorizedEvents(
	ctx context.Context,
	store *model.Store,
	scanAfter int64,
	prevCursor int64,
	limit int,
	repo string,
	types []model.IntegrationEventType,
	readable func(string) bool,
) (events []IntegrationEvent, nextCursor int64, nextScanAfter int64, hasMore bool, err error) {
	events = []IntegrationEvent{}
	nextCursor = prevCursor
	nextScanAfter = scanAfter
	batchSize := max(limit+1, 100)
	// queryAfter is where the next batch is read from. It always moves
	// past every row read, readable or not: nextScanAfter stops moving once
	// the page is full (so the next page starts right after its last
	// event), and reading the next batch from there re-read the same
	// unreadable rows forever when a full batch of them followed a full
	// page -- a repo-limited caller's request never returned.
	queryAfter := scanAfter

	for {
		rows, err := store.ListIntegrationEvents(ctx, model.IntegrationEventFilter{
			AfterID: queryAfter,
			Repo:    repo,
			Types:   types,
			Limit:   batchSize,
		})
		if err != nil {
			return nil, prevCursor, scanAfter, false, err
		}
		for _, row := range rows {
			queryAfter = row.ID
			if !readable(row.Repo) {
				if len(events) < limit {
					nextScanAfter = row.ID
				}
				continue
			}
			if len(events) == limit {
				return events, nextCursor, nextScanAfter, true, nil
			}
			events = append(events, toWireIntegrationEvent(row))
			nextCursor = row.ID
			nextScanAfter = row.ID
		}
		if len(rows) < batchSize {
			return events, nextCursor, nextScanAfter, false, nil
		}
	}
}

func (s *Server) eventsKeepaliveDuration() time.Duration {
	if s.eventsKeepalive > 0 {
		return s.eventsKeepalive
	}
	return defaultEventsKeepalive
}

// SetEventsKeepaliveForTest overrides GET /api/events's SSE keepalive interval
// in tests so idle heartbeat frames can be exercised without waiting 15 s.
func SetEventsKeepaliveForTest(s *Server, d time.Duration) {
	s.eventsKeepalive = d
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil || s.tasks.Store == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("task store is not configured"))
		return
	}
	q, err := s.parseEventsQuery(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	readable, unrestricted := s.eventsReadable(r)
	if q.repo != "" && !readable(q.repo) {
		writeError(w, http.StatusForbidden, fmt.Errorf("not authorized to read events for %s", q.repo))
		return
	}

	ctx := r.Context()
	store := s.tasks.Store

	var clientCursor, scanAfter int64
	if q.sinceNow {
		clientCursor, scanAfter, err = latestAuthorizedBaseline(ctx, store, q, readable, unrestricted)
		if err != nil {
			writeClientError(w, err)
			return
		}
	} else {
		clientCursor = q.since
		scanAfter = q.since
	}

	if q.stream {
		s.serveEventsSSE(w, r, q, readable, clientCursor, scanAfter)
		return
	}
	if q.wait > 0 {
		s.serveEventsLongPoll(w, r, q, readable, clientCursor, scanAfter)
		return
	}

	events, nextCursor, _, hasMore, err := readAuthorizedEvents(
		ctx, store, scanAfter, clientCursor, q.limit, q.repo, q.types, readable)
	if err != nil {
		writeClientError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, newEventsResponse(events, nextCursor, hasMore))
}

func (s *Server) serveEventsLongPoll(
	w http.ResponseWriter,
	r *http.Request,
	q eventsQuery,
	readable func(string) bool,
	clientCursor int64,
	scanAfter int64,
) {
	ctx := r.Context()
	store := s.tasks.Store
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(q.wait + 10*time.Second))

	timer := time.NewTimer(q.wait)
	defer timer.Stop()
	shutdown := s.tasks.Config.ShutdownStarted

	for {
		ch := store.Changed()
		events, nextCursor, nextScan, hasMore, err := readAuthorizedEvents(
			ctx, store, scanAfter, clientCursor, q.limit, q.repo, q.types, readable)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			writeClientError(w, err)
			return
		}
		clientCursor = nextCursor
		scanAfter = nextScan
		if len(events) > 0 {
			w.Header().Set("Cache-Control", "no-cache")
			writeJSON(w, http.StatusOK, newEventsResponse(events, clientCursor, hasMore))
			return
		}

		select {
		case <-ch:
		case <-timer.C:
			w.Header().Set("Cache-Control", "no-cache")
			writeJSON(w, http.StatusOK, EventsResponse{
				Events:   []IntegrationEvent{},
				Cursor:   strconv.FormatInt(clientCursor, 10),
				TimedOut: true,
			})
			return
		case <-shutdown:
			w.Header().Set("Cache-Control", "no-cache")
			writeJSON(w, http.StatusOK, EventsResponse{
				Events:   []IntegrationEvent{},
				Cursor:   strconv.FormatInt(clientCursor, 10),
				TimedOut: true,
			})
			return
		case <-ctx.Done():
			return
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, ev IntegrationEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) serveEventsSSE(
	w http.ResponseWriter,
	r *http.Request,
	q eventsQuery,
	readable func(string) bool,
	clientCursor int64,
	scanAfter int64,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprintf(w, ": ready cursor=%d\n\n", clientCursor); err != nil {
		return
	}
	flusher.Flush()

	ctx := r.Context()
	store := s.tasks.Store
	shutdown := s.tasks.Config.ShutdownStarted
	ticker := time.NewTicker(s.eventsKeepaliveDuration())
	defer ticker.Stop()

	for {
		ch := store.Changed()
		for {
			events, nextCursor, nextScan, hasMore, err := readAuthorizedEvents(
				ctx, store, scanAfter, clientCursor, MaxEventsLimit, q.repo, q.types, readable)
			if err != nil {
				return
			}
			clientCursor = nextCursor
			scanAfter = nextScan
			for _, ev := range events {
				if err := writeSSEEvent(w, flusher, ev); err != nil {
					return
				}
			}
			if !hasMore {
				break
			}
		}

		select {
		case <-ch:
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-shutdown:
			return
		case <-ctx.Done():
			return
		}
	}
}
