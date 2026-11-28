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

// Review 5925 on PR #1551: unadvertised Codex -max rows keep their effort
// unmarked, unadvertised Claude rows say Claude takes none, -xhigh models
// get no effort picker, and an enabled connection wins over an earlier
// disabled one serving the same model ID.
test(
  "classifies unadvertised models, -xhigh suffixes, and disabled/enabled model overlap",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    const settings = (url: URL) => url.pathname === "/api/settings";
    const deployment = (url: URL) => url.pathname === "/api/deployment";
    await page.route(settings, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const response = await route.fetch();
      const json = (await response.json()) as Record<string, unknown>;
      json["agentBackends"] = [
        {
          id: "claude-off",
          name: "Claude disabled",
          framework: "claude",
          disabled: true,
          models: ["opus-5.5"],
        },
        {
          id: "agy-live",
          name: "Ambient agy",
          framework: "antigravity",
          models: ["opus-5.5", "opus-5.5-xhigh", "gemini-3-pro"],
        },
      ];
      await route.fulfill({ response, json });
    });
    await page.route(deployment, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const response = await route.fetch();
      const json = (await response.json()) as Record<string, unknown>;
      json["modelSettings"] = [
        { model: "gpt-5.1-codex-max", effort: "high" },
        { model: "claude-opus-5-5", effort: "high" },
        { model: "opus-5.5-xhigh", effort: "high" },
      ];
      await route.fulfill({ response, json });
    });
    try {
      await page.goto("settings/models");
      const card = page.getByTestId("card-model-defaults");
      await expect(card).toBeVisible();
      await expect(
        card.getByTestId("model-setting-row-gpt-5.1-codex-max"),
      ).toBeVisible();
      await expect(
        card.getByTestId("model-setting-unused-gpt-5.1-codex-max"),
      ).toHaveCount(0);
      await expect(
        card.getByTestId("model-setting-unused-claude-opus-5-5"),
      ).toContainText("Claude takes no reasoning effort.");
      await expect(
        card.getByTestId("model-setting-unused-opus-5.5-xhigh"),
      ).toContainText("Effort is part of the model name: xhigh.");

      // Enabled antigravity wins over earlier disabled claude on opus-5.5.
      await card.getByTestId("picker-model-setting-model").click();
      await page
        .getByTestId("picker-model-setting-model-panel")
        .locator('[data-model="opus-5.5"]')
        .first()
        .click();
      const effort = card.getByTestId("select-model-setting-effort");
      await expect(effort).toBeEnabled();
      await expect(card.getByTestId("model-setting-effort-none")).toHaveCount(
        0,
      );

      // opus-5.5-xhigh names its effort: the picker is disabled and the
      // hint below the row says why.
      await card.getByTestId("picker-model-setting-model").click();
      await page
        .getByTestId("picker-model-setting-model-panel")
        .locator('[data-model="opus-5.5-xhigh"]')
        .click();
      const hint = card.getByTestId("model-setting-effort-none");
      await expect(hint).toContainText(
        "Effort is part of the model name: xhigh.",
      );
      await expect(effort).toBeDisabled();
      await expect(card.getByTestId("add-model-setting-btn")).toBeDisabled();

      const cardBox = (await card.boundingBox())!;
      const hintBox = (await hint.boundingBox())!;
      expect(hintBox.x).toBeGreaterThanOrEqual(cardBox.x);
      expect(hintBox.x + hintBox.width).toBeLessThanOrEqual(
        cardBox.x + cardBox.width + 1,
      );

      const sideways = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(sideways).toBeLessThanOrEqual(0);

      const violations = await axe();
      expect(
        violations.map((v) => `${v.id} (${v.nodes.length}) ${v.help}`),
      ).toEqual([]);
    } finally {
      await page.unroute(settings);
      await page.unroute(deployment);
    }
  },
);

test(
  "marks stale effort on no-effort default/fallback models as not applied and keeps tall effort menus in viewport",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    const settings = (url: URL) => url.pathname === "/api/settings";
    const deployment = (url: URL) => url.pathname === "/api/deployment";
    await page.route(settings, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const response = await route.fetch();
      const json = (await response.json()) as Record<string, unknown>;
      json["defaultModel"] =
        "opus-5.5-high,auto,claude-opus-5-5,gpt-5.1-codex-max";
      json["agentBackends"] = [
        {
          id: "agy-live",
          name: "Ambient agy",
          framework: "antigravity",
          models: ["opus-5.5-high", "auto"],
        },
        {
          id: "claude-live",
          name: "Claude live",
          framework: "claude",
          models: ["claude-opus-5-5"],
        },
        {
          id: "codex-live",
          name: "Codex live",
          framework: "codex",
          models: ["gpt-5.1-codex-max"],
        },
      ];
      await route.fulfill({ response, json });
    });
    await page.route(deployment, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const response = await route.fetch();
      const json = (await response.json()) as Record<string, unknown>;
      json["defaultModel"] =
        "opus-5.5-high,auto,claude-opus-5-5,gpt-5.1-codex-max";
      json["defaultModels"] = [
        "opus-5.5-high",
        "auto",
        "claude-opus-5-5",
        "gpt-5.1-codex-max",
      ];
      json["modelSettings"] = [
        { model: "opus-5.5-high", effort: "low" },
        { model: "auto", effort: "high" },
        { model: "claude-opus-5-5", effort: "xhigh" },
        { model: "gpt-5.1-codex-max", effort: "xhigh" },
      ];
      await route.fulfill({ response, json });
    });
    try {
      await page.goto("settings/models");
      const card = page.getByTestId("card-model-defaults");
      await expect(card).toBeVisible();

      // Stale efforts on no-effort default and fallback rows say "(not applied)"
      // and explain why, instead of "(not offered)" with no explanation.
      await expect(card.getByTestId("effort-default-model")).toContainText(
        "low (not applied)",
      );
      await expect(
        card.getByTestId("effort-default-model-unused"),
      ).toContainText(
        'Not applied: Effort is part of the model name: high. Choose "Model default" to clear it.',
      );
      await expect(card.getByTestId("effort-fallback-0")).toContainText(
        "high (not applied)",
      );
      await expect(card.getByTestId("effort-fallback-unused-0")).toContainText(
        'Not applied: The provider chooses the effort. Choose "Model default" to clear it.',
      );
      await expect(card.getByTestId("effort-fallback-1")).toContainText(
        "xhigh (not applied)",
      );
      await expect(card.getByTestId("effort-fallback-unused-1")).toContainText(
        'Not applied: Claude takes no reasoning effort. Choose "Model default" to clear it.',
      );

      // Fixed effort column stays aligned across rows and the stale pill never
      // overlaps the row's actions on desktop.
      const vp = page.viewportSize()!;
      if (vp.width > 600) {
        const eff0 = (await card
          .getByTestId("fallback-row-0")
          .locator(".mp-chain-effort")
          .boundingBox())!;
        const eff2 = (await card
          .getByTestId("fallback-row-2")
          .locator(".mp-chain-effort")
          .boundingBox())!;
        expect(Math.abs(eff0.x - eff2.x)).toBeLessThanOrEqual(1);
        const pill1 = (await card
          .getByTestId("effort-fallback-1")
          .boundingBox())!;
        const actions1 = (await card
          .getByTestId("fallback-row-1")
          .locator(".mp-chain-actions")
          .boundingBox())!;
        expect(pill1.x + pill1.width).toBeLessThanOrEqual(actions1.x + 1);
      }

      const sideways = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(sideways).toBeLessThanOrEqual(0);

      const violations = await axe();
      expect(
        violations.map((v) => `${v.id} (${v.nodes.length}) ${v.help}`),
      ).toEqual([]);

      // On compact 1024x860, opening the 9-option Codex effort menu stays
      // completely inside the viewport height (flips upward when below clips).
      if (vp.width >= 1024) {
        await page.setViewportSize({ width: 1024, height: 860 });
        await card.getByTestId("effort-fallback-2").click();
        const menu = page.getByTestId("effort-fallback-2-menu");
        await expect(menu).toBeVisible();
        const menuBox = (await menu.boundingBox())!;
        expect(menuBox.y).toBeGreaterThanOrEqual(0);
        expect(menuBox.y + menuBox.height).toBeLessThanOrEqual(860 + 1);
        expect(menuBox.x).toBeGreaterThanOrEqual(0);
        expect(menuBox.x + menuBox.width).toBeLessThanOrEqual(1024 + 1);
        await page.keyboard.press("Escape");
      }

      // Clearing a stale fallback effort returns the row to its no-effort note.
      await card.getByTestId("effort-fallback-0").click();
      await page.getByTestId("effort-fallback-0-opt-").click();
      await expect(card.getByTestId("effort-fallback-0")).toHaveCount(0);
      await expect(card.getByTestId("effort-fallback-none-0")).toContainText(
        "The provider chooses the effort",
      );
    } finally {
      await page.unroute(settings);
      await page.unroute(deployment);
    }
  },
);
