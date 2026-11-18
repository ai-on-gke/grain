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

// Regressions from the 2026-11-18 Settings walkthrough
// (docs/test-runs/2026-11-18-user-system-settings.md), each driven
// through the visible UI over the worker's real `grain demo`.

test("General's settings lock is unavailable without a config repository", async ({
  page,
}) => {
  // `grain demo` has no config repository: a lock would send every
  // config-managed section to pull requests against a repository that
  // does not exist.
  await page.goto("settings/general");
  const lock = page.getByTestId("switch-settings-locked");
  await expect(lock).toBeDisabled();
  await expect(lock).toHaveAttribute("aria-checked", "false");
  await expect(page.getByTestId("settings-lock-hint")).toHaveText(
    /Unavailable without a config repository/,
  );
  // The rest of the card is still editable.
  await expect(page.getByTestId("input-environment-name")).toBeEnabled();
});

test("Grant access says what is wrong with an entry beside the box", async ({
  page,
}) => {
  await page.goto("settings/access");
  const posts = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().endsWith("/api/access/principals")) {
      posts.push(r.postDataJSON());
    }
  });
  const entry = page.getByTestId("input-principal-entry");
  await expect(entry).toHaveAttribute("placeholder", "user:alice@example.com");
  const error = page.getByTestId("principal-entry-error");
  const grant = page.getByTestId("add-principal-btn");

  await entry.fill("walk-bob@example.com");
  await grant.click();
  await expect(error).toHaveText(
    /must start with user:, group: or serviceAccount:/,
  );
  await expect(entry).toHaveAttribute("aria-invalid", "true");
  await expect(error).toBeInViewport();

  await entry.fill("serviceAccount:bot@example.com");
  await expect(error).toHaveCount(0);
  await grant.click();
  await expect(error).toHaveText(/\*\.gserviceaccount\.com/);

  await entry.fill(
    "serviceAccount:walk-bot@demo-project.iam.gserviceaccount.com",
  );
  await grant.click();
  await expect(error).toHaveText("Give a reason to admit a service account.");
  expect(posts).toEqual([]);

  // A refusal only the server knows (a dangerous-scope role without a
  // reason) is shown in the same place, in view of the button.
  await entry.fill("user:walk-op@example.com");
  await page.getByTestId("select-new-principal-role").click();
  await page.getByRole("option", { name: /^grain\/operator/ }).click();
  await grant.click();
  await expect(error).toHaveText(/give a reason/i);
  await expect(error).toBeInViewport();
  expect(posts).toHaveLength(1);

  // A good entry is lowercased, granted, and survives a reload.
  await page.getByTestId("select-new-principal-role").click();
  await page.getByRole("option", { name: /^grain\/viewer/ }).click();
  await entry.fill("user:Walk-Bob@Example.com");
  await grant.click();
  const row = page.getByTestId("principal-row-user:walk-bob@example.com");
  await expect(row).toBeVisible();
  await expect(error).toHaveCount(0);
  await page.reload();
  await expect(row).toBeVisible();
  await page.getByTestId("remove-principal-user:walk-bob@example.com").click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Remove" })
    .click();
  await expect(row).toHaveCount(0);
});

test("leaving Memory with an unsaved edit asks first", async ({ page }) => {
  await page.goto("settings/memory");
  const box = page.getByTestId("input-deployment-memory");
  await expect(box).toBeEnabled();
  const saved = await box.inputValue();
  await box.fill(saved + "\nwalk unsaved line");

  // Cancel keeps the reader, and the edit, on Memory.
  const asked = [];
  page.once("dialog", (d) => {
    asked.push(d.message());
    void d.dismiss();
  });
  await page.getByRole("link", { name: "Inbox" }).first().click();
  await expect
    .poll(() => asked)
    .toEqual(["Leave Memory without saving your edit?"]);
  await expect(page).toHaveURL(/\/next\/settings\/memory$/);
  await expect(box).toHaveValue(saved + "\nwalk unsaved line");

  // Another section asks too: the edit would not be there on return.
  page.once("dialog", (d) => void d.accept());
  await page.getByTestId("settings-nav-tasks").click();
  await expect(page).toHaveURL(/\/next\/settings\/tasks$/);

  // Nothing was saved, and a clean Memory leaves without asking.
  await page.getByTestId("settings-nav-memory").click();
  await expect(box).toHaveValue(saved);
  page.on("dialog", (d) => {
    throw new Error(`unexpected dialog: ${d.message()}`);
  });
  await page.getByTestId("settings-nav-general").click();
  await expect(page).toHaveURL(/\/next\/settings\/general$/);
});
