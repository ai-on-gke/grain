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

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import SuiteRunPane, { suiteRunPasses } from "./SuiteRunPane.jsx";

// What a run's own pane is for (grain/task-322): the tasks a suite run
// filed, the passes it filed them in, and -- the part no list anywhere
// else in the UI can answer -- the tasks those tasks went on to spawn.

const run = {
  id: 7,
  suiteId: "suite-1",
  suiteName: "Nightly sweep",
  repo: "acme/widgets",
  base: "release-1",
  mode: "until_clean",
  maxPasses: 5,
  status: "active",
  pass: 2,
  createdAt: "2026-01-01T00:00:00Z",
  tasks: [
    {
      taskId: "1",
      templateId: "template-smoke",
      templateName: "Smoke",
      pass: 1,
      state: "completed",
    },
    {
      taskId: "2",
      templateId: "template-lint",
      templateName: "Lint",
      pass: 2,
      state: "running",
    },
  ],
};

// Task 1 proposed task 5, which proposed task 6; task 4 is somebody
// else's work entirely and must not appear.
const task = (id, extra = {}) => ({
  id,
  title: `Task ${id}`,
  state: "completed",
  capabilities: [],
  ...extra,
});
const tasks = [
  task("1", { title: "Sweep the smoke tests", suiteRun: true }),
  task("2", { title: "Run the linter", state: "running", suiteRun: true }),
  task("4", { title: "Somebody else's task" }),
  task("5", { title: "Fix the flake it found", generatedFrom: "1" }),
  task("6", { title: "Fix the fix", generatedFrom: "5" }),
];

const renderPane = (props = {}) =>
  render(
    <SuiteRunPane
      run={run}
      tasks={tasks}
      onClose={() => {}}
      onOpenTask={() => {}}
      {...props}
    />,
  );

const rowTitles = () =>
  [...document.querySelectorAll(".task-title")].map((e) => e.textContent);

describe("suiteRunPasses", () => {
  it("groups the run's tasks by pass, oldest pass first", () => {
    expect(
      suiteRunPasses(run, tasks).map((p) => [
        p.pass,
        p.roots.map((r) => r.task.id),
      ]),
    ).toEqual([
      [1, ["1"]],
      [2, ["2"]],
    ]);
  });

  it("hangs every descendant off the run task it came from", () => {
    const [pass1] = suiteRunPasses(run, tasks);
    expect(pass1.roots[0].children.map((c) => c.id)).toEqual(["5", "6"]);
  });

  it("falls back to the run's own snapshot for a task that is gone", () => {
    const [pass1] = suiteRunPasses(run, []);
    expect(pass1.roots[0].task).toMatchObject({ id: "1", title: "Smoke" });
  });

  it("lists a task once even if the links between them form a cycle", () => {
    const cyclic = [
      task("1", { generatedFrom: "5" }),
      task("5", { generatedFrom: "1" }),
    ];
    const [pass1] = suiteRunPasses(run, cyclic);
    expect(pass1.roots[0].children.map((c) => c.id)).toEqual(["5"]);
  });
});

describe("SuiteRunPane", () => {
  // grain/task-2415: the pane names the schedule that started the run by
  // linking to it, and a run nobody scheduled carries no such mark.
  it("links a scheduled run to its schedule", async () => {
    const user = userEvent.setup();
    const onOpenSchedule = vi.fn();
    const { unmount } = renderPane({
      run: { ...run, scheduleId: "sched-4" },
      onOpenSchedule,
    });
    const link = screen.getByRole("link", { name: /Scheduled/ });
    expect(link).toHaveAttribute("href", "/processes/schedules/sched-4");
    await user.click(link);
    expect(onOpenSchedule).toHaveBeenCalledWith("sched-4");
    unmount();

    renderPane();
    expect(screen.queryByText("Scheduled")).not.toBeInTheDocument();
  });

  it("heads the pane with the suite's name and how the run is going", () => {
    renderPane();

    expect(screen.getByRole("heading", { name: "Nightly sweep" })).toBeTruthy();
    expect(screen.getByText("Active")).toBeTruthy();
    expect(screen.getByText("acme/widgets")).toBeTruthy();
    expect(screen.getByText("release-1")).toBeTruthy();
  });

  it("says when each pass's tasks wait for approval, and only then", () => {
    const { unmount } = renderPane({ run: { ...run, requireApproval: true } });
    expect(
      screen.getByText(
        "run until clean (max 5) · each pass's tasks wait for approval",
      ),
    ).toBeTruthy();
    unmount();

    renderPane();
    expect(screen.getByText("run until clean (max 5)")).toBeTruthy();
    expect(screen.queryByText(/wait for approval/)).toBeNull();
  });

  it("lists every task of the run and everything they spawned", () => {
    renderPane();

    expect(rowTitles()).toEqual([
      "Sweep the smoke tests",
      "Fix the flake it found",
      "Fix the fix",
      "Run the linter",
    ]);
    expect(screen.getByText("Pass 1")).toBeTruthy();
    expect(screen.getByText("Pass 2")).toBeTruthy();
    expect(screen.queryByText("Somebody else's task")).toBeNull();
  });

  it("says each row's state to assistive technology, not just in colour", () => {
    renderPane();

    const rows = [...document.querySelectorAll(".task-row")];
    const named = (title) =>
      rows.find((r) => r.querySelector(".task-title")?.textContent === title);
    expect(
      within(named("Sweep the smoke tests")).getByRole("img", {
        name: "Merging",
      }),
    ).toBeTruthy();
    expect(
      within(named("Run the linter")).getAllByRole("img", { name: "Running" })
        .length,
    ).toBeGreaterThan(0);
  });

  it("nests the spawned tasks under the run task they came from", () => {
    renderPane();

    const sublists = document.querySelectorAll(".task-sublist");
    expect(sublists).toHaveLength(1);
    expect(
      [...sublists[0].querySelectorAll(".task-title")].map(
        (e) => e.textContent,
      ),
    ).toEqual(["Fix the flake it found", "Fix the fix"]);
  });

  it("counts the spawned tasks in the total, not just the run's own", () => {
    renderPane();

    const heading = screen.getByRole("heading", { name: "Tasks" });
    expect(within(heading.parentElement).getByText("4")).toBeTruthy();
  });

  it("opens a task when its row is clicked", async () => {
    const user = userEvent.setup();
    const onOpenTask = vi.fn();
    renderPane({ onOpenTask });

    await user.click(screen.getByText("Fix the flake it found"));

    expect(onOpenTask).toHaveBeenCalledWith("5");
  });

  it("opens a task on Enter on its row, the way a click does", async () => {
    const user = userEvent.setup();
    const onOpenTask = vi.fn();
    renderPane({ onOpenTask });

    // The pane's own rows are in tab order (grain/task-1424): tabbing
    // from the top of the pane reaches the first task's title, the link
    // a row opens by from the keyboard (grain/task-1582).
    const first = screen.getByRole("link", { name: "Sweep the smoke tests" });
    while (document.activeElement !== first) {
      await user.tab();
      if (document.activeElement === document.body) break;
    }
    expect(first).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(onOpenTask).toHaveBeenCalledWith("1");

    onOpenTask.mockClear();
    screen.getByRole("link", { name: "Fix the flake it found" }).focus();
    await user.keyboard("{Enter}");
    expect(onOpenTask).toHaveBeenCalledWith("5");
  });

  it("says so when the run has not filed anything yet", () => {
    renderPane({ run: { ...run, tasks: [] } });

    expect(screen.getByText(/filed no tasks yet/)).toBeTruthy();
  });
});
