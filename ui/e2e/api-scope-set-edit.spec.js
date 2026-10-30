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

import { accessHeaders } from "./accesscode.js";
import { expect, test } from "./fixtures.js";

// Settings → Capabilities → API scope sets, written from the card: a
// person declares a repo-limited set, edits it, sees it survive a reload
// and removes it (docs/test-runs/2026-10-28-unified-grants-scope-sets.md
// step 4), confirming first with what still holds it (O3).
// api-scope-sets.spec.js covers the listing alone.

const NAME = "walk/readers";

test("a scope set is created, edited and removed from the card", async ({
  page,
  baseURL,
}) => {
  try {
    await page.goto("/settings/capabilities#api-scope-sets");
    await page.getByRole("button", { name: "New scope set" }).click();
    const form = page.getByTestId("api-scope-set-form");
    await form.getByLabel("Name").fill(NAME);
    await form.getByLabel("Scopes").fill("tasks:read, ci:read");
    await form.getByLabel("Repositories").fill("acme/widgets");
    await form.getByRole("button", { name: "Save" }).click();

    const row = page.getByTestId(`api-scope-set-${NAME}`);
    await expect(row).toBeVisible();
    await expect(row.getByText("ci:read @ acme/widgets")).toBeVisible();
    await expect(row.getByText("tasks:read @ acme/widgets")).toBeVisible();

    await row.getByRole("button", { name: "Edit" }).click();
    await form.getByLabel("Description").fill("walkthrough readers");
    await form.getByLabel("Scopes").fill("tasks:read");
    await form.getByRole("button", { name: "Save" }).click();
    await expect(form).toHaveCount(0);

    await page.reload();
    await expect(row.getByText("walkthrough readers")).toBeVisible();
    await expect(row.getByText("tasks:read @ acme/widgets")).toBeVisible();
    await expect(row.getByText("ci:read", { exact: false })).toHaveCount(0);

    await row.getByRole("button", { name: "Remove" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText(`Nothing grants api:${NAME}.`);
    await dialog.getByRole("button", { name: "Remove" }).click();
    await expect(row).toHaveCount(0);
    await page.reload();
    await expect(page.getByTestId("api-scope-set-ops/triage")).toBeVisible();
    await expect(row).toHaveCount(0);
  } finally {
    await page.request.post(`${baseURL}/api/api-scope-sets/delete`, {
      headers: accessHeaders(),
      data: { name: NAME },
    });
  }
});

const HELD = "walk/held";

test("removing a scope set a task holds asks first, and Cancel keeps it", async ({
  page,
  baseURL,
}, testInfo) => {
  let taskID = "";
  try {
    const put = await page.request.put(`${baseURL}/api/api-scope-sets`, {
      headers: accessHeaders(),
      data: {
        set: { name: HELD, scopes: ["tasks:read"], repos: ["acme/widgets"] },
      },
    });
    expect(put.ok()).toBeTruthy();
    const created = await page.request.post(`${baseURL}/api/tasks`, {
      headers: accessHeaders(),
      data: {
        title: `E2E holds ${HELD} ${Date.now()}`,
        noRepo: true,
        capabilities: [`api:${HELD}@acme/widgets`],
      },
    });
    expect(created.ok()).toBeTruthy();
    taskID = (await created.json()).id;

    // An @repo the set does not reach grants nothing, so it is refused
    // rather than recorded (O1).
    const outside = await page.request.post(
      `${baseURL}/api/tasks/${taskID}/capabilities`,
      {
        headers: accessHeaders(),
        data: { capability: `api:${HELD}@acme/gadgets`, attach: true },
      },
    );
    expect(outside.status()).toBe(400);
    expect((await outside.json()).error).toContain("grants nothing");

    for (const [label, viewport] of [
      ["desktop", { width: 1440, height: 900 }],
      ["phone", { width: 390, height: 844 }],
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/settings/capabilities#api-scope-sets");
      const row = page.getByTestId(`api-scope-set-${HELD}`);
      await row.getByRole("button", { name: "Remove" }).click();

      const dialog = page.getByRole("dialog");
      await expect(dialog).toContainText(`Remove scope set ${HELD}?`);
      await expect(dialog).toContainText(
        `1 grant holds api:${HELD}, and will grant nothing once it is removed:`,
      );
      await expect(
        dialog.locator(`[data-holder="task:${taskID}"]`),
      ).toContainText(`api:${HELD}@acme/widgets`);

      // The dialog and both of its buttons fit the viewport.
      const box = await dialog.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);
      for (const name of ["Cancel", "Remove"]) {
        const b = await dialog.getByRole("button", { name }).boundingBox();
        expect(b.x + b.width).toBeLessThanOrEqual(box.x + box.width);
      }
      await page.screenshot({
        path: testInfo.outputPath(`remove-scope-set-${label}.png`),
      });

      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(dialog).toHaveCount(0);
      await page.reload();
      await expect(row).toBeVisible();
    }

    await page.setViewportSize({ width: 1440, height: 900 });
    const row = page.getByTestId(`api-scope-set-${HELD}`);
    await row.getByRole("button", { name: "Remove" }).click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Remove" })
      .click();
    await expect(row).toHaveCount(0);
  } finally {
    if (taskID) {
      await page.request.post(`${baseURL}/api/tasks/${taskID}/capabilities`, {
        headers: accessHeaders(),
        data: { capability: `api:${HELD}@acme/widgets`, attach: false },
      });
    }
    await page.request.post(`${baseURL}/api/api-scope-sets/delete`, {
      headers: accessHeaders(),
      data: { name: HELD },
    });
  }
});
