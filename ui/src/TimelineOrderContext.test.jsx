import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import {
  TimelineOrderProvider,
  useTimelineOrder,
} from "./TimelineOrderContext.jsx";

function Probe() {
  const { order, setOrder } = useTimelineOrder();
  return (
    <div>
      <span data-testid="order">{order}</span>
      <button onClick={() => setOrder("newest")}>newest</button>
      <button onClick={() => setOrder("oldest")}>oldest</button>
    </div>
  );
}

describe("TimelineOrderContext", () => {
  afterEach(() => {
    localStorage.clear();
  });

  it("defaults to newest first with nothing stored", () => {
    render(
      <TimelineOrderProvider>
        <Probe />
      </TimelineOrderProvider>,
    );
    expect(screen.getByTestId("order")).toHaveTextContent("newest");
  });

  it("reads a previously stored order on mount", () => {
    localStorage.setItem("grain.timelineOrder", "oldest");
    render(
      <TimelineOrderProvider>
        <Probe />
      </TimelineOrderProvider>,
    );
    expect(screen.getByTestId("order")).toHaveTextContent("oldest");
  });

  it("ignores a stored value that is not a known order", () => {
    localStorage.setItem("grain.timelineOrder", "sideways");
    render(
      <TimelineOrderProvider>
        <Probe />
      </TimelineOrderProvider>,
    );
    expect(screen.getByTestId("order")).toHaveTextContent("newest");
  });

  it("persists an explicit choice and reflects it immediately", async () => {
    const user = userEvent.setup();
    render(
      <TimelineOrderProvider>
        <Probe />
      </TimelineOrderProvider>,
    );

    await user.click(screen.getByRole("button", { name: "oldest" }));

    expect(screen.getByTestId("order")).toHaveTextContent("oldest");
    expect(localStorage.getItem("grain.timelineOrder")).toBe("oldest");
  });

  it("clears storage when switching back to newest first", async () => {
    localStorage.setItem("grain.timelineOrder", "oldest");
    const user = userEvent.setup();
    render(
      <TimelineOrderProvider>
        <Probe />
      </TimelineOrderProvider>,
    );

    await user.click(screen.getByRole("button", { name: "newest" }));

    expect(screen.getByTestId("order")).toHaveTextContent("newest");
    expect(localStorage.getItem("grain.timelineOrder")).toBeNull();
  });

  it("falls back to newest first outside of a provider", () => {
    render(<Probe />);
    expect(screen.getByTestId("order")).toHaveTextContent("newest");
  });
});
