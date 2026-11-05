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

// Settings -> Hostds' Install action shows a registered hostd's install
// instructions again, as the daemon renders them (GET
// /api/hostds/{name}/install, pkg/ui's RegisterHostdInstall): the real
// demo daemon's default root installer line, and -- for a build that
// registers its own -- several long lines and a note, which must wrap
// inside the alert with the copy button beside them at phone width.

test("Install shows the daemon's default root installer line", async ({
  page,
}) => {
  await page.goto("/settings/hostds");
  const row = page.getByTestId("hostd-row-cloudtop-alice");
  await row.getByRole("button", { name: "Install" }).click();
  const lines = page.getByTestId("hostd-install-lines");
  await expect(lines).toContainText("/api/hostd/install.sh");
  await expect(lines).toContainText(
    "grain-hostd-install --name cloudtop-alice",
  );
  await expect(lines).not.toContainText("ghd_");
  await expect(page.getByTestId("hostd-install-note")).toHaveCount(0);
  // The revoked hostd and the co-located one have no Install action.
  await expect(
    page
      .getByTestId("hostd-row-old-laptop")
      .getByRole("button", { name: "Install" }),
  ).toHaveCount(0);
  await expect(
    page
      .getByTestId("hostd-row-local")
      .getByRole("button", { name: "Install" }),
  ).toHaveCount(0);
});

for (const width of [1440, 390]) {
  test(`a build's own lines and note wrap inside the alert at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    const lines = [
      "git clone https://git.example.com/some/long/path/to/the/grain-hostd/repository.git ~/grain-hostd && cd ~/grain-hostd",
      "./scripts/install-grain-hostd.sh --server https://grain.example.com --name cloudtop-alice --owner alice@example.com",
    ];
    const note =
      "Run these as yourself, no sudo. Prefix the last with GRAIN_HOSTD_TOKEN=<token>, or paste it when asked.";
    await page.route("**/api/hostds/cloudtop-alice/install", (route) =>
      route.fulfill({ json: { release: "", lines, note } }),
    );
    await page.goto("/settings/hostds");
    await page
      .getByTestId("hostd-row-cloudtop-alice")
      .getByRole("button", { name: "Install" })
      .click();

    const alert = page.getByTestId("hostd-install-again");
    const block = page.getByTestId("hostd-install-lines");
    await expect(block).toHaveText(lines.join("\n"));
    await expect(page.getByTestId("hostd-install-note")).toHaveText(note);
    const copy = alert.getByRole("button", { name: "Copy install commands" });
    await expect(copy).toBeVisible();

    const a = await alert.boundingBox();
    const b = await block.boundingBox();
    const c = await copy.boundingBox();
    const n = await page.getByTestId("hostd-install-note").boundingBox();
    // The block and the copy button stay inside the alert and the
    // viewport: long lines wrap rather than run off the page.
    expect(b.x).toBeGreaterThanOrEqual(a.x);
    expect(b.x + b.width).toBeLessThanOrEqual(a.x + a.width + 1);
    expect(c.x + c.width).toBeLessThanOrEqual(
      Math.min(a.x + a.width, width) + 1,
    );
    expect(c.x).toBeGreaterThanOrEqual(b.x + b.width - 1);
    // The copy button sits at the block's top, beside it.
    expect(Math.abs(c.y - b.y)).toBeLessThan(c.height);
    // The note comes after the block.
    expect(n.y).toBeGreaterThanOrEqual(b.y + b.height - 1);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  });
}
