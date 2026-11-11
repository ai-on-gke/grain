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

import type { Notification, Task } from "../api/models";
import {
  buildEntries,
  classifyNotification,
  classifyTask,
  groupEntries,
  matchesFilter,
  matchesQuery,
  pendingReleaseOf,
  releaseConfirm,
  waitsOnUser,
} from "./inbox-classifier";

const task = (over: Partial<Task>): Task => ({
  id: "1",
  title: "A task",
  state: "running",
  ...over,
});

describe("inbox classifier", () => {
  it("admits only tasks waiting on a person, as the React Inbox does", () => {
    expect(waitsOnUser(task({ state: "running" }))).toBe(false);
    expect(waitsOnUser(task({ state: "awaiting_reply" }))).toBe(true);
    expect(waitsOnUser(task({ state: "proposed" }))).toBe(true);
    expect(waitsOnUser(task({ state: "failed" }))).toBe(true);
    expect(
      waitsOnUser(task({ state: "awaiting_reply", consoleSession: {} })),
    ).toBe(false);
    // A running task is never offered Submit, even with an open pull
    // request and no auto-merge (the classic UI lists it): its agent may
    // still be pushing to the branch.
    expect(
      waitsOnUser(
        task({ state: "running", pullRequest: { url: "https://x/pr/1" } }),
      ),
    ).toBe(false);
    expect(
      waitsOnUser(
        task({
          state: "running",
          autoMerge: true,
          pullRequest: { url: "https://x/pr/1" },
        }),
      ),
    ).toBe(false);
  });

  it("files each task under one group with the actions its group offers", () => {
    const reply = classifyTask(
      task({ id: "4", state: "awaiting_reply", questionSummary: "Fix both?" }),
    );
    expect(reply?.groupId).toBe("reply");
    expect(reply?.summary).toBe("Fix both?");
    expect(reply?.actions.map((a) => a.id)).toEqual(["acknowledge"]);

    const access = classifyTask(
      task({ state: "awaiting_reply", awaitingAccess: true }),
    );
    expect(access?.groupId).toBe("access");

    const proposal = classifyTask(task({ id: "9", state: "proposed" }));
    expect(proposal?.groupId).toBe("proposal");
    expect(proposal?.actions.map((a) => a.id)).toEqual([
      "approve",
      "defer",
      "decline",
    ]);
    expect(proposal?.actions[0].route).toBe("POST /api/tasks/{id}/approve");
    expect(proposal?.actions[0].params).toEqual({ id: "9" });
    expect(proposal?.actions[2].confirm).toContain("Decline #9");
    expect(proposal?.taskLink).toBe("/task/9");

    const failed = classifyTask(task({ state: "failed" }));
    expect(failed?.actions.map((a) => a.id)).toEqual(["retry", "close"]);

    const submit = classifyTask(
      task({
        state: "awaiting_submit",
        pullRequest: { url: "https://x/pr/2" },
      }),
    );
    expect(submit?.groupId).toBe("submit");
    expect(submit?.externalLink).toBe("https://x/pr/2");

    const base = classifyTask(
      task({
        state: "awaiting_submit",
        redBaseBranch: "main",
        pullRequest: { url: "https://x" },
      }),
    );
    expect(base?.groupId).toBe("base-repair");

    expect(classifyTask(task({ state: "running" }))).toBeNull();
  });

  it("reads a pending release, its soak, breaking changes and a failed dispatch", () => {
    const n: Notification = {
      id: 7,
      subject: "release.pending:v2.4.0",
      actions: ["approve-release"],
      repo: "acme/widgets",
      body: [
        "DISPATCH FAILED (123): the approval key is missing",
        "Remedy: set GRAIN_RELEASE_APPROVAL_KEY",
        "Run URL: https://ci.example/runs/123",
        "v2.4.0-rc.2 passed its soak.",
        "Soak: soaked 1h00m of 24h (FORCE=true)",
        "SHORT SOAK: under the minimum",
        "BREAKING CHANGES: drops the v1 API",
      ].join("\n"),
    };
    const r = pendingReleaseOf(n);
    expect(r).toMatchObject({
      repo: "acme/widgets",
      version: "v2.4.0",
      soak: "soaked 1h00m of 24h (FORCE=true)",
      shortSoak: "SHORT SOAK: under the minimum",
      breaking: "BREAKING CHANGES: drops the v1 API",
    });
    expect(r?.dispatchFailure).toMatchObject({
      runId: "123",
      needsApprovalKey: true,
      runUrl: "https://ci.example/runs/123",
    });
    const e = classifyNotification(n);
    expect(e.groupId).toBe("release");
    expect(e.summary).toBe("v2.4.0-rc.2 passed its soak.");
    expect(releaseConfirm(r!, true, true, true)).toContain(
      "SHORT SOAK and with BREAKING",
    );
    expect(releaseConfirm(r!, false, false, false)).toContain("Reject v2.4.0?");
    expect(
      pendingReleaseOf({ id: 1, subject: "release.pending:v2.4.1" }),
    ).toBeNull();
  });

  it("offers acknowledge, investigate and clear on an alert", () => {
    const e = classifyNotification({
      id: 3,
      subject: "ci.red:main",
      severity: "warning",
      body: "Main is red",
    });
    expect(e.groupId).toBe("notification");
    expect(e.actions.map((a) => a.id)).toEqual(["ack", "investigate", "clear"]);
    const acked = classifyNotification({
      id: 3,
      subject: "x",
      ackedAt: "2026-01-01T00:00:00Z",
    });
    expect(acked.acked).toBe(true);
    expect(acked.actions.map((a) => a.id)).toEqual(["investigate", "clear"]);
  });

  it("groups in urgency order and filters by kind, query and read state", () => {
    const entries = buildEntries(
      [
        task({ id: "1", state: "proposed", title: "Bump Go" }),
        task({ id: "2", state: "awaiting_reply", title: "Stall" }),
      ],
      [{ id: 5, subject: "alert" }],
      [{ id: "a1", taskId: "2", script: "deploy.sh", args: ["--prod"] }],
    );
    expect(groupEntries(entries).map((g) => g.id)).toEqual([
      "reply",
      "host-script",
      "proposal",
      "notification",
    ]);
    const seen = new Set(["task:1:proposal"]);
    expect(
      entries.filter((e) => matchesFilter(e, "proposals", seen)),
    ).toHaveLength(1);
    expect(
      entries.filter((e) => matchesFilter(e, "unread", seen)),
    ).toHaveLength(3);
    expect(entries.filter((e) => matchesQuery(e, "bump"))).toHaveLength(1);
    expect(entries.find((e) => e.kind === "host-script")?.summary).toBe(
      "deploy.sh --prod",
    );
  });
});
