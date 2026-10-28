import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import TaskTreeBoard from "./TaskTreeBoard.jsx";
import { NO_NARROWING } from "../taskFilters.js";

// A chat (12) that filed two tasks, one of which (8) filed a third (11),
// beside a task nobody filed -- the demo's own shape.
const row = (id, title, state, generatedFrom, repo = "acme/widgets") => ({
  id,
  title,
  state,
  repo,
  author: "alice",
  capabilities: [],
  blocked: false,
  generatedFrom,
  createdAt: "2026-10-01T00:00:00Z",
});
const tasks = [
  { ...row("12", "Director Session", "queued"), consoleSession: "director" },
  row("3", "Bump the Go toolchain", "running", "12"),
  row("8", "Add pagination to the tasks API", "running", "12"),
  row("11", "Extract pagination query helper", "queued", "8"),
  row("2", "Fix flaky retry test", "queued", undefined, "acme/gadgets"),
];

// Narrowed holds the narrowing the way App does, so the toolbar's
// patches land.
function Narrowed({ props }) {
  const [narrowing, setNarrowing] = useState(props.narrowing || NO_NARROWING);
  return (
    <TaskTreeBoard
      {...props}
      narrowing={narrowing}
      onNarrow={(patch) => setNarrowing((prev) => ({ ...prev, ...patch }))}
    />
  );
}

function renderTree(overrides = {}) {
  const props = { tasks, config: null, onOpenTask: vi.fn(), ...overrides };
  render(<Narrowed props={props} />);
  return props;
}

const shownIds = () =>
  [...document.querySelectorAll(".task-tree li")].map(
    (li) => li.dataset.taskId,
  );

describe("TaskTreeBoard", () => {
  it("nests every task under the one that filed it", () => {
    renderTree();
    expect(shownIds()).toEqual(["12", "3", "8", "11", "2"]);
    expect(screen.getByRole("main")).toBeInTheDocument();
  });

  it("draws no main of its own under a board's page", () => {
    // BoardPage is the landmark there (grain/task-1253).
    renderTree({ landmark: false });
    expect(shownIds()).toEqual(["12", "3", "8", "11", "2"]);
    expect(screen.queryByRole("main")).not.toBeInTheDocument();
  });

  it("orders roots and siblings by the sort it is given", () => {
    renderTree({
      narrowing: { ...NO_NARROWING, sort: [{ attr: "title", dir: "asc" }] },
    });
    // Director Session before Fix flaky retry test, and under the
    // session "Add pagination…" (with its own follow-up) before "Bump…".
    expect(shownIds()).toEqual(["12", "8", "11", "3", "2"]);
  });

  it("names its unsorted order for what it is", () => {
    renderTree();
    expect(
      screen.getByRole("button", { name: /Newest first/ }),
    ).toBeInTheDocument();
  });

  it("draws only what the board's saved query admits", () => {
    renderTree({
      narrowing: {
        ...NO_NARROWING,
        filters: [{ attr: "repo", op: "any", values: ["acme/gadgets"] }],
      },
    });
    expect(shownIds()).toEqual(["2"]);
  });

  it("narrows from its own search field, keeping a match whose parent is out", async () => {
    renderTree();
    await userEvent.type(screen.getByRole("textbox"), "query helper");
    expect(shownIds()).toEqual(["11"]);
    await userEvent.clear(screen.getByRole("textbox"));
    await userEvent.type(screen.getByRole("textbox"), "zzzznothing");
    expect(shownIds()).toEqual([]);
    expect(
      screen.getByText("No tasks match this search and these filters."),
    ).toBeInTheDocument();
  });

  it("opens a task on a plain click and leaves a modified one to the browser", () => {
    const { onOpenTask } = renderTree();
    const link = screen.getByRole("link", { name: "11" });
    // Whether the tree took the click, read after React has seen it --
    // then taken here anyway, since jsdom cannot follow a link.
    const prevented = [];
    const watch = (e) => {
      prevented.push(e.defaultPrevented);
      e.preventDefault();
    };
    document.addEventListener("click", watch);
    try {
      fireEvent.click(link, { ctrlKey: true });
      fireEvent.click(link, { metaKey: true });
      expect(onOpenTask).not.toHaveBeenCalled();
      fireEvent.click(link);
      expect(onOpenTask).toHaveBeenCalledWith("11");
    } finally {
      document.removeEventListener("click", watch);
    }
    expect(prevented).toEqual([false, false, true]);
  });

  it("opens a task and focuses its row link when clicking anywhere on the tree row", async () => {
    const { onOpenTask } = renderTree();
    const link = screen.getByRole("link", { name: "11" });
    const title = screen.getByText("Extract pagination query helper");
    await userEvent.click(title);
    expect(onOpenTask).toHaveBeenCalledWith("11");
    expect(document.activeElement).toBe(link);
  });
});
