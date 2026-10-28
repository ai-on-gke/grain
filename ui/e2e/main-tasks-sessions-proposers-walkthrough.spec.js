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

// docs/test-plans/2026-10-06.md §9 (grain/task-2387,
// docs/test-runs/2026-10-07-main-tasks-sessions-proposers.md): the
// main-only task and session changes, walked through against a real
// `grain demo -github-sim`. The steps whose subject was people and teams
// (the User filter over tasks filed as several people, a second team's
// session) went with them (docs/single-tenant-reset.md, stage 3e-iii-c2). The only stub
// is GitHub: the demo's githubsim serves acme/widgets with main and
// release/1.0 out of a real bare repo. The demo runs no orchestrator, so
// the branch being cut and the held task being dispatched are followed
// in Go instead (pkg/orchestrator's pending-branch and concurrent
// dispatch tests).
//
// A demoArgs of its own gives this file a demo of its own: it cuts
// branches the shared seed is not built around.
test.use({
  demoArgs: ["-github-sim", "-default-target-repo", "acme/widgets"],
});
test.describe.configure({ mode: "serial" });
test.setTimeout(180_000);

async function ok(res, what) {
  expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
  return res.json();
}

test("New task -> Create a new branch records the request, and refuses what cannot be cut", async ({
  page,
}) => {
  const filed = () =>
    page.waitForResponse(
      (r) => r.url().endsWith("/api/tasks") && r.request().method() === "POST",
      { timeout: 15_000 },
    );
  const open = async (title) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    await page.getByLabel("Title").fill(title);
    await page.getByLabel(/^Repos/).selectOption("acme/widgets");
    await page.getByRole("checkbox", { name: "Create a new branch" }).check();
    await expect(page.getByLabel("Base branch")).toHaveCount(0);
  };

  await open("Cut feature/x from release/1.0");
  await page.getByTestId("new-branch-name").fill("feature/x");
  await page.getByLabel("Create from").fill("release/1.0");
  let res = filed();
  await page.getByRole("button", { name: "Create task" }).click();
  res = await res;
  expect(res.status()).toBe(201);
  const task = await res.json();
  expect(task.base).toBe("feature/x");
  const branches = await ok(
    await page.request.get("/api/repos/acme/widgets/branches"),
    "branches",
  );
  expect(branches).toContainEqual(
    expect.objectContaining({
      name: "feature/x",
      from: "release/1.0",
      status: "pending",
    }),
  );

  // A name git cannot have.
  await open("Invalid branch name");
  await page.getByTestId("new-branch-name").fill("a..b");
  res = filed();
  await page.getByRole("button", { name: "Create task" }).click();
  res = await res;
  expect(res.status()).toBe(400);
  expect((await res.json()).error).toContain(
    'base "a..b" is not a usable branch name',
  );
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

  // A "Create from" the repo does not have: the form warns, the server
  // refuses.
  await page.getByTestId("new-branch-name").fill("feature/y");
  await page.getByLabel("Create from").fill("no-such-branch");
  await expect(
    page.getByText(/No branch "no-such-branch" on acme\/widgets/),
  ).toBeVisible();
  res = filed();
  await page.getByRole("button", { name: "Create task" }).click();
  res = await res;
  expect(res.status()).toBe(400);
  expect((await res.json()).error).toContain(
    'base "no-such-branch" does not exist on acme/widgets',
  );

  // The same pending name from another base is refused rather than cut
  // from the first request's base behind this task's back.
  await page.getByTestId("new-branch-name").fill("feature/x");
  await page.getByLabel("Create from").fill("main");
  res = filed();
  await page.getByRole("button", { name: "Create task" }).click();
  res = await res;
  expect(res.status()).toBe(400);
  expect((await res.json()).error).toContain(
    'branch "feature/x" is already waiting to be created on acme/widgets from "release/1.0"',
  );

  // A name already on GitHub is used as it stands, as the hint says.
  await page.getByTestId("new-branch-name").fill("release/1.0");
  await page.getByLabel("Create from").fill("main");
  res = filed();
  await page.getByRole("button", { name: "Create task" }).click();
  res = await res;
  expect(res.status()).toBe(201);
  expect((await res.json()).base).toBe("release/1.0");
});

test("Sessions: the session listed with its state, and none counted under Asked", async ({
  page,
}) => {
  await page.goto("/sessions");
  const director = page.getByRole("button", { name: /^Open session Director/ });
  await expect(director.first()).toBeVisible();
  // The demo's Director task is queued: "Starting".
  await expect(
    page.getByRole("listitem").filter({ has: director }).first(),
  ).toContainText("Starting");

  const tasks = await ok(await page.request.get("/api/tasks"), "tasks");
  const awaiting = tasks.filter(
    // "Asked" counts both asked states, sessions left out.
    (t) =>
      (t.state === "awaiting_reply" || t.state === "awaiting_submit") &&
      !t.consoleSession &&
      !t.interactive,
  ).length;
  const rail = page.locator("aside").getByRole("button", { name: /^Asked/ });
  await expect(rail.first()).toHaveText(new RegExp(`${awaiting}\\s*$`));

  // The demo's Director is never a task row, whatever the list asks
  // (taskFilters.js's isSessionTask), and is absent from the rail's
  // Asked count (grain/task-2473).
  await page.goto("/?f=state:none:closed");
  await expect(page.getByPlaceholder("Search tasks…")).toBeVisible();
  await expect(page.locator(".task-row").first()).toBeVisible();
  await expect(
    page.locator(".task-row", { hasText: "Director Session" }),
  ).toHaveCount(0);
  await expect(rail.first()).toHaveText(new RegExp(`${awaiting}\\s*$`));
});
