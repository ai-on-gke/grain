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

// docs/test-plans/2026-10-15.md §3, steps 2 and 6, in a real browser: the
// repository page's tabs by their URL hashes and through Back/Forward,
// an unsaved CI edit kept across a tab change with the leave prompt still
// standing, and the CI tab's red dot while the base-branch gate is
// unjudged (docs/test-runs/2026-10-15-repo-tabs-standing-work.md). Step
// 6, every write disabled and refused for a member who is not a repo
// admin and for a viewer, went with team roles
// (docs/single-tenant-reset.md, 3e-iii-c2).
//
// A server of its own on `grain demo -github-sim`: the unjudged gate is
// the one a demo with no grain CI wired produces (see
// base-branch-gate-warnings-walkthrough.spec.js), and -github-sim is the
// demo's GitHub stub.
test.use({ demoArgs: ["-github-sim"] });
test.describe.configure({ mode: "serial" });
test.setTimeout(180_000);

const repo = "acme/widgets";
const TABS = [
  ["overview", "Overview"],
  ["ci", "CI"],
  ["merge-queue", "Merge queue"],
  ["agents", "Agents"],
  ["branches", "Branches"],
  ["releases", "Releases"],
  ["advanced", "Advanced"],
];
const PROMPT = /unsaved changes/;

async function onTab(page, id, label) {
  await expect(
    page.getByRole("tab", { name: label, exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(`#repo-tabpanel-${id}`)).toBeVisible();
}

// A tab's own address is /repos/<o>/<n>/settings/<tab>, Overview's the
// page's (docs/design/settings-panel.md §2.3).
const tabURL = (id) =>
  new RegExp(`/repos/${repo}${id === "overview" ? "" : `/settings/${id}`}$`);

test("2. every tab by its path, Back and Forward between them", async ({
  page,
}) => {
  for (const [id, label] of TABS) {
    await page.goto(
      id === "overview" ? `/repos/${repo}` : `/repos/${repo}/settings/${id}`,
    );
    await onTab(page, id, label);
    await expect(page).toHaveURL(tabURL(id));
  }
  for (const [id, label] of [...TABS].reverse().slice(1)) {
    await page.goBack();
    await onTab(page, id, label);
  }
  for (const [id, label] of TABS.slice(1)) {
    await page.goForward();
    await onTab(page, id, label);
  }
  // A tab picked on the page is put in the address, for a link to share.
  await page.getByRole("tab", { name: "Merge queue" }).click();
  await expect(page).toHaveURL(tabURL("merge-queue"));
});

test("2. a link from when the tab rode in the hash opens that tab, at its path", async ({
  page,
}) => {
  await page.goto(`/repos/${repo}#branches`);
  await onTab(page, "branches", "Branches");
  await expect(page).toHaveURL(tabURL("branches"));
});

test("2. a half-written CI workflow survives a look at Releases, and leaving asks first", async ({
  page,
}) => {
  const dialogs = [];
  page.on("dialog", (d) => dialogs.push(d.message()));
  await page.goto(`/repos/${repo}#ci`);
  await page.getByRole("button", { name: "Add workflow" }).click();
  const file = page.getByRole("textbox", { name: "File", exact: true });
  await file.fill("lint.yml");

  await page.getByRole("tab", { name: "Releases" }).click();
  await onTab(page, "releases", "Releases");
  await page.getByRole("tab", { name: "CI", exact: true }).click();
  await expect(file).toHaveValue("lint.yml");

  page.once("dialog", (d) => d.dismiss());
  await page.getByRole("button", { name: "← Repos" }).click();
  await expect.poll(() => dialogs.length).toBe(1);
  expect(dialogs[0]).toMatch(PROMPT);
  await expect(file).toHaveValue("lint.yml");

  page.once("dialog", (d) => d.dismiss());
  await page.getByRole("button", { name: "Releases", exact: true }).click();
  await expect.poll(() => dialogs.length).toBe(2);
  expect(dialogs[1]).toMatch(PROMPT);
  await expect(page).toHaveURL(tabURL("ci"));

  await page.getByRole("tab", { name: "Branches", exact: true }).click();
  await page.getByLabel("Head branch").fill("feature");
  page.once("dialog", (d) => d.dismiss());
  await page.getByRole("button", { name: "Compare" }).click();
  await expect.poll(() => dialogs.length).toBe(3);
  expect(dialogs[2]).toMatch(PROMPT);
  await expect(page).toHaveURL(tabURL("branches"));
  await page.getByRole("tab", { name: "CI", exact: true }).click();

  // Removed, there is nothing to lose and leaving asks nothing.
  await page.getByRole("button", { name: "Remove workflow lint.yml" }).click();
  await page.getByRole("button", { name: "← Repos" }).click();
  await expect(file).toHaveCount(0);
  expect(dialogs).toHaveLength(3);
});

test("2. the CI tab carries a red dot while the gate is unjudged, and Go to CI Mode opens it", async ({
  page,
}) => {
  const watch = await page.request.put(
    `/api/repos/${repo}/watch-default-branch`,
    { data: { watchDefaultBranch: true } },
  );
  expect(watch.ok(), await watch.text()).toBe(true);
  const gates = await (await page.request.get("/api/base-branch-gates")).json();
  expect(gates.gates).toEqual([
    expect.objectContaining({ repo, judged: false, reason: "grain-ci-off" }),
  ]);

  await page.goto(`/repos/${repo}`);
  const ciTab = page.getByRole("tab", { name: "CI", exact: true });
  await expect(ciTab.locator(".MuiBadge-dot")).toBeVisible();
  await page.getByRole("button", { name: "Go to CI Mode" }).click();
  await onTab(page, "ci", "CI");
  await expect(page).toHaveURL(tabURL("ci"));
  await expect(
    page.getByRole("radio", {
      name: /Make grain CI the gate for acme\/widgets/,
    }),
  ).toBeInViewport();

  const unwatch = await page.request.put(
    `/api/repos/${repo}/watch-default-branch`,
    { data: { watchDefaultBranch: false } },
  );
  expect(unwatch.ok()).toBe(true);
  await page.reload();
  await expect(ciTab.locator(".MuiBadge-dot")).toHaveCount(0);
});
