import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import api from "../../api.js";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay capabilities", () => {
  afterEach(resetDetailMocks);

  it("toggles a capability via the capabilities select", async () => {
    // act must run its mutate callback synchronously, the same as the
    // real App.jsx does -- the callback closes over the change event's
    // selection, computed from the task's current capabilities, which
    // would otherwise be stale by the time a deferred `act.mock.calls`
    // invocation (as the other assertions in this file use) ran it.
    const act = vi.fn((mutate) => mutate());
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByLabelText("Legacy capabilities"));
    await user.click(await screen.findByRole("option", { name: "Web search" }));

    expect(api).toHaveBeenCalledWith("/api/tasks/12/capabilities", {
      method: "POST",
      body: JSON.stringify({ id: "web-search", attach: true }),
    });
  });

  // A capability the picker stopped listing since the task was granted
  // it ("scratch-repo", renamed to github-sandbox in
  // bwsalmon/agents#612) still needs a row of its own, or the chip for
  // it has nothing to untick and the grant -- which fails every run of
  // the task holding it -- can never be removed.
  it("offers a row for a granted capability the picker no longer lists", async () => {
    const act = vi.fn((mutate) => mutate());
    const user = userEvent.setup();
    const task = { ...baseTask, capabilities: ["scratch-repo"] };
    render(
      <DetailOverlay
        task={task}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByLabelText("Legacy capabilities"));
    await user.click(
      await screen.findByRole("option", { name: "scratch-repo" }),
    );

    expect(api).toHaveBeenCalledWith("/api/tasks/12/capabilities", {
      method: "POST",
      body: JSON.stringify({ id: "scratch-repo", attach: false }),
    });
  });
});
