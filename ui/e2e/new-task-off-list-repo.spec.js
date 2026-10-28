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

// "Other…" on the New task form takes any repo, and the server files one
// off the deployment's target repo list parked awaiting a reply rather
// than refusing it (ui.Client.parkOffAllowlist). The form used to close
// without a word, leaving the explanation in a comment on a task nobody
// had opened yet (the 2026-10-07 task-intake walkthrough). It now warns
// before submit, and the parked task waits in the Inbox.
test("warns before filing on an off-list repo, and the filed task waits in the Inbox", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();

  await page.getByRole("button", { name: "+ New task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

  const title = `Off-list repo ${Date.now()}`;
  await page.getByLabel("Title").fill(title);
  await page.getByLabel(/^Repos/).selectOption("acme/widgets");
  const warning = page.getByTestId("repo-off-allowlist");
  await expect(warning).toHaveCount(0);

  await page.getByLabel(/^Repos/).selectOption("Other…");
  await page.locator("input[name=repo]").fill("acme/gizmos");
  await expect(warning).toContainText(
    "acme/gizmos is not on this deployment's target repo list",
  );

  const created = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/tasks") &&
      response.request().method() === "POST",
    { timeout: 15_000 },
  );
  await page.getByRole("button", { name: "Create task" }).click();
  expect((await created).status()).toBe(201);

  await page.goto("/inbox");
  const row = page.locator(".inbox-item", { hasText: title });
  await expect(row).toBeVisible();
  await expect(
    page.locator(".inbox-group", { has: row }).getByText("Asked: reply"),
  ).toBeVisible();
});
