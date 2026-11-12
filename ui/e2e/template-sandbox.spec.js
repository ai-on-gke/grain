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

// A template's sandbox shape (ui.Template.sandbox, grain/task-4591): set
// from the template form, shown again when the template is opened, said
// by Use…'s preview, and shown (read-only) on a declared template.

const DECLARED = "/processes/templates/repo:template:acme:widgets:nightly";

test("a declared template's sandbox shape is shown on its page, in one row", async ({
  page,
}) => {
  await page.goto(DECLARED);
  const group = page.getByTestId("template-sandbox");
  const disk = group.getByRole("spinbutton", { name: "Sandbox disk (GiB)" });
  await expect(disk).toHaveValue("100");
  await expect(disk).toBeDisabled();
  await expect(
    group.getByRole("spinbutton", { name: "Sandbox vCPUs" }),
  ).toHaveValue("");

  // The three boxes sit side by side at desktop width, inside the group.
  const boxes = await Promise.all(
    ["Sandbox vCPUs", "Sandbox memory (MiB)", "Sandbox disk (GiB)"].map((n) =>
      group.getByRole("spinbutton", { name: n }).boundingBox(),
    ),
  );
  const outer = await group.boundingBox();
  for (const b of boxes) {
    expect(b.x).toBeGreaterThanOrEqual(outer.x - 1);
    expect(b.x + b.width).toBeLessThanOrEqual(outer.x + outer.width + 1);
  }
  if ((page.viewportSize()?.width ?? 0) >= 900) {
    expect(Math.abs(boxes[0].y - boxes[2].y)).toBeLessThan(4);
  }
});

test("a template's sandbox shape is set from the form and previewed", async ({
  page,
}) => {
  const name = `walk-sandbox-${Date.now()}`;
  let id = null;
  try {
    await page.goto("/processes?kind=template");
    await page.getByRole("button", { name: "+ New template" }).click();
    await page.locator('input[name="name"]').fill(name);
    await page.locator('input[name="title"]').fill("Publish the images");
    await page.locator('input[name="sandboxDiskGb"]').fill("100");
    const created = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        new URL(r.url()).pathname === "/api/templates",
    );
    await page.getByRole("button", { name: "Add template" }).click();
    const saved = await (await created).json();
    id = saved.id;
    expect(saved.sandbox).toEqual({ diskGb: 100 });

    await page.goto(`/processes/templates/${id}`);
    await expect(page.locator('input[name="sandboxDiskGb"]')).toHaveValue(
      "100",
    );

    await page.goto("/processes?kind=template");
    await page
      .locator(".process-row-template")
      .filter({ hasText: name })
      .getByRole("button", { name: /use/i })
      .click();
    const dialog = page.getByRole("dialog").last();
    const row = dialog
      .locator("dt", { hasText: /^Sandbox$/ })
      .locator("xpath=..");
    await expect(row.locator("dd .composition-value")).toHaveText(
      "100 GiB disk",
    );
    await expect(row).toContainText("from the template");
  } finally {
    if (id) await page.request.delete(`/api/templates/${id}`);
  }
});
