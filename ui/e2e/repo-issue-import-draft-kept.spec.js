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

// The issue importer's source, label and interval wait for their own
// Save. Before the fix, flipping "Queue imported tasks at once" put a
// typed label back to the stored one, and "← Repos" or a reload then
// dropped it without asking (the 2026-11-07 repository walkthrough).
test("a typed issue-import label survives the queue switch and is asked about on leaving", async ({
  page,
}) => {
  const dialogs = [];
  page.on("dialog", async (d) => {
    dialogs.push(d.message());
    await d.dismiss();
  });
  await page.goto("/repos/acme/widgets/settings/agents");
  const issueImport = page.getByTestId("repo-issue-import");
  const label = issueImport.getByLabel("Label");
  const queue = page.getByLabel(
    "Queue imported issues in acme/widgets without approval",
  );
  await expect(queue).not.toBeChecked();
  const stored = await label.inputValue();
  try {
    await label.fill("typed-not-saved");
    const saved = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/repos/acme/widgets/settings") &&
        r.request().method() === "PUT",
    );
    await queue.click();
    expect((await saved).ok()).toBeTruthy();
    await expect(queue).toBeChecked();
    await expect(label).toHaveValue("typed-not-saved");

    // "← Repos" asks, and told no, the page and the draft both stay.
    await page.getByRole("button", { name: "← Repos" }).click();
    expect(dialogs).toEqual([
      "You have unsaved changes here. Leave and discard them?",
    ]);
    await expect(page).toHaveURL(/\/repos\/acme\/widgets\/settings\/agents$/);
    await expect(label).toHaveValue("typed-not-saved");
  } finally {
    if (await queue.isChecked()) {
      await queue.click();
      await expect(queue).not.toBeChecked();
    }
    await label.fill(stored);
  }
});
