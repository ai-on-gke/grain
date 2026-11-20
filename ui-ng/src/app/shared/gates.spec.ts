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
import { ciChip, qaWorkflowChip, repoShort } from "./chips";
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

  it("reads a changelist task's Submit from clSubmit, not its absent gates", () => {
    expect(
      submitBlock(t(undefined, { clSubmit: { canSubmit: true } }), true),
    ).toBe("");
    expect(
      submitBlock(
        t(undefined, {
          clSubmit: {
            canSubmit: false,
            reasons: ["review is not approved (draft)", "behind head: sync it"],
          },
        }),
      ),
    ).toBe(
      "Not submittable yet: review is not approved (draft); behind head: sync it.",
    );
    expect(submitBlock(t(undefined, { clSubmit: { canSubmit: false } }))).toBe(
      "Not submittable yet.",
    );
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

describe("qaWorkflowChip", () => {
  it("returns null for nullish tasks or tasks without workflow metadata", () => {
    expect(qaWorkflowChip(null)).toBeNull();
    expect(qaWorkflowChip(undefined)).toBeNull();
    expect(qaWorkflowChip(t())).toBeNull();
  });

  it("formats workflow, complexity, blast, triage review, held, control, and fallback states", () => {
    expect(
      qaWorkflowChip(
        t(undefined, {
          qaSummary: {
            flowId: "standard-workflow",
            flowLabel: "Standard",
            arm: "treatment",
            complexityTier: "high",
            pending: true,
          },
        }),
      ),
    ).toEqual({
      text: "Standard · High",
      tone: "neutral",
      wsClass: "ws-chip-series",
      flowId: "standard-workflow",
      tip: "Queued under Standard workflow (estimated complexity: high). Stages execute on dispatch.",
    });

    expect(
      qaWorkflowChip(
        t(undefined, {
          qaSummary: {
            flowId: "fast-baseline",
            flowLabel: "Fast",
            arm: "treatment",
            complexityTier: "medium",
            stageCount: 1,
          },
        }),
      ),
    ).toEqual({
      text: "Fast · Med",
      tone: "setup",
      wsClass: "ws-chip-run",
      flowId: "fast-baseline",
      tip: "Running under Fast workflow (complexity: medium) · 1 stage recorded.",
    });

    expect(
      qaWorkflowChip(
        t(undefined, {
          qaSummary: {
            flowId: "standard-workflow",
            flowLabel: "Standard",
            arm: "treatment",
            blastTier: "T1",
            blastScore: 18,
            triageDecision: "auto_merge",
            verdict: "pass",
            stageCount: 4,
          },
        }),
      ),
    ).toEqual({
      text: "Standard · T1",
      tone: "healthy",
      wsClass: "ws-chip-done",
      flowId: "standard-workflow",
      tip: "Workflow Standard: blast T1 (18/100), 4 stages recorded.",
    });

    expect(
      qaWorkflowChip(
        t(undefined, {
          qaSummary: {
            flowId: "standard-workflow",
            flowLabel: "Standard",
            arm: "treatment",
            blastTier: "T2",
            blastScore: 44,
            triageDecision: "require_human",
            verdict: "pass",
          },
        }),
      ),
    ).toEqual({
      text: "Standard · T2 Review",
      tone: "setup",
      wsClass: "ws-chip-warn",
      flowId: "standard-workflow",
      triageHold: true,
      tip: "Workflow Standard: blast tier T2 routed to human review.",
    });

    expect(
      qaWorkflowChip(
        t(undefined, {
          qaSummary: {
            flowId: "gates-only",
            flowLabel: "Gates only",
            arm: "treatment",
            blastTier: "T2",
            verdict: "fail",
          },
        }),
      ),
    ).toEqual({
      text: "Gates only · T2 Held",
      tone: "error",
      wsClass: "ws-chip-err",
      flowId: "gates-only",
      tip: "Workflow Gates only: pre-PR gate reported blocking findings (blast T2 · 0/100).",
    });

    expect(
      qaWorkflowChip(
        t(undefined, {
          qaSummary: {
            flowId: "control",
            flowLabel: "Control",
            arm: "control",
          },
        }),
      ),
    ).toEqual({
      text: "Control",
      tone: "neutral",
      wsClass: "ws-chip-series",
      flowId: "control",
      tip: "Control arm — baseline path without active workflow prompt or blocking pre-PR gates.",
    });

    expect(
      qaWorkflowChip(
        t(undefined, {
          humanReviewHold: [
            {
              source: "qa-triage",
              reason: "[T3 · 72/100] Cross-package contract change",
            },
          ],
        }),
      ),
    ).toEqual({
      text: "T3 · Review",
      tone: "setup",
      wsClass: "ws-chip-warn",
      flowId: "standard-workflow",
      triageHold: true,
      tip: "[T3 · 72/100] Cross-package contract change",
    });

    expect(
      qaWorkflowChip(
        t(undefined, {
          qaFlowRef: "fast-baseline@1",
        }),
      ),
    ).toEqual({
      text: "Workflow · fast-baseline@1",
      tone: "neutral",
      wsClass: "ws-chip-series",
      flowId: "fast-baseline@1",
      tip: "Pinned to workflow fast-baseline@1.",
    });
  });
});
