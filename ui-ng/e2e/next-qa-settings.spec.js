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

test("Settings -> Tasks ships quality control off, previews & saves PR rollout, validates boundaries, and passes WCAG contrast", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  const initialRes = await fetch(`${demoServer}/api/settings`, {
    headers: { Authorization: `Bearer ${accessCode}` },
  });
  expect(initialRes.ok).toBe(true);
  const initial = await initialRes.json();

  try {
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.goto("/next/settings/tasks");
    const card = page.getByTestId("card-qa-rollout");
    await expect(card).toBeVisible();

    // Desktop drawer is visible, so mobile-only back link must stay hidden.
    await expect(page.getByTestId("settings-mobile-back")).toBeHidden();

    const enableSwitch = page.getByTestId("switch-qa-enabled");
    const flowBtn = page.getByTestId("pill-qa-flow");
    const rolloutInput = page.getByTestId("input-qa-rollout-pct");
    const shadowSwitch = page.getByTestId("switch-qa-shadow-control");

    // 1. Default shipped state: OFF, dependent controls disabled, zero native <select>, WCAG AA contrast clean.
    await expect(enableSwitch).toHaveAttribute("aria-checked", "false");
    await expect(flowBtn).toBeDisabled();
    await expect(rolloutInput).toBeDisabled();
    await expect(shadowSwitch).toBeDisabled();
    expect(await card.locator("select").count()).toBe(0);

    const axeLightOff = await new AxeBuilder({ page })
      .include("#card-qa-rollout")
      .analyze();
    expect(
      axeLightOff.violations.filter((v) =>
        ["serious", "critical"].includes(v.impact ?? ""),
      ),
    ).toEqual([]);

    // 2. Bounding-box check: flow pill button and rollout number input share row height & stay inside card.
    const cardBox = await card.boundingBox();
    const flowBox = await flowBtn.boundingBox();
    const rolloutBox = await rolloutInput.boundingBox();
    expect(cardBox).not.toBeNull();
    expect(flowBox).not.toBeNull();
    expect(rolloutBox).not.toBeNull();
    expect(flowBox.x).toBeGreaterThanOrEqual(cardBox.x);
    expect(rolloutBox.x + rolloutBox.width).toBeLessThanOrEqual(
      cardBox.x + cardBox.width + 1,
    );
    expect(Math.abs(flowBox.y - rolloutBox.y)).toBeLessThanOrEqual(2);
    expect(Math.abs(flowBox.height - rolloutBox.height)).toBeLessThanOrEqual(1);

    // 3. Drawer search filter matches "rollout" -> Tasks.
    const filterInput = page.getByTestId("settings-filter-input");
    await filterInput.fill("rollout");
    await expect(page.locator(".settings-nav-item")).toContainText(["Tasks"]);
    await filterInput.fill("");

    // 4. Honest input handling: Backspace clear and out-of-bounds "1009" show inline error & block Save without DOM stomp.
    await enableSwitch.click();
    await expect(rolloutInput).toBeEnabled();
    await rolloutInput.fill("");
    await expect(rolloutInput).toHaveValue("");
    await expect(page.getByTestId("error-qa-rollout-pct")).toBeVisible();
    await expect(page.getByTestId("settings-save-btn")).toBeDisabled();

    await rolloutInput.fill("1009");
    await expect(rolloutInput).toHaveValue("1009");
    await expect(page.getByTestId("error-qa-rollout-pct")).toContainText(
      "0 and 100",
    );
    await page.getByTestId("settings-discard-btn").click();
    await expect(enableSwitch).toHaveAttribute("aria-checked", "false");
    await expect(rolloutInput).toHaveValue("0");
    await expect(page.getByTestId("settings-save-bar")).toBeHidden();

    // 5. Stage valid 33% (non-multiple of 5 is valid!), flow selection, shadow=false, Plan preview, and Save.
    await enableSwitch.click();
    await flowBtn.click();
    await page
      .locator("#pill-qa-flow-menu button", {
        hasText: "deterministic-s3-only",
      })
      .click();
    await rolloutInput.fill("33");
    const isValid33 = await rolloutInput.evaluate((el) => el.checkValidity());
    expect(isValid33).toBe(true);
    await shadowSwitch.click();

    await page.getByTestId("settings-plan-btn").click();
    const planPreview = page.getByTestId("settings-plan-preview");
    await expect(planPreview).toBeVisible();
    await expect(planPreview).toContainText("deterministic-s3-only");
    await expect(planPreview).toContainText("33");

    const saveBtn = page.getByTestId("settings-save-btn");
    await saveBtn.click();
    await expect(saveBtn).toBeHidden({ timeout: 15_000 });

    // Raising rollout 33% -> 50% previews server-computed qaRegimeId bump (1 -> 2).
    const preBump = await (
      await fetch(`${demoServer}/api/settings`, {
        headers: { Authorization: `Bearer ${accessCode}` },
      })
    ).json();
    await rolloutInput.fill("50");
    await page.getByTestId("settings-plan-btn").click();
    await expect(page.getByTestId("settings-plan-preview")).toContainText(
      "Quality rollout regime",
    );
    await page.getByTestId("settings-save-btn").click();
    await expect(page.getByTestId("settings-save-bar")).toBeHidden({
      timeout: 15_000,
    });

    // 6. Verify backend API state and reload persistence.
    const settingsRes = await fetch(`${demoServer}/api/settings`, {
      headers: { Authorization: `Bearer ${accessCode}` },
    });
    expect(settingsRes.ok).toBe(true);
    const saved = await settingsRes.json();
    expect(saved.qaEnabled).toBe(true);
    expect(saved.qaFlowId).toBe("deterministic-s3-only");
    expect(saved.qaRolloutPct).toBe(50);
    expect(saved.qaShadowOnControl).toBe(false);
    expect(saved.qaRegimeId).toBe(preBump.qaRegimeId + 1);

    await page.reload();
    await expect(page.getByTestId("switch-qa-enabled")).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await expect(page.getByTestId("pill-qa-flow")).toContainText(
      "deterministic-s3-only",
    );
    await expect(page.getByTestId("input-qa-rollout-pct")).toHaveValue("50");

    // 7. Dark theme + 390x844 phone WCAG AA contrast & zero sideways overflow.
    await page.evaluate(() => localStorage.setItem("grain.themeMode", "dark"));
    await page.reload();
    await expect(page.getByTestId("card-qa-rollout")).toBeVisible();
    const axeDarkDesktop = await new AxeBuilder({ page })
      .include("#card-qa-rollout")
      .analyze();
    expect(
      axeDarkDesktop.violations.filter((v) =>
        ["serious", "critical"].includes(v.impact ?? ""),
      ),
    ).toEqual([]);

    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(250);
    const sideways = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(sideways).toBeLessThanOrEqual(0);
    const axeDarkPhone = await new AxeBuilder({ page })
      .include("#card-qa-rollout")
      .analyze();
    expect(
      axeDarkPhone.violations.filter((v) =>
        ["serious", "critical"].includes(v.impact ?? ""),
      ),
    ).toEqual([]);
  } finally {
    await page
      .evaluate(() => localStorage.removeItem("grain.themeMode"))
      .catch(() => {});
    await fetch(`${demoServer}/api/settings`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${accessCode}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        qaEnabled: Boolean(initial.qaEnabled),
        qaFlowId: initial.qaFlowId || "golden-v1-minimal",
        qaRolloutPct: Number(initial.qaRolloutPct ?? 0),
        qaShadowOnControl: Boolean(initial.qaShadowOnControl ?? true),
      }),
    });
  }
});

test("Settings -> Tasks enforces real grain/viewer read-only UI and 403 on forced POST, and two tabs do not stomp each other", async ({
  browser,
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(90_000);
  const initialRes = await fetch(`${demoServer}/api/settings`, {
    headers: { Authorization: `Bearer ${accessCode}` },
  });
  expect(initialRes.ok).toBe(true);
  const initial = await initialRes.json();

  let viewerTokenId = "";
  try {
    // Enable externalAgentApi and QA so we can mint a real grain/viewer token and test two-tab edits.
    const setupRes = await fetch(`${demoServer}/api/settings`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${accessCode}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        externalAgentApi: true,
        qaEnabled: true,
        qaFlowId: "golden-v1-minimal",
        qaRolloutPct: 30,
        qaShadowOnControl: true,
      }),
    });
    expect(setupRes.ok).toBe(true);

    const tokRes = await fetch(`${demoServer}/api/external-agents/tokens`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${accessCode}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        name: `qa-e2e-viewer-${Date.now()}`,
        scopeSet: "grain/viewer",
      }),
    });
    expect(tokRes.ok).toBe(true);
    const minted = await tokRes.json();
    viewerTokenId = minted.id || "";
    expect(minted.token).toMatch(/^gma_/);

    // 1. Real grain/viewer token session (zero route mocking): banner + disabled controls + 403 forced POST.
    const viewerCtx = await browser.newContext({
      baseURL: `${demoServer}/next/`,
      viewport: { width: 1440, height: 900 },
      extraHTTPHeaders: { Authorization: `Bearer ${minted.token}` },
    });
    try {
      const viewerPage = await viewerCtx.newPage();
      await viewerPage.goto("/next/settings/tasks");
      await expect(
        viewerPage.getByTestId("section-role-readonly-pill"),
      ).toBeVisible();
      await expect(
        viewerPage.getByTestId("settings-role-readonly"),
      ).toContainText(/settings:write|grain\/viewer/i);
      await expect(viewerPage.getByTestId("switch-qa-enabled")).toBeDisabled();
      await expect(viewerPage.getByTestId("pill-qa-flow")).toBeDisabled();
      await expect(
        viewerPage.getByTestId("input-qa-rollout-pct"),
      ).toBeDisabled();
      await expect(
        viewerPage.getByTestId("switch-qa-shadow-control"),
      ).toBeDisabled();

      const forced = await viewerPage.evaluate(async () => {
        const r = await fetch("/api/settings", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ qaEnabled: false, qaRolloutPct: 0 }),
        });
        return { status: r.status, text: await r.text() };
      });
      expect(forced.status).toBe(403);
      expect(forced.text).toMatch(/settings:write/);
    } finally {
      await viewerCtx.close();
    }

    // 2. Two tabs open simultaneously on Settings -> Tasks do not stomp each other's partial drafts.
    const pageB = await page.context().newPage();
    try {
      await page.setViewportSize({ width: 1440, height: 900 });
      await pageB.setViewportSize({ width: 1440, height: 900 });
      await page.goto("/next/settings/tasks");
      await pageB.goto("/next/settings/tasks");
      await expect(page.getByTestId("pill-qa-flow")).toBeEnabled();
      await expect(pageB.getByTestId("input-qa-rollout-pct")).toBeEnabled();

      // Tab A changes flow -> repo-custom and saves.
      await page.getByTestId("pill-qa-flow").click();
      await page
        .locator("#pill-qa-flow-menu button", { hasText: "repo-custom" })
        .click();
      await page.getByTestId("settings-save-btn").click();
      await expect(page.getByTestId("settings-save-bar")).toBeHidden();

      // Tab B (still showing pre-Tab-A state) changes rollout -> 45 and saves.
      await pageB.getByTestId("input-qa-rollout-pct").fill("45");
      await pageB.getByTestId("settings-save-btn").click();
      await expect(pageB.getByTestId("settings-save-bar")).toBeHidden();

      const merged = await (
        await fetch(`${demoServer}/api/settings`, {
          headers: { Authorization: `Bearer ${accessCode}` },
        })
      ).json();
      expect(merged.qaFlowId).toBe("repo-custom");
      expect(merged.qaRolloutPct).toBe(45);
      await expect(pageB.getByTestId("pill-qa-flow")).toContainText(
        "repo-custom",
      );
    } finally {
      await pageB.close();
    }
  } finally {
    if (viewerTokenId) {
      await fetch(`${demoServer}/api/external-agents/tokens/${viewerTokenId}`, {
        method: "DELETE",
        headers: { Authorization: `Bearer ${accessCode}` },
      }).catch(() => {});
    }
    await fetch(`${demoServer}/api/settings`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${accessCode}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        externalAgentApi: Boolean(initial.externalAgentApi),
        qaEnabled: Boolean(initial.qaEnabled),
        qaFlowId: initial.qaFlowId || "golden-v1-minimal",
        qaRolloutPct: Number(initial.qaRolloutPct ?? 0),
        qaShadowOnControl: Boolean(initial.qaShadowOnControl ?? true),
      }),
    });
  }
});
