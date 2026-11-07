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

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import { attributeById, attributesFor } from "../taskAttributes.js";
import {
  matchesFilters,
  optionsFor,
  resolveConditions,
} from "../taskFilters.js";
import Declared from "./detail/Declared.jsx";
import EditTaskForm from "./detail/EditTaskForm.jsx";
import Timeline from "./detail/Timeline.jsx";
import { TaskRow } from "./TaskList.jsx";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue({}) }));

describe("Task owner UI and timeline proposer attribution", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("displays owner on Declared and transfers ownership inline", async () => {
    const user = userEvent.setup();
    const act = vi.fn(async (fn) => {
      await fn();
      return true;
    });
    render(
      <Declared
        t={{ id: "17", title: "Fix cache", owner: "alice@example.com" }}
        config={{}}
        act={act}
      />,
    );
    expect(screen.getByTestId("task-owner")).toHaveTextContent(
      "alice@example.com",
    );

    await user.click(screen.getByRole("button", { name: "Change owner" }));
    const input = screen.getByRole("textbox", { name: "New owner" });
    await user.clear(input);
    await user.type(input, "bob@example.com");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(api).toHaveBeenCalledWith(
      "/api/tasks/17",
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({ owner: "bob@example.com" }),
      }),
    );
  });

  it("edits owner in EditTaskForm", async () => {
    const user = userEvent.setup();
    const act = vi.fn(async (fn) => {
      await fn();
      return true;
    });
    const onDone = vi.fn();
    render(
      <EditTaskForm
        t={{
          id: "19",
          title: "Refactor auth",
          description: "Body",
          owner: "alice@example.com",
        }}
        config={{}}
        templates={[]}
        act={act}
        onDone={onDone}
      />,
    );

    const ownerInput = screen.getByLabelText("Owner");
    expect(ownerInput).toHaveValue("alice@example.com");
    await user.clear(ownerInput);
    await user.type(ownerInput, "carol@example.com");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(api).toHaveBeenCalledWith(
      "/api/tasks/19",
      expect.objectContaining({
        method: "PATCH",
      }),
    );
    const sent = JSON.parse(api.mock.calls[0][1].body);
    expect(sent.owner).toBe("carol@example.com");
    expect(onDone).toHaveBeenCalled();
  });

  it("displays owner chip on TaskRow and filters by owner including me shorthand", () => {
    render(
      <TaskRow
        t={{
          id: "21",
          title: "Add metrics",
          state: "queued",
          repo: "acme/widgets",
          owner: "alice@example.com",
          capabilities: [],
        }}
        byId={new Map()}
        config={{}}
        onOpenTask={vi.fn()}
      />,
    );
    expect(screen.getByTestId("task-owner-chip")).toHaveTextContent(
      "alice@example.com",
    );

    const cfg = { viewerOwner: "alice@example.com" };
    const attrs = attributesFor(cfg);
    const ownerAttr = attributeById(attrs, "owner");
    const tasks = [
      { id: "1", title: "Mine", owner: "alice@example.com" },
      { id: "2", title: "Theirs", owner: "bob@example.com" },
    ];

    const opts = optionsFor(ownerAttr, tasks, cfg);
    expect(opts[0].value).toBe("me");
    expect(opts[0].label).toBe("me (alice@example.com)");

    const meConds = resolveConditions(
      [{ attr: "owner", op: "any", values: ["me"] }],
      attrs,
    );
    expect(matchesFilters(tasks[0], meConds, "", cfg)).toBe(true);
    expect(matchesFilters(tasks[1], meConds, "", cfg)).toBe(false);

    const userPrefixConds = resolveConditions(
      [{ attr: "owner", op: "any", values: ["user:Bob@Example.com"] }],
      attrs,
    );
    expect(matchesFilters(tasks[0], userPrefixConds, "", cfg)).toBe(false);
    expect(matchesFilters(tasks[1], userPrefixConds, "", cfg)).toBe(true);
  });

  it("renders Proposed attribution for human, run-proposed, automation, and owner_changed audit", async () => {
    const user = userEvent.setup();
    const onOpenTask = vi.fn();

    // 1. Run-proposed task with clickable proposing task + run links, on behalf of owner, plus owner_changed step.
    const { rerender } = render(
      <Timeline
        t={{
          id: "55",
          state: "queued",
          attempts: [],
          comments: [],
          transitions: [
            {
              state: "proposed",
              at: "2026-11-05T10:00:00Z",
              proposingTaskId: "42",
              proposingRunId: "run-abc",
              proposingRunAttempt: 2,
              onBehalfOfOwner: "alice@example.com",
              actor: { kind: "agent", id: "42", source: "sandbox" },
            },
            {
              state: "owner_changed",
              at: "2026-11-05T10:05:00Z",
              actorLabel: "alice@example.com",
              oldOwner: "alice@example.com",
              newOwner: "bob@example.com",
            },
            {
              state: "admission_changed",
              at: "2026-11-05T10:06:00Z",
              actorLabel: "alice@example.com",
              oldAdmission: "standard",
              newAdmission: "bypass",
              reason: "standard -> bypass",
            },
          ],
        }}
        act={vi.fn()}
        showError={vi.fn()}
        onOpenTask={onOpenTask}
      />,
    );

    const taskLink = screen.getByTestId("proposing-task-link");
    expect(taskLink).toHaveTextContent("#42");
    expect(taskLink).toHaveAttribute("href", "/tasks/42");

    const runLink = screen.getByTestId("proposing-run-link");
    expect(runLink).toHaveTextContent("run #2");
    expect(runLink).toHaveAttribute("href", "/tasks/42");
    expect(
      screen.getByText(/on behalf of alice@example\.com/),
    ).toBeInTheDocument();

    expect(screen.getByText("Owner changed")).toBeInTheDocument();
    expect(
      screen.getByText(/from alice@example\.com to bob@example\.com/),
    ).toBeInTheDocument();
    expect(screen.getByText("Admission changed")).toBeInTheDocument();
    expect(
      screen.getByText(/from Standard to Bypass queue/),
    ).toBeInTheDocument();

    await user.click(taskLink);
    expect(onOpenTask).toHaveBeenCalledWith("42");

    // 2. Automation-filed task ("schedule") and human-filed task.
    rerender(
      <Timeline
        t={{
          id: "56",
          state: "proposed",
          attempts: [],
          comments: [],
          transitions: [
            {
              state: "proposed",
              at: "2026-11-05T11:00:00Z",
              actorLabel: "schedule",
              by: "schedule",
            },
          ],
        }}
        act={vi.fn()}
        showError={vi.fn()}
        onOpenTask={onOpenTask}
      />,
    );
    expect(screen.getByText("by schedule")).toBeInTheDocument();
  });
});
