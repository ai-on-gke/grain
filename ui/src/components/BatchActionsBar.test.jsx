import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import BatchActionsBar from "./BatchActionsBar.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

describe("BatchActionsBar", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    api.mockReset();
  });

  it("renders nothing when no task is selected", () => {
    const { container } = render(
      <BatchActionsBar
        selection={[]}
        config={null}
        onRun={() => {}}
        onClear={() => {}}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  // Manual mode: the batch form of Run now, one request per task.
  it("asks for a run of every selected task with Run selected now", async () => {
    const onRun = vi.fn();
    render(
      <BatchActionsBar
        selection={[{ id: "1" }, { id: "2" }]}
        config={{ pipelinePause: { paused: true, runNowPasses: true } }}
        onRun={onRun}
        onClear={() => {}}
      />,
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Run selected now" }),
    );
    expect(onRun).toHaveBeenCalledTimes(1);
    onRun.mock.calls[0][0]("7");
    expect(api).toHaveBeenCalledWith("/api/tasks/7/run-now", {
      method: "POST",
    });
  });

  it("shows the selection count", () => {
    render(
      <BatchActionsBar
        selection={[{ id: "1" }, { id: "2" }, { id: "3" }]}
        config={null}
        onRun={() => {}}
        onClear={() => {}}
      />,
    );
    expect(screen.getByText("3 selected")).toBeInTheDocument();
  });

  // A selection outlives the view it was made in. Ticking rows under
  // Queued and opening Running used to leave a bar reading "2 selected"
  // over a list with nothing ticked, whose Close would close both.
  it("says how much of the selection the list is not showing", () => {
    const props = { config: null, onRun: () => {}, onClear: () => {} };
    const selection = [{ id: "1" }, { id: "2" }, { id: "3" }];
    const { rerender } = render(
      <BatchActionsBar
        selection={selection}
        shownIds={new Set(["1", "9"])}
        {...props}
      />,
    );
    expect(screen.getByText("2 not shown here")).toBeInTheDocument();

    rerender(
      <BatchActionsBar
        selection={selection}
        shownIds={new Set(["9"])}
        {...props}
      />,
    );
    expect(screen.getByText("none of them shown here")).toBeInTheDocument();

    rerender(
      <BatchActionsBar
        selection={selection}
        shownIds={new Set(["1", "2", "3"])}
        {...props}
      />,
    );
    expect(screen.queryByText(/shown here/)).not.toBeInTheDocument();

    // A board passes no shownIds: its cards are not this list's rows.
    rerender(<BatchActionsBar selection={selection} {...props} />);
    expect(screen.queryByText(/shown here/)).not.toBeInTheDocument();
  });

  it("warns before closing tasks that are not on screen", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    const user = userEvent.setup();
    render(
      <BatchActionsBar
        selection={[{ id: "1" }, { id: "2" }]}
        shownIds={new Set(["2"])}
        config={null}
        onRun={() => {}}
        onClear={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Close" }));

    expect(confirmSpy).toHaveBeenCalledWith(
      "Close 2 task(s)? 1 of them is not shown in this view.",
    );
  });

  // The bar is fixed to the bottom edge, so it stands on whatever is
  // scrolling underneath it -- the end of the task list, the end of a
  // board column -- and style.css reserves room for it out of the
  // property published here. jsdom measures everything as 0, so what
  // this can ask is that the reservation is made while there is a bar
  // and taken back when the selection empties: a stale one would leave
  // the list ending short of a bar nobody can see.
  it("publishes its height while it is up, and takes it back", () => {
    const height = () =>
      document.documentElement.style.getPropertyValue("--batch-bar-height");
    const props = { config: null, onRun: () => {}, onClear: () => {} };
    const { rerender, unmount } = render(
      <BatchActionsBar selection={[{ id: "1" }]} {...props} />,
    );
    expect(height()).not.toBe("");

    rerender(<BatchActionsBar selection={[]} {...props} />);
    expect(height()).toBe("");

    rerender(<BatchActionsBar selection={[{ id: "1" }]} {...props} />);
    expect(height()).not.toBe("");
    unmount();
    expect(height()).toBe("");
  });

  it("runs the approve action against each selected task via onRun", async () => {
    const onRun = vi.fn((mutate) => mutate(42));
    const user = userEvent.setup();
    render(
      <BatchActionsBar
        selection={[{ id: "1" }, { id: "2" }]}
        config={null}
        onRun={onRun}
        onClear={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(onRun).toHaveBeenCalledTimes(1);
    expect(api).toHaveBeenCalledWith("/api/tasks/42/approve", {
      method: "POST",
    });
  });

  // No confirmation on either of these, unlike Close: deferring loses
  // nothing and undoes with the button beside it.
  it("runs defer and undefer against each selected task", async () => {
    const onRun = vi.fn((mutate) => mutate(42));
    const user = userEvent.setup();
    render(
      <BatchActionsBar
        selection={[{ id: "1" }, { id: "2" }]}
        config={null}
        onRun={onRun}
        onClear={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Defer" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/42/defer", { method: "POST" });

    await user.click(screen.getByRole("button", { name: "Undefer" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/42/undefer", {
      method: "POST",
    });
  });

  it("asks for confirmation before closing, and does nothing when declined", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    const onRun = vi.fn();
    const user = userEvent.setup();
    render(
      <BatchActionsBar
        selection={[{ id: "1" }, { id: "2" }]}
        config={null}
        onRun={onRun}
        onClear={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Close" }));

    expect(confirmSpy).toHaveBeenCalled();
    expect(onRun).not.toHaveBeenCalled();
  });

  it("runs the close action once confirmed", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const onRun = vi.fn();
    const user = userEvent.setup();
    render(
      <BatchActionsBar
        selection={[{ id: "1" }, { id: "2" }]}
        config={null}
        onRun={onRun}
        onClear={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Close" }));

    expect(onRun).toHaveBeenCalledTimes(1);
  });

  it("disables Attach/Detach until a capability is picked", async () => {
    const config = { capabilities: [{ id: "web-search", name: "Web search" }] };
    const user = userEvent.setup();
    render(
      <BatchActionsBar
        selection={[{ id: "1" }]}
        config={config}
        onRun={() => {}}
        onClear={() => {}}
      />,
    );

    expect(screen.getByRole("button", { name: "Attach" })).toBeDisabled();

    // Named in its own right: the "Capability…" placeholder option was
    // its only label, and stops being one once a capability is picked.
    await user.selectOptions(
      screen.getByRole("combobox", { name: "Capability to attach or detach" }),
      "web-search",
    );

    expect(screen.getByRole("button", { name: "Attach" })).toBeEnabled();
    expect(
      screen.getByRole("combobox", { name: "Capability to attach or detach" }),
    ).toHaveValue("web-search");
  });

  it("opens the bulk edit form over the selection", async () => {
    const user = userEvent.setup();
    render(
      <BatchActionsBar
        selection={[{ id: "1", repo: "acme/widgets" }]}
        tasks={[{ id: "1", repo: "acme/widgets" }]}
        config={null}
        templates={[]}
        onRun={() => {}}
        onClear={() => {}}
      />,
    );

    expect(screen.queryByRole("heading", { name: "Bulk edit" })).toBeNull();

    await user.click(screen.getByRole("button", { name: "Bulk edit…" }));

    expect(
      screen.getByRole("heading", { name: "Bulk edit" }),
    ).toBeInTheDocument();
    // Named, not a bare "dialog" to a screen reader (2026-10-02 intake
    // walkthrough).
    expect(
      screen.getByRole("dialog", { name: "Bulk edit" }),
    ).toBeInTheDocument();
    // No endpoint is touched by opening it: the form is what sends,
    // once a field has actually been picked (BulkEditOverlay).
    expect(api).not.toHaveBeenCalled();
  });

  it("clears the selection via the clear button", async () => {
    const onClear = vi.fn();
    const user = userEvent.setup();
    render(
      <BatchActionsBar
        selection={[{ id: "1" }]}
        config={null}
        onRun={() => {}}
        onClear={onClear}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Clear selection" }));

    expect(onClear).toHaveBeenCalledTimes(1);
  });
});
