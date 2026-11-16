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

// What the 2026-11-16 repository walkthrough found on /next/
// (docs/test-runs/2026-11-16-repository-release-memory-ui.md), over a
// real `grain demo` with its GitHub simulation:
//
// - a refused repo setting's banner stayed up after the field was fixed
//   and saved;
// - on Repos, tapping a second handle while a row was picked up only
//   picked that row up instead, so a finger could not reorder at all;
// - the repo page had no New task, so filing into a repository meant
//   choosing it again in the sheet;
// - the Tasks page's tab counts ignored ?repo=, counting the whole
//   deployment over a narrowed list.

import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification"] });

const R = "/next/repos/acme/widgets";

async function withRepos(page, repos, fn) {
  for (const repo of repos) {
    const res = await page.request.post("/api/repos", { data: { repo } });
    expect(res.ok()).toBe(true);
  }
  try {
    await fn();
  } finally {
    for (const repo of repos) await page.request.delete(`/api/repos/${repo}`);
  }
}

test("a refused setting's banner clears once that setting saves cleanly @phone", async ({
  page,
}) => {
  const caps = page.getByLabel("Default capabilities (comma-separated)");
  const save = page.getByRole("button", { name: "Save agent settings" });
  const banner = page.getByTestId("repo-error-banner");
  try {
    await page.goto(`${R}/agents`);
    await caps.fill("not-a-real-capability-e2e");
    await save.click();
    await expect(banner).toContainText("unknown capability");

    // Another section saving cleanly is not this one's answer. The
    // refused value is still unsaved, so leaving the tab asks first.
    await page.getByTestId("repo-tab-merge-queue").click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "Discard" }).click();
    const upToDate = page.getByRole("switch", {
      name: "Require branch up to date",
    });
    const was = await upToDate.getAttribute("aria-checked");
    await upToDate.click();
    await expect(upToDate).not.toHaveAttribute("aria-checked", was);
    await expect(banner).toBeVisible();
    await upToDate.click();
    await expect(upToDate).toHaveAttribute("aria-checked", was);

    await page.getByTestId("repo-tab-agents").click();
    await caps.fill("");
    await save.click();
    await expect(banner).toHaveCount(0);
  } finally {
    await page.request.put("/api/repos/acme/widgets/capabilities", {
      data: { defaultCapabilities: [] },
    });
  }
});

test("a second handle tap puts the picked-up repo in that row's place @phone", async ({
  page,
}) => {
  const A = "acme/tap-a-e2e";
  const B = "acme/tap-b-e2e";
  const names = () =>
    page
      .getByRole("list", { name: "Repos" })
      .getByRole("listitem")
      .evaluateAll((lis) => lis.map((li) => li.getAttribute("data-repo")));
  await withRepos(page, [A, B], async () => {
    await page.goto("/next/repos");
    await expect.poll(names).toHaveLength(3);
    const before = await names();
    const last = before[2];
    const first = before[0];

    await page.locator(`[data-handle="${last}"]`).click();
    const hint = page.getByTestId("repos-pick-hint");
    await expect(hint).toContainText(`Moving ${last}`);
    const vw = page.viewportSize().width;
    const box = await hint.boundingBox();
    expect(box.x + box.width).toBeLessThanOrEqual(vw);

    await page.locator(`[data-handle="${first}"]`).click();
    await expect(hint).toHaveCount(0);
    const after = [last, before[0], before[1]];
    await expect.poll(names).toEqual(after);
    await expect(page.locator(`[data-handle="${last}"]`)).toBeFocused();
    await expect(page.getByTestId("repos-announcer")).toHaveText(
      `${last} moved to position 1 of 3`,
    );
    await page.reload();
    await expect.poll(names).toEqual(after);

    // Tapping the held row's own handle leaves it where it is.
    await page.locator(`[data-handle="${before[0]}"]`).click();
    await page.locator(`[data-handle="${before[0]}"]`).click();
    await expect.poll(names).toEqual(after);
  });
});

test("New task on a repo page files into that repository @phone", async ({
  page,
}) => {
  const REPO = "acme/file-here-e2e";
  const title = `Filed from the repo page ${Date.now()}`;
  await withRepos(page, [REPO], async () => {
    await page.goto(`/next/repos/${REPO}`);
    const btn = page.getByTestId("repo-new-task-btn");
    await expect(btn).toBeVisible();
    const vw = page.viewportSize().width;
    const box = await btn.boundingBox();
    expect(box.x + box.width).toBeLessThanOrEqual(vw);
    await btn.click();
    const sheet = page.getByRole("dialog").last();
    await expect(sheet.getByText(REPO, { exact: true })).toBeVisible();
    await sheet.getByLabel(/^Title/).fill(title);
    await sheet.getByRole("button", { name: "Create and start" }).click();
    await expect(sheet).toHaveCount(0);
    await expect
      .poll(async () => {
        const res = await page.request.get(
          `/api/tasks?q=${encodeURIComponent(title)}`,
        );
        const body = await res.json();
        const list = Array.isArray(body) ? body : (body.tasks ?? []);
        return list.find((t) => t.title === title)?.repo ?? null;
      })
      .toBe(REPO);
  });
});

test("the Tasks tabs count what ?repo= narrows the list to", async ({
  page,
}) => {
  await page.goto("/next/tasks");
  const backlog = page.getByTestId("tab-backlog").locator(".tab-count");
  await expect(backlog).toHaveText(/^[1-9]\d*$/);
  await page.goto("/next/tasks?repo=nosuch/nothing-here-e2e");
  await expect(backlog).toHaveText("0");
  // Matching is case-insensitive, as the list's own is.
  await page.goto("/next/tasks?repo=acme/widgets");
  await expect(backlog).toHaveText(/^[1-9]\d*$/);
  const widgets = await backlog.innerText();
  await page.goto("/next/tasks?repo=ACME/Widgets");
  await expect(backlog).toHaveText(widgets);
});
