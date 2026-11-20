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

import { test, expect } from "./fixtures.js";

// Provider traffic is stubbed explicitly; this checks UI wiring, not vendor access.
for (const framework of ["antigravity", "claude", "codex"]) {
  test(`tests saved ${framework} connection without saving edits`, async ({
    page,
    request,
  }) => {
    const id = `test-${framework}`;
    expect(
      (
        await request.put(`/api/deployment/agent-backends/${id}`, {
          data: { id, framework, authMode: "api_key" },
        })
      ).ok(),
    ).toBe(true);
    try {
      await page.goto("/next/settings/models");
      await page.getByTestId(`edit-backend-${id}`).click();
      await page.getByTestId("input-new-backend-name").fill("Unsaved name");
      let release;
      const pending = new Promise((resolve) => {
        release = resolve;
      });
      await page.route(
        `**/api/agent-keys/${framework}/check?backend=${id}`,
        async (route) => {
          expect(route.request().method()).toBe("POST");
          await pending;
          await route.fulfill({
            json: {
              ok: false,
              detail: "Provider refused: replace saved credential",
            },
          });
        },
      );
      const writes = [];
      page.on("request", (req) => {
        if (req.method() === "PUT") writes.push(req.url());
      });
      const button = page.getByTestId(`test-backend-${id}`);
      await button.click();
      await expect(button).toBeDisabled();
      await expect(page.getByTestId(`test-result-${id}`)).toHaveText(
        "Testing saved connection…",
      );
      release();
      await expect(page.getByTestId(`test-result-${id}`)).toContainText(
        "Provider refused",
      );
      await expect(button).toBeEnabled();
      await expect(page.getByTestId("input-new-backend-name")).toHaveValue(
        "Unsaved name",
      );
      expect(writes).toEqual([]);
      await page.unroute(`**/api/agent-keys/${framework}/check?backend=${id}`);
      await page.route(
        `**/api/agent-keys/${framework}/check?backend=${id}`,
        (route) =>
          route.fulfill({
            json: { ok: true, detail: "Provider accepted saved credential" },
          }),
      );
      await button.click();
      await expect(page.getByTestId(`test-result-${id}`)).toContainText(
        "Provider accepted",
      );
      await page.unroute(`**/api/agent-keys/${framework}/check?backend=${id}`);
      await page.route(
        `**/api/agent-keys/${framework}/check?backend=${id}`,
        (route) =>
          route.fulfill({
            status: 503,
            json: { error: "private-test-credential" },
          }),
      );
      await button.click();
      await expect(page.getByTestId(`test-result-${id}`)).toContainText(
        "Could not test",
      );
      await expect(page.getByTestId(`test-result-${id}`)).not.toContainText(
        "private-test-credential",
      );
      await expect(button).toBeEnabled();
    } finally {
      await request.delete(`/api/deployment/agent-backends/${id}`);
    }
  });
}
