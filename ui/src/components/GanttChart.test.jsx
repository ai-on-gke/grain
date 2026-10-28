import { useState } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import GanttChart from "./GanttChart.jsx";
import { GANTT_STORAGE_KEY } from "../gantt.js";
import { NO_NARROWING } from "../taskFilters.js";

// A fixed current moment, so a bar's position is a fact a test can
// assert rather than a race with the clock -- which is exactly why the
// chart takes `now` as a prop.
const NOW = Date.UTC(2026, 8, 6, 12, 0, 0);
const HOUR = 60 * 60 * 1000;
const DAY = 24 * HOUR;
const iso = (ms) => new Date(ms).toISOString();

const tasks = [
  {
    id: 1,
    title: "Fix the thing",
    state: "completed",
    repo: "acme/widgets",
    author: "alice",
    capabilities: [],
    blocked: false,
    createdAt: iso(NOW - 6 * HOUR),
    startedAt: iso(NOW - 5 * HOUR),
    endedAt: iso(NOW - 4 * HOUR),
  },
  {
    id: 2,
    title: "Ship the other thing",
    state: "running",
    repo: "acme/gadgets",
    author: "bob",
    capabilities: [],
    blocked: false,
    createdAt: iso(NOW - 3 * HOUR),
    startedAt: iso(NOW - 2 * HOUR),
  },
  {
    id: 3,
    title: "Ancient history",
    state: "closed",
    repo: "acme/widgets",
    author: "alice",
    capabilities: [],
    blocked: false,
    createdAt: iso(NOW - 40 * DAY),
    startedAt: iso(NOW - 40 * DAY),
    endedAt: iso(NOW - 39 * DAY),
  },
];

// Narrowed holds what the toolbar is asking, the way App does -- the
// search, sort and filters live there and in the URL, not in the view.
function Narrowed({ props }) {
  const [narrowing, setNarrowing] = useState(props.narrowing || NO_NARROWING);
  return (
    <GanttChart
      {...props}
      narrowing={narrowing}
      onNarrow={(patch) => setNarrowing((prev) => ({ ...prev, ...patch }))}
    />
  );
}

function renderChart(overrides = {}) {
  const props = {
    tasks,
    config: null,
    onOpenTask: vi.fn(),
    now: NOW,
    ...overrides,
  };
  render(<Narrowed props={props} />);
  return props;
}

// rows are the task rows on screen, top to bottom, by the title each one
// carries.
const rows = () =>
  [...document.querySelectorAll(".gantt-row .gantt-row-title")].map(
    (el) => el.textContent,
  );

const barOf = (title) =>
  [...document.querySelectorAll(".gantt-row")]
    .find((row) => within(row).queryByText(title))
    .querySelector(".gantt-bar");

describe("GanttChart", () => {
  beforeEach(() => localStorage.clear());

  // The whole point: every task in the range gets a bar, positioned
  // against one axis, so two tasks' lengths are comparable by eye.
  it("draws a bar per task, placed in the window", () => {
    renderChart();
    expect(rows()).toEqual(["Fix the thing", "Ship the other thing"]);
    // Six hours back in a seven-day window is most of the way along it.
    const bar = barOf("Fix the thing");
    expect(parseFloat(bar.style.left)).toBeCloseTo(
      ((7 * DAY - 6 * HOUR) / (7 * DAY)) * 100,
      1,
    );
    expect(parseFloat(bar.style.width)).toBeCloseTo(
      ((2 * HOUR) / (7 * DAY)) * 100,
      1,
    );
  });

  // A timeline row stays a plain listitem, and its title is the link a
  // keyboard opens its task by, as on a task row (grain/task-1582).
  it("opens a task from the keyboard", async () => {
    const user = userEvent.setup();
    const { onOpenTask } = renderChart();
    const row = barOf("Ship the other thing").closest(".gantt-row");
    expect(row.tagName).toBe("LI");
    expect(row).not.toHaveAttribute("tabindex");

    const link = within(row).getByRole("link", {
      name: "Ship the other thing",
    });
    expect(link).toHaveAttribute("href", "/tasks/2");
    link.focus();
    await user.keyboard("{Enter}");
    expect(onOpenTask).toHaveBeenLastCalledWith(2);
    expect(onOpenTask).toHaveBeenCalledTimes(1);
  });

  // A bar is no control, so a click on it used to leave focus on <body>
  // for the task pane to hand back on closing (2026-10-15 boards
  // walkthrough). The row's link takes it first.
  it("puts focus on the row's link when its bar is clicked", async () => {
    const user = userEvent.setup();
    const { onOpenTask } = renderChart();
    const bar = barOf("Ship the other thing");
    await user.click(bar);
    expect(onOpenTask).toHaveBeenLastCalledWith(2);
    expect(
      within(bar.closest(".gantt-row")).getByRole("link", {
        name: "Ship the other thing",
      }),
    ).toHaveFocus();
  });

  // A running task's bar reaches the current moment and is marked as
  // still going, because a bar that stops at the edge because the task
  // finished then is the opposite fact.
  it("marks an unfinished bar as ongoing", () => {
    renderChart();
    expect(barOf("Ship the other thing").className).toContain(
      "gantt-bar-ongoing",
    );
    expect(barOf("Fix the thing").className).not.toContain("gantt-bar-ongoing");
  });

  // The stretch before dispatch is drawn inside the bar, so the length
  // being compared is still one bar with the wait visible in it.
  it("marks the wait before the first attempt inside the bar", () => {
    renderChart();
    expect(
      barOf("Fix the thing").querySelector(".gantt-bar-wait"),
    ).not.toBeNull();
  });

  // What a range leaves out is said out loud: a chart that is mostly not
  // showing things gives a reader no reason to reach for the control
  // unless it admits it.
  it("says how many tasks the range leaves out", () => {
    renderChart();
    expect(screen.getByText("1 task outside this range.")).toBeInTheDocument();
  });

  // A chart drawn as a panel is a window onto the view, not the view: it
  // draws what it was budgeted and says how many more its range holds,
  // rather than putting hundreds of rows inside a box a few hundred
  // pixels tall (grain/task-311).
  it("draws no more rows than it was budgeted, and says how many more", () => {
    renderChart({ maxRows: 1 });
    expect(rows()).toEqual(["Fix the thing"]);
    // Two facts, not one: the budget left a bar out of a range that
    // holds it, and the range left a task out altogether.
    expect(
      screen.getByText(
        "Showing 1 of 2 in this range. 1 task outside this range.",
      ),
    ).toBeInTheDocument();
  });

  // The budget is a ceiling rather than a count: a range holding fewer
  // bars than the panel can draw says nothing at all.
  it("says nothing when the range fits inside the budget", () => {
    renderChart({ maxRows: 10, tasks: [tasks[0], tasks[1]] });
    expect(rows()).toEqual(["Fix the thing", "Ship the other thing"]);
    expect(screen.queryByText(/Showing/)).toBeNull();
  });

  // The full view has no budget: the pane it fills is where somebody
  // goes to scroll through everything there is.
  it("draws every row in the range when it has no budget", () => {
    renderChart();
    expect(rows()).toEqual(["Fix the thing", "Ship the other thing"]);
    expect(screen.queryByText(/Showing/)).toBeNull();
  });

  // The range is the operator's, and it is remembered -- this browser's
  // preference, kept where the board's columns are.
  it("widens to a range that includes everything, and remembers it", async () => {
    const user = userEvent.setup();
    renderChart();
    await user.click(screen.getByLabelText("Range"));
    await user.click(screen.getByRole("option", { name: "Everything" }));
    expect(rows()).toContain("Ancient history");
    expect(screen.queryByText(/outside this range/)).toBeNull();
    expect(JSON.parse(localStorage.getItem(GANTT_STORAGE_KEY)).range).toBe(
      "all",
    );
  });

  // Lanes are the other half of that: the same bars, grouped by
  // something worth asking about, with the grouping remembered too.
  it("groups the rows into lanes and remembers the choice", async () => {
    const user = userEvent.setup();
    renderChart();
    await user.click(screen.getByLabelText("Lanes"));
    await user.click(screen.getByRole("option", { name: "By repo" }));
    const lanes = [...document.querySelectorAll(".gantt-lane-title")].map(
      (el) => el.textContent,
    );
    expect(lanes).toEqual(["acme/gadgets", "acme/widgets"]);
    expect(JSON.parse(localStorage.getItem(GANTT_STORAGE_KEY)).lanes).toBe(
      "repo",
    );
  });

  it("opens the task a row is clicked on", async () => {
    const user = userEvent.setup();
    const props = renderChart();
    await user.click(screen.getByText("Fix the thing"));
    expect(props.onOpenTask).toHaveBeenCalledWith(1);
  });

  // The chart narrows by the same toolbar as the list and the board, so
  // a question asked on one is still being asked here.
  it("narrows by the shared toolbar", async () => {
    const user = userEvent.setup();
    renderChart();
    await user.type(screen.getByPlaceholderText(/Search/), "other");
    expect(rows()).toEqual(["Ship the other thing"]);
  });

  // Two empty charts, two different sentences: a range with nothing in
  // it is a range to widen, and a deployment with no tasks is not.
  it("says which kind of empty it is", () => {
    renderChart({ tasks: [tasks[2]] });
    expect(
      screen.getByText("No tasks in this range. Try a wider one."),
    ).toBeInTheDocument();
    document.body.innerHTML = "";
    renderChart({ tasks: [] });
    expect(screen.getByText("No tasks yet.")).toBeInTheDocument();
  });

  // A task with no dates at all cannot be drawn against a clock. It is
  // left off rather than pinned to the epoch, and counted as off the
  // chart like anything else the window does not hold.
  it("leaves out a task it has no dates for", () => {
    renderChart({
      tasks: [
        tasks[0],
        { id: 9, title: "Undated", state: "queued", capabilities: [] },
      ],
    });
    expect(rows()).toEqual(["Fix the thing"]);
    expect(screen.getByText("1 task outside this range.")).toBeInTheDocument();
  });
  // A blocked task is queued, but on a dependency rather than on the
  // dispatcher: its row carries the chip a list row and a card do, and
  // its bar and tooltip say "blocked" -- naming the blocker -- instead of
  // reading as a plain dispatch backlog.
  it("marks a blocked task as blocked", () => {
    renderChart({
      tasks: [
        tasks[1],
        {
          id: 11,
          title: "Extract the helper",
          state: "queued",
          capabilities: [],
          blocked: true,
          blockedBy: [2],
          createdAt: iso(NOW - HOUR),
        },
      ],
    });
    const row = screen
      .getByRole("link", { name: "Extract the helper" })
      .closest(".gantt-row");
    // No separate chip: the row's state badge is the blocked oval, and
    // its title names what the task waits on.
    expect(within(row).queryByText("Blocked")).toBeNull();
    const badge = row.querySelector(".badge");
    expect(badge).toHaveClass("badge-blocked");
    expect(badge).toHaveAttribute("title", "Blocked on: #2 (running)");
    expect(within(row).getByRole("img")).toHaveAttribute(
      "aria-label",
      expect.stringMatching(/^Waiting, blocked: /),
    );
    expect(row).toHaveAttribute(
      "title",
      expect.stringContaining("Blocked on: #2 (running)"),
    );

    const other = screen
      .getByRole("link", { name: "Ship the other thing" })
      .closest(".gantt-row");
    expect(within(other).queryByText("Blocked")).toBeNull();
    expect(other.querySelector(".badge")).not.toHaveClass("badge-blocked");
  });
});
