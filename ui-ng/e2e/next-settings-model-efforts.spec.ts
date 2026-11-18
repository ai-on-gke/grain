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
// hide the effort picker, and an enabled connection wins over an earlier
// disabled one serving the same model ID.
test(
  "classifies unadvertised models, -xhigh suffixes, and disabled/enabled model overlap",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }, testInfo) => {
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
      await expect(
        card.getByTestId("select-model-setting-effort"),
      ).toBeVisible();
      await expect(card.getByTestId("model-setting-no-effort")).toHaveCount(0);

      // Selecting opus-5.5-xhigh swaps the picker for the inline hint.
      await card.getByTestId("picker-model-setting-model").click();
      await page
        .getByTestId("picker-model-setting-model-panel")
        .locator('[data-model="opus-5.5-xhigh"]')
        .click();
      const hint = card.getByTestId("model-setting-no-effort");
      await expect(hint).toContainText(
        "Effort is part of the model name: xhigh.",
      );
      await expect(card.getByTestId("select-model-setting-effort")).toHaveCount(
        0,
      );

      const cardBox = (await card.boundingBox())!;
      const pickerBox = (await card
        .getByTestId("picker-model-setting-model")
        .boundingBox())!;
      const hintBox = (await hint.boundingBox())!;
      if (testInfo.project.name === "phone") {
        expect(hintBox.x).toBeGreaterThanOrEqual(cardBox.x);
        expect(hintBox.x + hintBox.width).toBeLessThanOrEqual(
          cardBox.x + cardBox.width + 1,
        );
      } else {
        expect(hintBox.x).toBeGreaterThanOrEqual(
          pickerBox.x + pickerBox.width - 2,
        );
        expect(
          Math.abs(
            hintBox.y +
              hintBox.height / 2 -
              (pickerBox.y + pickerBox.height / 2),
          ),
        ).toBeLessThanOrEqual(8);
        expect(hintBox.x + hintBox.width).toBeLessThanOrEqual(
          cardBox.x + cardBox.width + 1,
        );
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
    } finally {
      await page.unroute(settings);
      await page.unroute(deployment);
    }
  },
);
