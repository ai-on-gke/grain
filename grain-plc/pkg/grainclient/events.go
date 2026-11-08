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

package grainclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func buildEventsPath(filter EventsFilter) string {
	q := url.Values{}
	if filter.Since > 0 {
		q.Set("since", strconv.FormatInt(filter.Since, 10))
	}
	if len(filter.Types) > 0 {
		q.Set("types", strings.Join(filter.Types, ","))
	}
	if filter.ExternalRef != "" {
		q.Set("externalRef", filter.ExternalRef)
	}
	if filter.TaskID != "" {
		q.Set("taskId", filter.TaskID)
	}
	if filter.Limit > 0 {
		q.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Wait > 0 {
		q.Set("wait", filter.Wait.String())
	}
	path := "/api/events"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return path
}

// PollEvents fetches a page of integration events via GET /api/events in JSON
// long-poll mode (Accept: application/json).
func (ic *InstanceClient) PollEvents(ctx context.Context, filter EventsFilter) (*EventsPage, error) {
	done := ic.perf.Start("grain.poll_events")
	var page EventsPage
	err := ic.doJSON(ctx, http.MethodGet, buildEventsPath(filter), nil, nil, &page)
	done(err)
	if err != nil {
		return nil, err
	}
	kept := page.Events[:0]
	for _, ev := range page.Events {
		if matchesClientFilter(filter, ev) {
			kept = append(kept, ev)
		}
	}
	page.Events = kept
	return &page, nil
}

// StreamEvents opens a Server-Sent Events (SSE) stream against GET /api/events
// (Accept: text/event-stream) and invokes handler for each IntegrationEvent.
// If filter.Since > 0, Last-Event-ID is also sent for standard SSE resume.
func (ic *InstanceClient) StreamEvents(ctx context.Context, filter EventsFilter, handler func(IntegrationEvent) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ic.baseURL+buildEventsPath(filter), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if filter.Since > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(filter.Since, 10))
	}

	// Use a dedicated client copy without a hard request timeout so long-lived SSE streams stay open.
	streamClient := *ic.httpClient
	streamClient.Timeout = 0

	resp, err := streamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return &APIError{
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(raw)),
		}
	}

	return parseSSEStream(ctx, resp.Body, ic.perf, func(ev IntegrationEvent) error {
		if !matchesClientFilter(filter, ev) {
			return nil
		}
		return handler(ev)
	})
}

func parseSSEStream(ctx context.Context, r io.Reader, recorder interface{ RecordCacheHit(string) }, handler func(IntegrationEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	var (
		eventID   int64
		eventType string
		dataLines []string
	)

	dispatch := func() error {
		if len(dataLines) == 0 {
			eventID = 0
			eventType = ""
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]

		var ev IntegrationEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			eventID = 0
			eventType = ""
			return fmt.Errorf("decode sse event data: %w", err)
		}
		if ev.ID == 0 && eventID > 0 {
			ev.ID = eventID
		}
		if ev.Type == "" && eventType != "" {
			ev.Type = eventType
		}
		eventID = 0
		eventType = ""
		if recorder != nil {
			recorder.RecordCacheHit("sse.events_received")
		}
		return handler(ev)
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			// SSE keepalive comment.
			continue
		}
		field, val, hasColon := strings.Cut(line, ":")
		if !hasColon {
			continue
		}
		val = strings.TrimPrefix(val, " ")
		switch field {
		case "id":
			if parsed, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64); err == nil {
				eventID = parsed
			}
		case "event":
			eventType = strings.TrimSpace(val)
		case "data":
			dataLines = append(dataLines, val)
		}
	}

	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return err
	}
	return dispatch()
}
