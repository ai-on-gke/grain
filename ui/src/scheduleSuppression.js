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

// The words for a schedule that has come due and is not firing
// (ui.ScheduleSuppression, grain/task-544), shared by the schedules
// page's own row and by the pane that row opens so the two say the same
// thing in the same order.
//
// A firing its schedule's overlap policy suppressed leaves the next run
// exactly where it was, so the poll re-asks and re-skips every cycle and
// nothing at all happens until the previous firing finishes -- for a
// nightly whose review nobody closed, that is up to three nights of
// nothing running. All the page had to go on was a next-run time that
// had already gone by, which is why the sentence below exists and why a
// next run in the past is drawn as a signal rather than as a date.

// describeSuppression is why this schedule is not firing, as a clause
// that follows "not firing: ". One per model.ScheduleSuppressionReason,
// with a fallback for a reason a newer daemon knows and this build does
// not -- an unfamiliar word is no reason to leave a stopped schedule
// looking like a working one.
export function describeSuppression(suppressed) {
  switch (suppressed?.reason) {
    case "task":
      return "the task its last firing filed is still open";
    case "suiteRun":
      return "its last suite run has not finished";
    case "workflowRun":
      return "the CI run its last firing started has not finished";
    case "review":
      return "the review of its last run is still open";
    case "standingWork":
      // Every occurrence is skipped and the cadence moves on, so this is
      // the schedule's standing status rather than a wait
      // (model.SuppressedByStandingWork). The server's own sentence names
      // the repository.
      return suppressed.detail
        ? suppressed.detail
        : "skipped: this deployment doesn't run this repository's standing work";
    default:
      return "its last firing has not finished";
  }
}

// OVERDUE_GRACE_MS is how far past its next run a schedule has to be
// before the page treats that time as a signal rather than as a date.
//
// A schedule that came due a moment ago is one the next reconcile cycle
// is about to fire -- ten seconds, by default
// (model.DefaultPollInterval) -- and a row that lit up every time a
// cadence came round and went out again on the next poll would be noise.
// A next run minutes in the past is something else: either a firing that
// is being suppressed, or a daemon that is not running.
export const OVERDUE_GRACE_MS = 2 * 60 * 1000;

// isOverdue reports whether nextRunAt has gone by long enough to be
// worth drawing differently. `now` is a parameter so a test can say when
// it is rather than moving the clock.
export function isOverdue(nextRunAt, now = Date.now()) {
  if (!nextRunAt) return false;
  const at = new Date(nextRunAt).getTime();
  return Number.isFinite(at) && at < now - OVERDUE_GRACE_MS;
}

// describeSkip is one recorded skipped firing (ui.ScheduleSkip,
// grain/task-3560) as a sentence: the server's own summary ("skipped at
// <time>: #<task> from the previous firing is still open (<state>)"),
// followed by what became of it. Unlike describeSuppression this is a
// record, so it is still there after the blocker has gone.
export function describeSkip(skip, formatWhen = (t) => t) {
  if (!skip) return "";
  const summary = skip.summary || `skipped at ${formatWhen(skip.at)}`;
  switch (skip.outcome) {
    case "fired": {
      const folded =
        skip.folded > 0
          ? `, standing in for ${skip.folded} later ${skip.folded === 1 ? "firing" : "firings"}`
          : "";
      const task = skip.firedTaskId ? ` as #${skip.firedTaskId}` : "";
      return `${summary} -- fired late${task} at ${formatWhen(skip.resolvedAt)}${folded}`;
    }
    case "dropped":
      return `${summary} -- dropped; the schedule moved on to its next run`;
    default:
      // A failed firing (model.SkippedFiringFailed) is retried every
      // cycle rather than waiting on anything a person can see clear.
      if (skip.reason === "firingFailed") {
        return `${summary} -- held; retried every cycle, and it fires once as soon as a retry succeeds`;
      }
      return `${summary} -- held; it fires once as soon as that clears`;
  }
}
