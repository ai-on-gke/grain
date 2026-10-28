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
import { spawnSync } from "node:child_process";

// The browser half of docs/test-plans/2026-10-12.md §5
// (docs/test-runs/2026-10-12-suite-release-batch-fixes.md): a suite run
// whose template is deleted between passes, a release named after the
// default branch, and a cleared merge batch size, against the real demo
// server with the simulated GitHub (-github-sim) behind it.
//
// The demo runs no reconcile loop and no agents, so e2e/releasehelper
// stands in for both and nothing else: complete-suite-pass finishes a
// pass's tasks as an agent's run would, and sync-suites / sync-releases
// run one cycle of the real orchestrator.SyncSuites and SyncReleases --
// the latter against the same bare repo the demo's simulated GitHub
// serves -- logging a reconciler's error the way the daemon does.
//
// Not `grain demo -reconcile`, which runs those two on a timer: the
// suite test deletes its template between pass 1 finishing and the
// cycle that would file pass 2, and only a cycle this file runs itself
// can be held off until then.
test.use({ demoArgs: ["-github-sim"] });

function helper(page, ...args) {
  const bin = process.env.GRAIN_E2E_RELEASE_HELPER_BIN || "/tmp/releasehelper";
  const port = new URL(page.url()).port;
  const res = spawnSync(bin, ["-port", port, ...args], { encoding: "utf8" });
  expect(res.status, `releasehelper ${args.join(" ")}: ${res.stderr}`).toBe(0);
  return { out: res.stdout.trim(), log: res.stderr };
}

test.describe("suite run, default-branch release and merge batch walkthrough", () => {
  test("a suite run whose template is deleted after pass 1 fails with the reason, once", async ({
    page,
  }) => {
    page.on("dialog", (dialog) => dialog.accept());
    const stamp = Date.now();
    const tmplName = `walk-gone-${stamp}`;
    const suiteName = `Walk Gone Suite ${stamp}`;

    await page.goto("/templates");
    await page.getByRole("button", { name: "+ New template" }).click();
    await page.locator('input[name="name"]').fill(tmplName);
    await page.locator('input[name="title"]').fill(`Walk gone ${stamp}`);
    await page
      .locator('select[name="repo"]')
      .first()
      .selectOption("acme/widgets");
    await page.locator('input[name="base"]').first().fill("main");
    await page.getByRole("button", { name: "Add template" }).click();
    await expect(
      page.locator(".process-row-template", { hasText: tmplName }),
    ).toBeVisible();

    await page.goto("/suites");
    await page.getByRole("button", { name: "+ New suite" }).click();
    await page.locator('input[name="name"]').first().fill(suiteName);
    await page.getByLabel("Run mode").click();
    await page
      .getByRole("option", { name: /Run a fixed number of times/i })
      .click();
    await page.locator('input[name="count"]').fill("2");
    await page.getByLabel("Templates").click();
    await page.getByRole("option", { name: new RegExp(tmplName) }).click();
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Add suite" }).click();
    const suiteRow = page.locator(".process-row-suite", { hasText: suiteName });
    await expect(suiteRow).toContainText("run 2×");

    await suiteRow.getByRole("button", { name: "Run…" }).click();
    await page
      .locator('select[name="repo"]')
      .first()
      .selectOption("acme/widgets");
    await page.locator('input[name="base"]').first().fill("main");
    await page.getByRole("button", { name: "Run", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).toHaveCount(0);
    const runRow = page.locator(".suite-run-row", { hasText: suiteName });
    await expect(runRow).toContainText("Active");
    await expect(runRow).toContainText("pass 1");

    // Pass 1 finishes, then the suite and its template go, through the
    // UI, before the next cycle fires pass 2.
    helper(page, "complete-suite-pass");
    await suiteRow.click();
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(suiteRow).toHaveCount(0);
    await page.goto("/templates");
    await page.locator(".process-row-template", { hasText: tmplName }).click();
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(
      page.locator(".process-row-template", { hasText: tmplName }),
    ).toHaveCount(0);

    // Two reconcile cycles: the first ends the run, and neither logs
    // the error the run used to be retried on forever.
    const reason = /pass 2 could not be filed: template \S+ no longer exists/;
    const logs = [helper(page, "sync-suites"), helper(page, "sync-suites")];
    for (const { log } of logs) {
      expect(log).not.toContain("no longer exists");
    }

    await page.goto("/suites");
    await expect(runRow).toContainText("Failed");
    await expect(runRow).toContainText(reason);
    await runRow.click();
    await expect(page).toHaveURL(/\/processes\/suites\/runs\//);
    // The pane opens over the list, as a dialog of its own.
    const pane = page.getByRole("dialog");
    await expect(pane.getByText("Failed", { exact: true })).toBeVisible();
    await expect(pane.getByText(reason)).toBeVisible();
    const runID = page.url().split("/").pop();
    const run = await (
      await page.request.get(`/api/suite-runs/${runID}`)
    ).json();
    expect(run.status).toBe("failed");
    expect(run.error).toMatch(reason);
  });

  test("a release named after the default branch is refused and never moves it", async ({
    page,
  }) => {
    page.on("dialog", (dialog) => dialog.accept());
    await page.goto("/repos/acme/widgets/releases");
    const mainBefore = helper(page, "branch-sha", "main").out;

    await page.getByLabel("New release name").fill("main");
    await page.getByRole("button", { name: "Create release" }).click();
    await expect(
      page.getByText("latest: main.latest, prod: main"),
    ).toBeVisible();

    // Provisioning refuses it, says why on the release, and says it once.
    const refusal =
      'release "main" would use the default branch "main" as its prod branch';
    const first = helper(page, "-release", "main", "sync-releases");
    helper(page, "-release", "main", "sync-releases");
    expect(first.log).not.toContain("reconcile: releases");
    await page.reload();
    await expect(page.getByText(refusal)).toBeVisible();
    const release = await (
      await page.request.get("/api/repos/acme/widgets/releases/main")
    ).json();
    expect(release.status).toBe("provisioning");
    expect(release.error).toContain(refusal);
    expect(helper(page, "branch-sha", "main").out).toBe(mainBefore);

    // One provisioned before the check: its rc sits on a commit main does
    // not have, and promoting it through the pane is refused rather than
    // force-moving main there.
    const rc = helper(page, "-release", "main", "provision-legacy").out;
    await page.reload();
    const promote = page.getByRole("button", { name: "Promote current RC" });
    await expect(promote).toBeEnabled();
    await promote.click();
    await expect(
      page.locator(".candidate-current", { hasText: "main.rc.1 -- promoting" }),
    ).toBeVisible();
    helper(page, "-release", "main", "sync-releases");
    helper(page, "-release", "main", "sync-releases");
    expect(helper(page, "branch-sha", "main").out).toBe(mainBefore);
    expect(helper(page, "branch-sha", "main.rc.1").out).not.toBe(mainBefore);
    expect(rc).toContain(helper(page, "branch-sha", "main.rc.1").out);
    await page.reload();
    const current = page.locator(".candidate-current");
    await expect(current).toContainText("main.rc.1 -- promoting");
    await expect(current).toContainText(refusal);

    // And it can be removed, freeing nothing but its own branches' name:
    // the pane has no rename, so a new name is a new release.
    await page.getByRole("button", { name: "Delete release" }).click();
    await expect(page.getByText("latest: main.latest, prod: main")).toHaveCount(
      0,
    );
    expect(
      (
        await page.request.get("/api/repos/acme/widgets/releases/main")
      ).status(),
    ).toBe(404);
    await page.getByLabel("New release name").fill("walk-2");
    await page.getByRole("button", { name: "Create release" }).click();
    await expect(
      page.getByText("latest: walk-2.latest, prod: walk-2"),
    ).toBeVisible();
    helper(page, "-release", "walk-2", "sync-releases");
    expect(helper(page, "branch-sha", "walk-2.latest").out).toBe(mainBefore);
    expect(helper(page, "branch-sha", "main").out).toBe(mainBefore);
  });

  test("a cleared merge batch size reads blank after a reload and is grain's default", async ({
    page,
  }) => {
    const stored = async () =>
      (await (await page.request.get("/api/settings")).json()).mergeBatchSize;
    const box = async () => {
      await page.goto("/settings/engine");
      const b = page.getByLabel("Merge batch size");
      await expect(b).toBeVisible();
      return b;
    };
    const save = async (b, value) => {
      const put = page.waitForResponse(
        (r) =>
          r.url().endsWith("/api/settings") && r.request().method() === "PUT",
      );
      await b.fill(value);
      await b.press("Enter");
      return put;
    };

    expect((await save(await box(), "5")).status()).toBe(200);
    await expect(await box()).toHaveValue("5");
    expect(await stored()).toBe(5);

    expect((await save(await box(), "")).status()).toBe(200);
    const cleared = await box();
    await expect(cleared).toHaveValue("");
    await expect(cleared).toHaveAttribute("placeholder", "3");
    expect(await stored()).toBe(3);

    expect((await save(await box(), "5")).status()).toBe(200);
    const b = await box();
    await b.fill("-1");
    await b.press("Enter");
    await expect(
      page
        .getByText("Merge batch size must be at least 1; -1 was not saved.")
        .first(),
    ).toBeVisible();
    await expect(await box()).toHaveValue("5");
    expect(await stored()).toBe(5);

    // A number box will not hold "abc" in a real browser; the API is
    // where it can be sent, and it is refused there without a change.
    const abc = await page.request.put("/api/settings", {
      data: { mergeBatchSize: "abc" },
    });
    expect(abc.status()).toBe(400);
    expect(await abc.text()).toContain("mergeBatchSize");
    expect(await stored()).toBe(5);
  });
});
