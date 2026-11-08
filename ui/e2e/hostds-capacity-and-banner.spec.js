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

// Settings → Hosts: clearing Capacity on an edited host omitted capacity from
// PATCH /api/hostds/{name}, leaving the old cap=N in place; non-integer text
// serialized as null (NaN); and a refused action left the shell's 5s error
// banner up after the corrected action saved -- the 2026-11-08 connections
// and infrastructure settings walkthrough (task 3463,
// docs/test-runs/2026-11-08-connections-infrastructure-settings-task-3463.md).
test("editing a host validates capacity, clears blanked capacity, and dismisses the refusal banner", async ({
  page,
}) => {
  const hostName = "walk-cap-banner-e2e";
  await page.request.post("/api/hostds", {
    data: {
      name: hostName,
      owner: "alice@example.com",
      labels: { os: "linux" },
    },
  });
  await page.request.patch(`/api/hostds/${hostName}`, {
    data: { capacity: 6 },
  });

  try {
    await page.goto("/settings/hosts");
    const row = page.getByTestId(`hostd-row-${hostName}`);
    await expect(row).toContainText("cap=6");

    // 1. Server refusal on New code raises the banner; fixing it takes the banner down.
    await page.getByRole("button", { name: "New code" }).click();
    await page.getByLabel("Code name").fill("walk-banner-code");
    await page.getByLabel("Expires in").fill("not-a-duration");
    await page.getByRole("button", { name: "Create code" }).click();
    const durationBanner = page
      .locator(".MuiSnackbar-root")
      .getByText(/invalid expires_in/i);
    await expect(durationBanner).toBeVisible();

    await page.getByLabel("Expires in").fill("1h");
    await page.getByRole("button", { name: "Create code" }).click();
    await expect(
      page.getByTestId("enrollment-code-chip-walk-banner-code"),
    ).toBeVisible();
    await expect(durationBanner).toBeHidden({ timeout: 2000 });
    await page.getByRole("button", { name: "Revoke code" }).click();

    // 2. Client refusal on non-integer Capacity raises the banner; clearing Capacity
    //    saves capacity=0 (removing cap=6) and dismisses the banner immediately.
    await row.getByRole("button", { name: "Edit" }).click();
    const capInput = row.getByLabel("Capacity");
    await expect(capInput).toHaveValue("6");

    await capInput.fill("abc");
    await row.getByRole("button", { name: "Save" }).click();
    const capBanner = page
      .locator(".MuiSnackbar-root")
      .getByText('capacity "abc" must be a non-negative whole number');
    await expect(capBanner).toBeVisible();

    await capInput.fill("");
    await row.getByRole("button", { name: "Save" }).click();
    await expect(capBanner).toBeHidden({ timeout: 2000 });
    await expect(row).not.toContainText("cap=6");

    // Persisted across reload.
    await page.reload();
    const reloadedRow = page.getByTestId(`hostd-row-${hostName}`);
    await expect(reloadedRow).toBeVisible();
    await expect(reloadedRow).not.toContainText("cap=");
  } finally {
    await page.request.post(`/api/hostds/${hostName}/revoke`).catch(() => {});
    await page.request.delete(`/api/hostds/${hostName}`).catch(() => {});
  }
});
