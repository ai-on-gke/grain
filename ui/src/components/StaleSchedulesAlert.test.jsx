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
import { describe, expect, it } from "vitest";
import StaleSchedulesAlert, {
  formatCadence,
  staleScheduleRepo,
} from "./StaleSchedulesAlert.jsx";

const DAY_NS = 24 * 3600 * 1e9;

const dark = {
  id: "workflow-dark",
  title: "live-agent",
  repo: { Owner: "acme", Name: "widgets", Host: "" },
  workflow: "live-agent",
  cadence: DAY_NS,
  lastRunAt: "2026-10-28T07:37:00Z",
  reason:
    "acme/widgets's ci_mode is github, which drops .grain/workflows/live-agent.yml's cron.",
};
const never = {
  id: "never-ran",
  title: "never ran",
  repo: { Owner: "other", Name: "repo", Host: "" },
  cadence: 6 * 3600 * 1e9,
};

describe("StaleSchedulesAlert", () => {
  it("renders nothing when nothing is stale", () => {
    const { container } = render(<StaleSchedulesAlert schedules={[]} />);
    expect(container).toBeEmptyDOMElement();
    render(<StaleSchedulesAlert schedules={undefined} />);
    expect(screen.queryByTestId("stale-schedules")).toBeNull();
  });

  it("says each schedule's name, workflow, cadence, last run and reason", () => {
    render(<StaleSchedulesAlert schedules={[dark, never]} />);
    const alert = screen.getByTestId("stale-schedules");
    expect(alert).toHaveTextContent("2 schedules have stopped running");
    const [first, second] = within(alert).getAllByTestId("stale-schedule");
    expect(first).toHaveTextContent("live-agent");
    expect(first).toHaveTextContent("workflow live-agent");
    expect(first).toHaveTextContent("acme/widgets");
    expect(first).toHaveTextContent("every 1d");
    expect(first).not.toHaveTextContent("last ran never");
    expect(
      within(first).getByTestId("stale-schedule-reason"),
    ).toHaveTextContent(".grain/workflows/live-agent.yml");
    expect(within(first).getByTestId("stale-schedule-name")).toHaveAttribute(
      "href",
      "/processes/schedules/workflow-dark",
    );
    expect(second).toHaveTextContent("never ran");
    expect(second).toHaveTextContent("every 6h");
    expect(second).toHaveTextContent("last ran never");
    expect(second).not.toHaveTextContent("workflow");
  });

  it("narrows to one repository, without repeating its name", () => {
    render(
      <StaleSchedulesAlert schedules={[dark, never]} repo="Acme/Widgets" />,
    );
    const rows = screen.getAllByTestId("stale-schedule");
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveAttribute("data-schedule", "workflow-dark");
    expect(rows[0].firstChild).not.toHaveTextContent("acme/widgets");
    expect(screen.getByTestId("stale-schedules")).toHaveTextContent(
      "A schedule has stopped running",
    );
  });

  it("renders nothing for a repository with no stale schedule", () => {
    render(<StaleSchedulesAlert schedules={[dark]} repo="other/repo" />);
    expect(screen.queryByTestId("stale-schedules")).toBeNull();
  });
});

describe("formatCadence", () => {
  it("says a cadence in its largest whole unit", () => {
    expect(formatCadence(DAY_NS)).toBe("1d");
    expect(formatCadence(7 * DAY_NS)).toBe("7d");
    expect(formatCadence(3600e9)).toBe("1h");
    expect(formatCadence(90 * 60e9)).toBe("1h 30m");
    expect(formatCadence(15 * 60e9)).toBe("15m");
    expect(formatCadence(0)).toBe("—");
  });
});

describe("staleScheduleRepo", () => {
  it("writes a RepoRef the way every other surface does", () => {
    expect(staleScheduleRepo({ Owner: "a", Name: "b", Host: "" })).toBe("a/b");
    expect(
      staleScheduleRepo({ Owner: "a", Name: "b", Host: "git.example" }),
    ).toBe("git.example/a/b");
    expect(staleScheduleRepo("a/b")).toBe("a/b");
    expect(staleScheduleRepo(undefined)).toBe("");
  });
});
