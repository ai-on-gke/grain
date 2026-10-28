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

// Layout of the Grants section (GrantsPicker.jsx,
// docs/design/unified-grants.md) on the task pane, at desktop and phone
// widths: the held grants are one stack of sections inside it -- a grant
// with nothing to set on a single line -- "Add grant…" sits under it,
// the legacy checklist above it, and the menu stays on screen.

const KEY = "https://grain.dev/capabilities/cloud-key#cloud-key";
// grain demo's own capability server's method with arguments.
const CLOUD_KEY = "https://capabilities.demo.grain.example/cloud#cloud-key";

for (const [label, size] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`grants are one stack of sections under the legacy checklist (${label})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(size);
    const created = await request.post("/api/tasks", {
      data: {
        title: `E2E grants layout ${Date.now()}`,
        repo: "acme/widgets",
        capabilities: [
          "api:ci:read",
          "api:task-admin@acme/widgets",
          "api:merge:force",
          `${CLOUD_KEY}={"account":"dev","project":"acme-dev"}`,
        ],
      },
    });
    expect(created.ok()).toBeTruthy();
    const task = await created.json();
    await page.goto(`/tasks/${task.id}`);

    const section = page.getByTestId(`task-${task.id}-grants`);
    await section.scrollIntoViewIfNeeded();
    const lines = section.locator(".grants-held .grant-line[data-grant]");
    await expect(lines).toHaveCount(3);
    const box = await section.boundingBox();
    let previous = null;
    for (const line of await lines.all()) {
      const c = await line.boundingBox();
      expect(c.x).toBeGreaterThanOrEqual(box.x - 0.5);
      expect(c.x + c.width).toBeLessThanOrEqual(box.x + box.width + 0.5);
      // One line each, stacked: no option to set, so no second row.
      expect(c.height).toBeLessThanOrEqual(44);
      if (previous)
        expect(c.y).toBeGreaterThanOrEqual(previous.y + previous.height - 0.5);
      previous = c;
      // The remove button sits on the line, at its right-hand end.
      const remove = await line
        .getByRole("button", { name: /^Remove / })
        .boundingBox();
      expect(remove.y).toBeGreaterThanOrEqual(c.y - 0.5);
      expect(remove.y + remove.height).toBeLessThanOrEqual(
        c.y + c.height + 0.5,
      );
      expect(remove.x + remove.width).toBeGreaterThan(c.x + c.width - 48);
    }
    // A grant with arguments is a section with its form inline, inside
    // the section too, its inputs included.
    const args = section.locator(
      `.grants-held .grant-section[data-grant^="${CLOUD_KEY}="]`,
    );
    await expect(args.getByLabel(/^project/)).toHaveValue("acme-dev");
    for (const el of [args, args.getByLabel(/^project/)]) {
      const c = await el.boundingBox();
      expect(c.x).toBeGreaterThanOrEqual(box.x - 0.5);
      expect(c.x + c.width).toBeLessThanOrEqual(box.x + box.width + 0.5);
    }
    // A dangerous scope says so on its line.
    await expect(
      section.locator('[data-grant="api:merge:force"]').getByText("dangerous"),
    ).toBeVisible();
    // Nothing in the section pushes the page sideways.
    expect(box.x + box.width).toBeLessThanOrEqual(size.width);

    // "Add grant…" is below the last grant, not beside it.
    const held = await section.locator(".grants-held").boundingBox();
    const add = section.getByRole("button", { name: "Add grant…" });
    const addBox = await add.boundingBox();
    expect(addBox.y).toBeGreaterThanOrEqual(held.y + held.height - 0.5);

    // The legacy checklist is a separate control above the section.
    const legacy = await page
      .getByRole("combobox", { name: "Legacy capabilities" })
      .boundingBox();
    expect(legacy.y + legacy.height).toBeLessThanOrEqual(box.y);

    // The menu opens on screen.
    await add.click();
    const menu = page.getByRole("menu", { name: "Grants to add" });
    await expect(menu).toBeVisible();
    const m = await menu.boundingBox();
    expect(m.x).toBeGreaterThanOrEqual(0);
    expect(m.x + m.width).toBeLessThanOrEqual(size.width);
    const search = await page.getByLabel("Search grants").boundingBox();
    expect(search.y + search.height).toBeLessThanOrEqual(m.y + 0.5);
    await page.keyboard.press("Escape");
  });
}

test("one search on the new task overlay reaches both an API scope and a server method", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "+ New task" }).click();
  await page.getByLabel(/^Repos/).selectOption("none");
  const section = page.getByTestId("new-task-grants");
  await section.getByRole("button", { name: "Add grant…" }).click();
  await page.getByLabel("Search grants").fill("ci:read");
  await page.getByRole("menuitemcheckbox", { name: "ci:read" }).click();
  await page.getByLabel("Search grants").fill("cloud-key");
  await page.getByRole("menuitem", { name: /cloud-key/ }).click();
  // cloud-key needs arguments: it is a draft section inline, with its
  // form, and removing the draft grants nothing.
  const held = section.locator(".grants-held");
  const draft = held.locator("[data-grant-draft]");
  await expect(draft).toBeVisible();
  await expect(draft.getByLabel(/^project/)).toBeVisible();
  await draft.getByRole("button", { name: "Remove cloud-key draft" }).click();
  await expect(draft).toHaveCount(0);
  await expect(held.locator('[data-grant="api:ci:read"]')).toBeVisible();
  await expect(held.locator(`[data-grant^="${KEY}"]`)).toHaveCount(0);
});
