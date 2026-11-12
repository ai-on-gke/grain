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

import type { MergeGate, Task, TaskDetail } from "../api/models";
import { ciChip, repoShort } from "./chips";
import { gateSummary, isMerged, submitBlock, withDetailGates } from "./gates";
import { scopeLines, scopeOf } from "./scope";

const t = (gates?: MergeGate[], extra: Partial<Task> = {}): Task => ({
  id: "1",
  title: "x",
  state: "awaiting_submit",
  mergeGates: gates,
  ...extra,
});

const approval: MergeGate = {
  id: "approval",
  label: "Approval",
  state: "needed",
};

describe("merge gates", () => {
  it("rolls gates up, leaving the approval gate out of the count", () => {
    expect(gateSummary(t()).state).toBe("unknown");
    expect(
      gateSummary(t([{ id: "ci", label: "CI", state: "passed" }, approval])),
    ).toMatchObject({ state: "passed", passed: 1, total: 1 });
    expect(
      gateSummary(
        t([
          { id: "ci", label: "CI", state: "failing" },
          { id: "review", label: "Review", state: "findings" },
          approval,
        ]),
      ),
    ).toMatchObject({ state: "failing", failing: ["CI", "Review"] });
    expect(
      gateSummary(t([{ id: "ci", label: "CI", state: "running" }, approval]))
        .state,
    ).toBe("running");
    expect(
      gateSummary(t([{ id: "ci", label: "CI", state: "overridden" }])).state,
    ).toBe("passed");
  });

  it("blocks Submit on failing, findings, needed or unreported gates, not on running", () => {
    expect(submitBlock(t())).toContain("has not reported");
    expect(
      submitBlock(t([{ id: "ci", label: "CI", state: "failing" }, approval])),
    ).toContain("CI is failing");
    expect(
      submitBlock(t([{ id: "review", label: "Review", state: "findings" }])),
    ).toContain("Review is failing");
    expect(
      submitBlock(
        t([{ id: "security-review", label: "Security", state: "needed" }]),
      ),
    ).toContain("Security is still needed");
    // Approval needed is what Submit itself gives.
    expect(submitBlock(t([approval]))).toBe("");
    expect(
      submitBlock(t([{ id: "ci", label: "CI", state: "running" }, approval])),
    ).toBe("");
    expect(
      submitBlock(t([{ id: "ci", label: "CI", state: "passed" }, approval])),
    ).toBe("");
  });

  it("draws a compact CI chip with a glyph, never colour alone", () => {
    expect(
      ciChip(t([{ id: "ci", label: "CI", state: "passed" }, approval])),
    ).toMatchObject({ text: "✓ 1/1", tone: "healthy" });
    expect(
      ciChip(t([{ id: "ci", label: "CI", state: "failing" }])),
    ).toMatchObject({ text: "✕ CI", tone: "error" });
    expect(
      ciChip(t([{ id: "ci", label: "CI", state: "running" }])),
    ).toMatchObject({ text: "… CI", tone: "setup" });
    expect(ciChip(t([approval]))?.text).toBe("✓ no checks");
    expect(ciChip(t())?.text).toBe("CI unknown");
    // Nothing to say for a proposal with no pull request.
    expect(ciChip(t(undefined, { state: "proposed" }))).toBeNull();
  });

  it("shortens a repository to its name and reads merged closes", () => {
    expect(repoShort("sidthe/grain-scratch-repo")).toBe("grain-scratch-repo");
    expect(repoShort("solo")).toBe("solo");
    expect(repoShort(undefined)).toBe("");
    expect(isMerged(t(undefined, { prMergedAt: "2026-11-11T00:00:00Z" }))).toBe(
      true,
    );
    expect(isMerged(t(undefined, { closedReason: "merged" }))).toBe(true);
    expect(isMerged(t(undefined, { closedReason: "nothing_needed" }))).toBe(
      false,
    );
  });
  it("treats list-only 'waiting for checks' as Checking CI and merges detail gates", () => {
    const listPlaceholder = t([
      { id: "ci", label: "CI", state: "running", detail: "waiting for checks" },
      approval,
    ]);
    expect(gateSummary(listPlaceholder, true)).toMatchObject({
      state: "unknown",
      checking: true,
    });
    expect(submitBlock(listPlaceholder, true)).toContain("Checking CI");
    expect(ciChip(listPlaceholder, true)?.text).toBe("Checking CI…");

    const noDetail = withDetailGates(listPlaceholder, null);
    expect(noDetail.hasDetail).toBe(false);
    expect(noDetail.task).toBe(listPlaceholder);

    const detail = {
      ...listPlaceholder,
      mergeGates: [
        { id: "ci", label: "CI", state: "failing", detail: "unit tests" },
        approval,
      ],
    } as TaskDetail;
    const merged = withDetailGates(listPlaceholder, detail);
    expect(merged.hasDetail).toBe(true);
    expect(submitBlock(merged.task)).toContain("CI is failing");
    expect(ciChip(merged.task)?.text).toBe("✕ CI");
  });
});

describe("batch scope", () => {
  it("counts per repository and per owner, and flags more than one repository", () => {
    const s = scopeOf([
      { repo: "acme/web", owner: "ana" },
      { repo: "acme/web", owner: "bo" },
      { repo: "sidthe/grain", owner: "ana" },
      { repo: "", owner: "" },
    ]);
    expect(s.multiRepo).toBe(true);
    expect(scopeLines(s)).toEqual([
      "2 in acme/web",
      "1 in no repository",
      "1 in sidthe/grain",
      "2 owned by ana",
      "1 owned by bo",
    ]);
  });

  it("names one owner when other than the viewer or when multiple owners exist", () => {
    const sameViewer = scopeOf(
      [
        { repo: "acme/web", owner: "ana" },
        { repo: "acme/web", owner: "ana" },
      ],
      "ana",
    );
    expect(sameViewer.multiRepo).toBe(false);
    expect(sameViewer.otherOwner).toBe(false);
    expect(scopeLines(sameViewer)).toEqual(["2 in acme/web"]);

    const otherOwner = scopeOf(
      [
        { repo: "acme/web", owner: "bob" },
        { repo: "acme/web", owner: "bob" },
      ],
      "alice",
    );
    expect(otherOwner.otherOwner).toBe(true);
    expect(scopeLines(otherOwner)).toEqual(["2 in acme/web", "2 owned by bob"]);
  });
});
