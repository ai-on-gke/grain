// Copyright 2026 The Grain Authors
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

import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { test, expect } from "./fixtures.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const FIXTURE_PATH = path.join(__dirname, "hostds-update.fixture.json");

interface HostFixture {
  name: string;
  owner: string;
  state: string;
  labels?: Record<string, string>;
  connection?: Record<string, unknown>;
  update?: {
    build: string;
    available: string;
    command: string;
  };
}

test("Settings -> Hosts renders copyable update command, stacks without sideways scroll, and clears after update @phone @dark", async ({
  page,
  context,
  axe,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const hosts = JSON.parse(readFileSync(FIXTURE_PATH, "utf8")) as HostFixture[];
  let updated = false;

  await page.route("**/api/hostds", async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    const body = updated
      ? hosts.map((h) => {
          if (h.name !== "laptop") return h;
          const rest = { ...h };
          delete rest.update;
          return {
            ...rest,
            connection: {
              connected: true,
              host: "laptop",
              build: "v3.9.0",
            },
          };
        })
      : hosts;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });

  await page.goto("/next/settings/hosts");

  const table = page.getByRole("table", { name: "Registered hosts" });
  await expect(table).toBeVisible();

  const updateLine = page.getByTestId("hostd-update-laptop");
  await expect(updateLine).toBeVisible();
  await expect(updateLine).toContainText("Update available: v3.8.0 → v3.9.0");
  await expect(updateLine).toContainText("On the host, run");
  await expect(updateLine).toContainText("grain-agentd update -version v3.9.0");

  // Hosts without an offer show no update line.
  await expect(page.getByTestId("hostd-update-local")).toHaveCount(0);
  await expect(page.getByTestId("hostd-update-build-box")).toHaveCount(0);

  // Neither the page nor the hosts table scrolls sideways at desktop or phone.
  const layout = await page.evaluate(() => {
    const tbl = document.querySelector('[aria-label="Registered hosts"]')!;
    const wrap = tbl.closest(".s-table-wrap")!;
    const wr = wrap.getBoundingClientRect();
    const upd = document.querySelector('[data-testid="hostd-update-laptop"]')!;
    const ur = upd.getBoundingClientRect();
    const code = upd.querySelector("code")!.getBoundingClientRect();
    const btn = upd.querySelector("button")!.getBoundingClientRect();
    return {
      pageOverflowX: document.documentElement.scrollWidth > window.innerWidth,
      wrapOverflowX: wrap.scrollWidth > Math.ceil(wr.width),
      insideWrap: ur.left >= wr.left - 1 && ur.right <= wr.right + 1,
      codeAndBtnSeparate: code.right <= btn.left + 1,
      btnW: btn.width,
      btnH: btn.height,
    };
  });
  expect(layout.pageOverflowX).toBe(false);
  expect(layout.wrapOverflowX).toBe(false);
  expect(layout.insideWrap).toBe(true);
  expect(layout.codeAndBtnSeparate).toBe(true);
  expect(layout.btnW).toBeGreaterThanOrEqual(24);
  expect(layout.btnH).toBeGreaterThanOrEqual(24);

  // One-click copy writes the exact command and confirms via snackbar.
  await page
    .getByRole("button", { name: "Copy update command for laptop" })
    .click();
  await expect(page.getByText("Copied update command")).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    "grain-agentd update -version v3.9.0",
  );

  // WCAG 2.1 AA floor in light/dark at desktop/phone.
  expect(await axe()).toEqual([]);

  // After the host updates, the offer disappears and the new build is shown.
  updated = true;
  await page.reload();
  await expect(page.getByTestId("hostd-row-laptop")).toBeVisible();
  await expect(page.getByTestId("hostd-update-laptop")).toHaveCount(0);
  await expect(page.getByTestId("hostd-connection-laptop")).toContainText(
    "v3.9.0",
  );
});
