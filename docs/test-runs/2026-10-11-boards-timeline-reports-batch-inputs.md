# Walkthrough: inputs on boards, the timeline, reports and batch actions

**Date**: 2026-10-11
**Task**: grain/task-2896 (filed by task 2853)
**Suite**: `ui/e2e/boards-timeline-reports-batch-inputs-walkthrough.spec.js`
**Environment**: Chromium (Playwright) against a real `grain demo` server
on its embedded SQLite store, one per worker (`e2e/fixtures.js`), with
the frontend built by `npm run build`. Nothing in grain's own API or in
React was mocked; the only seeding is through grain's own REST API
(`POST /api/tasks`, `POST /api/boards`), and each test removes what it
seeded.

**Result**: 5 tests, run twice each (`--repeat-each=2`, two workers):
10 passed. No product defect was found.

## What the task asked for that grain does not have

The task listed several inputs that do not exist in this tree. I did not
invent them, and the walkthrough does not test them:

| Asked for                                                       | What is actually there                                                                                                                                                                                                                            |
| --------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Board name, title and description inputs                        | No in-app board form. "New board" opens the New task form prefilled with a request for an agent to add the board (`boards.js`). Boards are rows created through `POST /api/boards` or by an agent. `boards.spec.js` already covers that hand-off. |
| WIP limit numeric input, swimlane / group-by selector on boards | Neither exists. The columns editor has a title, a States multi-select, move left/right, remove, add, reset, cancel and save. Grouping exists only on the timeline (Lanes).                                                                        |
| Timeline date range pickers                                     | The Range control is a fixed choice (24h / 7d / 30d / 90d / Everything, `gantt.js` `RANGES`). There is no date picker.                                                                                                                            |
| Panel title input, query box, panel type picker on dashboards   | Not editable in the UI. Panels are declared on the board row. The only edits the UI makes are a panel's position (drag / tap-to-move) and "keep this narrowing" as a panel's query. `boards.spec.js` covers both.                                 |
| Markdown prose block editor in reports                          | Reports have no prose sections (`report.js` / `model.ReportSection`).                                                                                                                                                                             |
| Defer date picker in the batch bar                              | Batch Defer takes no date: a task stays deferred until it is undeferred. No UI anywhere sets a defer-until date; only a scheduled task filed through the API or by an agent has a `not_before`.                                                   |
| Bulk edit "title" input                                         | Bulk edit has no title field, which is by design: one title on many tasks is not a useful edit. Its fields are Repo, Base branch, Auto-merge, Team, Model, Agent framework, Review, Prompt extension, and the deployment's own fields.            |

If any of these are wanted, they are feature work rather than
verification. Each would need its own task.

## 1. Board columns (`BoardColumnsOverlay.jsx`)

- **Column title** (`Column N title`): focused and typed into from the
  keyboard (`End`, then typing). `Tab` moves focus to that column's own
  **States** picker.
- **States multi-select**: opened with `Enter` and an option picked with
  `Enter`; `Escape` closes the listbox without closing the dialog. A
  state belongs to one column only: picking Queued for a new column
  removes it from the Queued column, which then reads "No states".
- **+ Add column**: adds "New column" with no states.
- **Move left / Move right**: named after the column they move, and
  disabled at the ends.
- **Unsaved-edit guard**: `Escape` over an edited form raises the
  discard confirmation. Dismissing it keeps the dialog and the edit.
  **Cancel** drops the whole draft, and the board is unchanged.
- **Empty title**: saving a column whose title was cleared shows the
  label of the states it collects ("Queued"), not a nameless column.
- **Remove every column**: the "No columns. Saving now puts the default
  board back." note appears, and Save restores the default columns. The
  test ends on the default board, so later specs start clean.

## 2. Board card selection and the batch bar (`TaskBoard.jsx`, `BatchActionsBar.jsx`, `BulkEditOverlay.jsx`)

- **Card checkboxes** (`Select <id>`): one card ticked by mouse, one by
  focusing its checkbox and pressing `Space`. The bar counts "1
  selected", then "2 selected".
- **Column select-all** (`Select every task in Proposed`): selects every
  card in the column, and unticking it clears the selection.
- **Capability picker**: has an accessible name ("Capability to attach
  or detach"). Attach and Detach stay disabled until a capability is
  picked.
- **Bulk edit**: **Apply** is disabled while no field is ticked. Ticking
  the deployment's **Requested by** text field shows its input, which
  was filled in. The **Team** row shows the selection's own team and
  hides its control again when unticked. After Apply, both tasks carry
  the new `requested-by` value (read back through `GET /api/tasks/:id`),
  and the selection clears.
- Defer, Undefer and the Priority / Auto-merge bulk edit are already
  covered by `batch_actions_workflows.spec.js`, so they are not
  repeated.

## 3. Timeline (`GanttChart.jsx`, `gantt.js`)

- **Range**: focused, opened with `ArrowDown`, "Everything" chosen with
  `Enter`. Focus comes back to the picker.
- **Lanes**: opened with `Enter`, "By state" chosen. Lane titles appear.
- **Search** (`Search tasks…`): typing "Bump the Go toolchain" narrows
  the rows to exactly that one task. Clearing the box restores the full
  row count.
- **Malformed narrowing**: loading the board with `?f=nosuchattr:any:x`
  shows an "Unknown condition: nosuchattr" chip rather than dropping it
  silently. Deleting the chip removes it from the URL too.
- The test puts Range and Lanes back to their defaults. Persistence
  across a reload is `gantt.spec.js`'s job.

## 4. Report board (`ReportBoard.jsx`)

- **Window**: opened and set to "Last 30 days" from the keyboard.
- **Refresh**: goes disabled while loading and comes back enabled, with
  no "could not be loaded" alert.
- **Search**: accepts free text that matches no task without breaking
  the report.
- **Malformed narrowing** (`?f=nosuchattr:any:x`): shows the Unknown
  condition chip, and the report still loads.

## 5. Phone width (390×844)

- **Columns editor**: the title box stays wider than 100px, because the
  row wraps instead of squeezing it (grain/task-23's fix still holds).
- **Batch bar**: wraps, and **Clear selection** stays in the viewport.
- **Timeline**: Range and Lanes both stay on screen.
- **Report**: Window stays on screen.
- **Dashboard** (a states panel plus a list panel whose query is
  malformed): no horizontal overflow, and the bad panel shows its
  "Unknown condition" note.

## Drag and drop next to inputs

Moves on a board, a dashboard or a report all use the tap-to-pick-up
handle (`ReorderPickup.jsx`), which is a button separate from every
input. A click or a keystroke in a title box or picker cannot start a
move. The pickup gesture itself is covered by `boards.spec.js` ("drags a
panel into place on a dashboard", "reorders a board within its folder")
and is not repeated here.
