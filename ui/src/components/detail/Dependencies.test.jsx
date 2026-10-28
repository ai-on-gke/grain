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

// A chip's label is the id, the title and the state in spans of their
// own (so the title alone can be cut short), which getByText's default
// matcher, reading one element's own text nodes, would not find whole.
const chipLabel = (text) =>
  screen.getByText(
    (_, el) =>
      el.classList.contains("MuiChip-label") && el.textContent === text,
  );

describe("DetailOverlay dependencies", () => {
  afterEach(resetDetailMocks);

  it("shows a hint when there are no dependencies", () => {
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
    expect(screen.getByText("No dependencies.")).toBeInTheDocument();
  });

  it("renders dependency chips, marking blocking ones, and removes a dependency via the depends-on endpoint", async () => {
    const act = vi.fn();
    const onOpenTask = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, dependsOn: ["9", "10"], blockedBy: ["9"] }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={onOpenTask}
        act={act}
      />,
    );

    expect(chipLabel("9 (open)")).toBeInTheDocument();
    expect(chipLabel("10")).toBeInTheDocument();

    await user.click(screen.getByTitle("Remove dependency on 9"));
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/depends-on", {
      method: "POST",
      body: JSON.stringify({ id: "9", attach: false }),
    });

    await user.click(chipLabel("10"));
    expect(onOpenTask).toHaveBeenCalledWith("10");
  });

  it("labels a dependency chip with the depended-on task's title, and the whole task on hover", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, dependsOn: ["9"], blockedBy: ["9"] }}
        tasks={[{ id: "9", title: "Add dark mode", state: "running" }]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const chip = chipLabel("9 Add dark mode (open)");
    await user.hover(chip);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "9 Add dark mode — Running (blocking this task)",
    );
  });

  it("falls back to the id for a dependency the task list does not carry", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, dependsOn: ["9"] }}
        tasks={[{ id: "20", title: "Add dark mode", state: "queued" }]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    await user.hover(chipLabel("9"));
    expect(await screen.findByRole("tooltip")).toHaveTextContent("9");
  });

  it("keeps the remove control of a long-titled dependency, and cuts the title instead", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    const title =
      "Substrate bug hunt (CTL): control plane — ateapi, atecontroller, api/, pkg/client [report only] " +
      "https://github.com/agent-substrate/substrate/blob/main/pkg/atecontroller/reconcile/a_very_long_file_name.go";
    render(
      <DetailOverlay
        task={{ ...baseTask, dependsOn: ["9"], blockedBy: ["9"] }}
        tasks={[{ id: "9", title, state: "running" }]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    // The title is a part of its own, the one that ellipsizes; the id
    // and the state are not inside it, so cutting it never cuts them.
    const label = chipLabel(`9 ${title} (open)`);
    const titlePart = screen.getByText(title);
    expect(titlePart).toHaveClass("dep-title");
    expect(label).toContainElement(screen.getByText("9"));
    expect(label).toContainElement(screen.getByText("(open)"));
    expect(titlePart).not.toContainElement(screen.getByText("(open)"));

    await user.click(screen.getByTitle("Remove dependency on 9"));
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/depends-on", {
      method: "POST",
      body: JSON.stringify({ id: "9", attach: false }),
    });
  });

  it("adds a dependency picked from the task picker", async () => {
    const act = vi.fn();
    const tasks = [{ id: "20", title: "Add dark mode" }];
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.type(screen.getByPlaceholderText("Add a dependency…"), "20");
    await user.click(await screen.findByText("Add dark mode"));

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/depends-on", {
      method: "POST",
      body: JSON.stringify({ id: "20", attach: true }),
    });
  });
});
