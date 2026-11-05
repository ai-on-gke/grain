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

for (const [name, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  for (const theme of ["light", "dark"]) {
    test(`task page shows externalRef within the freshness line and filters the list on click (${name}, ${theme})`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      await page.addInitScript((mode) => {
        window.localStorage.setItem("grain.themeMode", mode);
      }, theme);
      await page.goto("/");

      const ca204Tasks = await page.evaluate(async () => {
        const res = await fetch("/api/tasks?externalRef=CA-204");
        return res.json();
      });
      expect(ca204Tasks).toHaveLength(2);
      const parent = ca204Tasks.find(
        (t) => t.title === "Add pagination to the tasks API",
      );
      expect(parent).toBeTruthy();

      await page.goto(`/tasks/${parent.id}`);

      const line = page.locator(".overlay-pane .freshness").first();
      const pr = line.getByRole("link", { name: "acme/widgets#104" });
      const refLink = line.getByRole("link", { name: "CA-204" });
      await expect(pr).toBeVisible();
      await expect(refLink).toBeVisible();
      await expect(refLink).toHaveAttribute(
        "href",
        "/?f=externalRef:any:CA-204",
      );

      const lineBox = await line.boundingBox();
      const prBox = await pr.boundingBox();
      const refBox = await refLink.boundingBox();
      expect(refBox.x).toBeGreaterThanOrEqual(lineBox.x - 1);
      expect(refBox.x + refBox.width).toBeLessThanOrEqual(
        lineBox.x + lineBox.width + 1,
      );
      expect(refBox.y).toBeGreaterThanOrEqual(lineBox.y - 1);
      expect(refBox.y + refBox.height).toBeLessThanOrEqual(
        lineBox.y + lineBox.height + 1,
      );
      if (name === "desktop") {
        expect(refBox.x).toBeGreaterThan(prBox.x + prBox.width);
        expect(
          Math.abs(refBox.y + refBox.height / 2 - (prBox.y + prBox.height / 2)),
        ).toBeLessThan(4);
      }

      const taskOverflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(taskOverflow).toBeLessThanOrEqual(0);

      await refLink.click();
      await expect(page).toHaveURL(/\/\?f=externalRef(%3A|:)any(%3A|:)CA-204/);
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect(
        page.getByText("Add pagination to the tasks API"),
      ).toBeVisible();
      await expect(
        page.getByText("Extract pagination query helper"),
      ).toBeVisible();
      await expect(
        page.getByText("Search results drop the last page"),
      ).toHaveCount(0);

      const listOverflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(listOverflow).toBeLessThanOrEqual(0);
    });
  }
}

test("GET /api/refs/CA-204/summary rolls up parent and inherited demo tasks", async ({
  page,
}) => {
  await page.goto("/");
  const summary = await page.evaluate(async () => {
    const res = await fetch("/api/refs/CA-204/summary");
    return { status: res.status, body: await res.json() };
  });
  expect(summary.status).toBe(200);
  expect(summary.body.externalRef).toBe("CA-204");
  expect(summary.body.total).toBe(2);
  expect(summary.body.pullRequests.opened).toBeGreaterThanOrEqual(1);
});
