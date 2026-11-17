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
  const headers = {
    Authorization: `Bearer ${accessCode}`,
    "Content-Type": "application/json",
  };
  await fetch(`${demoServer}/api/settings`, {
    method: "PUT",
    headers,
    body: JSON.stringify({
      qaEnabled: false,
      qaFlowId: "golden-v1-minimal",
      qaRolloutPct: 0,
      qaShadowOnControl: true,
    }),
  });
  const listRes = await fetch(`${demoServer}/api/qa/flow-versions`, {
    headers,
  });
  if (listRes.ok) {
    const payload = await listRes.json();
    for (const v of payload.versions || []) {
      if (!v.builtIn && !v.retiredAt && v.hash) {
        await fetch(`${demoServer}/api/qa/flow-versions`, {
          method: "POST",
          headers,
          body: JSON.stringify({ action: "retire", hash: v.hash }),
        });
      }
    }
  }
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

  // (c) /next/processes/quality renders the Software Workflows v5 Calm Progressive Studio and switches cleanly.
  const qualityTab = page.getByTestId("proc-tab-quality");
  await expect(qualityTab).toContainText("Software Workflow & QA");
  await qualityTab.click();
  await expect(page).toHaveURL(/\/next\/processes\/quality/);

  const flowsPanel = page.getByTestId("qa-flows-panel");
  const catalog = page.getByTestId("qa-workflow-catalog");
  const studio = page.getByTestId("qa-v3-studio");
  const stageTree = page.getByTestId("qa-stage-tree");
  const stepInspector = page.getByTestId("qa-step-inspector");
  const promptBudget = page.getByTestId("qa-prompt-budget").first();

  await expect(flowsPanel).toBeVisible();
  await expect(catalog).toBeVisible();
  await expect(studio).toBeVisible();
  await expect(stageTree).toBeVisible();
  await expect(stepInspector).toBeVisible();
  await expect(promptBudget).toBeVisible();
  await expect(page.getByTestId("qa-mobile-back")).toBeHidden();

  // Benchmark/ablation flows start tucked behind archived toggle on cold load.
  const archivedToggle = page.getByTestId("qa-toggle-archived-flows");
  const detRow = page.getByTestId("qa-flow-row-deterministic-s3-only");
  await expect(archivedToggle).toBeVisible();
  await expect(detRow).toBeHidden();

  const v4Row = page.getByTestId("qa-flow-row-engineering-workflow-v4");
  const v3Row = page.getByTestId("qa-flow-row-golden-v3");
  await expect(v4Row).toBeVisible();
  await expect(v3Row).toBeVisible();

  await v4Row.click();
  await expect(page.getByTestId("qa-flow-insp-title")).toContainText(
    "Full 8-stage workflow",
  );
  await expect(page.getByTestId("qa-schema-pill")).toContainText("v4");

  // Read-only inspect mode has zero disabled inputs and collapsed-by-default stages that fit above the fold at 1440x900.
  const disabledInReadOnly = await flowsPanel
    .locator("input:disabled, textarea:disabled, select:disabled")
    .count();
  expect(disabledInReadOnly).toBe(0);
  await expect(page.getByTestId("qa-expand-all-stages")).toHaveText(
    "Expand all",
  );
  const lastStageBox = await stageTree
    .locator(".qa-stage-group")
    .last()
    .boundingBox();
  expect(lastStageBox).not.toBeNull();
  expect(lastStageBox.y + lastStageBox.height).toBeLessThanOrEqual(900);

  // Verify independent overflowY: auto scrollers on all 3 columns and zero overflow-x: auto on toolbar.
  const scrollStyles = await page.evaluate(() => {
    const cat = document.querySelector(".qa-catalog-scroll");
    const spine = document.querySelector(".qa-stage-tree-list");
    const drawer = document.querySelector(".qa-step-inspector");
    const toolbar = document.querySelector(".qa-studio-toolbar");
    return {
      catOverflowY: cat ? getComputedStyle(cat).overflowY : "",
      spineOverflowY: spine ? getComputedStyle(spine).overflowY : "",
      drawerOverflowY: drawer ? getComputedStyle(drawer).overflowY : "",
      toolbarOverflowX: toolbar ? getComputedStyle(toolbar).overflowX : "",
    };
  });
  expect(scrollStyles.catOverflowY).toBe("auto");
  expect(scrollStyles.spineOverflowY).toBe("auto");
  expect(scrollStyles.drawerOverflowY).toBe("auto");
  expect(scrollStyles.toolbarOverflowX).not.toBe("auto");

  // Switch inspector tabs cleanly inside the right drawer and verify right-edge tab is unclipped.
  await expect(page.getByTestId("qa-insp-tab-empirical")).toHaveText(
    "Stats & dry-run",
  );
  const desktopYamlTabBox = await page
    .getByTestId("qa-insp-tab-yaml")
    .boundingBox();
  const desktopInspTabBox = await stepInspector.boundingBox();
  expect(desktopYamlTabBox).not.toBeNull();
  expect(desktopInspTabBox).not.toBeNull();
  expect(desktopYamlTabBox.x + desktopYamlTabBox.width).toBeLessThanOrEqual(
    desktopInspTabBox.x + desktopInspTabBox.width + 2,
  );

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
  await expect(page.getByTestId("qa-workflow-guide")).toBeVisible();

  // Customize & publish engineering-workflow-v4 (V4): verify Customize as v1 override CTA, exclude fixture survival, zero-JSON jsonschema check, and historical dry-run.
  await expect(page.getByTestId("qa-new-version-btn")).toHaveText(
    "Customize as v1 override",
  );
  await page.getByTestId("qa-new-version-btn").click();
  await expect(page.getByTestId("qa-publish-version-btn")).toHaveText(
    "Publish v1",
  );
  const draftFieldBox = await page
    .getByTestId("qa-draft-meta-card")
    .locator(".qa-field")
    .first()
    .boundingBox();
  expect(draftFieldBox).not.toBeNull();
  expect(draftFieldBox.height).toBeLessThan(95);

  // Verify S2 coding:skill never leaks workflow summary and switching to Append to parent clears unmodified parent text.
  await page.getByTestId("qa-stage-toggle-s2-coding").click();
  await page.getByTestId("qa-step-row-coding:skill").click();
  await expect(page.locator("textarea#codingSkillTextarea")).not.toContainText(
    "End-to-end 8-stage engineering workflow",
  );
  await page.getByTestId("qa-coding-skill-mode").click();
  await page
    .getByTestId("qa-coding-skill-mode-menu")
    .getByRole("option", { name: /^Append$/ })
    .click();
  await expect(page.getByTestId("qa-parent-skill-excerpt")).toBeVisible();
  await expect(page.locator("textarea#codingSkillTextarea")).toHaveValue("");
  await page.getByTestId("qa-coding-skill-mode").click();
  await page
    .getByTestId("qa-coding-skill-mode-menu")
    .getByRole("option", { name: /^Inherit$/ })
    .click();

  await page.getByTestId("qa-stage-toggle-s3-checks").click();
  await page.getByTestId("qa-step-row-custom:no-fmt-println-pkg").click();
  await expect(page.getByTestId("qa-check-exclude-input")).toHaveValue(
    "**/*_test.go",
  );
  await page
    .getByTestId("qa-check-fail-line")
    .fill('\tfmt.Println("bad debug line")');
  await expect(page.getByTestId("qa-v3-custom-check-row")).toContainText(
    "Caught",
  );
  await expect(page.getByTestId("qa-v3-custom-check-row")).toContainText(
    "Allowed",
  );

  await page
    .locator(
      '[data-testid="qa-stage-s3-checks"] [data-testid="qa-add-step-btn"]',
    )
    .click();
  await page.getByTestId("qa-v3-add-jsonschema").click();
  const jsonSchemaCard = page.getByTestId("qa-v3-custom-check-row");
  await expect(jsonSchemaCard).toBeVisible();
  expect(
    await jsonSchemaCard
      .locator('textarea:not([data-testid="qa-check-message-input"])')
      .count(),
  ).toBe(0);
  await expect(page.getByTestId("qa-check-required-keys")).toHaveValue(
    "name, version",
  );
  await expect(page.getByTestId("qa-jsonschema-examples")).toContainText(
    "Caught",
  );
  await expect(page.getByTestId("qa-jsonschema-examples")).toContainText(
    "Allowed",
  );

  // Verify unsaved-draft guard intercepts catalog switching and "Keep editing" preserves the draft.
  await expect(page.locator("g-qa-flows-panel")).toHaveAttribute(
    "data-qa-unsaved-draft",
    "true",
  );
  await v3Row.click();
  await expect(page.getByTestId("confirm-dialog")).toContainText(
    "Unsaved changes",
  );
  await page.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("confirm-dialog")).toHaveCount(0);

  await page.getByTestId("qa-test-menu-btn").click();
  await page.getByTestId("qa-dry-run-btn").click();
  await expect(page.getByTestId("qa-dry-run-banner")).toBeVisible();

  await page.getByTestId("qa-publish-version-btn").click();
  await page.getByTestId("confirm-accept").click();
  await expect(page.getByTestId("qa-new-version-btn")).toBeVisible();

  // Verify the newly published custom V4 version is active (!builtIn, so Retire version is enabled) and retire it back to built-in @v1.
  await page.getByTestId("qa-more-actions-btn").click();
  await expect(page.getByTestId("qa-retire-flow-btn")).toBeVisible();
  await page.getByTestId("qa-retire-flow-btn").click();
  await page.getByTestId("confirm-accept").click();
  await page.getByTestId("qa-more-actions-btn").click();
  await expect(page.getByTestId("qa-retire-flow-btn")).toHaveCount(0);
  await page.getByTestId("qa-more-actions-btn").click();

  // Switch back to golden-v3 cleanly, then inspect partial workflow golden-v1-minimal skipped stages summary bar.
  await v3Row.click();
  await expect(page.getByTestId("qa-flow-insp-title")).toContainText(
    "Plan critic & shadow triage",
  );
  await expect(page.getByTestId("qa-schema-pill")).toContainText("v3");

  await page.getByTestId("qa-flow-row-golden-v1-minimal").click();
  await expect(page.getByTestId("qa-toggle-skipped-stages")).toContainText(
    "skipped in this workflow",
  );

  // Customize & publish golden-v1-minimal (V2) twice (creating DB v1 then DB v2), switch versions via qa-version-switcher, and verify qa-flow-switcher at 1024x860.
  await page.getByTestId("qa-new-version-btn").click();
  await expect(page.getByTestId("qa-publish-version-btn")).toBeVisible();
  await expect(page.getByTestId("qa-test-menu-btn")).toBeVisible();
  await page.getByTestId("qa-stage-toggle-s2-coding").click();
  await page.getByTestId("qa-step-row-coding:skill").click();
  await page
    .locator("textarea#codingSkillTextarea")
    .fill(
      "Keep functions concise, verify sibling callers, and add unit tests.",
    );

  await page.getByTestId("qa-publish-version-btn").click();
  await page.getByTestId("confirm-accept").click();
  await expect(page.getByTestId("qa-new-version-btn")).toBeVisible();

  await page.getByTestId("qa-new-version-btn").click();
  await page.getByTestId("qa-stage-toggle-s2-coding").click();
  await page.getByTestId("qa-step-row-coding:skill").click();
  await page
    .locator("textarea#codingSkillTextarea")
    .fill(
      "Keep functions concise, verify sibling callers, and add deterministic unit tests.",
    );
  await page.getByTestId("qa-publish-version-btn").click();
  await page.getByTestId("confirm-accept").click();
  await expect(page.getByTestId("qa-version-switcher")).toContainText("@v2");
  await expect(page.getByTestId("qa-new-version-btn")).toContainText(
    "Customize as v3",
  );

  // Switch back to @v1 via qa-version-switcher -> URL carries ?flow=golden-v1-minimal@1; shadowed @v1 (built-in) carries pinned hash.
  await page.getByTestId("qa-version-switcher").click();
  await page
    .getByTestId("qa-version-switcher-menu")
    .getByRole("option", { name: /^@v1\s(?!\(built-in\))/ })
    .click();
  await expect(page).toHaveURL(/flow=golden-v1-minimal(@|%40)1/);
  await expect(page.getByTestId("qa-version-switcher")).toContainText("@v1");

  await page.getByTestId("qa-version-switcher").click();
  await page
    .getByTestId("qa-version-switcher-menu")
    .getByRole("option", { name: /@v1 \(built-in\)/ })
    .click();
  await expect(page).not.toHaveURL(/flow=golden-v1-minimal(@|%40)1($|&)/);
  await expect(page.getByTestId("qa-version-switcher")).toContainText(
    "@v1 (built-in)",
  );

  // Verify at 1024x860 while viewing @v1 that qa-flow-switcher still shows the family title and tabs remain unclipped.
  await page.setViewportSize({ width: 1024, height: 860 });
  await expect(page.getByTestId("qa-flow-switcher")).toContainText(
    "Recommended baseline",
  );
  const tabletYamlTabBox = await page
    .getByTestId("qa-insp-tab-yaml")
    .boundingBox();
  const tabletInspBoxCheck = await stepInspector.boundingBox();
  expect(tabletYamlTabBox).not.toBeNull();
  expect(tabletInspBoxCheck).not.toBeNull();
  expect(tabletYamlTabBox.x + tabletYamlTabBox.width).toBeLessThanOrEqual(
    tabletInspBoxCheck.x + tabletInspBoxCheck.width + 2,
  );
  await page.setViewportSize({ width: 1440, height: 900 });

  // Switch back to @v2 via qa-version-switcher -> URL returns to bare ?flow=golden-v1-minimal, then retire @v2 and custom @v1.
  await page.getByTestId("qa-version-switcher").click();
  await page
    .getByTestId("qa-version-switcher-menu")
    .getByRole("option", { name: /^@v2\b/ })
    .click();
  await expect(page).toHaveURL(/flow=golden-v1-minimal($|&)/);
  await page.getByTestId("qa-more-actions-btn").click();
  await page.getByTestId("qa-retire-flow-btn").click();
  await page.getByTestId("confirm-accept").click();
  await expect(page.getByTestId("qa-version-switcher")).toContainText("@v1");
  await page.getByTestId("qa-more-actions-btn").click();
  await page.getByTestId("qa-retire-flow-btn").click();
  await page.getByTestId("confirm-accept").click();
  await expect(page.getByTestId("qa-version-switcher")).toContainText("@v1");

  // Verify 3-column desktop geometry and 0 axe violations at 1440x900.
  const desktopCatBox = await catalog.boundingBox();
  const desktopTreeBox = await stageTree.boundingBox();
  const desktopInspBox = await stepInspector.boundingBox();
  expect(desktopCatBox).not.toBeNull();
  expect(desktopTreeBox).not.toBeNull();
  expect(desktopInspBox).not.toBeNull();
  expect(desktopCatBox.x + desktopCatBox.width).toBeLessThanOrEqual(
    desktopTreeBox.x + 2,
  );
  expect(desktopTreeBox.x + desktopTreeBox.width).toBeLessThanOrEqual(
    desktopInspBox.x + 2,
  );
  const desktopStudioOverflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(desktopStudioOverflow).toBeLessThanOrEqual(0);

  const axeDesktop = await new AxeBuilder({ page }).analyze();
  expect(axeDesktop.violations).toEqual([]);

  // Verify dark mode contrast and 0 axe violations at 1440x900.
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-theme", "dark");
  });
  const axeDesktopDark = await new AxeBuilder({ page }).analyze();
  expect(axeDesktopDark.violations).toEqual([]);
  await page.evaluate(() => {
    document.documentElement.removeAttribute("data-theme");
  });

  // Verify 1024x860 container-query 2-column layout (catalog hidden, qa-flow-switcher shown) and 0 axe violations.
  await page.setViewportSize({ width: 1024, height: 860 });
  await expect(catalog).toBeHidden();
  await expect(page.getByTestId("qa-flow-switcher")).toBeVisible();
  const tabletTreeBox = await stageTree.boundingBox();
  const tabletInspBox = await stepInspector.boundingBox();
  expect(tabletTreeBox).not.toBeNull();
  expect(tabletInspBox).not.toBeNull();
  expect(tabletTreeBox.x + tabletTreeBox.width).toBeLessThanOrEqual(
    tabletInspBox.x + 2,
  );
  const axeTablet = await new AxeBuilder({ page }).analyze();
  expect(axeTablet.violations).toEqual([]);

  // Verify 390x844 phone viewport 3-step progressive view: Step 1 Catalog -> Step 2 Stage Spine -> Step 3 Focused Drawer, 0 overflow, and 0 axe violations.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/next/processes/quality");
  await expect(page.getByTestId("quality-flows-list")).toBeVisible();
  await expect(page.getByTestId("qa-mobile-back")).toBeHidden();
  await expect(stageTree).toBeHidden();

  const axePhoneCatalog = await new AxeBuilder({ page }).analyze();
  expect(axePhoneCatalog.violations).toEqual([]);

  // Step 2: selecting a workflow opens the stage spine while keeping the right drawer tucked until a substep or Guide is chosen.
  await page.getByTestId("qa-flow-row-engineering-workflow-v4").click();
  await expect(page.getByTestId("qa-mobile-back")).toBeVisible();
  await expect(stageTree).toBeVisible();
  await expect(stepInspector).toBeHidden();
  const phoneSpineOverflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(phoneSpineOverflow).toBeLessThanOrEqual(0);

  const axePhoneSpine = await new AxeBuilder({ page }).analyze();
  expect(axePhoneSpine.violations).toEqual([]);

  // Step 3: selecting a substep opens the full-screen drawer with <- Back to stages.
  await page.getByTestId("qa-stage-toggle-s3-checks").click();
  await page.getByTestId("qa-step-row-custom:no-fmt-println-pkg").click();
  await expect(stepInspector).toBeVisible();
  await expect(page.getByTestId("qa-mobile-back-stages")).toBeVisible();
  await expect(stageTree).toBeHidden();
  const phoneDrawerOverflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(phoneDrawerOverflow).toBeLessThanOrEqual(0);

  const axePhoneDrawer = await new AxeBuilder({ page }).analyze();
  expect(axePhoneDrawer.violations).toEqual([]);

  // Back to stages -> Step 2, then Back -> Step 1 catalog.
  await page.getByTestId("qa-mobile-back-stages").click();
  await expect(stageTree).toBeVisible();
  await expect(stepInspector).toBeHidden();
  await page.getByTestId("qa-mobile-back").click();
  await expect(page.getByTestId("quality-flows-list")).toBeVisible();
  await expect(stageTree).toBeHidden();
});
