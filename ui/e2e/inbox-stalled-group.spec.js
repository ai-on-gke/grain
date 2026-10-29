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

// The Inbox's "Look at a stuck task" group (InboxPage.jsx's "stalled"),
// over `grain demo -stalled-tasks` (pkg/demo/demo_stalled.go): the only
// demo whose UI is handed the orchestrator's stall deadlines, so the only
// one a stuck task can be shown in. Each row says which part of grain
// should have moved it on and offers the control its sub-case names, and
// pressing that control takes the row out of the group (2026-10-29
// intake walkthrough).
test.use({ demoArgs: ["-stalled-tasks"] });

test("a stuck task's row offers its sub-case's control and leaves the group once pressed", async ({
  page,
}) => {
  await page.goto("/inbox");
  const group = page.locator(".inbox-group", {
    has: page.getByRole("heading", { name: "Look at a stuck task" }),
  });
  const queued = group.locator(".inbox-item", {
    hasText: "Backfill the audit log retention job",
  });
  const running = group.locator(".inbox-item", {
    hasText: "Regenerate the protobuf bindings",
  });
  await expect(queued).toContainText("reconcileDispatch");
  await expect(running).toContainText("ProcessResult");

  // Run now: nothing to confirm, the task stays queued and is no longer
  // read as stuck.
  await queued.getByRole("button", { name: "Run now", exact: true }).click();
  await expect(queued).toHaveCount(0);

  // Cancel closes the task, so it asks first; dismissing it changes
  // nothing.
  const cancel = running.getByRole("button", { name: "Cancel", exact: true });
  page.once("dialog", (d) => d.dismiss());
  await cancel.click();
  await expect(running).toHaveCount(1);
  page.once("dialog", (d) => d.accept());
  await cancel.click();
  await expect(running).toHaveCount(0);

  await page.reload();
  await expect(
    page.locator(".inbox-item", { hasText: "Backfill the audit log" }),
  ).toHaveCount(0);
  await expect(
    page.locator(".inbox-item", { hasText: "Regenerate the protobuf" }),
  ).toHaveCount(0);
});

// The rail's Inbox entry counts the config repository's sync failure, as
// the Inbox heading and its "settings are not being exported" row do.
// `grain demo` has no state repository to fail, so /api/config's answer
// is the one thing stubbed here.
test("the rail's Inbox count includes the config repository's sync failure", async ({
  page,
}) => {
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const config = await res.json();
    config.stateRepoSyncFailure = {
      since: "2026-10-29T09:00:00Z",
      error: "git fetch origin: authentication required",
    };
    await route.fulfill({ response: res, json: config });
  });
  // Compared with the heading's own count rather than with a reading
  // taken before the stub: the rail's figure grows as the task list and
  // the notifications arrive, and a count read mid-load is not a
  // baseline (the first CI run of this test read 11 and then saw 15).
  await page.goto("/inbox");
  await expect(
    page.getByRole("heading", { name: "Settings are not being exported" }),
  ).toBeVisible();
  const heading = page.locator(".content-header .count").first();
  await expect(heading).toHaveText(/^\d+$/);
  await expect(async () => {
    const want = (await heading.innerText()).trim();
    await expect(page.getByRole("button", { name: /^Inbox \d+/ })).toHaveText(
      new RegExp(`^Inbox\\s*${want}$`),
      { timeout: 1000 },
    );
  }).toPass();
});
