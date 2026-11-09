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

// docs/test-runs/2026-11-08-repository-release-memory-ui.md (task 3613),
// bug 1. Switching "Standing work on this deployment" off on a repo's
// Releases tab stops the server judging its base branch, and GET
// /api/base-branch-gates drops the unjudged warning at once
// (pkg/ui/basebranchgate.go, baseCheckSwitchedOff). The page never asked
// the shell to re-read the gates, so the red warning stayed up until a
// reload or the shell's minute-long poll -- and switching standing work
// back on did not bring it back either.
test.setTimeout(120_000);
// A server of its own: this watches acme/widgets' default branch and
// flips its CI Mode and work ownership, which other files on a shared
// server would count. (The same flags as
// base-branch-gate-warnings-walkthrough.spec.js in another order, so the
// two files never share a worker's server.)
test.use({ demoArgs: ["-open=false", "-github-sim"] });

const repo = "acme/widgets";

const headerGate = (page) =>
  page.locator(".repo-page-header .MuiAlert-root", {
    hasText: /Nothing is testing/i,
  });

async function gatesFor(request) {
  const body = await (await request.get("/api/base-branch-gates")).json();
  return (body.gates || []).filter((g) => g.repo === repo);
}

for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`switching standing work off and on moves the unjudged gate warning without a reload (${name})`, async ({
    page,
  }) => {
    const errors = [];
    page.on("pageerror", (err) => errors.push(String(err)));
    await page.setViewportSize(viewport);

    // Setup through the endpoints the repo page's own switches write:
    // the one unjudged gate a demo produces in real time.
    for (const [path, data] of [
      ["work-ownership", { default: "" }],
      ["ci-mode", { ciMode: "github" }],
      ["watch-default-branch", { watchDefaultBranch: true }],
    ]) {
      const res = await page.request.put(`/api/repos/${repo}/${path}`, {
        data,
      });
      expect(res.ok(), await res.text()).toBeTruthy();
    }
    expect(await gatesFor(page.request)).toHaveLength(1);

    await page.goto(`/repos/${repo}/settings/releases`);
    await expect(headerGate(page)).toHaveCount(1);
    const standing = page.getByRole("switch", {
      name: "Standing work on this deployment",
    });
    await expect(standing).toBeChecked();

    await standing.click();
    await expect(standing).not.toBeChecked();
    // Within seconds, not the shell's minute-long poll, and no reload.
    await expect(headerGate(page)).toHaveCount(0, { timeout: 10_000 });
    expect(await gatesFor(page.request)).toHaveLength(0);
    await page.getByRole("tab", { name: "Overview" }).click();
    await expect(page.getByText(/base branch not judged/i)).toHaveCount(0);

    await page.getByRole("tab", { name: "Releases" }).click();
    await standing.click();
    await expect(standing).toBeChecked();
    await expect(headerGate(page)).toHaveCount(1, { timeout: 10_000 });
    expect(await gatesFor(page.request)).toHaveLength(1);

    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    expect(errors).toEqual([]);
  });
}
