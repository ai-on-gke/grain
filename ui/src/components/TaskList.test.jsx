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
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import TaskList from "./TaskList.jsx";
import {
  NO_NARROWING,
  defaultNarrowing,
  defaultSortFor,
} from "../taskFilters.js";
import { TimeZoneProvider } from "../TimeZoneContext.jsx";

const tasks = [
  {
    id: 1,
    title: "Fix the thing",
    state: "queued",
    capabilities: [],
    blocked: false,
  },
  {
    id: 2,
    title: "Ship the other thing",
    state: "running",
    capabilities: [],
    blocked: true,
    blockedBy: [1],
  },
];

// Narrowed holds the search text, sort order and filters the toolbar
// drives, the way App does now that they live there and in the URL
// (grain/task-317) rather than inside TaskList itself -- starting, as
// App does, on the deployment's own default (taskFilters.js's
// defaultNarrowing), which is what leaves closed and deferred tasks out.
// Tests that only look at rows never touch it; a test that starts from
// an already narrowed list passes a `narrowing` prop, which seeds it.
function Narrowed({ props }) {
  const [narrowing, setNarrowing] = useState(() => {
    if (props.narrowing) return props.narrowing;
    const base = {
      ...defaultNarrowing(props.config),
      sort: defaultSortFor(props.stateFilter),
    };
    if (props.stateFilter === "all" || props.stateFilter === "blocked")
      return base;
    return {
      ...base,
      filters: (base.filters || []).filter((c) => c.attr !== "state"),
    };
  });
  return (
    <TaskList
      {...props}
      narrowing={narrowing}
      onNarrow={(patch) => setNarrowing((prev) => ({ ...prev, ...patch }))}
    />
  );
}

function renderList(overrides = {}) {
  const props = {
    tasks,
    stateFilter: "all",
    config: null,
    onOpenTask: vi.fn(),
    selected: new Set(),
    onToggleSelect: vi.fn(),
    onSelectAll: vi.fn(),
    ...overrides,
  };
  const wrap = (p) =>
    p.zone ? (
      <TimeZoneProvider zone={p.zone}>
        <Narrowed props={p} />
      </TimeZoneProvider>
    ) : (
      <Narrowed props={p} />
    );
  const { rerender } = render(wrap(props));
  // rerender takes another set of overrides, for a test that needs this
  // component to survive a change of props -- a poll bringing new task
  // states, the sidebar's own filter moving -- rather than mount fresh.
  // Whatever the toolbar was asking survives it, since Narrowed above
  // is what holds that.
  return {
    ...props,
    rerender: (next) => rerender(wrap({ ...props, ...next })),
  };
}

// row/rowFor is the draggable <li> a task's title sits inside -- what
// TaskList.jsx's onDragStart/onDragOver/onDrop are actually attached to,
// one level up from the text a test finds the row by.
function rowFor(title) {
  return screen.getByText(title).closest("li");
}

// The three gestures the shared toolbar takes (NarrowingBar.jsx), since
// every filter and sort test below starts with one of them. The two
// panel buttons are found by their hover text rather than their label,
// which says what is currently asked ("Filters (2)", "Newest first").
const FILTER_BUTTON = "Add and edit the conditions this list is narrowed by";
const SORT_BUTTON = "Choose the order this list is shown in";

function addButton(labels) {
  return screen.queryByRole("button", { name: labels });
}

// How many rows one of the two editors is showing right now, which is
// the row number the next one added will carry.
function panelRows(panel) {
  const el = document.querySelector(`.narrowing-panel[aria-label="${panel}"]`);
  return el ? el.querySelectorAll(".narrowing-row").length : 0;
}

// filterBy adds one condition on `attribute` and picks `values` in it,
// opening the editor first if it is not already open. The row it
// returns is the number the editor's own aria-labels use.
async function filterBy(user, attribute, ...values) {
  const labels = /^(Filter by…|Add filter)$/;
  if (!addButton(labels)) await user.click(screen.getByTitle(FILTER_BUTTON));
  const rows = panelRows("Filters");
  await user.click(addButton(labels));
  await user.click(await screen.findByRole("menuitem", { name: attribute }));
  const row = rows + 1;
  if (values.length > 0) {
    await user.click(screen.getByLabelText(`Value for filter ${row}`));
    for (const value of values) {
      await user.click(await screen.findByRole("option", { name: value }));
    }
    await user.keyboard("{Escape}");
  }
  return row;
}

// sortBy adds one sort key on `attribute`, and points it whichever way
// `direction` names when that is not the direction it starts in.
async function sortBy(user, attribute, direction) {
  const labels = /^(Sort by…|Then by…)$/;
  if (!addButton(labels)) await user.click(screen.getByTitle(SORT_BUTTON));
  const rows = panelRows("Sort");
  await user.click(addButton(labels));
  await user.click(await screen.findByRole("menuitem", { name: attribute }));
  if (direction) {
    await user.click(screen.getByLabelText(`Direction for sort ${rows + 1}`));
    await user.click(await screen.findByRole("option", { name: direction }));
  }
}

describe("TaskList", () => {
  // grain/task-1429: a task opened only on a pointer click on its row --
  // A click on the row anywhere but its title still hands the task pane
  // the title link to give focus back to, rather than <body>.
  it("puts focus on the row's link when the row is clicked off its title", async () => {
    const user = userEvent.setup();
    const { onOpenTask } = renderList();
    const link = screen.getByRole("link", { name: "Fix the thing" });
    await user.click(link.closest(".task-row"));
    expect(onOpenTask).toHaveBeenCalledWith(1);
    expect(link).toHaveFocus();
  });

  // Tab reached the move handle and the checkbox, and Enter on the handle
  // picks the row up rather than opening it. The title is a link to the
  // task's own page now, and the handle and checkbox keep their keys.
  it("opens a task from the keyboard through the link its title is", async () => {
    const user = userEvent.setup();
    const { onOpenTask, onToggleSelect } = renderList();

    const link = screen.getByRole("link", { name: "Fix the thing" });
    expect(link).toHaveAttribute("href", "/tasks/1");
    link.focus();
    await user.keyboard("{Enter}");
    expect(onOpenTask).toHaveBeenCalledTimes(1);
    expect(onOpenTask).toHaveBeenCalledWith(1);

    // Space on the checkbox selects, and opens nothing.
    onOpenTask.mockClear();
    screen.getByRole("checkbox", { name: "Select 1" }).focus();
    await user.keyboard(" ");
    expect(onToggleSelect).toHaveBeenCalledWith(1);
    expect(onOpenTask).not.toHaveBeenCalled();

    // A click opens it once; a modified click is the browser's new tab.
    await user.click(link);
    expect(onOpenTask).toHaveBeenCalledTimes(1);
    onOpenTask.mockClear();
    // The click is left to the browser: not prevented, and not handled.
    // A listener on React's own root container, added after React's,
    // sees the click once React is done with it -- and stops jsdom,
    // which cannot open a tab, from trying to follow it.
    const root = link.closest("body > div");
    let leftToBrowser;
    const follow = (e) => {
      leftToBrowser = !e.defaultPrevented;
      e.preventDefault();
    };
    root.addEventListener("click", follow);
    fireEvent.click(link, { ctrlKey: true });
    root.removeEventListener("click", follow);
    expect(leftToBrowser).toBe(true);
    expect(onOpenTask).not.toHaveBeenCalled();
  });

  it("lists every task when the filter is 'all'", () => {
    renderList();
    expect(screen.getByText("Fix the thing")).toBeInTheDocument();
    expect(screen.getByText("Ship the other thing")).toBeInTheDocument();
    expect(document.querySelector(".content-header .count")).toHaveTextContent(
      "2",
    );
  });

  // grain/task-1583: the state dot is a colour or a moving mark, which
  // says nothing to a screen reader unless the badge carries a name.
  it("names each row's state for assistive technology", () => {
    renderList();
    const badgeFor = (title) =>
      screen.getByText(title).closest(".task-row").querySelector(".badge-icon");
    expect(badgeFor("Fix the thing")).toHaveRole("img");
    expect(badgeFor("Fix the thing")).toHaveAccessibleName("Waiting");
    expect(badgeFor("Ship the other thing")).toHaveAccessibleName("Running");
  });

  it("filters down to a single state", () => {
    renderList({ stateFilter: "running" });
    expect(screen.queryByText("Fix the thing")).not.toBeInTheDocument();
    expect(screen.getByText("Ship the other thing")).toBeInTheDocument();
  });

  // The batch bar under the list is told which rows are on screen, so a
  // selection made in another view can say it is not (BatchActionsBar's
  // shownIds) -- and told again when the view moves.
  it("reports the rows it is showing, and again when they change", () => {
    const onShownChange = vi.fn();
    const { rerender } = renderList({ onShownChange });
    expect(onShownChange).toHaveBeenLastCalledWith(new Set([1, 2]));

    rerender({ stateFilter: "running" });
    expect(onShownChange).toHaveBeenLastCalledWith(new Set([2]));
  });

  // grain/task-372: a task is a kind with a figure of its own, and this
  // heading carries it whichever slice of the list is showing -- every
  // one of them is the same rows narrowed.
  it("heads the list with the tasks figure, in every state", () => {
    renderList();
    expect(
      document.querySelector('.header-icon svg[data-glyph="tasks"]'),
    ).toBeInTheDocument();
    renderList({ stateFilter: "running" });
    expect(
      document.querySelectorAll('.header-icon svg[data-glyph="tasks"]'),
    ).toHaveLength(2);
  });

  it("shows a blocked task's state as the blocked oval, with no extra chip", () => {
    renderList({
      tasks: [
        { id: 1, title: "Fix the thing", state: "running", capabilities: [] },
        {
          id: 2,
          title: "Wait for it",
          state: "queued",
          capabilities: [],
          blocked: true,
          blockedBy: [1],
        },
      ],
    });
    const row = screen.getByText("Wait for it").closest(".task-row");
    const badge = row.querySelector(".badge");
    expect(badge).toHaveClass("badge-blocked");
    expect(badge).toHaveAttribute("title", "Blocked on: #1 (running)");
    expect(badge).toHaveAttribute("aria-label", "Blocked on: #1 (running)");
    expect(row.querySelector(".MuiChip-root")).toBeNull();
    expect(row.textContent).not.toContain("Blocked");
  });

  it("keeps a running task's own badge even with an open dependency", () => {
    renderList();
    const row = screen.getByText("Ship the other thing").closest(".task-row");
    expect(row.querySelector(".badge")).toHaveClass("badge-running");
    expect(row.textContent).not.toContain("Blocked");
  });

  it("filters to blocked tasks", () => {
    renderList({ stateFilter: "blocked" });
    expect(screen.queryByText("Fix the thing")).not.toBeInTheDocument();
    expect(screen.getByText("Ship the other thing")).toBeInTheDocument();
  });

  it("filters to merged tasks and displays Merged header and badge", () => {
    const tasks = [
      {
        id: 1,
        title: "Merged PR task",
        state: "closed",
        prMergedAt: "2026-09-26T00:00:00Z",
        capabilities: [],
        blocked: false,
      },
      {
        id: 2,
        title: "Cancelled task",
        state: "closed",
        endedAt: null,
        capabilities: [],
        blocked: false,
      },
    ];
    renderList({ tasks, stateFilter: "merged", narrowing: NO_NARROWING });
    expect(screen.getByText("Merged PR task")).toBeInTheDocument();
    expect(screen.queryByText("Cancelled task")).not.toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: /^Merged\b/ }),
    ).toBeInTheDocument();
  });

  describe("the Merged panel", () => {
    const merged = [
      {
        id: 11,
        title: "Merged first",
        state: "closed",
        prMergedAt: "2026-09-01T10:00:00Z",
        capabilities: [],
        blocked: false,
      },
      {
        id: 12,
        title: "Merged by its history",
        state: "closed",
        pullRequestEvents: [
          { kind: "opened", at: "2026-10-01T09:00:00Z" },
          { kind: "merged", at: "2026-10-02T09:00:00Z" },
        ],
        capabilities: [],
        blocked: false,
      },
      {
        id: 13,
        title: "Merged last",
        state: "closed",
        closedReason: "merged",
        closedAt: "2026-10-20T10:00:00Z",
        capabilities: [],
        blocked: false,
      },
    ];
    const rowTitles = () =>
      [...document.querySelectorAll(".task-row .task-title")].map(
        (el) => el.textContent,
      );

    it("shows when each task merged, on the active clock", () => {
      const zone = "America/Los_Angeles";
      renderList({ tasks: merged, stateFilter: "merged", zone });
      for (const [title, at] of [
        ["Merged first", "2026-09-01T10:00:00Z"],
        ["Merged by its history", "2026-10-02T09:00:00Z"],
        ["Merged last", "2026-10-20T10:00:00Z"],
      ]) {
        const stamp = screen
          .getByText(title)
          .closest(".task-row")
          .querySelector(".task-merged-at");
        expect(stamp.textContent).toBe(
          `merged ${new Date(at).toLocaleString(undefined, { timeZone: zone })}`,
        );
        expect(stamp.querySelector("time").getAttribute("dateTime")).toBe(at);
      }
    });

    it("says nothing about merging on the other lists", () => {
      renderList({
        tasks: merged,
        stateFilter: "closed",
        narrowing: NO_NARROWING,
      });
      expect(document.querySelector(".task-merged-at")).toBeNull();
    });

    it("lists the newest merge first, and offers no Clear for it", () => {
      renderList({ tasks: merged, stateFilter: "merged" });
      expect(rowTitles()).toEqual([
        "Merged last",
        "Merged by its history",
        "Merged first",
      ]);
      expect(screen.getByTitle(SORT_BUTTON).textContent).toMatch(/^Merged/);
      expect(
        screen.queryByRole("button", { name: "Clear" }),
      ).not.toBeInTheDocument();
    });

    it("sorts and filters on Merged from the toolbar", () => {
      renderList({
        tasks: merged,
        stateFilter: "merged",
        narrowing: {
          search: "",
          sort: [{ attr: "merged", dir: "asc" }],
          filters: [{ attr: "merged", op: "after", values: ["2026-09-15"] }],
        },
      });
      expect(rowTitles()).toEqual(["Merged by its history", "Merged last"]);
    });
  });

  // Each row says where its pull request stands with its own state
  // badge, whose title is STATE_LABELS[state] -- no chip repeating it.
  it("tells a task waiting on Submit apart from one on the merge queue", () => {
    renderList({
      tasks: [
        {
          id: 3,
          title: "Awaiting submit task",
          state: "awaiting_submit",
          pullRequest: "acme/widgets#1",
          autoMerge: false,
          capabilities: [],
          blocked: false,
        },
        {
          id: 4,
          title: "Queued for merge task",
          state: "completed",
          pullRequest: "acme/widgets#2",
          autoMerge: true,
          capabilities: [],
          blocked: false,
        },
        // The third of them, and the one that used to be indisting-
        // uishable from the second: a run that answered and opened
        // nothing (model.StateAwaitingReply).
        {
          id: 5,
          title: "Answered task",
          state: "awaiting_reply",
          capabilities: [],
          blocked: false,
        },
      ],
    });
    // Both read "Asked", and the badge's title says which ask it is.
    expect(
      screen.getAllByTitle("Asked you to submit acme/widgets#1").length,
    ).toBeGreaterThan(0);
    expect(screen.getAllByTitle("Merging").length).toBe(1);
    expect(screen.getAllByTitle("Asked a question").length).toBeGreaterThan(0);
  });

  // The one correction still worth a chip: on the queue in name only.
  it("shows a merge-blocked chip once the queue has given up", () => {
    renderList({
      tasks: [
        {
          id: 5,
          title: "Stuck task",
          state: "completed",
          pullRequest: "acme/widgets#3",
          autoMerge: true,
          mergeQueueBlockedAt: "2026-08-01T00:00:00Z",
          capabilities: [],
          blocked: false,
        },
      ],
    });
    expect(screen.getByText("Merge blocked")).toBeInTheDocument();
  });

  // The other correction to a badge that is telling the truth: a queued
  // task that reads queued and has been queued for days
  // (ui.Task.Stall). The sentence rides in the tooltip, where a row has
  // room for it.
  it("shows a stalled chip with the sentence behind it", () => {
    renderList({
      tasks: [
        {
          id: 6,
          title: "Nothing is dispatching this",
          state: "queued",
          stall:
            "this has stood here for more than 3d, which is longer than `reconcileDispatch` should take",
          capabilities: [],
          blocked: false,
        },
      ],
    });
    expect(screen.getByText("Stalled")).toBeInTheDocument();
    expect(screen.getByTitle(/reconcileDispatch/)).toBeInTheDocument();
  });

  // And says nothing about the healthy ones, which is nearly all of
  // them: a flag that fires on a task that is merely waiting is worth
  // less than no flag at all.
  it("leaves a task that is inside its patience unmarked", () => {
    renderList();
    expect(screen.queryByText("Stalled")).not.toBeInTheDocument();
  });

  // Why a closed task is closed, on the row: a design refined five
  // times leaves four closed tasks behind it (grain/task-473), and
  // without this the only way to tell them from work somebody abandoned
  // is to open each one.
  it("names the replacement on a superseded task's row", () => {
    renderList({
      narrowing: NO_NARROWING,
      tasks: [
        {
          id: 468,
          title: "Merge queue design, revision four",
          state: "closed",
          supersededBy: "471",
          capabilities: [],
          blocked: false,
        },
      ],
    });
    expect(screen.getByText("superseded by 471")).toBeInTheDocument();
  });

  // A proposal its agent could have queued and held for a person
  // instead (propose_task's hold_for_approval) says so while it waits,
  // and stops saying so once somebody has approved it.
  const heldProposal = {
    id: 480,
    title: "Widen the target repos",
    state: "proposed",
    heldBy: "438",
    capabilities: [],
    blocked: false,
  };
  it("says a proposal was held for approval on purpose", () => {
    renderList({ narrowing: NO_NARROWING, tasks: [heldProposal] });
    expect(screen.getByText("held for approval by 438")).toBeInTheDocument();
  });
  it("drops the held note once somebody has approved it", () => {
    renderList({
      narrowing: NO_NARROWING,
      tasks: [{ ...heldProposal, state: "queued", approved: true }],
    });
    expect(
      screen.queryByText("held for approval by 438"),
    ).not.toBeInTheDocument();
  });

  it("shows an empty message when nothing matches the filter", () => {
    renderList({ tasks: [] });
    expect(screen.getByText("No tasks in this state.")).toBeInTheDocument();
  });

  // bwsalmon/agents#537 and grain/task-21: a closed task and a deferred
  // one are both out of a list until somebody asks for them. That is one
  // ordinary condition now -- "State is none of Deferred, Closed", the
  // one every task view opens narrowed by (taskFilters.js's
  // defaultFilters) -- where it used to be a checkbox each and a rule of
  // this component's own. So what these check is the list under that
  // condition, and what happens when a reader edits it like any other.
  describe("the states a list opens without", () => {
    const setAside = [
      ...tasks,
      {
        id: 3,
        title: "Long done",
        state: "closed",
        capabilities: [],
        blocked: false,
      },
      {
        id: 4,
        title: "Not this month",
        state: "deferred",
        capabilities: [],
        blocked: false,
      },
    ];

    it("leaves closed and deferred tasks out, and says so on a chip", () => {
      renderList({ tasks: setAside });
      expect(screen.queryByText("Long done")).not.toBeInTheDocument();
      expect(screen.queryByText("Not this month")).not.toBeInTheDocument();
      expect(
        document.querySelector(".content-header .count"),
      ).toHaveTextContent("2");
      expect(
        screen.getByText("State is none of Deferred, Closed"),
      ).toBeInTheDocument();
    });

    // Deleting the chip is the whole gesture where two checkboxes used
    // to be, and it brings back both states at once because it is one
    // condition.
    it("shows them once that condition is deleted", async () => {
      const user = userEvent.setup();
      renderList({ tasks: setAside });

      await user.click(screen.getAllByTestId("CancelIcon")[0]);

      expect(screen.getByText("Long done")).toBeInTheDocument();
      expect(screen.getByText("Not this month")).toBeInTheDocument();
    });

    // ...and editing it is how one of the two comes back on its own,
    // which is what the second checkbox used to be for.
    it("shows one of them when the condition is narrowed to the other", () => {
      renderList({
        tasks: setAside,
        narrowing: {
          search: "",
          sort: [],
          filters: [{ attr: "state", op: "none", values: ["closed"] }],
        },
      });
      expect(screen.getByText("Not this month")).toBeInTheDocument();
      expect(screen.queryByText("Long done")).not.toBeInTheDocument();
    });

    // The deployment-wide setting is what the default condition is built
    // from (taskFilters.js's SHOWN_BY_SETTING), so a deployment that
    // shows closed tasks opens on a narrower condition -- and deferred
    // tasks, which have no setting of their own, stay out of it.
    it("opens without the closed half where the deployment says so", () => {
      renderList({ tasks: setAside, config: { showClosedByDefault: true } });
      expect(screen.getByText("Long done")).toBeInTheDocument();
      expect(screen.queryByText("Not this month")).not.toBeInTheDocument();
      expect(screen.getByText("State is none of Deferred")).toBeInTheDocument();
    });

    // An empty list still has to account for itself: it is empty because
    // of the condition on screen, which is the chip right above it,
    // rather than because there is nothing here at all.
    it("stays on the plain empty message, with the chip saying why", () => {
      renderList({ tasks: [setAside[3]] });
      expect(screen.getByText("No tasks in this state.")).toBeInTheDocument();
      expect(
        screen.getByText("State is none of Deferred, Closed"),
      ).toBeInTheDocument();
    });

    it("keeps a state view un-narrowed on arrival and restores its rows on Clear", async () => {
      const user = userEvent.setup();
      renderList({ tasks: setAside, stateFilter: "deferred" });

      expect(screen.getByText("Not this month")).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Clear" }),
      ).not.toBeInTheDocument();

      await user.type(screen.getByPlaceholderText("Search tasks…"), "nowhere");
      expect(
        screen.getByText("No tasks match your search."),
      ).toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Clear" }));
      expect(screen.getByText("Not this month")).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Clear" }),
      ).not.toBeInTheDocument();
    });
  });

  it("opens a task when its row is clicked", async () => {
    const onOpenTask = vi.fn();
    const user = userEvent.setup();
    renderList({ onOpenTask });

    await user.click(screen.getByText("Fix the thing"));

    expect(onOpenTask).toHaveBeenCalledWith(1);
  });

  // A keyboard-only user has no click to give, so the row's title is a
  // link that opens it on Enter -- while the row itself is no control at
  // all, since it holds a checkbox and a move handle of its own, and the
  // same keys on the checkbox stay the checkbox's (grain/task-1582).
  it("opens a task from the keyboard, and leaves the row's checkbox its own keys", async () => {
    const onOpenTask = vi.fn();
    const onToggleSelect = vi.fn();
    const user = userEvent.setup();
    renderList({ onOpenTask, onToggleSelect });

    const row = screen.getByText("Fix the thing").closest(".task-row");
    expect(row).not.toHaveAttribute("tabindex");
    expect(row).not.toHaveAttribute("role");

    screen.getByRole("link", { name: "Fix the thing" }).focus();
    await user.keyboard("{Enter}");
    expect(onOpenTask).toHaveBeenLastCalledWith(1);
    expect(onOpenTask).toHaveBeenCalledTimes(1);

    screen.getAllByRole("checkbox")[1].focus();
    await user.keyboard(" ");
    expect(onToggleSelect).toHaveBeenCalledWith(1);
    expect(onOpenTask).toHaveBeenCalledTimes(1);
  });

  it("toggles a single task's selection without opening it", async () => {
    const onOpenTask = vi.fn();
    const onToggleSelect = vi.fn();
    const user = userEvent.setup();
    renderList({ onOpenTask, onToggleSelect });

    await user.click(screen.getAllByRole("checkbox")[1]);

    expect(onToggleSelect).toHaveBeenCalledWith(1);
    expect(onOpenTask).not.toHaveBeenCalled();
  });

  it("selects every visible task through the select-all checkbox", async () => {
    const onSelectAll = vi.fn();
    const user = userEvent.setup();
    renderList({ onSelectAll });

    await user.click(screen.getByLabelText("Select all"));

    expect(onSelectAll).toHaveBeenCalledWith([1, 2], true);
  });

  it("checks select-all once every visible task is already selected", () => {
    renderList({ selected: new Set([1, 2]) });
    expect(screen.getByLabelText("Select all")).toBeChecked();
  });

  it("selects a contiguous range of visible tasks when shift-clicking a second checkbox", async () => {
    const onToggleSelect = vi.fn();
    const onSelectAll = vi.fn();
    const user = userEvent.setup();
    renderList({
      tasks: [
        { id: 1, title: "First", state: "queued", capabilities: [] },
        { id: 2, title: "Second", state: "queued", capabilities: [] },
        { id: 3, title: "Third", state: "queued", capabilities: [] },
      ],
      onToggleSelect,
      onSelectAll,
    });

    await user.click(screen.getByRole("checkbox", { name: "Select 1" }));
    expect(onToggleSelect).toHaveBeenCalledWith(1);

    await user.keyboard("{Shift>}");
    await user.click(screen.getByRole("checkbox", { name: "Select 3" }));
    await user.keyboard("{/Shift}");

    expect(onSelectAll).toHaveBeenCalledWith([1, 2, 3], true);
  });

  describe("drag-and-drop reordering (bwsalmon/agents#476)", () => {
    const threeTasks = [
      {
        id: 1,
        title: "First",
        state: "queued",
        capabilities: [],
        blocked: false,
      },
      {
        id: 2,
        title: "Second",
        state: "queued",
        capabilities: [],
        blocked: false,
      },
      {
        id: 3,
        title: "Third",
        state: "queued",
        capabilities: [],
        blocked: false,
      },
    ];

    it("drops a task onto another, calling onReorder with its new neighbours", () => {
      const onReorder = vi.fn();
      renderList({ tasks: threeTasks, onReorder });

      fireEvent.dragStart(rowFor("Third"));
      fireEvent.dragOver(rowFor("First"));
      fireEvent.drop(rowFor("First"));

      // Third dropped onto First's row lands just before it -- no
      // preceding job, so afterId is null (the issue's own "moved to the
      // head of the list" case).
      expect(onReorder).toHaveBeenCalledWith([3], null, 1);
    });

    it("drops a task between two others", () => {
      const onReorder = vi.fn();
      renderList({ tasks: threeTasks, onReorder });

      fireEvent.dragStart(rowFor("Third"));
      fireEvent.dragOver(rowFor("Second"));
      fireEvent.drop(rowFor("Second"));

      expect(onReorder).toHaveBeenCalledWith([3], 1, 2);
    });

    it("drops a task at the very end of the list", () => {
      const onReorder = vi.fn();
      renderList({ tasks: threeTasks, onReorder });

      fireEvent.dragStart(rowFor("First"));
      // The trailing drop zone only renders once a drag is in flight.
      fireEvent.dragOver(document.querySelector(".task-drop-end"));
      fireEvent.drop(document.querySelector(".task-drop-end"));

      expect(onReorder).toHaveBeenCalledWith([1], 3, null);
    });

    it("drags every selected task as a block, in their existing backlog order, when dragging a selected row", () => {
      const onReorder = vi.fn();
      renderList({ tasks: threeTasks, onReorder, selected: new Set([1, 3]) });

      // Grabbing the un-selected middle task drags only itself.
      fireEvent.dragStart(rowFor("Second"));
      fireEvent.drop(rowFor("Third"));
      expect(onReorder).toHaveBeenLastCalledWith([2], 1, 3);

      // Grabbing a selected task drags the whole selection.
      fireEvent.dragStart(rowFor("Third"));
      fireEvent.drop(document.querySelector(".task-drop-end"));
      expect(onReorder).toHaveBeenLastCalledWith([1, 3], 2, null);
    });

    it("Enter on the move handle picks the row up rather than opening it", async () => {
      const onOpenTask = vi.fn();
      const onReorder = vi.fn();
      const user = userEvent.setup();
      renderList({ tasks: threeTasks, onOpenTask, onReorder });

      screen.getAllByRole("button", { name: /^Move task / })[0].focus();
      await user.keyboard("{Enter}");

      expect(onOpenTask).not.toHaveBeenCalled();
      expect(document.querySelector(".task-row-picked")).not.toBeNull();
    });

    it("clicking the drag handle does not open the task (bwsalmon/agents#490)", async () => {
      const onOpenTask = vi.fn();
      const onReorder = vi.fn();
      const user = userEvent.setup();
      renderList({ tasks: threeTasks, onOpenTask, onReorder });

      await user.click(document.querySelector(".task-drag-handle"));

      expect(onOpenTask).not.toHaveBeenCalled();
    });

    // A stacked merge fix (bwsalmon/agents#378) is nested under the task
    // it repairs and never draggable -- the merge queue runs it ahead of
    // the backlog whatever the backlog says -- so it keeps the column the
    // handle sits in rather than letting its own contents slide a handle's
    // width left of every other row's.
    describe("the handle column on a nested merge fix", () => {
      const parent = {
        id: 1,
        title: "First",
        state: "queued",
        capabilities: [],
        blocked: false,
      };
      const fix = {
        id: 4,
        title: "Repair the pull request",
        state: "queued",
        capabilities: [],
        blocked: false,
        stacked: true,
        generatedFrom: 1,
      };
      const rowOf = (title) => screen.getByText(title).closest(".task-row");

      it("holds the column open, and empty, while the rows around it are draggable", () => {
        renderList({ tasks: [parent, fix], onReorder: vi.fn() });

        expect(
          rowOf("Repair the pull request").querySelector(
            ".task-drag-placeholder",
          ),
        ).toBeInTheDocument();
        expect(
          rowOf("Repair the pull request").querySelector(".task-drag-handle"),
        ).not.toBeInTheDocument();
        // The task it is nested under still gets a handle of its own.
        expect(
          rowOf("First").querySelector(".task-drag-handle"),
        ).toBeInTheDocument();
      });

      it("holds no column open when no row in the list has a handle either", () => {
        renderList({ tasks: [parent, fix] });

        expect(
          document.querySelector(".task-drag-placeholder"),
        ).not.toBeInTheDocument();
      });
    });

    it("never starts a drag, and shows no drag handle, without an onReorder prop", () => {
      const { container } = render(
        <TaskList
          tasks={threeTasks}
          stateFilter="all"
          config={null}
          narrowing={NO_NARROWING}
          onNarrow={vi.fn()}
          onOpenTask={vi.fn()}
          selected={new Set()}
          onToggleSelect={vi.fn()}
          onSelectAll={vi.fn()}
        />,
      );
      expect(
        container.querySelector(".task-drag-handle"),
      ).not.toBeInTheDocument();
      expect(rowFor("First")).toHaveAttribute("draggable", "false");
    });

    // grain/task-23: the same reorder for a device that fires no drag
    // events at all. Tapping a handle picks the row up; the slots that
    // appear between the rows are where it can be put down.
    describe("tap to move (grain/task-23)", () => {
      const slots = () =>
        screen.queryAllByRole("button", { name: /^Move here/ });

      it("offers a slot at every position the picked-up task is not already in", async () => {
        const user = userEvent.setup();
        renderList({ tasks: threeTasks, onReorder: vi.fn() });

        expect(slots()).toHaveLength(0);

        await user.click(screen.getByRole("button", { name: "Move task 2" }));

        // Second is picked up out of the middle, so the two positions it
        // can go to are the top of the list and the end of it -- the gap
        // it is already in gets no slot, because tapping it would
        // promise a move and make none.
        expect(slots().map((b) => b.getAttribute("aria-label"))).toEqual([
          "Move here, above First",
          "Move here, to the end of the list",
        ]);
      });

      it("moves the task between the two rows the slot sits between", async () => {
        const onReorder = vi.fn();
        const user = userEvent.setup();
        renderList({ tasks: threeTasks, onReorder });

        await user.click(screen.getByRole("button", { name: "Move task 3" }));
        await user.click(
          screen.getByRole("button", { name: "Move here, above Second" }),
        );

        expect(onReorder).toHaveBeenCalledWith([3], 1, 2);
        // Putting it down ends the gesture: the slots go with it.
        expect(slots()).toHaveLength(0);
      });

      it("moves it to the end of the list", async () => {
        const onReorder = vi.fn();
        const user = userEvent.setup();
        renderList({ tasks: threeTasks, onReorder });

        await user.click(screen.getByRole("button", { name: "Move task 1" }));
        await user.click(
          screen.getByRole("button", {
            name: "Move here, to the end of the list",
          }),
        );

        expect(onReorder).toHaveBeenCalledWith([1], 3, null);
      });

      it("picks up the whole selection when the row is part of one", async () => {
        const onReorder = vi.fn();
        const user = userEvent.setup();
        renderList({
          tasks: threeTasks,
          onReorder,
          selected: new Set([1, 3]),
        });

        await user.click(screen.getByRole("button", { name: "Move task 3" }));
        expect(screen.getByText(/Moving 2 tasks/)).toBeInTheDocument();

        await user.click(
          screen.getByRole("button", { name: "Move here, above Second" }),
        );
        expect(onReorder).toHaveBeenCalledWith([1, 3], null, 2);
      });

      it("puts the task back down on a second tap of its own handle", async () => {
        const onReorder = vi.fn();
        const user = userEvent.setup();
        renderList({ tasks: threeTasks, onReorder });

        await user.click(screen.getByRole("button", { name: "Move task 2" }));
        await user.click(
          screen.getByRole("button", { name: "Stop moving task 2" }),
        );

        expect(slots()).toHaveLength(0);
        expect(onReorder).not.toHaveBeenCalled();
      });

      it("puts it back down on Cancel, and on Escape", async () => {
        const onReorder = vi.fn();
        const user = userEvent.setup();
        renderList({ tasks: threeTasks, onReorder });

        await user.click(screen.getByRole("button", { name: "Move task 2" }));
        await user.click(screen.getByRole("button", { name: "Cancel" }));
        expect(slots()).toHaveLength(0);

        await user.click(screen.getByRole("button", { name: "Move task 2" }));
        expect(slots().length).toBeGreaterThan(0);
        await user.keyboard("{Escape}");
        expect(slots()).toHaveLength(0);
        expect(onReorder).not.toHaveBeenCalled();
      });

      // Picking up the only row there is would put a bar on screen
      // offering no slots at all, which reads as a broken gesture rather
      // than as a list with one row in it.
      it("offers no pick-up on a list of one", () => {
        renderList({ tasks: [threeTasks[0]], onReorder: vi.fn() });

        expect(
          screen.getByRole("button", { name: "Move task 1" }),
        ).toHaveAttribute("disabled");
      });

      // The same rule the drag keeps: while the list is in some other
      // order, a move has nowhere meaningful to land.
      it("drops the pick-up when a display-only sort is chosen", async () => {
        const user = userEvent.setup();
        renderList({ tasks: threeTasks, onReorder: vi.fn() });

        await user.click(screen.getByRole("button", { name: "Move task 2" }));
        expect(slots().length).toBeGreaterThan(0);

        await sortBy(user, "Title");

        expect(slots()).toHaveLength(0);
      });

      it("drops the pick-up when narrowing hides the picked-up row", async () => {
        const user = userEvent.setup();
        renderList({ tasks: threeTasks, onReorder: vi.fn() });

        await user.click(screen.getByRole("button", { name: "Move task 2" }));
        expect(screen.getByText(/Moving 1 task/)).toBeInTheDocument();
        expect(slots().length).toBeGreaterThan(0);

        await user.type(
          screen.getByPlaceholderText("Search tasks…"),
          threeTasks[0].title,
        );

        expect(screen.queryByText(/Moving 1 task/)).not.toBeInTheDocument();
        expect(slots()).toHaveLength(0);
      });
    });
  });

  it("badges a task a schedule filed, and leaves an ordinary one unbadged", () => {
    renderList({
      tasks: [{ ...tasks[0], scheduled: true }, tasks[1]],
    });
    expect(
      screen.getByTitle("filed automatically by a schedule"),
    ).toHaveTextContent("scheduled");
    expect(
      screen.queryAllByTitle("filed automatically by a schedule"),
    ).toHaveLength(1);
  });

  // bwsalmon/agents#642: a suite run files its pass of tasks with nobody
  // typing them in, so they get scheduled's own badge treatment.
  it("badges a task a suite run filed, and leaves an ordinary one unbadged", () => {
    renderList({
      tasks: [{ ...tasks[0], suiteRun: true }, tasks[1]],
    });
    expect(
      screen.getByTitle("filed automatically by a suite run"),
    ).toHaveTextContent("suite");
    expect(
      screen.queryAllByTitle("filed automatically by a suite run"),
    ).toHaveLength(1);
    // The suite chip is its own thing, not the schedule one relabelled.
    expect(
      screen.queryByTitle("filed automatically by a schedule"),
    ).not.toBeInTheDocument();
  });

  // grain/task-320: the two "nobody filed this by hand" chips carry the
  // figure of the thing that did (ItemGlyph.jsx, docs/brand.md), so the
  // hourglass and the four lobes mean the same on a task row as they do
  // in the nav rail. A glyph on the wrong chip is exactly what nobody
  // spots by eye.
  it("marks each origin chip with the glyph of the thing that filed the task", () => {
    renderList({
      tasks: [
        { ...tasks[0], scheduled: true },
        { ...tasks[1], suiteRun: true },
      ],
    });

    expect(
      screen
        .getByTitle("filed automatically by a schedule")
        .querySelector('svg[data-glyph="schedules"]'),
    ).toBeInTheDocument();
    expect(
      screen
        .getByTitle("filed automatically by a suite run")
        .querySelector('svg[data-glyph="suites"]'),
    ).toBeInTheDocument();
    // Decoration beside the word, not a replacement for it: the chips
    // still read "scheduled" and "suite".
    expect(
      screen.getByTitle("filed automatically by a schedule"),
    ).toHaveTextContent("scheduled");
  });

  it("leaves an ordinary task's chips unglyphed", () => {
    renderList();
    expect(document.querySelector(".chips svg[data-glyph]")).toBeNull();
  });

  // bwsalmon/agents#378: a stacked task explains itself by sitting under
  // the task it repairs, so the chip is only for the rows where that
  // nesting is missing.
  describe("the merge-fix chip on a stacked task", () => {
    const parent = {
      id: 1,
      title: "Fix the thing",
      state: "queued",
      capabilities: [],
      blocked: false,
    };
    const fix = {
      id: 2,
      title: "Repair the pull request",
      state: "queued",
      capabilities: [],
      blocked: false,
      stacked: true,
      generatedFrom: 1,
    };

    it("leaves a fix nested under its own parent unchipped", () => {
      renderList({ tasks: [parent, fix] });
      expect(document.querySelector(".task-sublist")).toHaveTextContent(
        "Repair the pull request",
      );
      expect(screen.queryByText("merge fix")).not.toBeInTheDocument();
    });

    it("chips a fix listed at top level because its parent is filtered out of the view", () => {
      renderList({
        tasks: [{ ...parent, state: "completed" }, fix],
        stateFilter: "queued",
      });
      expect(document.querySelector(".task-sublist")).not.toBeInTheDocument();
      expect(
        screen.getByTitle("the merge queue's own automatic fix for 1"),
      ).toHaveTextContent("merge fix");
    });

    it("chips a fix whose parent is gone entirely", () => {
      renderList({ tasks: [fix] });
      expect(
        screen.getByTitle("the merge queue's own automatic fix for 1"),
      ).toHaveTextContent("merge fix");
    });

    it("chips a fix with no generatedFrom at all, without naming a parent", () => {
      renderList({ tasks: [{ ...fix, generatedFrom: undefined }] });
      expect(
        screen.getByTitle(
          "the merge queue's own automatic fix for another task's pull request",
        ),
      ).toHaveTextContent("merge fix");
    });

    // grain/task-284: a review is stacked and nested exactly like a fix,
    // so it would read as one if the chip went by stacked alone. The two
    // are not the same event -- one is a second agent reading a change
    // before it merges, the other is a repair of a red build.
    it("chips a review as a review rather than as a merge fix", () => {
      renderList({
        tasks: [{ ...fix, id: 3, title: "Review the change", review: true }],
      });
      expect(
        screen.getByTitle("a review of 1's own code, run before it merges"),
      ).toHaveTextContent("review");
      expect(screen.queryByText("merge fix")).not.toBeInTheDocument();
    });
  });

  // Sessions live in the Sessions pane (SessionsPage.jsx): an
  // interactive task or a standing session's own task is never a row,
  // whatever the list is narrowed by.
  it("always leaves interactive and standing-session tasks off", () => {
    renderList({
      tasks: [
        { ...tasks[0], interactive: true },
        { ...tasks[1], consoleSession: "director" },
        { ...tasks[1], id: 99, title: "Ordinary work" },
      ],
      narrowing: NO_NARROWING,
    });
    expect(
      [...document.querySelectorAll(".task-title")].map((el) => el.textContent),
    ).toEqual(["Ordinary work"]);
  });

  // bwsalmon/agents#586
  it("gives a running task's state dot the grain mark instead of the plain CSS spinner", () => {
    renderList();
    const queuedDot = rowFor("Fix the thing").querySelector(".badge");
    const runningDot = rowFor("Ship the other thing").querySelector(".badge");

    expect(queuedDot).not.toHaveClass("badge-mark");
    expect(queuedDot).toBeEmptyDOMElement();
    expect(runningDot).toHaveClass("badge-mark");
    // At the badge's size the mark plays a pre-recorded sheet rather
    // than painting, so it needs no canvas and renders here the same way
    // it does in a browser -- see GrainMark.test.jsx for the mechanics.
    expect(runningDot.querySelector(".grain-mark-sheet")).toHaveAttribute(
      "title",
      "Running",
    );
  });

  // A run that is the merge queue repairing this task's own pull request
  // branch, rather than writing the change: the same mark, moving the
  // same way, in blue instead of the accent (.grain-mark-repair), and a
  // badge that says which kind of work it is. A row that has gone back to
  // running has not gone back to the beginning.
  it("marks a task the merge queue is repairing apart from one merely running", () => {
    renderList({
      tasks: [{ ...tasks[1], state: "running", repairing: true }],
    });
    const dot = rowFor("Ship the other thing").querySelector(".badge");
    const mark = dot.querySelector(".grain-mark-sheet");

    expect(dot).toHaveClass("badge-mark");
    expect(mark).toHaveClass("grain-mark-repair");
    expect(mark).toHaveAttribute("title", "Repairing");
  });

  // The row is where this had to land. grain has known a stalled
  // merge-queue wait from a healthy one since grain/task-314, and said so
  // in a sentence only somebody who opened the task read; the mark beside
  // the title went on saying the opposite (state.js's mergeQueueStalled).
  it("stops animating a merge-queue wait grain has flagged as stalled, and says so", () => {
    renderList({
      tasks: [
        {
          ...tasks[0],
          title: "Stuck on CI",
          state: "completed",
          mergeQueueState: "waiting_ci",
          mergeQueueStateAt: new Date(Date.now() - 4 * 3600_000).toISOString(),
          mergeQueueStalledAt: new Date(Date.now() - 90 * 60_000).toISOString(),
        },
      ],
      narrowing: NO_NARROWING,
    });
    const row = rowFor("Stuck on CI");
    expect(row.querySelector(".badge")).not.toHaveClass("badge-mark");
    // The phrase stays -- it is still nominally waiting for CI -- and the
    // chip is what says that has stopped meaning anything.
    expect(row).toHaveTextContent("Waiting for CI");
    expect(row).toHaveTextContent("Stalled");
  });

  it("does not animate stale merge activity on a closed task", () => {
    renderList({
      tasks: [
        {
          ...tasks[0],
          title: "Already closed",
          state: "closed",
          mergeQueueState: "merging",
        },
      ],
      narrowing: NO_NARROWING,
    });
    const row = rowFor("Already closed");
    expect(row.querySelector(".badge")).not.toHaveClass("badge-mark");
    expect(row).not.toHaveTextContent("Merging");
  });

  it("uses the normal CSS spinner when grain is performing setup or active merge operations", () => {
    renderList({
      tasks: [
        {
          ...tasks[0],
          id: 101,
          title: "Setting up sandbox",
          state: "running",
          activityBySetup: true,
          activity: "building a sandbox",
        },
        {
          ...tasks[0],
          id: 102,
          title: "Actively merging",
          state: "completed",
          mergeQueueState: "merging",
        },
        {
          ...tasks[0],
          id: 103,
          title: "Running CI checks",
          state: "completed",
          mergeQueueState: "waiting_ci",
        },
      ],
      narrowing: NO_NARROWING,
    });

    const setupDot = rowFor("Setting up sandbox").querySelector(".badge");
    expect(setupDot).toHaveClass("badge-running");
    expect(setupDot).not.toHaveClass("badge-mark");
    expect(setupDot).not.toHaveClass("badge-repair");
    expect(setupDot.querySelector(".grain-mark-sheet")).toBeNull();

    const mergingDot = rowFor("Actively merging").querySelector(".badge");
    expect(mergingDot).toHaveClass("badge-running");
    expect(mergingDot).toHaveClass("badge-repair");
    expect(mergingDot).not.toHaveClass("badge-mark");
    expect(mergingDot.querySelector(".grain-mark-sheet")).toBeNull();

    const ciDot = rowFor("Running CI checks").querySelector(".badge");
    expect(ciDot).toHaveClass("badge-running");
    expect(ciDot).toHaveClass("badge-repair");
    expect(ciDot).not.toHaveClass("badge-mark");
    expect(ciDot.querySelector(".grain-mark-sheet")).toBeNull();
  });

  it("colors normal spinners blue for merge repairs", () => {
    renderList({
      tasks: [
        {
          ...tasks[0],
          id: 110,
          title: "Setting up repair sandbox",
          state: "running",
          activityBySetup: true,
          repairing: true,
        },
      ],
      narrowing: NO_NARROWING,
    });

    const repairDot = rowFor("Setting up repair sandbox").querySelector(
      ".badge",
    );
    expect(repairDot).toHaveClass("badge-running");
    expect(repairDot).toHaveClass("badge-repair");
    expect(repairDot).not.toHaveClass("badge-mark");
    expect(repairDot.querySelector(".grain-mark-sheet")).toBeNull();
  });

  it("does not animate things waiting in a queue", () => {
    renderList({
      tasks: [
        {
          ...tasks[0],
          id: 201,
          title: "In task queue",
          state: "queued",
        },
        {
          ...tasks[0],
          id: 202,
          title: "Waiting for grant",
          state: "queued",
          activityBySetup: true,
          activity: "waiting for Gemini API keys...",
        },
        {
          ...tasks[0],
          id: 203,
          title: "Waiting in merge queue",
          state: "completed",
          mergeQueueState: "waiting_queue",
        },
        {
          ...tasks[0],
          id: 204,
          title: "Waiting for review",
          state: "completed",
          mergeQueueState: "waiting_review",
        },
        {
          ...tasks[0],
          id: 205,
          title: "Gathering in merge queue",
          state: "completed",
          mergeQueueState: "gathering",
        },
        {
          ...tasks[0],
          id: 206,
          title: "Task waiting on repair queue",
          state: "completed",
          mergeQueueState: "repair_queued",
        },
      ],
      narrowing: NO_NARROWING,
    });

    for (const title of [
      "In task queue",
      "Waiting for grant",
      "Waiting in merge queue",
      "Waiting for review",
      "Gathering in merge queue",
      "Task waiting on repair queue",
    ]) {
      const dot = rowFor(title).querySelector(".badge");
      expect(dot).not.toHaveClass("badge-mark");
      expect(dot).not.toHaveClass("badge-running");
      expect(dot.querySelector(".grain-mark-sheet")).toBeNull();
    }
  });

  describe("search (bwsalmon/agents#460)", () => {
    it("filters down to tasks whose title matches the search text", async () => {
      const user = userEvent.setup();
      renderList();

      await user.type(screen.getByPlaceholderText("Search tasks…"), "ship");

      expect(screen.queryByText("Fix the thing")).not.toBeInTheDocument();
      expect(screen.getByText("Ship the other thing")).toBeInTheDocument();
      expect(
        document.querySelector(".content-header .count"),
      ).toHaveTextContent("1");
    });

    it("filters down to tasks whose id matches the search text, case-insensitively", async () => {
      const user = userEvent.setup();
      renderList({
        tasks: [
          {
            id: "abc-1",
            title: "Fix the thing",
            state: "queued",
            capabilities: [],
            blocked: false,
          },
          {
            id: "xyz-2",
            title: "Ship the other thing",
            state: "running",
            capabilities: [],
            blocked: false,
          },
        ],
      });

      await user.type(screen.getByPlaceholderText("Search tasks…"), "ABC");

      expect(screen.getByText("Fix the thing")).toBeInTheDocument();
      expect(
        screen.queryByText("Ship the other thing"),
      ).not.toBeInTheDocument();
    });

    it("shows a search-specific empty message when nothing matches", async () => {
      const user = userEvent.setup();
      renderList();

      await user.type(
        screen.getByPlaceholderText("Search tasks…"),
        "nothing matches this",
      );

      expect(
        screen.getByText("No tasks match your search."),
      ).toBeInTheDocument();
    });

    it("hides the toolbar entirely when there are no tasks at all", () => {
      renderList({ tasks: [] });
      expect(
        screen.queryByPlaceholderText("Search tasks…"),
      ).not.toBeInTheDocument();
    });
  });

  describe("sort (bwsalmon/agents#460)", () => {
    const threeTasks = [
      {
        id: 1,
        title: "Charlie",
        state: "queued",
        capabilities: [],
        blocked: false,
        createdAt: "2026-01-03T00:00:00Z",
      },
      {
        id: 2,
        title: "Alpha",
        state: "queued",
        capabilities: [],
        blocked: false,
        createdAt: "2026-01-01T00:00:00Z",
      },
      {
        id: 3,
        title: "Bravo",
        state: "queued",
        capabilities: [],
        blocked: false,
        createdAt: "2026-01-02T00:00:00Z",
      },
    ];

    function titlesInOrder() {
      return [...document.querySelectorAll(".task-title")].map(
        (el) => el.textContent,
      );
    }

    it("defaults to the given (backlog) order", () => {
      renderList({ tasks: threeTasks });
      expect(titlesInOrder()).toEqual(["Charlie", "Alpha", "Bravo"]);
    });

    it("sorts alphabetically by title when picked from the sort editor", async () => {
      const user = userEvent.setup();
      renderList({ tasks: threeTasks });

      await sortBy(user, "Title");

      expect(titlesInOrder()).toEqual(["Alpha", "Bravo", "Charlie"]);
    });

    it("sorts by createdAt, newest first to start with and oldest first on request", async () => {
      const user = userEvent.setup();
      renderList({ tasks: threeTasks });

      // A date is the one attribute that starts pointing the other way:
      // what somebody wants from "Created" is the recent end of it.
      await sortBy(user, "Created");
      expect(titlesInOrder()).toEqual(["Charlie", "Bravo", "Alpha"]);

      await user.click(screen.getByLabelText("Direction for sort 1"));
      await user.click(
        await screen.findByRole("option", { name: "Oldest first" }),
      );
      expect(titlesInOrder()).toEqual(["Alpha", "Bravo", "Charlie"]);
    });

    // Several keys are one sort rather than a list of alternatives: the
    // first decides and the rest break its ties, which is the whole
    // point of an editor over a single menu (grain/task-42).
    it("breaks the first key's ties with the second", async () => {
      const user = userEvent.setup();
      renderList({
        tasks: [
          {
            id: 1,
            title: "Charlie",
            state: "queued",
            repo: "acme/widgets",
            capabilities: [],
            blocked: false,
          },
          {
            id: 2,
            title: "Alpha",
            state: "running",
            repo: "acme/widgets",
            capabilities: [],
            blocked: false,
          },
          {
            id: 3,
            title: "Bravo",
            state: "queued",
            repo: "acme/gadgets",
            capabilities: [],
            blocked: false,
          },
        ],
      });

      await sortBy(user, "Repo");
      expect(titlesInOrder()).toEqual(["Bravo", "Charlie", "Alpha"]);

      await sortBy(user, "Title");
      expect(titlesInOrder()).toEqual(["Bravo", "Alpha", "Charlie"]);
      expect(
        screen.getByTitle("Choose the order this list is shown in"),
      ).toHaveTextContent("Repo A–Z, then Title A–Z");
    });

    // grain/task-288: the same menu now orders on the other attributes a
    // row shows -- state, repo, author -- with the backlog's own order
    // kept inside each group, since the sort is stable.
    it("sorts by state in the sidebar's own order", async () => {
      const user = userEvent.setup();
      renderList({
        tasks: [
          {
            id: 1,
            title: "Charlie",
            state: "completed",
            capabilities: [],
            blocked: false,
          },
          {
            id: 2,
            title: "Alpha",
            state: "proposed",
            capabilities: [],
            blocked: false,
          },
          {
            id: 3,
            title: "Bravo",
            state: "running",
            capabilities: [],
            blocked: false,
          },
        ],
      });

      await sortBy(user, "State");

      expect(titlesInOrder()).toEqual(["Alpha", "Bravo", "Charlie"]);
    });

    it("sorts by repo, with the tasks that have none last", async () => {
      const user = userEvent.setup();
      renderList({
        tasks: [
          {
            id: 1,
            title: "Charlie",
            state: "queued",
            capabilities: [],
            blocked: false,
          },
          {
            id: 2,
            title: "Alpha",
            state: "queued",
            repo: "acme/gadgets",
            capabilities: [],
            blocked: false,
          },
          {
            id: 3,
            title: "Bravo",
            state: "queued",
            repo: "acme/widgets",
            capabilities: [],
            blocked: false,
          },
        ],
      });

      await sortBy(user, "Repo");

      expect(titlesInOrder()).toEqual(["Alpha", "Bravo", "Charlie"]);
    });

    it("sorts by author", async () => {
      const user = userEvent.setup();
      renderList({
        tasks: [
          {
            id: 1,
            title: "Charlie",
            state: "queued",
            author: "grace",
            capabilities: [],
            blocked: false,
          },
          {
            id: 2,
            title: "Alpha",
            state: "queued",
            author: "ada",
            capabilities: [],
            blocked: false,
          },
          {
            id: 3,
            title: "Bravo",
            state: "queued",
            author: "bob",
            capabilities: [],
            blocked: false,
          },
        ],
      });

      await sortBy(user, "Author");

      expect(titlesInOrder()).toEqual(["Alpha", "Bravo", "Charlie"]);
    });

    it("disables drag-and-drop reordering once a non-backlog sort is chosen", async () => {
      const onReorder = vi.fn();
      const user = userEvent.setup();
      renderList({ tasks: threeTasks, onReorder });

      expect(rowFor("Charlie")).toHaveAttribute("draggable", "true");

      await sortBy(user, "Title");

      expect(
        document.querySelector(".task-drag-handle"),
      ).not.toBeInTheDocument();
      expect(rowFor("Alpha")).toHaveAttribute("draggable", "false");

      fireEvent.dragStart(rowFor("Alpha"));
      fireEvent.drop(rowFor("Bravo"));
      expect(onReorder).not.toHaveBeenCalled();
    });
  });

  // grain/task-288: the toolbar narrows on the attributes the sidebar's
  // own state filter says nothing about -- repo, base branch,
  // capability, author, how the task got filed, whether it is a chat,
  // whether it merges itself.
  describe("filter (grain/task-288)", () => {
    const config = {
      capabilities: [
        { id: "gh", name: "GitHub" },
        { id: "gcp", name: "Google Cloud" },
      ],
    };
    const mixed = [
      {
        id: 1,
        title: "Widget work",
        state: "queued",
        blocked: false,
        repo: "acme/widgets",
        base: "main",
        author: "ada",
        capabilities: ["gh"],
        autoMerge: true,
      },
      {
        id: 2,
        title: "Gadget work",
        state: "queued",
        blocked: false,
        repo: "acme/gadgets",
        base: "release",
        author: "grace",
        capabilities: ["gcp"],
        scheduled: true,
      },
      {
        id: 3,
        title: "Loose work",
        state: "queued",
        blocked: false,
        author: "ada",
        capabilities: [],
      },
    ];

    function titles() {
      return [...document.querySelectorAll(".task-title")].map(
        (el) => el.textContent,
      );
    }

    // These are about the editor itself -- which row is which, what the
    // menus offer, what a comparison does -- so they start from a list
    // narrowed by nothing at all rather than from the condition a view
    // opens with (taskFilters.js's defaultFilters, which "the states a
    // list opens without" above is about). Otherwise that condition
    // would be filter 1 and every row number here would be one out.
    const renderMixed = (props = {}) =>
      renderList({ tasks: mixed, config, narrowing: NO_NARROWING, ...props });

    it("narrows to one repo, and to the tasks with no repo at all", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Repo", "acme/widgets");
      expect(titles()).toEqual(["Widget work"]);

      await user.click(screen.getByLabelText("Value for filter 1"));
      await user.click(await screen.findByRole("option", { name: "No repo" }));
      await user.click(
        await screen.findByRole("option", { name: "acme/widgets" }),
      );
      await user.keyboard("{Escape}");
      expect(titles()).toEqual(["Loose work"]);
    });

    // The whole point of a multiselect: "either of these two" was not
    // sayable at all when an attribute had one menu with one value in it
    // (grain/task-42).
    it("narrows to any of several values in one condition", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Repo", "acme/widgets", "acme/gadgets");

      expect(titles()).toEqual(["Widget work", "Gadget work"]);
    });

    it("narrows to everything a value is not", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Repo");
      await user.click(screen.getByLabelText("Comparison for filter 1"));
      await user.click(
        await screen.findByRole("option", { name: "is none of" }),
      );
      await user.click(screen.getByLabelText("Value for filter 1"));
      await user.click(
        await screen.findByRole("option", { name: "acme/widgets" }),
      );
      await user.keyboard("{Escape}");

      expect(titles()).toEqual(["Gadget work", "Loose work"]);
    });

    it("narrows to one capability, by the name the deployment gives it", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Capability", "Google Cloud");

      expect(titles()).toEqual(["Gadget work"]);
    });

    // "has all of" is the comparison only a multi-valued attribute
    // needs: a task with one of the two capabilities is not a task with
    // both of them.
    it("narrows to the tasks holding every capability named", async () => {
      const user = userEvent.setup();
      renderMixed({
        tasks: [
          { ...mixed[0], capabilities: ["gh", "gcp"] },
          { ...mixed[1], capabilities: ["gcp"] },
        ],
        config,
      });

      await filterBy(user, "Capability");
      await user.click(screen.getByLabelText("Comparison for filter 1"));
      await user.click(
        await screen.findByRole("option", { name: "has all of" }),
      );
      await user.click(screen.getByLabelText("Value for filter 1"));
      await user.click(await screen.findByRole("option", { name: "GitHub" }));
      await user.click(
        await screen.findByRole("option", { name: "Google Cloud" }),
      );
      await user.keyboard("{Escape}");

      expect(titles()).toEqual(["Widget work"]);
    });

    it("narrows on a word in the title", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Title");
      await user.type(screen.getByLabelText("Value for filter 1"), "gadget");

      expect(titles()).toEqual(["Gadget work"]);
    });

    it("narrows to what was created in the last so many days", async () => {
      const user = userEvent.setup();
      const day = 24 * 60 * 60 * 1000;
      renderMixed({
        tasks: [
          { ...mixed[0], createdAt: new Date(Date.now() - day).toISOString() },
          {
            ...mixed[1],
            createdAt: new Date(Date.now() - 30 * day).toISOString(),
          },
        ],
        config,
      });

      await filterBy(user, "Created");
      await user.type(screen.getByLabelText("Value for filter 1"), "7");

      expect(titles()).toEqual(["Widget work"]);
    });

    it("narrows to the tasks an attribute is set on, and the ones it is not", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Repo");
      await user.click(screen.getByLabelText("Comparison for filter 1"));
      await user.click(await screen.findByRole("option", { name: "is empty" }));
      // "is empty" compares against nothing, so the value control goes
      // away with the comparison that wanted it.
      expect(screen.queryByLabelText("Value for filter 1")).toBeNull();
      expect(titles()).toEqual(["Loose work"]);

      await user.click(screen.getByLabelText("Comparison for filter 1"));
      await user.click(await screen.findByRole("option", { name: "is set" }));
      expect(titles()).toEqual(["Widget work", "Gadget work"]);
    });

    it("narrows to how the task was filed", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Origin", "Scheduled");
      expect(titles()).toEqual(["Gadget work"]);
    });

    // grain/task-47: the attributes that offer values the app keeps a
    // list of -- every value of the Repo menu is a repo, every value of
    // Base is a branch (grain/task-372), and the schedule and the suite
    // are among the origins -- carry the same figures the nav rail and
    // the task rows do (ItemGlyph.jsx, docs/brand.md). The rows that
    // name nothing a figure stands for ("No repo", "Repo default",
    // "Filed by hand") hold the slot open and empty, so one menu's
    // labels stay in one column.
    it("glyphs the value rows that name a repo, a branch, a schedule or a suite", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Repo");
      await user.click(screen.getByLabelText("Value for filter 1"));
      expect(
        (
          await screen.findByRole("option", { name: "acme/widgets" })
        ).querySelector('svg[data-glyph="repos"]'),
      ).toBeInTheDocument();
      expect(
        screen
          .getByRole("option", { name: "No repo" })
          .querySelector("svg[data-glyph]"),
      ).toBeNull();
      await user.keyboard("{Escape}");

      await filterBy(user, "Base");
      await user.click(screen.getByLabelText("Value for filter 2"));
      expect(
        (await screen.findByRole("option", { name: "main" })).querySelector(
          'svg[data-glyph="branches"]',
        ),
      ).toBeInTheDocument();
      expect(
        screen
          .getByRole("option", { name: "Repo default" })
          .querySelector("svg[data-glyph]"),
      ).toBeNull();
      await user.keyboard("{Escape}");

      await filterBy(user, "Origin");
      await user.click(screen.getByLabelText("Value for filter 3"));
      expect(
        (
          await screen.findByRole("option", { name: "Scheduled" })
        ).querySelector('svg[data-glyph="schedules"]'),
      ).toBeInTheDocument();
      expect(
        screen
          .getByRole("option", { name: "Filed by hand" })
          .querySelector("svg[data-glyph]"),
      ).toBeNull();
    });

    it("narrows to the tasks that merge themselves", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Auto-merge", "On");

      expect(titles()).toEqual(["Widget work"]);
    });

    // A row that has been added but not answered yet is not a filter
    // that matches nothing: it is a question still being written.
    it("narrows nothing until the condition has a value", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Repo");

      expect(titles()).toEqual(["Widget work", "Gadget work", "Loose work"]);
    });

    it("combines conditions with each other and with the search box", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Author", "ada");
      await filterBy(user, "Auto-merge", "Off");
      expect(titles()).toEqual(["Loose work"]);

      await user.type(screen.getByPlaceholderText("Search tasks…"), "widget");
      expect(titles()).toEqual([]);
      expect(
        screen.getByText("No tasks match your search."),
      ).toBeInTheDocument();
    });

    // Two conditions on one attribute are a range, which is the other
    // thing one menu per attribute could never say.
    it("takes more than one condition on the same attribute", async () => {
      const user = userEvent.setup();
      renderMixed({
        tasks: [
          { ...mixed[0], createdAt: "2026-01-01T12:00:00Z" },
          { ...mixed[1], createdAt: "2026-02-01T12:00:00Z" },
          { ...mixed[2], createdAt: "2026-03-01T12:00:00Z" },
        ],
        config,
      });

      await filterBy(user, "Created");
      await user.click(screen.getByLabelText("Comparison for filter 1"));
      await user.click(await screen.findByRole("option", { name: "is after" }));
      fireEvent.change(screen.getByLabelText("Value for filter 1"), {
        target: { value: "2026-01-15" },
      });

      await filterBy(user, "Created");
      await user.click(screen.getByLabelText("Comparison for filter 2"));
      await user.click(
        await screen.findByRole("option", { name: "is before" }),
      );
      fireEvent.change(screen.getByLabelText("Value for filter 2"), {
        target: { value: "2026-02-15" },
      });

      expect(titles()).toEqual(["Gadget work"]);
    });

    // Changing the attribute of a row keeps the comparison where the new
    // attribute offers it too, and never keeps the values: a repo is not
    // an author.
    it("keeps a condition answerable when its attribute changes", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Repo", "acme/widgets");
      expect(titles()).toEqual(["Widget work"]);

      await user.click(screen.getByLabelText("Attribute for filter 1"));
      await user.click(await screen.findByRole("option", { name: "Author" }));

      expect(
        screen.getByLabelText("Comparison for filter 1"),
      ).toHaveTextContent("is any of");
      expect(titles()).toEqual(["Widget work", "Gadget work", "Loose work"]);
    });

    it("says the filters are what emptied the list", async () => {
      const user = userEvent.setup();
      renderMixed({
        tasks: [
          { ...mixed[0], capabilities: [] },
          { ...mixed[1], capabilities: ["gcp"] },
        ],
        config,
      });

      await filterBy(user, "Repo", "acme/widgets");
      await filterBy(user, "Capability", "Google Cloud");

      expect(
        screen.getByText("No tasks match these filters."),
      ).toBeInTheDocument();
    });

    it("puts the whole list back with one Clear", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Repo", "acme/widgets");
      await user.type(screen.getByPlaceholderText("Search tasks…"), "widget");
      expect(titles()).toEqual(["Widget work"]);

      await user.click(screen.getByRole("button", { name: "Clear" }));

      expect(titles()).toEqual(["Widget work", "Gadget work", "Loose work"]);
      expect(
        screen.queryByRole("button", { name: "Clear" }),
      ).not.toBeInTheDocument();
    });

    // A narrowed list says what it is narrowed by without anybody having
    // to open the editor, and each condition is removable where it
    // stands.
    it("leaves every condition on screen as a chip once the editor is closed", async () => {
      const user = userEvent.setup();
      renderMixed();

      await filterBy(user, "Repo", "acme/widgets", "acme/gadgets");
      await user.click(screen.getByTitle(FILTER_BUTTON));

      const chip = screen.getByText(
        "Repo is any of acme/widgets, acme/gadgets",
      );
      expect(chip).toBeInTheDocument();
      expect(screen.getByTitle(FILTER_BUTTON).textContent).toContain(
        "Filters (1)",
      );

      await user.click(chip.parentElement.querySelector(".MuiChip-deleteIcon"));
      expect(titles()).toEqual(["Widget work", "Gadget work", "Loose work"]);
    });

    // The menus are built from the tasks in view, so a value the state
    // filter is already hiding is never offered.
    it("offers only the values the tasks in view carry", async () => {
      const user = userEvent.setup();
      renderMixed({ tasks: mixed, config, stateFilter: "queued" });

      await filterBy(user, "Base");
      await user.click(screen.getByLabelText("Value for filter 1"));

      expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
        "main",
        "release",
        "Repo default",
      ]);
    });

    // A condition naming an attribute this build does not have -- a
    // custom one the deployment has dropped, or a link from a newer
    // build -- opens the list it names rather than an empty one.
    it("ignores a condition on an attribute it has never heard of", () => {
      renderMixed({
        tasks: mixed,
        config,
        narrowing: {
          search: "",
          sort: [],
          filters: [{ attr: "custom.team", op: "any", values: ["infra"] }],
        },
      });

      expect(titles()).toEqual(["Widget work", "Gadget work", "Loose work"]);
    });
  });

  // grain/task-175: the prompt an agent was handed is reached from the
  // task's own page (DetailOverlay's Prompt button), not from a button
  // on every row of every list the task appears in -- so a row carries
  // no prompt affordance at all, and TaskList never fetches one.
  it("carries no per-row prompt button", () => {
    renderList();
    expect(
      screen.queryByRole("button", { name: /prompt/i }),
    ).not.toBeInTheDocument();
  });

  // A deployment's own fields on the rows themselves (grain/task-100):
  // the field the list is sorted by, so the order it is in is one a
  // reader can see, and the fields declared as belonging on every row.
  describe("a declared field's value on the row", () => {
    const fieldsConfig = {
      customFields: [
        {
          name: "priority",
          label: "Priority",
          type: "enum",
          options: ["high", "low"],
        },
        { name: "customer" },
      ],
    };
    const withValues = [
      {
        id: 1,
        title: "Fix the thing",
        state: "queued",
        capabilities: [],
        blocked: false,
        customFields: { priority: "high", customer: "acme" },
      },
      {
        id: 2,
        title: "Ship the other thing",
        state: "queued",
        capabilities: [],
        blocked: false,
      },
    ];

    // The field chips on one row, by the "Label: value" each carries as
    // its title -- the whole of what it says, unclipped.
    const fieldChips = (title) =>
      [
        ...screen
          .getByText(title)
          .closest(".task-row")
          .querySelectorAll(".chip-field"),
      ].map((el) => el.getAttribute("title"));

    it("says nothing about a field nothing asks to see", () => {
      renderList({ tasks: withValues, config: fieldsConfig });
      expect(fieldChips("Fix the thing")).toEqual([]);
    });

    it("shows the field the list has been sorted by", async () => {
      const user = userEvent.setup();
      renderList({ tasks: withValues, config: fieldsConfig });

      await sortBy(user, "Priority");

      expect(fieldChips("Fix the thing")).toEqual(["Priority: high"]);
      // A task that says nothing about the field carries no chip: it is
      // already saying so by sorting to the bottom.
      expect(fieldChips("Ship the other thing")).toEqual([]);
    });

    it("shows a field the deployment declared for every row", () => {
      renderList({
        tasks: withValues,
        config: {
          customFields: [
            { ...fieldsConfig.customFields[0] },
            { name: "customer", label: "Customer", showOnRow: true },
          ],
        },
      });
      expect(fieldChips("Fix the thing")).toEqual(["Customer: acme"]);
    });
  });

  // What the run itself says it is doing (grain/task-240, the
  // update_status tool): the one thing on a row that changes while you
  // watch it, and the only answer to "what has this task been doing for
  // the last half hour?" short of reading a transcript.
  describe("a running task's own status", () => {
    const running = (extra) => ({
      id: 7,
      title: "Ship the other thing",
      state: "running",
      capabilities: [],
      blocked: false,
      ...extra,
    });

    it("shows what the run says it is doing, with how long ago it said it", () => {
      renderList({
        tasks: [
          running({
            activity: "waiting for CI on the second push",
            activityAt: new Date(Date.now() - 22 * 60 * 1000).toISOString(),
          }),
        ],
      });
      expect(
        screen.getByText("waiting for CI on the second push"),
      ).toBeInTheDocument();
      expect(screen.getByText("22m ago")).toBeInTheDocument();
    });

    // The age is spent only where it says something. A line a run wrote
    // a moment ago is current -- which is what somebody reading it
    // assumes anyway -- so dating it would put a number on every running
    // row to tell nobody anything (state.js's activityAge).
    it("leaves a status the run has just written undated", () => {
      renderList({
        tasks: [
          running({
            activity: "reading pkg/orchestrator",
            activityAt: new Date(Date.now() - 30 * 1000).toISOString(),
          }),
        ],
      });
      expect(screen.getByText("reading pkg/orchestrator")).toBeInTheDocument();
      expect(document.querySelector(".task-activity-age")).toBeNull();
    });

    // A synopsis with no timestamp -- a row written by an older build --
    // is shown alone rather than with an invented age.
    it("shows a status with no timestamp on its own", () => {
      renderList({ tasks: [running({ activity: "running the test suite" })] });
      expect(screen.getByText("running the test suite")).toBeInTheDocument();
      expect(
        document.querySelector(".task-activity-age"),
      ).not.toBeInTheDocument();
    });

    // The API only carries one for a live run, but a poll's answer is up
    // to three seconds old: a task that has just finished must not keep
    // showing what it was doing, which beside a "Completed" badge would
    // read as a run still going.
    it("drops the status the moment the task stops running", () => {
      renderList({
        tasks: [
          running({
            state: "completed",
            activity: "waiting for CI on the second push",
          }),
        ],
      });
      expect(
        screen.queryByText("waiting for CI on the second push"),
      ).not.toBeInTheDocument();
    });

    it("says nothing at all for a run that has not said anything", () => {
      renderList({ tasks: [running()] });
      expect(document.querySelector(".task-activity")).not.toBeInTheDocument();
    });

    // grain/task-295: the stretch before the agent's first turn -- the
    // sandbox being built, the repo cloned -- is narrated by grain
    // itself, since there is no agent yet to narrate it. Everything else
    // that has ever appeared here was an agent's own words, so whose
    // sentence this is is marked rather than left to the wording.
    it("marks a status grain wrote during setup as grain's own", () => {
      renderList({
        tasks: [
          running({ activity: "cloning acme/widgets", activityBySetup: true }),
        ],
      });
      expect(screen.getByText("cloning acme/widgets")).toBeInTheDocument();
      expect(document.querySelector(".task-activity-by")).toHaveTextContent(
        "grain",
      );
      expect(document.querySelector(".task-activity")).toHaveAttribute(
        "title",
        expect.stringContaining("What grain is doing to get this run started"),
      );
    });

    it("leaves the run's own status unmarked", () => {
      renderList({ tasks: [running({ activity: "running the test suite" })] });
      expect(
        document.querySelector(".task-activity-by"),
      ).not.toBeInTheDocument();
      expect(document.querySelector(".task-activity")).toHaveAttribute(
        "title",
        expect.stringContaining("What this run says it is doing"),
      );
    });

    it("keeps a waiting queued task simply queued in the row text and puts the wait reason on the icon hover", () => {
      const waitReason =
        "waiting: all connections serving claude-sonnet-4-6 are busy (1 busy: 4/4 in use)";
      renderList({
        tasks: [
          {
            ...tasks[0],
            id: 301,
            title: "Waiting in queue",
            state: "queued",
            activity: waitReason,
            activityBySetup: true,
          },
        ],
      });
      const row = rowFor("Waiting in queue");
      expect(row.querySelector(".task-activity")).toBeNull();
      expect(screen.queryByText(waitReason)).not.toBeInTheDocument();
      const badge = row.querySelector(".badge-icon");
      expect(badge).toHaveAccessibleName("Waiting");
      expect(badge).toHaveAttribute(
        "title",
        `Waiting: ${waitReason.replace(/^waiting: /, "")}`,
      );
    });
  });

  it("opens the Contact running agents dialog from the toolbar with only running tasks", async () => {
    const user = userEvent.setup();
    renderList();

    const btn = screen.getByRole("button", { name: "Contact running (1)" });
    await user.click(btn);

    expect(
      screen.getByRole("heading", { name: "Contact running agents" }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("notify-running-summary")).toHaveTextContent(
      "1 running task agent will receive this notice:",
    );
    expect(screen.getByText("#2 Ship the other thing")).toBeInTheDocument();
  });
});

// The operator's ask on grain task 1402: a row parked on a question says
// what it asked on its status line, as much of the summary as fits.
describe("a task parked on a question", () => {
  it("puts the question's summary on the row's status line", () => {
    renderList({
      tasks: [
        {
          id: "77",
          title: "Retire the exporter",
          capabilities: [],
          blocked: false,
          state: "awaiting_reply",
          questionSummary: "Keep the v1 API or remove it?",
        },
      ],
    });
    const note = document.querySelector(".task-activity-note");
    expect(note).toHaveTextContent("Keep the v1 API or remove it?");
    expect(note.closest(".task-activity")).toHaveAttribute(
      "title",
      "Asked: Keep the v1 API or remove it?",
    );
  });

  it("says nothing on the status line of a stopped turn", () => {
    renderList({
      tasks: [
        {
          id: "77",
          title: "Retire the exporter",
          capabilities: [],
          blocked: false,
          state: "awaiting_reply",
          turnStopped: true,
          questionSummary: "Stopped",
        },
      ],
    });
    expect(document.querySelector(".task-activity-note")).toBeNull();
  });
});
