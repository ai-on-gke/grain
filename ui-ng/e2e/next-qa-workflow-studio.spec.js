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
import { test, expect } from "./fixtures.js";

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

test("Telemetry tab-strip active border, Processes full-width composition body well, and Software Workflow & QA Studio workbench render and switch cleanly", async ({
  page,
}) => {
  test.setTimeout(120_000);

  // (a) /next/telemetry renders .tab-strip inside .stage-header with visible >= 2px bottom border on the active tab
  // and zero horizontal overflow at 1440x900 and 390x844.
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto("/next/telemetry");
    await expect(page.getByTestId("telemetry-page")).toBeVisible();

    const stageHeaderTabStrip = page.locator(".stage-header > .tab-strip");
    await expect(stageHeaderTabStrip).toBeVisible();

    const activeTab = stageHeaderTabStrip
      .locator('.tab-link[aria-selected="true"]')
      .first();
    await expect(activeTab).toBeVisible();

    const tabMetrics = await activeTab.evaluate((el) => {
      const cs = getComputedStyle(el);
      const header = el.closest(".stage-header");
      const headerCs = header ? getComputedStyle(header) : null;
      return {
        borderBottomWidth: parseFloat(cs.borderBottomWidth || "0"),
        borderBottomStyle: cs.borderBottomStyle,
        headerPaddingBottom: headerCs
          ? parseFloat(headerCs.paddingBottom || "0")
          : -1,
        sidewaysOverflow:
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      };
    });

    expect(tabMetrics.borderBottomWidth).toBeGreaterThanOrEqual(2);
    expect(tabMetrics.borderBottomStyle).not.toBe("none");
    expect(tabMetrics.headerPaddingBottom).toBe(0);
    expect(tabMetrics.sidewaysOverflow).toBeLessThanOrEqual(0);
  }

  // Restore desktop viewport for Processes checks.
  await page.setViewportSize({ width: 1440, height: 900 });

  // (b) /next/processes/schedules renders .comp-body-well at >= 85% of .comp-task clientWidth (never crushed to ~220px),
  // and operator Software workflow picker binds qaFlowRef cleanly.
  await page.goto("/next/processes/schedules");
  const compPreview = page.getByTestId("composition-preview");
  await expect(compPreview).toBeVisible();

  const bodyWell = page.getByTestId("composition-body-well").first();
  await expect(bodyWell).toBeVisible();

  const widthRatio = await bodyWell.evaluate((el) => {
    const compTask = el.closest(".comp-task");
    if (!compTask || compTask.clientWidth === 0) return 0;
    return el.clientWidth / compTask.clientWidth;
  });
  expect(widthRatio).toBeGreaterThanOrEqual(0.85);

  // Verify Software workflow (<g-pill-select data-testid="process-qa-flow-select">) inside Schedule edit drawer.
  await page
    .getByRole("button", { name: /Release Qualification/ })
    .first()
    .click();
  await page.getByTestId("sched-edit-btn").click();
  const flowSelectBtn = page.getByTestId("process-qa-flow-select");
  await expect(flowSelectBtn).toBeVisible();
  await flowSelectBtn.click();
  await page
    .getByTestId("process-qa-flow-select-opt-engineering-workflow-v4@1")
    .click();
  await page.getByTestId("sched-save-btn").click();

  const flowBadge = page.getByTestId("process-qa-flow-badge");
  await expect(flowBadge).toBeVisible();
  await expect(flowBadge).toContainText("Workflow: engineering-workflow-v4@1");

  // Restore schedule qaFlowRef back to Deployment default ("") to keep demo store clean.
  await page.getByTestId("sched-edit-btn").click();
  await page.getByTestId("process-qa-flow-select").click();
  await page.getByTestId("process-qa-flow-select-opt-").click();
  await page.getByTestId("sched-save-btn").click();
  await expect(page.getByTestId("process-qa-flow-badge")).toHaveCount(0);

  // Verify Template and Suite drawers also bind and reset qaFlowRef cleanly.
  await page.getByTestId("proc-tab-templates").click();
  await expect(page).toHaveURL(/\/next\/processes\/templates/);
  await page
    .getByRole("button", { name: /release-qualification/i })
    .first()
    .click();
  await page.getByTestId("tmpl-edit-btn").click();
  await page.getByTestId("process-qa-flow-select").click();
  await page.getByTestId("process-qa-flow-select-opt-golden-v3@1").click();
  await page.getByTestId("tmpl-save-btn").click();
  await expect(page.getByTestId("process-qa-flow-badge")).toContainText(
    "Workflow: golden-v3@1",
  );
  await page.getByTestId("tmpl-edit-btn").click();
  await page.getByTestId("process-qa-flow-select").click();
  await page.getByTestId("process-qa-flow-select-opt-").click();
  await page.getByTestId("tmpl-save-btn").click();
  await expect(page.getByTestId("process-qa-flow-badge")).toHaveCount(0);

  await page.getByTestId("proc-tab-suites").click();
  await expect(page).toHaveURL(/\/next\/processes\/suites/);
  await page
    .getByRole("button", { name: /Release Qualification Suite/i })
    .first()
    .click();
  await page.getByTestId("suite-edit-btn").click();
  await page.getByTestId("process-qa-flow-select").click();
  await page.getByTestId("process-qa-flow-select-opt-golden-v2@1").click();
  await page.getByTestId("suite-save-btn").click();
  await expect(page.getByTestId("process-qa-flow-badge")).toContainText(
    "Workflow: golden-v2@1",
  );
  await page.getByTestId("suite-edit-btn").click();
  await page.getByTestId("process-qa-flow-select").click();
  await page.getByTestId("process-qa-flow-select-opt-").click();
  await page.getByTestId("suite-save-btn").click();
  await expect(page.getByTestId("process-qa-flow-badge")).toHaveCount(0);

  // (c) /next/processes/quality renders the Software Workflow & QA workbench and switches cleanly.
  const qualityTab = page.getByTestId("proc-tab-quality");
  await expect(qualityTab).toContainText("Software Workflow & QA");
  await qualityTab.click();
  await expect(page).toHaveURL(/\/next\/processes\/quality/);

  const flowsPanel = page.getByTestId("qa-flows-panel");
  const studio = page.getByTestId("qa-v3-studio");
  const stageTree = page.getByTestId("qa-stage-tree");
  const stepInspector = page.getByTestId("qa-step-inspector");
  const promptBudget = page.getByTestId("qa-prompt-budget").first();

  await expect(flowsPanel).toBeVisible();
  await expect(studio).toBeVisible();
  await expect(stageTree).toBeVisible();
  await expect(stepInspector).toBeVisible();
  await expect(promptBudget).toBeVisible();
  await expect(page.getByTestId("qa-mobile-back")).toBeHidden();

  const v4Row = page.getByTestId("qa-flow-row-engineering-workflow-v4");
  const v3Row = page.getByTestId("qa-flow-row-golden-v3");
  await expect(v4Row).toBeVisible();
  await expect(v3Row).toBeVisible();

  await v4Row.click();
  await expect(page.getByTestId("qa-flow-insp-title")).toContainText(
    "engineering workflow v4",
  );
  await expect(page.getByTestId("qa-schema-pill")).toContainText("v4");

  // Switch inspector tabs cleanly inside the 70% pane.
  await page.getByTestId("qa-insp-tab-yaml").click();
  await expect(page.getByTestId("qa-insp-tab-yaml")).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await page.getByTestId("qa-insp-tab-definition").click();
  await expect(page.getByTestId("qa-insp-tab-definition")).toHaveAttribute(
    "aria-selected",
    "true",
  );

  // Switch back to golden-v3 cleanly.
  await v3Row.click();
  await expect(page.getByTestId("qa-flow-insp-title")).toContainText(
    "golden v3",
  );
  await expect(page.getByTestId("qa-schema-pill")).toContainText("v3");

  // Verify 30/70 desktop split geometry and zero horizontal overflow at 1440x900.
  const desktopTreeBox = await stageTree.boundingBox();
  const desktopInspBox = await stepInspector.boundingBox();
  expect(desktopTreeBox).not.toBeNull();
  expect(desktopInspBox).not.toBeNull();
  expect(desktopTreeBox.x + desktopTreeBox.width).toBeLessThanOrEqual(
    desktopInspBox.x + 2,
  );
  const desktopStudioOverflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(desktopStudioOverflow).toBeLessThanOrEqual(0);

  const axeStudio = await new AxeBuilder({ page }).analyze();
  expect(
    axeStudio.violations.filter((v) =>
      ["serious", "critical"].includes(v.impact ?? ""),
    ),
  ).toEqual([]);

  // Verify single-column vertical stack and zero horizontal overflow on 390x844 phone viewport.
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(stageTree).toBeVisible();
  await expect(stepInspector).toBeVisible();
  const phoneTreeBox = await stageTree.boundingBox();
  const phoneInspBox = await stepInspector.boundingBox();
  expect(phoneTreeBox).not.toBeNull();
  expect(phoneInspBox).not.toBeNull();
  expect(phoneInspBox.y).toBeGreaterThanOrEqual(
    phoneTreeBox.y + phoneTreeBox.height - 2,
  );
  const phoneStudioOverflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(phoneStudioOverflow).toBeLessThanOrEqual(0);
});
