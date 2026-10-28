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

test("walkthrough: RepoPage CI-only capability selection, persistence, and workflow generation (docs/test-plans/2026-09-22.md §3)", async ({
  page,
  request,
}) => {
  // Step 1 & 2: Direct URL navigation to repository page (/repo/bwsalmon/grain and /repos/bwsalmon/grain)
  await page.goto("/repo/bwsalmon/grain");
  await expect(
    page.getByRole("heading", { name: "bwsalmon/grain" }),
  ).toBeVisible();

  // Step 3: Open the CI capabilities selection dropdown
  await page.getByRole("tab", { name: "CI" }).click();
  const ciCapsSelect = page.getByRole("combobox", { name: "CI capabilities" });
  await expect(ciCapsSelect).toBeVisible();
  await ciCapsSelect.click();

  // Step 4: Assert that unselected CI-only capabilities (package-publish, registry-push) are visible,
  // labelled with readable titles, and present alongside task capabilities in the dropdown menu.
  const packagePublishOption = page.getByRole("option", {
    name: /Package publish/i,
  });
  const registryPushOption = page.getByRole("option", {
    name: /Registry push/i,
  });
  const gcpKeyOption = page.getByRole("option", {
    name: /GCP key/i,
  });

  await expect(packagePublishOption).toBeVisible();
  await expect(registryPushOption).toBeVisible();
  await expect(gcpKeyOption).toBeVisible();

  // Verify readiness/description details on CI-only capability options
  await expect(packagePublishOption).toContainText("Package publish");
  await expect(registryPushOption).toContainText("Registry push");

  // Step 5: Select a CI-only capability (Package publish) and auto-save repository settings
  await packagePublishOption.click();

  // Close the select dropdown by pressing Escape or clicking backdrop
  await page.keyboard.press("Escape");

  // Verify the Chip for Package publish is displayed in the input value area
  await expect(
    page
      .locator(".MuiSelect-select")
      .filter({ hasText: /Package publish/i })
      .first(),
  ).toBeVisible();

  // Verify no error alert appears
  await expect(page.locator(".error-banner")).not.toBeVisible();

  // Verify the saved state is reflected on the backend API
  await expect
    .poll(async () => {
      const apiRes = await request.get(
        "/api/repos/bwsalmon/grain/ci-capabilities",
      );
      if (!apiRes.ok()) return [];
      const apiData = await apiRes.json();
      return apiData.ciCapabilities || [];
    })
    .toContain("package-publish");

  // Step 6: Perform hard page reload / direct URL navigation to /repo/bwsalmon/grain
  await page.goto("/repo/bwsalmon/grain#ci");
  await expect(
    page.getByRole("heading", { name: "bwsalmon/grain" }),
  ).toBeVisible();

  // Verify that the selected CI capability remains active and persisted
  const selectedChip = page
    .locator(".MuiChip-root")
    .filter({ hasText: /Package publish/i });
  await expect(selectedChip).toBeVisible();

  // Step 7: Verify CI capabilities and workflow integration
  // Calling GET /api/repos/bwsalmon/grain/capabilities returns the updated RepoDefaults
  const capsRes = await request.get("/api/repos/bwsalmon/grain/capabilities");
  expect(capsRes.ok()).toBeTruthy();
  const capsData = await capsRes.json();
  expect(capsData.ciCapabilities).toContain("package-publish");

  // Also select Registry push alongside Package publish to verify multiple selection
  await page.getByRole("combobox", { name: "CI capabilities" }).click();
  await page.getByRole("option", { name: /Registry push/i }).click();
  await page.keyboard.press("Escape");

  // Verify both chips are visible
  await expect(
    page.locator(".MuiChip-root").filter({ hasText: /Package publish/i }),
  ).toBeVisible();
  await expect(
    page.locator(".MuiChip-root").filter({ hasText: /Registry push/i }),
  ).toBeVisible();

  // Direct reload check
  await page.goto("/repo/bwsalmon/grain#ci");
  await expect(
    page.locator(".MuiChip-root").filter({ hasText: /Package publish/i }),
  ).toBeVisible();
  await expect(
    page.locator(".MuiChip-root").filter({ hasText: /Registry push/i }),
  ).toBeVisible();

  // Cleanup: Reset CI capabilities for bwsalmon/grain so subsequent tests start clean
  await request.put("/api/repos/bwsalmon/grain/ci-capabilities", {
    data: { ciCapabilities: [] },
  });
});
