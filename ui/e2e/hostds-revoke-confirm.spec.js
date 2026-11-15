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

import { expect, test } from "./fixtures.js";

// Revoke took a host's credential away on one click, with nothing to undo
// it but enrolling the machine again (2026-11-11 walkthrough). It asks
// first now, and declining leaves the host as it was.
test("revoking a host asks first, and declining keeps it", async ({ page }) => {
  const hostName = "walk-revoke-confirm-e2e";
  await page.request.post("/api/hostds", {
    data: { name: hostName, owner: "alice@example.com" },
  });
  try {
    await page.goto("/settings/hosts");
    const row = page.getByTestId(`hostd-row-${hostName}`);
    await expect(row).toContainText("Never connected");

    const asked = [];
    page.once("dialog", (dialog) => {
      asked.push(dialog.message());
      return dialog.dismiss();
    });
    await row.getByRole("button", { name: "Revoke" }).click();
    await expect.poll(() => asked.length).toBe(1);
    expect(asked[0]).toContain(`Revoke host ${hostName}?`);
    await page.reload();
    await expect(row).toContainText("Never connected");
    await expect(row.getByRole("button", { name: "Delete" })).toHaveCount(0);

    page.once("dialog", (dialog) => dialog.accept());
    await row.getByRole("button", { name: "Revoke" }).click();
    await expect(row).toContainText("Revoked");
    await page.reload();
    await expect(row).toContainText("Revoked");
    await expect(row.getByRole("button", { name: "Delete" })).toBeVisible();
  } finally {
    await page.request.post(`/api/hostds/${hostName}/revoke`).catch(() => {});
    await page.request.delete(`/api/hostds/${hostName}`).catch(() => {});
  }
});
