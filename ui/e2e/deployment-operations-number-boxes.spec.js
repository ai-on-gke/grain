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

// Regressions found by the 2026-10-07 deployment-operations walkthrough
// (docs/test-runs/2026-10-07-deployment-operations.md), against the real
// demo server and built frontend, with nothing stubbed.
//
// The Admin number boxes read themselves with parseInt on blur. A real
// browser reports a number box holding something it cannot parse ("5e",
// half of "5e3") as "", which the pane took for a box somebody emptied:
// the setting was cleared back to grain's default without a word. A
// fraction was saved truncated (1.5 vCPUs as 1), and an exponent the box
// itself accepts (1e1) was saved as its first digit. jsdom models none
// of the browser's own number parsing, which is why this is here.

async function stored(page, key) {
  const settings = await page.evaluate(async () =>
    (await fetch("/api/settings")).json(),
  );
  return settings[key];
}

test("keeps a setting when its number box is left half-typed", async ({
  page,
}) => {
  await page.goto("/settings/ci");
  const entries = page.getByLabel(/^ci job cache entries$/i);
  await entries.fill("500");
  const saved = page.waitForResponse(
    (r) => r.url().endsWith("/api/settings") && r.request().method() === "PUT",
  );
  await entries.press("Tab");
  expect((await saved).status()).toBe(200);
  await expect.poll(() => stored(page, "ciCacheEntries")).toBe(500);

  const puts = [];
  page.on("request", (r) => {
    if (r.url().endsWith("/api/settings") && r.method() === "PUT") {
      puts.push(r.postData());
    }
  });
  await entries.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.type("5e");
  await entries.press("Tab");

  await expect(
    page.getByText(
      "CI job cache entries: what was typed is not a number, so it was not saved.",
    ),
  ).toBeVisible();
  await expect(entries).toHaveValue("500");
  expect(puts).toEqual([]);
  expect(await stored(page, "ciCacheEntries")).toBe(500);
});

test("refuses a fractional vCPU count and reads an exponent as a number", async ({
  page,
}) => {
  await page.goto("/settings/sandboxes");
  const cpus = page.getByLabel(/^sandbox vcpus$/i);
  const before = await stored(page, "sandboxCpus");

  await cpus.fill("1.5");
  await cpus.press("Tab");
  await expect(
    page.getByText("Sandbox vCPUs takes a whole number; 1.5 was not saved."),
  ).toBeVisible();
  await expect(cpus).toHaveValue(before ? String(before) : "");
  expect(await stored(page, "sandboxCpus")).toBe(before);

  await cpus.fill("1e1");
  await cpus.press("Tab");
  await expect.poll(() => stored(page, "sandboxCpus")).toBe(10);
  await page.reload();
  await expect(page.getByLabel(/^sandbox vcpus$/i)).toHaveValue("10");
});
