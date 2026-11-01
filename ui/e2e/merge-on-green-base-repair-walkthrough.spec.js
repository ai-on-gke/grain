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

// docs/test-plans/2026-10-25.md §3 walkthrough (task 929): the two
// repository switches the merge queue reads -- merge_on_green
// (model.RepoConfig.MergeOnGreen) and base_repair_bypasses_human_gates
// (model.RepoConfig.BaseRepairBypassesHumanGates) -- toggled in a real
// browser from both places they are offered, each saved with its own PUT,
// and still there after a reload. Both are repository-wide: there is no
// per-branch override of either, so none is offered.
const repo = "acme/widgets";
const greenLabel = `Merge pull requests into ${repo} when green and clean`;
const repoBypassLabel = `Base-branch repair PRs in ${repo} may merge without a human when their CI is green`;
const settingsBypassLabel = `${repo}: Base-branch repair PRs may merge without a human when their CI is green`;

async function reset(page) {
  await page.request.put(`/api/repos/${repo}/merge-on-green`, {
    data: { mergeOnGreen: false },
  });
  await page.request.put(
    `/api/repos/${repo}/base-repair-bypasses-human-gates`,
    {
      data: { baseRepairBypassesHumanGates: false },
    },
  );
}

// toggle clicks box and returns the PUT it sent and the body it answered.
// Turning the bypass on asks first (confirmBaseRepairBypass); toggle
// accepts, and records what it asked in asked.
async function toggle(page, box, route, on) {
  const asked = [];
  const answer = (d) => {
    asked.push(d.message());
    d.accept();
  };
  page.on("dialog", answer);
  const put = page.waitForResponse(
    (r) =>
      r.request().method() === "PUT" &&
      r.url().endsWith(`/api/repos/${repo}/${route}`),
  );
  // click() rather than check(): the tick only follows the server's
  // answer, after the click (see merge-on-green.spec.js).
  if (on) await expect(box).not.toBeChecked();
  else await expect(box).toBeChecked();
  await box.click();
  const res = await put;
  if (on) await expect(box).toBeChecked();
  else await expect(box).not.toBeChecked();
  page.off("dialog", answer);
  expect(res.ok()).toBe(true);
  if (on && route === "base-repair-bypasses-human-gates") {
    expect(asked).toHaveLength(1);
    expect(asked[0]).toContain(repo);
    expect(asked[0]).toContain("trust-boundary code");
  } else {
    expect(asked).toEqual([]);
  }
  return { sent: res.request().postDataJSON(), got: await res.json() };
}

// dismissBypass clicks the off bypass box, dismisses the confirmation, and
// checks that no PUT went out and the box stayed off.
async function dismissBypass(page, box) {
  const puts = [];
  const onRequest = (r) => {
    if (
      r.method() === "PUT" &&
      r.url().endsWith(`/api/repos/${repo}/base-repair-bypasses-human-gates`)
    )
      puts.push(r);
  };
  page.on("request", onRequest);
  const dialog = page.waitForEvent("dialog");
  await expect(box).not.toBeChecked();
  await box.click();
  const d = await dialog;
  expect(d.type()).toBe("confirm");
  expect(d.message()).toContain(repo);
  await d.dismiss();
  await expect(box).not.toBeChecked();
  // Give a stray PUT the chance to show itself before saying none went.
  await page.waitForTimeout(300);
  page.off("request", onRequest);
  expect(puts).toEqual([]);
  const stored = await (
    await page.request.get(
      `/api/repos/${repo}/base-repair-bypasses-human-gates`,
    )
  ).json();
  expect(stored.baseRepairBypassesHumanGates).toBe(false);
}

test("the repository page's merge-queue switches save and survive a reload", async ({
  page,
}) => {
  try {
    await reset(page);
    await page.goto(`/repos/${repo}/settings/merge-queue`);
    const green = page.getByLabel(greenLabel);
    const bypass = page.getByLabel(repoBypassLabel);
    await expect(green).not.toBeChecked();
    await expect(bypass).not.toBeChecked();

    const g = await toggle(page, green, "merge-on-green", true);
    expect(g.sent).toEqual({ mergeOnGreen: true });
    expect(g.got.mergeOnGreen).toBe(true);
    const b = await toggle(
      page,
      bypass,
      "base-repair-bypasses-human-gates",
      true,
    );
    expect(b.sent).toEqual({ baseRepairBypassesHumanGates: true });
    expect(b.got.baseRepairBypassesHumanGates).toBe(true);
    // Turning the second on must not have turned the first back off.
    expect(b.got.mergeOnGreen).toBe(true);

    await page.reload();
    await expect(page.getByLabel(greenLabel)).toBeChecked();
    await expect(page.getByLabel(repoBypassLabel)).toBeChecked();

    // The Overview's summary rows read the same stored row.
    await page.goto(`/repos/${repo}`);
    const summary = page.getByText("Merge when green", { exact: true });
    await expect(summary).toBeVisible();
    await expect(
      page.getByText("Base repair bypass", { exact: true }),
    ).toBeVisible();

    // And Settings > Repositories shows the bypass the repo page set.
    await page.goto(`/settings/repositories`);
    await expect(page.getByLabel(settingsBypassLabel)).toBeChecked();

    // Off again from the repository page, one switch at a time.
    await page.goto(`/repos/${repo}/settings/merge-queue`);
    const off = await toggle(
      page,
      page.getByLabel(greenLabel),
      "merge-on-green",
      false,
    );
    expect(off.got.mergeOnGreen).toBe(false);
    expect(off.got.baseRepairBypassesHumanGates).toBe(true);
    await page.reload();
    await expect(page.getByLabel(greenLabel)).not.toBeChecked();
    await expect(page.getByLabel(repoBypassLabel)).toBeChecked();
  } finally {
    await reset(page);
  }
});

test("Settings > Repositories' bypass tick saves and survives a reload", async ({
  page,
}) => {
  try {
    await reset(page);
    await page.goto(`/settings/repositories`);
    const box = page.getByLabel(settingsBypassLabel);
    await expect(box).toBeEnabled();
    await expect(box).not.toBeChecked();

    const on = await toggle(
      page,
      box,
      "base-repair-bypasses-human-gates",
      true,
    );
    expect(on.sent).toEqual({ baseRepairBypassesHumanGates: true });
    expect(on.got.baseRepairBypassesHumanGates).toBe(true);
    await page.reload();
    await expect(page.getByLabel(settingsBypassLabel)).toBeChecked();
    const stored = await (
      await page.request.get(
        `/api/repos/${repo}/base-repair-bypasses-human-gates`,
      )
    ).json();
    expect(stored.baseRepairBypassesHumanGates).toBe(true);

    await page.goto(`/repos/${repo}/settings/merge-queue`);
    await expect(page.getByLabel(repoBypassLabel)).toBeChecked();
    await expect(page.getByLabel(greenLabel)).not.toBeChecked();

    await page.goto(`/settings/repositories`);
    const off = await toggle(
      page,
      page.getByLabel(settingsBypassLabel),
      "base-repair-bypasses-human-gates",
      false,
    );
    expect(off.got.baseRepairBypassesHumanGates).toBe(false);
    await page.reload();
    await expect(page.getByLabel(settingsBypassLabel)).not.toBeChecked();
  } finally {
    await reset(page);
  }
});

test("dismissing the bypass confirmation sends nothing and leaves it off", async ({
  page,
}) => {
  try {
    await reset(page);
    await page.goto(`/repos/${repo}/settings/merge-queue`);
    await dismissBypass(page, page.getByLabel(repoBypassLabel));
    await page.reload();
    await expect(page.getByLabel(repoBypassLabel)).not.toBeChecked();

    await page.goto(`/settings/repositories`);
    const box = page.getByLabel(settingsBypassLabel);
    await expect(box).toBeEnabled();
    await dismissBypass(page, box);
    await page.reload();
    await expect(page.getByLabel(settingsBypassLabel)).not.toBeChecked();
  } finally {
    await reset(page);
  }
});
