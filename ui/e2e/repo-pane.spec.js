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

// A repo has a page of its own (grain/task-111), at /repos/{owner}/
// {name}: the list is one plain row per repo, and everything about one
// repo -- its tasks, its branches, its default capabilities, its
// releases -- is on that repo's page. The list rows used to carry all of
// it as buttons, each depending on not also firing the row's own
// navigation, which is exactly the kind of real-click, real-DOM behavior
// tasks.spec.js's own doc comment says jsdom component tests can't be
// trusted to catch; the same goes for the page those clicks land on now.

test("opens a repo's own page from a plain list row and files a task scoped to it", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: /^Repos/ }).click();

  const row = page
    .locator(".repo-list li")
    .filter({ hasText: "acme/widgets" })
    .first();
  await expect(row).toBeVisible();
  const repoName = await row.locator(".repo-list-name").innerText();
  // Nothing on the row to press but the row itself and its drag handle,
  // which became a <button> in grain/task-23 so the order can be changed
  // by tapping as well as by dragging.
  await expect(row.locator("button")).toHaveCount(1);
  await expect(row.locator("button.task-drag-handle")).toHaveCount(1);

  await row.click();
  await expect(page.getByRole("heading", { name: repoName })).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/repos/${repoName}$`));
  // Tasks filed here are reachable from the count at the top, without
  // scrolling past the repo's forms.
  const tasksLink = page.getByRole("link", { name: /tasks? filed here/ });
  await expect(tasksLink).toBeVisible();
  await tasksLink.click();
  await expect(page.locator(".task-row").first()).toBeVisible();
  await page.goBack();
  await expect(page.getByRole("heading", { name: repoName })).toBeVisible();

  const title = `E2E repo-page task ${Date.now()}`;
  await page.getByRole("button", { name: "New task", exact: true }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  await expect(page.getByLabel(/^Repos/)).toHaveValue(repoName);
  await page.getByLabel("Title").fill(title);
  await page.getByRole("button", { name: "Create task" }).click();

  await expect(page.getByRole("heading", { name: repoName })).toBeVisible();
});

test("reaches a repo's branches, capabilities and releases from its page", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: /^Repos/ }).click();

  const row = page
    .locator(".repo-list li")
    .filter({ hasText: "acme/widgets" })
    .first();
  const repoName = await row.locator(".repo-list-name").innerText();
  await row.click();
  // The repo's own deployment-wide defaults, which are all the page
  // edits (single-tenant reset, stage 3e-ii).
  await page.getByRole("tab", { name: "Agents" }).click();

  // Each form is filled in by a read that has to have landed.
  await page.getByRole("tab", { name: "Branches" }).click();
  await expect(page.getByLabel("Branch name")).toBeVisible();
  await page.getByRole("tab", { name: "Agents" }).click();
  await expect(
    page.getByText(`A task filed against ${repoName} starts with:`),
  ).toBeVisible();
  await expect(
    page.getByLabel(new RegExp(`Prompt extension for ${repoName}`)),
  ).toBeVisible();

  // `exact`, because the release settings form on this same page offers
  // "Turn off automatic releases" when a repo has them on
  // (RepoReleaseWorkflowSettings.jsx), and a substring match on
  // "Releases" claims both buttons.
  await page.getByRole("button", { name: "Releases", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: `${repoName} releases` }),
  ).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/repos/${repoName}/releases$`));

  // Back out of releases lands on the repo page, not on the list, and on
  // the tab it was showing, which is in the path.
  await page.getByRole("button", { name: repoName }).click();
  await expect(page.getByRole("heading", { name: repoName })).toBeVisible();
  await expect(page).toHaveURL(
    new RegExp(`/repos/${repoName}/settings/agents$`),
  );
});
