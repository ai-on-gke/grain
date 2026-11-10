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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import api from "../../api.js";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";
import { repoGrantBox } from "../../repoGrantsTesting.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

// The task pane's repository grants (unified-grants slice 5): the repos
// it reads and writes are lines of the Access section's Grants list,
// each edit still saved as the task's whole reads or writes set.
describe("DetailOverlay repository grants", () => {
  afterEach(resetDetailMocks);

  // grain/task-294: the task's read-only repos are lines of the list,
  // not a static "Reads" row -- so the same lines that say which repos
  // they are are also how one is taken off.
  it("lists the task's own reads as repo#read grants", () => {
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

    expect(
      screen.getByRole("group", { name: "acme/shared · read" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("group", { name: "acme/schema · read" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Remove acme/shared · read" }),
    ).toBeInTheDocument();
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

    await user.click(await repoGrantBox(user, "read"));
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

    await user.click(await repoGrantBox(user, "read"));

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

    await user.click(
      screen.getByRole("button", { name: "Remove acme/shared · read" }),
    );

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

    await user.type(await repoGrantBox(user, "read"), "acme/shared{Enter}");
    await user.type(await repoGrantBox(user, "read"), "acme/schema{Enter}");

    expect(api).toHaveBeenLastCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({ reads: ["acme/shared", "acme/schema"] }),
    });
  });

  // The lines are already showing the new set by the time the server
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

    await user.click(
      screen.getByRole("button", { name: "Remove acme/shared · read" }),
    );

    expect(showError).toHaveBeenCalled();
    expect(
      await screen.findByRole("button", { name: "Remove acme/shared · read" }),
    ).toBeInTheDocument();
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

  // The task's own repo and base are a #target line that cannot be
  // removed (Q2): they are changed in the Repos section above.
  it("shows the task's own repo and base as a non-removable target line", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets", base: "main" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    const line = screen.getByRole("group", {
      name: "acme/widgets · main · target",
    });
    expect(line).toHaveAttribute(
      "data-grant",
      "https://grain.dev/capabilities/repo#target",
    );
    expect(
      screen.queryByRole("button", { name: /Remove acme\/widgets/ }),
    ).not.toBeInTheDocument();
  });

  // A declared template's push: refs are what its tasks' runs may push
  // beyond their own branch; the declaration grants them, so they are
  // shown and not editable here.
  it("shows a declared template's push refs read-only", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          repo: "acme/widgets",
          declaredPush: {
            repo: "acme/widgets",
            path: ".grain/templates/release.yml",
            refs: [
              { ref: "refs/tags/v*" },
              { ref: "refs/heads/latest", force: true },
            ],
          },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    expect(
      screen.getByRole("group", {
        name: "acme/widgets refs/tags/v* · push (declared by the template)",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("group", {
        name: "acme/widgets refs/heads/latest (force) · push (declared by the template)",
      }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Remove acme\/widgets refs/ }),
    ).not.toBeInTheDocument();
  });
});
