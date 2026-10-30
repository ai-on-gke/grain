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

import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { Button, Checkbox, Chip, FormControlLabel } from "@mui/material";
import { heldForRunNow } from "../alerts.js";
import CampaignIcon from "@mui/icons-material/Campaign";
import api from "../api.js";
import GrantChip from "./GrantChip.jsx";
import {
  STATE_LABELS,
  statesOf,
  activityTitle,
  questionActivity,
  completionPhase,
  isAgentRunning,
  isTaskMerged,
  isTaskRepairing,
  mergeQueueStall,
  reasonControl,
  reasonLabel,
  reasonTitle,
  runActivity,
  stackedChip,
  stateLabel,
  stateTitle,
  taskBadgeModifier,
  taskBadgeState,
  taskMergedAt,
} from "../state.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";
import { attributesFor } from "../taskAttributes.js";
import { CustomFieldChips, rowFields } from "./CustomFields.jsx";
import {
  defaultFilters,
  isSessionTask,
  isNarrowed,
  matchesFilters,
  resolveConditions,
  sortTasks,
} from "../taskFilters.js";
import {
  focusRowLink,
  ListEmpty,
  ListHeader,
  RowLink,
} from "./ListPrimitives.jsx";
import NarrowingBar from "./NarrowingBar.jsx";
import {
  MoveHandle,
  MoveSlot,
  MovingBar,
  gapsOf,
  usePickup,
} from "./ReorderPickup.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import NotifyRunningDialog from "./NotifyRunningDialog.jsx";
import StateDot from "./StateDot.jsx";

const FILTER_TITLES = {
  all: "All tasks",
  blocked: "Blocked",
  merged: "Merged",
};

// A stacked task -- the merge queue's own automatic fix for another
// task's pull request (bwsalmon/agents#378) -- is not new work of its
// own, so it is nested under the task named by generatedFrom instead of
// listed as a separate row, as long as that task also passes the
// current filter. One that doesn't (its parent fell out of the filtered
// view, or the parent it names is gone) falls back to a plain row of
// its own, so it is never silently dropped from the list.
function groupByStack(tasks, matches) {
  const topLevel = [];
  const orphans = [];
  for (const t of tasks) {
    if (!matches(t)) continue;
    if (t.stacked && t.generatedFrom) orphans.push(t);
    else topLevel.push(t);
  }
  const topLevelIds = new Set(topLevel.map((t) => t.id));
  const children = new Map();
  for (const c of orphans) {
    if (!topLevelIds.has(c.generatedFrom)) {
      topLevel.push(c);
      continue;
    }
    const list = children.get(c.generatedFrom) || [];
    list.push(c);
    children.set(c.generatedFrom, list);
  }
  return { topLevel, children };
}

// `landmark` false draws the view's root as a plain element rather than
// a <main>: a saved board's page (BoardPage.jsx) is the main landmark
// itself, so its heading is inside it, and a second main nested in the
// first would be one landmark too many (grain/task-1253).
export default function TaskList({
  tasks,
  stateFilter,
  config,
  narrowing,
  onNarrow,
  onOpenTask,
  selected,
  onToggleSelect,
  onSelectAll,
  onReorder,
  onShownChange,
  onRefresh,
  showError,
  landmark = true,
}) {
  const [notifyOpen, setNotifyOpen] = useState(false);
  // A row's own sub-case control (state.js's reasonControl), pressed from
  // the list: the request, then the list read again so the row says what
  // it did.
  const control = async (t, action) => {
    if (action.confirm && !confirm(action.confirm(t))) return;
    try {
      await api(`/api/tasks/${encodeURIComponent(t.id)}${action.post}`, {
        method: "POST",
      });
      await onRefresh?.();
    } catch (err) {
      showError?.(err);
    }
  };
  const runningTasks = useMemo(
    () => tasks.filter((t) => t.state === "running" && !t.consoleSession),
    [tasks],
  );
  // The search text, the sort order and the attribute filters are all
  // App's now, and the URL's (grain/task-317): they used to be local
  // state here, on the grounds that a refinement of the list on screen
  // could reset the next time this component mounted, but a list
  // narrowed by half a dozen conditions at once is a view somebody wants to
  // come back to and to send to someone else -- so it lives in the
  // query string, where every other piece of navigable state already
  // lives (paths.js), and survives switching views and back the way
  // stateFilter does.
  //
  // narrowing.filters is a list of conditions and narrowing.sort a list
  // of sort keys, both of them edited by the shared toolbar below
  // (NarrowingBar.jsx) rather than by this file. onNarrow takes a patch
  // of the narrowing, so a control only says what it changes.
  const { search, sort, filters } = narrowing;

  const q = search.trim().toLowerCase();

  // inState is what this list is *about* before any of the toolbar's own
  // refinements: the sidebar's question, and nothing else. It is
  // separated out because the toolbar's filter menus are built from the
  // tasks that pass it -- a menu should not offer a repo whose every
  // task the current state filter is already hiding, and it *should* go
  // on offering "Closed" while the default condition is leaving closed
  // tasks out, since taking that condition off is the point of the menu.
  //
  // Closed and deferred tasks used to be taken out here too, under two
  // checkboxes of their own. They are an ordinary condition in the
  // narrowing now (taskFilters.js's defaultFilters), so this is one
  // question again rather than two asked in different places.
  const inState = (t) => {
    if (stateFilter === "blocked") return !!t.blocked;
    if (stateFilter === "merged") return isTaskMerged(t);
    if (stateFilter === "closed")
      return t.state === "closed" && !isTaskMerged(t);
    // "asked" is both of state.js's ASKED_STATES; a link naming either
    // one alone still opens just that one.
    return stateFilter === "all" || statesOf(stateFilter).includes(t.state);
  };
  const inStateTasks = tasks.filter(inState);

  // The attributes this deployment can be asked about, and the
  // conditions currently set over them (taskFilters.js). The toolbar's
  // own menus are built from the tasks the state filter admits, so a
  // menu never offers a value that would come back empty.
  const attributes = attributesFor(config);
  const conditions = resolveConditions(filters, attributes);

  // A session's own task is always left off (taskFilters.js's
  // isSessionTask): the rail does not count it under "Asked",
  // so the list that entry opens does not show it either.
  const matches = (t) =>
    !isSessionTask(t) && inState(t) && matchesFilters(t, conditions, q, config);
  // What an empty list owes the reader: "there is nothing here" and
  // "there is nothing here that you are looking at" are different
  // answers, and the second one is only true once the list is narrowed
  // by more than it opens narrowed by (taskFilters.js's isNarrowed).
  // What the default condition is leaving out is on screen either way,
  // as the chip that says so.
  //
  // A state in the rail takes the State condition out of the default
  // (App.jsx's listDefault and selectStateFilter), so Clear and the
  // empty state measure against that too.
  const defaults = defaultFilters(config).filter(
    (c) =>
      stateFilter === "all" || stateFilter === "blocked" || c.attr !== "state",
  );
  const narrowed = isNarrowed(narrowing, defaults);

  const reorderEnabled = !!onReorder && sort.length === 0;
  const movable = () => reorderEnabled;
  const sortedTasks = sortTasks(tasks, sort, attributes, config);

  // The custom fields these rows carry the values of: the ones this sort
  // has put the list in the order of, and the ones the deployment says
  // belong on a row (CustomFields.jsx's rowFields). Worked out once for
  // the list rather than per row, since it is the same answer for every
  // row of it -- and passed down, so a nested stacked task shows what
  // the row above it shows.
  const fields = rowFields(config, sort);
  const byId = useMemo(
    () => new Map((tasks || []).map((t) => [t.id, t])),
    [tasks],
  );

  const { topLevel, children } = groupByStack(sortedTasks, matches);
  const visibleIds = topLevel.flatMap((t) => [
    t.id,
    ...(children.get(t.id) || []).map((c) => c.id),
  ]);
  const allSelected =
    visibleIds.length > 0 && visibleIds.every((id) => selected.has(id));
  const lastSelectedRef = useRef(null);
  const handleToggleSelect = onToggleSelect
    ? (id, opts) => {
        if (opts?.shiftKey && onSelectAll && lastSelectedRef.current !== null) {
          const anchorIdx = visibleIds.indexOf(lastSelectedRef.current);
          const targetIdx = visibleIds.indexOf(id);
          if (anchorIdx !== -1 && targetIdx !== -1) {
            const [start, end] =
              anchorIdx <= targetIdx
                ? [anchorIdx, targetIdx]
                : [targetIdx, anchorIdx];
            const rangeIds = visibleIds.slice(start, end + 1);
            const nextChecked =
              typeof opts.checked === "boolean"
                ? opts.checked
                : !selected.has(id);
            lastSelectedRef.current = id;
            onSelectAll(rangeIds, nextChecked);
            return;
          }
        }
        lastSelectedRef.current = id;
        onToggleSelect(id);
      }
    : undefined;
  // The rows on screen, for the batch bar to tell a selection it can see
  // from one it cannot (BatchActionsBar's shownIds). Keyed on the ids
  // themselves rather than the array, which is a fresh one every render.
  const shownKey = visibleIds.join(",");
  useEffect(() => {
    onShownChange?.(new Set(visibleIds));
  }, [shownKey, onShownChange]);

  const title =
    FILTER_TITLES[stateFilter] || STATE_LABELS[stateFilter] || stateFilter;

  // dragIds is every id being dragged right now, in their own current
  // backlog order -- a single row not part of the current selection
  // drags alone; a row that is part of a multi-selection drags the whole
  // selection as a block (bwsalmon/agents#476). null means nothing is
  // being dragged. overId is purely the drop-target highlight and never
  // drives the actual move -- that only happens in onDrop.
  const [dragIds, setDragIds] = useState(null);
  const [overId, setOverId] = useState(null);

  // The same reorder for a finger or a keyboard: tap the handle to pick
  // a row up, then tap one of the slots that appear between the rows
  // (ReorderPickup.jsx). The drag below is untouched -- this is the
  // gesture for the devices that never fire a drag event at all.
  const { picked, toggle: togglePickup, cancel: cancelPickup } = usePickup();
  // A pick-up made under "Backlog order" means nothing once the list is
  // showing some other order, or once narrowing hides the picked-up row,
  // for the same reason the drag is withdrawn there: the slots would be
  // offering positions in an order the screen is not showing.
  const topLevelIds = new Set(topLevel.map((t) => t.id));
  const pickedPresent =
    reorderEnabled &&
    Boolean(picked?.ids?.length) &&
    picked.ids.every((id) => topLevelIds.has(id));
  useEffect(() => {
    if (picked && !pickedPresent) cancelPickup();
  }, [picked, pickedPresent, cancelPickup]);
  const pickedIds = pickedPresent ? picked.ids : null;

  // The rows a pick-up can land between, and the gaps between them worth
  // offering (gapsOf drops the one the block already sits in).
  const kept = pickedIds
    ? topLevel.filter((t) => !pickedIds.includes(t.id))
    : topLevel;
  const gapBefore = new Map(kept.map((t, i) => [t.id, i]));
  const gaps = pickedIds
    ? new Set(
        gapsOf(
          topLevel.map((t) => t.id),
          pickedIds,
        ),
      )
    : null;

  // moveTo is where both gestures end up: `gap` is a position among the
  // tasks currently visible under this filter, and its two neighbours
  // are what onReorder is told. The store places the moved tasks between
  // whatever those two names resolve to in the *full*, unfiltered
  // backlog, which is what makes a move inside a filtered view still
  // land correctly relative to tasks the filter is hiding: a move to the
  // very top of a filtered view has no preceding job, so it goes just
  // before the following one instead -- the same rule the issue itself
  // asks for.
  const moveTo = (ids, rows, gap) =>
    onReorder(
      ids,
      gap > 0 ? rows[gap - 1].id : null,
      gap < rows.length ? rows[gap].id : null,
    );

  // dropOn resolves a drop at targetId (or at the very end, when
  // targetId is null) to that same gap among the visible tasks.
  const dropOn = (targetId) => {
    if (!dragIds) return;
    const dragging = new Set(dragIds);
    const visible = topLevel.filter((t) => !dragging.has(t.id));
    const idx =
      targetId === null
        ? visible.length
        : visible.findIndex((t) => t.id === targetId);
    moveTo(dragIds, visible, idx);
    setDragIds(null);
    setOverId(null);
  };

  // blockFor is what grabbing this row moves -- itself, or the whole
  // current selection when the row is part of one (bwsalmon/agents#476)
  // -- in their own current backlog order. Both gestures start here, so
  // a tap picks up exactly what a drag would have taken.
  const blockFor = (t) => {
    const ids =
      selected.has(t.id) && selected.size > 1 ? selected : new Set([t.id]);
    return topLevel.filter((x) => ids.has(x.id)).map((x) => x.id);
  };

  const startDrag = (t) => setDragIds(blockFor(t));

  const Root = landmark ? "main" : "div";
  return (
    <Root className={landmark ? undefined : "task-view"}>
      {/* The tasks figure at heading size (ItemGlyph.jsx), the same one
          the rail's All tasks entry carries. It heads every heading this
          page takes -- "All tasks", one state's name, "Blocked" --
          because all of them are this same list narrowed, and the
          figure says what the rows are rather than which slice of them
          is showing. */}
      <ListHeader
        title={title}
        count={visibleIds.length}
        icon={<ItemGlyph kind="tasks" size={20} />}
      />
      {tasks.length > 0 && (
        <NarrowingBar
          tasks={inStateTasks}
          config={config}
          narrowing={narrowing}
          onNarrow={onNarrow}
          defaults={defaults}
        >
          <Button
            size="small"
            startIcon={<CampaignIcon />}
            title="Send a notice to all currently running task agents"
            onClick={() => setNotifyOpen(true)}
          >
            {runningTasks.length > 0
              ? `Contact running (${runningTasks.length})`
              : "Contact running"}
          </Button>
        </NarrowingBar>
      )}
      <NotifyRunningDialog
        open={notifyOpen}
        onClose={() => setNotifyOpen(false)}
        runningTasks={runningTasks}
        onSent={onRefresh}
        showError={showError}
      />
      {visibleIds.length > 0 && (
        <div className="select-all">
          <FormControlLabel
            control={
              <Checkbox
                size="small"
                checked={allSelected}
                onChange={(e) => onSelectAll(visibleIds, e.target.checked)}
              />
            }
            label="Select all"
          />
        </div>
      )}
      {pickedIds && (
        <MovingBar
          count={pickedIds.length}
          nowhere={gaps.size === 0}
          onCancel={cancelPickup}
        />
      )}
      <ul className="task-list">
        {topLevel.map((t) => (
          <Fragment key={t.id}>
            {/* The place this row's own position offers, drawn above it
                -- so the slots read as the gaps between the rows, which
                is what they are. A row that is itself picked up gets
                none: its neighbours' slots already name every position
                it could take. */}
            {pickedIds &&
              gapBefore.has(t.id) &&
              gaps.has(gapBefore.get(t.id)) && (
                <MoveSlot
                  className="reorder-slot-gutter"
                  label={`Move here, above ${t.title}`}
                  onMove={() => {
                    moveTo(pickedIds, kept, gapBefore.get(t.id));
                    cancelPickup();
                  }}
                />
              )}
            <li
              className={
                overId === t.id && dragIds && !dragIds.includes(t.id)
                  ? "task-drop-target"
                  : undefined
              }
              draggable={movable(t)}
              onDragStart={() => movable(t) && startDrag(t)}
              onDragEnd={() => {
                setDragIds(null);
                setOverId(null);
              }}
              onDragOver={(e) => {
                if (!dragIds || dragIds.includes(t.id)) return;
                e.preventDefault();
                setOverId(t.id);
              }}
              onDrop={(e) => {
                e.preventDefault();
                dropOn(t.id);
              }}
            >
              <TaskRow
                t={t}
                byId={byId}
                config={config}
                fields={fields}
                onOpenTask={onOpenTask}
                onControl={control}
                selected={selected}
                onToggleSelect={handleToggleSelect}
                draggable={movable(t)}
                // Space for the handle a read-only row does not get, so
                // its title lines up with the movable rows around it.
                dragPlaceholder={reorderEnabled && !movable(t)}
                dragging={dragIds?.includes(t.id) ?? false}
                picked={pickedIds?.includes(t.id) ?? false}
                // A list of one has nowhere to move its own row to.
                canMove={topLevel.length > 1}
                onPickUp={() => togglePickup(blockFor(t))}
                showMergedAt={stateFilter === "merged"}
              />
              {children.has(t.id) && (
                <ul className="task-sublist">
                  {children.get(t.id).map((c) => (
                    <li key={c.id}>
                      <TaskRow
                        t={c}
                        byId={byId}
                        config={config}
                        fields={fields}
                        onOpenTask={onOpenTask}
                        onControl={control}
                        selected={selected}
                        onToggleSelect={handleToggleSelect}
                        dragPlaceholder={reorderEnabled}
                        nested
                        showMergedAt={stateFilter === "merged"}
                      />
                    </li>
                  ))}
                </ul>
              )}
            </li>
          </Fragment>
        ))}
        {pickedIds && gaps.has(kept.length) && (
          <MoveSlot
            className="reorder-slot-gutter"
            label="Move here, to the end of the list"
            onMove={() => {
              moveTo(pickedIds, kept, kept.length);
              cancelPickup();
            }}
          />
        )}
        {reorderEnabled && dragIds && (
          <li
            className={`task-drop-end${overId === "__end__" ? " task-drop-target" : ""}`}
            onDragOver={(e) => {
              e.preventDefault();
              setOverId("__end__");
            }}
            onDrop={(e) => {
              e.preventDefault();
              dropOn(null);
            }}
          />
        )}
      </ul>
      {topLevel.length === 0 && (
        <ListEmpty>
          {q
            ? "No tasks match your search."
            : narrowed
              ? "No tasks match these filters."
              : "No tasks in this state."}
        </ListEmpty>
      )}
    </Root>
  );
}

// TaskRow is exported so any other view that lists tasks can render a
// row identical to this one instead of re-deriving the badge and chip
// markup. Selection and dragging are both opt-in: a caller with nowhere
// to put a batch-actions bar or a reorderable list just omits
// onToggleSelect/draggable and gets a plain row with no checkbox or drag
// handle rather than a dead one.
//
// `draggable` brings the handle, and with it both ways of moving the row
// -- the drag the <li> around this row carries, and the tap-to-move the
// handle itself is a button for (ReorderPickup.jsx). A caller passing
// `draggable` passes `onPickUp` and `picked` with it, or the handle
// still drags but no longer picks anything up.
//
// nested says this row is already sitting in a .task-sublist under the
// task it was generated from, which is the only thing that makes a
// stacked task self-explaining -- so it is what decides whether the
// "merge fix"/"review" chip below is worth its space. Callers that list tasks
// flat (groupByStack's own fallback for a stacked task whose parent is
// filtered out or gone) leave it off and get the chip.
//
// A row carries no "show the prompt" button of its own any more: the
// whole prompt an agent was handed is a detail about one task, so it is
// reached from that task's own page (DetailOverlay's Prompt button)
// rather than from every row of every list a task can appear in.
//
// fields is which of this deployment's own declared fields this row
// shows the value of (CustomFields.jsx's rowFields), worked out by the
// list, since it depends on the sort the list is applying. A caller with
// no sort to speak of -- the inbox, a panel -- may leave it off and gets
// the fields the deployment flagged for every row, which is the same
// answer rowFields would give an empty sort.
//
// dragPlaceholder is for a row with no handle of its own in a list where
// the other rows have one -- a stacked merge fix or review, neither of
// which is ever reordered, since both are filed ahead of the backlog.
// Without it that row's badge, number and title would each sit a handle's
// width left of every other row's, so the column the handle occupies is
// held open and empty instead.
export function TaskRow({
  t,
  byId,
  config,
  fields,
  onOpenTask,
  selected,
  onToggleSelect,
  draggable,
  dragging,
  picked,
  canMove = true,
  onPickUp,
  dragPlaceholder,
  nested,
  onControl,
  // The Merged panel's rows say when each one merged: it is what that
  // list is sorted by and what somebody scanning it is looking for.
  showMergedAt = false,
  // The inbox shows a parked question's summary under the row itself, so
  // its rows leave it off their status line rather than say it twice.
  hideQuestion = false,
}) {
  const zone = useTimeZone();
  const mergedAt = showMergedAt ? taskMergedAt(t) : null;
  const phase = completionPhase(t);
  // What the run itself says it is doing, for as long as it is running
  // (state.js's runActivity). It sits between the title and the chips
  // rather than in the chip row: it is a sentence somebody wrote, not a
  // label off a fixed vocabulary, and it is the one thing on this row
  // that changes while you watch it. A queued task waiting on a grant or
  // connection stays simply queued on the row and puts the wait reason
  // on the state icon's hover and in the detail pane instead.
  const rawActivity = runActivity(t);
  // A task parked on a question says what it asked, as much of the
  // question's one-line summary as the row has room for -- except where
  // the row's own page already shows that line under it (the inbox).
  const activity =
    t.state === "queued" && !rawActivity?.byQueue
      ? null
      : rawActivity || (hideQuestion ? null : questionActivity(t));
  const agentRunning = isAgentRunning(t);
  const badgeState = taskBadgeState(t);
  // grain's own verdict that this row's merge-queue wait has stopped
  // being one (state.js's mergeQueueStall). It goes beside the phase
  // chip because it is the same kind of correction: the badge and the
  // activity line both read as a change on its way, and this is the
  // thing that says otherwise on the row itself.
  const stall = mergeQueueStall(t);
  const stalledControl =
    onControl && (t.stall || stall) ? reasonControl(t) : null;
  const shownFields = fields || rowFields(config);
  const shiftClickRef = useRef(false);
  return (
    <div
      className={`task-row${dragging ? " task-row-dragging" : ""}${picked ? " task-row-picked" : ""}`}
      onClick={(e) => {
        focusRowLink(e);
        onOpenTask(t.id);
      }}
    >
      {draggable ? (
        <MoveHandle
          label={picked ? `Stop moving task ${t.id}` : `Move task ${t.id}`}
          picked={!!picked}
          disabled={!canMove}
          onToggle={() => onPickUp?.()}
        />
      ) : (
        dragPlaceholder && (
          <span className="task-drag-placeholder" aria-hidden="true" />
        )
      )}
      {onToggleSelect && (
        <Checkbox
          size="small"
          className="task-select"
          checked={selected.has(t.id)}
          onClick={(e) => {
            e.stopPropagation();
            shiftClickRef.current = Boolean(e.shiftKey);
          }}
          onChange={(e) => {
            const shiftKey = Boolean(
              e?.nativeEvent?.shiftKey || e?.shiftKey || shiftClickRef.current,
            );
            shiftClickRef.current = false;
            if (shiftKey) {
              onToggleSelect(t.id, {
                shiftKey: true,
                checked: e.target.checked,
              });
            } else {
              onToggleSelect(t.id);
            }
          }}
          inputProps={{ "aria-label": `Select ${t.id}` }}
        />
      )}
      {(() => {
        const modifier = taskBadgeModifier(t);
        const repairing = isTaskRepairing(t);
        // The badge is the only place the row says what state the task
        // is in, and a coloured dot or a moving mark says nothing to a
        // screen reader: name it, so a flat list of mixed states (a suite
        // run's pane) reads "Running", "Failed" rather than nothing at
        // all (grain/task-1583). role="img" makes the mark inside it
        // presentational, so the state is announced once.
        return (
          <span
            className={`badge badge-icon badge-${badgeState}${agentRunning ? " badge-mark" : ""}${modifier ? ` ${modifier}` : ""}`}
            title={stateTitle(t, byId)}
            role="img"
            aria-label={
              badgeState === "blocked" ? stateTitle(t, byId) : stateLabel(t)
            }
          >
            <StateDot
              state={badgeState}
              title={stateTitle(t, byId)}
              repairing={repairing}
              agentRunning={agentRunning}
            />
          </span>
        );
      })()}
      <span className="task-number">{t.id}</span>
      {/* The title is the link that opens this task from the keyboard
          (ListPrimitives.jsx's RowLink): the row around it holds a move
          handle and a checkbox, so it cannot be a link or a button
          itself -- one interactive role nested in another is announced
          as a single run-on control, and the ones inside it can go
          unreached (grain/task-1582). The row keeps a pointer click. */}
      <RowLink
        className="task-title"
        href={`/tasks/${encodeURIComponent(t.id)}`}
        onOpen={() => onOpenTask(t.id)}
        aria-label={t.title ? undefined : `Task ${t.id}`}
      >
        {t.title}
      </RowLink>
      {activity && (
        <span className="task-activity" title={activityTitle(activity)}>
          {/* Whose sentence this is, said out loud rather than left to
              the phrasing: every status on this row until now was an
              agent's own, and grain narrating a run's setup is a
              different voice (state.js's runActivity). */}
          {activity.bySetup && <span className="task-activity-by">grain</span>}
          <span className="task-activity-note">{activity.note}</span>
          {/* Which of the merge-queue state's ways of waiting this is
              (Task.MergeQueueReason); the hover says what drives it. */}
          {activity.reason && (
            <span className="task-activity-reason">
              {reasonLabel(activity.reason)}
            </span>
          )}
          {activity.age && (
            <span className="task-activity-age">{activity.age}</span>
          )}
        </span>
      )}
      {mergedAt && (
        <span className="task-merged-at" title="when its pull request merged">
          merged{" "}
          <time dateTime={mergedAt}>{formatDateTime(mergedAt, zone)}</time>
        </span>
      )}
      <span className="chips">
        {/* What this deployment's own fields say about the task, first
            in the row: on a list sorted by one of them it is the answer
            the order was made of, and it is what somebody scanning a
            backlog for "the high-priority ones" is reading past
            everything else to find. */}
        <CustomFieldChips fields={shownFields} values={t.customFields} />
        {/* The two chips that say where a task came from carry the
            figure of the thing it came from (ItemGlyph.jsx) -- the same
            hourglass and lobes the nav rail shows for Schedules and
            Suites, so a row filed by a schedule is recognisable in a
            list of forty without reading the word. Decorative: the chip
            says "scheduled" beside it either way. */}
        {t.scheduled && (
          <Chip
            size="small"
            className="chip-scheduled"
            icon={<ItemGlyph kind="schedules" />}
            title="filed automatically by a schedule"
            label="scheduled"
          />
        )}
        {t.suiteRun && (
          <Chip
            size="small"
            className="chip-suite"
            icon={<ItemGlyph kind="suites" />}
            title="filed automatically by a suite run"
            label="suite"
          />
        )}
        {t.imported && (
          <Chip
            size="small"
            className="chip-imported"
            title={
              t.origin
                ? `imported from GitHub issue ${t.origin}`
                : "imported from a GitHub issue"
            }
            label="imported"
          />
        )}
        {t.stacked && !nested && (
          <Chip size="small" className="chip-stacked" {...stackedChip(t)} />
        )}
        {/* Why this task is closed, on the row rather than three clicks
            away: a design refined five times leaves four closed tasks
            behind it (model.LinkSupersededBy, grain/task-473), and
            without this the only way to tell them from work somebody
            abandoned is to open each one and read it. The successor's
            number is the whole answer, so it is the label. */}
        {t.supersededBy && (
          <Chip
            size="small"
            variant="outlined"
            title={`closed as replaced by task ${t.supersededBy}, which was filed to supersede it`}
            label={`superseded by ${t.supersededBy}`}
          />
        )}
        {/* A proposal its agent could have queued on its own permission
            and asked to wait for a person instead (propose_task's
            hold_for_approval, ui.Task.HeldBy). Said while it still waits,
            so whoever is approving can tell a deliberate hold from a
            proposal the permission simply did not cover. */}
        {t.heldBy && !t.approved && (
          <Chip
            size="small"
            variant="outlined"
            className="chip-held"
            title={`task ${t.heldBy} could have queued this on its own permission and asked for a person to approve it first`}
            label={`held for approval by ${t.heldBy}`}
          />
        )}
        {/* Manual mode: the deployment is paused, and this task would
            otherwise be picked up -- it starts when somebody presses Run
            now on it (alerts.js's heldForRunNow). */}
        {heldForRunNow(config?.pipelinePause, t) && (
          <Chip
            size="small"
            variant="outlined"
            className="chip-held-for-run-now"
            title="The deployment is paused: this task starts only when somebody presses Run now on it"
            label="paused — waits for Run now"
          />
        )}
        {t.repo && <Chip size="small" label={t.repo} />}
        {t.base && t.base !== "main" && (
          <Chip size="small" variant="outlined" label={t.base} />
        )}
        {(t.reads || []).map((repo) => (
          <Chip
            key={repo}
            size="small"
            variant="outlined"
            title="read-only"
            label={`${repo} (read)`}
          />
        ))}
        {/* The other repos this task's work may land in (Task.Writes,
            grain/task-54), beside the read-only ones and said the same
            way. The chip above names the one repo this task's own runs
            push to, and these are the rest of the answer to "where can
            this land" -- which a row never said until now. It is also
            what keeps a repo's own page honest: that page lists the
            tasks that only write to this repo alongside the ones filed
            against it (repoTasks, state.js, grain/task-116), and without
            this chip a row whose repo chip names some other repo would
            be there with nothing to explain why. The branch
            each target goes on is in the tooltip rather than the label,
            for the reason the "writes" filter is over repo names alone
            (taskAttributes.js): the repo is the question anybody asks. */}
        {(t.writes || [])
          .filter((w) => w?.repo)
          .map((w, i) => (
            <Chip
              key={`${w.repo}:${w.base || ""}:${i}`}
              size="small"
              variant="outlined"
              title={`may also land work in ${w.repo}${w.base ? ` on ${w.base}` : ""}`}
              label={`${w.repo} (write)`}
            />
          ))}
        {t.capabilities.map((id) => (
          <GrantChip key={id} grant={id} config={config} />
        ))}
      </span>
      {stall && (
        <Chip
          size="small"
          color={stall.color}
          title={stall.title}
          label={stall.label}
        />
      )}
      {phase && (
        <Chip
          size="small"
          color={phase.color}
          title={phase.title}
          label={phase.label}
        />
      )}
      {/* A state that has stood for longer than it should take
          (ui.Task.Stall): queued with nothing dispatching it, a run no
          process is finishing, a change the merge queue is not landing.
          Like the two chips above it this is a correction to the badge
          rather than a replacement for it -- the task really is queued
          -- and the sentence saying which component ought to have ended
          the wait is in the tooltip, where a row has room for it.
          Warning rather than error: it is a diagnosis off timestamps,
          not a failure grain has recorded. */}
      {t.stall && (
        <Chip size="small" color="warning" title={t.stall} label="Stalled" />
      )}
      {/* The sub-case of the state the badge names (Task.Reason,
          orchestrator's taskReasonOf): which of that state's ways of
          waiting this is, with which of the three conditions, what it
          waits on and what ends it on hover. Outlined and last: it
          refines the badge rather than correcting it, unlike the chips
          above. A closed task has nothing left to wait on. */}
      {t.reason && t.state !== "closed" && (
        <Chip
          size="small"
          variant="outlined"
          className="task-reason"
          title={reasonTitle(t.reason)}
          label={reasonLabel(t.reason)}
        />
      )}
      {/* And the control that ends the wait, on a row grain has flagged
          as stuck: the escalation to a person is this button beside the
          sentence that says so (the inbox's stalled group offers the
          same one, and passes no onControl here so it is not offered
          twice). */}
      {stalledControl && (
        <Button
          size="small"
          variant="outlined"
          className="task-reason-control"
          color={stalledControl.color}
          title={stalledControl.title}
          onClick={(e) => {
            e.stopPropagation();
            onControl(t, stalledControl);
          }}
        >
          {stalledControl.label}
        </Button>
      )}
    </div>
  );
}
