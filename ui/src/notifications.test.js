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
});

describe("notificationSentence", () => {
  it("is the body's first line and how often it was raised", () => {
    expect(notificationSentence(open)).toBe(
      "Retried three times. Raised 2 times.",
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
  afterEach(() => vi.restoreAllMocks());

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
    });
  });

  it("fall back to grain's own repository when the notification names none", () => {
    const { repo: _repo, ...unnamed } = pending;
    expect(pendingReleaseOf(unnamed)).toEqual({
      repo: "ai-on-gke/grain",
      version: "v2.4.0",
    });
  });

  it("are only the release.pending family with its actions", () => {
    expect(pendingReleaseOf(open)).toBeNull();
    expect(pendingReleaseOf({ ...pending, actions: [] })).toBeNull();
    expect(
      pendingReleaseOf({ ...pending, subject: "release.pending:latest" }),
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
