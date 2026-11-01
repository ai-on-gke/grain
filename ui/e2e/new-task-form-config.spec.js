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

// The New Task form as settings/new-task-form.yml lays it out
// (docs/design/config-repo-settings.md §6), in a real browser: with no
// configuration its fields keep their places around "Advanced options",
// and a configuration moves a field up the form, tucks another under
// Advanced options and leaves a hidden one out. /api/config is answered
// with the demo's own config plus a newTaskForm, the way the daemon
// serves a deployment whose config repository carries the file.

import { expect, test } from "./fixtures.js";

async function serveForm(page, newTaskForm) {
  await page.route("**/api/config", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, newTaskForm } });
  });
}

async function openForm(page) {
  await page.goto("/new");
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
}

const top = async (locator) => (await locator.boundingBox()).y;

test("the unconfigured form keeps Advanced options between Depends on and Queue immediately", async ({
  page,
}) => {
  await openForm(page);
  const dialog = page.getByRole("dialog");
  const depends = dialog.getByText("Depends on", { exact: false }).first();
  const advanced = dialog.getByRole("button", { name: "Advanced options" });
  const queue = dialog.getByLabel(/Queue immediately/);
  expect(await top(depends)).toBeLessThan(await top(advanced));
  expect(await top(advanced)).toBeLessThan(await top(queue));
  await expect(dialog.getByLabel(/Auto-merge once checks pass/)).toBeVisible();
});

test("a configured form moves, tucks away and hides fields", async ({
  page,
}) => {
  await serveForm(page, {
    default: {
      fields: {
        networkAccess: { visibility: "shown", order: 15 },
        approved: { visibility: "more", label: "File it approved" },
        autoMerge: { visibility: "hidden", default: true },
        title: { label: "Summary" },
      },
    },
  });
  await openForm(page);
  const dialog = page.getByRole("dialog");
  const title = dialog.getByLabel("Summary");
  const network = dialog.getByLabel("Network access");
  const description = dialog.getByLabel("Description");
  await expect(network).toBeVisible();
  // Network access, at order 15, sits between the title (10) and the
  // description (20), and spans the same column as both.
  const [t, n, d] = await Promise.all(
    [title, network, description].map((l) => l.boundingBox()),
  );
  expect(t.y + t.height).toBeLessThanOrEqual(n.y);
  expect(n.y + n.height).toBeLessThanOrEqual(d.y);
  expect(Math.abs(n.x - d.x)).toBeLessThan(20);
  await expect(dialog.getByLabel(/Auto-merge/)).toHaveCount(0);
  await expect(dialog.getByLabel("File it approved")).toBeHidden();
  await dialog.getByRole("button", { name: "Advanced options" }).click();
  await expect(dialog.getByLabel("File it approved")).toBeVisible();
});
