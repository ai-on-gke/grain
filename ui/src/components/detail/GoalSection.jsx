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

import { useState } from "react";
import { Button, MenuItem, Stack, TextField } from "@mui/material";
import api from "../../api.js";
import { useTimeZone } from "../../TimeZoneContext.jsx";
import { formatDateTime } from "../../time.js";

// GoalSection is this task's goal (model.TaskGoal,
// docs/design/durable-tasks.md#DT-4): what grain checks every cycle to
// close the task, or put it to rest, rather than waiting for its agent
// to say it is done -- with the live check, the progress grain has seen
// towards it, and its limits. It edits the goal too (PUT and DELETE
// /api/tasks/{id}/goal): a person is one of the four ways a goal is set.
//
// The goal is read on a single task's own read only (Task.Goal), so a
// task with none shows the "Add goal" control and nothing else.

export const GOAL_KINDS = [
  ["base-green", "Base branch green"],
  ["pr-merged", "Pull request merged"],
  ["tasks-closed", "Tasks closed"],
  ["person-closes", "A person closes it"],
  ["pipeline-healthy", "Pipeline healthy"],
];

const kindLabel = (kind) =>
  (GOAL_KINDS.find(([k]) => k === kind) || [kind, kind])[1];

// goalSummary is the goal in a sentence: what it waits for.
export function goalSummary(goal) {
  const s = goal.spec || {};
  switch (goal.kind) {
    case "base-green":
      return `${s.branch} on ${s.repo} green${s.publishing ? " and published" : ""}`;
    case "pr-merged":
      return s.task
        ? `task ${s.task}'s pull request merged`
        : `${s.repo}#${s.number} merged`;
    case "tasks-closed":
      return `${(s.tasks || []).join(", ")} ${s.until || "closed"}`;
    case "pipeline-healthy":
      return s.repos?.length
        ? `the pipeline healthy for ${s.repos.join(", ")}`
        : "the pipeline healthy";
    default:
      return "a person closes it";
  }
}

function seconds(ns) {
  if (!ns) return "";
  const h = ns / 3.6e12;
  return h >= 1 ? `${Math.round(h * 10) / 10}h` : `${Math.round(ns / 6e10)}m`;
}

// draftOf is the editor's state for goal (or a fresh one for the task).
function draftOf(goal, t) {
  const s = goal?.spec || {};
  return {
    kind: goal?.kind || "base-green",
    repo: s.repo || t.repo || "",
    branch: s.branch || t.base || "main",
    task: s.task || "",
    tasks: (s.tasks || []).join(", "),
    until: s.until || "closed",
    onMet: goal?.onMet || "close",
    onLimit: goal?.onLimit || "hold",
  };
}

// bodyOf is the editor's state as PUT /api/tasks/{id}/goal takes it.
export function bodyOf(d) {
  const spec = {};
  if (d.kind === "base-green")
    Object.assign(spec, { repo: d.repo, branch: d.branch });
  if (d.kind === "pr-merged") spec.task = d.task;
  if (d.kind === "tasks-closed") {
    spec.tasks = d.tasks
      .split(/[\s,]+/)
      .map((x) => x.replace(/^#/, ""))
      .filter(Boolean);
    spec.until = d.until;
  }
  return { kind: d.kind, spec, on_met: d.onMet, on_limit: d.onLimit };
}

export default function GoalSection({ t, act }) {
  const zone = useTimeZone();
  const goal = t.goal;
  const [draft, setDraft] = useState(null);
  const closed = t.state === "closed";
  const set = (k) => (e) => setDraft({ ...draft, [k]: e.target.value });

  const save = () =>
    act(
      () =>
        api(`/api/tasks/${t.id}/goal`, {
          method: "PUT",
          body: JSON.stringify(bodyOf(draft)),
        }),
      t.id,
    ).then((ok) => ok && setDraft(null));
  const remove = () =>
    act(() => api(`/api/tasks/${t.id}/goal`, { method: "DELETE" }), t.id);

  return (
    <fieldset data-testid="goal-section" style={{ minWidth: 0 }}>
      <legend>Goal</legend>
      {!goal && !draft && (
        <p className="hint">
          No goal: this task completes when its agent says it is done.
        </p>
      )}
      {goal && !draft && (
        <div data-testid="goal-summary">
          <p style={{ margin: "0 0 4px" }}>
            <strong>{kindLabel(goal.kind)}</strong>: {goalSummary(goal)}.{" "}
            {goal.outcome === "goal-met"
              ? "Met; the task was closed."
              : goal.metAt
                ? `Met ${formatDateTime(goal.metAt, zone)}.`
                : "Not met yet."}
          </p>
          {goal.evidence && (
            <p className="hint" data-testid="goal-evidence">
              {goal.evidence}
            </p>
          )}
          <p className="hint" data-testid="goal-progress">
            On met: {goal.onMet}. {goal.resumesSinceProgress || 0} resume(s)
            without progress since {formatDateTime(goal.progressAt, zone)}
            {goal.maxResumes || goal.maxStall
              ? ` (limit ${goal.maxResumes || "∞"} resumes or ${seconds(goal.maxStall) || "no stall limit"}, then ${goal.onLimit})`
              : " (no progress limit)"}
            .
            {goal.limitNotifiedAt && " Held at its limit: a person has to act."}
          </p>
        </div>
      )}
      {draft && (
        <Stack spacing={1} sx={{ mt: 1 }} data-testid="goal-editor">
          <TextField
            select
            size="small"
            label="Goal"
            value={draft.kind}
            onChange={set("kind")}
          >
            {GOAL_KINDS.map(([k, label]) => (
              <MenuItem key={k} value={k}>
                {label}
              </MenuItem>
            ))}
          </TextField>
          {draft.kind === "base-green" && (
            <Stack spacing={1}>
              <TextField
                size="small"
                label="Repo"
                value={draft.repo}
                onChange={set("repo")}
                fullWidth
              />
              <TextField
                size="small"
                label="Branch"
                value={draft.branch}
                onChange={set("branch")}
              />
            </Stack>
          )}
          {draft.kind === "pr-merged" && (
            <TextField
              size="small"
              label="Task"
              value={draft.task}
              onChange={set("task")}
            />
          )}
          {draft.kind === "tasks-closed" && (
            <Stack spacing={1}>
              <TextField
                size="small"
                label="Tasks"
                value={draft.tasks}
                onChange={set("tasks")}
                fullWidth
              />
              <TextField
                select
                size="small"
                label="Until"
                value={draft.until}
                onChange={set("until")}
              >
                <MenuItem value="closed">closed</MenuItem>
                <MenuItem value="merged">merged</MenuItem>
              </TextField>
            </Stack>
          )}
          <Stack spacing={1}>
            <TextField
              select
              size="small"
              label="When met"
              value={draft.onMet}
              onChange={set("onMet")}
            >
              <MenuItem value="close">Close the task</MenuItem>
              <MenuItem value="rest">Let it rest</MenuItem>
            </TextField>
            <TextField
              select
              size="small"
              label="At its limit"
              value={draft.onLimit}
              onChange={set("onLimit")}
              fullWidth
            >
              <MenuItem value="hold">Hold for a person</MenuItem>
              <MenuItem value="notify">Notify and back off</MenuItem>
            </TextField>
          </Stack>
        </Stack>
      )}
      {!closed && (
        <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
          {draft ? (
            <>
              <Button size="small" variant="contained" onClick={save}>
                Save goal
              </Button>
              <Button size="small" onClick={() => setDraft(null)}>
                Cancel
              </Button>
            </>
          ) : (
            <>
              <Button size="small" onClick={() => setDraft(draftOf(goal, t))}>
                {goal ? "Edit goal" : "Add goal"}
              </Button>
              {goal && (
                <Button size="small" color="warning" onClick={remove}>
                  Remove goal
                </Button>
              )}
            </>
          )}
        </Stack>
      )}
    </fieldset>
  );
}
