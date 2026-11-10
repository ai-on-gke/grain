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

// Repository access as grants (design/design/unified-grants.design.md,
// slice 5): the New task form's Grants list shows the picked repo as a
// #target line that cannot be removed, and adds repo#read and repo#write
// grants from "Add grant…", each write's branch edited on its own line.
// A filed task carries them as its reads and writes, and its task pane
// lists the same lines.

import { test, expect } from "./fixtures.js";
import { chooseOption } from "./searchSelect.js";
import { repoGrantBox } from "./repoGrants.js";

let originalTargetRepos = [];

test.beforeEach(async ({ request }) => {
  const team = await (await request.get("/api/deployment")).json();
  originalTargetRepos = team.targetRepos || [];
  const res = await request.put("/api/deployment", {
    data: {
      targetRepos: Array.from(new Set([...originalTargetRepos, "acme/tools"])),
    },
  });
  expect(res.ok()).toBeTruthy();
});

test.afterEach(async ({ request }) => {
  await request.put("/api/deployment", {
    data: { targetRepos: originalTargetRepos },
  });
});

test("repository grants in the New task Grants list file as reads and writes", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();
  await page.getByRole("button", { name: "+ New task" }).click();
  const dialog = page.getByRole("dialog");
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

  const title = `Repo grants ${Date.now()}`;
  await page.getByLabel("Title").fill(title);
  await chooseOption(page.getByLabel(/^Repos/), "acme/widgets");

  // The target, as a grant that is changed above rather than removed.
  const grants = dialog.getByTestId("new-task-grants");
  const target = grants.getByRole("group", {
    name: /^acme\/widgets · .* · target$/,
  });
  await expect(target).toBeVisible();
  await expect(target.getByRole("button")).toHaveCount(0);

  // A read: typed, since the demo's repos are what it offers.
  await (await repoGrantBox(grants, "read")).fill("acme/shared-lib");
  await page.getByRole("menuitem", { name: /acme\/shared-lib/ }).click();
  await expect(
    grants.getByRole("group", { name: "acme/shared-lib · read" }),
  ).toBeVisible();

  // A write, then its branch on the same line as its repo.
  await (await repoGrantBox(grants, "write")).fill("acme/tools");
  await page.getByRole("menuitem", { name: /acme\/tools/ }).click();
  const branch = grants.getByLabel("Base branch in acme/tools");
  await branch.fill("deploy");
  await page.getByLabel("Title").click();
  const write = grants.getByRole("group", {
    name: "acme/tools · deploy · write",
  });
  await expect(write).toBeVisible();

  // Layout: the line's remove button is on its first row, at its right
  // edge, and the branch box sits under that row, inside the line.
  const line = await write.boundingBox();
  const remove = await write
    .getByRole("button", { name: /^Remove acme\/tools/ })
    .boundingBox();
  expect(remove.x + remove.width).toBeLessThanOrEqual(line.x + line.width + 1);
  expect(remove.x + remove.width).toBeGreaterThan(line.x + line.width - 12);
  const label = await write.locator(".grant-label").boundingBox();
  expect(remove.y).toBeLessThan(label.y + label.height);
  const box = await branch.boundingBox();
  expect(box.y).toBeGreaterThanOrEqual(label.y + label.height - 1);
  expect(box.x + box.width).toBeLessThanOrEqual(line.x + line.width + 1);

  const created = page.waitForResponse(
    (r) => r.url().endsWith("/api/tasks") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Create task" }).click();
  const res = await created;
  expect(res.ok(), await res.text()).toBeTruthy();
  const filed = await res.json();
  expect(filed.reads).toEqual(["acme/shared-lib"]);
  expect(filed.writes).toEqual([{ repo: "acme/tools", base: "deploy" }]);

  // The task pane lists the same grants in Access.
  await page.goto(`/tasks/${filed.id}`);
  const access = page.locator("fieldset", {
    has: page.locator("legend", { hasText: /^Access$/ }),
  });
  await expect(
    access.getByRole("group", { name: /^acme\/widgets · .* · target$/ }),
  ).toBeVisible();
  await expect(
    access.getByRole("group", { name: "acme/shared-lib · read" }),
  ).toBeVisible();
  await expect(
    access.getByRole("group", { name: "acme/tools · deploy · write" }),
  ).toBeVisible();

  // Removing the read there saves the reads that are left.
  const patched = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/tasks/${filed.id}`) &&
      r.request().method() === "PATCH",
  );
  await access
    .getByRole("button", { name: "Remove acme/shared-lib · read" })
    .click();
  const after = await (await patched).json();
  expect(after.reads || []).toEqual([]);
  await expect(
    access.getByRole("group", { name: "acme/shared-lib · read" }),
  ).toHaveCount(0);
});
