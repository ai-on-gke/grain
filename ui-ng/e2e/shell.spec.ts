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

import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "./fixtures";

// The shell every /next/ page sits in, checked at each project's size and
// theme: tagged @phone and @dark so the phone and dark projects run it as
// well as desktop (playwright.config.ts). It is also the smallest example
// of this suite's conventions -- relative paths under baseURL, role and
// label selectors only, and the axe and pageErrors fixtures.

test(
  "the Inbox opens inside the shell with one h1, no sideways scroll and no serious axe violation",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }, testInfo) => {
    await page.goto("inbox");
    await expect(page).toHaveURL(/\/next\/inbox$/);
    await expect(page.getByRole("main")).toBeVisible();
    await expect(page.getByRole("heading", { level: 1 })).toHaveCount(1);

    // The theme follows prefers-color-scheme until someone picks one, so
    // the dark project's page is dark and the others' are light.
    const want = testInfo.project.name === "dark" ? "dark" : "light";
    await expect(page.locator("html")).toHaveAttribute("data-theme", want);

    const sideways = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(sideways, "the page scrolls sideways").toBeLessThanOrEqual(0);

    const violations = await axe();
    expect(
      violations.map((v) => `${v.id} (${v.nodes.length}) ${v.help}`),
    ).toEqual([]);
  },
);

test("the primary destinations are reachable by name from the Inbox", async ({
  page,
}) => {
  await page.goto("inbox");
  const nav = page.getByRole("navigation", { name: "Primary destinations" });
  await expect(nav).toBeVisible();
  await nav.getByRole("link", { name: "Tasks", exact: true }).click();
  await expect(page).toHaveURL(/\/next\/tasks/);
  await expect(
    page.getByRole("heading", { level: 1, name: "Tasks", exact: true }),
  ).toBeVisible();
});

// A showing tooltip is page content too: bare in <body> it sat outside
// every landmark (axe "region"), which failed next-inbox's dark axe pass
// whenever the theme toggle's tip appeared mid-scan (2026-11-13). Hover
// on purpose and wait for the tip, so the case is not left to timing.
test("a showing tooltip sits inside a landmark", async ({ page }) => {
  await page.goto("inbox");
  await page.getByTestId("theme-toggle").hover();
  await expect(page.getByRole("tooltip")).toBeVisible();
  const { violations } = await new AxeBuilder({ page })
    .withRules(["region"])
    .analyze();
  expect(violations.map((v) => v.nodes.map((n) => n.html))).toEqual([]);
});
