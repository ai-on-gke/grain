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

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import GoalSection, { bodyOf, goalSummary } from "./GoalSection.jsx";

const goal = {
  kind: "base-green",
  spec: { repo: "acme/widgets", branch: "main" },
  onMet: "close",
  onLimit: "hold",
  evidence: "`main` on acme/widgets is red at c0ffee123456: go unit",
  progressAt: "2026-10-26T12:00:00Z",
  resumesSinceProgress: 2,
  maxResumes: 6,
  maxStall: 12 * 3.6e12,
};

describe("GoalSection", () => {
  it("offers to add a goal to a task with none", () => {
    render(<GoalSection t={{ id: "7", state: "queued" }} act={vi.fn()} />);
    expect(screen.getByText(/No goal/)).toBeTruthy();
    fireEvent.click(screen.getByText("Add goal"));
    expect(screen.getByTestId("goal-editor")).toBeTruthy();
  });

  it("shows the goal, its live check and its progress", () => {
    render(
      <GoalSection t={{ id: "7", state: "deferred", goal }} act={vi.fn()} />,
    );
    expect(screen.getByTestId("goal-summary").textContent).toContain(
      "main on acme/widgets green",
    );
    expect(screen.getByTestId("goal-evidence").textContent).toContain(
      "red at c0ffee",
    );
    expect(screen.getByTestId("goal-progress").textContent).toContain(
      "2 resume(s) without progress",
    );
    expect(screen.getByTestId("goal-progress").textContent).toContain(
      "6 resumes or 12h",
    );
    expect(screen.getByText("Remove goal")).toBeTruthy();
  });

  it("saves the edited goal as the API takes it", async () => {
    const act = vi.fn(() => Promise.resolve(true));
    render(
      <GoalSection
        t={{ id: "7", state: "queued", repo: "acme/widgets" }}
        act={act}
      />,
    );
    fireEvent.click(screen.getByText("Add goal"));
    fireEvent.click(screen.getByText("Save goal"));
    expect(act).toHaveBeenCalledTimes(1);
  });

  it("draws no controls on a closed task", () => {
    render(
      <GoalSection
        t={{ id: "7", state: "closed", goal: { ...goal, outcome: "goal-met" } }}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText("Edit goal")).toBeNull();
    expect(screen.getByTestId("goal-summary").textContent).toContain(
      "Met; the task was closed.",
    );
  });
});

describe("bodyOf and goalSummary", () => {
  it("keeps only the fields a kind reads", () => {
    expect(
      bodyOf({
        kind: "tasks-closed",
        tasks: "#12, 13",
        until: "merged",
        onMet: "rest",
        onLimit: "notify",
        repo: "x/y",
      }),
    ).toEqual({
      kind: "tasks-closed",
      spec: { tasks: ["12", "13"], until: "merged" },
      on_met: "rest",
      on_limit: "notify",
    });
    expect(goalSummary({ kind: "person-closes" })).toBe("a person closes it");
    expect(goalSummary({ kind: "pr-merged", spec: { task: "9" } })).toBe(
      "task 9's pull request merged",
    );
  });
});
