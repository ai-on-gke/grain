import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ITEM_GLYPH_PATHS } from "../brand/item-glyph-paths.js";
import BoardsPage from "./BoardsPage.jsx";
import { RowLink } from "./ListPrimitives.jsx";

const boards = [
  {
    id: "board-1",
    name: "Waiting on me",
    folder: "",
    kind: "list",
    description: "Everything parked on a question",
    query: "f=state:any:awaiting_reply",
  },
  {
    id: "board-2",
    name: "Flaky tests",
    folder: "Reports",
    kind: "kanban",
    description: "",
    query: "",
  },
];

describe("BoardsPage", () => {
  // grain/task-1580: a board opens a page in the list's place, so Back
  // mounted a fresh list with focus on <body> and the keyboard user's
  // place was lost. The link that opened it takes focus back.
  it("puts focus back on the board's link when the list comes back", async () => {
    const user = userEvent.setup();
    const props = { boards, onOpenBoard: () => {}, onNewBoard: () => {} };
    const { unmount } = render(<BoardsPage {...props} />);
    screen.getByRole("link", { name: "Flaky tests" }).focus();
    await user.keyboard("{Enter}");
    unmount();
    expect(document.activeElement).toBe(document.body);

    const again = render(<BoardsPage {...props} />);
    expect(screen.getByRole("link", { name: "Flaky tests" })).toHaveFocus();
    again.unmount();

    // Once: a later visit to the list leaves focus where it is.
    render(<BoardsPage {...props} />);
    expect(document.activeElement).toBe(document.body);
  });

  // 2026-10-22 boards walkthrough: a board opened by clicking its row off
  // the title link (e.g. its kind chip or description) left returnFocusTo
  // unset, and opening any task RowLink inside a board overwrote the
  // single returnFocusTo with "/tasks/N", so coming Back to /boards in
  // either case dropped focus to <body>.
  it("keeps return focus on the board's link after an off-title click or opening a task inside the board", async () => {
    const user = userEvent.setup();
    const props = { boards, onOpenBoard: () => {}, onNewBoard: () => {} };

    // 1. Off-title click on the board row (the kind chip).
    const first = render(<BoardsPage {...props} />);
    await user.click(screen.getByText("Kanban"));
    expect(screen.getByRole("link", { name: "Flaky tests" })).toHaveFocus();
    first.unmount();

    const afterOffTitle = render(<BoardsPage {...props} />);
    expect(screen.getByRole("link", { name: "Flaky tests" })).toHaveFocus();
    afterOffTitle.unmount();

    // 2. Opening a board via its link, then opening a task inside that
    // board before coming Back to /boards.
    const second = render(<BoardsPage {...props} />);
    screen.getByRole("link", { name: "Waiting on me" }).focus();
    await user.keyboard("{Enter}");
    second.unmount();

    const taskRow = render(
      <RowLink href="/tasks/42" onOpen={() => {}}>
        Fix flaky retry test
      </RowLink>,
    );
    await user.click(
      screen.getByRole("link", { name: "Fix flaky retry test" }),
    );
    taskRow.unmount();

    const afterTask = render(<BoardsPage {...props} />);
    expect(screen.getByRole("link", { name: "Waiting on me" })).toHaveFocus();
    afterTask.unmount();
  });

  // grain/task-1429: a board opened only on a pointer click on its row.
  // Its name is a link to the board's own page now, so Tab reaches it and
  // Enter opens it in place.
  it("opens a board from the keyboard through the link its name is", async () => {
    const onOpenBoard = vi.fn();
    const user = userEvent.setup();
    render(
      <BoardsPage
        boards={boards}
        onOpenBoard={onOpenBoard}
        onNewBoard={() => {}}
      />,
    );

    const link = screen.getByRole("link", { name: "Waiting on me" });
    expect(link).toHaveAttribute("href", "/boards/board-1");
    link.focus();
    await user.keyboard("{Enter}");
    expect(onOpenBoard).toHaveBeenCalledTimes(1);
    expect(onOpenBoard).toHaveBeenCalledWith("board-1");

    // A click on the name opens it once, not once for the link and once
    // more for the row around it.
    onOpenBoard.mockClear();
    await user.click(link);
    expect(onOpenBoard).toHaveBeenCalledTimes(1);
  });

  it("lists the boards under their folders, with what each one is", () => {
    const { container } = render(
      <BoardsPage
        boards={boards}
        onOpenBoard={() => {}}
        onNewBoard={() => {}}
      />,
    );

    // The item figure belongs once on the list heading; individual board
    // rows are saved task views and do not repeat it.
    const glyphs = container.querySelectorAll('svg[data-glyph="boards"] path');
    expect(glyphs).toHaveLength(1);
    expect(glyphs[0]).toHaveAttribute("d", ITEM_GLYPH_PATHS.boards);

    expect(screen.getByText("Waiting on me")).toBeInTheDocument();
    expect(
      screen.getByText("Everything parked on a question"),
    ).toBeInTheDocument();
    expect(screen.getByText("Reports")).toBeInTheDocument();
    expect(screen.getByText("Kanban")).toBeInTheDocument();
    // A board that narrows nothing says so rather than showing a blank
    // line where every other row has a description.
    expect(screen.getByText("every task")).toBeInTheDocument();
  });

  it("opens the board that was clicked", async () => {
    const onOpenBoard = vi.fn();
    const user = userEvent.setup();
    render(
      <BoardsPage
        boards={boards}
        onOpenBoard={onOpenBoard}
        onNewBoard={() => {}}
      />,
    );

    await user.click(screen.getByText("Waiting on me"));
    expect(onOpenBoard).toHaveBeenCalledWith("board-1");
  });

  // A deployment with no boards is where somebody learns that boards
  // exist, so the empty state is the button said louder rather than a
  // sentence saying there is nothing here.
  it("offers a new board from an empty page", async () => {
    const onNewBoard = vi.fn();
    const user = userEvent.setup();
    render(
      <BoardsPage boards={[]} onOpenBoard={() => {}} onNewBoard={onNewBoard} />,
    );

    expect(screen.getByText("No boards yet.")).toBeInTheDocument();
    const buttons = screen.getAllByRole("button", { name: "New board" });
    await user.click(buttons[buttons.length - 1]);
    expect(onNewBoard).toHaveBeenCalled();
  });

  // ...and says, in as many words, that a board is made by asking an
  // agent for one -- which is the part nobody would guess.
  it("says how a board gets made", () => {
    render(
      <BoardsPage
        boards={boards}
        onOpenBoard={() => {}}
        onNewBoard={() => {}}
      />,
    );

    expect(
      screen.getByText(/opens a task, already written/),
    ).toBeInTheDocument();
  });

  // grain/task-81: where a board sits is the one thing about it this
  // page writes itself, and this is the page that carries the handle --
  // the rail is drag-only, so on a phone these buttons are the only way
  // a board moves at all.
  describe("moving a board", () => {
    const renderPage = (onMoveBoard) =>
      render(
        <BoardsPage
          boards={boards}
          onOpenBoard={() => {}}
          onNewBoard={() => {}}
          onMoveBoard={onMoveBoard}
        />,
      );

    it("files a picked-up board into another folder", async () => {
      const onMoveBoard = vi.fn();
      const user = userEvent.setup();
      renderPage(onMoveBoard);

      await user.click(
        screen.getByRole("button", { name: "Move Waiting on me" }),
      );
      // The slots are every place it could go: above the one board in
      // Reports, or after it. Not where it already is -- it is the only
      // board outside a folder, so the top level offers nothing.
      await user.click(
        screen.getByRole("button", { name: "Move here, above Flaky tests" }),
      );

      expect(onMoveBoard).toHaveBeenCalledWith("board-1", {
        folder: "Reports",
        beforeId: "board-2",
      });
    });

    // 2026-10-12 boards walkthrough: a board moved into another folder is
    // drawn as a new row under that folder's heading once the boards
    // come back, so the handle that was put down went away and focus
    // fell to <body> -- a keyboard user was thrown back to the top of
    // the page. The handle in the board's new place takes it.
    it("keeps focus on the handle of a board moved into another folder", async () => {
      const user = userEvent.setup();
      const props = { onOpenBoard: () => {}, onNewBoard: () => {} };
      const { rerender } = render(
        <BoardsPage {...props} boards={boards} onMoveBoard={() => {}} />,
      );

      screen.getByRole("button", { name: "Move Waiting on me" }).focus();
      await user.keyboard("{Enter}");
      screen
        .getByRole("button", { name: "Move here, at the end of Reports" })
        .focus();
      await user.keyboard("{Enter}");

      // What App hands back once the PATCH has landed.
      const moved = [
        boards[1],
        { ...boards[0], folder: "Reports", orderKey: 1 },
      ];
      rerender(<BoardsPage {...props} boards={moved} onMoveBoard={() => {}} />);
      expect(
        screen.getByRole("button", { name: "Move Waiting on me" }),
      ).toHaveFocus();
    });

    it("offers the end of a folder as well as the gaps in it", async () => {
      const onMoveBoard = vi.fn();
      const user = userEvent.setup();
      renderPage(onMoveBoard);

      await user.click(
        screen.getByRole("button", { name: "Move Waiting on me" }),
      );
      await user.click(
        screen.getByRole("button", {
          name: "Move here, at the end of Reports",
        }),
      );

      expect(onMoveBoard).toHaveBeenCalledWith("board-1", {
        folder: "Reports",
        beforeId: null,
      });
    });

    // A board in a folder can always come back out, even when nothing
    // else is at the top level -- which is what the empty loose section
    // exists for.
    it("offers the top level to a board that is filed away", async () => {
      const onMoveBoard = vi.fn();
      const user = userEvent.setup();
      renderPage(onMoveBoard);

      await user.click(
        screen.getByRole("button", { name: "Move Flaky tests" }),
      );
      await user.click(
        screen.getByRole("button", { name: "Move here, outside every folder" }),
      );

      expect(onMoveBoard).toHaveBeenCalledWith("board-2", {
        folder: "",
        beforeId: null,
      });
    });

    it("drops a dragged board onto a folder's own heading", () => {
      const onMoveBoard = vi.fn();
      renderPage(onMoveBoard);
      const row = screen.getByText("Waiting on me").closest("li");
      const heading = screen.getByText("Reports").closest("li");

      fireEvent.dragStart(row);
      fireEvent.dragOver(heading);
      fireEvent.drop(heading);

      // Dropping on a folder's name means the top of that folder.
      expect(onMoveBoard).toHaveBeenCalledWith("board-1", {
        folder: "Reports",
        beforeId: "board-2",
      });
    });

    // Nothing to move it with, and nothing to move it to, on a page
    // whose caller has not offered the gesture at all.
    it("has no handles when moving is not offered", () => {
      render(
        <BoardsPage
          boards={boards}
          onOpenBoard={() => {}}
          onNewBoard={() => {}}
        />,
      );

      expect(
        screen.queryByRole("button", { name: /^Move / }),
      ).not.toBeInTheDocument();
    });
  });
});
