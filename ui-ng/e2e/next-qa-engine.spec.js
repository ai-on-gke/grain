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
      qaFlowId: "golden-v1-minimal",
      qaRolloutPct: 0,
      qaShadowOnControl: true,
    }),
  });
});

test("Processes -> Quality checks and Task Detail -> Pre-PR quality checks work end to end and pass WCAG AA", async ({
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

  // 1. Enable 50% golden-v1-minimal rollout on the demo server.
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

  // 2. Inspect /next/processes/quality in light/dark + desktop/phone.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/next/processes/quality");

  const goldenRow = page.getByTestId("qa-flow-row-golden-v1-minimal");
  const detRow = page.getByTestId("qa-flow-row-deterministic-s3-only");
  const archivedToggle = page.getByTestId("qa-toggle-archived-flows");
  await expect(goldenRow).toBeVisible();
  await expect(detRow).toBeHidden();
  await expect(goldenRow).toContainText("Active (50%)");

  await archivedToggle.click();
  await expect(detRow).toBeVisible();
  await expect(detRow).toContainText("Available");

  const inspTitle = page.getByTestId("qa-flow-insp-title");
  const detailCard = page.getByTestId("qa-flow-detail-card");
  await expect(inspTitle).toContainText("Recommended baseline");
  await expect(page.getByTestId("qa-mobile-back")).toBeHidden();

  // All stages start collapsed by default; expanding reveals stage substeps & rule hooks.
  await expect(
    page.getByTestId("qa-rule-card-golden:test-integrity-guard"),
  ).toBeHidden();
  await page.getByTestId("qa-expand-all-stages").click();
  await expect(
    page.getByTestId("qa-rule-card-golden:test-integrity-guard"),
  ).toBeVisible();
  await expect(
    page.getByTestId("qa-rule-card-golden:s4-orthogonal-verifier"),
  ).toBeVisible();

  if (shotsDir) {
    await page.screenshot({
      path: path.join(shotsDir, "m2-processes-quality-desktop-light.png"),
    });
  }

  const axeProcLight = await new AxeBuilder({ page })
    .include('[data-testid="proc-inspector"]')
    .include('[data-testid="quality-flows-list"]')
    .analyze();
  expect(axeProcLight.violations).toEqual([]);

  await detRow.click();
  await expect(inspTitle).toContainText("Deterministic checks only");
  await expect(detailCard).toContainText("disabled");
  await goldenRow.click();
  await expect(inspTitle).toContainText("Recommended baseline");
  // Browser Back twice: first back to ?flow=deterministic-s3-only, then back to bare /next/processes/quality.
  await page.goBack();
  await expect(inspTitle).toContainText("Deterministic checks only");
  await page.goBack();
  await expect(inspTitle).toContainText("Recommended baseline");

  // 3. Create a real task via POST /api/tasks and record a Pre-PR S3 check run + finding.
  const createTaskRes = await fetch(`${demoServer}/api/tasks`, {
    method: "POST",
    headers: authHeaders,
    body: JSON.stringify({
      title: "Add strict YAML config parser for scheduler",
      description: "Implement strict YAML decoding with unit tests.",
      repo: "acme/widgets",
      base: "main",
      approved: false,
    }),
  });
  expect(createTaskRes.ok).toBe(true);
  const createdTask = await createTaskRes.json();
  const taskId = String(createdTask.id);

  const recordRunRes = await fetch(
    `${demoServer}/api/tasks/${taskId}/qa/check-runs`,
    {
      method: "POST",
      headers: authHeaders,
      body: JSON.stringify({
        entries: [
          {
            run: {
              runId: "run-1",
              stage: "S3",
              checkId: "golden:strict-config-decoder",
              arm: "treatment",
              shadow: false,
              commitSha: "9f8e7d6c5b4a3210",
              verdict: "fail",
              durationMs: 14,
              allocBytes: 4096,
            },
            findings: [
              {
                stage: "S3",
                ruleId: "golden:strict-config-decoder",
                severity: "blocking",
                filePath: "pkg/sched/config.go",
                line: 42,
                summary:
                  "yaml.NewDecoder in pkg/sched/config.go omits KnownFields(true), allowing typos in config keys to pass silently.",
                evidence:
                  "dec := yaml.NewDecoder(r)\n// missing dec.KnownFields(true)\nreturn dec.Decode(&cfg)",
              },
            ],
          },
        ],
      }),
    },
  );
  expect(recordRunRes.ok).toBe(true);

  const qaBefore = await (
    await fetch(`${demoServer}/api/tasks/${taskId}/qa`, {
      headers: authHeaders,
    })
  ).json();
  expect(qaBefore.findings.length).toBe(1);
  const findingId = qaBefore.findings[0].id;

  // 4. Open Task Detail -> Changes & CI and verify Pre-PR quality checks card + voting.
  await page.goto(`/next/task/${taskId}?tab=changes`);
  const qaSection = page.getByTestId("task-qa-section");
  const ciSection = page.getByTestId("task-ci-section");
  await expect(qaSection).toBeVisible();
  await expect(ciSection).toBeVisible();

  // Bounding-box check: Pre-PR quality checks appears cleanly above CI checks & runs without overlap.
  const qaBox = await qaSection.boundingBox();
  const ciBox = await ciSection.boundingBox();
  expect(qaBox).not.toBeNull();
  expect(ciBox).not.toBeNull();
  expect(qaBox.y + qaBox.height).toBeLessThanOrEqual(ciBox.y + 1);

  // Expand evidence details and verify containment, aria-expanded, and tight button width.
  const detailsBtn = page.getByTestId(`qa-evidence-btn-${findingId}`);
  await expect(detailsBtn).toHaveText("Show details");
  await expect(detailsBtn).toHaveAttribute("aria-expanded", "false");
  const btnBox = await detailsBtn.boundingBox();
  expect(btnBox).not.toBeNull();
  expect(btnBox.width).toBeLessThan(200);
  await detailsBtn.click();
  await expect(detailsBtn).toHaveText("Hide details");
  await expect(detailsBtn).toHaveAttribute("aria-expanded", "true");
  await expect(qaSection).toContainText("missing dec.KnownFields(true)");

  // Verify host $USER never leaks into rendered task text.
  const hostUser = process.env.USER || "";
  if (hostUser && hostUser !== "demo-operator" && hostUser !== "root") {
    const bodyText = await page.evaluate(() => document.body.innerText);
    expect(bodyText.toLowerCase()).not.toContain(hostUser.toLowerCase());
  }

  // Vote Helpful -> persists to backend; vote Not helpful -> persists -1.
  const upBtn = page.getByTestId(`qa-vote-up-${findingId}`);
  const downBtn = page.getByTestId(`qa-vote-down-${findingId}`);
  await expect(upBtn).toHaveAttribute("aria-pressed", "false");
  await upBtn.click();
  await expect(upBtn).toHaveAttribute("aria-pressed", "true");

  const qaAfterUp = await (
    await fetch(`${demoServer}/api/tasks/${taskId}/qa`, {
      headers: authHeaders,
    })
  ).json();
  expect(qaAfterUp.findings[0].humanVote).toBe(1);

  await downBtn.click();
  await expect(downBtn).toHaveAttribute("aria-pressed", "true");
  await expect(upBtn).toHaveAttribute("aria-pressed", "false");

  const qaAfterDown = await (
    await fetch(`${demoServer}/api/tasks/${taskId}/qa`, {
      headers: authHeaders,
    })
  ).json();
  expect(qaAfterDown.findings[0].humanVote).toBe(-1);

  if (shotsDir) {
    await page.screenshot({
      path: path.join(shotsDir, "m2-task-qa-desktop-light.png"),
    });
  }

  const axeTaskLight = await new AxeBuilder({ page })
    .include('[data-testid="task-qa-section"]')
    .analyze();
  expect(
    axeTaskLight.violations.filter((v) =>
      ["serious", "critical"].includes(v.impact ?? ""),
    ),
  ).toEqual([]);

  // 5. Dark mode + 390x844 phone viewport checks across both surfaces.
  await page.evaluate(() => localStorage.setItem("grain.themeMode", "dark"));
  await page.reload();
  await expect(page.getByTestId("task-qa-section")).toBeVisible();
  if (shotsDir) {
    await page.screenshot({
      path: path.join(shotsDir, "m2-task-qa-desktop-dark.png"),
    });
  }
  const axeTaskDark = await new AxeBuilder({ page })
    .include('[data-testid="task-qa-section"]')
    .analyze();
  expect(
    axeTaskDark.violations.filter((v) =>
      ["serious", "critical"].includes(v.impact ?? ""),
    ),
  ).toEqual([]);

  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(200);
  const phoneOverflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(phoneOverflow).toBeLessThanOrEqual(1);
  if (shotsDir) {
    await page.screenshot({
      path: path.join(shotsDir, "m2-task-qa-phone-dark.png"),
    });
  }

  await page.goto("/next/processes/quality");
  await expect(page.getByTestId("qa-flow-row-golden-v1-minimal")).toBeVisible();
  await page.getByTestId("qa-flow-row-golden-v1-minimal").click();
  await expect(page.getByTestId("qa-mobile-back")).toBeVisible();
  if (shotsDir) {
    await page.screenshot({
      path: path.join(
        shotsDir,
        "m2-processes-quality-phone-inspector-dark.png",
      ),
    });
  }
  await page.getByTestId("qa-mobile-back").click();
  await expect(page.getByTestId("qa-flow-row-golden-v1-minimal")).toBeVisible();
  const procPhoneOverflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(procPhoneOverflow).toBeLessThanOrEqual(1);
  if (shotsDir) {
    await page.screenshot({
      path: path.join(shotsDir, "m2-processes-quality-phone-dark.png"),
    });
    // Also capture 1024x860 tablet and 2048x1200 wide viewports.
    await page.setViewportSize({ width: 1024, height: 860 });
    await page.waitForTimeout(150);
    await page.screenshot({
      path: path.join(shotsDir, "m2-processes-quality-tablet-dark.png"),
    });
    await page.setViewportSize({ width: 2048, height: 1200 });
    await page.evaluate(() => localStorage.setItem("grain.themeMode", "light"));
    await page.reload();
    await expect(
      page.getByTestId("qa-flow-row-golden-v1-minimal"),
    ).toBeVisible();
    await page.screenshot({
      path: path.join(shotsDir, "m2-processes-quality-wide-light.png"),
    });
  }

  // 6. Verify prefers-reduced-motion zeroes transitions on interactive rows and buttons.
  await page.emulateMedia({ reducedMotion: "reduce" });
  const procRowTransition = await page
    .getByTestId("qa-flow-row-golden-v1-minimal")
    .evaluate((el) => getComputedStyle(el).transitionDuration);
  expect(procRowTransition).toBe("0s");
});

test("/next/processes/quality syncs selected flow to ?flow= URL and preserves selection across Back & reload", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const authHeaders = {
    Authorization: `Bearer ${accessCode}`,
    "Content-Type": "application/json",
  };
  await fetch(`${demoServer}/api/settings`, {
    method: "PUT",
    headers: authHeaders,
    body: JSON.stringify({
      qaEnabled: true,
      qaFlowId: "golden-v1-minimal",
      qaRolloutPct: 50,
      qaShadowOnControl: true,
    }),
  });

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/next/processes/quality?flow=deterministic-s3-only");
  await expect(page.getByTestId("qa-toggle-archived-flows")).toHaveAttribute(
    "aria-expanded",
    "true",
  );
  await expect(
    page.getByTestId("qa-flow-row-deterministic-s3-only"),
  ).toBeVisible();
  await expect(page.getByTestId("qa-flow-insp-title")).toContainText(
    "Deterministic checks only",
  );

  await page.getByTestId("qa-flow-row-repo-custom").click();
  await expect(page).toHaveURL(/flow=repo-custom/);
  await page.getByTestId("qa-configure-rollout-link").click();
  await expect(page).toHaveURL(/\/next\/settings\/tasks/);
  await page.goBack();
  await expect(page.getByTestId("qa-flow-insp-title")).toContainText(
    "Repository custom (.grain/qa-flow.yml)",
  );
});

test("Pre-PR quality checks card stays hidden on closed/merged tasks with no QA assignment or runs", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const authHeaders = {
    Authorization: `Bearer ${accessCode}`,
    "Content-Type": "application/json",
  };
  await fetch(`${demoServer}/api/settings`, {
    method: "PUT",
    headers: authHeaders,
    body: JSON.stringify({
      qaEnabled: true,
      qaFlowId: "golden-v1-minimal",
      qaRolloutPct: 50,
      qaShadowOnControl: true,
    }),
  });

  await page.goto("/next/task/10?tab=changes");
  await expect(page.getByTestId("task-ci-section")).toBeVisible();
  await expect(page.getByTestId("task-qa-section")).toHaveCount(0);
});

test("TaskDetail Pre-PR quality checks live-refreshes in-turn check-runs without collapsing open evidence or stealing focus", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const authHeaders = {
    Authorization: `Bearer ${accessCode}`,
    "Content-Type": "application/json",
  };
  await fetch(`${demoServer}/api/settings`, {
    method: "PUT",
    headers: authHeaders,
    body: JSON.stringify({
      qaEnabled: true,
      qaFlowId: "golden-v1-minimal",
      qaRolloutPct: 50,
      qaShadowOnControl: true,
    }),
  });

  const created = await (
    await fetch(`${demoServer}/api/tasks`, {
      method: "POST",
      headers: authHeaders,
      body: JSON.stringify({
        title: "Live QA SSE update test",
        description: "Verify in-place live QA refresh.",
        repo: "acme/widgets",
        base: "main",
        approved: false,
      }),
    })
  ).json();
  const taskId = String(created.id);

  await fetch(`${demoServer}/api/tasks/${taskId}/qa/check-runs`, {
    method: "POST",
    headers: authHeaders,
    body: JSON.stringify({
      entries: [
        {
          run: {
            runId: `${taskId}-1`,
            stage: "S3",
            checkId: "golden:strict-config-decoder",
            arm: "treatment",
            commitSha: "111122223333",
            verdict: "fail",
            durationMs: 12,
          },
          findings: [
            {
              stage: "S3",
              ruleId: "golden:strict-config-decoder",
              severity: "blocking",
              filePath: "pkg/cfg/load.go",
              line: 19,
              summary: "Missing KnownFields(true).",
              evidence: "dec := yaml.NewDecoder(r)",
            },
          ],
        },
      ],
    }),
  });
  const qa1 = await (
    await fetch(`${demoServer}/api/tasks/${taskId}/qa`, {
      headers: authHeaders,
    })
  ).json();
  const finding1Id = qa1.findings[0].id;

  await page.goto(`/next/task/${taskId}?tab=changes`);
  await page.getByTestId(`qa-evidence-btn-${finding1Id}`).click();
  await expect(
    page.locator('[data-testid="task-qa-section"] pre.ci-step-log'),
  ).toBeVisible();
  const upBtn = page.getByTestId(`qa-vote-up-${finding1Id}`);
  await upBtn.focus();

  await fetch(`${demoServer}/api/tasks/${taskId}/qa/check-runs`, {
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
            commitSha: "444455556666",
            verdict: "fail",
            durationMs: 9,
          },
          findings: [
            {
              stage: "S3",
              ruleId: "golden:test-integrity-guard",
              severity: "blocking",
              filePath: "pkg/cfg/load_test.go",
              line: 8,
              summary: "TestLoad has zero assertions.",
            },
          ],
        },
      ],
    }),
  });
  const qa2 = await (
    await fetch(`${demoServer}/api/tasks/${taskId}/qa`, {
      headers: authHeaders,
    })
  ).json();
  const finding2Id = qa2.findings.find(
    (f) => f.ruleId === "golden:test-integrity-guard",
  ).id;

  await expect(page.getByTestId(`qa-finding-${finding2Id}`)).toBeVisible({
    timeout: 5500,
  });

  await fetch(`${demoServer}/api/tasks/${taskId}/comments`, {
    method: "POST",
    headers: authHeaders,
    body: JSON.stringify({ body: "Agent repair note" }),
  });
  await page.waitForTimeout(600);
  await expect(
    page.locator('[data-testid="task-qa-section"] pre.ci-step-log'),
  ).toBeVisible();
  await expect(upBtn).toBeFocused();
});

test("Feedback vote records signed-in access-code user identity rather than generic operator", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const authHeaders = {
    Authorization: `Bearer ${accessCode}`,
    "Content-Type": "application/json",
  };
  await fetch(`${demoServer}/api/settings`, {
    method: "PUT",
    headers: authHeaders,
    body: JSON.stringify({
      qaEnabled: true,
      qaFlowId: "golden-v1-minimal",
      qaRolloutPct: 50,
      qaShadowOnControl: true,
    }),
  });
  const created = await (
    await fetch(`${demoServer}/api/tasks`, {
      method: "POST",
      headers: authHeaders,
      body: JSON.stringify({
        title: "Voter attribution test",
        description: "Verify RequestActor attribution on feedback votes.",
        repo: "acme/widgets",
        base: "main",
        approved: false,
      }),
    })
  ).json();
  const taskId = String(created.id);
  await fetch(`${demoServer}/api/tasks/${taskId}/qa/check-runs`, {
    method: "POST",
    headers: authHeaders,
    body: JSON.stringify({
      entries: [
        {
          run: {
            runId: `${taskId}-1`,
            stage: "S3",
            checkId: "golden:strict-config-decoder",
            arm: "treatment",
            verdict: "fail",
          },
          findings: [
            {
              stage: "S3",
              ruleId: "golden:strict-config-decoder",
              severity: "blocking",
              summary: "Missing KnownFields(true).",
            },
          ],
        },
      ],
    }),
  });
  const qaBefore = await (
    await fetch(`${demoServer}/api/tasks/${taskId}/qa`, {
      headers: authHeaders,
    })
  ).json();
  const findingId = qaBefore.findings[0].id;

  await page.goto(`/next/task/${taskId}?tab=changes`);
  await page.getByTestId(`qa-vote-up-${findingId}`).click();
  await expect(page.getByTestId(`qa-vote-up-${findingId}`)).toHaveAttribute(
    "aria-pressed",
    "true",
  );

  const qaAfter = await (
    await fetch(`${demoServer}/api/tasks/${taskId}/qa`, {
      headers: authHeaders,
    })
  ).json();
  expect(qaAfter.findings[0].votedBy).toContain("alice@example.com");
});
