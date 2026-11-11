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
// The other direction is sticky too: a "yes" -- the filer's
// auto_merge: true, or the default a task filed without one got -- comes
// back when the holds that took it away clear. Each of grain's own holds
// names itself on the task with a link (autoMergeHolds), and holds are
// only ever put on a change that was merging itself, so a write that
// drops the last of those links is the hold that turned auto-merge off
// clearing. putTask turns it back on in that same write
// (restoreAfterHolds), whatever writer it was: before this, each release
// path handed auto-merge back on its own account, and the ones that
// dropped a hold without doing so (a carried clearance lifting a
// human-only hold, a head re-read clean) left a change filed to merge
// itself waiting for a Submit nobody had been asked for.
//
// Not every hold ending is the change going through. One that ends in a
// person's decision -- a review that left findings, fixes that never
// landed, a gating trigger that requires a human -- says so
// (AwaitPersonForAutoMerge), and only auto-merge turning on (Submit, an
// approval, a person's PATCH, a review's answered questions) lifts that.
// Outstanding approvals and a merge policy of `never` keep it off too.
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

// LinkAutoMergeAwaitsPerson marks a change one of grain's holds let go of
// for a person to decide, rather than for it to merge itself: putTask
// does not hand auto-merge back to it when its last hold clears. Its
// target is why. It is dropped by whatever turns auto-merge back on.
const LinkAutoMergeAwaitsPerson LinkKind = "auto-merge-awaits-person"

// autoMergeHolds are the links grain's own holds put on a change that was
// merging itself when they take its auto-merge away. The last of them
// going is that change's hold clearing (restoreAfterHolds).
var autoMergeHolds = []LinkKind{LinkSecurityHold, LinkBrokenRulesHold, LinkPathTriggerHold, LinkHumanOnlyHold,
	LinkHumanReviewHold, LinkReviewFindings, LinkReviewQuestions}

// AutoMergeAwaitsPerson reports whether a hold on t ended in a person's
// decision (LinkAutoMergeAwaitsPerson).
func AutoMergeAwaitsPerson(t Task) bool {
	return slices.ContainsFunc(t.Links, func(l Link) bool { return l.Kind == LinkAutoMergeAwaitsPerson })
}

// AwaitPersonForAutoMerge turns t's auto-merge off and leaves it for a
// person: the hold that is ending, for reason, did not clear.
func AwaitPersonForAutoMerge(t *Task, reason string) {
	t.AutoMerge = false
	if AutoMergeAwaitsPerson(*t) {
		return
	}
	t.Links = append(t.Links, Link{Kind: LinkAutoMergeAwaitsPerson, Target: strings.TrimSpace(reason)})
}

// ClearAutoMergeAwaitsPerson drops AwaitPersonForAutoMerge's mark, for a
// writer that has put the decision somewhere else (a review's questions,
// which hand auto-merge back themselves once answered).
func ClearAutoMergeAwaitsPerson(t *Task) {
	t.Links = slices.DeleteFunc(t.Links, func(l Link) bool { return l.Kind == LinkAutoMergeAwaitsPerson })
}

func heldBy(links []Link) []string {
	var out []string
	for _, l := range links {
		if slices.Contains(autoMergeHolds, l.Kind) && !slices.Contains(out, string(l.Kind)) {
			out = append(out, string(l.Kind))
		}
	}
	return out
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
	normalizeHumanReviewHold(t)
	e, err := guardAutoMergeChange(ctx, tx, t)
	if err == nil && t.AutoMerge {
		// Whatever turned it on settled what a person was asked.
		ClearAutoMergeAwaitsPerson(t)
	}
	return e, err
}

func guardAutoMergeChange(ctx context.Context, tx *sql.Tx, t *Task) (*TaskEvent, error) {
	var before bool
	err := tx.QueryRowContext(ctx, "SELECT `auto_merge` FROM `task` WHERE `id` = ?", t.ID).Scan(&before)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s's auto-merge: %w", t.ID, err)
	}
	restored, err := restoreAfterHolds(ctx, tx, t)
	if err != nil {
		return nil, err
	}
	if before == t.AutoMerge {
		return declinedWhileOff(ctx, tx, t)
	}
	by, reason := autoMergeChangeFrom(ctx)
	if restored != "" {
		reason = strings.TrimSpace(restored + " " + reason)
	}
	e := &TaskEvent{TaskID: t.ID, Kind: TaskEventAutoMergeOff, At: time.Now().UTC(), By: by, Reason: reason,
		Detail: autoMergeWriter()}
	if t.AutoMerge {
		e.Kind = TaskEventAutoMergeOn
		held := HumanReviewHeld(*t)
		reviewHeld := HasPendingReviewHold(*t)
		if AutoMergeDeclined(*t) || held || reviewHeld {
			t.AutoMerge = false
			e.Kind = TaskEventAutoMergeKeptOff
			switch {
			case reviewHeld && !AutoMergeDeclined(*t) && !held:
				e.Reason = "kept off: waiting for required review gates (" + strings.Join(heldBy(t.Links), ", ") + ") to pass"
			case held && !AutoMergeDeclined(*t):
				if HasReviewFindings(*t) {
					e.Reason = "kept off: review left findings (" + HumanReviewSummary(*t) +
						"), and clearing findings requires a clean re-review or a merge:force gate override"
				} else {
					e.Reason = "kept off: held for a person's review (" + HumanReviewSummary(*t) +
						"), and only a person's Submit or Release ends that"
				}
			case e.Reason == "":
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

// restoreAfterHolds is putTask's other half (see this file's doc
// comment): a write that leaves t's auto-merge off but drops the last of
// the hold links the row holds now is that hold clearing, and t goes back
// to merging itself -- unless it was declined, a hold ended in a person's
// decision, approvals are still owed, or its merge rules say `never`. It
// returns the reason to record, or "" when it left t alone.
//
// Nor without a clearance (LinkTrustBoundaryCleared) or a recorded human
// Submit (LinkHumanSubmitted) left on t: a change with auto-merge on and
// neither is unclassified, so a write that drops the clearance along with
// the hold leaves the next head to be classified before anything turns
// auto-merge on.
func restoreAfterHolds(ctx context.Context, tx *sql.Tx, t *Task) (string, error) {
	hasClearanceOrSubmit := slices.ContainsFunc(t.Links, func(l Link) bool {
		return (l.Kind == LinkTrustBoundaryCleared || l.Kind == LinkHumanSubmitted || l.Kind == LinkHumanApproved) && l.Target != ""
	})
	if t.AutoMerge || AutoMergeDeclined(*t) || AutoMergeAwaitsPerson(*t) || len(heldBy(t.Links)) > 0 ||
		len(PendingApprovals(*t)) > 0 || !hasClearanceOrSubmit ||
		slices.ContainsFunc(t.Links, func(l Link) bool { return l.Kind == LinkMergePolicyWithdrawn }) {
		return "", nil
	}
	args := []any{t.ID}
	for _, k := range autoMergeHolds {
		args = append(args, string(k))
	}
	rows, err := tx.QueryContext(ctx, "SELECT `kind` FROM `task_link` WHERE `task_id` = ? AND `kind` IN (?"+
		strings.Repeat(",?", len(autoMergeHolds)-1)+")", args...)
	if err != nil {
		return "", fmt.Errorf("reading %s's holds: %w", t.ID, err)
	}
	defer rows.Close()
	var cleared []Link
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return "", fmt.Errorf("reading %s's holds: %w", t.ID, err)
		}
		cleared = append(cleared, Link{Kind: LinkKind(kind)})
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("reading %s's holds: %w", t.ID, err)
	}
	if len(cleared) == 0 {
		return "", nil
	}
	t.AutoMerge = true
	return fmt.Sprintf("back on: the hold that turned it off (%s) cleared, so it merges itself as it was filed to.",
		strings.Join(heldBy(cleared), ", ")), nil
}

// declinedWhileOff is guardAutoMerge's event for a write that leaves
// auto-merge off but declines it for the first time -- a person's PATCH
// to false on a change a hold had already turned off. Nothing about
// auto_merge changes, yet from here on only a person's Submit or PATCH
// turns it back on, so it is recorded as an auto-merge-off by whoever
// declined it (docs/test-runs/2026-11-03-operator-attribution.md F5).
// Before this, the decision left no event and no timeline step.
func declinedWhileOff(ctx context.Context, tx *sql.Tx, t *Task) (*TaskEvent, error) {
	if t.AutoMerge || !AutoMergeDeclined(*t) {
		return nil, nil
	}
	var one int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM `task_link` WHERE `task_id` = ? AND `kind` = ?",
		t.ID, string(LinkAutoMergeDeclined)).Scan(&one)
	if err == nil {
		return nil, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("reading %s's auto-merge mark: %w", t.ID, err)
	}
	by, reason := autoMergeChangeFrom(ctx)
	return &TaskEvent{TaskID: t.ID, Kind: TaskEventAutoMergeOff, At: time.Now().UTC(), By: by, Reason: reason,
		Detail: autoMergeWriter()}, nil
}

// modelPackage is this package's import path, the prefix every frame of
// the store's own machinery shares.
const modelPackage = "github.com/ai-on-gke/grain/pkg/model"

// autoMergeWrappers are functions outside this package that only pass an
// edit through to UpdateTask on their callers' behalf. Naming one would
// name every write the same (ui.(*Client).mutate, which every Client
// edit goes through), so autoMergeWriter looks past them to the function
// that made the change.
var autoMergeWrappers = map[string]bool{
	"github.com/ai-on-gke/grain/pkg/ui.(*Client).mutate": true,
}

// autoMergeWriter is the first function outside the store -- the caller
// of UpdateTask or PutTask -- that is writing, as "pkg.Func (file.go:N)",
// past any of autoMergeWrappers.
func autoMergeWriter() string {
	pcs := make([]uintptr, 48)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		fn := f.Function
		if fn != "" && !strings.HasPrefix(fn, modelPackage) && !strings.HasPrefix(fn, "database/sql") &&
			!strings.HasPrefix(fn, "runtime.") && !autoMergeWrappers[fn] {
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
