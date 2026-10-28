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

// Regression sweep of the 2026-10-22 UI fixes on candidate
// (docs/test-plans/2026-10-23.md §9), against the real `grain demo`
// server. The fixes each have their own spec; this one checks them
// together: unsaved-edit guards meeting the top bar, the rail and Back,
// /processes refreshing what an edit touched, and the System panes that
// used to swallow a failed read or a typed branch. The run is recorded
// in docs/test-runs/2026-10-23-ui-fix-regression-sweep.md.

const PROMPT = /unsaved changes/i;

async function seedOne(page, path, data) {
  const res = await page.request.post(path, { data });
  expect(res.ok(), `POST ${path}: ${res.status()}`).toBe(true);
  return res.json();
}

// leaveIsRefused counts the questions one way out asks, dismisses them,
// and checks the page stayed where it was: exactly one prompt, the same
// address, and the edit still in its box.
async function leaveIsRefused(page, leave, box, value) {
  const asked = [];
  const answer = (d) => {
    asked.push(d.message());
    return d.dismiss();
  };
  page.on("dialog", answer);
  const url = page.url();
  await leave();
  await expect.poll(() => asked.length).toBe(1);
  // A second prompt from a second guard would land after the first one
  // is answered, so give it a moment to show up before counting.
  await page.waitForTimeout(300);
  page.off("dialog", answer);
  expect(asked).toHaveLength(1);
  expect(asked[0]).toMatch(PROMPT);
  await expect(page).toHaveURL(url);
  await expect(box).toHaveValue(value);
}

// The three ways out of a page that §9 asks about: a top-bar button,
// a rail item, and the browser's own Back.
async function everyWayOutAsksOnce(page, box, value) {
  await leaveIsRefused(
    page,
    () => page.getByTestId("top-bar-system").click(),
    box,
    value,
  );
  await leaveIsRefused(
    page,
    () =>
      page
        .getByRole("complementary")
        .getByRole("button", { name: /^Inbox/ })
        .click(),
    box,
    value,
  );
  await leaveIsRefused(page, () => page.goBack(), box, value);
}

test.describe("2026-10-22 UI fixes, together on one tip", () => {
  test("a dirty Settings card asks once per way out and keeps the edit", async ({
    page,
  }) => {
    await page.goto("/");
    await page.getByTestId("top-bar-settings").click();
    await expect(page).toHaveURL(/\/settings\/general$/);
    await page.getByTestId("settings-nav-agents").click();
    await expect(page).toHaveURL(/\/settings\/agents$/);

    const box = page.getByLabel("Standing instructions for every run");
    const value = `${await box.inputValue()} Sweep edit.`;
    await box.fill(value);
    await expect(page.getByTestId("settings-dirty-dot")).toBeVisible();

    await everyWayOutAsksOnce(page, box, value);
    // Nothing was saved behind the refused leaves.
    const saved = await (await page.request.get("/api/settings")).json();
    expect(saved.promptExtension || "").not.toContain("Sweep edit.");
    await expect(page.getByTestId("settings-status")).toHaveText(
      /^Unsaved changes in /,
    );
  });

  test("General's own boxes commit on the way out instead of asking", async ({
    page,
  }) => {
    // General has no draft cards: its one text box saves on blur, so
    // a top-bar click is a save and not a question.
    await page.goto("/settings/general");
    const box = page.getByLabel("Environment name");
    let asked = 0;
    page.on("dialog", (d) => {
      asked++;
      return d.dismiss();
    });
    await box.fill("sweep");
    await page.getByTestId("top-bar-system").click();
    await expect(page).toHaveURL(/\/system/);
    await expect
      .poll(
        async () =>
          (await (await page.request.get("/api/settings")).json())
            .environmentName,
      )
      .toBe("sweep");
    expect(asked).toBe(0);
    await page.goBack();
    await expect(page.getByLabel("Environment name")).toHaveValue("sweep");
    await page.getByLabel("Environment name").fill("");
    await page.getByLabel("Environment name").press("Tab");
    await expect(page.getByTestId("settings-status")).toHaveText(
      "All changes saved",
    );
  });

  test("a Settings checkbox's hint sits under its label, the box beside the label", async ({
    page,
  }) => {
    // Found by this sweep's screenshots: outside a <form> the hint ran on
    // inline in the label's own type ("by defaultoff (default)"), and the
    // box sat against the middle of the paragraph.
    await page.goto("/settings/general");
    const input = page.locator('input[name="showClosedByDefault"]');
    await input.waitFor({ state: "attached" });
    const boxes = await input.evaluate((el) => {
      const label = el.closest("label");
      const text = label.querySelector(".MuiFormControlLabel-label");
      const hint = text.querySelector(".hint");
      const title = document.createRange();
      title.selectNodeContents(text.firstChild);
      const rect = (r) => ({ top: r.top, bottom: r.bottom });
      return {
        title: rect(title.getClientRects()[0]),
        hint: rect(hint.getBoundingClientRect()),
        box: rect(el.closest(".MuiCheckbox-root").getBoundingClientRect()),
      };
    });
    // The hint starts below the label's line, not on it.
    expect(boxes.hint.top).toBeGreaterThanOrEqual(boxes.title.bottom - 1);
    // The box is centred on the label's first line, within a few pixels.
    const centre = (r) => (r.top + r.bottom) / 2;
    expect(Math.abs(centre(boxes.box) - centre(boxes.title))).toBeLessThan(4);
  });

  test("a dirty Memory note asks once per way out and keeps the edit", async ({
    page,
  }) => {
    await page.goto("/settings/general");
    await page.getByTestId("settings-nav-memory").click();
    await expect(page).toHaveURL(/\/settings\/memory$/);
    const box = page.getByLabel("Memory carried by every run");
    const value = `${await box.inputValue()} Sweep note.`;
    await box.fill(value);
    await expect(page.getByTestId("settings-status")).toHaveText(
      "Unsaved changes in Memory",
    );
    await everyWayOutAsksOnce(page, box, value);
  });

  test("a dirty repo page asks once per way out and keeps the edit", async ({
    page,
  }) => {
    await page.goto("/repos");
    await page.getByRole("link", { name: "acme/widgets", exact: true }).click();
    await expect(page).toHaveURL(/\/repos\/acme\/widgets/);
    await page.getByRole("tab", { name: "CI", exact: true }).click();
    await page.getByRole("button", { name: "Add workflow" }).click();
    const box = page.getByRole("textbox", { name: "File", exact: true });
    await box.fill("unsaved.yml");
    await everyWayOutAsksOnce(page, box, "unsaved.yml");
  });

  test("Back from a task opened on a repo page returns to the repo page", async ({
    page,
  }) => {
    // 2026-10-22 repository report, bug 2: opening the New task overlay
    // over RepoPage replaced its history entry, so Back skipped it.
    await page.goto("/repos");
    await page.getByRole("link", { name: "acme/widgets", exact: true }).click();
    await expect(page).toHaveURL(/\/repos\/acme\/widgets$/);
    await page.getByRole("button", { name: "New task", exact: true }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    await expect(page.getByLabel(/^Repos/)).toHaveValue("acme/widgets");
    await page.goBack();
    await expect(page).toHaveURL(/\/repos\/acme\/widgets$/);
  });

  test("/processes refreshes the rows an edit or a Run now touched", async ({
    page,
  }) => {
    const stamp = Date.now();
    const tmpl = await seedOne(page, "/api/templates", {
      name: `sweep-${stamp}`,
      title: `Sweep ${stamp}`,
    });
    const suite = await seedOne(page, "/api/suites", {
      name: `sweepsuite-${stamp}`,
      templateIds: [tmpl.id],
      mode: "count",
      count: 1,
    });
    await seedOne(page, "/api/schedules", {
      repo: "acme/widgets",
      templateId: tmpl.id,
      recurrence: { kind: "daily", timeOfDay: "02:00" },
    });
    const suiteSched = await seedOne(page, "/api/schedules", {
      suiteId: suite.id,
      repo: "acme/widgets",
      base: "main",
      recurrence: { kind: "everyNHours", everyNHours: 6 },
    });

    await page.goto("/processes");
    // Marks this document: a reload anywhere below would drop it.
    await page.evaluate(() => {
      window.__sweepNoReload = true;
    });
    await expect(
      page.getByRole("link", { name: `Sweep ${stamp}`, exact: true }),
    ).toBeVisible();

    // A template edit renames the schedule row it backs.
    await page
      .locator(`a[href="/processes/templates/${tmpl.id}"]`)
      .first()
      .click();
    await page
      .getByRole("textbox", { name: "Task title" })
      .fill(`Sweep ${stamp} v2`);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=template)?$/);
    await expect(
      page.getByRole("link", { name: `Sweep ${stamp} v2`, exact: true }),
    ).toBeVisible();

    // A suite rename shows on the schedule that runs it.
    await page
      .locator(`a[href="/processes/suites/${suite.id}"]`)
      .first()
      .click();
    await page
      .getByRole("textbox", { name: "Name", exact: true })
      .fill(`sweepsuite-${stamp}-v2`);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=suite)?$/);
    // The schedule that runs it says the new name on its Suite chip. Its
    // own title is the schedule's, stored when it was made, so that keeps
    // the old name (pkg/ui/schedules.go) -- only the chip is derived.
    await expect(
      page
        .getByRole("listitem")
        .filter({
          has: page.locator(`a[href="/processes/schedules/${suiteSched.id}"]`),
        })
        .getByText(`Suite: sweepsuite-${stamp}-v2`, { exact: true }),
    ).toBeVisible();

    // Run now on the suite schedule puts its run in Suite runs.
    await page
      .locator(`a[href="/processes/schedules/${suiteSched.id}"]`)
      .first()
      .click();
    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Run now" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);
    await expect(
      page
        .locator(
          `.suite-run-row a[href="/processes/schedules/${suiteSched.id}"]`,
        )
        .first(),
    ).toBeVisible();

    expect(await page.evaluate(() => window.__sweepNoReload)).toBe(true);
  });

  test("System → Sandboxes says a failed executors read failed", async ({
    page,
  }) => {
    // Stub at the HTTP boundary (recorded in the report): the demo has
    // no state in which /api/executors fails on its own.
    await page.route("**/api/executors", (route) =>
      route.fulfill({ status: 500, body: "executor registry unavailable" }),
    );
    await page.goto("/system/sandboxes");
    await expect(
      page.getByText(/Model servers could not be read/),
    ).toBeVisible();
    await expect(page.getByText("No model servers connected.")).toHaveCount(
      0,
    );
    await expect(
      page.getByText(
        /The tokens they connect with are on Settings → Executors/,
      ),
    ).toBeVisible();
  });

  test("a typed upgrade branch survives the status polls", async ({ page }) => {
    // The demo wires no Upgrader, so /api/upgrade is stubbed (recorded
    // in the report). The pane only polls while an upgrade is running or
    // scheduled, so the stub starts with one scheduled, and once a branch
    // is typed answers with that upgrade finished on main -- the phase
    // "ok", no-channel reading whose poll used to put "main" back over a
    // typed branch. Nothing is ever posted.
    const running = { image: "ghcr.io/bwsalmon/grain:sweep" };
    let finished = false;
    let polls = 0;
    let upgrades = 0;
    await page.route("**/api/upgrade", (route) => {
      if (route.request().method() !== "GET") {
        upgrades++;
        return route.fulfill({ status: 409, body: "stubbed by the sweep" });
      }
      polls++;
      return route.fulfill({
        json: finished
          ? {
              enabled: true,
              mode: "image",
              running,
              branch: "main",
              phase: "ok",
              detail: "",
            }
          : {
              enabled: true,
              mode: "image",
              running,
              branch: "",
              phase: "",
              detail: "",
              scheduled: {
                branch: "main",
                requestedAt: new Date().toISOString(),
                byId: "operator",
                busyRuns: 1,
                busyCiRuns: 0,
              },
            },
      });
    });
    await page.goto("/settings/upgrade");
    const branch = page.getByLabel("Branch");
    await branch.fill("grain/sweep-upgrade");
    finished = true;
    const seen = polls;
    // STATUS_POLL_MS is 3s: wait for the poll that reads "ok".
    await expect.poll(() => polls, { timeout: 10_000 }).toBeGreaterThan(seen);
    await page.waitForTimeout(500);
    await expect(branch).toHaveValue("grain/sweep-upgrade");
    expect(upgrades).toBe(0);
  });

  test("state-sync-failing alert and Inbox StateSyncGroup open Settings -> Config repo", async ({
    page,
  }) => {
    const baseConfig = await (await page.request.get("/api/config")).json();
    await page.route("**/api/config", (route) => {
      if (route.request().method() !== "GET") return route.continue();
      return route.fulfill({
        json: {
          ...baseConfig,
          stateRepo: "acme/grain-state",
          stateRepoBranch: "main",
          stateRepoSyncFailure: {
            repo: "acme/grain-state",
            branch: "main",
            at: "2026-10-23T12:00:00Z",
            error: "push rejected: non-fast-forward",
            failures: 3,
          },
        },
      });
    });

    // 1. System -> Health (/system): "Config repo" action in the right-hand slot opens /settings/config-repo
    await page.goto("/system");
    const healthAlertRow = page.locator(
      '[data-testid="alert-row"][data-alert-id="state-sync-failing"]',
    );
    await expect(healthAlertRow).toBeVisible();
    await expect(
      healthAlertRow.getByRole("button", { name: "Open System" }),
    ).toHaveCount(0);
    const healthBtn = healthAlertRow.getByRole("button", {
      name: "Config repo",
    });
    await expect(healthBtn).toBeVisible();
    const healthBoxes = await healthAlertRow.evaluate((row) => {
      const scope = row
        .querySelector('[data-testid="alert-scope"]')
        .getBoundingClientRect();
      const btn = row.querySelector("button").getBoundingClientRect();
      return {
        rowRight: row.getBoundingClientRect().right,
        scopeRight: scope.right,
        btnLeft: btn.left,
        btnRight: btn.right,
      };
    });
    expect(healthBoxes.btnLeft).toBeGreaterThan(healthBoxes.scopeRight);
    expect(healthBoxes.rowRight - healthBoxes.btnRight).toBeLessThan(32);
    await healthBtn.click();
    await expect(page).toHaveURL(/\/settings\/config-repo$/);

    // 2. Top-bar AlertsButton popover: "Config repo" action opens /settings/config-repo
    await page.goto("/tasks");
    await page.getByTestId("alerts-button").click();
    const popoverAlertRow = page
      .getByRole("dialog", { name: "Alerts" })
      .locator('[data-testid="alert-row"][data-alert-id="state-sync-failing"]');
    await expect(popoverAlertRow).toBeVisible();
    const popoverBtn = popoverAlertRow.getByRole("button", {
      name: "Config repo",
    });
    await expect(popoverBtn).toBeVisible();
    await popoverBtn.click();
    await expect(page).toHaveURL(/\/settings\/config-repo$/);

    // 3. /inbox StateSyncGroup: "Config repo" action in the right-hand slot opens /settings/config-repo
    await page.goto("/inbox");
    const inboxGroup = page
      .locator("section.inbox-group")
      .filter({ hasText: "Settings are not being exported" });
    await expect(inboxGroup).toBeVisible();
    await expect(
      inboxGroup.getByRole("button", { name: "Open System" }),
    ).toHaveCount(0);
    const inboxBtn = inboxGroup.getByRole("button", { name: "Config repo" });
    await expect(inboxBtn).toBeVisible();
    const inboxBoxes = await inboxGroup.evaluate((group) => {
      const row = group.querySelector(".inbox-line");
      const repo = row
        .querySelector(".inbox-gate-repo")
        .getBoundingClientRect();
      const btn = row.querySelector("button").getBoundingClientRect();
      return {
        rowRight: row.getBoundingClientRect().right,
        repoRight: repo.right,
        btnLeft: btn.left,
        btnRight: btn.right,
      };
    });
    expect(inboxBoxes.btnLeft).toBeGreaterThan(inboxBoxes.repoRight);
    expect(inboxBoxes.rowRight - inboxBoxes.btnRight).toBeLessThan(32);
    await inboxBtn.click();
    await expect(page).toHaveURL(/\/settings\/config-repo$/);
  });
});
