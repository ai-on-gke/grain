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

// The /next/ repository page's Auto-merge tab, over a real `grain demo`.
// Merge ordering was removed, so the tab that was "Merge Queue" says
// "Auto-merge", its old /merge-queue address still opens it, and its list
// says what each open pull request waits for -- never a position, and
// never a task that has already closed (#4538's bug in `grain
// merge-queue`).

import { test, expect } from "./fixtures.js";

test("the old merge-queue address opens the Auto-merge tab and lists only open work", async ({
  page,
}) => {
  await page.goto("/next/repos/acme/widgets/merge-queue");
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/auto-merge$/);
  await expect(page.getByTestId("repo-tab-auto-merge")).toHaveText(
    "Auto-merge",
  );
  await expect(page.getByText("Merge Queue", { exact: true })).toHaveCount(0);

  const list = page.getByTestId("auto-merging-list");
  await expect(list).toBeVisible();
  const rows = list.locator("li");
  await expect(rows.first()).toContainText("Auto-merging");
  await expect(list).not.toContainText(/merge queue/i);

  const res = await page.request.get("/api/tasks?repo=acme/widgets");
  expect(res.ok()).toBe(true);
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : (body.tasks ?? []);
  const closed = new Set(
    tasks
      .filter((t) => t.state === "closed")
      .map((t) => `auto-merging-${t.id}`),
  );
  for (const id of await rows.evaluateAll((els) =>
    els.map((e) => e.dataset.testid),
  )) {
    expect(closed.has(id)).toBe(false);
  }

  // Back does not land on the old address again (replaceUrl).
  await page.getByTestId("repo-tab-overview").click();
  await page.goBack();
  await expect(page).toHaveURL(/\/auto-merge$/);
});
