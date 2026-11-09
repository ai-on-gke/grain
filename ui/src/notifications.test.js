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

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  decideRelease,
  notificationAlerts,
  notificationSentence,
  pendingReleaseOf,
} from "./notifications.js";
import api from "./api.js";

vi.mock("./api.js", () => ({ default: vi.fn(async () => ({})) }));

const open = {
  id: 7,
  subject: "pipeline.merge-stalled:12",
  severity: "warning",
  title: "A merge has been stalled",
  body: "Retried three times.\nMore detail.",
  raiseCount: 2,
  raisedAt: "2026-10-26T08:00:00Z",
};

describe("notificationAlerts", () => {
  it("turns open, unacknowledged notifications into alert rows", () => {
    const rows = notificationAlerts([
      open,
      { ...open, id: 8, ackedAt: "2026-10-26T08:30:00Z" },
      { ...open, id: 9, clearedAt: "2026-10-26T08:40:00Z" },
    ]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      id: "notification-7",
      severity: "warning",
      title: "A merge has been stalled",
    });
    expect(rows[0].actions.map((a) => a.kind)).toEqual([
      "ack-notification",
      "investigate-notification",
    ]);
  });

  it("offers the existing investigation rather than a new one", () => {
    const [row] = notificationAlerts([{ ...open, investigationTaskId: "40" }]);
    expect(row.actions[1].label).toBe("Open investigation");
  });

  // The base-branch reconciler's red-base notification names its repair
  // in taskId; the bell offered no way to it, only a markdown link drawn
  // as text (2026-11-03 deployment-operations walkthrough).
  it("opens the task a notification names, before anything else", () => {
    const [row] = notificationAlerts([
      { ...open, subject: "pipeline.base-red:acme/widgets@main", taskId: "5" },
    ]);
    expect(row.actions[0]).toEqual({
      kind: "open-task",
      label: "Open #5",
      taskId: "5",
    });
    expect(row.actions.map((a) => a.kind)).toEqual([
      "open-task",
      "ack-notification",
      "investigate-notification",
    ]);
  });
});

describe("notificationSentence", () => {
  it("is the body's first line and how often it was raised", () => {
    expect(notificationSentence(open)).toBe(
      "Retried three times. Raised 2 times.",
    );
  });

  it("draws a markdown body's links and inline code as their text", () => {
    expect(
      notificationSentence({
        ...open,
        raiseCount: 1,
        body:
          'grain CI on acme/widgets `main` is confirmed red: at 55d6e7c ("failure: test"). ' +
          "[Task #5](/tasks/5) is repairing it; everything waiting on the branch waits on that.",
      }),
    ).toBe(
      'grain CI on acme/widgets main is confirmed red: at 55d6e7c ("failure: test"). ' +
        "Task #5 is repairing it; everything waiting on the branch waits on that.",
    );
  });

  it("falls back to the subject for an empty body", () => {
    expect(notificationSentence({ ...open, body: "", raiseCount: 1 })).toBe(
      "pipeline.merge-stalled:12",
    );
  });
});

const pending = {
  id: 11,
  subject: "release.pending:v2.4.0",
  family: "release.pending",
  severity: "warning",
  title: "v2.4.0 is ready to promote (v2.4.0-rc.2 passed its soak)",
  body: "v2.4.0-rc.2 passed its 24h staging soak.",
  repo: "ai-on-gke/grain",
  actions: ["approve-release", "reject-release"],
  raiseCount: 1,
  raisedAt: "2026-10-26T08:00:00Z",
};

describe("pending releases", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    api.mockClear();
  });

  it("offer Approve and Reject before anything else", () => {
    const [row] = notificationAlerts([pending]);
    expect(row.actions.map((a) => a.label)).toEqual([
      "Approve",
      "Reject",
      "Acknowledge",
      "Investigate",
    ]);
    expect(row.actions[0].release).toEqual({
      repo: "ai-on-gke/grain",
      version: "v2.4.0",
      soak: null,
      shortSoak: null,
      breaking: null,
    });
  });

  it("fall back to grain's own repository when the notification names none", () => {
    const { repo: _repo, ...unnamed } = pending;
    expect(pendingReleaseOf(unnamed)).toEqual({
      repo: "ai-on-gke/grain",
      version: "v2.4.0",
      soak: null,
      shortSoak: null,
      breaking: null,
    });
  });

  it("offer Approve and Reject for a patch release the train stages (vX.Y.Z)", async () => {
    const patch = {
      ...pending,
      subject: "release.pending:v2.4.1",
      title: "v2.4.1 is ready to promote (v2.4.1-rc.1 passed its soak)",
      body:
        "v2.4.1-rc.1 passed qualification (Stage 1 nightly.yml, Stage 2 grain/release-qualification) and is staged as v2.4.1-pending.\n\n" +
        "Qualification: PASS",
    };
    expect(pendingReleaseOf(patch)).toEqual({
      repo: "ai-on-gke/grain",
      version: "v2.4.1",
      soak: null,
      shortSoak: null,
      breaking: null,
    });
    const [row] = notificationAlerts([patch]);
    expect(row.actions.map((a) => a.label)).toEqual([
      "Approve",
      "Reject",
      "Acknowledge",
      "Investigate",
    ]);

    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(true);
    expect(await decideRelease(pendingReleaseOf(patch), true)).toBe(true);
    expect(confirm.mock.calls.at(-1)[0]).toContain("Approve v2.4.1?");
    expect(api).toHaveBeenCalledWith(
      "/api/repos/ai-on-gke/grain/pending-releases/v2.4.1/approve",
      { method: "POST" },
    );
  });

  it("are only the release.pending family with its actions", () => {
    expect(pendingReleaseOf(open)).toBeNull();
    expect(pendingReleaseOf({ ...pending, actions: [] })).toBeNull();
    expect(
      pendingReleaseOf({ ...pending, subject: "release.pending:latest" }),
    ).toBeNull();
    expect(
      pendingReleaseOf({ ...pending, subject: "release.pending:v2.4" }),
    ).toBeNull();
    expect(
      pendingReleaseOf({
        ...pending,
        subject: "release.pending:v2.4.1-rc.1",
      }),
    ).toBeNull();
  });

  it("ask before approving, and post to the person-only route", async () => {
    vi.spyOn(window, "confirm").mockReturnValueOnce(false);
    expect(await decideRelease(pendingReleaseOf(pending), true)).toBe(false);
    expect(api).not.toHaveBeenCalled();

    vi.spyOn(window, "confirm").mockReturnValueOnce(true);
    expect(await decideRelease(pendingReleaseOf(pending), true)).toBe(true);
    expect(api).toHaveBeenCalledWith(
      "/api/repos/ai-on-gke/grain/pending-releases/v2.4.0/approve",
      { method: "POST" },
    );
  });

  it("read the soak and the SHORT SOAK warning off the train's body", () => {
    const short = {
      ...pending,
      body:
        "v2.4.0-rc.2 passed its 24h staging soak and is staged as v2.4.0-pending.\n\n" +
        "SHORT SOAK: under the 22h minimum; approving it needs short_soak_ok: true.\n\n" +
        "Soak: soaked 1h00m of 24h (FORCE=true): supervisor: ok; http: ok\n" +
        "Release notes: task 12",
    };
    expect(pendingReleaseOf(short)).toMatchObject({
      soak: "soaked 1h00m of 24h (FORCE=true)",
      shortSoak:
        "SHORT SOAK: under the 22h minimum; approving it needs short_soak_ok: true.",
    });
    const full = {
      ...pending,
      body: "rc passed.\n\nSoak: soaked 25h10m of 24h: PASS\nRelease notes: x",
    };
    expect(pendingReleaseOf(full)).toMatchObject({
      soak: "soaked 25h10m of 24h",
      shortSoak: null,
    });
    expect(notificationAlerts([short])[0].actions[0].label).toBe(
      "Approve short soak",
    );
    expect(notificationAlerts([full])[0].actions[0].label).toBe("Approve");
  });

  it("approve a short soak only with short_soak_ok, after saying so", async () => {
    const release = {
      repo: "ai-on-gke/grain",
      version: "v2.4.0",
      soak: "soaked 1h00m of 24h",
      shortSoak: "SHORT SOAK: under the 22h minimum.",
    };
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(true);
    expect(await decideRelease(release, true)).toBe(true);
    expect(confirm.mock.calls.at(-1)[0]).toContain("SHORT SOAK");
    expect(confirm.mock.calls.at(-1)[0]).toContain("soaked 1h00m of 24h");
    expect(api).toHaveBeenLastCalledWith(
      "/api/repos/ai-on-gke/grain/pending-releases/v2.4.0/approve",
      { method: "POST", body: JSON.stringify({ short_soak_ok: true }) },
    );

    vi.spyOn(window, "confirm").mockReturnValueOnce(true);
    expect(await decideRelease(release, false)).toBe(true);
    expect(api).toHaveBeenLastCalledWith(
      "/api/repos/ai-on-gke/grain/pending-releases/v2.4.0/reject",
      { method: "POST" },
    );
  });

  it("read breaking-change warnings and promotion_blocked lines off the body", () => {
    const breakingNotice = {
      ...pending,
      subject: "release.pending:v3.2.0",
      body:
        "v3.2.0 is staged as v3.2.0-pending on ai-on-gke/grain and waits for a person.\n\n" +
        "BREAKING CHANGES: v3.2.0 carries breaking changes since v3.1.1 and is not the major release RELEASE_MAJOR asks for; approving it needs allow_breaking: true.\n\n" +
        "Qualification: PASS",
    };
    expect(pendingReleaseOf(breakingNotice)).toMatchObject({
      version: "v3.2.0",
      soak: null,
      shortSoak: null,
      breaking:
        "BREAKING CHANGES: v3.2.0 carries breaking changes since v3.1.1 and is not the major release RELEASE_MAJOR asks for; approving it needs allow_breaking: true.",
    });
    expect(notificationAlerts([breakingNotice])[0].actions[0].label).toBe(
      "Approve breaking",
    );

    const trainBlocked = {
      ...pending,
      subject: "release.pending:v3.2.0",
      body:
        "v3.2.0-rc.3 qualified, but the train does not release it itself.\n" +
        "promotion_blocked=it carries breaking changes since v3.1.1 and is not the major release RELEASE_MAJOR asks for",
    };
    expect(pendingReleaseOf(trainBlocked)).toMatchObject({
      breaking:
        "promotion_blocked=it carries breaking changes since v3.1.1 and is not the major release RELEASE_MAJOR asks for",
    });

    const majorBumpBlocked = {
      ...pending,
      subject: "release.pending:v4.0.0",
      body: "promotion_blocked=it is the first release of major 4, and a major bump is a person's to release",
    };
    expect(pendingReleaseOf(majorBumpBlocked).breaking).toBeNull();
  });

  it("approve breaking changes with allow_breaking, and combine with short_soak_ok when both apply", async () => {
    const breakingOnly = {
      repo: "ai-on-gke/grain",
      version: "v3.2.0",
      soak: null,
      shortSoak: null,
      breaking:
        "BREAKING CHANGES: v3.2.0 carries breaking changes since v3.1.1 and is not the major release RELEASE_MAJOR asks for; approving it needs allow_breaking: true.",
    };
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false);
    expect(await decideRelease(breakingOnly, true)).toBe(false);
    expect(api).not.toHaveBeenCalled();

    confirm.mockReturnValueOnce(true);
    expect(await decideRelease(breakingOnly, true)).toBe(true);
    expect(confirm.mock.calls.at(-1)[0]).toContain(
      "Approve v3.2.0 with BREAKING changes outside a major release?",
    );
    expect(confirm.mock.calls.at(-1)[0]).toContain(
      "v3.2.0 carries breaking changes since v3.1.1 and is not the major release RELEASE_MAJOR asks for",
    );
    expect(api).toHaveBeenLastCalledWith(
      "/api/repos/ai-on-gke/grain/pending-releases/v3.2.0/approve",
      { method: "POST", body: JSON.stringify({ allow_breaking: true }) },
    );

    confirm.mockReturnValueOnce(true);
    expect(await decideRelease(breakingOnly, false)).toBe(true);
    expect(api).toHaveBeenLastCalledWith(
      "/api/repos/ai-on-gke/grain/pending-releases/v3.2.0/reject",
      { method: "POST" },
    );

    const both = {
      ...breakingOnly,
      soak: "soaked 1h00m of 24h",
      shortSoak: "SHORT SOAK: under the 22h minimum.",
    };
    expect(
      notificationAlerts([
        {
          ...pending,
          subject: "release.pending:v3.2.0",
          body: `${both.shortSoak}\nSoak: ${both.soak}\n${both.breaking}`,
        },
      ])[0].actions[0].label,
    ).toBe("Approve short soak & breaking");
    confirm.mockReturnValueOnce(true);
    expect(await decideRelease(both, true)).toBe(true);
    expect(confirm.mock.calls.at(-1)[0]).toContain("SHORT SOAK");
    expect(confirm.mock.calls.at(-1)[0]).toContain(
      "BREAKING changes outside a major release",
    );
    expect(api).toHaveBeenLastCalledWith(
      "/api/repos/ai-on-gke/grain/pending-releases/v3.2.0/approve",
      {
        method: "POST",
        body: JSON.stringify({ short_soak_ok: true, allow_breaking: true }),
      },
    );
  });

  it("post a host-qualified repository's decision under its escaped owner", async () => {
    vi.spyOn(window, "confirm").mockReturnValueOnce(true);
    expect(
      await decideRelease(
        { repo: "git.forge.example/experimental/grain", version: "v2.4.0" },
        false,
      ),
    ).toBe(true);
    expect(api).toHaveBeenCalledWith(
      "/api/repos/git.forge.example%2Fexperimental/grain/pending-releases/v2.4.0/reject",
      { method: "POST" },
    );
  });
});
