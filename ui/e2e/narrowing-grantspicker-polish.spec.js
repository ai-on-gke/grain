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

// Regressions from the 2026-11-08 §7 walkthrough
// (docs/test-runs/2026-11-08-ui-narrowing-grantspicker-polish.md):
// F1 `owner:any:me` asked the server for owner=operator before the
// viewer was known; F2 a repo condition spelled in another case matched
// nothing (the removed-repo count link on /repos/ACME/...); F3 the Add
// grant… menu's group titles were axe listitem / aria-required-children
// violations; F4 the "What this files" preview's <dl> rows held their
// source chip beside the <dt> and <dd> (axe definition-list, dlitem).

import AxeBuilder from "@axe-core/playwright";
import { test, expect } from "./fixtures.js";

const axeRules = (page, include, rules) =>
  new AxeBuilder({ page }).include(include).withRules(rules).analyze();

test("owner:any:me waits for the viewer before asking for closed work", async ({
  page,
  request,
}) => {
  const config = await (await request.get("/api/config")).json();
  const viewer = String(config.viewerOwner || config.actor || "")
    .replace(/^user:/, "")
    .toLowerCase();
  expect(viewer).not.toBe("");
  // GET /api/config held back, as on a slow cold load.
  await page.route(
    (u) => u.pathname === "/api/config",
    async (route) => {
      await new Promise((r) => setTimeout(r, 1200));
      await route.fallback();
    },
  );
  const owners = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.pathname === "/api/tasks" && u.searchParams.get("state") === "closed")
      owners.push(u.searchParams.get("owner") || "");
  });
  await page.goto("/?f=state:any:closed&f=owner:any:me");
  await expect.poll(() => owners.filter(Boolean)).toContain(viewer);
  if (viewer !== "operator") expect(owners).not.toContain("operator");
});

test("a repo condition matches whatever case it is spelled in", async ({
  page,
  request,
}) => {
  const title = "Repo case walkthrough row";
  const created = await request.post("/api/tasks", {
    data: { title, repo: "acme/widgets" },
  });
  expect(created.ok()).toBeTruthy();
  const { id } = await created.json();
  try {
    for (const spelled of ["ACME/Widgets", "acme/widgets"]) {
      await page.goto(`/?f=repo:any:${spelled}`);
      await expect(
        page.locator(".task-row").filter({ hasText: title }),
      ).toBeVisible();
    }
  } finally {
    await request.delete(`/api/tasks/${id}`);
  }
});

test("Add grant…'s menu passes axe's list structure rules", async ({
  page,
}) => {
  await page.goto("/tasks/2");
  await page
    .getByTestId("task-2-grants")
    .getByRole("button", { name: "Add grant…" })
    .click();
  const menu = page.getByRole("menu", { name: "Grants to add" });
  await expect(menu).toBeVisible();
  await expect(menu.getByRole("separator").first()).toHaveAccessibleName(/\S/);
  const grants = await axeRules(page, '[role="menu"]', [
    "listitem",
    "aria-required-children",
  ]);
  expect(grants.violations.map((v) => v.id)).toEqual([]);
});

test("the What this files preview passes axe's definition list rules", async ({
  page,
}) => {
  await page.goto("/processes/schedules/sched-2");
  const dl = page.locator("dl").filter({ has: page.locator("dt") });
  await expect(dl.first()).toBeVisible();
  // A row's source chip sits inside its <dd>, after the value.
  const row = dl.locator("dt").first().locator("xpath=..");
  await expect(row.locator("dd .composition-value").first()).toBeVisible();
  await expect(row.locator("dd .MuiChip-root").first()).toBeVisible();
  const preview = await axeRules(page, "dl", ["definition-list", "dlitem"]);
  expect(preview.violations.map((v) => v.id)).toEqual([]);
});
