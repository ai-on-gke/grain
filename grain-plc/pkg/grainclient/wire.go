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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// This file reconciles grainclient's types with the JSON a live grain
// daemon speaks. Walking docs/test-plans/2026-11-07.md §7 against a real
// `grain demo` showed three mismatches the package's own fakes hid:
//
//   - grain's `designs` is a list of `[owner/name:]path[@rev]` strings, in
//     requests and responses alike, not {repo, path, rev} objects, so every
//     task carrying committed context failed to decode (and a request with
//     designs failed to file);
//   - grain names a task's text `description`, not `body`;
//   - GET /api/refs/{ref}/summary answers {total, byState, pullRequests,
//     openQuestions}, not {counts, tasks}, so every rollup read as zero.
//
// Each type below accepts both shapes, so the package's fakes keep working.

// DesignRefs is the `designs` list: committed-context references written
// the way grain's API and CLI write them (`[owner/name:]path[@rev]`).
type DesignRefs []ContextRef

// String renders r as `[owner/name:]path[@rev]`.
func (r ContextRef) String() string {
	out := r.Path
	if r.Repo != "" {
		out = r.Repo + ":" + out
	}
	if r.Rev != "" {
		out += "@" + r.Rev
	}
	return out
}

// ParseDesignRef reads `[owner/name:]path[@rev]`.
func ParseDesignRef(raw string) ContextRef {
	s := strings.TrimSpace(raw)
	var ref ContextRef
	if i := strings.LastIndex(s, "@"); i > 0 {
		ref.Rev = s[i+1:]
		s = s[:i]
	}
	if i := strings.Index(s, ":"); i > 0 && strings.Contains(s[:i], "/") {
		ref.Repo = s[:i]
		s = s[i+1:]
	}
	ref.Path = s
	return ref
}

// MarshalJSON writes the list as grain's strings.
func (d DesignRefs) MarshalJSON() ([]byte, error) {
	out := make([]string, 0, len(d))
	for _, r := range d {
		out = append(out, r.String())
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads grain's strings, or {repo, path, rev} objects.
func (d *DesignRefs) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(DesignRefs, 0, len(raw))
	for _, item := range raw {
		var s string
		if json.Unmarshal(item, &s) == nil {
			out = append(out, ParseDesignRef(s))
			continue
		}
		var ref ContextRef
		if err := json.Unmarshal(item, &ref); err != nil {
			return err
		}
		out = append(out, ref)
	}
	*d = out
	return nil
}

// MarshalJSON sends Body as grain's `description` too.
func (r CreateTaskRequest) MarshalJSON() ([]byte, error) {
	type plain CreateTaskRequest
	return json.Marshal(struct {
		plain
		Description string `json:"description,omitempty"`
	}{plain(r), r.Body})
}

// MarshalJSON sends Body as grain's `description` too.
func (p TaskPatch) MarshalJSON() ([]byte, error) {
	type plain TaskPatch
	return json.Marshal(struct {
		plain
		Description *string `json:"description,omitempty"`
	}{plain(p), p.Body})
}

// UnmarshalJSON fills Body from grain's `description` when no `body` came.
func (t *Task) UnmarshalJSON(data []byte) error {
	type plain Task
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.Body == "" {
		p.Body = p.Description
	}
	*t = Task(p)
	return nil
}

// RefPullRequests is grain's pull request rollup for an external reference.
type RefPullRequests struct {
	Opened int `json:"opened"`
	Merged int `json:"merged"`
	Open   int `json:"open"`
}

// RefOpenQuestion is one open awaiting_reply question under a reference.
type RefOpenQuestion struct {
	TaskID  string `json:"taskId"`
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary"`
}

// UnmarshalJSON derives Counts from grain's {total, byState, pullRequests}
// when the response carries no "counts" object.
func (s *RefSummary) UnmarshalJSON(data []byte) error {
	type plain RefSummary
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	var probe struct {
		Counts json.RawMessage `json:"counts"`
	}
	_ = json.Unmarshal(data, &probe)
	if len(probe.Counts) == 0 && (p.Total > 0 || len(p.ByState) > 0) {
		c := RefRollupCounts{Total: p.Total}
		for state, n := range p.ByState {
			switch state {
			case "completed":
				c.Done += n
			case "closed", "deferred":
				c.Closed += n
			case "failed":
				c.Failed += n
			case "cancelled":
				c.Cancelled += n
			case "awaiting_reply", "awaiting_access", "awaiting_secret", "awaiting_submit", "blocked":
				c.Blocked += n
			default:
				c.Active += n
			}
		}
		if p.PullRequests != nil {
			c.Merged = p.PullRequests.Merged
		}
		p.Counts = c
	}
	*s = RefSummary(p)
	return nil
}

// UnmarshalJSON reads grain's GET /api/events event (ui.IntegrationEvent):
// a string id, `at`, `previousState`, `questionSummary`, `pullRequest` and
// `ciConclusion`, beside the names this package's own fakes use. Before it,
// the first event of a live stream failed to decode ("cannot unmarshal
// string into ... IntegrationEvent.id") and ended the gateway's stream.
func (e *IntegrationEvent) UnmarshalJSON(data []byte) error {
	type plain IntegrationEvent
	var wire struct {
		plain
		ID              json.RawMessage `json:"id"`
		At              *time.Time      `json:"at"`
		PreviousState   string          `json:"previousState"`
		QuestionSummary string          `json:"questionSummary"`
		PullRequest     string          `json:"pullRequest"`
		CIConclusion    string          `json:"ciConclusion"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	out := IntegrationEvent(wire.plain)
	if len(wire.ID) > 0 {
		var n int64
		if json.Unmarshal(wire.ID, &n) == nil {
			out.ID = n
		} else {
			var s string
			if err := json.Unmarshal(wire.ID, &s); err != nil {
				return fmt.Errorf("event id: %w", err)
			}
			if s != "" {
				parsed, err := strconv.ParseInt(s, 10, 64)
				if err != nil {
					return fmt.Errorf("event id %q: %w", s, err)
				}
				out.ID = parsed
			}
		}
	}
	if out.Time.IsZero() && wire.At != nil {
		out.Time = *wire.At
	}
	if out.PrevState == "" {
		out.PrevState = wire.PreviousState
	}
	if out.Question == "" {
		out.Question = wire.QuestionSummary
	}
	if out.Summary == "" {
		out.Summary = wire.QuestionSummary
	}
	if out.PRURL == "" {
		out.PRURL = wire.PullRequest
	}
	if out.Status == "" {
		out.Status = wire.CIConclusion
	}
	*e = out
	return nil
}

// matchesClientFilter applies the EventsFilter fields grain's GET
// /api/events does not filter on itself (it narrows by repo, types and
// since only), so a caller asking for one externalRef or task is not
// handed every other task's events.
func matchesClientFilter(filter EventsFilter, ev IntegrationEvent) bool {
	if filter.ExternalRef != "" && !strings.EqualFold(ev.ExternalRef, filter.ExternalRef) {
		return false
	}
	if filter.TaskID != "" && ev.TaskID != filter.TaskID {
		return false
	}
	return true
}
