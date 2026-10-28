import { describe, expect, it } from "vitest";
import {
  describeSkip,
  describeSuppression,
  isOverdue,
  OVERDUE_GRACE_MS,
} from "./scheduleSuppression.js";

describe("describeSuppression", () => {
  it("has a clause for each reason a firing is held back", () => {
    expect(describeSuppression({ reason: "task" })).toMatch(/task/);
    expect(describeSuppression({ reason: "suiteRun" })).toMatch(/suite run/);
    expect(describeSuppression({ reason: "workflowRun" })).toMatch(/CI run/);
    expect(describeSuppression({ reason: "review" })).toMatch(/review/);
  });

  // A daemon newer than this build may know a reason this one does not,
  // and a stopped schedule must not read as a working one because of a
  // word.
  it("still says the last firing has not finished for a reason it does not know", () => {
    expect(describeSuppression({ reason: "somethingNew" })).toMatch(
      /has not finished/,
    );
  });
});

describe("isOverdue", () => {
  const now = Date.parse("2026-09-22T12:00:00Z");

  it("is false for a next run that is still ahead", () => {
    expect(isOverdue("2026-09-22T12:30:00Z", now)).toBe(false);
  });

  // The cadence came round seconds ago and the next reconcile cycle is
  // about to fire it -- a row that lit up here and went out again on the
  // next poll would be noise.
  it("is false just after a schedule comes due", () => {
    expect(isOverdue(new Date(now - 30 * 1000).toISOString(), now)).toBe(false);
  });

  it("is true once it has gone by longer than a few cycles", () => {
    expect(
      isOverdue(new Date(now - OVERDUE_GRACE_MS - 1000).toISOString(), now),
    ).toBe(true);
  });

  it("is false for a schedule with no next run at all", () => {
    expect(isOverdue(undefined, now)).toBe(false);
    expect(isOverdue("not a date", now)).toBe(false);
  });
});

describe("describeSkip", () => {
  const summary =
    "skipped at 2026-10-13 02:00 UTC: #3201 from the previous firing is still open (awaiting_submit)";

  it("says a held firing will still fire", () => {
    expect(describeSkip({ summary, outcome: "" })).toBe(
      `${summary} -- held; it fires once as soon as that clears`,
    );
  });

  it("says a failed firing is being retried", () => {
    const failed =
      "skipped at 2026-10-15 03:34 UTC: firing failed: connection refused";
    expect(
      describeSkip({ summary: failed, reason: "firingFailed", outcome: "" }),
    ).toBe(
      `${failed} -- held; retried every cycle, and it fires once as soon as a retry succeeds`,
    );
  });

  it("says when a held firing fired late, and what it absorbed", () => {
    expect(
      describeSkip(
        {
          summary,
          outcome: "fired",
          firedTaskId: "3560",
          resolvedAt: "02:09",
          folded: 1,
        },
        (t) => `at-${t}`,
      ),
    ).toBe(
      `${summary} -- fired late as #3560 at at-02:09, standing in for 1 later firing`,
    );
  });

  it("says a dropped firing is not coming", () => {
    expect(describeSkip({ summary, outcome: "dropped" })).toMatch(/dropped/);
  });
});
