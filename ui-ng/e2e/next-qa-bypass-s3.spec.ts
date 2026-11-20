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

import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "./fixtures";

test.use({ demoArgs: ["-as", "demo-operator"] });
test.describe.configure({ mode: "serial" });

test.afterEach(async ({ demoServer, accessCode }) => {
  await fetch(`${demoServer}/api/settings`, {
    method: "PUT",
    headers: {
      Authorization: `Bearer ${accessCode}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      qaEnabled: false,
      qaFlowId: "standard-workflow",
      qaRolloutPct: 0,
      qaShadowOnControl: true,
    }),
  });
});

test("Blocked S3 staging gate renders staging branch, operator bypass bar, accessible dialog, and submits override @phone @dark", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  const authHeaders = {
    Authorization: `Bearer ${accessCode}`,
    "Content-Type": "application/json",
  };

  // 1. Enable standard-workflow at 100% on the real demo server.
  const putSettings = await fetch(`${demoServer}/api/settings`, {
    method: "PUT",
    headers: authHeaders,
    body: JSON.stringify({
      qaEnabled: true,
      qaFlowId: "standard-workflow",
      qaRolloutPct: 100,
      qaShadowOnControl: false,
    }),
  });
  expect(putSettings.ok).toBe(true);

  // 2. Verify standard-workflow catalog in Processes -> Quality checks marks
  // repo-opinionated checks as warn while keeping test-integrity-guard blocking.
  await page.goto("/next/processes/quality?flow=standard-workflow");
  const expandBtn = page.getByTestId("qa-expand-all-stages");
  if (await expandBtn.isVisible()) {
    await expandBtn.click();
    await expect(
      page.getByTestId("qa-rule-card-golden:strict-config-decoder"),
    ).toContainText("warn");
    await expect(
      page.getByTestId("qa-rule-card-golden:ui-budget-and-spec-floor"),
    ).toContainText("warn");
    await expect(
      page.getByTestId("qa-rule-card-golden:test-integrity-guard"),
    ).toContainText("blocking");
  }

  // 3. Create a real task and seed a blocking S3 failure on its staging commit.
  const createRes = await fetch(`${demoServer}/api/tasks`, {
    method: "POST",
    headers: authHeaders,
    body: JSON.stringify({
      title: "Fix QA staging gate override flow",
      description: "Verify operator bypass of a blocked S3 staging gate.",
      repo: "acme/widgets",
      base: "main",
      approved: false,
    }),
  });
  expect(createRes.ok).toBe(true);
  const createdTask = (await createRes.json()) as { id: string };
  const taskId = String(createdTask.id);
  const headSha = "9f8e7d6c5b4a3210fedcba987654321098765432";

  const recordRes = await fetch(
    `${demoServer}/api/tasks/${taskId}/qa/check-runs`,
    {
      method: "POST",
      headers: authHeaders,
      body: JSON.stringify({
        entries: [
          {
            run: {
              runId: `${taskId}-1`,
              stage: "S3",
              checkId: "golden:test-integrity-guard",
              arm: "treatment",
              shadow: false,
              commitSha: headSha,
              verdict: "fail",
              durationMs: 14,
              allocBytes: 2048,
            },
            findings: [
              {
                stage: "S3",
                ruleId: "golden:test-integrity-guard",
                severity: "blocking",
                filePath: "pkg/demo/smoke_test.go",
                line: 24,
                summary: "TestSmoke contains zero assertions.",
                evidence: "func TestSmoke(t *testing.T) { runSmoke() }",
              },
            ],
          },
        ],
      }),
    },
  );
  expect(recordRes.ok).toBe(true);

  // Real backend computes s3Blocked=true, stageBranch=grain-qa-stage/<id>, and s3HeadSha.
  const qaInitial = (await (
    await fetch(`${demoServer}/api/tasks/${taskId}/qa`, {
      headers: authHeaders,
    })
  ).json()) as {
    s3Blocked?: boolean;
    stageBranch?: string;
    s3HeadSha?: string;
  };
  expect(qaInitial.s3Blocked).toBe(true);
  expect(qaInitial.stageBranch).toBe(`grain-qa-stage/${taskId}`);
  expect(qaInitial.s3HeadSha).toBe(headSha);

  // 4. Open Task Detail -> Changes & CI and assert layout & accessibility.
  await page.goto(`/next/task/${taskId}?tab=changes`);
  const qaSection = page.getByTestId("task-qa-section");
  const blockedBar = page.getByTestId("qa-s3-blocked-bar");
  const bypassBtn = page.getByTestId("qa-bypass-s3-btn");
  await expect(qaSection).toBeVisible();
  await expect(page.getByTestId("changes-stage-branch")).toContainText(
    `grain-qa-stage/${taskId}`,
  );
  await expect(blockedBar).toBeVisible();
  await expect(bypassBtn).toBeVisible();

  const viewport = page.viewportSize()!;
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);

  const qaBox = await qaSection.boundingBox();
  const barBox = await blockedBar.boundingBox();
  const btnBox = await bypassBtn.boundingBox();
  expect(qaBox).not.toBeNull();
  expect(barBox).not.toBeNull();
  expect(btnBox).not.toBeNull();
  expect(barBox!.x).toBeGreaterThanOrEqual(qaBox!.x - 1);
  expect(barBox!.x + barBox!.width).toBeLessThanOrEqual(
    qaBox!.x + qaBox!.width + 1,
  );
  expect(btnBox!.x).toBeGreaterThanOrEqual(barBox!.x - 1);
  expect(btnBox!.x + btnBox!.width).toBeLessThanOrEqual(
    barBox!.x + barBox!.width + 1,
  );
  expect(btnBox!.y).toBeGreaterThanOrEqual(barBox!.y - 1);
  expect(btnBox!.y + btnBox!.height).toBeLessThanOrEqual(
    barBox!.y + barBox!.height + 1,
  );

  const axeBlocked = await new AxeBuilder({ page })
    .include('[data-testid="task-qa-section"]')
    .analyze();
  expect(
    axeBlocked.violations.filter((v) =>
      ["serious", "critical"].includes(v.impact ?? ""),
    ),
  ).toEqual([]);

  // 5. Open confirmation dialog, verify viewport fit, Escape restore, and WCAG AA.
  await bypassBtn.click();
  const dialog = page.getByTestId("bypass-s3-dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText(`grain-qa-stage/${taskId}`);
  await expect(dialog).toContainText(headSha.slice(0, 12));
  await expect(page.getByTestId("bypass-s3-confirm")).toBeDisabled();

  const dialogBox = await dialog.boundingBox();
  expect(dialogBox).not.toBeNull();
  expect(dialogBox!.x).toBeGreaterThanOrEqual(0);
  expect(dialogBox!.y).toBeGreaterThanOrEqual(0);
  expect(dialogBox!.x + dialogBox!.width).toBeLessThanOrEqual(
    viewport.width + 1,
  );
  expect(dialogBox!.y + dialogBox!.height).toBeLessThanOrEqual(
    viewport.height + 1,
  );

  const axeDialog = await new AxeBuilder({ page })
    .include('[data-testid="bypass-s3-dialog"]')
    .analyze();
  expect(
    axeDialog.violations.filter((v) =>
      ["serious", "critical"].includes(v.impact ?? ""),
    ),
  ).toEqual([]);

  // Escape closes the modal without submitting.
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);

  // 6. Re-open, enter override reason, and verify POST /api/tasks/{id}/qa/bypass-s3.
  let postedBody: Record<string, unknown> | null = null;
  let bypassed = false;
  await page.route(`**/api/tasks/${taskId}/qa/bypass-s3`, async (route) => {
    postedBody = route.request().postDataJSON() as Record<string, unknown>;
    bypassed = true;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ok: true,
        pullRequest: "acme/widgets#99",
        branch: `grain/task-${taskId}`,
        stageBranch: `grain-qa-stage/${taskId}`,
        bypassedBy: "demo-operator",
      }),
    });
  });
  await page.route(`**/api/tasks/${taskId}/qa`, async (route) => {
    if (!bypassed) {
      await route.continue();
      return;
    }
    const upstream = await route.fetch();
    const data = (await upstream.json()) as {
      s3Blocked?: boolean;
      checkRuns?: unknown[];
    };
    data.s3Blocked = false;
    data.checkRuns = [
      ...(data.checkRuns ?? []),
      {
        id: 999,
        taskId,
        runId: "operator-bypass",
        commitSha: headSha,
        stage: "S3",
        checkId: "qa:s3-operator-bypass",
        arm: "treatment",
        shadow: false,
        verdict: "bypassed",
        durationMs: 0,
        tokensIn: 0,
        tokensOut: 0,
        allocBytes: 0,
        createdAt: new Date().toISOString(),
      },
    ];
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(data),
    });
  });

  await bypassBtn.click();
  await expect(dialog).toBeVisible();
  await page
    .getByTestId("bypass-s3-reason")
    .fill("False positive on generated fixture test; verified manually");
  await expect(page.getByTestId("bypass-s3-confirm")).toBeEnabled();
  await page.getByTestId("bypass-s3-confirm").click();

  await expect(dialog).toHaveCount(0);
  await expect(blockedBar).toHaveCount(0);
  expect(postedBody).toEqual({
    reason: "False positive on generated fixture test; verified manually",
    head: headSha,
  });
  await expect(qaSection).toContainText("qa:s3-operator-bypass");
  await expect(qaSection).toContainText("bypassed");
});
