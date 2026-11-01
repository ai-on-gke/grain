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

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import PanelBoard from "./PanelBoard.jsx";
import api from "../api.js";
import { normalizeBoard } from "../boards.js";
import { BOARD_STORAGE_KEY } from "../board.js";
import { GANTT_STORAGE_KEY } from "../gantt.js";
import { panelMove } from "../panelOrder.js";
import { narrowingFromQuery } from "../paths.js";
import { NO_NARROWING, defaultNarrowing } from "../taskFilters.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// The dashboard (grain/task-88): several views of the same tasks at
// once, each panel narrowed to its own question. What is worth pinning
// here is that each panel asks its *own* question of the tasks the page
// already holds, that a panel is one click from the view it is a small
// copy of, and that a dashboard nobody has finished building says so.

const NOW = Date.UTC(2026, 8, 6, 12, 0, 0);
const HOUR = 60 * 60 * 1000;
const iso = (ms) => new Date(ms).toISOString();

const task = (over) => ({
  capabilities: [],
  blocked: false,
  createdAt: iso(NOW - 3 * HOUR),
  startedAt: iso(NOW - 2 * HOUR),
  ...over,
});

const tasks = [
  task({
    id: 1,
    title: "Fix the flake",
    state: "running",
    repo: "acme/widgets",
    author: "alice",
  }),
  task({
    id: 2,
    title: "Ship the gadget",
    state: "running",
    repo: "acme/gadgets",
    author: "bob",
  }),
  task({
    id: 3,
    title: "Answer the question",
    state: "awaiting_reply",
    repo: "acme/widgets",
    author: "alice",
  }),
];

const config = { repos: [], capabilities: [] };

// What the throughput panel asked /api/metrics for, as parameters
// rather than as a string: a repo is "acme/widgets" in the question and
// percent-encoded in the URL, and it is the question these tests are
// about (report.test.js reads reportPath's own output the same way).
const askedFor = () =>
  new URLSearchParams(api.mock.calls[0][0].split("?")[1] || "");

// A board as App holds one: straight through normalizeBoard, so these
// tests see exactly the shape the rest of the UI does.
const board = (panels, over = {}) =>
  normalizeBoard({
    id: "board-1",
    name: "Operations",
    kind: "panels",
    panels,
    ...over,
  });

const draw = (props) =>
  render(
    <PanelBoard
      board={board([{ kind: "list" }])}
      tasks={tasks}
      config={config}
      onOpenTask={() => {}}
      now={NOW}
      {...props}
    />,
  );

// Narrowable is the dashboard wired up the way App wires it: App holds
// the narrowing every task view shares and hands back the patches its
// toolbar makes of it, so a dashboard narrowed from the toolbar is only
// a dashboard drawn again with a different question.
function Narrowable({ start = NO_NARROWING, ...props }) {
  const [narrowing, setNarrowing] = useState(start);
  return (
    <PanelBoard
      tasks={tasks}
      config={config}
      onOpenTask={() => {}}
      now={NOW}
      narrowing={narrowing}
      onNarrow={(patch) => setNarrowing((prev) => ({ ...prev, ...patch }))}
      {...props}
    />
  );
}

describe("PanelBoard", () => {
  beforeEach(() => {
    localStorage.clear();
    api.mockReset();
    // The shape /api/metrics really answers with (pkg/ui/metrics.go):
    // the totals are tasksFiled and tasksCompleted, and only a bucket
    // says plain `filed` and `completed`. A stub written in the buckets'
    // words once let the panel read fields the server never sends and
    // print 0 on every real dashboard (grain/task-1356).
    api.mockResolvedValue({
      throughput: {
        tasksFiled: 12,
        tasksCompleted: 9,
        buckets: [
          { filed: 1, completed: 0 },
          { filed: 2, completed: 3 },
        ],
      },
    });
  });

  it("draws one panel per row, headed by what each one is", () => {
    draw({
      board: board([
        { kind: "list", title: "Waiting on me" },
        { kind: "states" },
      ]),
    });
    expect(screen.getByText("Waiting on me")).toBeInTheDocument();
    // A panel with no title of its own is named after what it is.
    expect(screen.getByText("State counts")).toBeInTheDocument();
  });

  // The point of a panel: its own query, asked of the same tasks the
  // page already holds. The list panel here is narrowed to one repo, so
  // the other repo's task is not in it -- while the panel beside it,
  // narrowed to nothing, counts all three.
  it("narrows each panel to its own question", () => {
    draw({
      board: board([
        { kind: "list", title: "Widgets", query: "f=repo:any:acme/widgets" },
        { kind: "list", title: "Everything" },
      ]),
    });
    const widgets = screen.getByText("Widgets").closest(".panel");
    expect(within(widgets).getByText("Fix the flake")).toBeInTheDocument();
    expect(within(widgets).queryByText("Ship the gadget")).toBeNull();
    const everything = screen.getByText("Everything").closest(".panel");
    expect(within(everything).getByText("Ship the gadget")).toBeInTheDocument();
  });

  // A panel with no query of its own asks the dashboard's question,
  // which is what makes "a dashboard about one repo" one query. That
  // question is the narrowing the page is holding -- seeded from the
  // board's own query when it was opened (App's openBoard), and the
  // reader's from then on.
  it("falls back to the dashboard's own question", () => {
    draw({
      board: board([{ kind: "list", title: "Mine" }]),
      narrowing: narrowingFromQuery("f=repo:any:acme/gadgets"),
    });
    const panel = screen.getByText("Mine").closest(".panel");
    expect(within(panel).getByText("Ship the gadget")).toBeInTheDocument();
    expect(within(panel).queryByText("Fix the flake")).toBeNull();
  });

  // The gesture the toolbar is for (grain/task-111): narrow the whole
  // dashboard on the spot, without editing a row.
  it("narrows every panel that inherits when the toolbar is narrowed", async () => {
    render(
      <Narrowable
        board={board([
          { kind: "list", title: "Mine" },
          { kind: "list", title: "Widgets", query: "f=repo:any:acme/widgets" },
        ])}
      />,
    );
    const mine = screen.getByText("Mine").closest(".panel");
    expect(within(mine).getByText("Fix the flake")).toBeInTheDocument();

    await userEvent.type(
      screen.getByPlaceholderText("Search this dashboard…"),
      "gadget",
    );

    expect(within(mine).getByText("Ship the gadget")).toBeInTheDocument();
    expect(within(mine).queryByText("Fix the flake")).toBeNull();
    // ...and the panel that already said what it was about is left
    // exactly where it was: narrowing it further would be two
    // conditions on one attribute, which is how a panel silently
    // becomes empty (panels.js's panelNarrowing).
    const widgets = screen.getByText("Widgets").closest(".panel");
    expect(within(widgets).getByText("Fix the flake")).toBeInTheDocument();
  });

  // What the toolbar narrowed is what the link out of a panel carries,
  // so a narrowed dashboard opens the full view narrowed the same way.
  it("carries the dashboard's narrowing into a panel's own link", () => {
    draw({
      board: board([{ kind: "list", title: "Mine" }]),
      narrowing: narrowingFromQuery("q=flake"),
    });
    expect(screen.getByRole("link", { name: "Open" })).toHaveAttribute(
      "href",
      "/?q=flake",
    );
  });

  // The state counts are the board's own grouping (board.js) with the
  // cards left off -- this browser's columns, since that is the layout
  // its reader has already chosen for the same question.
  it("counts the tasks into the columns this browser is set to", () => {
    localStorage.setItem(
      BOARD_STORAGE_KEY,
      JSON.stringify([
        { id: "c1", title: "Working", states: ["running"] },
        { id: "c2", title: "Waiting", states: ["awaiting_reply"] },
      ]),
    );
    draw({ board: board([{ kind: "states" }]) });
    const working = screen.getByText("Working").closest(".panel-state");
    expect(within(working).getByText("2")).toBeInTheDocument();
    const waiting = screen.getByText("Waiting").closest(".panel-state");
    expect(within(waiting).getByText("1")).toBeInTheDocument();
  });

  // Every panel is one click from the full view of the same kind asking
  // the same question -- as a real URL, so it can be copied and sent.
  // The full view of the tasks the panel is about, asking the panel's
  // own question. A timeline panel lands on the list rather than on a
  // chart of its own: /gantt went with grain/task-162, and panels.js
  // says why the list is the honest destination now.
  it("links a panel to the view it is a small copy of", () => {
    draw({
      board: board([
        { kind: "gantt", title: "Timeline", query: "f=state:any:running" },
      ]),
    });
    expect(screen.getByRole("link", { name: "Open" })).toHaveAttribute(
      "href",
      "/?f=state:any:running",
    );
  });

  // ...and clicking one is followed in-app rather than as a page load,
  // carrying exactly the question the panel was asking.
  it("opens a panel's own view in-app when its link is clicked", async () => {
    const onOpen = vi.fn();
    draw({
      board: board([
        { kind: "list", title: "Running", query: "f=state:any:running" },
      ]),
      onOpen,
    });
    await userEvent.click(screen.getByRole("link", { name: "Open" }));
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(onOpen.mock.calls[0][0].view).toBe("tasks");
    expect(onOpen.mock.calls[0][0].narrowing.filters).toEqual([
      { attr: "state", op: "any", values: ["running"] },
    ]);
  });

  // A count is a number worth clicking, and what it opens is those
  // tasks: the panel's own narrowing plus the column's states.
  it("opens the list of exactly those tasks from a state count", async () => {
    const onOpen = vi.fn();
    localStorage.setItem(
      BOARD_STORAGE_KEY,
      JSON.stringify([{ id: "c1", title: "Working", states: ["running"] }]),
    );
    draw({
      board: board([{ kind: "states", query: "f=repo:any:acme/widgets" }]),
      onOpen,
    });
    await userEvent.click(screen.getByText("Working").closest("a"));
    expect(onOpen.mock.calls[0][0].narrowing.filters).toEqual([
      { attr: "repo", op: "any", values: ["acme/widgets"] },
      { attr: "state", op: "any", values: ["running"] },
    ]);
  });

  // 2026-10-22 boards walkthrough: a modified click (Ctrl/Cmd/Shift/Alt or
  // middle-click) on a panel's "Open" link or a state count tile is the
  // browser's own (open in new tab) and must not navigate the dashboard
  // tab in place.
  it("leaves modified clicks on Open and state counts to the browser", () => {
    const onOpen = vi.fn();
    localStorage.setItem(
      BOARD_STORAGE_KEY,
      JSON.stringify([{ id: "c1", title: "Working", states: ["running"] }]),
    );
    draw({
      board: board([
        { kind: "states", title: "Counts", query: "" },
        { kind: "list", title: "Running", query: "f=state:any:running" },
      ]),
      onOpen,
    });

    const openLink = screen.getAllByRole("link", { name: "Open" })[1];
    const stateLink = screen.getByText("Working").closest("a");

    const prevented = [];
    const watch = (e) => {
      prevented.push(e.defaultPrevented);
      e.preventDefault();
    };
    document.addEventListener("click", watch);
    try {
      for (const el of [openLink, stateLink]) {
        el.dispatchEvent(
          new MouseEvent("click", {
            bubbles: true,
            cancelable: true,
            button: 0,
            ctrlKey: true,
          }),
        );
      }
      expect(onOpen).not.toHaveBeenCalled();
    } finally {
      document.removeEventListener("click", watch);
    }
    expect(prevented).toEqual([false, false]);
  });

  // The timeline as a panel is the timeline: the same chart, without the
  // heading and the narrowing bar it fills a pane with (GanttChart's own
  // `embedded`).
  it("draws a timeline panel without the toolbar the full view carries", () => {
    draw({ board: board([{ kind: "gantt", title: "Timeline", range: "7d" }]) });
    const panel = screen.getByText("Timeline").closest(".panel");
    expect(panel.querySelector(".gantt-body")).not.toBeNull();
    expect(within(panel).queryByPlaceholderText("Search tasks…")).toBeNull();
    expect(within(panel).queryByLabelText("Range")).toBeNull();
  });

  // ...and it is a window onto that timeline rather than the whole of
  // it: a panel is a few hundred pixels tall, and a deployment's own
  // last week is however many tasks it is (grain/task-311). The rows are
  // budgeted like the list panel's, and what the budget leaves out is
  // said under the chart rather than scrolled to.
  it("draws a timeline panel at panel size, and says what it left out", () => {
    const many = Array.from({ length: 20 }, (_, i) =>
      task({ id: 100 + i, title: `Task ${i}`, state: "running" }),
    );
    draw({
      board: board([{ kind: "gantt", title: "Timeline", range: "7d" }]),
      tasks: many,
    });
    const panel = screen.getByText("Timeline").closest(".panel");
    expect(panel.querySelectorAll(".gantt-row")).toHaveLength(10);
    // The count on the header is still the whole question the panel
    // asks -- what its "Open" link leads to -- so the two numbers
    // together say "twenty of these, ten drawn".
    expect(
      within(panel.querySelector(".panel-header")).getByText("20"),
    ).toBeInTheDocument();
    expect(
      within(panel).getByText("Showing 10 of 20 in this range."),
    ).toBeInTheDocument();
  });

  // The list panel's own budget is eight rows, and it says so the way
  // the timeline panel does: without the note, eight rows under a header
  // counting twelve read as a list that has eight.
  it("says how much of a list panel it left out", () => {
    const many = Array.from({ length: 12 }, (_, i) =>
      task({ id: 100 + i, title: `Task ${i}`, state: "running" }),
    );
    draw({ board: board([{ kind: "list", title: "Live" }]), tasks: many });
    const panel = screen.getByText("Live").closest(".panel");
    expect(panel.querySelectorAll(".task-list > li")).toHaveLength(8);
    expect(within(panel).getByText("Showing 8 of 12.")).toBeInTheDocument();
  });

  it("says nothing about a list panel that shows every row", () => {
    draw({ board: board([{ kind: "list", title: "Live" }]) });
    const panel = screen.getByText("Live").closest(".panel");
    expect(within(panel).queryByText(/^Showing/)).toBeNull();
  });

  // A range this build cannot use falls back rather than failing, which
  // is right, and used to fall back in silence, which is not: the panel
  // then draws a window nobody asked for while looking entirely healthy
  // -- and it is the browser's own last range it falls back to, so
  // there is not even one wrong answer to notice. A hand run measuring
  // this very dashboard was misled by exactly that
  // (the 2026-09-07 panel-dashboard hand run, section 5).
  it("says which range it could not use, and the span it drew instead", () => {
    localStorage.setItem(
      GANTT_STORAGE_KEY,
      JSON.stringify({ range: "7d", lanes: "none" }),
    );
    draw({
      board: board([{ kind: "gantt", title: "Yesterday", range: "1d" }]),
    });
    const panel = screen.getByText("Yesterday").closest(".panel");
    const note = panel.querySelector(".panel-range-note");
    expect(note).not.toBeNull();
    expect(note.textContent).toContain("1d");
    expect(note.textContent).toContain("last 7 days");
  });

  // Chrome nobody sees until something is wrong: a dashboard whose
  // ranges are words this build has says nothing about them.
  it("says nothing about a range it understood", () => {
    draw({
      board: board([
        { kind: "gantt", title: "Timeline", range: "24h" },
        { kind: "list", title: "Mine" },
      ]),
    });
    expect(document.querySelector(".panel-range-note")).toBeNull();
  });

  // A panel's own query naming an attribute this deployment does not
  // have used to drop it in silence: the panel replaced the default
  // narrowing with a question that asked nothing and showed every task
  // without a word (grain/task-1421).
  it("says which of its own conditions it could not apply", () => {
    draw({
      board: board([
        { kind: "list", title: "Bad", query: "f=nosuchattr:any:x" },
        { kind: "list", title: "Good", query: "f=state:any:queued" },
      ]),
    });
    const bad = screen.getByText("Bad").closest(".panel");
    const note = bad.querySelector(".panel-unknown-note");
    expect(note).not.toBeNull();
    expect(note.textContent).toContain("Unknown condition: nosuchattr");
    const good = screen.getByText("Good").closest(".panel");
    expect(good.querySelector(".panel-unknown-note")).toBeNull();
  });

  // ...and the same for a comparison this build has never heard of,
  // which used to be parsed away before anything could name it: a panel
  // asking `f=state:bogus:running` showed every task (grain/task-1531).
  it("says which of its own comparisons it could not apply", () => {
    draw({
      board: board([
        { kind: "list", title: "Bad op", query: "f=state:bogus:running" },
      ]),
    });
    const bad = screen.getByText("Bad op").closest(".panel");
    const note = bad.querySelector(".panel-unknown-note");
    expect(note).not.toBeNull();
    expect(note.textContent).toContain("Unknown condition: state:bogus");
  });

  // The one panel that is not about the task list asks the server for
  // the same report the Metrics pane prints, over its own window.
  it("asks the metrics report for the throughput panel's own window", async () => {
    draw({ board: board([{ kind: "metrics", range: "30d" }]) });
    await waitFor(() => expect(api).toHaveBeenCalled());
    expect(api.mock.calls[0][0]).toContain("window=30d");
    expect(await screen.findByText("12")).toBeInTheDocument();
    expect(screen.getByText("completed")).toBeInTheDocument();
  });

  // ...and it asks it about what the dashboard is about, which is the
  // whole of grain/task-317: the endpoint takes a scope now, so the one
  // panel where narrowing a dashboard used to do nothing narrows with
  // the rest.
  it("scopes the throughput panel to what the dashboard is asking", async () => {
    draw({
      board: board([{ kind: "metrics" }]),
      narrowing: narrowingFromQuery("f=repo:any:acme/widgets"),
    });
    await waitFor(() => expect(api).toHaveBeenCalled());
    expect(askedFor().getAll("repo")).toEqual(["acme/widgets"]);
  });

  // A panel that has said what it measures keeps measuring it, the same
  // rule every other panel's own query gets (panels.js's
  // panelNarrowing): the dashboard's question is the fallback, not
  // something intersected with the panel's.
  it("leaves a throughput panel with its own question out of the dashboard's", async () => {
    draw({
      board: board([{ kind: "metrics", query: "f=repo:any:acme/gadgets" }]),
      narrowing: narrowingFromQuery("f=repo:any:acme/widgets"),
    });
    await waitFor(() => expect(api).toHaveBeenCalled());
    expect(askedFor().getAll("repo")).toEqual(["acme/gadgets"]);
  });

  // What a report cannot be asked is dropped, and the panel says so
  // where it has room to: two counts and two sparklines have none, so
  // it is the tooltip rather than the line ReportBoard prints.
  it("says what the report was scoped to and what it could not use", async () => {
    draw({
      board: board([{ kind: "metrics" }]),
      narrowing: narrowingFromQuery("f=repo:any:acme/widgets&q=flake"),
    });
    const note = await screen.findByTitle(/Measured over the tasks/);
    expect(note.getAttribute("title")).toContain("in acme/widgets");
    expect(note.getAttribute("title")).toContain("not in force");
    expect(note.getAttribute("title")).toContain("flake");
  });

  // The heading of a throughput panel leads to the report it is a small
  // copy of, scoped and windowed to what the sparkline is drawing rather
  // than to the deployment-wide report -- which would answer a wider
  // question than the panel without saying so (panels.js's
  // panelDestination). The click opens the same thing the href names.
  it("links a throughput panel's heading to its own report", async () => {
    const onOpen = vi.fn();
    draw({
      board: board([{ kind: "metrics", title: "Throughput", range: "30d" }]),
      narrowing: narrowingFromQuery("f=repo:any:acme/widgets"),
      onOpen,
    });
    const panel = screen.getByText("Throughput").closest(".panel");
    expect(within(panel).getByText("Open").getAttribute("href")).toBe(
      "/system/metrics?f=repo:any:acme/widgets&f=created:within:30",
    );
    await userEvent.click(within(panel).getByText("Open"));
    expect(onOpen).toHaveBeenCalledWith(
      expect.objectContaining({ view: "system", systemTab: "metrics" }),
    );
  });

  // A dashboard whose report cannot be loaded is still a dashboard: the
  // panel says so and the others go on drawing.
  it("says so inside the panel when the report cannot be loaded", async () => {
    api.mockRejectedValue(new Error("nope"));
    draw({
      board: board([{ kind: "metrics" }, { kind: "list", title: "Tasks" }]),
    });
    expect(
      await screen.findByText("The metrics report could not be loaded."),
    ).toBeInTheDocument();
    expect(screen.getByText("Fix the flake")).toBeInTheDocument();
  });

  // Changing a dashboard is the same road every other change to a board
  // takes: a pre-written task, not a form (boards.js).
  it("files a task to add a panel", async () => {
    const onAddPanel = vi.fn();
    draw({ onAddPanel });
    await userEvent.click(screen.getByRole("button", { name: "Add panel" }));
    expect(onAddPanel).toHaveBeenCalledWith(
      expect.objectContaining({ id: "board-1" }),
    );
  });

  // "Keep": the step between narrowing a dashboard live and filing a
  // task about it. The reader narrows the toolbar, one panel says what
  // they meant, and keeping it is a click on that panel rather than a
  // paragraph describing the query they are looking at.
  it("offers to keep the narrowing on the panel that is showing it", async () => {
    const onKeepPanelQuery = vi.fn();
    render(
      <Narrowable
        board={board([{ kind: "list", title: "Live work" }])}
        onKeepPanelQuery={onKeepPanelQuery}
      />,
    );
    // Nothing to keep until the reader has narrowed it to something the
    // board does not already say.
    expect(screen.queryByRole("button", { name: /^Keep/ })).toBeNull();

    await userEvent.type(
      screen.getByPlaceholderText("Search this dashboard…"),
      "flake",
    );
    const keep = await screen.findByRole("button", {
      name: "Keep this narrowing on “Live work”",
    });
    await userEvent.click(keep);
    expect(onKeepPanelQuery).toHaveBeenCalledTimes(1);
    expect(onKeepPanelQuery.mock.calls[0][0].id).toBe("board-1");
    expect(onKeepPanelQuery.mock.calls[0][1].title).toBe("Live work");
  });

  // Two panels do not offer it, and each for its own reason: one that
  // carries a question of its own is not showing this narrowing at all
  // (it does not follow the toolbar), and the throughput panel reads a
  // query as a scope, so a search term is a narrowing it is not showing
  // either.
  it("does not offer it on a panel the narrowing is not showing in", async () => {
    render(
      <Narrowable
        board={board([
          { kind: "list", title: "Live work" },
          { kind: "list", title: "Running now", query: "f=state:any:running" },
          { kind: "metrics", title: "Throughput" },
        ])}
        onKeepPanelQuery={vi.fn()}
      />,
    );
    await userEvent.type(
      screen.getByPlaceholderText("Search this dashboard…"),
      "flake",
    );
    await screen.findByRole("button", {
      name: "Keep this narrowing on “Live work”",
    });
    for (const title of ["Running now", "Throughput"]) {
      const panel = screen.getByText(title).closest(".panel");
      expect(within(panel).queryByRole("button", { name: /^Keep/ })).toBeNull();
    }
  });

  // ...but the throughput panel does offer it for a narrowing its report
  // can actually be scoped by (grain/task-317): a repo reaches it, and
  // keeping that on the panel is the same gesture as on the other three.
  it("offers it on the throughput panel for a narrowing its report can use", async () => {
    render(
      <Narrowable
        board={board([{ kind: "metrics", title: "Throughput" }])}
        start={narrowingFromQuery("f=repo:any:acme/widgets")}
        onKeepPanelQuery={vi.fn()}
      />,
    );
    const panel = screen.getByText("Throughput").closest(".panel");
    await waitFor(() =>
      expect(
        within(panel).getByRole("button", { name: /^Keep/ }),
      ).toBeInTheDocument(),
    );
  });

  // A dashboard whose board carries a query opens on it, so a toolbar
  // still showing exactly that is nobody's narrowing: there is nothing
  // to keep until the reader changes it (boards.js's keptQuery, the
  // reading "Add panel" already took).
  it("offers nothing to keep on a dashboard opened on its own question", () => {
    render(
      <Narrowable
        board={board([{ kind: "list", title: "Live work" }], {
          query: "f=repo:any:acme/widgets",
        })}
        start={narrowingFromQuery("f=repo:any:acme/widgets")}
        onKeepPanelQuery={vi.fn()}
      />,
    );
    expect(screen.queryByRole("button", { name: /^Keep/ })).toBeNull();
  });

  // A dashboard whose board names no query does not open on nothing
  // either: it opens on the deployment's own default (grain/task-309),
  // so the state condition already on its toolbar is a condition nobody
  // typed. There is nothing to keep until the reader narrows it -- and
  // nothing again the moment Clear puts the toolbar back to exactly
  // that, which is what Clear means here.
  it("offers nothing to keep on the default a dashboard opens on, or after Clear", async () => {
    render(
      <Narrowable
        board={board([{ kind: "list", title: "Live work" }])}
        start={defaultNarrowing(config)}
        onKeepPanelQuery={vi.fn()}
      />,
    );
    expect(screen.queryByRole("button", { name: /^Keep/ })).toBeNull();

    await userEvent.type(
      screen.getByPlaceholderText("Search this dashboard…"),
      "flake",
    );
    await screen.findByRole("button", {
      name: "Keep this narrowing on “Live work”",
    });

    await userEvent.click(screen.getByRole("button", { name: "Clear" }));
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: /^Keep/ })).toBeNull(),
    );
  });

  it("says how to fill a dashboard that has no panels yet", () => {
    draw({ board: board([]) });
    expect(screen.getByText(/no panels yet/)).toBeInTheDocument();
  });

  // ...and the one change to a dashboard that is not a task at all: where
  // a panel sits (grain/task-368, panelOrder.js). Driven through the
  // handle's tap gesture rather than the mouse drag, for the reason the
  // boards e2e spec gives: two clicks are the same drop and are
  // deterministic, where a synthesized HTML5 drag is a bet on jsdom's
  // idea of the event sequence.
  describe("moving a panel", () => {
    const three = board([
      { kind: "list", title: "Live work" },
      { kind: "states", title: "Where everything is" },
      { kind: "gantt", title: "This week" },
    ]);

    it("offers nothing to move on a dashboard it cannot write", () => {
      draw({ board: three });
      expect(screen.queryByRole("button", { name: /^Move / })).toBeNull();
    });

    it("puts a panel above the one whose slot is tapped", async () => {
      const onMovePanel = vi.fn();
      draw({ board: three, onMovePanel });
      await userEvent.click(
        screen.getByRole("button", { name: "Move panel 3, “This week”" }),
      );
      await userEvent.click(
        screen.getByRole("button", {
          name: "Move here, above panel 1, “Live work”",
        }),
      );
      // The board, the panel that moved, and the panel it lands above --
      // a panel is named by its position, since a panel row has no id
      // anybody chose (panels.js).
      expect(onMovePanel).toHaveBeenCalledWith(
        expect.objectContaining({ id: "board-1" }),
        "panel-2",
        "panel-0",
      );
    });

    it("puts a panel at the end of the dashboard", async () => {
      const onMovePanel = vi.fn();
      draw({ board: three, onMovePanel });
      await userEvent.click(
        screen.getByRole("button", { name: "Move panel 1, “Live work”" }),
      );
      await userEvent.click(
        screen.getByRole("button", {
          name: "Move here, at the end of the dashboard",
        }),
      );
      expect(onMovePanel).toHaveBeenCalledWith(
        expect.objectContaining({ id: "board-1" }),
        "panel-0",
        null,
      );
    });

    // The gap it is already in is not offered: a slot that promises a
    // move and writes nothing reads as a broken button.
    it("does not offer the place the panel already has", async () => {
      draw({ board: three, onMovePanel: vi.fn() });
      await userEvent.click(
        screen.getByRole("button", { name: "Move panel 1, “Live work”" }),
      );
      expect(
        screen.queryByRole("button", {
          name: "Move here, above panel 2, “Where everything is”",
        }),
      ).toBeNull();
      expect(
        screen.getByRole("button", {
          name: "Move here, above panel 3, “This week”",
        }),
      ).toBeInTheDocument();
    });

    // Two panels with the same heading, or none, are still two things a
    // reader has to tell apart -- the number is the only thing about a
    // panel row that can.
    it("names panels that share a heading by their place", async () => {
      draw({
        board: board([{ kind: "list" }, { kind: "list" }]),
        onMovePanel: vi.fn(),
      });
      expect(
        screen.getByRole("button", { name: "Move panel 1, “Task list”" }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Move panel 2, “Task list”" }),
      ).toBeInTheDocument();
    });

    // A move rewrites the board, and the board comes back with the
    // panels in their new order. The handle a keyboard put the panel
    // down with has to still be that panel's: rendered by position, it
    // stayed where the panel had been and was relabelled with the panel
    // that moved into its place, so the next Enter picked up the wrong
    // one.
    it("leaves focus on the moved panel's own handle", async () => {
      function Movable() {
        const [row, setRow] = useState(three);
        return (
          <PanelBoard
            board={row}
            tasks={tasks}
            config={config}
            onOpenTask={() => {}}
            now={NOW}
            onMovePanel={(b, key, beforeKey) =>
              setRow(
                normalizeBoard({
                  ...b,
                  panels: panelMove(b.panels, key, beforeKey),
                }),
              )
            }
          />
        );
      }
      const user = userEvent.setup();
      render(<Movable />);
      screen.getByRole("button", { name: "Move panel 3, “This week”" }).focus();
      await user.keyboard("{Enter}");
      screen
        .getByRole("button", { name: "Move here, above panel 1, “Live work”" })
        .focus();
      await user.keyboard("{Enter}");
      await waitFor(() =>
        expect(
          screen.getByRole("button", { name: "Move panel 1, “This week”" }),
        ).toHaveFocus(),
      );
    });

    it("disables the handle on the only panel there is", () => {
      draw({
        board: board([{ kind: "list", title: "Live work" }]),
        onMovePanel: vi.fn(),
      });
      expect(
        screen.getByRole("button", { name: "Move panel 1, “Live work”" }),
      ).toBeDisabled();
    });

    // Picking a panel up stacks the dashboard: the slots between the
    // panels are a column of drop targets rather than gaps wedged into a
    // wrapping row (this file's own "Why a picked-up panel stacks the
    // dashboard").
    it("stacks the dashboard while a panel is up, and unstacks it after", async () => {
      const { container } = render(
        <PanelBoard
          board={three}
          tasks={tasks}
          config={config}
          onOpenTask={() => {}}
          now={NOW}
          onMovePanel={vi.fn()}
        />,
      );
      const grid = () => container.querySelector(".panel-grid");
      expect(grid().className).not.toContain("panel-grid-arranging");
      await userEvent.click(
        screen.getByRole("button", { name: "Move panel 1, “Live work”" }),
      );
      expect(grid().className).toContain("panel-grid-arranging");
      await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
      expect(grid().className).not.toContain("panel-grid-arranging");
    });

    // A pick-up is a panel's *position*, so it means a different panel
    // on the next dashboard. Opening another board puts it down.
    it("puts a picked-up panel down when another dashboard is opened", async () => {
      const onMovePanel = vi.fn();
      const { rerender } = render(
        <PanelBoard
          board={three}
          tasks={tasks}
          config={config}
          onOpenTask={() => {}}
          now={NOW}
          onMovePanel={onMovePanel}
        />,
      );
      await userEvent.click(
        screen.getByRole("button", { name: "Move panel 1, “Live work”" }),
      );
      expect(screen.getByRole("status")).toHaveTextContent("Moving 1 panel");
      rerender(
        <PanelBoard
          board={board([{ kind: "list", title: "Somewhere else" }], {
            id: "board-2",
          })}
          tasks={tasks}
          config={config}
          onOpenTask={() => {}}
          now={NOW}
          onMovePanel={onMovePanel}
        />,
      );
      expect(screen.queryByRole("status")).toBeNull();
    });

    it("puts a picked-up panel down when that panel is removed from the dashboard", async () => {
      const onMovePanel = vi.fn();
      const { container, rerender } = render(
        <PanelBoard
          board={three}
          tasks={tasks}
          config={config}
          onOpenTask={() => {}}
          now={NOW}
          onMovePanel={onMovePanel}
        />,
      );
      await userEvent.click(
        screen.getByRole("button", { name: "Move panel 3, “This week”" }),
      );
      expect(screen.getByRole("status")).toHaveTextContent("Moving 1 panel");
      rerender(
        <PanelBoard
          board={board([
            { kind: "list", title: "Live work" },
            { kind: "states", title: "Where everything is" },
          ])}
          tasks={tasks}
          config={config}
          onOpenTask={() => {}}
          now={NOW}
          onMovePanel={onMovePanel}
        />,
      );
      expect(screen.queryByRole("status")).toBeNull();
      expect(container.querySelector(".panel-grid").className).not.toContain(
        "panel-grid-arranging",
      );
    });
  });

  // What a dashboard costs, pinned as a bound rather than as a
  // measurement.
  //
  // Six panels over 929 tasks were 1.0-1.2s of script on every
  // three-second poll and ten were 2.1-2.3s -- a saturated main thread
  // on a deployment where nothing was happening
  // (the 2026-09-07 panel-dashboard hand run, section 5). The cause
  // was not the panels: it was one shared list sorted and filtered once
  // per panel, N answers to the question the dashboard asked once.
  // taskFilters.js's narrowTasks is where that is now answered once and
  // shared, and this is the test that fails if a panel ever goes back to
  // asking on its own.
  //
  // How it counts: the tasks report every read of their title, which the
  // sort below is over, and the panels are state counts -- the one kind
  // that draws no titles of its own, so every read here is a narrowing's
  // and nothing else's.
  describe("what N panels cost", () => {
    const counted = (title, state, reads) => {
      const t = task({ id: title, state });
      Object.defineProperty(t, "title", {
        enumerable: true,
        get() {
          reads.count++;
          return title;
        },
      });
      return t;
    };

    // A dashboard sorted by title, which is what makes the sort readable
    // from outside. Six state-count panels, none of them carrying a
    // query, so every one of them inherits it.
    const cost = (panels) => {
      const reads = { count: 0 };
      const titles = ["c", "a", "d", "b", "e"];
      render(
        <PanelBoard
          board={board(
            Array.from({ length: panels }, () => ({ kind: "states" })),
          )}
          tasks={titles.map((n, i) =>
            counted(n, i % 2 === 0 ? "running" : "queued", reads),
          )}
          config={config}
          narrowing={{
            search: "",
            sort: [{ attr: "title", dir: "asc" }],
            filters: [],
          }}
          onOpenTask={() => {}}
          now={NOW}
        />,
      );
      return reads.count;
    };

    it("asks one question of the task list, however many panels ask it", () => {
      const one = cost(1);
      // The sort really did happen -- a bound of zero would pass on a
      // dashboard that had stopped narrowing anything at all.
      expect(one).toBeGreaterThan(0);
      expect(cost(6)).toBe(one);
    });
  });
});
