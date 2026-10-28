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

// pipeline_changes.go is GET /api/pipeline/changes: one compact entry per
// thing that changed in the pipeline since a cursor, for a judgement agent
// that has to wake, see what moved, and go back to sleep inside a bounded
// context (docs/design/durable-tasks.md#DT-13).
//
// It stores nothing of its own. Every entry is read off a record grain
// already keeps for another reason -- a task's own timestamps, the merge
// queue's observation, the base-branch verdict row, the merge-batch
// history -- and the stall onsets are the same TaskStallNote the row in
// the UI is drawn with. So the feed can only say what those records can:
// a task's state transitions are the moments its record stamps (filed,
// run started, run ended, closed, merged), and a base branch's verdict is
// its latest one. Notifications (DT-5) are not a source yet; they join the
// feed when their store lands.
//
// The cursor is an (at, seq) pair: at is the entry's own instant and seq
// orders entries that share it, by a key that does not depend on map
// order, so a cursor read back on the next wake lands on the same entry.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// PipelineChange is one entry of the feed: what changed (ID, Kind), from
// what to what, and when.
type PipelineChange struct {
	At     time.Time `json:"at"`
	Seq    int       `json:"seq"`
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	From   string    `json:"from,omitempty"`
	To     string    `json:"to"`
	Detail string    `json:"detail,omitempty"`
}

// The kinds an entry can be.
const (
	PipelineChangeTask  = "task"
	PipelineChangeQueue = "queue"
	PipelineChangeStall = "stall"
	PipelineChangeBase  = "base"
	PipelineChangeBatch = "batch"
)

// PipelineChanges is one page of the feed. Next is the cursor to pass as
// after on the next read -- the last entry's, or the one that was asked
// with when nothing has changed -- and More says the limit cut this page.
type PipelineChanges struct {
	Changes []PipelineChange `json:"changes"`
	Next    string           `json:"next"`
	More    bool             `json:"more,omitempty"`
	// Since is the instant a read with no cursor started from.
	Since time.Time `json:"since"`
}

// PipelineCursor is a position in the feed: every entry after (At, Seq).
type PipelineCursor struct {
	At  time.Time
	Seq int
}

// String is the cursor as a caller passes it back: the instant, a tilde,
// and the sequence number.
func (c PipelineCursor) String() string {
	return c.At.UTC().Format(time.RFC3339Nano) + "~" + strconv.Itoa(c.Seq)
}

// ParsePipelineCursor reads a cursor String wrote. A bare RFC 3339 instant
// is accepted too, as "everything after this moment".
func ParsePipelineCursor(s string) (PipelineCursor, error) {
	s = strings.TrimSpace(s)
	at, seqText, hasSeq := strings.Cut(s, "~")
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return PipelineCursor{}, fmt.Errorf("%q is not a pipeline cursor: want <RFC 3339 time>~<seq>, as the last read's next", s)
	}
	c := PipelineCursor{At: t, Seq: -1}
	if hasSeq {
		if c.Seq, err = strconv.Atoi(seqText); err != nil {
			return PipelineCursor{}, fmt.Errorf("%q is not a pipeline cursor: the part after ~ must be a number", s)
		}
	} else {
		// A bare instant means everything strictly after it.
		c.Seq = int(^uint(0) >> 1)
	}
	return c, nil
}

// after reports whether ch comes after the cursor.
func (c PipelineCursor) after(ch PipelineChange) bool {
	if !ch.At.Equal(c.At) {
		return ch.At.After(c.At)
	}
	return ch.Seq > c.Seq
}

const (
	// DefaultPipelineChangeLimit and MaxPipelineChangeLimit bound one page.
	DefaultPipelineChangeLimit = 100
	MaxPipelineChangeLimit     = 500
	// pipelineChangesLookback is where a read with no cursor starts: a
	// first wake wants today, not the deployment's whole history.
	pipelineChangesLookback = 24 * time.Hour
)

// PipelineChanges is the feed after the cursor (nil: the last
// pipelineChangesLookback), at most limit entries, oldest first.
func (c *Client) PipelineChanges(ctx context.Context, after *PipelineCursor, limit int) (PipelineChanges, error) {
	return c.PipelineChangesIn(ctx, "", after, limit)
}

// PipelineChangesIn is PipelineChanges for one repository's tasks, base
// branches and merge batches -- every repository's for repo "". It is
// what ?repo= asks for, and the agent API gate admits a repository-scoped
// tasks:read grant only to a call that names its repository, so that
// grant reads its own repository's feed and nobody else's.
func (c *Client) PipelineChangesIn(ctx context.Context, repo string, after *PipelineCursor, limit int) (PipelineChanges, error) {
	if limit <= 0 {
		limit = DefaultPipelineChangeLimit
	}
	limit = min(limit, MaxPipelineChangeLimit)
	now := c.now()
	cursor := PipelineCursor{At: now.Add(-pipelineChangesLookback), Seq: -1}
	if after != nil {
		cursor = *after
	}

	all, err := c.pipelineChanges(ctx, now, strings.TrimSpace(repo))
	if err != nil {
		return PipelineChanges{}, err
	}
	out := PipelineChanges{Changes: []PipelineChange{}, Next: cursor.String(), Since: cursor.At}
	for _, ch := range all {
		if !cursor.after(ch) {
			continue
		}
		if len(out.Changes) == limit {
			out.More = true
			break
		}
		out.Changes = append(out.Changes, ch)
	}
	if n := len(out.Changes); n > 0 {
		last := out.Changes[n-1]
		out.Next = PipelineCursor{At: last.At, Seq: last.Seq}.String()
	}
	return out, nil
}

// pipelineChanges is every entry the records can say about repo (every
// repository for ""), in feed order with Seq assigned.
func (c *Client) pipelineChanges(ctx context.Context, now time.Time, repo string) ([]PipelineChange, error) {
	inRepo := func(r string) bool { return repo == "" || strings.EqualFold(r, repo) }
	tasks, err := c.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var times map[string]model.StateTimes
	if c.Config.Stall != nil {
		if times, err = c.Store.StateTimes(ctx); err != nil {
			return nil, err
		}
	}
	var all []PipelineChange
	for _, t := range tasks {
		if !inRepo(t.Repo) {
			continue
		}
		all = append(all, taskChanges(t)...)
		if t.Stall != "" && c.Config.Stall != nil {
			if since := times[t.ID].Since(t.State); since != nil {
				at := stallOnset(c.Config.Stall, t.State, *since, now)
				all = append(all, PipelineChange{At: at, ID: t.ID, Kind: PipelineChangeStall,
					To: string(t.State) + " stalled"})
			}
		}
	}

	bases, err := c.Store.ListBaseBranchCI(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading base-branch CI: %w", err)
	}
	for _, row := range bases {
		if !inRepo(row.Repo.String()) {
			continue
		}
		id := row.Repo.String() + "@" + row.Branch
		if !row.StartedAt.IsZero() {
			all = append(all, PipelineChange{At: row.StartedAt, ID: id, Kind: PipelineChangeBase,
				To: "running", Detail: "run " + row.RunID})
		}
		if row.FinishedAt != nil {
			all = append(all, PipelineChange{At: *row.FinishedAt, ID: id, Kind: PipelineChangeBase,
				From: "running", To: blankAsWord(row.Conclusion, "finished"), Detail: "run " + row.RunID})
		}
	}

	batches, err := c.Store.MergeBatchRecords(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading merge-batch history: %w", err)
	}
	for _, b := range batches {
		if b.FinishedAt.IsZero() || !inRepo(b.Repo.String()) {
			continue
		}
		detail := fmt.Sprintf("%s@%s merged %d of %d", b.Repo, b.Branch, b.Merged, b.Size)
		if b.RunID != "" {
			detail += ", run " + b.RunID
		}
		all = append(all, PipelineChange{At: b.FinishedAt, ID: "batch " + strconv.FormatInt(b.ID, 10),
			Kind: PipelineChangeBatch, From: "running", To: blankAsWord(b.Outcome, "finished"), Detail: detail})
	}

	sortPipelineChanges(all)
	return all, nil
}

// taskChanges is what one task's record can say about its own history:
// the moments it stamps, each one a transition from whatever the previous
// one left it in.
func taskChanges(t Task) []PipelineChange {
	var out []PipelineChange
	add := func(at *time.Time, kind, to, detail string) {
		if at != nil && !at.IsZero() {
			out = append(out, PipelineChange{At: *at, ID: t.ID, Kind: kind, To: to, Detail: detail})
		}
	}
	add(t.CreatedAt, PipelineChangeTask, "filed", "")
	add(t.StartedAt, PipelineChangeTask, "running", "")
	if t.EndedAt != nil && (t.StartedAt == nil || !t.EndedAt.Before(*t.StartedAt)) {
		to := string(t.State)
		if t.State == model.StateRunning || t.State == model.StateClosed {
			to = "ended"
		}
		add(t.EndedAt, PipelineChangeTask, to, "")
	}
	add(t.ClosedAt, PipelineChangeTask, "closed", "")
	add(t.PrMergedAt, PipelineChangeTask, "merged", "")
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	for i := 1; i < len(out); i++ {
		out[i].From = out[i-1].To
	}

	add(t.MergeQueueStateAt, PipelineChangeQueue, t.MergeQueueState, "")
	add(t.MergeQueueStalledAt, PipelineChangeStall, "queue stalled", t.MergeQueueState)
	add(t.MergeQueueBlockedAt, PipelineChangeStall, "queue blocked", "waiting for a person's Retry merge")
	return out
}

// stallOnset is the moment a state's patience ran out: the earliest
// instant between since and now at which the stall note is already said.
// The note is a pure function of the state, its start and the clock, and
// it changes exactly once, so a bisection finds the instant to well under
// a second.
func stallOnset(note StallNoter, state model.State, since, now time.Time) time.Time {
	lo, hi := since, now
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2)
		if note(state, since, mid) != "" {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi.Truncate(time.Second)
}

// sortPipelineChanges puts the feed in (at, key) order and numbers the
// entries that share an instant.
func sortPipelineChanges(all []PipelineChange) {
	key := func(ch PipelineChange) string { return ch.Kind + "\x00" + ch.ID + "\x00" + ch.To }
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].At.Equal(all[j].At) {
			return all[i].At.Before(all[j].At)
		}
		return key(all[i]) < key(all[j])
	})
	for i := range all {
		if i > 0 && all[i].At.Equal(all[i-1].At) {
			all[i].Seq = all[i-1].Seq + 1
		} else {
			all[i].Seq = 0
		}
	}
}

func blankAsWord(s, word string) string {
	if s == "" {
		return word
	}
	return s
}

func (s *Server) handlePipelineChanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var after *PipelineCursor
	if text := q.Get("after"); text != "" {
		c, err := ParsePipelineCursor(text)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		after = &c
	}
	limit := 0
	if text := q.Get("limit"); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("limit %q is not a positive number", text))
			return
		}
		limit = n
	}
	page, err := s.tasks.PipelineChangesIn(r.Context(), q.Get("repo"), after, limit)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// PipelineChanges reads one page of GET /api/pipeline/changes.
func (c *HTTPClient) PipelineChanges(ctx context.Context, after string, limit int) (PipelineChanges, error) {
	q := make([]string, 0, 2)
	if after = strings.TrimSpace(after); after != "" {
		q = append(q, "after="+url.QueryEscape(after))
	}
	if limit > 0 {
		q = append(q, "limit="+strconv.Itoa(limit))
	}
	path := "/api/pipeline/changes"
	if len(q) > 0 {
		path += "?" + strings.Join(q, "&")
	}
	var page PipelineChanges
	if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
		return PipelineChanges{}, err
	}
	return page, nil
}
