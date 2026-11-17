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

import fs from "node:fs/promises";
import path from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { test, expect } from "./fixtures.js";

// A named actor, so no screenshot or task payload carries the host account's user name.
test.use({ demoArgs: ["-as", "demo-operator"] });

test.afterEach(async ({ demoServer, accessCode }) => {
  await fetch(`${demoServer}/api/settings`, {
    method: "PUT",
    headers: {
      Authorization: `Bearer ${accessCode}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      qaEnabled: false,
      qaFlowId: "golden-v1-minimal",
      qaRolloutPct: 0,
      qaShadowOnControl: true,
    }),
  });
});

test("Telemetry QA rollout (A/B) and Pre-PR check latency cards render calmly in off and active states across desktop/phone + light/dark", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  const shotsDir = process.env.GRAIN_UI_SHOTS || "";
  if (shotsDir) {
    await fs.mkdir(shotsDir, { recursive: true });
  }
  const authHeaders = {
    Authorization: `Bearer ${accessCode}`,
    "Content-Type": "application/json",
  };

  // Sign in to the Angular app.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(`${demoServer}/next/telemetry`);
  const codeInput = page.locator('input[type="password"]');
  if (await codeInput.isVisible({ timeout: 3_000 }).catch(() => false)) {
    const emailInput = page.locator('input[type="email"], input[name="name"]');
    if (await emailInput.isVisible().catch(() => false)) {
      await emailInput.fill("qa-operator@example.com");
    }
    await codeInput.fill(accessCode);
    await page.locator('button[type="submit"]').click();
  }

  await expect(page.getByTestId("telemetry-page")).toBeVisible();

  // Verify Overview operational cards and QA digest card are visible
  await expect(page.getByTestId("telemetry-kpis")).toBeVisible();
  await expect(page.getByTestId("card-throughput")).toBeVisible();
  await expect(page.getByTestId("card-latency")).toBeVisible();
  await expect(page.getByTestId("card-outcomes")).toBeVisible();
  await expect(page.getByTestId("card-tools")).toBeVisible();
  await expect(page.getByTestId("telemetry-usage")).toBeVisible();
  const digestCard = page.getByTestId("qa-digest");
  await expect(digestCard).toBeVisible();
  await expect(digestCard).toContainText("Quality checks");
  await expect(page.getByTestId("qa-digest-open")).toBeVisible();

  // Click the digest card's link to enter ?tab=quality
  await page.getByTestId("qa-digest-open").click();

  await expect(page.getByTestId("card-qa-rollout")).toBeVisible();
  await expect(page.getByTestId("qa-rollout-pill")).toContainText("Off");

  await page.getByTestId("card-qa-rollout").scrollIntoViewIfNeeded();
  if (shotsDir) {
    await page.screenshot({
      path: path.join(shotsDir, "01-telemetry-qa-off-desktop-light.png"),
    });
  }

  // Enable 50% golden-v1-minimal and record an S3 check run + finding on an existing demo task.
  const putSettings = await fetch(`${demoServer}/api/settings`, {
    method: "PUT",
    headers: authHeaders,
    body: JSON.stringify({
      qaEnabled: true,
      qaFlowId: "golden-v1-minimal",
      qaRolloutPct: 50,
      qaShadowOnControl: true,
    }),
  });
  expect(putSettings.ok).toBe(true);

  const tasksRes = await fetch(`${demoServer}/api/tasks?limit=10`, {
    headers: authHeaders,
  });
  const tasksJson = await tasksRes.json();
  const tasks = Array.isArray(tasksJson) ? tasksJson : tasksJson.tasks || [];
  expect(tasks.length).toBeGreaterThan(0);
  const targetTask = tasks[0];

  const recRes = await fetch(
    `${demoServer}/api/tasks/${encodeURIComponent(targetTask.id)}/qa/check-runs`,
    {
      method: "POST",
      headers: authHeaders,
      body: JSON.stringify({
        entries: [
          {
            run: {
              commitSha: "a1b2c3d4e5f6",
              stage: "S3",
              checkId: "strict-config-decoder",
              arm: "treatment",
              shadow: false,
              verdict: "fail",
              durationMs: 19,
              allocBytes: 32768,
            },
            findings: [
              {
                stage: "S3",
                ruleId: "strict-config-decoder",
                severity: "blocking",
                filePath: "pkg/config/decode.go",
                line: 42,
                summary: "yaml.NewDecoder omits KnownFields(true)",
              },
            ],
          },
        ],
      }),
    },
  );
  expect(recRes.ok).toBe(true);

  // Refresh Telemetry and verify live check rule metrics + 50% rollout status pill.
  await page.getByTestId("telemetry-refresh").click();
  await expect(page.getByTestId("qa-rollout-pill")).toContainText(
    "50% · golden-v1-minimal",
  );
  await expect(page.getByTestId("qa-checks-table")).toContainText(
    "strict-config-decoder",
  );
  await expect(page.getByTestId("qa-checks-table")).toContainText("19ms");

  // Verify non-overlapping geometry of the two QA cards at desktop 1440x900.
  const boxRollout = await page.getByTestId("card-qa-rollout").boundingBox();
  const boxChecks = await page.getByTestId("card-qa-checks").boundingBox();
  expect(boxRollout).not.toBeNull();
  expect(boxChecks).not.toBeNull();
  const overlapsHoriz =
    boxRollout.x < boxChecks.x + boxChecks.width &&
    boxChecks.x < boxRollout.x + boxRollout.width;
  const overlapsVert =
    boxRollout.y < boxChecks.y + boxChecks.height &&
    boxChecks.y < boxRollout.y + boxRollout.height;
  expect(overlapsHoriz && overlapsVert).toBe(false);

  await page.getByTestId("card-qa-rollout").scrollIntoViewIfNeeded();
  if (shotsDir) {
    await page.screenshot({
      path: path.join(shotsDir, "02-telemetry-qa-active-desktop-light.png"),
    });
  }

  // Axe WCAG AA check in light mode.
  const axeLight = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa"])
    .analyze();
  expect(axeLight.violations).toEqual([]);

  // Switch to dark theme and verify WCAG AA + screenshot.
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-theme", "dark");
    localStorage.setItem("grain.theme", "dark");
  });
  await page.waitForTimeout(100);
  await page.getByTestId("card-qa-rollout").scrollIntoViewIfNeeded();
  if (shotsDir) {
    await page.screenshot({
      path: path.join(shotsDir, "03-telemetry-qa-active-desktop-dark.png"),
    });
  }
  const axeDark = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa"])
    .analyze();
  expect(axeDark.violations).toEqual([]);

  // Verify zero horizontal clipping inside card-qa-checks & card-qa-rollout across all 4 mandatory breakpoints.
  for (const vp of [
    { width: 2048, height: 1200, name: "wide" },
    { width: 1440, height: 900, name: "desktop" },
    { width: 1024, height: 860, name: "tablet" },
    { width: 390, height: 844, name: "phone" },
  ]) {
    await page.setViewportSize({ width: vp.width, height: vp.height });
    await page.waitForTimeout(80);
    await page.getByTestId("card-qa-checks").scrollIntoViewIfNeeded();
    const metrics = await page.evaluate(() => {
      const checks = document.querySelector('[data-testid="card-qa-checks"]');
      const rollout = document.querySelector('[data-testid="card-qa-rollout"]');
      const table = document.querySelector('[data-testid="qa-checks-table"]');
      return {
        docOverflow: document.documentElement.scrollWidth > window.innerWidth,
        checksOverflow: checks
          ? checks.scrollWidth > checks.clientWidth + 1
          : false,
        rolloutOverflow: rollout
          ? rollout.scrollWidth > rollout.clientWidth + 1
          : false,
        tableOverflow: table
          ? table.scrollWidth > table.clientWidth + 1
          : false,
      };
    });
    expect(metrics, `viewport ${vp.name} (${vp.width}x${vp.height})`).toEqual({
      docOverflow: false,
      checksOverflow: false,
      rolloutOverflow: false,
      tableOverflow: false,
    });
  }
  if (shotsDir) {
    await page.screenshot({
      path: path.join(shotsDir, "04-telemetry-qa-active-phone-dark.png"),
    });
  }
});
