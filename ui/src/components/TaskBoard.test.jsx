import { useState } from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import TaskBoard from "./TaskBoard.jsx";
import { BOARD_STORAGE_KEY } from "../board.js";
import { NO_NARROWING } from "../taskFilters.js";

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
    blocked: false,
  },
  {
    id: 3,
    title: "Answer me",
    state: "awaiting_reply",
    capabilities: [],
    blocked: false,
  },
  {
    id: 4,
    title: "Long done",
    state: "closed",
    capabilities: [],
    blocked: false,
  },
];

// Narrowed holds what the toolbar is asking, which is App's job now
// that the search text, the sort order and the filters live there and
// in the URL (grain/task-317) rather than inside the board itself.
function Narrowed({ props }) {
  const [narrowing, setNarrowing] = useState(props.narrowing || NO_NARROWING);
  return (
    <TaskBoard
      {...props}
      narrowing={narrowing}
      onNarrow={(patch) => setNarrowing((prev) => ({ ...prev, ...patch }))}
    />
  );
}

function renderBoard(overrides = {}) {
  const props = {
    tasks,
    config: null,
    onOpenTask: vi.fn(),
    selected: new Set(),
    onToggleSelect: vi.fn(),
    onSelectAll: vi.fn(),
    ...overrides,
  };
  const { rerender } = render(<Narrowed props={props} />);
  return {
    ...props,
    rerender: (next) => rerender(<Narrowed props={{ ...props, ...next }} />),
  };
}

// column finds a column by the title in its own header, and cardsIn the
// task titles inside it, in order.
function column(title) {
  // By its header: a card's own state badge can read the same word.
  return [...document.querySelectorAll(".board-column")].find(
    (el) => el.querySelector(".board-column-title")?.textContent === title,
  );
}

function cardsIn(title) {
  return [...column(title).querySelectorAll(".board-card-title")].map(
    (el) => el.textContent,
  );
}

function cardFor(text) {
  return screen.getByText(text).closest("li");
}

// The board narrows itself with the same toolbar the list does
// (NarrowingBar.jsx), so these are TaskList.test.jsx's own gestures:
// open the editor, add a condition or a sort key, answer it.
const FILTER_BUTTON = "Add and edit the conditions this list is narrowed by";
const SORT_BUTTON = "Choose the order this list is shown in";

async function filterBy(user, attribute, ...values) {
  const labels = /^(Filter by…|Add filter)$/;
  if (!screen.queryByRole("button", { name: labels }))
    await user.click(screen.getByTitle(FILTER_BUTTON));
  await user.click(screen.getByRole("button", { name: labels }));
  await user.click(await screen.findByRole("menuitem", { name: attribute }));
  if (values.length > 0) {
    await user.click(screen.getByLabelText("Value for filter 1"));
    for (const value of values) {
      await user.click(await screen.findByRole("option", { name: value }));
    }
    await user.keyboard("{Escape}");
  }
}

async function sortBy(user, attribute) {
  await user.click(screen.getByTitle(SORT_BUTTON));
  await user.click(screen.getByRole("button", { name: "Sort by…" }));
  await user.click(await screen.findByRole("menuitem", { name: attribute }));
}

describe("TaskBoard", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  describe("the default board", () => {
    it("puts each task in the column its state belongs to", () => {
      renderBoard();
      expect(cardsIn("Waiting")).toEqual(["Fix the thing"]);
      expect(cardsIn("Running")).toEqual(["Ship the other thing"]);
      expect(cardsIn("Asked")).toEqual(["Answer me"]);
    });

    it("keeps a blocked task in its state's column, wearing the blocked oval", () => {
      renderBoard({
        tasks: tasks.map((task) =>
          task.id === 1 ? { ...task, blocked: true, blockedBy: [2] } : task,
        ),
      });
      expect(cardsIn("Waiting")).toEqual(["Fix the thing"]);
      const card = cardFor("Fix the thing");
      const badge = card.querySelector(".badge");
      expect(badge).toHaveClass("badge-blocked");
      expect(badge).toHaveAttribute("title", "Blocked on: #2 (running)");
      expect(card.querySelector(".MuiChip-root")).toBeNull();
      // An unblocked card in a one-state column still shows no badge.
      expect(
        cardFor("Ship the other thing").querySelector(".badge"),
      ).toBeNull();
    });

    // Sessions live in the Sessions pane (SessionsPage.jsx), so an
    // interactive task is never a card, live or not.
    it("leaves an interactive task off the board", () => {
      renderBoard({
        tasks: tasks.map((task) =>
          task.id === 2 ? { ...task, interactive: true } : task,
        ),
      });

      expect(screen.queryByText("Ship the other thing")).toBeNull();
    });

    it("leaves closed tasks off the board, and says how many it is not showing", () => {
      renderBoard();
      expect(screen.queryByText("Long done")).not.toBeInTheDocument();
      expect(
        screen.getByText(/1 task in no column \(Closed\)/),
      ).toBeInTheDocument();
    });

    // The default board has no column for "Deferred" either (board.js),
    // and the note names it only when there is a deferred task to be
    // hiding -- the board above has none, and says "Closed" alone.
    it("leaves deferred tasks off the board too, and names that state as well", () => {
      renderBoard({
        tasks: [
          ...tasks,
          {
            id: 9,
            title: "Not this month",
            state: "deferred",
            capabilities: [],
            blocked: false,
          },
        ],
      });
      expect(screen.queryByText("Not this month")).not.toBeInTheDocument();
      expect(
        screen.getByText(/2 tasks in no column \(Deferred, Closed\)/),
      ).toBeInTheDocument();
    });

    it("counts what is on the board, and what is in each column", () => {
      renderBoard();
      expect(
        document.querySelector(".content-header .count"),
      ).toHaveTextContent("3");
      expect(column("Waiting").querySelector(".count")).toHaveTextContent("1");
      expect(column("Proposed").querySelector(".count")).toHaveTextContent("0");
    });

    it("shows every column, including the ones with nothing in them", () => {
      renderBoard();
      expect(column("Proposed")).toBeInTheDocument();
      expect(column("Proposed")).toHaveTextContent("Nothing here.");
    });

    it("opens a task when its card is clicked", async () => {
      const user = userEvent.setup();
      const { onOpenTask } = renderBoard();
      await user.click(screen.getByText("Fix the thing"));
      expect(onOpenTask).toHaveBeenCalledWith(1);
    });

    it("puts focus on the card's link when the card is clicked off its title", async () => {
      const user = userEvent.setup();
      const { onOpenTask } = renderBoard();
      const card = screen.getByText("Fix the thing").closest(".board-card");
      await user.click(card);
      expect(onOpenTask).toHaveBeenCalledWith(1);
      expect(
        within(card).getByRole("link", { name: "Fix the thing" }),
      ).toHaveFocus();
    });

    it("opens a task from the keyboard, and leaves the card's checkbox its own keys", async () => {
      const user = userEvent.setup();
      const { onOpenTask, onToggleSelect } = renderBoard();
      // The card is no control itself -- it holds a checkbox and a move
      // handle -- so its title is the link a keyboard opens it by
      // (grain/task-1582).
      const card = screen.getByText("Fix the thing").closest(".board-card");
      expect(card).not.toHaveAttribute("tabindex");
      expect(card).not.toHaveAttribute("role");

      const link = within(card).getByRole("link", { name: "Fix the thing" });
      expect(link).toHaveAttribute("href", "/tasks/1");
      link.focus();
      await user.keyboard("{Enter}");
      expect(onOpenTask).toHaveBeenLastCalledWith(1);
      expect(onOpenTask).toHaveBeenCalledTimes(1);

      within(card).getByRole("checkbox").focus();
      await user.keyboard(" ");
      expect(onToggleSelect).toHaveBeenCalledWith(1);
      expect(onOpenTask).toHaveBeenCalledTimes(1);
    });
  });

  describe("what a card shows", () => {
    it("carries the same chips a row does", () => {
      renderBoard({
        tasks: [
          {
            id: 7,
            title: "Scheduled work",
            state: "queued",
            repo: "acme/widgets",
            capabilities: ["web-search"],
            blocked: false,
            scheduled: true,
          },
        ],
        config: { capabilities: [{ id: "web-search", name: "Web search" }] },
      });
      expect(screen.getByText("scheduled")).toBeInTheDocument();
      expect(screen.getByText("acme/widgets")).toBeInTheDocument();
      expect(screen.getByText("Web search")).toBeInTheDocument();
    });

    it("shows supersededBy, reads and writes chips on a card just like a row", () => {
      renderBoard({
        tasks: [
          {
            id: 7,
            title: "Multi-repo follow-up",
            state: "queued",
            repo: "acme/widgets",
            reads: ["acme/docs"],
            writes: [{ repo: "acme/infra", base: "release/2" }],
            supersededBy: 12,
            capabilities: [],
            blocked: false,
          },
        ],
      });
      expect(
        screen.getByTitle(
          "closed as replaced by task 12, which was filed to supersede it",
        ),
      ).toHaveTextContent("superseded by 12");
      expect(screen.getByText("acme/docs (read)")).toBeInTheDocument();
      expect(
        screen.getByTitle("may also land work in acme/infra on release/2"),
      ).toHaveTextContent("acme/infra (write)");
    });

    it("names a multi-state column card's state badge with role=img for screen readers", () => {
      renderBoard();
      const card = cardFor("Answer me");
      const badge = within(card).getByRole("img", { name: /^Asked/ });
      expect(badge).toHaveClass("badge-awaiting_reply");
    });

    // Including the one that corrects what the card says about itself: a
    // card whose merge-queue wait grain has flagged as stalled says so
    // beside the phrase it is nominally waiting on (state.js's
    // mergeQueueStall). A board is scanned the way a list is, and the
    // card's own mark goes with it -- mergeQueueActive is what both
    // draw it from, and TaskList's own test pins the mark.
    it("says so on a card whose merge-queue wait has stalled", () => {
      renderBoard({
        tasks: [
          {
            id: 9,
            title: "Stuck on CI",
            state: "completed",
            capabilities: [],
            blocked: false,
            mergeQueueState: "waiting_ci",
            mergeQueueStateAt: new Date(
              Date.now() - 4 * 3600 * 1000,
            ).toISOString(),
            mergeQueueStalledAt: new Date(
              Date.now() - 90 * 60 * 1000,
            ).toISOString(),
          },
        ],
      });

      expect(screen.getByText("Stalled")).toBeInTheDocument();
      expect(screen.getByText("Waiting for CI")).toBeInTheDocument();
      expect(
        document.querySelector(".board-card .grain-mark-sheet"),
      ).toBeNull();
    });

    // grain/task-47: "the same chips a row does" includes the figures
    // those chips carry (ItemGlyph.jsx, docs/brand.md) -- the board is
    // the same tasks in columns, so a card filed by a schedule has to be
    // recognisable the way the row for it already is.
    it("carries the origin chips' glyphs too", () => {
      renderBoard({
        tasks: [
          {
            id: 7,
            title: "Scheduled work",
            state: "queued",
            capabilities: [],
            blocked: false,
            scheduled: true,
          },
          {
            id: 8,
            title: "Suite work",
            state: "queued",
            capabilities: [],
            blocked: false,
            suiteRun: true,
          },
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
    });

    // grain/task-284: the board has no nesting to explain a stacked task
    // with, so every stacked card carries the chip -- and it has to say
    // which of the two kinds it is, since a review read as a merge fix
    // would claim the change had a red build.
    it("tells a review apart from a merge fix on a stacked card", () => {
      renderBoard({
        tasks: [
          {
            id: 10,
            title: "Repair the pull request",
            state: "queued",
            capabilities: [],
            blocked: false,
            stacked: true,
            generatedFrom: 1,
          },
          {
            id: 11,
            title: "Review the change",
            state: "queued",
            capabilities: [],
            blocked: false,
            stacked: true,
            review: true,
            generatedFrom: 2,
          },
        ],
      });
      expect(
        screen.getByTitle("the merge queue's own automatic fix for 1"),
      ).toHaveTextContent("merge fix");
      expect(
        screen.getByTitle("a review of 2's own code, run before it merges"),
      ).toHaveTextContent("review");
    });

    it("says what a running task's own agent is doing", () => {
      renderBoard({
        tasks: [
          {
            id: 8,
            title: "Busy",
            state: "running",
            capabilities: [],
            blocked: false,
            activity: "waiting for CI",
            activityAt: new Date().toISOString(),
          },
        ],
      });
      expect(screen.getByText("waiting for CI")).toBeInTheDocument();
      expect(document.querySelector(".board-card-activity")).toHaveAttribute(
        "title",
        expect.stringContaining("What this run says it is doing"),
      );
      // An agent's own words carry no attribution, on a card as on a row.
      expect(
        document.querySelector(".task-activity-by"),
      ).not.toBeInTheDocument();
    });

    // How long the phrase has stood, once it has stood long enough to be
    // worth saying (state.js's activityAge). A card is read for the same
    // reason a row is -- "what has this been doing for the last half
    // hour?" -- and the note alone cannot answer it.
    it("dates a status that has stood long enough to matter", () => {
      renderBoard({
        tasks: [
          {
            id: 8,
            title: "Busy",
            state: "running",
            capabilities: [],
            blocked: false,
            activity: "waiting for CI on the second push",
            activityAt: new Date(Date.now() - 22 * 60 * 1000).toISOString(),
          },
        ],
      });
      expect(screen.getByText("22m ago")).toBeInTheDocument();
    });

    // grain/task-478: the row and the detail pane both mark whose
    // sentence this is, and a card that did not would put grain's own
    // setup narration in the agent's voice (state.js's runActivity,
    // TaskList's own test pins the same mark on a row).
    it("marks a status grain wrote during setup as grain's own", () => {
      renderBoard({
        tasks: [
          {
            id: 8,
            title: "Busy",
            state: "running",
            capabilities: [],
            blocked: false,
            activity: "cloning acme/widgets",
            activityBySetup: true,
          },
        ],
      });
      expect(screen.getByText("cloning acme/widgets")).toBeInTheDocument();
      expect(document.querySelector(".task-activity-by")).toHaveTextContent(
        "grain",
      );
      expect(document.querySelector(".board-card-activity")).toHaveAttribute(
        "title",
        expect.stringContaining("What grain is doing to get this run started"),
      );
    });

    it("keeps a waiting queued card free of the full wait description and shows the reason on the state icon hover", () => {
      const waitReason = "waiting for Gemini API keys (gemini-key grant)";
      renderBoard({
        tasks: [
          {
            id: 10,
            title: "Waiting on grant",
            state: "queued",
            capabilities: [],
            blocked: false,
            activity: waitReason,
            activityBySetup: true,
          },
        ],
      });
      const card = cardFor("Waiting on grant");
      expect(card.querySelector(".board-card-activity")).toBeNull();
      expect(screen.queryByText(waitReason)).not.toBeInTheDocument();
      const badge = card.querySelector(".badge-icon");
      expect(badge).toBeInTheDocument();
      expect(badge).toHaveAttribute("title", `Waiting — ${waitReason}`);
    });

    // The queue's voice is grain's too, and it is the one a finished
    // task's card shows for as long as the change is being landed.
    it("marks the merge queue's own phrase as grain's", () => {
      renderBoard({
        tasks: [
          {
            id: 9,
            title: "Landing",
            state: "completed",
            capabilities: [],
            blocked: false,
            mergeQueueState: "waiting_ci",
            mergeQueueStateAt: new Date().toISOString(),
          },
        ],
      });
      expect(screen.getByText("Waiting for CI")).toBeInTheDocument();
      expect(document.querySelector(".task-activity-by")).toHaveTextContent(
        "grain",
      );
    });

    it("says a task is blocked, and on what", () => {
      renderBoard({
        tasks: [
          {
            id: 1,
            title: "Upstream blocker",
            state: "closed",
            capabilities: [],
          },
          {
            id: 9,
            title: "Task 9",
            state: "queued",
            capabilities: [],
            blocked: true,
            blockedBy: [1],
          },
        ],
      });
      expect(
        screen.getByTitle("Blocked on: #1 (cancelled)"),
      ).toBeInTheDocument();
    });

    // grain/task-100: a card carries this deployment's own fields by
    // the same rule a row does (CustomFields.jsx's rowFields) -- the
    // board is the same tasks, so a sort by `priority` explains itself
    // here too and a field flagged for the row is flagged for the card.
    it("carries a declared field's value, sorted on or flagged", async () => {
      const user = userEvent.setup();
      const task = {
        id: 12,
        title: "Fix the thing",
        state: "queued",
        capabilities: [],
        blocked: false,
        customFields: { priority: "high", customer: "acme" },
      };
      const priority = {
        name: "priority",
        label: "Priority",
        type: "enum",
        options: ["high", "low"],
      };
      renderBoard({
        tasks: [task],
        config: {
          customFields: [priority, { name: "customer", showOnRow: true }],
        },
      });
      expect(screen.getByTitle("customer: acme")).toBeInTheDocument();
      expect(screen.queryByTitle("Priority: high")).not.toBeInTheDocument();

      await sortBy(user, "Priority");
      expect(screen.getByTitle("Priority: high")).toBeInTheDocument();
    });

    // A column of one state would put the same dot on every card in it;
    // a column collecting three needs it to tell them apart.
    it("shows a state dot only where a column holds more than one state", () => {
      renderBoard();
      expect(cardFor("Answer me").querySelector(".badge")).toBeInTheDocument();
      expect(
        cardFor("Fix the thing").querySelector(".badge"),
      ).not.toBeInTheDocument();
    });
  });

  describe("narrowing which tasks show up", () => {
    const wider = [
      {
        id: 1,
        title: "Alpha",
        state: "queued",
        repo: "acme/widgets",
        capabilities: [],
        blocked: false,
      },
      {
        id: 2,
        title: "Beta",
        state: "running",
        repo: "acme/gadgets",
        capabilities: [],
        blocked: false,
      },
    ];

    it("searches by title", async () => {
      const user = userEvent.setup();
      renderBoard({ tasks: wider });
      await user.type(screen.getByPlaceholderText("Search tasks…"), "alph");
      expect(screen.getByText("Alpha")).toBeInTheDocument();
      expect(screen.queryByText("Beta")).not.toBeInTheDocument();
    });

    it("narrows by an attribute, using the list's own filter editor", async () => {
      const user = userEvent.setup();
      renderBoard({ tasks: wider });
      await filterBy(user, "Repo", "acme/gadgets");
      expect(screen.queryByText("Alpha")).not.toBeInTheDocument();
      expect(screen.getByText("Beta")).toBeInTheDocument();
    });

    it("puts everything back with one Clear", async () => {
      const user = userEvent.setup();
      renderBoard({ tasks: wider });
      await user.type(screen.getByPlaceholderText("Search tasks…"), "alph");
      await user.click(screen.getByRole("button", { name: "Clear" }));
      expect(screen.getByText("Beta")).toBeInTheDocument();
    });

    it("says so when the search and filters leave nothing", async () => {
      const user = userEvent.setup();
      renderBoard({ tasks: wider });
      await user.type(
        screen.getByPlaceholderText("Search tasks…"),
        "nothing matches this",
      );
      expect(
        screen.getByText("No tasks match this search and these filters."),
      ).toBeInTheDocument();
    });

    // The filter menus are built from the tasks the columns admit, so a
    // board with no "Closed" column never offers a value only a closed
    // task carries.
    it("does not offer a filter value only an off-board task has", async () => {
      const user = userEvent.setup();
      renderBoard({
        tasks: [
          {
            id: 1,
            title: "Alpha",
            state: "queued",
            repo: "acme/widgets",
            capabilities: [],
            blocked: false,
          },
          {
            id: 2,
            title: "Old",
            state: "closed",
            repo: "acme/archive",
            capabilities: [],
            blocked: false,
          },
        ],
      });
      await filterBy(user, "Repo");
      await user.click(screen.getByLabelText("Value for filter 1"));
      expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
        "acme/widgets",
      ]);
    });

    it("orders the cards within a column", async () => {
      const user = userEvent.setup();
      renderBoard({
        tasks: [
          {
            id: 1,
            title: "Zulu",
            state: "queued",
            capabilities: [],
            blocked: false,
          },
          {
            id: 2,
            title: "Alpha",
            state: "queued",
            capabilities: [],
            blocked: false,
          },
        ],
      });
      expect(cardsIn("Waiting")).toEqual(["Zulu", "Alpha"]);
      await sortBy(user, "Title");
      expect(cardsIn("Waiting")).toEqual(["Alpha", "Zulu"]);
    });
  });

  describe("customizing the columns", () => {
    it("lays the board out the way this browser last saved it", () => {
      localStorage.setItem(
        BOARD_STORAGE_KEY,
        JSON.stringify([
          {
            id: "everything",
            title: "Everything",
            states: ["queued", "running", "awaiting_reply", "closed"],
          },
        ]),
      );
      renderBoard();
      expect(cardsIn("Everything")).toEqual([
        "Fix the thing",
        "Ship the other thing",
        "Answer me",
        "Long done",
      ]);
      expect(screen.queryByText(/in no column/)).not.toBeInTheDocument();
    });

    it("saves an edit and lays the board out again straight away", async () => {
      const user = userEvent.setup();
      renderBoard();

      await user.click(screen.getByRole("button", { name: "Columns" }));
      await user.click(screen.getByRole("button", { name: "+ Add column" }));
      const titles = screen.getAllByLabelText(/Column \d+ title/);
      const last = titles.length - 1;
      await user.clear(titles[last]);
      await user.type(titles[last], "Archive");
      await user.click(screen.getAllByLabelText("States")[last]);
      await user.click(screen.getByRole("option", { name: "Closed" }));
      // A multi-state Select stays open for the next pick; Escape is how
      // somebody who has picked their one state gets back to the form.
      await user.keyboard("{Escape}");
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(cardsIn("Archive")).toEqual(["Long done"]);
      expect(
        JSON.parse(localStorage.getItem(BOARD_STORAGE_KEY)).at(-1),
      ).toMatchObject({
        title: "Archive",
        states: ["closed"],
      });
    });

    it("leaves the board alone when the editor is cancelled", async () => {
      const user = userEvent.setup();
      renderBoard();
      await user.click(screen.getByRole("button", { name: "Columns" }));
      await user.click(screen.getByRole("button", { name: "Remove Waiting" }));
      await user.click(screen.getByRole("button", { name: "Cancel" }));
      expect(column("Waiting")).toBeInTheDocument();
      expect(localStorage.getItem(BOARD_STORAGE_KEY)).toBeNull();
    });

    it("offers the editor from the note about what is off the board", async () => {
      const user = userEvent.setup();
      renderBoard();
      await user.click(screen.getByRole("button", { name: "Edit columns" }));
      expect(screen.getByText("Board columns")).toBeInTheDocument();
    });
  });

  describe("selecting tasks", () => {
    it("selects one task from its card", async () => {
      const user = userEvent.setup();
      const { onToggleSelect, onOpenTask } = renderBoard();
      await user.click(screen.getByRole("checkbox", { name: "Select 1" }));
      expect(onToggleSelect).toHaveBeenCalledWith(1);
      // The checkbox is not a way into the task.
      expect(onOpenTask).not.toHaveBeenCalled();
    });

    it("selects a contiguous range of cards when shift-clicking a second checkbox", async () => {
      const user = userEvent.setup();
      const { onToggleSelect, onSelectAll } = renderBoard();
      await user.click(screen.getByRole("checkbox", { name: "Select 1" }));
      expect(onToggleSelect).toHaveBeenCalledWith(1);

      await user.keyboard("{Shift>}");
      await user.click(screen.getByRole("checkbox", { name: "Select 3" }));
      await user.keyboard("{/Shift}");

      expect(onSelectAll).toHaveBeenCalledWith([1, 2, 3], true);
    });

    it("selects a whole column at once", async () => {
      const user = userEvent.setup();
      const { onSelectAll } = renderBoard();
      await user.click(
        screen.getByRole("checkbox", {
          name: "Select every task in Asked",
        }),
      );
      expect(onSelectAll).toHaveBeenCalledWith([3], true);
    });

    it("offers no column checkbox for a column with nothing in it", () => {
      renderBoard();
      expect(
        screen.queryByRole("checkbox", {
          name: "Select every task in Proposed",
        }),
      ).not.toBeInTheDocument();
    });
  });

  describe("dragging to reorder", () => {
    const queued = [
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
      {
        id: 4,
        title: "Elsewhere",
        state: "running",
        capabilities: [],
        blocked: false,
      },
    ];

    it("reorders the backlog between the two cards a drop landed between", () => {
      const onReorder = vi.fn();
      renderBoard({ tasks: queued, onReorder });
      fireEvent.dragStart(cardFor("Third"));
      fireEvent.dragOver(cardFor("Second"));
      fireEvent.drop(cardFor("Second"));
      expect(onReorder).toHaveBeenCalledWith([3], 1, 2);
    });

    it("drops at the end of a column", () => {
      const onReorder = vi.fn();
      renderBoard({ tasks: queued, onReorder });
      fireEvent.dragStart(cardFor("First"));
      fireEvent.dragOver(column("Waiting").querySelector(".board-drop-end"));
      fireEvent.drop(column("Waiting").querySelector(".board-drop-end"));
      expect(onReorder).toHaveBeenCalledWith([1], 3, null);
    });

    // A task's state is derived by the daemon, not set by the UI, so
    // there is no such thing as dropping a card into another column.
    it("refuses a drop into a different column", () => {
      const onReorder = vi.fn();
      renderBoard({ tasks: queued, onReorder });
      fireEvent.dragStart(cardFor("First"));
      fireEvent.drop(cardFor("Elsewhere"));
      expect(onReorder).not.toHaveBeenCalled();
    });

    it("drags the whole selection when a selected card is picked up", () => {
      const onReorder = vi.fn();
      renderBoard({ tasks: queued, onReorder, selected: new Set([1, 3]) });
      fireEvent.dragStart(cardFor("Third"));
      fireEvent.drop(cardFor("Second"));
      expect(onReorder).toHaveBeenCalledWith([1, 3], null, 2);
    });

    it("never starts a drag without an onReorder prop", () => {
      renderBoard({ tasks: queued });
      expect(cardFor("First")).toHaveAttribute("draggable", "false");
      expect(
        document.querySelector(".task-drag-handle"),
      ).not.toBeInTheDocument();
    });

    it("stops offering drags once a display-only sort is chosen", async () => {
      const user = userEvent.setup();
      renderBoard({ tasks: queued, onReorder: vi.fn() });
      expect(cardFor("First")).toHaveAttribute("draggable", "true");
      await sortBy(user, "Title");
      expect(cardFor("First")).toHaveAttribute("draggable", "false");
    });

    // grain/task-23: a phone fires none of the drag events above, so a
    // card is also picked up by tapping its handle and put down by
    // tapping one of the slots that appear between the cards.
    describe("tap to move (grain/task-23)", () => {
      const slots = () =>
        screen.queryAllByRole("button", { name: /^Move here/ });

      it("offers slots only in the column the card was picked up in", async () => {
        const user = userEvent.setup();
        renderBoard({ tasks: queued, onReorder: vi.fn() });

        await user.click(screen.getByRole("button", { name: "Move task 2" }));

        // A card only ever moves within its own column -- a task's
        // state is derived, not set -- so "Running" offers nothing.
        expect(slots().map((b) => b.getAttribute("aria-label"))).toEqual([
          "Move here, above First, in Waiting",
          "Move here, to the end of Waiting",
        ]);
        expect(
          column("Running").querySelectorAll(".reorder-slot"),
        ).toHaveLength(0);
      });

      it("moves a card between the two cards the slot sits between", async () => {
        const onReorder = vi.fn();
        const user = userEvent.setup();
        renderBoard({ tasks: queued, onReorder });

        await user.click(screen.getByRole("button", { name: "Move task 3" }));
        await user.click(
          screen.getByRole("button", {
            name: "Move here, above Second, in Waiting",
          }),
        );

        expect(onReorder).toHaveBeenCalledWith([3], 1, 2);
        expect(slots()).toHaveLength(0);
      });

      it("moves a card to the end of its column", async () => {
        const onReorder = vi.fn();
        const user = userEvent.setup();
        renderBoard({ tasks: queued, onReorder });

        await user.click(screen.getByRole("button", { name: "Move task 1" }));
        await user.click(
          screen.getByRole("button", {
            name: "Move here, to the end of Waiting",
          }),
        );

        expect(onReorder).toHaveBeenCalledWith([1], 3, null);
      });

      it("picks up the whole selection when the card is part of one", async () => {
        const onReorder = vi.fn();
        const user = userEvent.setup();
        renderBoard({ tasks: queued, onReorder, selected: new Set([1, 3]) });

        await user.click(screen.getByRole("button", { name: "Move task 3" }));
        await user.click(
          screen.getByRole("button", {
            name: "Move here, above Second, in Waiting",
          }),
        );

        expect(onReorder).toHaveBeenCalledWith([1, 3], null, 2);
      });

      it("puts the card back down on Cancel", async () => {
        const onReorder = vi.fn();
        const user = userEvent.setup();
        renderBoard({ tasks: queued, onReorder });

        await user.click(screen.getByRole("button", { name: "Move task 2" }));
        expect(screen.getByText(/Moving 1 task/)).toBeInTheDocument();
        await user.click(screen.getByRole("button", { name: "Cancel" }));

        expect(slots()).toHaveLength(0);
        expect(onReorder).not.toHaveBeenCalled();
      });

      it("disables the move handle on a card that is the only one in its column", () => {
        renderBoard({ tasks: queued, onReorder: vi.fn() });

        // Running has only task 4 ("Elsewhere"), while Queued has 1, 2, 3.
        const loneHandle = screen.getByRole("button", { name: "Move task 4" });
        expect(loneHandle).toBeDisabled();
        expect(loneHandle).toHaveAttribute(
          "title",
          "Nowhere to move this: it is the only one here",
        );
        expect(
          screen.getByRole("button", { name: "Move task 1" }),
        ).not.toBeDisabled();
      });

      it("drops the pick-up when narrowing hides the picked-up card", async () => {
        const user = userEvent.setup();
        renderBoard({ tasks: queued, onReorder: vi.fn() });

        await user.click(screen.getByRole("button", { name: "Move task 2" }));
        expect(screen.getByText(/Moving 1 task/)).toBeInTheDocument();
        expect(slots().length).toBeGreaterThan(0);

        await user.type(screen.getByPlaceholderText("Search tasks…"), "First");

        expect(screen.queryByText(/Moving 1 task/)).not.toBeInTheDocument();
        expect(slots()).toHaveLength(0);
      });
    });
  });

  // A standing session's task rests in awaiting_reply between exchanges.
  // It is not work waiting on anybody, so "Asked" leaves it out,
  // whatever the board's own query asks.
  const sessionTask = {
    id: 9,
    title: "Director chat",
    state: "awaiting_reply",
    consoleSession: "director",
    interactive: true,
    capabilities: [],
    blocked: false,
  };

  it("leaves a standing session's task out of Asked", () => {
    renderBoard({ tasks: [...tasks, sessionTask] });
    expect(cardsIn("Asked")).toEqual(["Answer me"]);
  });

  it("leaves a standing session's task out even on a board narrowed by nothing", () => {
    renderBoard({
      tasks: [...tasks, sessionTask],
      narrowing: NO_NARROWING,
    });
    expect(cardsIn("Asked")).toEqual(["Answer me"]);
  });

  it("reports visible card ids via onShownChange as narrowing changes", async () => {
    const onShownChange = vi.fn();
    renderBoard({ onShownChange });
    expect(onShownChange).toHaveBeenLastCalledWith(new Set([1, 2, 3]));
    await userEvent.type(screen.getByRole("textbox"), "Answer");
    expect(onShownChange).toHaveBeenLastCalledWith(new Set([3]));
  });
});
