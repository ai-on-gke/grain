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

test.use({ demoArgs: ["-as", "demo-operator"] });

test("Settings Git - Verify Git scope column, scope field, and connections banner", async ({
  page,
}) => {
  await page.goto("/next/settings/git");

  await expect(page.locator("th", { hasText: "Scope" })).toBeVisible();
  await expect(page.getByTestId("input-git-conn-scope")).toBeVisible();
  await expect(page.getByTestId("gh-token-default-hint")).toBeVisible();

  const verifyBtn = page.getByTestId("check-git-connections-btn");
  await verifyBtn.click();
  const banner = page.getByTestId("git-check-banner");
  await expect(banner).toBeVisible();
});

test("Settings Repositories - Add repo picker, reachability confirm dialog, and removal confirm dialog", async ({
  page,
}) => {
  await page.goto("/next/settings/repositories");

  const repoInput = page.getByTestId("input-add-repo");
  await repoInput.click();
  await repoInput.fill("acme/demo-repo");

  const addBtn = page.getByTestId("add-repo-btn");
  await addBtn.click();

  // If unreachable or policy-gated, accept via <g-confirm-dialog>
  const addDialog = page.getByTestId("confirm-dialog");
  if (await addDialog.isVisible().catch(() => false)) {
    await page.getByTestId("confirm-accept").click();
  }

  await expect(page.getByTestId("repo-row-acme/demo-repo")).toBeVisible();

  // Remove opens <g-confirm-dialog> naming per-repo settings deletion
  await page.getByTestId("remove-repo-acme/demo-repo").click();
  await expect(page.getByTestId("confirm-dialog")).toBeVisible();
  await expect(page.getByTestId("confirm-dialog")).toContainText(
    "Per-repo settings",
  );
  await page.getByTestId("confirm-accept").click();
  await expect(page.getByTestId("repo-row-acme/demo-repo")).toHaveCount(0);
});
