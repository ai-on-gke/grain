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

// docs/test-runs/2026-11-09-repository-release-memory-ui.md (task 3716).
// Git allows "#" in a branch name (fix/#123 is a common one), and the
// compare picker offers whatever branches the repository has. The
// compare page's path wrote the ref plainly, so in-app the comparison
// opened, but the address bar held a "#" that a reload -- or anybody the
// link was sent to -- read as a fragment, and the page compared fix/
// instead: "GitHub has nothing to compare".
test.setTimeout(90_000);
// A server of its own: this adds a branch to acme/widgets, which the
// compare picker specs on a shared -github-sim server list.
test.use({ demoArgs: ["-github-sim", "-as", "compare-hash"] });

const SIM = "/api/e2e/github-sim/repos/acme/widgets";
const BRANCH = "fix/#3716-hash";

// A branch somebody pushed to GitHub, made through the sim's own git
// database API: grain's Add branch refuses a "#" in a branch it creates,
// but cannot stop one existing. The second test finds it already there.
async function pushBranch(request) {
  const main = await request.get(`${SIM}/branches/main`);
  expect(main.ok()).toBe(true);
  const sha = (await main.json()).commit.sha;
  const made = await request.post(`${SIM}/git/refs`, {
    data: { ref: `refs/heads/${BRANCH}`, sha },
  });
  const text = await made.text();
  expect(
    made.status() === 201 || /already exists/.test(text),
    `${made.status()} ${text}`,
  ).toBe(true);
}

for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`a compare of a branch with # in its name survives a reload (${name})`, async ({
    page,
  }) => {
    const errors = [];
    page.on("pageerror", (err) => errors.push(String(err)));
    await page.setViewportSize(viewport);
    await pushBranch(page.request);
    await page.goto("/repos/acme/widgets/settings/branches");

    const head = page.getByRole("textbox", { name: "Head branch" });
    await head.click();
    await page.getByRole("menuitem", { name: BRANCH }).click();
    await page.getByRole("button", { name: "Compare", exact: true }).click();

    await expect(page).toHaveURL(
      /\/repos\/acme\/widgets\/compare\/main\.\.\.fix\/%233716-hash$/,
    );
    const url = new URL(page.url());
    expect(url.hash).toBe("");
    expect(url.search).toBe("");
    const same = page.getByText("There are no changes between these branches.");
    await expect(same).toBeVisible();

    for (const open of [() => page.reload(), () => page.goto(url.href)]) {
      await open();
      await expect(same).toBeVisible();
      await expect(page.getByText(/GitHub has nothing to compare/)).toHaveCount(
        0,
      );
    }
    const width = await page.evaluate(() => ({
      scroll: document.documentElement.scrollWidth,
      client: document.documentElement.clientWidth,
    }));
    expect(width.scroll).toBeLessThanOrEqual(width.client);
    expect(errors).toEqual([]);
  });
}
