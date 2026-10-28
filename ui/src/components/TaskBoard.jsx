import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { Button, Checkbox, Chip, Typography } from "@mui/material";
import GrantChip from "./GrantChip.jsx";
import {
  activityTitle,
  isBlockedWaiting,
  completionPhase,
  isAgentRunning,
  isTaskRepairing,
  mergeQueueStall,
  runActivity,
  stackedChip,
  stateLabel,
  stateLabels,
  stateTitle,
  taskBadgeModifier,
  taskBadgeState,
} from "../state.js";
import { attributesFor } from "../taskAttributes.js";
import {
  isSessionTask,
  isNarrowed,
  matchesFilters,
  resolveConditions,
  sortTasks,
} from "../taskFilters.js";
import {
  boardStates,
  groupIntoColumns,
  hiddenStates,
  loadColumns,
  saveColumns,
} from "../board.js";
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
import BoardColumnsOverlay from "./BoardColumnsOverlay.jsx";
import { CustomFieldChips, rowFields } from "./CustomFields.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import StateDot from "./StateDot.jsx";

// TaskBoard is the same backlog as TaskList, laid out as a Kanban board:
// one column per group of states, tasks as cards inside them
// (grain/task-287).
//
// It is a second *view*, not a second source of truth. The tasks, the
// search box, the sort orders and every attribute filter are the list's
// own (taskFilters.js), so narrowing to one repo means the same thing on
// both; what the board adds is that "which tasks show up" is also
// answered by the columns themselves, which are the operator's to
// arrange (board.js, BoardColumnsOverlay.jsx).
//
// # Why a card cannot be dragged into another column
//
// Because a task's state is not a field anybody sets. The daemon
// derives it from what has actually happened to the task -- whether a
// run is live, whether an attempt failed, whether a pull request has
// landed (model.StateOf, docs/data-model.md) -- so there is no write
// behind "drop this card in Running" for the UI to make. The gestures
// that really do move a task between these columns are the ones on the
// task itself (approve it, retry it, submit it, close it), which is why
// a card opens the task rather than pretending otherwise.
//
// Dragging *within* a column is real, and is the same edit the list's
// own drag makes: Store.Reorder on the backlog, in exactly the
// "between these two neighbours" terms TaskList uses, so a drag in a
// filtered column still lands correctly among the tasks it cannot see.
// Like the list, it is only offered under "Backlog order", since
// dragging a row while looking at "Newest first" would reorder
// something the screen is not showing.
//
// `landmark` false draws the view's root as a plain element rather than
// a <main>: a saved board's page (BoardPage.jsx) is the main landmark
// itself, so its heading is inside it, and a second main nested in the
// first would be one landmark too many (grain/task-1253).
export default function TaskBoard({
  tasks,
  config,
  narrowing,
  onNarrow,
  onOpenTask,
  selected,
  onToggleSelect,
  onSelectAll,
  onReorder,
  onShownChange,
  fixedColumns = null,
  landmark = true,
}) {
  // The columns this browser last saved, or the default board
  // (board.js). Held here rather than in App: nothing outside this view
  // means anything by them, and the board is mounted for as long as the
  // view is showing.
  //
  // fixedColumns overrides both, and is a saved board's own layout
  // (boards.js): a Kanban board somebody asked an agent for carries the
  // columns it was asked for, and those are a row in grain's database
  // rather than a preference this browser may rewrite. So the editor is
  // not offered while they are in force -- editing them is editing the
  // board, which is what the "Edit board" button on the page above is
  // for.
  const [ownColumns, setOwnColumns] = useState(() => loadColumns());
  const columns = fixedColumns || ownColumns;
  const [editing, setEditing] = useState(false);

  // The same three refinements as TaskList, held in the same place: App
  // owns them and the query string is where they live (paths.js,
  // grain/task-317). Shared with the list rather than kept per view for
  // the reason in this file's own header -- narrowing to one repo means
  // the same thing on both -- so moving between the list and the board
  // keeps the question you were asking, and only the layout changes.
  const { search, sort, filters } = narrowing;

  const q = search.trim().toLowerCase();

  const saveColumnLayout = (next) => {
    setOwnColumns(next);
    saveColumns(next);
  };

  // onBoard is what the board is about before the toolbar has its say:
  // the states the columns cover. It is the board's counterpart to the
  // list's state filter, and it is what the filter menus are built from,
  // so a board with no "Closed" column never offers a repo only closed
  // tasks carry.
  const covered = new Set(boardStates(columns));
  const onBoardTasks = tasks.filter((t) => covered.has(t.state));

  const attributes = attributesFor(config);
  const conditions = resolveConditions(filters, attributes);
  // A session's task is parked on its own conversation between
  // exchanges, so on a board it would be one permanent card per session
  // in whichever column holds awaiting_reply -- the default board's
  // "Asked". It is not work waiting on anybody; the session is in
  // the Sessions pane (SessionsPage.jsx), so boards always leave it out
  // (taskFilters.js's isSessionTask).
  const matches = (t) =>
    !isSessionTask(t) && matchesFilters(t, conditions, q, config);

  const { cards, hidden } = groupIntoColumns(
    sortTasks(tasks, sort, attributes, config),
    columns,
    matches,
  );
  const visibleIds = columns.flatMap((c) => cards.get(c.id).map((t) => t.id));
  const shown = visibleIds.length;
  const shownKey = visibleIds.join(",");
  useEffect(() => {
    onShownChange?.(new Set(visibleIds));
  }, [shownKey, onShownChange]);
  // The states behind `hidden` -- not every state no column names, only
  // the ones some task is actually in right now. A board with no column
  // for "Deferred" and no deferred tasks is not hiding anything of the
  // sort, and naming it here would send somebody into the column editor
  // after tasks that do not exist.
  const offBoard = hiddenStates(columns).filter((s) =>
    tasks.some((t) => t.state === s && matches(t)),
  );

  const reorderEnabled = !!onReorder && sort.length === 0;
  const movable = () => reorderEnabled;

  // The fields the cards carry the values of, by the same rule the list
  // uses (CustomFields.jsx's rowFields): a board sorted by `priority` is
  // in an order its columns do not explain any more than a list's rows
  // do, and a field flagged for the row is flagged for the card -- the
  // board is the same tasks, so a task reads the same either way.
  const fields = rowFields(config, sort);

  // dragIds is what is being dragged, dragColumn the column it was
  // picked up in -- a drop is only accepted back into that same column,
  // for the reason in this file's own header. overId is the highlight
  // and never drives the move.
  const [dragIds, setDragIds] = useState(null);
  const [dragColumn, setDragColumn] = useState(null);
  const [overId, setOverId] = useState(null);

  // blockFor is what grabbing this card moves: itself, or the whole
  // selection when the card is part of one -- restricted to the column
  // it was grabbed in, since a card only ever moves within its own
  // column. Both gestures start here, so a tap picks up exactly what a
  // drag would have taken.
  const byId = useMemo(
    () => new Map((tasks || []).map((t) => [t.id, t])),
    [tasks],
  );

  const blockFor = (columnId, t) => {
    const ids =
      selected.has(t.id) && selected.size > 1 ? selected : new Set([t.id]);
    return cards
      .get(columnId)
      .filter((x) => ids.has(x.id))
      .map((x) => x.id);
  };

  const startDrag = (columnId, t) => {
    setDragIds(blockFor(columnId, t));
    setDragColumn(columnId);
  };

  const endDrag = () => {
    setDragIds(null);
    setDragColumn(null);
    setOverId(null);
  };

  // The same reorder for a finger or a keyboard: tap a card's handle to
  // pick it up, then tap one of the slots that appear between that
  // column's cards (ReorderPickup.jsx). The column it was picked up in
  // is the pick-up's scope, so the slots only ever appear in the one
  // column a card can be put back into -- the same rule the drag keeps
  // by refusing a drop from another column.
  const { picked, toggle: togglePickup, cancel: cancelPickup } = usePickup();
  const pickedColumnIds = new Set(
    (picked && cards.get(picked.scope) ? cards.get(picked.scope) : []).map(
      (t) => t.id,
    ),
  );
  const pickedPresent =
    reorderEnabled &&
    Boolean(picked?.ids?.length) &&
    picked.ids.every((id) => pickedColumnIds.has(id));
  useEffect(() => {
    if (picked && !pickedPresent) cancelPickup();
  }, [picked, pickedPresent, cancelPickup]);
  const pickedIds = pickedPresent ? picked.ids : null;
  const pickedColumn = pickedIds ? picked.scope : null;
  // How many places the picked-up cards could go, for the bar above the
  // board: a column whose every card is in the block has none, and the
  // bar says that rather than asking for a choice the column is not
  // offering. The columns compute their own slots below.
  const pickedGaps =
    pickedIds && cards.has(pickedColumn)
      ? gapsOf(
          cards.get(pickedColumn).map((t) => t.id),
          pickedIds,
        ).length
      : 0;

  // moveTo is where both gestures end up: `gap` is a position among a
  // column's own visible cards, and its two neighbours are what
  // onReorder is told. The store places the moved tasks between whatever
  // those names resolve to in the full backlog, so a card moved to the
  // top of a column goes above the card that was there rather than to
  // the top of the whole backlog.
  const moveTo = (ids, rows, gap) =>
    onReorder(
      ids,
      gap > 0 ? rows[gap - 1].id : null,
      gap < rows.length ? rows[gap].id : null,
    );

  // dropOn resolves a drop onto targetId -- or onto the end of the
  // column, when targetId is null -- to that same gap among the
  // column's visible cards.
  const dropOn = (columnId, targetId) => {
    if (!dragIds || columnId !== dragColumn) return endDrag();
    const dragging = new Set(dragIds);
    const visible = cards.get(columnId).filter((t) => !dragging.has(t.id));
    const idx =
      targetId === null
        ? visible.length
        : visible.findIndex((t) => t.id === targetId);
    moveTo(dragIds, visible, idx);
    endDrag();
  };

  // A column's own "select all": the batch actions bar under the board
  // is the list's, and "run everything queued" is the reason somebody
  // opens a board in the first place.
  const toggleColumn = (columnId, checked) => {
    onSelectAll(
      cards.get(columnId).map((t) => t.id),
      checked,
    );
  };

  const visibleBoardIds = columns.flatMap((c) =>
    (cards.get(c.id) || []).map((t) => t.id),
  );
  const lastSelectedRef = useRef(null);
  const handleToggleSelect = onToggleSelect
    ? (id, opts) => {
        if (opts?.shiftKey && onSelectAll && lastSelectedRef.current !== null) {
          const anchorIdx = visibleBoardIds.indexOf(lastSelectedRef.current);
          const targetIdx = visibleBoardIds.indexOf(id);
          if (anchorIdx !== -1 && targetIdx !== -1) {
            const [start, end] =
              anchorIdx <= targetIdx
                ? [anchorIdx, targetIdx]
                : [targetIdx, anchorIdx];
            const rangeIds = visibleBoardIds.slice(start, end + 1);
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

  const Root = landmark ? "main" : "div";
  return (
    <Root className={landmark ? "board" : "board task-view"}>
      <ListHeader
        title="Board"
        count={shown}
        action={
          fixedColumns ? null : (
            <Button
              size="small"
              sx={{ ml: "auto" }}
              onClick={() => setEditing(true)}
            >
              Columns
            </Button>
          )
        }
      />
      {tasks.length > 0 && (
        <NarrowingBar
          tasks={onBoardTasks}
          config={config}
          narrowing={narrowing}
          onNarrow={onNarrow}
        />
      )}

      {/* What the board is deliberately not showing. Said once, under
          the toolbar, with the number of tasks it comes to -- so a board
          whose "Closed" column somebody removed doesn't quietly look
          like a deployment with fewer tasks in it than it has. */}
      {hidden > 0 && (
        <Typography
          variant="caption"
          color="text.secondary"
          component="p"
          className="board-hidden-note"
        >
          {hidden} task{hidden === 1 ? "" : "s"} in no column (
          {stateLabels(offBoard).join(", ")}).{" "}
          {!fixedColumns && (
            <Button size="small" onClick={() => setEditing(true)}>
              Edit columns
            </Button>
          )}
        </Typography>
      )}

      {pickedIds && (
        <MovingBar
          count={pickedIds.length}
          nowhere={pickedGaps === 0}
          onCancel={cancelPickup}
        />
      )}

      <div className="board-columns">
        {columns.map((c) => {
          const columnCards = cards.get(c.id);
          const allSelected =
            columnCards.length > 0 &&
            columnCards.every((t) => selected.has(t.id));
          // The cards a pick-up in *this* column can land between, and
          // the gaps between them worth offering. Every other column
          // renders exactly as it did: nothing was picked up there, and
          // a card cannot move to it.
          const lifted = pickedColumn === c.id ? pickedIds : null;
          const kept = lifted
            ? columnCards.filter((t) => !lifted.includes(t.id))
            : columnCards;
          const gapBefore = new Map(kept.map((t, i) => [t.id, i]));
          const gaps = lifted
            ? new Set(
                gapsOf(
                  columnCards.map((t) => t.id),
                  lifted,
                ),
              )
            : null;
          return (
            <section key={c.id} className="board-column">
              <header className="board-column-header">
                {onSelectAll && columnCards.length > 0 && (
                  <Checkbox
                    size="small"
                    checked={allSelected}
                    onChange={(e) => toggleColumn(c.id, e.target.checked)}
                    inputProps={{
                      "aria-label": `Select every task in ${c.title}`,
                    }}
                  />
                )}
                <span className="board-column-title">{c.title}</span>
                <span className="count">{columnCards.length}</span>
              </header>
              <ul className="board-column-cards">
                {columnCards.map((t) => (
                  <Fragment key={t.id}>
                    {lifted &&
                      gapBefore.has(t.id) &&
                      gaps.has(gapBefore.get(t.id)) && (
                        <MoveSlot
                          label={`Move here, above ${t.title}, in ${c.title}`}
                          onMove={() => {
                            moveTo(lifted, kept, gapBefore.get(t.id));
                            cancelPickup();
                          }}
                        />
                      )}
                    <li
                      className={
                        overId === t.id &&
                        dragIds &&
                        dragColumn === c.id &&
                        !dragIds.includes(t.id)
                          ? "board-drop-target"
                          : undefined
                      }
                      draggable={movable(t)}
                      onDragStart={() => movable(t) && startDrag(c.id, t)}
                      onDragEnd={endDrag}
                      onDragOver={(e) => {
                        if (
                          !dragIds ||
                          dragColumn !== c.id ||
                          dragIds.includes(t.id)
                        )
                          return;
                        e.preventDefault();
                        setOverId(t.id);
                      }}
                      onDrop={(e) => {
                        e.preventDefault();
                        dropOn(c.id, t.id);
                      }}
                    >
                      <BoardCard
                        t={t}
                        byId={byId}
                        config={config}
                        fields={fields}
                        onOpenTask={onOpenTask}
                        selected={selected}
                        onToggleSelect={handleToggleSelect}
                        draggable={movable(t)}
                        dragging={dragIds?.includes(t.id) ?? false}
                        picked={lifted?.includes(t.id) ?? false}
                        // A column of one has nowhere to move its own card to.
                        canMove={columnCards.length > 1}
                        onPickUp={() => togglePickup(blockFor(c.id, t), c.id)}
                        // A fixed-state column normally says enough on its
                        // own.
                        showState={
                          c.states.length > 1 ||
                          (t.state === "queued" &&
                            Boolean(t.activityBySetup && t.activity)) ||
                          // A blocked card stays in its own state's column
                          // (it is still queued or proposed) but wears the
                          // blocked oval there, since that is the one thing
                          // the column heading cannot say for it.
                          isBlockedWaiting(t)
                        }
                      />
                    </li>
                  </Fragment>
                ))}
                {lifted && gaps.has(kept.length) && (
                  <MoveSlot
                    label={`Move here, to the end of ${c.title}`}
                    onMove={() => {
                      moveTo(lifted, kept, kept.length);
                      cancelPickup();
                    }}
                  />
                )}
                {reorderEnabled && dragIds && dragColumn === c.id && (
                  <li
                    className={`board-drop-end${overId === `__end__${c.id}` ? " board-drop-target" : ""}`}
                    onDragOver={(e) => {
                      e.preventDefault();
                      setOverId(`__end__${c.id}`);
                    }}
                    onDrop={(e) => {
                      e.preventDefault();
                      dropOn(c.id, null);
                    }}
                  />
                )}
              </ul>
              {columnCards.length === 0 && (
                <p className="board-column-empty">
                  {c.states.length === 0
                    ? "No states in this column."
                    : "Nothing here."}
                </p>
              )}
            </section>
          );
        })}
      </div>

      {columns.length === 0 && (
        <ListEmpty>
          This board has no columns. Add one with “Columns”.
        </ListEmpty>
      )}
      {columns.length > 0 && shown === 0 && (
        <ListEmpty>
          {isNarrowed(narrowing)
            ? "No tasks match this search and these filters."
            : "No tasks in any of these columns."}
        </ListEmpty>
      )}

      {editing && (
        <BoardColumnsOverlay
          columns={columns}
          onSave={saveColumnLayout}
          onClose={() => setEditing(false)}
        />
      )}
    </Root>
  );
}

// BoardCard is a task as it appears on the board: the same facts
// TaskRow puts in a row, stacked into a card that survives a column a
// few hundred pixels wide. It is not TaskRow itself -- that row is one
// line of fixed columns (handle, number, title, chips) whose whole
// shape assumes the width of a page -- but it shows the same things and
// takes the same click, so a task reads the same either way.
//
// fields is TaskRow's own prop, meaning the same thing: which declared
// fields this card shows the value of (CustomFields.jsx's rowFields).
// Left off, it is the deployment's flagged ones -- the answer for a
// caller with no sort of its own.
function BoardCard({
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
  showState = true,
}) {
  const phase = completionPhase(t);
  const rawActivity = runActivity(t);
  const activity =
    t.state === "queued" && !rawActivity?.byQueue ? null : rawActivity;
  const agentRunning = isAgentRunning(t);
  const badgeState = taskBadgeState(t);
  // grain's own verdict that this row's merge-queue wait has stopped
  // being one (state.js's mergeQueueStall). It goes beside the phase
  // chip because it is the same kind of correction: the badge and the
  // activity line both read as a change on its way, and this is the
  // thing that says otherwise on the row itself.
  const stall = mergeQueueStall(t);
  // stateTitle rather than STATE_LABELS, and the repairing flag passed
  // through to the mark, so a card says "Repairing" in the queue's own
  // green wherever the same task's row in TaskList would, and carries
  // the wait reason on hover when a queued task is waiting.
  const label = stateTitle(t, byId);
  const shownFields = fields || rowFields(config);
  const shiftClickRef = useRef(false);
  return (
    <div
      className={`board-card${dragging ? " board-card-dragging" : ""}${picked ? " board-card-picked" : ""}`}
      onClick={(e) => {
        focusRowLink(e);
        onOpenTask(t.id);
      }}
    >
      <div className="board-card-top">
        {draggable && (
          <MoveHandle
            label={picked ? `Stop moving task ${t.id}` : `Move task ${t.id}`}
            picked={!!picked}
            disabled={!canMove}
            onToggle={() => onPickUp?.()}
          />
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
                e?.nativeEvent?.shiftKey ||
                e?.shiftKey ||
                shiftClickRef.current,
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
        {showState &&
          (() => {
            const modifier = taskBadgeModifier(t);
            const repairing = isTaskRepairing(t);
            return (
              <span
                className={`badge badge-icon badge-${badgeState}${agentRunning ? " badge-mark" : ""}${modifier ? ` ${modifier}` : ""}`}
                title={label}
                role="img"
                aria-label={label}
              >
                <StateDot
                  state={badgeState}
                  title={label}
                  repairing={repairing}
                  agentRunning={agentRunning}
                />
              </span>
            );
          })()}
        <span className="task-number">{t.id}</span>
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
        {/* The same correction to the badge the row carries
            (TaskList.jsx): this column is where the card is because of
            its state, and this says that state has stood longer than it
            should take. */}
        {t.stall && (
          <Chip size="small" color="warning" title={t.stall} label="Stalled" />
        )}
      </div>
      {/* The title is the card's way in from the keyboard, as it is on a
          row (TaskList.jsx): a real link, since the card around it holds
          a move handle and a checkbox and so cannot be one itself. */}
      <RowLink
        className="board-card-title"
        href={`/tasks/${encodeURIComponent(t.id)}`}
        onOpen={() => onOpenTask(t.id)}
        aria-label={t.title ? undefined : `Task ${t.id}`}
      >
        {t.title}
      </RowLink>
      {activity && (
        <div
          className="task-activity board-card-activity"
          title={activityTitle(activity)}
        >
          {/* Whose sentence this is, exactly as a row says it
              (TaskList.jsx): everything an agent writes here is its own
              words, and grain's two other voices -- the setup it is
              doing before there is an agent, and the merge queue's
              account of landing the change -- are marked rather than
              left to be told apart by their wording (state.js's
              runActivity). A card that dropped the mark would put
              "Waiting for CI" in the agent's voice, which is the one
              thing the mark exists to prevent. */}
          {activity.bySetup && <span className="task-activity-by">grain</span>}
          <span className="task-activity-note">{activity.note}</span>
          {activity.age && (
            <span className="task-activity-age">{activity.age}</span>
          )}
        </div>
      )}
      <div className="board-card-chips">
        {/* This deployment's own fields first, as on a row (TaskList.jsx)
            -- the card wraps where the row does not, so they cost the
            title nothing here, but they still read first. */}
        <CustomFieldChips fields={shownFields} values={t.customFields} />
        {/* The two "nobody filed this by hand" chips carry the figure of
            the thing that did, exactly as they do on a task row
            (TaskList.jsx, ItemGlyph.jsx): the board is the same tasks in
            columns, so the hourglass and the four lobes have to mean the
            same here as in the list and in the nav rail. */}
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
        {t.stacked && (
          <Chip size="small" className="chip-stacked" {...stackedChip(t)} />
        )}
        {t.supersededBy && (
          <Chip
            size="small"
            variant="outlined"
            title={`closed as replaced by task ${t.supersededBy}, which was filed to supersede it`}
            label={`superseded by ${t.supersededBy}`}
          />
        )}
        {/* TaskList.jsx's held chip, on a card as on a row. */}
        {t.heldBy && !t.approved && (
          <Chip
            size="small"
            variant="outlined"
            className="chip-held"
            title={`task ${t.heldBy} could have queued this on its own permission and asked for a person to approve it first`}
            label={`held for approval by ${t.heldBy}`}
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
        {(t.capabilities || []).map((id) => (
          <GrantChip key={id} grant={id} config={config} />
        ))}
      </div>
    </div>
  );
}
