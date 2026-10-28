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

import { Chip, Stack, Typography } from "@mui/material";
import { processPath } from "../paths.js";
import ItemGlyph from "./ItemGlyph.jsx";
import Overlay from "./Overlay.jsx";
import { ListEmpty, ListHeader } from "./ListPrimitives.jsx";
import { TaskRow } from "./TaskList.jsx";

// childrenOf indexes every task in the backlog by the task that
// generated it (ui.Task.GeneratedFrom, read off the proposed-by link) --
// which is the one edge that says "this task exists because of that
// one", whether it was an agent's propose_task, the review grain filed
// of a change, or the merge queue's own repair of one.
function childrenOf(tasks) {
  const byParent = new Map();
  for (const t of tasks) {
    if (!t.generatedFrom) continue;
    const list = byParent.get(t.generatedFrom) || [];
    list.push(t);
    byParent.set(t.generatedFrom, list);
  }
  return byParent;
}

// descendantsOf is every task under `id`, depth first, flattened into
// one list rather than a tree of sublists.
//
// Flat because of what the reader is being told. The question this pane
// answers is "what did this run produce?", and the answer is the run's
// own task and everything that came out of it -- one indent says that.
// A tree would need an indent per generation, and .task-sublist only
// draws the one (style.css), so a grandchild would sit exactly where a
// child sits while claiming to be somewhere else. Each row still says
// what kind of thing it is (TaskRow's own "review"/"merge fix" chips,
// which is why these rows are not marked `nested`).
//
// `seen` guards against a task being counted twice -- links are written
// by agents and by the merge queue, and a cycle among them would
// otherwise be an infinite walk here.
function descendantsOf(id, byParent, seen) {
  const out = [];
  for (const child of byParent.get(id) || []) {
    if (seen.has(child.id)) continue;
    seen.add(child.id);
    out.push(child);
    out.push(...descendantsOf(child.id, byParent, seen));
  }
  return out;
}

// suiteRunPasses is what this pane draws: one entry per pass the run has
// fired, oldest first, each holding that pass's own tasks in the order
// the run filed them, and under each of those every task descended from
// it.
//
// The run itself only records the tasks its passes instantiated
// (model.SuiteRunTask) -- a follow-up an agent proposed is an ordinary
// task, filed with no idea that a suite is behind it -- so the rest of
// the answer is walked out of the backlog here. `tasks` is the whole of
// it (App's own /api/tasks list, which every view already holds), so
// nothing extra is fetched to draw this.
//
// A run task that is not in that list at all still gets a row, built
// from the run's own snapshot of it: a task deleted since the run filed
// it is part of what happened, and dropping the row would leave a pass
// silently short.
export function suiteRunPasses(run, tasks) {
  const byId = new Map(tasks.map((t) => [t.id, t]));
  const byParent = childrenOf(tasks);
  const seen = new Set((run.tasks || []).map((t) => t.taskId));
  const passes = new Map();
  for (const entry of run.tasks || []) {
    const task = byId.get(entry.taskId) || {
      id: entry.taskId,
      title: entry.templateName,
      state: entry.state,
      capabilities: [],
    };
    const list = passes.get(entry.pass) || [];
    list.push({
      task,
      children: descendantsOf(entry.taskId, byParent, seen),
    });
    passes.set(entry.pass, list);
  }
  return [...passes.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([pass, roots]) => ({ pass, roots }));
}

// STATUS_LABELS/STATUS_COLORS render model.SuiteRunStatus's own three
// values the way a human reads them -- Chip's own "color" vocabulary,
// TaskList's own state-chip precedent applied to a run instead of a
// task. A status this build has never heard of is shown as it came,
// rather than as a blank chip.
const STATUS_LABELS = {
  active: "Active",
  succeeded: "Succeeded",
  failed: "Failed",
};
const STATUS_COLORS = { active: "info", succeeded: "success", failed: "error" };

// describeMode says how a run decides when to stop -- model.SuiteMode's
// two values in English. Exported for the runs list, which says it about
// a run and about the suite behind it (SuitesList.jsx), for
// RunStatusChip's own reason.
export function describeMode(run) {
  return run.mode === "count"
    ? `run ${run.count}×`
    : `run until clean (max ${run.maxPasses})`;
}

// describeRunPlan is describeMode plus the one setting that changes what
// a reader of a run should expect to happen next: a run copied
// requireApproval off its suite files every pass's tasks as proposals,
// and nothing moves until somebody approves them. The suite's own row
// says "Requires approval"; a run of it said nothing, so a run sitting
// on proposals read exactly like one whose tasks were queued.
export function describeRunPlan(run) {
  const mode = describeMode(run);
  return run.requireApproval
    ? `${mode} · each pass's tasks wait for approval`
    : mode;
}

// RunStatusChip is exported because the runs list draws the same chip
// for the same run (SuitesList.jsx): a run's status has one vocabulary
// and one colour scheme, and the row and the pane it opens must not
// drift into disagreeing about either.
export function RunStatusChip({ run }) {
  return (
    <Chip
      size="small"
      color={STATUS_COLORS[run.status]}
      label={STATUS_LABELS[run.status] || run.status}
    />
  );
}

// ScheduledChip is the mark saying a clock started this run, drawn on the
// run's row (SuitesList.jsx) and at the top of its pane, the same way in
// both places. It is a link to the schedule that started the run: "a
// schedule did this" raises the question of which schedule, and the run
// is the one place that knows (model.SuiteRun.ScheduleID). It is a real
// link to the schedule's own deep link, the way RowLink is, so a
// keyboard reaches it and a modified click opens a new tab. A plain
// click stays in the page, through onOpenSchedule, and stops at the chip
// so the run row it sits on does not open the run as well.
//
// A schedule deleted since leaves the link pointing at an id the
// schedules page no longer has. That page says so ('no schedule
// "sched-3"'), which is the truth about where the run came from.
export function ScheduledChip({ scheduleId, onOpenSchedule }) {
  const href = processPath("schedule", scheduleId);
  return (
    <Chip
      size="small"
      variant="outlined"
      icon={<ItemGlyph kind="schedules" />}
      label="Scheduled"
      component="a"
      href={href}
      clickable
      aria-label={`Scheduled by ${scheduleId}`}
      title="Open the schedule that started this run"
      onClick={(e) => {
        e.stopPropagation();
        if (
          !onOpenSchedule ||
          e.button !== 0 ||
          e.metaKey ||
          e.ctrlKey ||
          e.shiftKey ||
          e.altKey
        )
          return;
        e.preventDefault();
        onOpenSchedule(scheduleId);
      }}
    />
  );
}

// SuiteRunPane is one suite run's own page: every task that run has put
// into the backlog, the passes it fired them in, and the tasks those
// tasks went on to spawn (grain/task-322).
//
// The suites page could already say how a run was going -- active,
// failed, which pass it is on -- and nothing more. What it could not
// answer is the question anybody actually has about a run ("what did it
// do?"), because the tasks are scattered through a backlog of hundreds
// with only a "suite" chip to tell them apart, and the follow-ups they
// proposed do not carry even that. This pane is that answer, and it is a
// link of its own (/suites/runs/7) so it can be sent to somebody.
//
// The rows are the flat list's own rows (TaskRow), the way every other
// view that lists tasks here draws them -- so a task reads the same
// here as it does anywhere else, and clicking one opens it.
export default function SuiteRunPane({
  run,
  tasks = [],
  config,
  onClose,
  onOpenTask,
  onOpenSchedule,
}) {
  const passes = suiteRunPasses(run, tasks);
  const total = passes.reduce(
    (n, p) => n + p.roots.reduce((m, r) => m + 1 + r.children.length, 0),
    0,
  );

  const header = (
    <>
      <Stack
        direction="row"
        alignItems="center"
        spacing={1}
        sx={{ mb: 1, flexWrap: "wrap", rowGap: 1 }}
      >
        <ItemGlyph kind="suites" size={20} />
        <Typography variant="h6" component="h2" sx={{ m: 0 }}>
          {run.suiteName || run.suiteId}
        </Typography>
        {/* The same chips the run's row on the suites page carries, in
            the same order (SuitesList.jsx): how it is going, then the
            repo and the branch it is going against, then the mark
            saying a clock started it. A reader who clicked that row
            should find the row's own answer at the top of what it
            opened. */}
        <span className="chips">
          <RunStatusChip run={run} />
          <Chip size="small" label={run.repo} />
          <Chip size="small" variant="outlined" label={run.base} />
          {run.scheduleId && (
            <ScheduledChip
              scheduleId={run.scheduleId}
              onOpenSchedule={onOpenSchedule}
            />
          )}
        </span>
      </Stack>
      {/* Why there is more than one pass below, or why there is only
          one: what this run stops on is the whole of its plan, and the
          headings under it are otherwise a sequence with no explanation.
          The run's own row says the same thing (SuitesList.jsx). */}
      <p className="hint">{describeRunPlan(run)}</p>
      {run.error && (
        <p
          className="hint"
          style={{ color: "var(--mui-palette-error-main, #c62828)" }}
        >
          {run.error}
        </p>
      )}
    </>
  );

  return (
    <Overlay
      onClose={onClose}
      pane
      label={run.suiteName || run.suiteId}
      backLabel="Suites"
      header={header}
    >
      <ListHeader
        title="Tasks"
        icon={<ItemGlyph kind="suites" size={20} />}
        count={total}
      />
      {passes.map(({ pass, roots }) => (
        <div key={pass}>
          {/* Every run has a pass, and a run of one pass has one heading
              saying so rather than a special case: "Pass 1" is a true
              thing to say about it, and the heading is what keeps a
              second pass from reading as more of the first.
              The count is every row under the heading, spawned tasks
              included, so the passes add up to the total above rather
              than leaving a reader to wonder what the difference is. */}
          <ListHeader
            title={`Pass ${pass}`}
            count={roots.reduce((n, r) => n + 1 + r.children.length, 0)}
          />
          <ul className="task-list">
            {roots.map(({ task, children }) => (
              <li key={task.id}>
                <TaskRow t={task} config={config} onOpenTask={onOpenTask} />
                {children.length > 0 && (
                  <ul className="task-sublist">
                    {children.map((c) => (
                      <li key={c.id}>
                        <TaskRow
                          t={c}
                          config={config}
                          onOpenTask={onOpenTask}
                        />
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            ))}
          </ul>
        </div>
      ))}
      {total === 0 && (
        <ListEmpty>
          This run has filed no tasks yet -- its first pass is on the way.
        </ListEmpty>
      )}
    </Overlay>
  );
}
