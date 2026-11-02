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

package model

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Who turned a task's auto-merge on or off, and why.
//
// Task.AutoMerge has many writers: Submit and PATCH, and grain's own
// holds -- the security hold, the path-trigger gate, a review's open
// questions, the broken-rules hold -- which turn it off while they wait
// and back on when they release. Before this file none of them said so
// anywhere, so a task filed with auto_merge: false that later read true
// (tasks 2329, 2371, 2380, 2387, 2395 and 2396, 2026-11-01..02) left
// nothing to tell which of them had done it.
//
// Two things close that:
//
//   - Every write through putTask that changes auto_merge appends a
//     TaskEvent (TaskEventAutoMergeOn/Off) in the same transaction,
//     attributed to whoever the context names (WithAutoMergeChange) and
//     always carrying the Go function that made the write, so a writer
//     nobody thought to attribute is still named.
//   - A "no" a person or the filer said out loud is sticky
//     (LinkAutoMergeDeclined, DeclineAutoMerge). While a task carries it,
//     putTask refuses any write that turns auto-merge back on unless the
//     same write drops the mark (AllowAutoMerge) -- which only an explicit
//     human or API action does: Submit, an approval that releases the
//     change, a PATCH to true. A hold releasing, a clearance carried
//     over, anything else grain does on its own account, leaves it off
//     and records that it tried (TaskEventAutoMergeKeptOff).
//
// The guard lives in the store rather than in each writer because the
// writers are many and keep growing: one forgotten path is exactly the
// bug.

const (
	TaskEventAutoMergeOn  TaskEventKind = "auto-merge-on"
	TaskEventAutoMergeOff TaskEventKind = "auto-merge-off"
	// TaskEventAutoMergeKeptOff is a write that would have turned
	// auto-merge on over a person's or the filer's explicit "no", which
	// putTask declined.
	TaskEventAutoMergeKeptOff TaskEventKind = "auto-merge-kept-off"
)

// The timeline's names for those events (MergeTaskEvents). Like
// TransitionReopened, they are not states StateOf answers.
const (
	TransitionAutoMergeOn      State = "auto_merge_on"
	TransitionAutoMergeOff     State = "auto_merge_off"
	TransitionAutoMergeKeptOff State = "auto_merge_kept_off"
)

// LinkAutoMergeDeclined marks a task whose auto-merge was turned off by
// an explicit choice -- the filer's auto_merge: false, or a person's or
// the API's PATCH to false. Its target is who said so. See this file's
// doc comment.
const LinkAutoMergeDeclined LinkKind = "auto-merge-declined"

// AutoMergeDeclined reports whether t carries an explicit "no" to
// auto-merge that only a person may lift.
func AutoMergeDeclined(t Task) bool {
	return slices.ContainsFunc(t.Links, func(l Link) bool { return l.Kind == LinkAutoMergeDeclined })
}

// DeclineAutoMerge turns t's auto-merge off and makes that sticky, by
// whoever by names ("" for nobody in particular).
func DeclineAutoMerge(t *Task, by string) {
	t.AutoMerge = false
	if AutoMergeDeclined(*t) {
		return
	}
	t.Links = append(t.Links, Link{Kind: LinkAutoMergeDeclined, Target: strings.TrimSpace(by)})
}

// AllowAutoMerge lifts DeclineAutoMerge's mark: for an explicit human or
// API action turning auto-merge on (Submit, an approval, a PATCH to
// true), in the same write that sets Task.AutoMerge.
func AllowAutoMerge(t *Task) {
	t.Links = slices.DeleteFunc(t.Links, func(l Link) bool { return l.Kind == LinkAutoMergeDeclined })
}

type autoMergeChangeKey struct{}

type autoMergeChange struct {
	by     Principal
	reason string
}

// WithAutoMergeChange names who is writing, and why, for any auto-merge
// change a task write under ctx makes. Without it the event is grain's
// own, with no reason but the writer's function name.
func WithAutoMergeChange(ctx context.Context, by Principal, reason string) context.Context {
	return context.WithValue(ctx, autoMergeChangeKey{}, autoMergeChange{by: by, reason: reason})
}

func autoMergeChangeFrom(ctx context.Context) (Principal, string) {
	if c, ok := ctx.Value(autoMergeChangeKey{}).(autoMergeChange); ok {
		if c.by.Kind == "" && c.by.ID == "" {
			c.by = GrainPrincipal
		}
		return c.by, c.reason
	}
	return GrainPrincipal, ""
}

// guardAutoMerge is putTask's half of this file, run before the row is
// replaced: it reads the auto-merge the row holds now, keeps a declined
// task's auto-merge off against a write that has not lifted the mark,
// and returns the event to record (nil for none). A new task records
// nothing: what it was filed with is on the task itself.
func guardAutoMerge(ctx context.Context, tx *sql.Tx, t *Task) (*TaskEvent, error) {
	var before bool
	err := tx.QueryRowContext(ctx, "SELECT `auto_merge` FROM `task` WHERE `id` = ?", t.ID).Scan(&before)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s's auto-merge: %w", t.ID, err)
	}
	if before == t.AutoMerge {
		return nil, nil
	}
	by, reason := autoMergeChangeFrom(ctx)
	e := &TaskEvent{TaskID: t.ID, Kind: TaskEventAutoMergeOff, At: time.Now().UTC(), By: by, Reason: reason,
		Detail: autoMergeWriter()}
	if t.AutoMerge {
		e.Kind = TaskEventAutoMergeOn
		if AutoMergeDeclined(*t) {
			t.AutoMerge = false
			e.Kind = TaskEventAutoMergeKeptOff
			if e.Reason == "" {
				e.Reason = "kept off: turned off explicitly, and only a person's Submit or PATCH turns it back on"
			}
			// A writer retrying every cycle (a hold whose release keeps
			// being attempted) is said once, not once a cycle.
			var last, detail string
			_ = tx.QueryRowContext(ctx,
				"SELECT `kind`, `detail` FROM `task_event` WHERE `task_id` = ? AND `kind` LIKE 'auto-merge-%' "+
					"ORDER BY `id` DESC LIMIT 1", t.ID).Scan(&last, &detail)
			if last == string(TaskEventAutoMergeKeptOff) && detail == e.Detail {
				return nil, nil
			}
		}
	}
	return e, nil
}

// modelPackage is this package's import path, the prefix every frame of
// the store's own machinery shares.
const modelPackage = "github.com/ai-on-gke/grain/pkg/model"

// autoMergeWriter is the first function outside the store -- the caller
// of UpdateTask or PutTask -- that is writing, as "pkg.Func (file.go:N)".
func autoMergeWriter() string {
	pcs := make([]uintptr, 48)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		fn := f.Function
		if fn != "" && !strings.HasPrefix(fn, modelPackage) && !strings.HasPrefix(fn, "database/sql") &&
			!strings.HasPrefix(fn, "runtime.") {
			if i := strings.LastIndex(fn, "/"); i >= 0 {
				fn = fn[i+1:]
			}
			return fmt.Sprintf("%s (%s:%d)", fn, filepath.Base(f.File), f.Line)
		}
		if !more {
			return ""
		}
	}
}
