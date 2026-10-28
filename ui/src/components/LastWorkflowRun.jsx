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

import { formatDateTime } from "../time.js";

// describeWorkflowRunOutcome is how the last firing of a schedule that
// runs a workflow ended, in words (ui.ScheduleWorkflowRun).
export function describeWorkflowRunOutcome(run) {
  if (!run.runId) return run.conclusion ? "could not start" : "starting";
  if (!run.conclusion) return "running";
  if (run.unfinished) return "never finished";
  if (run.conclusion === "success") return "green";
  return run.conclusion;
}

// LastWorkflowRun is the last firing of a schedule that runs a workflow:
// the grain CI run it started, on which branch, how it ended, and the
// review it filed -- or that it needed none.
//
// It is the one place a green night shows (grain/task-3160): a run that
// passed files no task, so without this the schedule would look as if it
// had done nothing at all. Links stop propagation because on a list row
// the row itself opens the schedule.
export default function LastWorkflowRun({ run, zone }) {
  if (!run) return null;
  const stop = (e) => e.stopPropagation();
  const outcome = describeWorkflowRunOutcome(run);
  return (
    <span
      className="schedule-last-workflow-run"
      data-testid="last-workflow-run"
    >
      {"Last run "}
      {run.runId ? (
        <a href={`/ci/${run.runId}`} onClick={stop}>
          {run.runId}
        </a>
      ) : (
        "(none)"
      )}
      {run.branch && <> on {run.branch}</>}
      {": "}
      <span
        className={
          outcome === "green" || outcome === "running"
            ? null
            : "schedule-overdue"
        }
      >
        {outcome}
      </span>
      {run.finishedAt && <> {formatDateTime(run.finishedAt, zone)}</>}
      {run.reviewTaskId ? (
        <>
          {", reviewed in "}
          <a href={`/tasks/${run.reviewTaskId}`} onClick={stop}>
            task {run.reviewTaskId}
          </a>
        </>
      ) : (
        run.reviewNotNeeded && <>, no review needed</>
      )}
    </span>
  );
}
