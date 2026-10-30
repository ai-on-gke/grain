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

import { expect, test } from "./fixtures.js";
import { alertRow, openAlerts } from "./alerts.js";

// Release-from-trunk pending-release approval walkthrough
// (docs/test-plans/2026-10-28.md §6, docs/design/release-from-trunk.md §4).
//
// `grain demo -github-sim -fake-ci` seeds a pending-release notification
// (release.pending:v2.4.0) on GrainSourceRepo() and wires demoFakeCI so
// approving or rejecting from the Inbox or the Alerts bell starts
// .grain/workflows/promote-release.yml on main, clears the notification,
// and refuses any agent caller.
test.use({ demoArgs: ["-github-sim", "-fake-ci"] });
test.describe.configure({ mode: "serial" });
test.setTimeout(90_000);

const DESKTOP = { width: 1440, height: 900 };

test("pending releases: approved from Inbox and Alerts, rejected, and refused to an agent", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);

  const ok = async (res, what) => {
    expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
    return res.json();
  };
  const ciRuns = async () =>
    (await ok(await page.request.get("/api/ci/runs"), "ci runs")).runs;
  const notificationBySubject = async (subject) => {
    const list = await ok(
      await page.request.get("/api/notifications"),
      "notifications",
    );
    return list.find((n) => n.subject === subject);
  };
  const raisePendingRelease = async (version) => {
    await ok(
      await page.request.post("/api/notifications", {
        data: {
          subject: `release.pending:${version}`,
          severity: "warning",
          title: `${version} is ready to promote (${version}-rc.1 passed its soak)`,
          body: `${version}-rc.1 passed its 24h staging soak and is staged as ${version}-pending.`,
          repo: "ai-on-gke/grain",
          actions: ["approve-release", "reject-release"],
        },
      }),
      `raise ${version}`,
    );
  };

  // 1. Approve the seeded v2.4.0 pending release from the Inbox.
  await page.goto("/inbox");
  const section = page.getByTestId("inbox-notifications");
  await expect(section).toBeVisible();
  await section.locator(".inbox-group-toggle").click();
  const v240Row = section
    .getByTestId("inbox-notification")
    .filter({ hasText: "v2.4.0 is ready to promote" });
  await expect(v240Row).toBeVisible();

  let dialogMessage = "";
  page.once("dialog", (dialog) => {
    dialogMessage = dialog.message();
    dialog.accept();
  });
  await v240Row.getByRole("button", { name: "Approve" }).click();
  expect(dialogMessage).toContain("Approve v2.4.0?");
  await expect(v240Row).toHaveCount(0);

  let runs = await ciRuns();
  expect(runs).toHaveLength(1);
  expect(runs[0]).toMatchObject({
    id: "fake-ci-1",
    repo: "ai-on-gke/grain",
    ref: "main",
    event: "workflow_dispatch",
    jobs: [
      {
        jobId: ".grain/workflows/promote-release.yml:promote",
        workflowPath: ".grain/workflows/promote-release.yml",
      },
    ],
  });
  let n240 = await notificationBySubject("release.pending:v2.4.0");
  expect(n240?.clearedAt).toBeTruthy();
  expect(n240?.clearedBy).toMatch(/^human:/);

  // 2. Raise v2.5.0 and approve it from the Alerts bell.
  await raisePendingRelease("v2.5.0");
  await page.goto("/");
  await openAlerts(page);
  const v250Alert = alertRow(page, "v2.5.0 is ready to promote");
  await expect(v250Alert).toHaveCount(1);

  dialogMessage = "";
  page.once("dialog", (dialog) => {
    dialogMessage = dialog.message();
    dialog.accept();
  });
  await v250Alert.getByRole("button", { name: "Approve" }).click();
  expect(dialogMessage).toContain("Approve v2.5.0?");
  await expect(v250Alert).toHaveCount(0);

  runs = await ciRuns();
  expect(runs).toHaveLength(2);
  expect(runs[0]).toMatchObject({
    id: "fake-ci-2",
    repo: "ai-on-gke/grain",
    ref: "main",
    event: "workflow_dispatch",
    jobs: [
      {
        jobId: ".grain/workflows/promote-release.yml:promote",
        workflowPath: ".grain/workflows/promote-release.yml",
      },
    ],
  });
  let n250 = await notificationBySubject("release.pending:v2.5.0");
  expect(n250?.clearedAt).toBeTruthy();
  expect(n250?.clearedBy).toMatch(/^human:/);

  // 3. Raise v2.6.0 and reject it from the Inbox.
  await raisePendingRelease("v2.6.0");
  await page.goto("/inbox");
  const v260Row = section
    .getByTestId("inbox-notification")
    .filter({ hasText: "v2.6.0 is ready to promote" });
  await expect(v260Row).toBeVisible();

  dialogMessage = "";
  page.once("dialog", (dialog) => {
    dialogMessage = dialog.message();
    dialog.accept();
  });
  await v260Row.getByRole("button", { name: "Reject" }).click();
  expect(dialogMessage).toContain("Reject v2.6.0?");
  await expect(v260Row).toHaveCount(0);

  runs = await ciRuns();
  expect(runs).toHaveLength(3);
  expect(runs[0]).toMatchObject({
    id: "fake-ci-3",
    repo: "ai-on-gke/grain",
    ref: "main",
    event: "workflow_dispatch",
    jobs: [
      {
        jobId: ".grain/workflows/promote-release.yml:reject",
        workflowPath: ".grain/workflows/promote-release.yml",
      },
    ],
  });
  let n260 = await notificationBySubject("release.pending:v2.6.0");
  expect(n260?.clearedAt).toBeTruthy();
  expect(n260?.clearedBy).toMatch(/^human:/);

  // 4. Confirm an agent token is refused even when holding api:releases:write.
  await ok(
    await page.request.post("/api/tasks/1/capabilities", {
      data: { id: "api:releases:write", attach: true },
    }),
    "attach api:releases:write",
  );
  for (const verb of ["approve", "reject"]) {
    const res = await page.request.post(
      `/api/repos/ai-on-gke/grain/pending-releases/v2.7.0/${verb}`,
      {
        headers: { "X-Grain-Acting-Task": "1" },
      },
    );
    expect(res.status()).toBe(403);
    const body = await res.json();
    expect(body.error).toContain(`only a person may ${verb} a release`);
  }
  expect(await ciRuns()).toHaveLength(3);
});
