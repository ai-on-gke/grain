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

import { useState } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ProcessesList from "./ProcessesList.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const schedules = [
  {
    id: "sched-1",
    title: "Nightly bump",
    repo: "acme/widgets",
    recurrence: { kind: "daily", timeOfDay: "02:00" },
    templateId: "template-1",
    templateName: "Bump",
    enabled: true,
    lastRunAt: "2026-10-18T02:00:00Z",
    nextRunAt: "2030-10-21T02:00:00Z",
  },
  {
    id: "sched-2",
    title: "Hourly bump",
    repo: "acme/widgets",
    recurrence: { kind: "everyNHours", everyNHours: 1 },
    templateId: "template-1",
    templateName: "Bump",
    enabled: true,
    lastRunAt: "2026-10-19T09:00:00Z",
    nextRunAt: "2030-10-20T10:00:00Z",
  },
  {
    id: "repo:schedule:acme:widgets:nightly",
    title: "nightly workflow",
    repo: "acme/widgets",
    recurrence: { kind: "daily", timeOfDay: "03:00" },
    workflowReview: "on-failure",
    enabled: true,
    nextRunAt: "2030-10-22T03:00:00Z",
    declaredIn: { repo: "acme/widgets", path: ".grain/workflows/nightly.yml" },
  },
  {
    id: "system:upgrade-check",
    title: "Upgrade check",
    repo: "grain/config",
    recurrence: { kind: "everyNHours", everyNHours: 6 },
    system: true,
    enabled: false,
    nextRunAt: "2030-10-22T00:00:00Z",
  },
];
const templates = [
  { id: "template-1", name: "Bump", title: "Bump deps", repo: "acme/widgets" },
];
const suites = [
  {
    id: "suite-1",
    name: "Qualify",
    mode: "once",
    items: [{ templateId: "template-1" }],
  },
];
const triggers = [{ repo: "acme/widgets", path: ".grain/triggers/docs.yml" }];

// Controlled the way App holds it: the query and which item is open.
function Harness({ initialQuery, onQueryChange, ...props }) {
  const [query, setQuery] = useState(initialQuery || {});
  const [openScheduleId, setOpenScheduleId] = useState(null);
  const [openTemplateId, setOpenTemplateId] = useState(null);
  const [openSuiteId, setOpenSuiteId] = useState(null);
  return (
    <ProcessesList
      schedules={schedules}
      templates={templates}
      suites={suites}
      triggers={triggers}
      query={query}
      onQueryChange={(q) => {
        onQueryChange?.(q);
        setQuery(q);
      }}
      openScheduleId={openScheduleId}
      onOpenSchedule={setOpenScheduleId}
      openTemplateId={openTemplateId}
      onOpenTemplate={setOpenTemplateId}
      openSuiteId={openSuiteId}
      onOpenSuite={setOpenSuiteId}
      onOpenRun={() => {}}
      showError={() => {}}
      {...props}
    />
  );
}

const rowsOf = (kind) =>
  document.querySelectorAll(`.process-row[data-kind="${kind}"]`);

describe("ProcessesList", () => {
  beforeEach(() => {
    localStorage.clear();
    api.mockImplementation(() => Promise.resolve({}));
  });
  afterEach(() => {
    api.mockReset();
  });

  it("lists every kind in one table, each row with its kind chip, and counts them", () => {
    render(<Harness />);
    expect(rowsOf("schedule")).toHaveLength(3);
    expect(rowsOf("workflow")).toHaveLength(1);
    expect(rowsOf("template")).toHaveLength(1);
    expect(rowsOf("suite")).toHaveLength(1);
    expect(rowsOf("trigger")).toHaveLength(1);
    expect(within(rowsOf("workflow")[0]).getByText("Workflow")).toBeTruthy();
    expect(within(rowsOf("trigger")[0]).getByText("docs.yml")).toBeTruthy();
    const filter = screen.getByTestId("process-kind-filter");
    expect(within(filter).getByText("All 7")).toBeTruthy();
    expect(within(filter).getByText("Schedules 3")).toBeTruthy();
    expect(within(filter).getByText("Workflow schedules 1")).toBeTruthy();
    expect(within(filter).getByText("Templates 1")).toBeTruthy();
    expect(within(filter).getByText("Suites 1")).toBeTruthy();
    expect(within(filter).getByText("Triggers 1")).toBeTruthy();
  });

  it("narrows to the kinds picked, and writes them to the query", async () => {
    const onQueryChange = vi.fn();
    const user = userEvent.setup();
    render(<Harness onQueryChange={onQueryChange} />);
    const filter = screen.getByTestId("process-kind-filter");

    for (const [label, kind] of [
      ["Schedules 3", "schedule"],
      ["Workflow schedules 1", "workflow"],
      ["Templates 1", "template"],
      ["Suites 1", "suite"],
      ["Triggers 1", "trigger"],
    ]) {
      await user.click(within(filter).getByText(label));
      expect(onQueryChange.mock.lastCall[0].filters).toEqual([
        { attr: "kind", op: "any", values: [kind] },
      ]);
      const shown = [...document.querySelectorAll(".process-row[data-kind]")];
      expect(shown.length).toBeGreaterThan(0);
      expect({ kind, shown: shown.map((r) => r.dataset.kind) }).toEqual({
        kind,
        shown: shown.map(() => kind),
      });
      // Picking it again goes back to every kind.
      await user.click(within(filter).getByText(label));
      expect(onQueryChange.mock.lastCall[0].filters).toEqual([]);
    }
  });

  it("opens with the kinds a ?kind= link named", () => {
    render(<Harness initialQuery={{ kinds: ["suite", "trigger"] }} />);
    expect(rowsOf("schedule")).toHaveLength(0);
    expect(rowsOf("suite")).toHaveLength(1);
    expect(rowsOf("trigger")).toHaveLength(1);
  });

  it("shows a template's last and next runs derived from the two schedules that fire it", () => {
    render(<Harness />);
    const row = rowsOf("template")[0];
    // The newer of the two last runs and the sooner of the two next ones.
    expect(row.querySelector(".process-last").textContent).toBe(
      new Date("2026-10-19T09:00:00Z").toLocaleString(),
    );
    expect(row.querySelector(".process-next").textContent).toBe(
      new Date("2030-10-20T10:00:00Z").toLocaleString(),
    );
    expect(
      rowsOf("trigger")[0].querySelector(".process-next").textContent,
    ).toBe("on change");
  });

  it("marks a system schedule with its lock, and a declared row with its chip", () => {
    render(<Harness />);
    const system = [...rowsOf("schedule")].find((r) =>
      r.textContent.includes("Upgrade check"),
    );
    expect(within(system).getByText("🔒 System")).toBeTruthy();
    expect(within(system).getByText("Paused")).toBeTruthy();
    // The declared row carries DeclaredChip, naming the repo, with the
    // file in its tooltip; the rows nothing declared carry none.
    const chips = document.querySelectorAll(".process-row .declared-chip");
    expect(chips).toHaveLength(1);
    expect(rowsOf("workflow")[0].contains(chips[0])).toBe(true);
    expect(chips[0]).toHaveTextContent("Declared by acme/widgets");
    expect(chips[0]).toHaveAttribute(
      "title",
      "Declared in acme/widgets, .grain/workflows/nightly.yml",
    );
  });

  it("asks for the kind on New, and offers only what has a form", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("button", { name: "+ New" }));
    const menu = await screen.findByTestId("new-process-kind");
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((m) => m.textContent),
    ).toEqual(["Schedule", "Template", "Suite"]);
    expect(within(menu).getByText(/declared in a/)).toBeTruthy();
    await user.click(within(menu).getByRole("menuitem", { name: /Template/ }));
    expect(
      await screen.findByRole("heading", { name: "New template" }),
    ).toBeTruthy();
  });

  it("skips the menu when the filter is narrowed to one creatable kind", async () => {
    const user = userEvent.setup();
    render(<Harness initialQuery={{ kinds: ["schedule"] }} />);
    await user.click(screen.getByRole("button", { name: "+ New schedule" }));
    expect(screen.queryByTestId("new-process-kind")).toBeNull();
    expect(
      await screen.findByRole("heading", { name: "New schedule" }),
    ).toBeTruthy();
  });

  it("keeps the menu for a kind that cannot be created here", async () => {
    const user = userEvent.setup();
    render(<Harness initialQuery={{ kinds: ["trigger"] }} />);
    await user.click(screen.getByRole("button", { name: "+ New" }));
    expect(await screen.findByTestId("new-process-kind")).toBeTruthy();
  });

  it("opens each kind's own overlay from its row", async () => {
    const onOpenTrigger = vi.fn();
    const user = userEvent.setup();
    render(<Harness onOpenTrigger={onOpenTrigger} />);
    await user.click(screen.getByText("Nightly bump"));
    expect(
      await screen.findByRole("heading", { name: "Edit schedule" }),
    ).toBeTruthy();
    expect(
      document.querySelector('a.row-link[href="/processes/schedules/sched-1"]'),
    ).toHaveTextContent("Nightly bump");
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    await user.click(screen.getByText("docs.yml"));
    expect(onOpenTrigger).toHaveBeenCalledWith("acme/widgets");
  });

  it("cancels an active row pickup when narrowing filters the picked row out", async () => {
    const user = userEvent.setup();
    render(<Harness initialQuery={{ sort: "custom" }} />);
    await user.click(screen.getByRole("button", { name: "Move Nightly bump" }));
    expect(screen.getByRole("status")).toHaveTextContent("Moving 1 process");
    await user.type(
      screen.getByPlaceholderText("Search processes…"),
      "Upgrade check",
    );
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("opens the suppressing task in-app without opening the schedule row", async () => {
    const user = userEvent.setup();
    const onOpenSchedule = vi.fn();
    const onOpenTask = vi.fn();
    render(
      <ProcessesList
        schedules={[
          {
            ...schedules[0],
            suppressed: { reason: "task", taskId: "42" },
          },
        ]}
        onOpenSchedule={onOpenSchedule}
        onOpenTask={onOpenTask}
      />,
    );

    await user.click(screen.getByRole("link", { name: "task 42" }));
    expect(onOpenTask).toHaveBeenCalledWith("42");
    expect(onOpenSchedule).not.toHaveBeenCalled();
  });

  it("closes a local create overlay when resetSeq increments or a suite run opens", async () => {
    const user = userEvent.setup();
    const { rerender } = render(
      <ProcessesList
        schedules={[schedules[0]]}
        query={{ kinds: ["schedule"] }}
        resetSeq={0}
      />,
    );

    await user.click(screen.getByRole("button", { name: "+ New schedule" }));
    expect(
      screen.getByRole("heading", { name: "New schedule" }),
    ).toBeInTheDocument();

    rerender(
      <ProcessesList
        schedules={[schedules[0]]}
        query={{ kinds: ["schedule"] }}
        resetSeq={1}
      />,
    );
    expect(
      screen.queryByRole("heading", { name: "New schedule" }),
    ).not.toBeInTheDocument();
  });

  it("refreshes runs and tasks on schedule Run now, and refreshes schedules when a template or suite is saved", async () => {
    const onRefreshSchedules = vi.fn(() => Promise.resolve());
    const onRefreshTemplates = vi.fn(() => Promise.resolve());
    const onRefreshSuites = vi.fn(() => Promise.resolve());
    const onRefreshRuns = vi.fn(() => Promise.resolve());
    const onRefreshTasks = vi.fn(() => Promise.resolve());
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const user = userEvent.setup();
    render(
      <Harness
        onRefreshSchedules={onRefreshSchedules}
        onRefreshTemplates={onRefreshTemplates}
        onRefreshSuites={onRefreshSuites}
        onRefreshRuns={onRefreshRuns}
        onRefreshTasks={onRefreshTasks}
      />,
    );

    // 1. Run now on a schedule refreshes schedules, suite runs, and tasks.
    await user.click(screen.getByText("Nightly bump"));
    await user.click(await screen.findByRole("button", { name: "Run now" }));
    expect(onRefreshSchedules).toHaveBeenCalledTimes(1);
    expect(onRefreshRuns).toHaveBeenCalledTimes(1);
    expect(onRefreshTasks).toHaveBeenCalledTimes(1);

    // 2. Saving a template refreshes both templates and schedules.
    onRefreshSchedules.mockClear();
    await user.click(screen.getByRole("link", { name: "Bump" }));
    await user.click(await screen.findByRole("button", { name: "Save" }));
    expect(onRefreshTemplates).toHaveBeenCalledTimes(1);
    expect(onRefreshSchedules).toHaveBeenCalledTimes(1);

    // 3. Saving a suite refreshes both suites and schedules.
    onRefreshSchedules.mockClear();
    await user.click(screen.getByRole("link", { name: "Qualify" }));
    await user.click(await screen.findByRole("button", { name: "Save" }));
    expect(onRefreshSuites).toHaveBeenCalledTimes(1);
    expect(onRefreshSchedules).toHaveBeenCalledTimes(1);
  });
});
