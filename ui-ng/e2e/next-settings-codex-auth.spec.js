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

// Settings → Models (ui-ng): a Codex connection in the ChatGPT
// subscription auth mode keeps its own auth.json, uploaded and cleared
// from its credential row through /api/agent-backends/{id}/codex-auth.
// The mode is offered only for the codex framework. Rendered at a desktop
// and a phone width, light and dark, with axe holding the row to WCAG 2.1
// AA.

import AxeBuilder from "@axe-core/playwright";
import { test, expect } from "./fixtures.js";

const VIEWPORTS = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];
const THEMES = ["light", "dark"];
const WCAG_AA = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"];
const ID = "codex-sub-e2e";
const AUTH_JSON = '{"tokens":{"refresh_token":"e2e-fake-refresh"}}';

async function seed(request) {
  const put = await request.put(`/api/deployment/agent-backends/${ID}`, {
    data: {
      id: ID,
      name: "Codex subscription",
      framework: "codex",
      authMode: "subscription",
    },
  });
  expect(put.ok()).toBe(true);
}

async function unseed(request) {
  await request.delete(`/api/agent-backends/${ID}/codex-auth`);
  await request.delete(`/api/deployment/agent-backends/${ID}`);
}

test.describe("Settings → Models Codex subscription login", () => {
  test.beforeEach(async ({ request }) => {
    await seed(request);
  });
  test.afterEach(async ({ request }) => {
    await unseed(request);
  });

  for (const vp of VIEWPORTS) {
    for (const theme of THEMES) {
      test(`uploads and clears a connection's auth.json at ${vp.name} in ${theme}, WCAG AA clean`, async ({
        page,
        request,
      }) => {
        test.setTimeout(90_000);
        await page.setViewportSize({ width: vp.width, height: vp.height });
        await page.addInitScript(
          (t) => localStorage.setItem("grain.themeMode", t),
          theme,
        );
        await page.goto("/next/settings/models");
        await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
        const card = page.getByTestId("card-model-backends");
        await expect(card.getByTestId(`backend-row-${ID}`)).toContainText(
          "subscription",
        );

        await card.getByTestId(`toggle-cred-${ID}`).click();
        const box = card.getByTestId(`codex-auth-${ID}`);
        await expect(box).toBeVisible();
        // The subscription mode replaces the API key field.
        await expect(
          card.getByRole("textbox", { name: `Credential for ${ID}` }),
        ).toHaveCount(0);
        const status = box.getByTestId(`codex-auth-status-${ID}`);
        await expect(status).toHaveText("Not set");
        await expect(box.getByTestId(`clear-codex-auth-${ID}`)).toBeDisabled();

        const input = box.getByTestId(`input-codex-auth-${ID}`);
        const save = box.getByTestId(`save-codex-auth-${ID}`);
        await expect(save).toBeDisabled();
        await input.fill(AUTH_JSON);
        await expect(save).toBeEnabled();

        // The textarea and its buttons stay inside the card at this width.
        const cardBox = await card.boundingBox();
        for (const el of [
          input,
          save,
          box.getByTestId(`clear-codex-auth-${ID}`),
        ]) {
          const b = await el.boundingBox();
          expect(b.x).toBeGreaterThanOrEqual(cardBox.x);
          expect(b.x + b.width).toBeLessThanOrEqual(
            cardBox.x + cardBox.width + 1,
          );
        }
        const axe = await new AxeBuilder({ page })
          .include(`[data-testid="backend-cred-row-${ID}"]`)
          .withTags(WCAG_AA)
          .analyze();
        expect(axe.violations.map((v) => `${v.id}: ${v.nodes.length}`)).toEqual(
          [],
        );

        await save.click();
        await expect(status).toHaveText("Configured");
        await expect(input).toHaveValue("");
        const stored = await (
          await request.get(`/api/agent-backends/${ID}/codex-auth`)
        ).json();
        expect(stored.set).toBe(true);

        await box.getByTestId(`clear-codex-auth-${ID}`).click();
        await page.getByTestId("confirm-accept").click();
        await expect(status).toHaveText("Not set");
        const cleared = await (
          await request.get(`/api/agent-backends/${ID}/codex-auth`)
        ).json();
        expect(cleared.set).toBe(false);
      });
    }
  }

  test("offers the subscription auth mode only for the codex framework and saves it", async ({
    page,
    request,
  }) => {
    test.setTimeout(90_000);
    const newId = "codex-sub-new-e2e";
    try {
      await page.setViewportSize({ width: 1440, height: 900 });
      await page.goto("/next/settings/models");
      const card = page.getByTestId("card-model-backends");
      await expect(card).toBeVisible();

      const authOption = page.getByTestId(
        "select-new-backend-auth-opt-subscription",
      );
      await page.getByTestId("select-new-backend-auth").click();
      await expect(
        page.getByTestId("select-new-backend-auth-opt-api_key"),
      ).toBeVisible();
      await expect(authOption).toHaveCount(0);
      await page.keyboard.press("Escape");

      await page.getByTestId("select-new-backend-fw").click();
      await page.getByTestId("select-new-backend-fw-opt-codex").click();
      await page.getByTestId("select-new-backend-auth").click();
      await authOption.click();
      await expect(page.getByTestId("select-new-backend-auth")).toContainText(
        "ChatGPT subscription",
      );

      // Switching away from codex drops the codex-only mode.
      await page.getByTestId("select-new-backend-fw").click();
      await page.getByTestId("select-new-backend-fw-opt-claude").click();
      await expect(
        page.getByTestId("select-new-backend-auth"),
      ).not.toContainText("ChatGPT subscription");

      await page.getByTestId("select-new-backend-fw").click();
      await page.getByTestId("select-new-backend-fw-opt-codex").click();
      await page.getByTestId("select-new-backend-auth").click();
      await authOption.click();
      await page.getByTestId("input-new-backend-id").fill(newId);
      await page.getByTestId("add-model-backend").click();
      await expect(card.getByTestId(`backend-row-${newId}`)).toContainText(
        "subscription",
      );
      // The row is re-read from the server after the save.
      await card.getByTestId(`toggle-cred-${newId}`).click();
      await expect(card.getByTestId(`codex-auth-${newId}`)).toBeVisible();
    } finally {
      await request.delete(`/api/deployment/agent-backends/${newId}`);
    }
  });
});
