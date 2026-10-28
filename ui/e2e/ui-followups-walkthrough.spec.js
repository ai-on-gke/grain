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

import { request as playwrightRequest } from "@playwright/test";
import { test, expect } from "./fixtures.js";

// docs/test-plans/2026-10-04.md §9, "UI behaviour from yesterday's
// walkthroughs" (grain/task-1835, docs/test-runs/2026-10-04-ui-followups.md),
// walked against the real `grain demo` server with nothing of grain's
// stubbed. `page.route` is used once, to hold /api/tasks/{id} back for two
// seconds on its way to the real server -- a slow network, not a mock.
//
// What demo cannot show is noted where it would be: it has no
// orchestrator to end a run on nothing_needed (that half is
// pkg/orchestrator/nothing_needed_filed_test.go) and no GitHub to say a
// base branch is missing (GET .../github-branches is a 404, and every
// base check fails open).
//
// A distinct demoArgs gives this file a demo of its own: it moves the
// deployment's time zone. (Its 403 refusals of a viewer's edit and reply
// went with team roles: docs/single-tenant-reset.md, 3e-iii-c2.)
test.use({
  demoArgs: ["-open=false", "-default-target-repo", "acme/widgets"],
});
test.describe.configure({ mode: "serial" });
test.setTimeout(180_000);

const PHONE = { width: 390, height: 844 };
const DESKTOP = { width: 1400, height: 900 };

async function ok(res, what) {
  expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
  return res.json();
}

// prompts records every window.confirm the page raises and answers it
// with `answer` -- false unless a step says otherwise.
function prompts(page) {
  const seen = [];
  const state = { answer: false, seen };
  page.on("dialog", (dialog) => {
    seen.push(dialog.message());
    if (state.answer) dialog.accept();
    else dialog.dismiss();
  });
  return state;
}

test("nothingNeededAction picked on a template is what Run now files", async ({
  page,
}) => {
  const asked = prompts(page);
  const stamp = Date.now();
  for (const [action, option] of [
    ["ack", "Leave for acknowledgement"],
    ["close", "Close"],
  ]) {
    const name = `nn-${action}-${stamp}`;
    await page.goto("/templates");
    await page.getByRole("button", { name: "+ New template" }).click();
    await page.locator('input[name="name"]').fill(name);
    await page.locator('input[name="title"]').fill(`NN ${action} ${stamp}`);
    await page
      .locator('select[name="repo"]')
      .first()
      .selectOption("acme/widgets");
    await page.getByLabel("When the agent finds nothing to do").click();
    await page.getByRole("option", { name: option, exact: true }).click();
    await page.getByRole("button", { name: "Add template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toHaveCount(0);
    const template = (await ok(await page.request.get("/api/templates"))).find(
      (t) => t.name === name,
    );
    expect(template.nothingNeededAction).toBe(action);

    await page.goto("/schedules");
    await page.getByRole("button", { name: "+ New schedule" }).click();
    await page.getByLabel("Title", { exact: true }).fill(`NN ${action}`);
    await page.getByLabel("Template").click();
    await page.getByRole("option", { name: new RegExp(name) }).click();
    await page.getByRole("button", { name: "Add schedule" }).click();
    await expect(
      page.getByRole("heading", { name: "New schedule" }),
    ).toHaveCount(0);
    const schedule = (await ok(await page.request.get("/api/schedules"))).find(
      (s) => s.templateId === template.id,
    );
    expect(schedule.nothingNeededAction).toBe(action);

    await page.goto(`/schedules/${schedule.id}`);
    asked.answer = true;
    const ran = page.waitForResponse(
      (r) =>
        r.url().endsWith(`/api/schedules/${schedule.id}/run`) &&
        r.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Run now" }).click();
    expect((await ran).ok()).toBe(true);
    asked.answer = false;

    const filed = (await ok(await page.request.get("/api/tasks"))).filter(
      (t) => t.title === `NN ${action} ${stamp}`,
    );
    expect(filed).toHaveLength(1);
    const task = await ok(await page.request.get(`/api/tasks/${filed[0].id}`));
    expect(task.nothingNeededAction).toBe(action);

    // And a suite over the same template files the same.
    const suite = await ok(
      await page.request.post("/api/suites", {
        data: {
          name: `NN suite ${action} ${stamp}`,
          templateIds: [template.id],
          mode: "count",
          count: 1,
        },
      }),
    );
    await ok(
      await page.request.post("/api/suite-runs", {
        data: { suiteId: suite.id, repo: "acme/widgets", base: "main" },
      }),
      "suite run",
    );
    const bySuite = (await ok(await page.request.get("/api/tasks"))).filter(
      (t) => t.title === `NN ${action} ${stamp}` && t.id !== task.id,
    );
    expect(bySuite.map((t) => t.nothingNeededAction)).toEqual([action]);
  }
});

test("an edited schedule asks before Escape, Back and browser Back; a saved or declared one does not", async ({
  page,
}) => {
  const asked = prompts(page);
  await page.goto("/schedules");
  await page
    .locator(".process-row-schedule", { hasText: "Weekly Dependency" })
    .click();
  await expect(
    page.getByRole("heading", { name: "Edit schedule" }),
  ).toBeVisible();
  const title = page.getByLabel("Title", { exact: true });
  await title.fill("Weekly audit, edited");

  const leave = {
    Escape: () => page.keyboard.press("Escape"),
    "← Back": () => page.getByRole("button", { name: /^←/ }).first().click(),
    "browser Back": () => page.goBack(),
  };
  for (const [how, go] of Object.entries(leave)) {
    const before = asked.seen.length;
    await go();
    await expect.poll(() => asked.seen.length, how).toBe(before + 1);
    expect(asked.seen.at(-1)).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(title, how).toHaveValue("Weekly audit, edited");
  }

  await page.getByRole("button", { name: "Save" }).click();
  await expect(
    page.getByRole("heading", { name: "Edit schedule" }),
  ).toHaveCount(0);
  const afterSave = asked.seen.length;
  await page.goBack();
  await page.goBack();
  await expect(page.getByRole("heading", { name: "Processes" })).toBeVisible();

  for (const link of [
    "/schedules/repo:schedule:acme:widgets:nightly",
    "/templates/repo:template:acme:widgets:nightly",
    "/suites/repo:suite:acme:widgets:smoke",
  ]) {
    await page.goto(link);
    await expect(page.getByRole("dialog")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
  }
  expect(asked.seen.length, asked.seen.join("\n")).toBe(afterSave);
});

test("a base branch GitHub cannot be asked about is accepted without a warning in demo", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "+ New task" }).click();
  await page.getByLabel("Title").fill(`Base probe ${Date.now()}`);
  await page.getByLabel(/^Repos/).selectOption("acme/widgets");
  const refs = page.waitForResponse((r) =>
    r.url().endsWith("/api/repos/acme/widgets/github-branches"),
  );
  await page.getByLabel("Base branch").fill("no-such-branch");
  expect((await refs).status()).toBe(404);
  await expect(page.getByText(/No branch "/)).toHaveCount(0);
  const created = page.waitForResponse(
    (r) => r.url().endsWith("/api/tasks") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Create task" }).click();
  expect((await created).status()).toBe(201);
});

test("a slow task load shows the loading pane, never the last task's body under the next one's title", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();
  await page.route(/\/api\/tasks\/\d+$/, async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 2000));
    await route.continue();
  });
  const pane = page.getByRole("dialog");

  await page
    .locator(".task-row", { hasText: "Add a dark mode toggle" })
    .click();
  await expect(pane).toHaveAccessibleName("Loading task 1");
  await expect(pane).toHaveAccessibleName("Task 1: Add a dark mode toggle", {
    timeout: 10_000,
  });
  await page.keyboard.press("Escape");
  await expect(pane).toHaveCount(0);

  // Sampled every 100ms from the click on B until B's pane is drawn:
  // only "Loading task 2" or task 2 itself, and never task 1's body.
  await page.locator(".task-row", { hasText: "Fix flaky retry test" }).click();
  const seen = [];
  for (let i = 0; i < 40; i++) {
    const now = await pane.evaluateAll((els) =>
      els.map((el) => [el.getAttribute("aria-label"), el.innerText]),
    );
    seen.push(...now);
    if (now.some(([label]) => label?.startsWith("Task 2:"))) break;
    await page.waitForTimeout(100);
  }
  expect(seen.length).toBeGreaterThan(0);
  for (const [label, text] of seen) {
    expect(["Loading task 2", "Task 2: Fix flaky retry test"]).toContain(label);
    expect(text).not.toContain("Settings page needs a theme switch");
  }
  expect(seen.at(-1)[0]).toBe("Task 2: Fix flaky retry test");
  await page.unrouteAll({ behavior: "wait" });
});

test("executor token times follow the deployment time zone, and a bad team time zone is a 400", async ({
  page,
  baseURL,
}) => {
  const local = await playwrightRequest.newContext({ baseURL });
  await ok(
    await local.put("/api/settings", {
      data: { timeZone: "America/Los_Angeles" },
    }),
    "deployment time zone",
  );
  const { record } = await ok(
    await local.post("/api/executors/tokens", {
      data: { name: "la-probe" },
    }),
    "executor token",
  );
  await local.dispose();

  // The browser runs in UTC; the deployment is in Los Angeles. What is
  // drawn is the deployment's wall clock.
  const created = new Date(record.created_at);
  const la = created.toLocaleString("en-US", {
    timeZone: "America/Los_Angeles",
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
  });
  const utc = created.toLocaleString("en-US", {
    timeZone: "UTC",
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
  });
  expect(la).not.toBe(utc);
  await page.goto("/settings/executors");
  const row = page
    .getByText("la-probe")
    .locator("xpath=ancestor::*[contains(., 'Created')][1]");
  await expect(row).toContainText(`Created:`);
  await expect(row).toContainText(la);
  await expect(row).not.toContainText(utc);
});

// An executor token's name is whatever the admin typed, so it can be an
// id with nowhere to break, up to the 128 characters the server allows.
// Unwrapped, a long one ran the card out past 1500px on a 375px phone:
// the token list scrolled sideways and took the Active chip, the team and
// Revoke with it. It is also named in the one-time "Token for:" line and
// in the revoke confirmation.
test("an unbroken executor token name wraps and leaves Revoke on a phone", async ({
  page,
}) => {
  const name = "y".repeat(128);
  await page.setViewportSize({ width: 375, height: 812 });
  await page.goto("/settings/executors");
  await page.getByRole("button", { name: "Issue token" }).click();
  await page.getByLabel(/Token Name/).fill(name);
  await page.getByRole("button", { name: "Generate Token" }).click();
  const created = page.getByRole("dialog", {
    name: "Executor Token Generated",
  });
  const tokenFor = created.getByText(/Token for:/);
  await expect(tokenFor).toContainText(name);
  const [forWidth, forScroll] = await tokenFor.evaluate((el) => [
    el.clientWidth,
    el.scrollWidth,
  ]);
  expect(forScroll, "Token for: line scrolls sideways").toBeLessThanOrEqual(
    forWidth,
  );
  await created.getByRole("button", { name: "Done" }).click();

  const card = page
    .locator(".MuiCard-root")
    .filter({ has: page.getByText(name, { exact: true }) });
  const revoke = card.getByRole("button", { name: "Revoke" });
  // The connected executors are listed above the tokens on the same tab.
  await card.scrollIntoViewIfNeeded();
  for (const target of [revoke, card.getByText("Active")]) {
    await expect(target).toBeInViewport({ ratio: 1 });
  }
  // Nothing between the card and the page may scroll sideways either: the
  // token list's outlined Paper was the one that did.
  const sideways = await card.evaluate((el) => {
    const out = [];
    for (let n = el; n; n = n.parentElement) {
      if (n.scrollWidth > n.clientWidth + 1) {
        out.push(`${n.className} ${n.scrollWidth}/${n.clientWidth}`);
      }
    }
    return out;
  });
  expect(sideways, "scrolls sideways").toEqual([]);

  await revoke.click();
  const confirm = page.getByRole("dialog", { name: "Revoke Executor Token" });
  await expect(confirm).toContainText(name);
  await expect(
    confirm.getByRole("button", { name: "Revoke Token" }),
  ).toBeInViewport();
  await confirm.getByRole("button", { name: "Cancel" }).click();
});
