import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import BoardColumnsOverlay from "./BoardColumnsOverlay.jsx";
import { defaultColumns } from "../board.js";

const columns = [
  { id: "wait", title: "Waiting", states: ["proposed", "queued"] },
  { id: "doing", title: "Doing", states: ["running"] },
];

function renderEditor(overrides = {}) {
  const props = { columns, onSave: vi.fn(), onClose: vi.fn(), ...overrides };
  render(<BoardColumnsOverlay {...props} />);
  return props;
}

describe("BoardColumnsOverlay", () => {
  it("shows every column with its title and its states", () => {
    renderEditor();
    expect(screen.getByLabelText("Column 1 title")).toHaveValue("Waiting");
    expect(screen.getByLabelText("Column 2 title")).toHaveValue("Doing");
    expect(screen.getByText("Proposed, Waiting")).toBeInTheDocument();
  });

  it("renames a column", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor();
    await user.clear(screen.getByLabelText("Column 1 title"));
    await user.type(screen.getByLabelText("Column 1 title"), "Backlog");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onSave.mock.calls[0][0][0]).toMatchObject({
      title: "Backlog",
      states: ["proposed", "queued"],
    });
  });

  it("adds a column, and removes one", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor();
    await user.click(screen.getByRole("button", { name: "Remove Doing" }));
    await user.click(screen.getByRole("button", { name: "+ Add column" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onSave.mock.calls[0][0].map((c) => c.title)).toEqual([
      "Waiting",
      "New column",
    ]);
  });

  // A keyboard reader moving a column to either end, or removing one,
  // used to land on the dialog itself: the arrow they pressed was now
  // disabled, or gone (2026-10-13 boards walkthrough).
  it("keeps focus on the moved column when it reaches either end", async () => {
    const user = userEvent.setup();
    renderEditor({
      columns: [...columns, { id: "done", title: "Done", states: [] }],
    });
    screen.getByRole("button", { name: "Move Doing left" }).focus();
    await user.keyboard("{Enter}");
    expect(
      screen.getByRole("button", { name: "Move Doing left" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Move Doing right" }),
    ).toHaveFocus();
    await user.keyboard("{Enter}");
    // Back in the middle, the arrow pressed is still enabled and keeps focus.
    expect(
      screen.getByRole("button", { name: "Move Doing right" }),
    ).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(
      screen.getByRole("button", { name: "Move Doing left" }),
    ).toHaveFocus();
  });

  it("moves focus to a neighbour when a column is removed, and into a new one", async () => {
    const user = userEvent.setup();
    renderEditor();
    screen.getByRole("button", { name: "Remove Waiting" }).focus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("button", { name: "Remove Doing" })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("button", { name: "+ Add column" })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(screen.getByLabelText("Column 1 title")).toHaveFocus();
  });

  it("reorders the columns", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor();
    await user.click(screen.getByRole("button", { name: "Move Doing left" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onSave.mock.calls[0][0].map((c) => c.title)).toEqual([
      "Doing",
      "Waiting",
    ]);
  });

  it("will not move the first column further left, or the last further right", () => {
    renderEditor();
    expect(
      screen.getByRole("button", { name: "Move Waiting left" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Move Doing right" }),
    ).toBeDisabled();
  });

  // One state, one column: giving a state to a column takes it away
  // from whichever column had it, so no task can be on the board twice.
  it("takes a state out of the column that had it when another claims it", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor();
    await user.click(screen.getAllByLabelText("States")[1]);
    await user.click(screen.getByRole("option", { name: "Waiting" }));
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Save" }));
    const saved = onSave.mock.calls[0][0];
    expect(saved[0].states).toEqual(["proposed"]);
    expect(saved[1].states).toEqual(["running", "queued"]);
  });

  // The states picker always draws something inside its box -- the
  // states picked, or "No states" -- and MUI floats a label clear of the
  // box only for a field it counts as filled, which an empty column is
  // not. Without the shrink the label lands on top of that line and the
  // field reads as garbled text.
  it("keeps the States label clear of what the picker shows", async () => {
    const user = userEvent.setup();
    renderEditor({ columns: [{ id: "wait", title: "Waiting", states: [] }] });
    const label = screen
      .getByLabelText("States")
      .closest(".MuiFormControl-root")
      .querySelector("label");
    expect(label).toHaveAttribute("data-shrink", "true");
    expect(screen.getByText("No states")).toBeInTheDocument();
    // ...and still floated once the column has states of its own.
    await user.click(screen.getByLabelText("States"));
    await user.click(screen.getByRole("option", { name: "Waiting" }));
    await user.keyboard("{Escape}");
    expect(label).toHaveAttribute("data-shrink", "true");
  });

  it("says which states the board will not be showing", async () => {
    const user = userEvent.setup();
    renderEditor();
    expect(screen.getByText(/Off the board:/)).toHaveTextContent("Asked");
    await user.click(screen.getByRole("button", { name: "Remove Doing" }));
    expect(screen.getByText(/Off the board:/)).toHaveTextContent("Running");
  });

  it("puts the default board back", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor();
    await user.click(screen.getByRole("button", { name: "Reset to default" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onSave).toHaveBeenCalledWith(defaultColumns());
  });

  it("saves the default board rather than one with no columns at all", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor();
    await user.click(screen.getByRole("button", { name: "Remove Waiting" }));
    await user.click(screen.getByRole("button", { name: "Remove Doing" }));
    expect(screen.getByText(/No columns/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onSave).toHaveBeenCalledWith(defaultColumns());
  });

  it("names a column somebody emptied the title of after its own states", async () => {
    const user = userEvent.setup();
    const { onSave } = renderEditor();
    await user.clear(screen.getByLabelText("Column 2 title"));
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onSave.mock.calls[0][0][1].title).toBe("Running");
  });

  it("changes nothing on Cancel", async () => {
    const user = userEvent.setup();
    const { onSave, onClose } = renderEditor();
    await user.click(screen.getByRole("button", { name: "Remove Doing" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onSave).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalled();
  });

  it("closes once a save has gone through", async () => {
    const user = userEvent.setup();
    const { onClose } = renderEditor();
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onClose).toHaveBeenCalled();
  });

  describe("leaving", () => {
    afterEach(() => vi.restoreAllMocks());

    it("leaves an untouched editor on Escape without asking", async () => {
      const confirm = vi.spyOn(window, "confirm");
      const user = userEvent.setup();
      const { onClose } = renderEditor();
      await user.keyboard("{Escape}");
      expect(confirm).not.toHaveBeenCalled();
      expect(onClose).toHaveBeenCalled();
    });

    it("asks before Escape throws an edit away, and stays when told to", async () => {
      const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
      const user = userEvent.setup();
      const { onClose, onSave } = renderEditor();
      await user.type(screen.getByLabelText("Column 1 title"), " soon");
      await user.keyboard("{Escape}");
      expect(confirm).toHaveBeenCalledTimes(1);
      expect(onClose).not.toHaveBeenCalled();
      expect(screen.getByLabelText("Column 1 title")).toHaveValue(
        "Waiting soon",
      );
      expect(onSave).not.toHaveBeenCalled();
    });
  });
});
