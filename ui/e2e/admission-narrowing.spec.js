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

// Admission as a narrowing attribute (docs/test-runs/2026-11-07-ui-search-
// scroll-resilience.md, F5). Every row of GET /api/tasks carries one, but
// `/?f=admission:any:durable` used to draw the warning chip "Unknown
// condition: admission" and leave the list unnarrowed. Now it is a closed
// enum over standard|bypass|durable: a real value narrows quietly, and a
// typo warns the way `origin:any:bogus` does.

const chips = (page) => page.locator(".narrowing-chips .MuiChip-root");
const warnings = (page) =>
  page.locator(".narrowing-chips .MuiChip-colorWarning");
const rows = (page) => page.locator(".task-row");

const DURABLE = "Admission narrowing: durable seed";

test("admission:any:durable narrows without a warning; a bogus value warns", async ({
  page,
  request,
}) => {
  // A proposed task, so nothing dispatches it while the test looks.
  const created = await request.post("/api/tasks", {
    data: { title: DURABLE, repo: "acme/widgets", admission: "durable" },
  });
  expect(created.ok()).toBeTruthy();
  const { id } = await created.json();
  try {
    await page.goto("/?f=admission:any:durable");
    await expect(
      chips(page).filter({ hasText: "Admission is any of Durable" }),
    ).toBeVisible();
    await expect(page.getByText(/Unknown condition/)).toHaveCount(0);
    await expect(warnings(page)).toHaveCount(0);
    // The condition is applied: the durable seed is listed and an
    // ordinary demo task is not.
    await expect(rows(page).filter({ hasText: DURABLE })).toBeVisible();
    await expect(
      rows(page).filter({ hasText: "Bump the Go toolchain to 1.24" }),
    ).toHaveCount(0);

    await page.goto("/?f=admission:any:bogus");
    const bad = warnings(page);
    await expect(bad).toHaveCount(1);
    await expect(bad).toContainText("Admission is any of “bogus” (unknown)");
    await expect(bad).toHaveAttribute("title", /not a value Admission has/);
    // Still the attribute's own chip, not "Unknown condition".
    await expect(page.getByText(/Unknown condition/)).toHaveCount(0);
  } finally {
    await request.delete(`/api/tasks/${id}`);
  }
});
