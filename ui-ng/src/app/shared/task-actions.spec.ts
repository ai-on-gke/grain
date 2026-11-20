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

import { describe, expect, it } from "vitest";
import type { MergeGate } from "../api/models";
import {
  autoMergeSentence,
  availableActions,
  nextStep,
  sendBackComment,
  whatsNeeded,
  type ActionViewer,
  type AnyTask,
} from "./task-actions";

const operator: ActionViewer = { can: () => true, fullAccess: true };
const limited: ActionViewer = { can: () => true, fullAccess: false };
const reader: ActionViewer = { can: () => false, fullAccess: false };

function task(over: Partial<AnyTask>): AnyTask {
  return { id: "5812", title: "t", state: "queued", repo: "acme/web", ...over };
}

const CI_RUNNING: MergeGate = { id: "ci", label: "CI", state: "running" };
const CI_PASSED: MergeGate = { id: "ci", label: "CI", state: "passed" };
const CI_FAILING: MergeGate = { id: "ci", label: "CI", state: "failing" };
const SEC_PASSED: MergeGate = {
  id: "security-review",
  label: "Security review",
  state: "passed",
};
const REVIEW_FINDINGS: MergeGate = {
  id: "review",
  label: "Code review",
  state: "findings",
  reviewTask: "5882",
  overridable: true,
  acceptable: true,
};
const APPROVAL: MergeGate = {
  id: "approval",
  label: "Approval",
  state: "passed",
};

const ids = (t: AnyTask, v = operator) =>
  availableActions(t, v).map((a) => a.id);

describe("availableActions", () => {
  it("offers Reopen and Follow up only on a closed task", () => {
    expect(ids(task({ state: "closed" }))).toEqual(["reopen", "follow-up"]);
  });

  it("offers Approve first on a proposal", () => {
    const got = ids(task({ state: "proposed" }));
    expect(got[0]).toBe("approve");
    expect(got).toContain("defer");
    expect(got).toContain("close");
    expect(got).not.toContain("submit");
    expect(got).not.toContain("stop");
  });

  it("offers Bring back on a deferred task, and Approve when unapproved", () => {
    expect(ids(task({ state: "deferred", approved: true }))).toContain(
      "undefer",
    );
    expect(ids(task({ state: "deferred", approved: true }))).not.toContain(
      "approve",
    );
    expect(ids(task({ state: "deferred" }))).toContain("approve");
  });

  it("offers Run now, Defer, Withdraw and Pause on queued work", () => {
    const got = ids(task({ state: "queued" }));
    expect(got).toEqual(
      expect.arrayContaining(["run-now", "defer", "withdraw", "pause"]),
    );
    expect(got).not.toContain("approve");
  });

  it("offers Stop and the running-only controls while running", () => {
    const got = ids(task({ state: "running" }));
    expect(got).toEqual(
      expect.arrayContaining([
        "stop",
        "pause",
        "pause-immediate",
        "recreate-sandbox",
      ]),
    );
    expect(got).not.toContain("run-now");
    expect(got).not.toContain("retry");
  });

  it("offers Resume instead of Pause on a paused task", () => {
    const got = ids(
      task({
        state: "queued",
        paused: { at: "x", by: "ana", fromState: "queued" },
      }),
    );
    expect(got[0]).toBe("resume-pause");
    expect(got).not.toContain("pause");
  });

  it("offers Answer on a question, Resume on a stopped turn", () => {
    const q = availableActions(
      task({ state: "awaiting_reply", questionSummary: "Which API?" }),
      operator,
    );
    expect(q[0]).toMatchObject({ id: "reply", label: "Answer" });
    const s = availableActions(
      task({ state: "awaiting_reply", turnStopped: true }),
      operator,
    );
    expect(s[0]).toMatchObject({ id: "retry", label: "Resume" });
    expect(s.map((a) => a.id)).not.toContain("reply");
  });

  it("offers no reply on a run parked on a grant: its card answers it", () => {
    const got = ids(task({ state: "awaiting_reply", awaitingAccess: true }));
    expect(got).not.toContain("reply");
    expect(got).not.toContain("run-now");
  });

  it("offers Retry on a failed task", () => {
    expect(ids(task({ state: "failed" }))[0]).toBe("retry");
  });

  it("findings plus CI running: send back, accept, no Submit", () => {
    const t = task({
      state: "awaiting_submit",
      pullRequest: "acme/web#1",
      mergeGates: [CI_RUNNING, SEC_PASSED, REVIEW_FINDINGS, APPROVAL],
      reviewFindings: [
        { gate: "review", summary: "nil deref", reviewTask: "5882" },
      ],
    });
    const acts = availableActions(t, operator);
    expect(acts.map((a) => a.id)).toEqual(
      expect.arrayContaining(["send-back", "accept-findings", "force-merge"]),
    );
    expect(acts.find((a) => a.id === "send-back")?.gate).toBe("review");
    expect(acts.find((a) => a.id === "accept-findings")?.gate).toBe("review");
    expect(acts.map((a) => a.id)).not.toContain("submit");
    expect(whatsNeeded(t)).toBe(
      "Code review left 1 finding. Fix it (send it back to the agent) or accept it to continue. CI is still running.",
    );
    expect(nextStep(t)).toBe("Fix or accept 1 review finding");
  });

  it("never offers accept or force merge to a limited role", () => {
    const t = task({
      state: "awaiting_submit",
      mergeGates: [CI_PASSED, REVIEW_FINDINGS],
      reviewFindings: [
        { gate: "review", summary: "nil deref", reviewTask: "5882" },
      ],
    });
    const got = ids(t, limited);
    expect(got).toContain("send-back");
    expect(got).not.toContain("accept-findings");
    expect(got).not.toContain("force-merge");
    expect(whatsNeeded(t, limited)).toBe(
      "Code review left 1 finding. Fix it (send it back to the agent) to continue.",
    );
    expect(nextStep(t, false, limited)).toBe("Fix 1 review finding");
    // Findings no review stands behind are not even an operator's to accept.
    const held = task({
      state: "awaiting_submit",
      mergeGates: [CI_PASSED, { ...REVIEW_FINDINGS, acceptable: false }],
      reviewFindings: [
        { gate: "review", summary: "nil deref", reviewTask: "5882" },
      ],
    });
    expect(ids(held, operator)).not.toContain("accept-findings");
    expect(whatsNeeded(held, operator)).toBe(
      "Code review left 1 finding. Fix it (send it back to the agent) to continue.",
    );
    expect(nextStep(held, false, operator)).toBe("Fix 1 review finding");
  });

  it("CI failed: send back to fix, no Submit", () => {
    const t = task({
      state: "awaiting_submit",
      mergeGates: [CI_FAILING, APPROVAL],
    });
    const acts = availableActions(t, limited);
    expect(acts[0]).toMatchObject({
      id: "send-back",
      label: "Send back to fix",
    });
    expect(acts.map((a) => a.id)).not.toContain("submit");
    expect(whatsNeeded(t)).toContain("CI is failing");
    expect(nextStep(t)).toBe("Fix CI");
  });

  it("CI running only: Submit merges when CI passes", () => {
    const t = task({
      state: "awaiting_submit",
      mergeGates: [CI_RUNNING, { ...APPROVAL, state: "needed" }],
    });
    const submit = availableActions(t, limited).find((a) => a.id === "submit");
    expect(submit).toMatchObject({ label: "Submit (merges when CI passes)" });
    expect(submit?.disabled).toBeUndefined();
    expect(whatsNeeded(t)).toContain("Submit now and it merges when it passes");
    expect(nextStep(t)).toBe("Waiting for CI, then Submit");
  });

  it("CI not reported yet: Submit shown off, with its reason", () => {
    const t = task({ state: "awaiting_submit", mergeGates: [] });
    const submit = availableActions(t, limited).find((a) => a.id === "submit");
    expect(submit?.disabled).toContain("CI has not reported");
  });

  it("all gates passed: Submit is the one main action", () => {
    const t = task({
      state: "awaiting_submit",
      mergeGates: [CI_PASSED, SEC_PASSED, { ...APPROVAL, state: "needed" }],
    });
    const acts = availableActions(t, limited);
    expect(acts[0]).toMatchObject({ id: "submit", label: "Submit" });
    expect(nextStep(t)).toBe("Ready to Submit");
    expect(whatsNeeded(t)).toContain("Submit");
  });

  it("security hold: security review running, Submit merges after", () => {
    const t = task({
      state: "awaiting_submit",
      mergeGates: [
        CI_PASSED,
        { id: "security-review", label: "Security review", state: "running" },
      ],
    });
    const submit = availableActions(t, limited).find((a) => a.id === "submit");
    expect(submit?.label).toBe("Submit (merges when its gates pass)");
    expect(whatsNeeded(t)).toContain("Security review is still running");
  });

  it("offers Run now on a green Submit, but not beside Send back", () => {
    const green = task({
      state: "awaiting_submit",
      mergeGates: [CI_PASSED, { ...APPROVAL, state: "needed" }],
    });
    expect(ids(green, limited)).toContain("run-now");
    const red = task({ state: "awaiting_submit", mergeGates: [CI_FAILING] });
    expect(ids(red, limited)).toContain("send-back");
    expect(ids(red, limited)).not.toContain("run-now");
  });

  it("offers nothing a reader may not do", () => {
    expect(ids(task({ state: "awaiting_submit" }), reader)).toEqual([]);
    expect(ids(task({ state: "running" }), reader)).toEqual([]);
  });
});

describe("autoMergeSentence", () => {
  it("says why auto-merge went off and when to submit again", () => {
    const t = task({
      state: "awaiting_submit",
      autoMerge: false,
      mergeGates: [CI_RUNNING, REVIEW_FINDINGS],
      transitions: [
        { state: "auto_merge_on", at: "1" },
        {
          state: "auto_merge_off",
          at: "2",
          reason: "the code review pushed fixes that need a fresh look",
        },
      ],
    } as Partial<AnyTask>);
    expect(autoMergeSentence(t)).toBe(
      "Auto-merge was turned off because the code review pushed fixes that need a fresh look. Submit again after Code review is settled.",
    );
  });

  it("says nothing once auto-merge is back on", () => {
    const t = task({
      autoMerge: true,
      transitions: [{ state: "auto_merge_off", at: "2", reason: "x" }],
    } as Partial<AnyTask>);
    expect(autoMergeSentence(t)).toBe("");
  });
});

describe("whatsNeeded", () => {
  it("is never empty", () => {
    for (const state of [
      "proposed",
      "queued",
      "deferred",
      "running",
      "awaiting_reply",
      "awaiting_submit",
      "failed",
      "completed",
      "closed",
    ]) {
      expect(whatsNeeded(task({ state })).length).toBeGreaterThan(10);
    }
  });
});

describe("sendBackComment", () => {
  it("names each finding and its review task", () => {
    const body = sendBackComment(
      task({
        mergeGates: [REVIEW_FINDINGS, CI_FAILING],
        reviewFindings: [
          { gate: "review", summary: "nil deref in Close", reviewTask: "5882" },
        ],
      }),
    );
    expect(body).toContain(
      "- Code review: nil deref in Close (review task #5882)",
    );
    expect(body).toContain("- CI is failing");
  });
});
