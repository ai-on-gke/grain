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

// A well-formed base branch the repo does not have (grain/task-1620).
// The forms warn about it as soon as GitHub's listing says it is not
// there; the server refuses it only for a task nothing holds back.
//
// No stub: `grain demo -github-sim` serves a simulated GitHub
// (pkg/github/githubsim) behind the real GitHub client, whose
// acme/widgets has branches main and release/1.0 in a real bare repo.
// Everything from grain's github-branches endpoint inward is the real
// server.
test.use({ demoArgs: ["-github-sim"] });

const missingWarning = (form) =>
  `No branch "no-such-branch" on acme/widgets: ${form}`;

test("the New task form warns about a missing base and the server refuses it", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();

  await page.getByRole("button", { name: "+ New task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  const title = `Missing base, refused ${Date.now()}`;
  await page.getByLabel("Title").fill(title);
  await page.getByLabel(/^Repos/).selectOption("acme/widgets");

  const base = page.getByLabel("Base branch");
  await base.fill("no-such-branch");
  await expect(
    page.getByText(
      missingWarning(
        "push it first, or have the task depend on the one that cuts it",
      ),
    ),
  ).toBeVisible();

  // The warning does not stop Create; the server does. A one-shot task
  // that waits on nothing can only fail at dispatch on a missing base.
  const refused = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/tasks") &&
      response.request().method() === "POST",
    { timeout: 15_000 },
  );
  await page.getByRole("button", { name: "Create task" }).click();
  const refusal = await refused;
  expect(refusal.status()).toBe(400);
  expect((await refusal.json()).error).toContain(
    'base "no-such-branch" does not exist on acme/widgets',
  );
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

  // A branch the repo has clears the warning, and the same form files.
  await base.fill("release/1.0");
  await expect(page.getByText(/No branch "/)).toHaveCount(0);
  const created = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/tasks") &&
      response.request().method() === "POST",
    { timeout: 15_000 },
  );
  await page.getByRole("button", { name: "Create task" }).click();
  expect((await created).status()).toBe(201);
});

// Schedules and templates are written ahead of the release branches they
// name, so their forms warn and the server accepts.
test("the schedule form warns about a missing base but saves it", async ({
  page,
}) => {
  await page.goto("/schedules");
  await expect(page.getByRole("heading", { name: "Processes" })).toBeVisible();
  await page.getByRole("button", { name: "+ New schedule" }).click();
  await expect(
    page.getByRole("heading", { name: "New schedule" }),
  ).toBeVisible();

  const title = `Missing base schedule ${Date.now()}`;
  await page.getByLabel("Title", { exact: true }).fill(title);
  await page.getByLabel("Repeat").click();
  await page.getByRole("option", { name: "Every N hours" }).click();
  await page.getByLabel("Every N hours").fill("24");
  await page
    .getByLabel("Description", { exact: true })
    .fill("Runs against a release branch that is not cut yet.");
  await page.locator("select[name='repo']").selectOption("acme/widgets");
  await page.getByLabel("Base branch").fill("no-such-branch");
  await expect(
    page.getByText(missingWarning("each firing fails until it is cut")),
  ).toBeVisible();

  const saved = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/schedules") &&
      response.request().method() === "POST",
    { timeout: 15_000 },
  );
  await page.getByRole("button", { name: "Add schedule" }).click();
  expect((await saved).status()).toBe(201);
  await expect(
    page.locator(".process-row-schedule", { hasText: title }),
  ).toBeVisible();
});

test("the template form warns about a missing base but saves it", async ({
  page,
}) => {
  await page.goto("/templates");
  await page.getByRole("button", { name: "+ New template" }).click();
  await expect(
    page.getByRole("heading", { name: "New template" }),
  ).toBeVisible();

  const name = `missing-base-${Date.now()}`;
  await page.locator('input[name="name"]').fill(name);
  await page.locator('input[name="title"]').fill("Release follow-up");
  await page
    .locator('textarea[name="description"]')
    .fill("Filed against a release branch that is not cut yet.");
  await page
    .locator('select[name="repo"]')
    .first()
    .selectOption("acme/widgets");
  const base = page.locator('input[name="base"]').first();
  await base.fill("no-such-branch");
  await expect(
    page.getByText(
      missingWarning("tasks filed from this template fail until it is cut"),
    ),
  ).toBeVisible();

  const saved = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/templates") &&
      response.request().method() === "POST",
    { timeout: 15_000 },
  );
  await page.getByRole("button", { name: "Add template" }).click();
  expect((await saved).status()).toBe(201);
  await expect(
    page.locator(".process-row-template", { hasText: name }),
  ).toBeVisible();
});

// The same refusal over the API alone, with no form in front of it: what
// a CLI or an agent filing a task gets (docs/test-plans/2026-10-06.md §10).
test("POST /api/tasks refuses a missing base on acme/widgets and files a present one", async ({
  page,
}) => {
  await page.goto("/");
  const refused = await page.request.post("/api/tasks", {
    data: {
      title: `Missing base over the API ${Date.now()}`,
      description: "Filed straight at the API.",
      repo: "acme/widgets",
      base: "nope",
    },
  });
  expect(refused.status()).toBe(400);
  const { error } = await refused.json();
  expect(error).toContain('base "nope" does not exist on acme/widgets');
  test.info().annotations.push({ type: "refusal", description: error });

  const filed = await page.request.post("/api/tasks", {
    data: {
      title: `Present base over the API ${Date.now()}`,
      description: "Filed straight at the API.",
      repo: "acme/widgets",
      base: "release/1.0",
    },
  });
  expect(filed.status()).toBe(201);
});
