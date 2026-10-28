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
import { describe, expect, it, vi } from "vitest";
import TaskPicker from "./TaskPicker.jsx";

const tasks = [
  { id: "12", title: "Fix the login bug" },
  { id: "15", title: "Add dark mode" },
  { id: "20", title: "Login page redesign" },
];

describe("TaskPicker", () => {
  it("filters by id or title as the user types, case-insensitively", async () => {
    const user = userEvent.setup();
    render(<TaskPicker tasks={tasks} onPick={() => {}} />);

    await user.type(screen.getByRole("textbox"), "login");

    expect(screen.getByText("Fix the login bug")).toBeInTheDocument();
    expect(screen.getByText("Login page redesign")).toBeInTheDocument();
    expect(screen.queryByText("Add dark mode")).not.toBeInTheDocument();
  });

  it("shows nothing until text is entered", async () => {
    render(<TaskPicker tasks={tasks} onPick={() => {}} />);
    expect(screen.queryByText("Fix the login bug")).not.toBeInTheDocument();
  });

  it("excludes ids in the exclude list", async () => {
    const user = userEvent.setup();
    render(<TaskPicker tasks={tasks} exclude={["12"]} onPick={() => {}} />);

    await user.type(screen.getByRole("textbox"), "login");

    expect(screen.queryByText("Fix the login bug")).not.toBeInTheDocument();
    expect(screen.getByText("Login page redesign")).toBeInTheDocument();
  });

  it("shows a no-matches message when nothing matches", async () => {
    const user = userEvent.setup();
    render(<TaskPicker tasks={tasks} onPick={() => {}} />);

    await user.type(screen.getByRole("textbox"), "xyz");

    expect(screen.getByText("No matching tasks")).toBeInTheDocument();
  });

  it("calls onPick and clears the query when a result is clicked", async () => {
    const onPick = vi.fn();
    const user = userEvent.setup();
    render(<TaskPicker tasks={tasks} onPick={onPick} />);

    const input = screen.getByRole("textbox");
    await user.type(input, "dark");
    await user.click(screen.getByText("Add dark mode"));

    expect(onPick).toHaveBeenCalledWith(tasks[1]);
    expect(input).toHaveValue("");
    expect(screen.queryByText("Add dark mode")).not.toBeInTheDocument();
  });

  it("navigates matches with the arrow keys and picks the highlighted one on Enter", async () => {
    const onPick = vi.fn();
    const user = userEvent.setup();
    render(<TaskPicker tasks={tasks} onPick={onPick} />);

    const input = screen.getByRole("textbox");
    await user.type(input, "login");
    await user.keyboard("{ArrowDown}");
    await user.keyboard("{Enter}");

    expect(onPick).toHaveBeenCalledWith(tasks[2]);
  });

  // The task list this is handed is backlog order (next to run first),
  // so a picker that took matches in that order would offer the oldest
  // work rather than the work somebody has just been looking at.
  it("orders matches newest first, whatever order the tasks arrive in", async () => {
    const user = userEvent.setup();
    const dated = [
      { id: "12", title: "Login one", createdAt: "2026-01-01T00:00:00Z" },
      { id: "20", title: "Login three", createdAt: "2026-03-01T00:00:00Z" },
      { id: "15", title: "Login two", createdAt: "2026-02-01T00:00:00Z" },
    ];
    render(<TaskPicker tasks={dated} onPick={() => {}} />);

    await user.type(screen.getByRole("textbox"), "login");

    const shown = screen.getAllByRole("menuitem").map((el) => el.textContent);
    expect(shown.map((t) => t.replace(/\D/g, ""))).toEqual(["20", "15", "12"]);
  });

  // A newer task whose title carries the digits used to take Enter from
  // the task whose id was typed, attaching the wrong dependency silently.
  const numbered = [
    {
      id: "14",
      title: "Retry the webhook delivery on a 502",
      createdAt: "2026-01-01T00:00:00Z",
    },
    { id: "140", title: "Tidy the inbox", createdAt: "2026-02-01T00:00:00Z" },
    {
      id: "21",
      title: "Port acme/widgets#140 to candidate",
      createdAt: "2026-03-01T00:00:00Z",
    },
  ];

  it("picks the task whose id was typed on Enter, ahead of newer title matches", async () => {
    const onPick = vi.fn();
    const user = userEvent.setup();
    render(<TaskPicker tasks={numbered} onPick={onPick} />);

    await user.type(screen.getByRole("textbox"), "14");
    const shown = screen.getAllByRole("menuitem").map((el) => el.textContent);
    expect(shown[0]).toContain("Retry the webhook delivery");
    expect(shown[1]).toContain("Tidy the inbox");
    expect(shown[2]).toContain("Port acme/widgets#140");

    await user.keyboard("{Enter}");
    expect(onPick).toHaveBeenCalledWith(numbered[0]);
  });

  it.each(["#14", "task 14", "task-14"])(
    "reads %s as the id 14",
    async (typed) => {
      const onPick = vi.fn();
      const user = userEvent.setup();
      render(<TaskPicker tasks={numbered} onPick={onPick} />);

      await user.type(screen.getByRole("textbox"), typed);
      await user.keyboard("{Enter}");
      expect(onPick).toHaveBeenCalledWith(numbered[0]);
    },
  );

  it("closes the results on Escape", async () => {
    const user = userEvent.setup();
    render(<TaskPicker tasks={tasks} onPick={() => {}} />);

    await user.type(screen.getByRole("textbox"), "login");
    expect(screen.getByText("Fix the login bug")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(screen.queryByText("Fix the login bug")).not.toBeInTheDocument();
  });

  // The New task drawer closes on an Escape that reaches it, so the one
  // that closes the results must stop here; the next one is the drawer's.
  it("keeps the Escape that closes the results from the drawer around it", async () => {
    const user = userEvent.setup();
    const onDrawerKey = vi.fn();
    render(
      <div onKeyDown={(e) => e.key === "Escape" && onDrawerKey()}>
        <TaskPicker tasks={tasks} onPick={() => {}} />
      </div>,
    );

    await user.type(screen.getByRole("textbox"), "login");
    await user.keyboard("{Escape}");
    expect(onDrawerKey).not.toHaveBeenCalled();

    await user.keyboard("{Escape}");
    expect(onDrawerKey).toHaveBeenCalledTimes(1);
  });

  it("closes the results when clicking outside the picker", async () => {
    const user = userEvent.setup();
    render(
      <div>
        <TaskPicker tasks={tasks} onPick={() => {}} />
        <button>outside</button>
      </div>,
    );

    await user.type(screen.getByRole("textbox"), "login");
    expect(screen.getByText("Fix the login bug")).toBeInTheDocument();

    await user.click(screen.getByText("outside"));
    expect(screen.queryByText("Fix the login bug")).not.toBeInTheDocument();
  });
});
