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

// A well-formed base branch the repo does not have (grain/task-1620),
// on a demo with no GitHub behind it: `grain demo` without -github-sim
// wires no GitHub client, so its github-branches endpoint answers "not
// available". base-branch-existence.spec.js walks the same forms with
// a simulated GitHub that can be read.
//
// With GitHub unreadable -- the demo's own state, unstubbed -- grain
// cannot tell a missing branch from a present one, so it neither warns
// nor refuses: the write is accepted and dispatch stays the backstop.
// Schedules accept a missing base whatever GitHub says, since they are
// written ahead of the release branches they name.
test("a missing base is accepted when GitHub cannot be read, with no warning", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();

  await page.getByRole("button", { name: "+ New task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  const title = `Missing base, GitHub unreadable ${Date.now()}`;
  await page.getByLabel("Title").fill(title);
  await page.getByLabel(/^Repos/).selectOption("acme/widgets");
  const refs = page.waitForResponse((response) =>
    response.url().endsWith("/api/repos/acme/widgets/github-branches"),
  );
  await page.getByLabel("Base branch").fill("no-such-branch");
  expect((await refs).status()).toBe(404);
  await expect(page.getByText(/No branch "/)).toHaveCount(0);

  const created = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/tasks") &&
      response.request().method() === "POST",
    { timeout: 15_000 },
  );
  await page.getByRole("button", { name: "Create task" }).click();
  expect((await created).status()).toBe(201);

  const schedule = await page.request.post("/api/schedules", {
    data: {
      title: `Release nightly ${Date.now()}`,
      repo: "acme/widgets",
      base: "release/9.9",
      recurrence: { kind: "everyNHours", everyNHours: 24 },
    },
  });
  expect(schedule.status()).toBe(201);
});
