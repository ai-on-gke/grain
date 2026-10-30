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

import { useEffect, useMemo, useState } from "react";
import { Button, Chip, Menu, MenuItem, Typography } from "@mui/material";
import { knownRepos } from "../state.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";
import { migrateProcessOrder, useListOrder } from "../listOrder.js";
import { processPath } from "../paths.js";
import {
  CREATABLE_KINDS,
  PROCESS_KINDS,
  PROCESS_SEGMENTS,
  buildProcessRows,
  countByKind,
  describeRecurrence,
  processKind,
} from "../processes.js";
import {
  PROCESS_ATTRIBUTES,
  kindsOf,
  narrowProcesses,
  processNarrowingOf,
  withKinds,
} from "../processFilters.js";
import NarrowingBar from "./NarrowingBar.jsx";
import {
  describeSkip,
  describeSuppression,
  isOverdue,
} from "../scheduleSuppression.js";
import ScheduleOverlay from "./ScheduleOverlay.jsx";
import TemplateOverlay from "./TemplateOverlay.jsx";
import UseTemplateOverlay from "./UseTemplateOverlay.jsx";
import SuiteOverlay from "./SuiteOverlay.jsx";
import SuiteRunOverlay from "./SuiteRunOverlay.jsx";
import SuiteRunPane, {
  RunStatusChip,
  ScheduledChip,
  describeMode,
  describeRunPlan,
} from "./SuiteRunPane.jsx";
import {
  focusRowLink,
  ListEmpty,
  ListHeader,
  ReorderableList,
  RowLink,
} from "./ListPrimitives.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import { DeclaredChip } from "./Declared.jsx";
import LastWorkflowRun from "./LastWorkflowRun.jsx";
import ChipStrip from "./ChipStrip.jsx";

const DECLARATIONS_DOC =
  "https://github.com/ai-on-gke/grain/blob/main/docs/declarations.md";

// ProcessesList is the Processes page (docs/design/settings-panel.md §5):
// one table of every schedule, workflow schedule, template, suite and
// trigger, where there used to be a Schedules, a Templates and a Suites
// page. Each row carries a kind chip, and the toolbar has a kind filter
// beside the search and the sort the three old lists had.
//
// The toolbar is the one every task view and the CI pane wear
// (NarrowingBar.jsx), over the attributes processFilters.js declares:
// kind, source, type, name, target repo, enabled, cadence, next and last
// run and the rest. The kind chips under it are a shortcut for its one
// "Kind is any of" condition rather than a filter of their own.
//
// A row opens the overlay it always opened (§5.2): which schedule,
// template, suite or suite run is open is App's state, so the URL names
// it (/processes/schedules/sched-1). The narrowing -- search, sort and
// conditions -- is App's too (`query`), so a filtered view is a link
// (/processes?f=kind:any:schedule&f=enabled:any:no). What stays here is what has no
// address: the blank create forms, and a template being used or a suite
// being run.
//
// The open item is looked up in the lists as App holds them; a link to
// one that is not there is App's to report (keepIfListed), not this
// list's to fall through to (grain/task-1696).
const byName = (a, b) => a.name.localeCompare(b.name);

export default function ProcessesList({
  schedules = [],
  templates = [],
  suites = [],
  suiteRuns = [],
  triggers = [],
  config,
  tasks,
  query = {},
  onQueryChange = () => {},
  resetSeq = 0,
  openScheduleId,
  onOpenSchedule,
  openTemplateId,
  onOpenTemplate,
  openSuiteId,
  onOpenSuite,
  openSuiteRunId,
  onOpenRun,
  onOpenScheduleFromRun = null,
  onOpenTrigger = () => {},
  onOpenTask,
  onRefreshSchedules = async () => {},
  onRefreshTemplates = async () => {},
  onRefreshSuites = async () => {},
  onRefreshRuns = async () => {},
  onRefreshTasks = async () => {},
  showError,
}) {
  const zone = useTimeZone();
  const narrowing = processNarrowingOf(query);
  const { filters } = narrowing;
  const kinds = kindsOf(filters);
  const custom = narrowing.sort.length === 0;
  const narrow = (patch) => onQueryChange({ ...narrowing, ...patch });

  // Which create form is open (a kind), the template a "Use…" opened, and
  // the suite a "Run…" opened (or true for the bare "+ Run").
  const [creating, setCreating] = useState(null);
  const [newMenu, setNewMenu] = useState(null);
  const [using, setUsing] = useState(null);
  const [running, setRunning] = useState(null);

  const repoOptions = knownRepos(config, tasks);
  // What a schedule or suite may fire: this deployment's own templates.
  // grain's built-in ones (ui.Template.System) are only filed from by
  // hand, and the server refuses a schedule or suite naming one.
  const schedulableTemplates = useMemo(
    () => templates.filter((t) => !t.system),
    [templates],
  );
  const editingSchedule =
    schedules.find((s) => s.id === openScheduleId) || null;
  const editingTemplate =
    templates.find((t) => t.id === openTemplateId) || null;
  const editingSuite = suites.find((s) => s.id === openSuiteId) || null;
  // A run id is a number over the wire and a string in the URL.
  const openRun =
    suiteRuns.find((r) => String(r.id) === String(openSuiteRunId)) || null;

  const rows = useMemo(
    () =>
      buildProcessRows({ schedules, templates, suites, suiteRuns, triggers }),
    [schedules, templates, suites, suiteRuns, triggers],
  );

  // The three old pages' custom orders become this one's, once (§5.6),
  // before the order below is first read.
  useState(() => migrateProcessOrder());
  // Every row, not just the ones the search and filter leave showing:
  // what a drag stores has to know where the hidden rows sit too.
  const [ordered, move] = useListOrder("processes", rows, (r) => r.key, byName);
  // A suite's mode chip ("run until clean (max 3)") is searchable too,
  // in the words SuiteRunPane.jsx writes it in.
  const extraOf = (r) => (r.kind === "suite" ? [describeMode(r.item)] : []);
  const source = custom ? ordered : rows;
  const visible = narrowProcesses(source, narrowing, config, extraOf);
  // Each chip's count is over what everything but the kind condition
  // leaves, so picking one kind does not zero the others' counts.
  const unkinded = {
    ...narrowing,
    filters: filters.filter((c) => c.attr !== "kind"),
  };
  const searched = narrowProcesses(source, unkinded, config, extraOf);
  const counts = countByKind(searched);
  // Every row in the order asked for, nothing filtered: the order "+ Run"
  // offers suites in.
  const sorted = narrowProcesses(
    source,
    { sort: narrowing.sort },
    config,
    extraOf,
  );

  // Picking a chip narrows to that kind; picking it again, or All, goes
  // back to every kind. Several can be on at once. Either way it is the
  // toolbar's own "Kind is any of" condition that changes.
  const toggleKind = (id) => {
    const next = kinds.includes(id)
      ? kinds.filter((k) => k !== id)
      : [...kinds, id];
    narrow({ filters: withKinds(filters, next) });
  };

  // New skips its menu when the filter is narrowed to one kind that has
  // a form (§5.3).
  const onlyKind =
    kinds.length === 1 && CREATABLE_KINDS.includes(kinds[0]) ? kinds[0] : null;

  const openRow = (r) => {
    if (r.kind === "schedule" || r.kind === "workflow") onOpenSchedule(r.id);
    else if (r.kind === "template") onOpenTemplate(r.id);
    else if (r.kind === "suite") onOpenSuite(r.id);
    else if (r.kind === "trigger") onOpenTrigger(r.item.repo);
  };

  const showRuns = kinds.length === 0 || kinds.includes("suite");
  // A run's Scheduled chip opens the schedule that started it, in the
  // run's place: an open run is the pane on top (paths.js), so it closes.
  // App's own openSchedule, when it is given one, also holds the id the
  // way a link's is held, so a schedule deleted since says so.
  const openScheduleFromRun =
    onOpenScheduleFromRun ||
    ((id) => {
      onOpenRun(null);
      onOpenSchedule(id);
    });

  // A link to an item that is open closes any create form left open.
  useEffect(() => {
    if (openScheduleId || openTemplateId || openSuiteId || openSuiteRunId) {
      setCreating(null);
      setUsing(null);
      setRunning(null);
    }
  }, [openScheduleId, openTemplateId, openSuiteId, openSuiteRunId]);

  useEffect(() => {
    if (!resetSeq) return;
    setCreating(null);
    setUsing(null);
    setRunning(null);
  }, [resetSeq]);

  return (
    <main className="processes-page">
      <ListHeader
        title="Processes"
        icon={<ItemGlyph kind="processes" size={20} />}
        count={visible.length}
        action={
          <>
            <Button
              variant="contained"
              size="small"
              sx={{ ml: "auto" }}
              aria-haspopup={onlyKind ? undefined : "menu"}
              onClick={(e) =>
                onlyKind ? setCreating(onlyKind) : setNewMenu(e.currentTarget)
              }
            >
              {onlyKind
                ? `+ New ${processKind(onlyKind).chip.toLowerCase()}`
                : "+ New"}
            </Button>
            <Menu
              anchorEl={newMenu}
              open={!!newMenu}
              onClose={() => setNewMenu(null)}
              MenuListProps={{ "data-testid": "new-process-kind" }}
            >
              {CREATABLE_KINDS.map((k) => (
                <MenuItem
                  key={k}
                  onClick={() => {
                    setNewMenu(null);
                    setCreating(k);
                  }}
                >
                  <ItemGlyph kind={processKind(k).glyph} />
                  <span style={{ marginLeft: "0.5rem" }}>
                    {processKind(k).chip}
                  </span>
                </MenuItem>
              ))}
              <Typography
                component="li"
                variant="caption"
                color="text.secondary"
                className="new-process-hint"
                sx={{ display: "block", px: 2, py: 1, maxWidth: 260 }}
              >
                Workflow schedules and triggers are declared in a
                repository&apos;s <code>.grain/</code> directory --{" "}
                <a href={DECLARATIONS_DOC} target="_blank" rel="noreferrer">
                  declarations
                </a>
                .
              </Typography>
            </Menu>
          </>
        }
      />
      {rows.length > 0 && (
        <NarrowingBar
          tasks={rows}
          config={config}
          attributes={PROCESS_ATTRIBUTES}
          narrowing={narrowing}
          onNarrow={narrow}
          searchPlaceholder="Search processes…"
          orderLabel="Custom order"
          orderNote="The page's own order, which is the one a drag changes."
        />
      )}
      {rows.length > 0 && (
        <div
          className="process-kinds"
          role="group"
          aria-label="Kind"
          data-testid="process-kind-filter"
        >
          <Chip
            size="small"
            label={`All ${searched.length}`}
            color={kinds.length === 0 ? "primary" : "default"}
            variant="outlined"
            sx={kinds.length === 0 ? { fontWeight: 600, borderWidth: 2 } : null}
            aria-pressed={kinds.length === 0}
            onClick={() => narrow({ filters: withKinds(filters, []) })}
          />
          {PROCESS_KINDS.map((k) => (
            <Chip
              key={k.id}
              size="small"
              data-kind={k.id}
              label={`${k.label} ${counts[k.id] || 0}`}
              color={kinds.includes(k.id) ? "primary" : "default"}
              variant="outlined"
              sx={
                kinds.includes(k.id)
                  ? { fontWeight: 600, borderWidth: 2 }
                  : null
              }
              aria-pressed={kinds.includes(k.id)}
              onClick={() => toggleKind(k.id)}
            />
          ))}
        </div>
      )}
      {visible.length > 0 && (
        <div className="process-row process-head hint" aria-hidden="true">
          <span />
          <span>Kind</span>
          <span>Name</span>
          <span>Repo</span>
          <span>Last run</span>
          <span>Next run</span>
          <span>Enabled</span>
          <span />
        </div>
      )}
      <ReorderableList
        className="processes-list"
        items={visible}
        idOf={(r) => r.key}
        nameOf={(r) => r.name}
        noun="process"
        reorder={custom ? move : null}
      >
        {(r, { handle, dragging }) => (
          <ProcessRow
            row={r}
            handle={handle}
            dragging={dragging}
            zone={zone}
            onOpen={() => openRow(r)}
            onOpenTask={onOpenTask}
            onUse={() => setUsing(r.id)}
            onRun={() => setRunning(r.id)}
          />
        )}
      </ReorderableList>
      {rows.length === 0 && (
        <ListEmpty>
          No processes yet -- a schedule fires work on a clock, a template is a
          task to file again and again, and a suite runs several of them against
          a branch.
        </ListEmpty>
      )}
      {rows.length > 0 && visible.length === 0 && (
        <ListEmpty>No processes match your search.</ListEmpty>
      )}

      {/* A suite's runs are what a suite's own status is, so they stay
          beneath the table whenever suites are among what it shows,
          the list the Suites page drew under its suites. */}
      {showRuns && (
        <>
          <ListHeader
            title="Suite runs"
            icon={<ItemGlyph kind="suites" size={20} />}
            count={suiteRuns.length}
            action={
              suites.length > 0 ? (
                <Button
                  variant="outlined"
                  size="small"
                  sx={{ ml: "auto" }}
                  onClick={() => setRunning(true)}
                >
                  + Run
                </Button>
              ) : null
            }
            style={{ marginTop: "1.5rem" }}
          />
          <ul className="template-list">
            {suiteRuns.map((run) => (
              <li
                className="suite-run-row template-row"
                key={run.id}
                onClick={(e) => {
                  focusRowLink(e);
                  onOpenRun(String(run.id));
                }}
              >
                <RowLink
                  className="template-name"
                  href={processPath("run", run.id)}
                  onOpen={() => onOpenRun(String(run.id))}
                >
                  {run.suiteName ||
                    suites.find((s) => s.id === run.suiteId)?.name ||
                    run.suiteId}
                </RowLink>
                <ChipStrip>
                  <RunStatusChip run={run} />
                  <Chip size="small" label={run.repo} />
                  <Chip size="small" variant="outlined" label={run.base} />
                  {run.scheduleId && (
                    <ScheduledChip
                      scheduleId={run.scheduleId}
                      onOpenSchedule={openScheduleFromRun}
                    />
                  )}
                </ChipStrip>
                <span className="template-title hint">
                  pass {run.pass} · {describeRunPlan(run)}
                </span>
                {run.error && (
                  <span
                    className="template-title hint"
                    style={{ color: "var(--mui-palette-error-main, #c62828)" }}
                  >
                    {run.error}
                  </span>
                )}
              </li>
            ))}
          </ul>
          {suiteRuns.length === 0 && <ListEmpty>No suite runs yet.</ListEmpty>}
        </>
      )}

      {creating === "schedule" && (
        <ScheduleOverlay
          repoOptions={repoOptions}
          tasks={tasks}
          templates={schedulableTemplates}
          suites={suites}
          config={config}
          onClose={() => setCreating(null)}
          onSaved={onRefreshSchedules}
          showError={showError}
        />
      )}
      {creating === "template" && (
        <TemplateOverlay
          repoOptions={repoOptions}
          tasks={tasks}
          config={config}
          onClose={() => setCreating(null)}
          onSaved={onRefreshTemplates}
          showError={showError}
        />
      )}
      {creating === "suite" && (
        <SuiteOverlay
          templates={schedulableTemplates}
          repoOptions={repoOptions}
          config={config}
          tasks={tasks}
          onClose={() => setCreating(null)}
          onSaved={onRefreshSuites}
          onTemplatesChanged={onRefreshTemplates}
          showError={showError}
        />
      )}
      {editingSchedule && (
        <ScheduleOverlay
          schedule={editingSchedule}
          repoOptions={repoOptions}
          tasks={tasks}
          templates={schedulableTemplates}
          suites={suites}
          config={config}
          onClose={() => onOpenSchedule(null)}
          onOpenTask={onOpenTask}
          onSaved={() =>
            Promise.all([
              onRefreshSchedules?.(),
              onRefreshRuns?.(),
              onRefreshTasks?.(),
            ])
          }
          showError={showError}
        />
      )}
      {editingTemplate && (
        <TemplateOverlay
          template={editingTemplate}
          repoOptions={repoOptions}
          tasks={tasks}
          config={config}
          onClose={() => onOpenTemplate(null)}
          onSaved={() =>
            Promise.all([onRefreshTemplates?.(), onRefreshSchedules?.()])
          }
          showError={showError}
        />
      )}
      {editingSuite && (
        <SuiteOverlay
          suite={editingSuite}
          templates={schedulableTemplates}
          repoOptions={repoOptions}
          config={config}
          tasks={tasks}
          onClose={() => onOpenSuite(null)}
          onSaved={() =>
            Promise.all([onRefreshSuites?.(), onRefreshSchedules?.()])
          }
          onTemplatesChanged={onRefreshTemplates}
          showError={showError}
        />
      )}
      {openRun && (
        <SuiteRunPane
          run={openRun}
          tasks={tasks}
          config={config}
          onClose={() => onOpenRun(null)}
          onOpenTask={onOpenTask}
          onOpenSchedule={openScheduleFromRun}
        />
      )}
      {using && (
        <UseTemplateOverlay
          templates={templates}
          templateId={using}
          tasks={tasks}
          config={config}
          onClose={() => setUsing(null)}
          onCreated={onRefreshTasks}
          onOpenTask={onOpenTask}
          showError={showError}
        />
      )}
      {running && (
        <SuiteRunOverlay
          // In the order the table lists them, so the bare "+ Run"
          // preselects the suite the page leads with.
          suites={sorted.filter((r) => r.kind === "suite").map((r) => r.item)}
          suiteId={running === true ? undefined : running}
          repoOptions={repoOptions}
          config={config}
          onClose={() => setRunning(null)}
          onStarted={() => Promise.all([onRefreshRuns(), onRefreshTasks()])}
          showError={showError}
        />
      )}
    </main>
  );
}

// ProcessRow is one row of the table: the shared columns of §5.1, with
// what only one kind has -- a schedule's cadence and what it fires, a
// suite's mode -- as chips under the name, where the old lists drew
// them.
function ProcessRow({
  row: r,
  handle,
  dragging,
  zone,
  onOpen,
  onOpenTask,
  onUse,
  onRun,
}) {
  const kind = processKind(r.kind);
  const s = r.item;
  const isSchedule = r.kind === "schedule" || r.kind === "workflow";
  const when = (iso) => formatDateTime(iso, zone);
  const href = processPath(r.kind, r.id);
  return (
    <div
      // process-row-<segment> names the kind the way the path does, so a
      // workflow schedule is a process-row-schedule too.
      className={`process-row process-row-${PROCESS_SEGMENTS[r.kind].replace(/s$/, "")}${dragging ? " task-row-dragging" : ""}`}
      data-kind={r.kind}
      onClick={(e) => {
        focusRowLink(e);
        onOpen();
      }}
    >
      <span className="process-handle">{handle}</span>
      <span className="process-kind">
        <Chip
          size="small"
          variant="outlined"
          icon={<ItemGlyph kind={kind.glyph} />}
          label={kind.chip}
        />
      </span>
      <span className="process-name">
        <RowLink
          className="schedule-title template-name"
          href={href}
          onOpen={onOpen}
        >
          {r.name}
        </RowLink>
        <span className="chips">
          {/* A trigger too (§5.1: every row with a declaredIn): it is
            only ever declared, and the chip's title is the one place the
            row names the repository and the file it came from. */}
          <DeclaredChip declaredIn={r.declaredIn} />
          {r.system && (
            <Chip
              size="small"
              variant="outlined"
              label="🔒 System"
              title={
                r.kind === "template"
                  ? "One of grain's own built-in templates: it can be filed from, not edited or deleted."
                  : "One of grain's own standing schedules: it can be paused, not edited or deleted."
              }
            />
          )}
          {isSchedule && (
            <Chip size="small" label={describeRecurrence(s.recurrence)} />
          )}
          {isSchedule && s.suiteId && (
            <Chip
              size="small"
              variant="outlined"
              icon={<ItemGlyph kind="suites" />}
              label={`Suite: ${s.suiteName || s.suiteId}`}
            />
          )}
          {isSchedule && s.templateId && (
            <Chip
              size="small"
              variant="outlined"
              icon={<ItemGlyph kind="templates" />}
              label={`Template: ${s.templateName || s.templateId}`}
            />
          )}
          {isSchedule && s.overlap === "allow" && (
            <Chip size="small" variant="outlined" label="Can overlap" />
          )}
          {isSchedule && s.workflowReview === "always" && (
            <Chip size="small" variant="outlined" label="Reviews every run" />
          )}
          {isSchedule && s.suppressed && (
            <Chip
              size="small"
              color="warning"
              variant="outlined"
              label="Not firing"
            />
          )}
          {r.kind === "template" && s.title && (
            <span className="template-title hint">{s.title}</span>
          )}
          {r.kind === "suite" && (
            <>
              <Chip size="small" label={describeMode(s)} />
              <Chip
                size="small"
                variant="outlined"
                icon={<ItemGlyph kind="templates" />}
                label={`${s.items.length} template${s.items.length === 1 ? "" : "s"}`}
              />
              {s.requireApproval && (
                <Chip size="small" label="Requires approval" />
              )}
            </>
          )}
        </span>
        {isSchedule && s.suppressed && (
          <span className="schedule-overdue hint">
            not firing: {describeSuppression(s.suppressed)}
            {s.suppressed.taskId && (
              <>
                {", "}
                <a
                  href={`/tasks/${s.suppressed.taskId}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    if (
                      onOpenTask &&
                      e.button === 0 &&
                      !e.metaKey &&
                      !e.ctrlKey &&
                      !e.shiftKey &&
                      !e.altKey
                    ) {
                      e.preventDefault();
                      onOpenTask(s.suppressed.taskId);
                    }
                  }}
                >
                  task {s.suppressed.taskId}
                </a>
              </>
            )}
          </span>
        )}
        {isSchedule && !s.suppressed && s.skips?.length > 0 && (
          <span className="schedule-last-skip hint">
            last {describeSkip(s.skips[0], when)}
          </span>
        )}
      </span>
      <span className="process-repo">{r.repo || "—"}</span>
      <span className="process-last hint">
        {r.kind === "workflow" && r.lastWorkflowRun ? (
          <LastWorkflowRun run={r.lastWorkflowRun} zone={zone} />
        ) : r.lastRunAt ? (
          when(r.lastRunAt)
        ) : (
          "—"
        )}
      </span>
      <span className="process-next hint">
        {r.nextLabel ? (
          r.nextLabel
        ) : isSchedule && !r.enabled ? (
          // A paused schedule does not fire at its next run; it keeps the
          // time, and resuming fires it then, or at once if that has gone
          // by -- so "Next run" alone would promise a firing that is not
          // coming.
          new Date(r.nextRunAt).getTime() <= Date.now() ? (
            `Held -- fires as soon as it is resumed (was due ${when(r.nextRunAt)})`
          ) : (
            `Held -- fires ${when(r.nextRunAt)} if resumed by then`
          )
        ) : (
          <span
            className={
              isSchedule && isOverdue(r.nextRunAt) ? "schedule-overdue" : null
            }
          >
            {when(r.nextRunAt)}
          </span>
        )}
      </span>
      <span className="process-enabled">
        {r.enabled === null ? (
          <span className="hint">—</span>
        ) : r.enabled ? (
          <span className="hint">Enabled</span>
        ) : (
          <Chip size="small" color="error" label="Paused" />
        )}
      </span>
      <span className="process-actions">
        {r.kind === "template" && (
          <Button
            size="small"
            onClick={(e) => {
              e.stopPropagation();
              onUse();
            }}
          >
            Use…
          </Button>
        )}
        {r.kind === "suite" && (
          <Button
            size="small"
            onClick={(e) => {
              e.stopPropagation();
              onRun();
            }}
          >
            Run…
          </Button>
        )}
      </span>
    </div>
  );
}
