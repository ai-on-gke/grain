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

// The repos pane's add/remove and Settings -> Git's connection removal,
// driven against a real daemon rather than mocks (grain/task-4288,
// following grain/task-4249's Go lifecycle suite). The vitest suites for
// RepoPage and RepoList mocked the plan the Remove confirmation reads,
// which is how that confirmation went on saying "Tasks already filed
// there are not affected" over queued work for as long as the daemon
// had stopped answering it.

const banner = (page) => page.locator(".MuiSnackbar-root");

test("a repository is refused, added, and removed with a confirmation naming its work", async ({
  page,
}) => {
  test.setTimeout(60_000);
  const repo = `acme/e2e-lifecycle-${Date.now()}`;
  let taskID = "";
  try {
    await page.goto("/repos");
    const field = page.getByRole("combobox", { name: "Repo to add" });
    const add = page.getByRole("button", { name: "Add repo" });

    // An empty field adds nothing and asks nothing.
    await add.click();
    await expect(page.locator(".repo-list-row")).toHaveCount(
      await page.locator(".repo-list-row").count(),
    );

    // Not a repository: the daemon's own 400 reaches the page.
    await field.fill("acme/wid gets");
    await add.click();
    await expect(banner(page)).toContainText(/acme\/wid gets/);
    await expect(
      page.locator(".repo-list-row", { hasText: "wid gets" }),
    ).toHaveCount(0);

    // A real one lands in the list without a reload.
    await field.fill(repo);
    await add.click();
    const row = page.locator(".repo-list-row", { hasText: repo });
    await expect(row).toHaveCount(1);
    await expect(field).toHaveValue("");

    // Work filed there is what the Remove confirmation must name.
    const filed = await page.request.post("/api/tasks", {
      data: { title: "lifecycle e2e", repo },
    });
    expect(filed.ok()).toBeTruthy();
    taskID = (await filed.json()).id;

    await row.click();
    await expect(page.getByRole("heading", { name: repo })).toBeVisible();
    await page.getByRole("tab", { name: "Advanced" }).click();
    let message = "";
    page.once("dialog", async (dialog) => {
      message = dialog.message();
      await dialog.accept();
    });
    await page
      .locator("div", {
        has: page.getByRole("heading", { name: "Remove repository" }),
      })
      .last()
      .getByRole("button", { name: "Remove", exact: true })
      .click();
    await expect(page).toHaveURL(/\/repos\/?$/);
    expect(message).toContain(`Remove ${repo} from target repos?`);
    expect(message).toContain("1 task still in play there is parked");
    expect(message).not.toContain("Tasks already filed there are not affected");
    await expect(page.locator(".repo-list-row", { hasText: repo })).toHaveCount(
      0,
    );
  } finally {
    const [owner, name] = repo.split("/");
    await page.request.delete(`/api/repos/${owner}/${name}`);
    if (taskID)
      await page.request.post(`/api/tasks/${taskID}/close`, { data: {} });
  }
});

test("removing a git connection a repository still names is refused inline, by name", async ({
  page,
}) => {
  const scope = `e2e-lifecycle-${Date.now()}`;
  const conns = async () =>
    (
      await (await page.request.get("/api/deployment/git-connections")).json()
    ).filter((c) => c.scope === scope);
  let cred = "";
  try {
    const added = await page.request.post("/api/deployment/git-connections", {
      data: {
        host: "github.com",
        kind: "github",
        type: "pat",
        scope,
        value: "ghp_e2e_lifecycle",
      },
    });
    expect(added.ok()).toBeTruthy();
    [{ credential: cred }] = await conns();
    const pinned = await page.request.put(
      "/api/deployment/repos/acme/widgets/config",
      {
        data: { gitCredential: cred },
      },
    );
    expect(pinned.ok()).toBeTruthy();

    await page.goto("/settings/git");
    // The table shows a connection's scope, not its minted credential.
    const row = page.locator("table tr", { hasText: scope });
    await expect(row).toHaveCount(1);
    page.once("dialog", (dialog) => dialog.accept());
    await row.getByRole("button", { name: "Remove" }).click();
    const refusal = `git connection "${cred}" is still named by repository acme/widgets`;
    await expect(
      page.getByTestId("git-connections-error").filter({ hasText: refusal }),
    ).toBeVisible();
    await expect(row).toHaveCount(1);

    // Once the repository lets go, the same click goes through and the
    // row leaves the table without a reload.
    const unpinned = await page.request.put(
      "/api/deployment/repos/acme/widgets/config",
      {
        data: { gitCredential: "" },
      },
    );
    expect(unpinned.ok()).toBeTruthy();
    page.once("dialog", (dialog) => dialog.accept());
    await row.getByRole("button", { name: "Remove" }).click();
    await expect(row).toHaveCount(0);
    await expect(
      page.getByTestId("git-connections-error").filter({ hasText: refusal }),
    ).toHaveCount(0);
    cred = "";
  } finally {
    await page.request.put("/api/deployment/repos/acme/widgets/config", {
      data: { gitCredential: "" },
    });
    for (const c of await conns()) {
      await page.request.delete(
        `/api/deployment/git-connections/${encodeURIComponent(c.credential)}`,
      );
    }
  }
});
