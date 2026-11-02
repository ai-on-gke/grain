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

package namespace

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// GrainLogQuery narrows a deploy://grain.log read the way read_grain_log's
// own arguments do (pkg/capability/grainlog): a window, a least priority,
// a plain substring and a line cap. The zero value is that tool's own
// default -- the last stretch of the log, every priority.
type GrainLogQuery struct {
	Since, Until string
	MinPriority  string
	Contains     string
	MaxLines     int
	// TaskID is the task whose sandbox is reading, for read_grain_log's
	// audit line. The namespace sets it from the caller's Scope; it is
	// never read from the query.
	TaskID string
}

// MetricsQuery narrows a deploy://metrics read the way read_metrics' own
// arguments do (mcp.MetricsQuery): a relative window or an absolute
// since/until span, and the repositories and authors the numbers are
// about. The zero value is the daemon's default report.
type MetricsQuery struct {
	Window, Since, Until string
	Repos, Authors       []string
}

type queryKey struct{}

// withQuery hands a request's query to Resolve, whose signature carries
// only the path every scheme shares. Only deploy:// reads it: there it
// is the one way `grain log` and `grain metrics` can ask for a window or
// a filter instead of the provider's default.
func withQuery(ctx context.Context, q url.Values) context.Context {
	return context.WithValue(ctx, queryKey{}, q)
}

func queryFrom(ctx context.Context) url.Values {
	q, _ := ctx.Value(queryKey{}).(url.Values)
	return q
}

// grainLogQueryFrom reads ?since=, ?until=, ?min_priority=, ?contains=
// and ?max_lines= (or its short form ?lines=).
func grainLogQueryFrom(ctx context.Context) (GrainLogQuery, error) {
	q := queryFrom(ctx)
	out := GrainLogQuery{
		Since:       strings.TrimSpace(q.Get("since")),
		Until:       strings.TrimSpace(q.Get("until")),
		MinPriority: strings.TrimSpace(q.Get("min_priority")),
		Contains:    q.Get("contains"),
	}
	raw := strings.TrimSpace(q.Get("max_lines"))
	if raw == "" {
		raw = strings.TrimSpace(q.Get("lines"))
	}
	if raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return GrainLogQuery{}, fmt.Errorf("%w: max_lines %q is not a positive whole number of entries", ErrBadRequest, raw)
		}
		out.MaxLines = n
	}
	return out, nil
}

// pipelineQueryFrom reads deploy://pipeline-changes' ?cursor= (or ?since=,
// the same thing: a cursor a page ended with, or an RFC 3339 time) and
// ?page=, the most entries to return. Not ?limit=, which every namespace
// read already takes as a byte count.
func pipelineQueryFrom(ctx context.Context) (cursor string, limit int, err error) {
	q := queryFrom(ctx)
	cursor = strings.TrimSpace(q.Get("cursor"))
	if cursor == "" {
		cursor = strings.TrimSpace(q.Get("since"))
	}
	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n <= 0 {
			return "", 0, fmt.Errorf("%w: page %q is not a positive whole number of changes", ErrBadRequest, raw)
		}
		limit = n
	}
	return cursor, limit, nil
}

// metricsQueryFrom reads ?window=, ?since=, ?until=, and every ?repo= and
// ?author= (each also comma-separated).
func metricsQueryFrom(ctx context.Context) MetricsQuery {
	q := queryFrom(ctx)
	return MetricsQuery{
		Window:  strings.TrimSpace(q.Get("window")),
		Since:   strings.TrimSpace(q.Get("since")),
		Until:   strings.TrimSpace(q.Get("until")),
		Repos:   splitQueryList(q["repo"]),
		Authors: splitQueryList(q["author"]),
	}
}

func splitQueryList(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}
