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
// (docs/test-plans/2026-10-28.md §6, design/design/release-from-trunk.design.md §4).
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
const PHONE = { width: 390, height: 844 };

test("pending releases: approved from Inbox and Alerts, rejected, and refused to an agent", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);

  const ok = async (res, what) => {
    expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
    return res.json();
  };
  const ciRuns = async () =>
    (
      await ok(
        await page.request.get("/api/ci/runs?repo=ai-on-gke%2Fgrain"),
        "ci runs",
      )
    ).runs;
  const notificationBySubject = async (subject) => {
    const list = await ok(
      await page.request.get("/api/notifications"),
      "notifications",
    );
    return list.find((n) => n.subject === subject);
  };
  const raisePendingRelease = async (version, body) => {
    await ok(
      await page.request.post("/api/notifications", {
        data: {
          subject: `release.pending:${version}`,
          severity: "warning",
          title: `${version} is ready to promote (${version}-rc.1 passed its soak)`,
          body:
            body ||
            `${version}-rc.1 passed its 24h staging soak and is staged as ${version}-pending.`,
          repo: "ai-on-gke/grain",
          actions: ["approve-release", "reject-release"],
        },
      }),
      `raise ${version}`,
    );
  };

  // 1. Approve the seeded v2.4.0 pending release from the Inbox ("Asked: approve a release").
  await page.goto("/inbox");
  const section = page.getByTestId("inbox-pending-releases");
  await expect(section).toBeVisible();
  await expect(section.locator(".inbox-group-toggle")).toHaveAttribute(
    "aria-expanded",
    "true",
  );
  const v240Row = section
    .getByTestId("inbox-pending-release")
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
    .getByTestId("inbox-pending-release")
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

  // 4. Raise v2.8.0 on a short soak: the Inbox says so beside its
  // Approve, and approving it asks about the short soak and accepts it
  // with short_soak_ok.
  await raisePendingRelease(
    "v2.8.0",
    "v2.8.0-rc.1 passed its 24h staging soak and is staged as v2.8.0-pending.\n\n" +
      "SHORT SOAK: under the 22h minimum; approving it needs short_soak_ok: true.\n\n" +
      "Soak: soaked 1h00m of 24h (FORCE=true): PASS",
  );
  await page.goto("/inbox");
  const v280Row = section
    .getByTestId("inbox-pending-release")
    .filter({ hasText: "v2.8.0 is ready to promote" });
  await expect(v280Row).toBeVisible();
  const soak = v280Row.getByTestId("inbox-release-soak");
  await expect(soak).toHaveText("SHORT SOAKsoaked 1h00m of 24h (FORCE=true)");
  const approveShort = v280Row.getByRole("button", {
    name: "Approve short soak",
  });
  const soakBox = await soak.boundingBox();
  const approveShortBox = await approveShort.boundingBox();
  expect(soakBox.x + soakBox.width).toBeLessThanOrEqual(approveShortBox.x);
  expect(
    Math.abs(
      soakBox.y +
        soakBox.height / 2 -
        (approveShortBox.y + approveShortBox.height / 2),
    ),
  ).toBeLessThanOrEqual(2);

  dialogMessage = "";
  page.once("dialog", (dialog) => {
    dialogMessage = dialog.message();
    dialog.accept();
  });
  const approval = page.waitForRequest((req) =>
    req.url().endsWith("/pending-releases/v2.8.0/approve"),
  );
  await approveShort.click();
  expect((await approval).postDataJSON()).toEqual({ short_soak_ok: true });
  expect(dialogMessage).toContain("Approve v2.8.0 on a SHORT SOAK?");
  expect(dialogMessage).toContain("soaked 1h00m of 24h (FORCE=true)");
  await expect(v280Row).toHaveCount(0);
  expect(await ciRuns()).toHaveLength(4);

  // 5. Raise a patch release (release.pending:v2.4.1) staged by the
  // qualification-driven train: the Inbox offers Approve and Reject in the
  // right-hand action slot with no soak chip, and Approve reaches
  // /pending-releases/v2.4.1/approve.
  await raisePendingRelease(
    "v2.4.1",
    "v2.4.1-rc.1 passed qualification (Stage 1 nightly.yml, Stage 2 grain/release-qualification) and is staged as v2.4.1-pending.\n\n" +
      "Qualification: PASS",
  );
  await page.goto("/inbox");
  const v241Row = section
    .getByTestId("inbox-pending-release")
    .filter({ hasText: "v2.4.1 is ready to promote" });
  await expect(v241Row).toBeVisible();
  await expect(v241Row.getByTestId("inbox-release-soak")).toHaveCount(0);
  const approvePatch = v241Row.getByRole("button", { name: "Approve" });
  const rejectPatch = v241Row.getByRole("button", { name: "Reject" });
  await expect(approvePatch).toBeVisible();
  await expect(rejectPatch).toBeVisible();

  const rowBox = await v241Row.boundingBox();
  const titleBox = await v241Row.locator(".inbox-gate").boundingBox();
  const approveBox = await approvePatch.boundingBox();
  const rejectBox = await rejectPatch.boundingBox();
  expect(titleBox.x + titleBox.width).toBeLessThanOrEqual(approveBox.x);
  expect(approveBox.x + approveBox.width).toBeLessThanOrEqual(rejectBox.x);
  expect(rejectBox.x + rejectBox.width).toBeLessThanOrEqual(
    rowBox.x + rowBox.width,
  );
  expect(
    Math.abs(
      approveBox.y +
        approveBox.height / 2 -
        (rejectBox.y + rejectBox.height / 2),
    ),
  ).toBeLessThanOrEqual(2);

  await page.setViewportSize(PHONE);
  await expect
    .poll(async () => {
      const phoneRowBox = await v241Row.boundingBox();
      const phoneApproveBox = await approvePatch.boundingBox();
      const phoneRejectBox = await rejectPatch.boundingBox();
      if (!phoneRowBox || !phoneApproveBox || !phoneRejectBox) return null;
      return {
        rowFitsViewport: phoneRowBox.width <= PHONE.width,
        approveInsideRow: phoneApproveBox.x >= phoneRowBox.x,
        approveBeforeReject:
          phoneApproveBox.x + phoneApproveBox.width <= phoneRejectBox.x,
        rejectInsideRow:
          phoneRejectBox.x + phoneRejectBox.width <=
          phoneRowBox.x + phoneRowBox.width,
      };
    })
    .toEqual({
      rowFitsViewport: true,
      approveInsideRow: true,
      approveBeforeReject: true,
      rejectInsideRow: true,
    });
  await page.setViewportSize(DESKTOP);

  dialogMessage = "";
  page.once("dialog", (dialog) => {
    dialogMessage = dialog.message();
    dialog.accept();
  });
  const patchApproval = page.waitForRequest((req) =>
    req.url().endsWith("/pending-releases/v2.4.1/approve"),
  );
  await approvePatch.click();
  const patchReq = await patchApproval;
  expect(patchReq.method()).toBe("POST");
  expect(patchReq.postData()).toBeNull();
  expect(dialogMessage).toContain("Approve v2.4.1?");
  await expect(v241Row).toHaveCount(0);

  runs = await ciRuns();
  expect(runs).toHaveLength(5);
  expect(runs[0]).toMatchObject({
    id: "fake-ci-5",
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
  let n241 = await notificationBySubject("release.pending:v2.4.1");
  expect(n241?.clearedAt).toBeTruthy();
  expect(n241?.clearedBy).toMatch(/^human:/);

  // 6. Confirm an agent token is refused even when holding api:releases:write.
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
  expect(await ciRuns()).toHaveLength(5);
});
