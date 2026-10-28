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

// Starting a release run (docs/test-plans/2026-10-02.md §1,
// grain/task-1692, docs/test-runs/2026-10-03-release-run-ref-checks.md).
// A release run is handed the repository's release secrets and executes
// the workflow read at its ref, so it is refused until grain CI is on for
// the repository. Which refs a repository admin who is not a system admin
// may name (pkg/ui's checkReleaseRef) was walked here as named people,
// and went with team roles (docs/single-tenant-reset.md, 3e-iii-c2):
// the access code's session is the deployment's owner, who may name any
// ref and pin a commit.
//
// Ordinary `grain demo` wires no grain CI, and every release-run request
// there is a 404 before those checks are asked. -fake-ci wires a stub in
// ui.Config.CI that records runs and never executes one
// (pkg/demo/demo_fakeci.go), and records acme/widgets main as a watched
// base branch -- the row a real deployment's reconciler writes. Everything
// in front of the stub is the real server. The refusal is checked for a
// readable message and for no run having been recorded.
test.use({ demoArgs: ["-fake-ci"] });
test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

const RELEASE_RUNS = "/api/repos/acme/widgets/release-runs";

test("release runs: refused until grain CI is on, then started from a branch or pinned to a commit", async ({
  page,
}) => {
  const ok = async (res, what) => {
    expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
    return res.json();
  };
  const releaseRuns = async () => {
    const out = await ok(await page.request.get("/api/ci/runs"), "ci runs");
    expect(out.enabled, "-fake-ci wires a grain CI").toBe(true);
    return out.runs.filter((run) => run.event === "release");
  };
  expect(await releaseRuns()).toHaveLength(0);

  // Grain CI not yet on for the repository.
  const refused = await page.request.post(RELEASE_RUNS, {
    data: { branch: "main" },
  });
  expect(refused.status()).toBe(403);
  expect((await refused.json()).error).toMatch(
    /grain CI is not enabled for acme\/widgets/,
  );
  await ok(
    await page.request.put("/api/repos/acme/widgets/ci-mode", {
      data: { ciMode: "grain" },
    }),
    "ci mode",
  );
  expect(await releaseRuns()).toHaveLength(0);

  // The watched base branch, through the pane, opens the run it started.
  await page.goto("/repos/acme/widgets#releases");
  await page.getByRole("button", { name: "Run release by hand" }).click();
  await page.getByLabel("Release branch").fill("main");
  await page.getByRole("button", { name: "Run release", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  let runs = await releaseRuns();
  expect(runs).toHaveLength(1);
  expect(runs[0]).toMatchObject({
    repo: "acme/widgets",
    ref: "main",
    event: "release",
  });
  expect(runs[0].commit ?? "").toBe("");
  // The stub never executes: the run is accepted and stays pending.
  expect(runs[0].conclusion ?? "").toBe("");

  // Any ref, pinned to a commit -- how a deployment republishes one by
  // hand. The pane has no commit field, so the API.
  const pinned = await page.request.post(RELEASE_RUNS, {
    data: {
      branch: "grain/task-9",
      commit: "0123456789abcdef0123456789abcdef01234567",
    },
  });
  expect(pinned.status(), await pinned.text()).toBe(202);
  runs = await releaseRuns();
  expect(runs).toHaveLength(2);
  expect(runs[0]).toMatchObject({
    ref: "grain/task-9",
    commit: "0123456789abcdef0123456789abcdef01234567",
  });
});

// docs/test-plans/2026-10-04.md §5 step 4: the stub's runs are real rows on
// the CI page, shown pending -- the stub never executes one -- until
// somebody cancels them there, which is the one way a stub run finishes.
// Runs after the test above, on the same worker's demo, which left two
// release runs pending.
test("fake CI runs show pending on the CI page until cancelled there", async ({
  page,
}) => {
  await page.goto("/ci");
  const rows = page.locator("tr", { hasText: "release" });
  await expect(rows).toHaveCount(2);
  for (const row of await rows.all()) {
    await expect(row).toContainText(/pending/i);
  }

  // The newest is the pinned grain/task-9 run; its repo is its link.
  await rows.first().getByRole("button", { name: "acme/widgets" }).click();
  // CIRunOverlay asks for a reason with the browser's own prompt() first.
  page.once("dialog", (dialog) => dialog.accept("walkthrough"));
  await page.getByRole("button", { name: "Cancel run" }).click();
  await expect(page.getByRole("button", { name: "Cancel run" })).toHaveCount(0);

  const res = await page.request.get("/api/ci/runs");
  expect(res.ok()).toBe(true);
  const runs = (await res.json()).runs.filter((r) => r.event === "release");
  expect(runs.map((r) => [r.ref, r.conclusion ?? ""])).toEqual([
    ["grain/task-9", "cancelled"],
    ["main", ""],
  ]);
  expect(runs[0].cancelReason).toBe("walkthrough");

  await page.goto("/ci");
  await expect(rows.first()).toContainText(/cancel/i);
  await expect(rows.nth(1)).toContainText(/pending/i);
});
