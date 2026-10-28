import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import api from "../../api.js";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay read-only repos", () => {
  afterEach(resetDetailMocks);

  // grain/task-294: the task's read-only repos are the picker's own
  // picked set, not a static "Reads" row -- so the same chips that say
  // which repos they are are also how one is taken off.
  it("seeds the read-only repos picker from the task's own reads", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, reads: ["acme/shared", "acme/schema"] }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    expect(screen.getByText("acme/shared")).toBeInTheDocument();
    expect(screen.getByText("acme/schema")).toBeInTheDocument();
    expect(screen.getByTitle("Remove acme/shared")).toBeInTheDocument();
  });

  // Reads has no attach/detach endpoint the way capabilities and
  // dependencies do: ui.UpdateTaskRequest.Reads replaces the whole set,
  // so an add has to send the repos the task already had along with it.
  it("patches the whole read-only repo set when one is added", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets", reads: ["acme/shared"] }}
        tasks={[{ id: "13", repo: "acme/schema" }]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByLabelText(/Read-only repos/));
    await user.click(await screen.findByText("acme/schema"));

    expect(api).toHaveBeenCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({ reads: ["acme/shared", "acme/schema"] }),
    });
  });

  // grain/task-271: the repo this task is filed against is already
  // cloned into its sandbox and is the one place its run may push, so
  // the picker does not offer it -- a PATCH naming it is refused
  // (ui.refuseTargetAmongReads).
  it("does not offer the task's own repo among the read-only repos", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets", reads: [] }}
        tasks={[{ id: "13", repo: "acme/schema" }]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByLabelText(/Read-only repos/));

    expect(await screen.findByText("acme/schema")).toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: "acme/widgets" }),
    ).not.toBeInTheDocument();
  });

  it("patches the repos that are left when one is removed", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, reads: ["acme/shared", "acme/schema"] }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByTitle("Remove acme/shared"));

    expect(api).toHaveBeenCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({ reads: ["acme/schema"] }),
    });
  });

  // A whole-set PATCH makes the second edit depend on the first: the
  // picker holds the list it just sent rather than re-reading a task the
  // refresh has not caught up with, or adding a second repo would send a
  // list that had forgotten the first.
  it("computes a second edit from the first, not from the task as it was", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, reads: [] }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    const box = screen.getByLabelText(/Read-only repos/);
    await user.type(box, "acme/shared{Enter}");
    await user.type(box, "acme/schema{Enter}");

    expect(api).toHaveBeenLastCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({ reads: ["acme/shared", "acme/schema"] }),
    });
  });

  // The chips are already showing the new set by the time the server
  // answers, so a refused set (ui.parseReads rejects anything that is not
  // owner/name) has to put them back rather than leave the picker
  // claiming repos the task does not have.
  it("puts the picked repos back when the server refuses the new set", async () => {
    const showError = vi.fn();
    const user = userEvent.setup();
    api.mockRejectedValueOnce(new Error("read-only repo: not a repo"));
    render(
      <DetailOverlay
        task={{ ...baseTask, reads: ["acme/shared"] }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={showError}
      />,
    );

    await user.click(screen.getByTitle("Remove acme/shared"));

    expect(showError).toHaveBeenCalled();
    expect(await screen.findByTitle("Remove acme/shared")).toBeInTheDocument();
  });

  // The checkout and the prompt both happen once, when the run starts,
  // so an edit made mid-run reaches the git proxy but not the sandbox
  // already up -- said where the edit is made rather than left to be
  // discovered by a run that never clones the repo.
  it("warns that a repo added mid-run does not reach the sandbox already up", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "running", reads: ["acme/shared"] }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    expect(screen.getByText(/not cloned into it/)).toBeInTheDocument();
  });

  it("leaves that warning off a task that is not running", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, reads: ["acme/shared"] }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    expect(screen.queryByText(/not cloned into it/)).not.toBeInTheDocument();
  });
});
