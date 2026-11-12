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

import { expect, test } from "./fixtures.js";

// Settings → Capabilities → Repository declarations over the demo's two
// accepted CI jobs (pkg/demo seedDemoDeclarations): each offers Revoke
// inside its card, the widened one beside Accept on the same row, and
// revoking one turns it back to awaiting acceptance with Accept offered
// and Revoke gone (task 4604).
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

// A demo of its own: revoking changes the shared server's declarations.
test.use({ demoArgs: ["-open=false", "-as", "declaration-revoke"] });

const card = (page, id) =>
  page.locator(`.declaration-acceptance[data-declaration="${id}"]`);

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`Revoke sits inside each accepted declaration on a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/capabilities");
    const widened = card(page, "ci:acme/widgets:deploy-preview");
    await expect(widened).toBeVisible();
    for (const d of [card(page, "ci:acme/widgets:publish-images"), widened]) {
      const revoke = d.getByRole("button", { name: "Revoke" });
      await expect(revoke).toBeVisible();
      const box = await d.boundingBox();
      const btn = await revoke.boundingBox();
      expect(btn.x).toBeGreaterThanOrEqual(box.x);
      expect(btn.x + btn.width).toBeLessThanOrEqual(box.x + box.width);
      expect(btn.y + btn.height).toBeLessThanOrEqual(box.y + box.height);
    }
    const accept = await widened
      .getByRole("button", { name: "Accept" })
      .boundingBox();
    const revoke = await widened
      .getByRole("button", { name: "Revoke" })
      .boundingBox();
    expect(Math.abs(accept.y - revoke.y)).toBeLessThan(2);
    expect(revoke.x).toBeGreaterThan(accept.x + accept.width);
    const scroll = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(scroll).toBeLessThanOrEqual(0);
  });
}

test("revoking a declaration holds it for acceptance again", async ({
  page,
}) => {
  await page.goto("/settings/capabilities");
  const d = card(page, "ci:acme/widgets:publish-images");
  await expect(d.getByTestId("declaration-status")).toHaveText("Accepted");
  await d.getByRole("button", { name: "Revoke" }).click();
  await expect(d.getByTestId("declaration-status")).toHaveText(
    "Awaiting acceptance",
  );
  await expect(d.getByRole("button", { name: "Revoke" })).toHaveCount(0);
  await expect(d).not.toContainText("Accepted by");
  // Accepting again is how a person undoes a revocation.
  await d.getByRole("button", { name: "Accept" }).click();
  await expect(d.getByTestId("declaration-status")).toHaveText("Accepted");
  await expect(d.getByRole("button", { name: "Revoke" })).toBeVisible();
});
