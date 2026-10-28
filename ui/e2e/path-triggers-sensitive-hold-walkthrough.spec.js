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

// docs/test-plans/2026-10-04.md §3 step 2 and step 5, in a browser on the
// real `grain demo` server (docs/test-runs/2026-10-04-path-triggers-
// sensitive-paths.md). `-sensitive-hold` seeds the state the
// orchestrator leaves when acme/widgets' sensitive `payments` trigger
// (human: required) holds a change, and a later declaration read that
// failed on a malformed trigger file. The demo is single-tenant, so the
// trigger's one approver is the deployment's owner, and the owner's
// Submit is the release. tests/e2e's TestPathTriggersWalkthrough records
// that state the real way and covers a new head clearing an approval,
// which needs a git server this demo does not have. Nothing of grain's
// is stubbed here: every request below is answered by the demo's own
// handlers.
test.use({ demoArgs: ["-sensitive-hold"] });

async function heldTask(request) {
  const res = await request.get("/api/tasks");
  expect(res.ok()).toBeTruthy();
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  const held = tasks.find((t) => t.title === "Charge in cents");
  expect(held, "the -sensitive-hold seed's change").toBeTruthy();
  return held;
}

test.describe("Sensitive path hold walkthrough", () => {
  test("the owner's Submit releases a change a sensitive trigger holds", async ({
    page,
    request,
  }) => {
    const held = await heldTask(request);

    await page.goto(`/tasks/${held.id}`);
    const hold = page.getByTestId("sensitive-hold");
    await expect(hold).toBeVisible();
    await expect(hold).toContainText(
      "Held by this repository's gating triggers (human: required).",
    );
    await expect(hold).toContainText(".grain/triggers/payments.yml");
    await expect(page.getByTestId("approval-rule")).toContainText(
      "trigger:payments (.grain/triggers/payments.yml): waiting for ",
    );
    const autoMerge = page.getByLabel("Auto-merge once checks pass");
    await expect(autoMerge).not.toBeChecked();

    const released = page.waitForResponse(
      (r) =>
        r.url().endsWith(`/api/tasks/${held.id}/submit`) &&
        r.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Submit", exact: true }).click();
    expect((await released).ok()).toBeTruthy();
    await expect(autoMerge).toBeChecked();
    await expect(page.getByTestId("sensitive-hold")).toHaveCount(0);

    const after = await (await request.get(`/api/tasks/${held.id}`)).json();
    expect(after.autoMerge).toBe(true);
    expect(after.approvals).toEqual([
      expect.objectContaining({ rule: "trigger:payments" }),
    ]);
  });

  test("the repository page says a trigger file failed to read and what still runs", async ({
    page,
  }) => {
    await page.goto("/repos/acme/widgets#advanced");
    await expect(
      page.getByText(
        /Last read failed: .*\.grain\/triggers\/broken\.yml: yaml: line 2.*still running what 0f1e2d3c4b5a declared/,
      ),
    ).toBeVisible();
  });
});
