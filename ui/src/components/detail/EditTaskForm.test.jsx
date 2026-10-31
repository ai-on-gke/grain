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

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay edit form", () => {
  afterEach(resetDetailMocks);

  // bwsalmon/agents#523: "After creating a task the user should be able
  // to edit it."
  it("edits the title and description via the PATCH endpoint", async () => {
    const act = vi.fn().mockResolvedValue(true);
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

    await user.click(screen.getByRole("button", { name: "Edit" }));

    // Pasted rather than typed: the strings are worth keeping as they
    // are -- the new title has to differ from the old one visibly for
    // the heading assertion below to mean anything -- and a paste is one
    // render of the overlay where typing is one per character. That is
    // what this test was spending its time on; see
    // NewTaskOverlay.test.jsx's header for what a render costs here.
    const title = screen.getByLabelText("Title");
    await user.clear(title);
    await user.paste("Fix the login bug for real");
    const description = screen.getByLabelText("Description");
    await user.clear(description);
    await user.paste("New repro steps");

    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(act).toHaveBeenCalledWith(expect.any(Function), "12");
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({
        title: "Fix the login bug for real",
        description: "New repro steps",
        reviewTemplateId: "",
      }),
    });
    // Back to the read-only view once saved.
    expect(screen.queryByLabelText("Title")).not.toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: /Fix the login bug/ }),
    ).toHaveTextContent("12 Fix the login bug");
  });

  // grain/task-1699: a save the server refused used to close the editor
  // anyway, and the edit went with it.
  it("stays open with the edit when the save does not land", async () => {
    // What App's act() resolves to once it has reported a failure.
    const act = vi.fn().mockResolvedValue(false);
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

    await user.click(screen.getByRole("button", { name: "Edit" }));
    const title = screen.getByLabelText("Title");
    await user.clear(title);
    await user.paste("Edited while the server errors");
    const description = screen.getByLabelText("Description");
    await user.clear(description);
    await user.paste("A long description the user typed carefully.");

    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(act).toHaveBeenCalledTimes(1);
    expect(screen.getByLabelText("Title")).toHaveValue(
      "Edited while the server errors",
    );
    expect(screen.getByLabelText("Description")).toHaveValue(
      "A long description the user typed carefully.",
    );
  });

  // grain/task-284: attaching a review to a task that already exists is
  // the flow that matters most -- somebody decides, part-way through,
  // that this change wants a second agent over it before it merges.
  it("attaches a review to an existing task from the edit form", async () => {
    const act = vi.fn().mockResolvedValue(true);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        templates={[{ id: "7", name: "Security pass" }]}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByLabelText("Review"));
    await user.click(screen.getByRole("option", { name: "Security pass" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    act.mock.calls[0][0]();
    expect(JSON.parse(api.mock.calls[0][1].body).reviewTemplateId).toBe("7");
  });

  // grain/task-1549: the task page edits what a nothing_needed ending
  // does to this task, and sends the field only when it changed.
  it("leaves a nothing-needed ending for acknowledgement from the edit form", async () => {
    const act = vi.fn().mockResolvedValue(true);
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

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(
      screen.getByLabelText("When the agent finds nothing to do"),
    );
    await user.click(
      screen.getByRole("option", { name: "Leave for acknowledgement" }),
    );
    await user.click(screen.getByRole("button", { name: "Save" }));

    act.mock.calls[0][0]();
    expect(JSON.parse(api.mock.calls[0][1].body).nothingNeededAction).toBe(
      "ack",
    );
  });

  it("toggles keepRunning from the edit form", async () => {
    const act = vi.fn().mockResolvedValue(true);
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

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(
      screen.getByRole("checkbox", {
        name: /Keep running \(immediately restart when a run exits\)/,
      }),
    );
    await user.click(screen.getByRole("button", { name: "Save" }));

    act.mock.calls[0][0]();
    expect(JSON.parse(api.mock.calls[0][1].body).keepRunning).toBe(true);
  });

  // grain/task-110: the same form moves a task that is already filed
  // onto one particular model -- the case this is for, since "this one
  // needs the expensive model" is usually learned after a run. The model
  // and the connection are two pickers, as they are on New task.
  const pooled = {
    ...config,
    deployment: { id: "default", defaultModel: "gpt-5.1" },
    agentBackends: [
      {
        id: "codex-day",
        name: "Day account",
        framework: "codex",
        models: ["gpt-5.1"],
      },
      {
        id: "claude-day",
        name: "Claude day",
        framework: "claude",
        models: ["claude-opus-5"],
      },
      {
        id: "claude-deep",
        name: "Deep account",
        framework: "claude",
        models: ["claude-opus-5"],
        disabled: true,
      },
    ],
  };
  const openEdit = async (user, task = baseTask, act = vi.fn()) => {
    render(
      <DetailOverlay
        task={task}
        tasks={[]}
        config={pooled}
        templates={[]}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Edit" }));
  };
  const optionNames = () =>
    screen.getAllByRole("option").map((o) => o.textContent);

  it("offers the deployment's models and sends the chosen one as model", async () => {
    const act = vi.fn().mockResolvedValue(true);
    const user = userEvent.setup();
    await openEdit(user, baseTask, act);

    await user.click(screen.getByLabelText("Model"));
    expect(optionNames()).toEqual([
      "Deployment default (gpt-5.1)",
      "gpt-5.1",
      "claude-opus-5",
    ]);
    await user.click(screen.getByRole("option", { name: "claude-opus-5" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    act.mock.calls[0][0]();
    const body = JSON.parse(api.mock.calls[0][1].body);
    expect(body.model).toBe("claude-opus-5");
    expect(body.agentBackendId).toBe("");
  });

  it("pins a connection that serves the model, disabled ones labelled", async () => {
    const act = vi.fn().mockResolvedValue(true);
    const user = userEvent.setup();
    await openEdit(user, { ...baseTask, model: "claude-opus-5" }, act);

    await user.click(screen.getByLabelText("Specific connection"));
    expect(optionNames()).toEqual([
      "Any enabled connection serving the model",
      "Claude day · Claude",
      "Deep account · Claude · disabled, pin only",
    ]);
    await user.click(
      screen.getByRole("option", {
        name: "Deep account · Claude · disabled, pin only",
      }),
    );
    await user.click(screen.getByRole("button", { name: "Save" }));

    act.mock.calls[0][0]();
    const body = JSON.parse(api.mock.calls[0][1].body);
    expect(body.model).toBe("claude-opus-5");
    expect(body.agentBackendId).toBe("claude-deep");
  });

  it("drops a pin the newly chosen model's connections do not include", async () => {
    const act = vi.fn().mockResolvedValue(true);
    const user = userEvent.setup();
    await openEdit(
      user,
      { ...baseTask, model: "claude-opus-5", agentBackendId: "claude-deep" },
      act,
    );

    await user.click(screen.getByLabelText("Model"));
    await user.click(screen.getByRole("option", { name: "gpt-5.1" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    act.mock.calls[0][0]();
    const body = JSON.parse(api.mock.calls[0][1].body);
    expect(body.model).toBe("gpt-5.1");
    expect(body.agentBackendId).toBe("");
  });

  // A pin made before models were separated may not serve the team's
  // default; an edit about something else must not take it off.
  it("keeps a saved pin that does not serve the model", async () => {
    const act = vi.fn().mockResolvedValue(true);
    const user = userEvent.setup();
    await openEdit(user, { ...baseTask, agentBackendId: "claude-day" }, act);

    expect(
      screen.getByText("Claude day · Claude (does not serve gpt-5.1)"),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save" }));

    act.mock.calls[0][0]();
    const body = JSON.parse(api.mock.calls[0][1].body);
    expect(body.model).toBe("");
    expect(body.agentBackendId).toBe("claude-day");
  });

  // The picker draws "No review" inside the box while nothing is
  // attached, and MUI floats a label clear of the box only for a field
  // it counts as filled -- which an unpicked select is not. Without the
  // shrink the label lands on top of that line and the field reads as
  // garbled text; data-shrink is the label's own account of where it is.
  it("keeps the review label clear of the No review line", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        templates={[{ id: "7", name: "Security pass" }]}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Edit" }));
    const label = screen
      .getByLabelText("Review")
      .closest(".MuiFormControl-root")
      .querySelector("label");
    expect(label).toHaveAttribute("data-shrink", "true");
  });

  // Once the review task exists there is nothing left to choose: exactly
  // one review is filed per task, so the picker says which task is
  // carrying it out rather than offering an edit that would do nothing.
  it("locks the review picker once the review task has been filed", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          reviewTemplateId: "7",
          reviewTemplateName: "Security pass",
          reviewTask: "91",
        }}
        tasks={[]}
        config={config}
        templates={[{ id: "7", name: "Security pass" }]}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(
      screen.getByText(/Task 91 is already reviewing this one/),
    ).toBeInTheDocument();
  });

  it("leaves the task unchanged when editing is cancelled", async () => {
    const act = vi.fn();
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

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.type(screen.getByLabelText("Title"), " (draft)");
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(act).not.toHaveBeenCalled();
    expect(
      screen.getByRole("heading", { name: /Fix the login bug/ }),
    ).toHaveTextContent("12 Fix the login bug");
  });

  it("disables Save once the title is cleared", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.clear(screen.getByLabelText("Title"));

    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  describe("unsaved edits guard", () => {
    it("asks before Escape discards an edited task, and stays when told no", async () => {
      const onClose = vi.fn();
      const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
      const user = userEvent.setup();
      render(
        <DetailOverlay
          task={baseTask}
          tasks={[]}
          config={{}}
          templates={[]}
          onClose={onClose}
          onOpenTask={() => {}}
          act={vi.fn()}
        />,
      );

      await user.click(screen.getByRole("button", { name: "Edit" }));
      const titleInput = screen.getByLabelText("Title");
      await user.type(titleInput, " modified");

      await user.keyboard("{Escape}");

      expect(confirm).toHaveBeenCalledWith(
        "You have unsaved changes here. Leave and discard them?",
      );
      expect(onClose).not.toHaveBeenCalled();
      expect(screen.getByLabelText("Title")).toBeInTheDocument();
      confirm.mockRestore();
    });

    it("closes when discarding an edited task is confirmed", async () => {
      const onClose = vi.fn();
      const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
      const user = userEvent.setup();
      render(
        <DetailOverlay
          task={baseTask}
          tasks={[]}
          config={{}}
          templates={[]}
          onClose={onClose}
          onOpenTask={() => {}}
          act={vi.fn()}
        />,
      );

      await user.click(screen.getByRole("button", { name: "Edit" }));
      const titleInput = screen.getByLabelText("Title");
      await user.type(titleInput, " modified");

      await user.keyboard("{Escape}");

      expect(confirm).toHaveBeenCalledWith(
        "You have unsaved changes here. Leave and discard them?",
      );
      expect(onClose).toHaveBeenCalledTimes(1);
      confirm.mockRestore();
    });

    it("does not ask on Escape when task editor is open but untouched", async () => {
      const onClose = vi.fn();
      const confirm = vi.spyOn(window, "confirm");
      const user = userEvent.setup();
      render(
        <DetailOverlay
          task={baseTask}
          tasks={[]}
          config={{}}
          templates={[]}
          onClose={onClose}
          onOpenTask={() => {}}
          act={vi.fn()}
        />,
      );

      await user.click(screen.getByRole("button", { name: "Edit" }));
      await user.keyboard("{Escape}");

      expect(confirm).not.toHaveBeenCalled();
      expect(onClose).toHaveBeenCalledTimes(1);
      confirm.mockRestore();
    });
  });

  it("shows the priority badge and edits Task priority via PATCH", async () => {
    const act = vi.fn().mockResolvedValue(true);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, priority: 7 }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    expect(screen.getByTestId("detail-priority-badge")).toHaveTextContent(
      "priority: 7",
    );

    await user.click(screen.getByRole("button", { name: "Edit" }));
    const priorityInput = screen.getByLabelText("Task priority");
    expect(priorityInput).toHaveValue(7);

    await user.clear(priorityInput);
    await user.type(priorityInput, "9");
    await user.click(screen.getByRole("button", { name: "Save" }));

    act.mock.calls[0][0]();
    expect(JSON.parse(api.mock.calls[0][1].body).priority).toBe(9);
  });
});
