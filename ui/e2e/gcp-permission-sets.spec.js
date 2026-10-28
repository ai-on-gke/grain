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

// The gcp capability's permission sets end to end (docs/capabilities.md,
// "GCP"), against grain demo's gcp server: grain's own in-tree gcp
// capability served over HTTP, configured by Settings' schema form like
// any other server. Its only stub is the external boundary, GCP itself:
// gcpfake's in-memory IAM, which says every admin is
// you@demo.grain.example.
//
// The flow adds an admin and a permission set to the server's
// configuration, tests it -- the report names the admin's identity and
// the new set -- saves it, and grants the new set on a task from the
// grant form's dropdown.

const GCP = "https://capabilities.demo.grain.example/gcp";
const KEY = `${GCP}#gcp-key`;

function field(scope, path) {
  return scope.locator(`[data-arg="${path}"]`);
}

async function pick(page, scope, path, option) {
  await scope
    .locator(".MuiFormControl-root", {
      has: page.locator(`[data-arg="${path}"]`),
    })
    .last()
    .getByRole("combobox")
    .click();
  await page.getByRole("option", { name: option, exact: true }).click();
}

async function addListItem(scope, path, name) {
  const list = scope.locator(`[data-arg-list="${path}"]`);
  await list
    .getByRole("button", { name: `Add ${name}` })
    .last()
    .click();
}

test("adds a permission set, tests it, and grants the set on a task", async ({
  page,
  request,
}) => {
  await page.goto("/settings/capabilities#capability-servers");
  await page.locator(`[data-capability-server-row="${GCP}"]`).click();
  const card = page.locator(`[data-capability-server="${GCP}"]`);
  await expect(card).toBeVisible();

  await card.getByRole("button", { name: "Edit server settings" }).click();
  const config = card.getByTestId("capability-server-config");

  // Admin credential source defaults to ambient; add a permission set directly.
  await addListItem(config, "permission_sets", "permission_sets");
  await field(config, "permission_sets[1].name").fill("e2e-reader");
  await field(config, "permission_sets[1].project").fill("e2e-project");
  await addListItem(config, "permission_sets[1].roles", "roles");
  await field(config, "permission_sets[1].roles[0]").fill(
    "roles/storage.objectViewer",
  );

  await config.getByRole("button", { name: "Save server settings" }).click();
  await expect(config).toHaveCount(0);

  // Test: the report names the default admin's identity and the new set.
  await card.getByRole("button", { name: "Test", exact: true }).click();
  const result = card.getByTestId("capability-test-result");
  await expect(result).toContainText(
    "admin default (ambient) is you@demo.grain.example",
  );
  await expect(result).toContainText("has every permission it needs");
  await expect(result).toContainText("set e2e-reader (e2e-project");

  // The set is in the grant form's dropdown.
  await page.goto("/");
  const title = `E2E gcp set ${Date.now()}`;
  await page.getByRole("button", { name: "+ New task" }).click();
  await page.getByLabel("Title").fill(title);
  await page.getByLabel(/^Repos/).selectOption("none");
  const grants = page.getByTestId("new-task-grants");
  await grants.getByRole("button", { name: "Add grant…" }).click();
  await page.getByRole("menuitem", { name: /gcp-key/ }).click();
  // The method is a section inline, its arguments with it; picking the
  // set saves the grant (there is no dialog to confirm).
  const section = grants.locator(`[data-method="${KEY}"] .grant-section`);
  await expect(section).toHaveCount(1);
  await pick(page, section, "permission_set", "e2e-reader");
  await expect(grants.locator(`[data-grant^="${KEY}="]`)).toHaveAttribute(
    "data-grant",
    /"permission_set":"e2e-reader"/,
  );

  await page.getByRole("button", { name: "Create task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(0);
  const list = await (await request.get("/api/tasks")).json();
  const tasks = Array.isArray(list) ? list : list.tasks;
  const filed = tasks.find((t) => t.title === title);
  expect(filed).toBeTruthy();
  const held = (filed.capabilities || []).filter((c) => c.startsWith(KEY));
  expect(held).toHaveLength(1);
  expect(held[0]).toContain('"permission_set":"e2e-reader"');
});
