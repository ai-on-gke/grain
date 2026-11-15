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

// The /next/ repository page's Advanced tab where acme/widgets declares
// path triggers and pairs branches (next-repo-advanced.spec.ts has the
// rest of the tab): `-sensitive-hold` declares a payments trigger and
// records a later read that failed, `-mechanical-port` pairs main and
// candidate with port-types in both layers, one of them misspelt.

import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures.js";

const R = "/next/repos/acme/widgets/advanced";

// checkLayout checks the layout the tab rests on: nothing pushes the
// page wider than the viewport, every card sits inside it, and neither
// table scrolls sideways.
const checkLayout = async (page: Page) => {
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  const width = page.viewportSize()!.width;
  for (const id of [
    "declarations",
    "triggers",
    "push-refs",
    "ports",
    "remove",
  ]) {
    const box = await page.getByTestId(`card-repo-${id}`).boundingBox();
    expect(box, id).not.toBeNull();
    expect(box!.x, id).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width, id).toBeLessThanOrEqual(width + 0.5);
  }
  // Paths and ref patterns break inside their cells, so even the four
  // port columns fit a phone without scrolling.
  for (const name of ["Extra push refs table", "Port directions table"]) {
    const wrap = page.getByRole("region", { name });
    if ((await wrap.count()) === 0) continue;
    const [scroll, client] = await wrap.evaluate((el) => [
      el.scrollWidth,
      el.clientWidth,
    ]);
    expect(scroll, name).toBeLessThanOrEqual(client);
  }
};

test.use({
  demoArgs: [
    "-github-sim",
    "-qualification",
    "-sensitive-hold",
    "-mechanical-port",
  ],
});

test.describe("over -sensitive-hold -mechanical-port", () => {
  test("trigger files, a failed read, and the Ports table with each warning said once @phone @dark", async ({
    page,
    axe,
  }) => {
    await page.goto(R);
    await expect(
      page.getByRole("list", { name: "Trigger files" }).getByRole("listitem"),
    ).toHaveText([".grain/triggers/payments.yml"]);

    const status = page.getByTestId("declarations-status");
    await expect(status).toContainText(
      "Last read failed: reading acme/widgets's declarations: .grain/triggers/broken.yml",
    );
    await expect(status).toContainText(
      "still running what 0f1e2d3c4b5a declared.",
    );
    await expect(page.getByTestId("declarations-still-running")).toHaveText(
      /Still running from 0f1e2d3c4b5a: 1 template\(s\), 1 suite\(s\), 1 schedule\(s\), 1 trigger\(s\)/,
    );

    await expect(page.getByTestId("ports-summary")).toContainText(
      "Grain ports each merge between main, candidate",
    );
    const ports = page.getByRole("table", { name: "Port directions" });
    await expect(
      ports.getByRole("row", {
        name: /main→candidate fix fix, revert fix, revert/,
      }),
    ).toBeVisible();
    await expect(
      ports.getByRole("row", { name: /candidate→main — fix every merge/ }),
    ).toBeVisible();
    await expect(
      ports.getByRole("row", { name: /nothing \(not a paired branch\)/ }),
    ).toBeVisible();
    // The repository's own port-types findings are among the
    // declarations warnings, and are not drawn a second time.
    await expect(page.getByText(/main->candidtae names candidtae/)).toHaveCount(
      1,
    );
    await checkLayout(page);
    expect(await axe()).toEqual([]);
  });
});
