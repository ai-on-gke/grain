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

import { accessHeaders } from "./accesscode.js";
import { expect, test } from "./fixtures.js";

// Settings → Capabilities → API scope sets, written from the card: a
// person declares a repo-limited set, edits it, sees it survive a reload
// and removes it (docs/test-runs/2026-10-28-unified-grants-scope-sets.md
// step 4). api-scope-sets.spec.js covers the listing alone.

const NAME = "walk/readers";

test("a scope set is created, edited and removed from the card", async ({
  page,
  baseURL,
}) => {
  try {
    await page.goto("/settings/capabilities#api-scope-sets");
    await page.getByRole("button", { name: "New scope set" }).click();
    const form = page.getByTestId("api-scope-set-form");
    await form.getByLabel("Name").fill(NAME);
    await form.getByLabel("Scopes").fill("tasks:read, ci:read");
    await form.getByLabel("Repositories").fill("acme/widgets");
    await form.getByRole("button", { name: "Save" }).click();

    const row = page.getByTestId(`api-scope-set-${NAME}`);
    await expect(row).toBeVisible();
    await expect(row.getByText("ci:read @ acme/widgets")).toBeVisible();
    await expect(row.getByText("tasks:read @ acme/widgets")).toBeVisible();

    await row.getByRole("button", { name: "Edit" }).click();
    await form.getByLabel("Description").fill("walkthrough readers");
    await form.getByLabel("Scopes").fill("tasks:read");
    await form.getByRole("button", { name: "Save" }).click();
    await expect(form).toHaveCount(0);

    await page.reload();
    await expect(row.getByText("walkthrough readers")).toBeVisible();
    await expect(row.getByText("tasks:read @ acme/widgets")).toBeVisible();
    await expect(row.getByText("ci:read", { exact: false })).toHaveCount(0);

    await row.getByRole("button", { name: "Remove" }).click();
    await expect(row).toHaveCount(0);
    await page.reload();
    await expect(page.getByTestId("api-scope-set-ops/triage")).toBeVisible();
    await expect(row).toHaveCount(0);
  } finally {
    await page.request.post(`${baseURL}/api/api-scope-sets/delete`, {
      headers: accessHeaders(),
      data: { name: NAME },
    });
  }
});
