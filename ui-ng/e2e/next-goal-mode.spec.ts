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

import { expect, test } from "./fixtures";

test.use({ demoArgs: ["-as", "demo-operator"] });

test.describe("Autonomous Goal Mode (/next/)", () => {
  test("launches Prototype a goal from + Create, renders Goal Scorecard & badges, opens Revise, and switches to supervised @phone @dark", async ({
    page,
    axe,
  }) => {
    await page.goto("/next/inbox");
    await expect(page.getByTestId("next-inbox-headline")).toBeVisible();

    // 1. Open + Create menu (or phone More sheet) and select "Prototype a goal".
    const railMenu = page.getByTestId("rail-create-menu");
    if (await railMenu.isVisible()) {
      await railMenu.click();
    } else {
      await page.getByTestId("compact-more").click();
    }
    const createGoalItem = page.getByTestId("create-prototype-goal");
    await expect(createGoalItem).toBeVisible();
    await expect(createGoalItem).toContainText("Prototype a goal");
    await createGoalItem.click();

    // 2. Verify NewTaskSheet opens in Autonomous goal posture.
    const sheet = page.getByTestId("new-task-sheet");
    await expect(sheet).toBeVisible();
    await expect(page.getByTestId("nt-posture")).toContainText(
      "Autonomous goal",
    );
    const goalConfig = page.getByTestId("nt-goal-config");
    await expect(goalConfig).toBeVisible();
    await expect(page.getByTestId("nt-max-passes")).toHaveValue("8");
    await expect(page.getByTestId("nt-wall-clock")).toHaveValue("120");
    await expect(page.getByTestId("nt-mock-secrets")).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await expect(page.getByTestId("new-task-submit")).toContainText(
      "Start autonomous goal",
    );
    expect(await axe()).toEqual([]);

    // 3. Customize budget, verify .sheet-body scrolls cleanly clear of .sheet-foot,
    // and file the autonomous goal task against grain demo.
    await page.getByTestId("nt-max-passes").fill("6");
    await page.getByTestId("nt-wall-clock").fill("90");
    await page
      .getByTestId("nt-title")
      .fill("Prototype OAuth2 PKCE device flow");
    await page
      .getByTestId("nt-description")
      .fill("Implement and verify PKCE device code polling end to end.");

    const sheetBody = sheet.locator(".sheet-body");
    const sheetFoot = sheet.locator(".sheet-foot");
    await sheetBody.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    const bodyBox = await sheetBody.boundingBox();
    const footBox = await sheetFoot.boundingBox();
    const moreBtnBox = await page.getByTestId("more-options").boundingBox();
    expect(bodyBox).not.toBeNull();
    expect(footBox).not.toBeNull();
    expect(moreBtnBox).not.toBeNull();
    expect(bodyBox!.y + bodyBox!.height).toBeLessThanOrEqual(footBox!.y + 1);
    expect(moreBtnBox!.y + moreBtnBox!.height).toBeLessThanOrEqual(
      footBox!.y - 16,
    );

    const createReqPromise = page.waitForRequest(
      (req) =>
        req.method() === "POST" && new URL(req.url()).pathname === "/api/tasks",
    );
    const createResPromise = page.waitForResponse(
      (res) =>
        res.request().method() === "POST" &&
        new URL(res.url()).pathname === "/api/tasks",
    );
    await page.getByTestId("new-task-submit").click();

    const createReq = await createReqPromise;
    const createPayload = createReq.postDataJSON() as Record<string, unknown>;
    expect(createPayload).toMatchObject({
      title: "Prototype OAuth2 PKCE device flow",
      autoApproveProposals: true,
      goalMode: {
        enabled: true,
        maxPasses: 6,
        wallClockMinutes: 90,
        mockMissingSecrets: true,
      },
    });

    const createRes = await createResPromise;
    expect(createRes.ok()).toBe(true);
    const createdTask = (await createRes.json()) as { id: string };
    const taskId = createdTask.id;
    await expect(sheet).toBeHidden();

    // Verify the active snackbar does not intersect the bottom status bar or Shortcuts button.
    const snackbar = page.getByTestId("snackbar");
    await expect(snackbar).toBeVisible();
    const snackBox = await snackbar.boundingBox();
    const bottomBar = page.locator(".bottom-bar");
    const shortcutsBtn = page.getByTestId("shortcuts-toggle");
    expect(snackBox).not.toBeNull();
    if (await bottomBar.isVisible()) {
      const barBox = await bottomBar.boundingBox();
      const shortBox = await shortcutsBtn.boundingBox();
      expect(barBox).not.toBeNull();
      expect(shortBox).not.toBeNull();
      expect(snackBox!.y + snackBox!.height).toBeLessThanOrEqual(barBox!.y - 8);
      expect(snackBox!.y + snackBox!.height).toBeLessThanOrEqual(
        shortBox!.y - 8,
      );
    }

    // 4. Enrich GET /api/tasks/{id} with recorded assumptions & verified scorecard
    // so we can verify the full Goal Scorecard, Revise loop, and Switch to supervised.
    let switchedToSupervised = false;
    await page.route(`**/api/tasks/${taskId}*`, async (route) => {
      if (route.request().method() === "PATCH") {
        const patchBody = route.request().postDataJSON() as {
          goalMode?: { enabled?: boolean };
        } | null;
        if (patchBody?.goalMode?.enabled === false) {
          switchedToSupervised = true;
        }
        await route.continue();
        return;
      }
      if (route.request().method() !== "GET") {
        await route.continue();
        return;
      }
      const reqHeaders = { ...route.request().headers() };
      delete reqHeaders["if-none-match"];
      const response = await route.fetch({ headers: reqHeaders });
      const body = (await response.json()) as Record<string, unknown>;
      body["goalMode"] = switchedToSupervised
        ? { enabled: false }
        : {
            enabled: true,
            maxPasses: 6,
            wallClockMinutes: 90,
            mockMissingSecrets: true,
          };
      body["goalAssumptions"] = [
        {
          id: "asmp-1",
          topic: "Token persistence",
          decision: "SQLite encrypted session table",
          rationale:
            "Keeps local prototype self-contained without external KMS",
          alternatives: "In-memory store",
          pass: 1,
        },
      ];
      body["goalScorecard"] = {
        passesUsed: 2,
        maxPasses: 6,
        verified: true,
        summary: "PKCE token exchange and polling verified across 2 passes.",
        deliverables: [
          {
            requirement: "PKCE challenge generator and verifier",
            evidence: "pkg/auth/pkce_test.go (PASS)",
            verified: true,
          },
          {
            requirement: "Device token polling handler",
            evidence: "pkg/auth/device_poll_test.go (PASS)",
            verified: true,
          },
        ],
      };
      const resHeaders = { ...response.headers() };
      delete resHeaders["etag"];
      await route.fulfill({ response, headers: resHeaders, json: body });
    });

    // 5. Verify Goal badge on /next/tasks and <g-task-goal-card> on /next/task/:id.
    await page.goto("/next/tasks");
    await expect(page.getByTestId(`goal-badge-${taskId}`)).toBeVisible();
    await page.goto(`/next/task/${taskId}`);
    const goalCard = page.getByTestId("task-goal-card");
    await expect(goalCard).toBeVisible();
    await expect(page.getByTestId("goal-status-pill")).toContainText(
      "Goal verified · 2 passes",
    );
    await expect(page.getByTestId("goal-deliverable")).toHaveCount(2);
    await expect(page.getByTestId("goal-assumption")).toHaveCount(1);
    await expect(page.getByTestId("goal-assumptions")).toContainText(
      "Token persistence:",
    );
    expect(await axe()).toEqual([]);

    // 6. Click Revise… and verify NewTaskSheet opens pre-filled for follow-up.
    await page.getByTestId("act-revise-goal").click();
    await expect(sheet).toBeVisible();
    await expect(page.getByTestId("nt-title")).toHaveValue(
      "Revise: Prototype OAuth2 PKCE device flow",
    );
    await expect(page.getByTestId("nt-posture")).toContainText(
      "Autonomous goal",
    );
    await page.getByTestId("new-task-close").click();
    await expect(sheet).toBeHidden();

    // 7. Click Switch to supervised and verify PATCH /api/tasks/{id}.
    const patchReqPromise = page.waitForRequest(
      (req) =>
        req.method() === "PATCH" &&
        new URL(req.url()).pathname === `/api/tasks/${taskId}`,
    );
    await page.getByTestId("act-switch-supervised").click();
    const patchReq = await patchReqPromise;
    expect(patchReq.postDataJSON()).toEqual({ goalMode: { enabled: false } });
    await expect(page.getByTestId("act-switch-supervised")).toBeHidden();
  });
});
