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
import { alertRow, closeAlerts, openAlerts } from "./alerts.js";

// The Inbox's collapsible sections in a real browser (PR #199, the
// 2026-10-30 test plan's §6 gap 1 and 4;
// docs/test-runs/2026-10-31-inbox-collapse-director-notifications.md):
// mouse and keyboard on the heading, the choice kept in localStorage
// across a reload and a trip through another page, a section that
// empties and refills, a critical notification arriving while
// Notifications is folded, and the rail's Inbox chip agreeing with the
// heading's total with a local-script call waiting and the config
// repository's sync failing.
//
// Everything this files on the worker's shared demo -- a task, a
// local-script call, a notification -- is answered, closed or cleared in
// `finally`. localStorage belongs to the test's own browser context.

test.setTimeout(120_000);

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };
const STORAGE_KEY = "grain.inbox.collapsed";

function section(page, id) {
  return page.locator(`.inbox-group[data-section-id="${id}"]`);
}

function toggleOf(page, id) {
  return section(page, id).locator(".inbox-group-toggle");
}

async function storedMap(page) {
  return page.evaluate(
    (key) => JSON.parse(localStorage.getItem(key) || "{}"),
    STORAGE_KEY,
  );
}

// The Inbox heading's total, which ListHeader puts in the page header
// beside the page's title rather than inside main.
async function headingCount(page) {
  return Number(
    await page.locator(".content-header > .count").first().innerText(),
  );
}

async function railCount(page) {
  const chip = page
    .locator(".MuiListItemButton-root", { hasText: "Inbox" })
    .first()
    .locator(".rail-count");
  return Number(await chip.innerText());
}

async function ok(res, what) {
  expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
  return res.json();
}

async function askForApproval(run, taskId, call) {
  return ok(
    await run.post(`/api/tasks/${taskId}/local-script-approvals`, {
      data: {
        id: call,
        tool: "local_deploy",
        script: "deploy",
        args: ["--preview"],
        deadline: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
      },
    }),
    `ask for local-script call ${call}`,
  );
}

async function answer(request, taskId, approvalId) {
  await request.post(
    `/api/tasks/${taskId}/local-script-approvals/${approvalId}/answer`,
    { data: { approve: false } },
  );
}

test("folds and unfolds every section by mouse, Enter and Space, and keeps the choice across reload and navigation", async ({
  page,
}) => {
  for (const viewport of [DESKTOP, PHONE]) {
    await page.setViewportSize(viewport);
    await page.goto("/inbox");
    await page.evaluate((key) => localStorage.removeItem(key), STORAGE_KEY);
    await page.reload();

    // Defaults: Notifications folded, every task group open.
    const notifications = toggleOf(page, "notifications");
    await expect(notifications).toHaveAttribute("aria-expanded", "false");
    const groups = page.locator(".inbox-group");
    await expect(groups.nth(1)).toBeVisible();
    const ids = await groups.evaluateAll((els) =>
      els.map((el) => el.dataset.sectionId),
    );
    expect(ids[0]).toBe("notifications");
    expect(ids.length).toBeGreaterThan(1);
    for (const id of ids.slice(1)) {
      await expect(toggleOf(page, id)).toHaveAttribute("aria-expanded", "true");
    }

    // The heading names the body it controls, and folding empties it
    // while the heading and count stay.
    const first = ids[1];
    const toggle = toggleOf(page, first);
    const bodyId = await toggle.getAttribute("aria-controls");
    expect(bodyId).toBe(`inbox-group-${first}`);
    const body = page.locator(`#${bodyId}`);
    await expect(body).toBeVisible();
    const count = await toggle.locator(".count").innerText();

    // Mouse.
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(body).toBeHidden();
    await expect(toggle.locator(".count")).toHaveText(count);
    expect(await storedMap(page)).toEqual({ [first]: true });
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    // Back at its default, the section leaves nothing stored.
    expect(
      await page.evaluate((key) => localStorage.getItem(key), STORAGE_KEY),
    ).toBeNull();

    // Keyboard: Enter folds, Space unfolds, focus stays on the heading.
    await toggle.focus();
    await page.keyboard.press("Enter");
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(toggle).toBeFocused();
    await page.keyboard.press("Space");
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await expect(toggle).toBeFocused();

    // Tab reaches Notifications' heading, and Space unfolds it.
    await notifications.focus();
    await page.keyboard.press("Space");
    await expect(notifications).toHaveAttribute("aria-expanded", "true");
    await expect(
      section(page, "notifications").getByTestId("inbox-notification").first(),
    ).toBeVisible();

    // Fold the task group again, then reload: both choices hold.
    await toggle.press("Enter");
    expect(await storedMap(page)).toEqual({
      notifications: false,
      [first]: true,
    });
    await page.reload();
    await expect(toggleOf(page, "notifications")).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    await expect(toggleOf(page, first)).toHaveAttribute(
      "aria-expanded",
      "false",
    );

    // Away to another page and back through the app's own navigation
    // (the Inbox unmounts and mounts again): still as left.
    await page.goto("/tasks");
    await expect(page.locator("main.inbox")).toHaveCount(0);
    await page.goBack();
    await expect(toggleOf(page, "notifications")).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    await expect(toggleOf(page, first)).toHaveAttribute(
      "aria-expanded",
      "false",
    );
  }
});

test("a section that empties and refills keeps its fold, and the rail's Inbox chip matches the heading", async ({
  page,
  playwright,
  baseURL,
}) => {
  await page.setViewportSize(DESKTOP);
  // The config repository's sync failure is a property of a daemon with
  // a grain/config repository, which `grain demo` has none of: it is
  // added to /api/config's own answer, the one boundary stubbed here.
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const config = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...config,
        stateRepoSyncFailure: {
          since: new Date(Date.now() - 10 * 60 * 1000).toISOString(),
          error: "staterepo: push rejected (walkthrough stub)",
          repo: "grain/config",
        },
      },
    });
  });
  const task = await ok(
    await page.request.post("/api/tasks", {
      data: {
        title: "Inbox collapse walkthrough: local scripts",
        description: "Asks for a local script, for the Inbox's fold.",
        capabilities: ["local-scripts:ops"],
      },
    }),
    "file the task",
  );
  const run = await playwright.request.newContext({ baseURL });
  const asked = [];
  try {
    asked.push(await askForApproval(run, task.id, "1"));
    await page.goto("/inbox");
    const scripts = toggleOf(page, "local-scripts");
    await expect(scripts).toHaveAttribute("aria-expanded", "true");
    await expect(section(page, "state-sync")).toBeVisible();

    // The chip and the heading agree, expanded and collapsed: folding
    // changes what is shown, never what is counted.
    const agree = async () => {
      await expect
        .poll(async () => (await railCount(page)) - (await headingCount(page)))
        .toBe(0);
    };
    await agree();
    const total = await headingCount(page);
    for (const id of ["local-scripts", "state-sync", "notifications"]) {
      await toggleOf(page, id).click();
    }
    await expect(scripts).toHaveAttribute("aria-expanded", "false");
    await expect(toggleOf(page, "state-sync")).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    await expect(toggleOf(page, "notifications")).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    expect(await headingCount(page)).toBe(total);
    await agree();
    for (const id of ["state-sync", "notifications"]) {
      await toggleOf(page, id).click();
    }

    // Empty the local-scripts section: answered, its group goes.
    await answer(page.request, task.id, asked[0].id);
    await page.reload();
    await expect(section(page, "local-scripts")).toHaveCount(0);
    await agree();

    // Refill it: the group comes back folded, as it was left.
    asked.push(await askForApproval(run, task.id, "2"));
    await page.reload();
    await expect(scripts).toHaveAttribute("aria-expanded", "false");
    await expect(scripts.locator(".count")).toHaveText("1");
    await agree();
  } finally {
    for (const a of asked) await answer(page.request, task.id, a.id);
    await page.request.post(`/api/tasks/${task.id}/close`);
    await run.dispose();
  }
});

test("a critical notification arriving while Notifications is folded is counted on the heading and in the bell", async ({
  page,
}) => {
  // Each viewport waits out up to one notifications poll (below).
  test.setTimeout(180_000);
  for (const viewport of [DESKTOP, PHONE]) {
    await page.setViewportSize(viewport);
    await page.goto("/inbox");
    await page.evaluate((key) => localStorage.removeItem(key), STORAGE_KEY);
    await page.reload();
    const toggle = toggleOf(page, "notifications");
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    const before = Number(await toggle.locator(".count").innerText());
    const total = await headingCount(page);

    const title = `Walkthrough critical ${viewport.width} ${Date.now()}`;
    const raised = await ok(
      await page.request.post("/api/notifications", {
        data: {
          subject: `walkthrough.collapse-${viewport.width}-${Date.now()}`,
          title,
          severity: "critical",
        },
      }),
      "raise a critical notification",
    );
    try {
      // Without a reload: the page's own poll brings it in. That poll
      // is every 30s (POLL_MS in notifications.js), so a raise just after
      // one lands a whole interval later plus the read itself: waiting
      // exactly 30s lost that race on a loaded CI runner.
      await expect(toggle.locator(".count")).toHaveText(String(before + 1), {
        timeout: 45_000,
      });
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      expect(await headingCount(page)).toBe(total + 1);
      await openAlerts(page);
      const row = alertRow(page, title);
      await expect(row).toHaveCount(1);
      await expect(row).toHaveAttribute("data-severity", "critical");
      await closeAlerts(page);
    } finally {
      await page.request.post(
        `/api/notifications/${raised.notification.id}/clear`,
      );
    }
  }
});
