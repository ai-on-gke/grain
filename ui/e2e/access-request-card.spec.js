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

// A run's request_access call parks its task on a card listing what it
// asked for, with Grant and Deny (detail/PendingAccess.jsx). grain demo
// parks "Investigate the merge queue stall" on a question, not on an
// access request -- only a finished run's tool call makes one -- so this
// answers that task's detail with a pending request added, and answers
// the decision routes itself: the worker's demo server is shared, and
// nothing here may leave the task changed for the next spec.

const SIZES = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};

const TITLE = "Investigate the merge queue stall";

const pendingAccess = {
  summary: "Grant write to acme/lib so the fix lands in both repositories",
  reason:
    "The stall comes from a retry bug in the shared client library; fixing it here alone leaves the queue's other consumer stuck.",
  createdAt: "2026-11-03T10:00:00Z",
  items: [
    { kind: "read", repo: "acme/docs" },
    { kind: "write", repo: "acme/lib", base: "release-2" },
    { kind: "network", access: "open" },
    {
      kind: "grant",
      capability: "api:tasks:admin",
      grant: "api:tasks:admin",
      dangerous: true,
    },
    {
      kind: "grant",
      capability: "https://grain.dev/capabilities/repo#write",
      with: { force: true, refs: ["refs/heads/release-*"] },
      grant:
        'https://grain.dev/capabilities/repo#write={"force":true,"refs":["refs/heads/release-*"]}',
      requiresPerson: true,
    },
  ],
  holds: {
    reads: ["acme/handbook"],
    writes: [],
    grants: ["self-debug"],
    networkAccess: "",
  },
  earlier: [
    {
      denied: true,
      reason: "Use the vendored copy until the release branch is cut.",
      by: { kind: "human", id: "alice" },
      at: "2026-11-02T09:00:00Z",
    },
  ],
};

async function awaitingTask(request) {
  const response = await request.get("/api/tasks");
  expect(response.ok()).toBeTruthy();
  const tasks = await response.json();
  const list = Array.isArray(tasks) ? tasks : tasks.tasks || [];
  return list.find((t) => t.title === TITLE);
}

// fakeAccessRequest answers task id's detail as parked on pendingAccess,
// and records each decision posted rather than letting it reach the
// store.
async function fakeAccessRequest(page, id, pending = pendingAccess) {
  await page.route(new RegExp(`/api/tasks/${id}(\\?.*)?$`), async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    // The page polls this route. Under load one poll's response can be
    // disposed before it is read ("Response has been disposed"), and a
    // throw here fails the test; let that one through unfaked instead --
    // the next poll is faked, and the card is still asserted below.
    let response, body;
    try {
      response = await route.fetch({ headers });
      body = await response.json();
    } catch {
      return route.fallback().catch(() => {});
    }
    await route.fulfill({
      response,
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        ...body,
        awaitingAccess: true,
        pendingAccess: pending,
      }),
    });
  });
  const decisions = [];
  await page.route(
    new RegExp(`/api/tasks/${id}/access-request/(grant|deny)$`),
    async (route) => {
      decisions.push({
        verb: route.request().url().split("/").pop(),
        body: JSON.parse(route.request().postData() || "{}"),
      });
      await route.fulfill({ status: 200, json: {} });
    },
  );
  return decisions;
}

async function expectInside(card, page, width) {
  const box = await card.boundingBox();
  expect(box).not.toBeNull();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(width + 1);
  for (const name of [/^Grant/, /^Deny$/]) {
    const button = card.getByRole("button", { name }).first();
    const b = await button.boundingBox();
    expect(b.x).toBeGreaterThanOrEqual(box.x);
    expect(b.x + b.width).toBeLessThanOrEqual(box.x + box.width + 1);
  }
  const overflow = await card.evaluate((el) => el.scrollWidth - el.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  // Every item's chip sits inside the card, however long its grant.
  for (const chip of await card.getByTestId("access-item-chip").all()) {
    const c = await chip.boundingBox();
    expect(c.x + c.width).toBeLessThanOrEqual(box.x + box.width + 1);
  }
  await expect(page.locator("body")).toBeVisible();
}

for (const [name, size] of Object.entries(SIZES)) {
  test(`access request card on the task and in the inbox (${name})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(size);
    const task = await awaitingTask(request);
    expect(task, `the demo's "${TITLE}" task`).toBeTruthy();
    const decisions = await fakeAccessRequest(page, task.id);

    // The task's own pane.
    await page.goto(`/tasks/${task.id}`);
    const card = page.getByRole("region", {
      name: "Access request waiting for your decision",
    });
    await expect(card).toBeVisible();
    await expect(card).toContainText(pendingAccess.summary);
    await expect(card.getByTestId("access-reason")).toContainText(
      "retry bug in the shared client library",
    );
    await expect(card).toContainText("acme/lib › write · base: release-2");
    await expect(card.getByText("dangerous")).toBeVisible();
    await expect(card.getByText("person only")).toBeVisible();
    await expect(card.getByTestId("access-earlier")).toContainText(
      "Use the vendored copy",
    );
    await expect(card.getByTestId("access-holds")).toContainText(
      "reads acme/handbook",
    );
    await expectInside(card, page, size.width);

    const dir = process.env.SHOTS_DIR;
    if (dir) {
      for (const theme of ["light", "dark"]) {
        await page.evaluate(
          (t) => localStorage.setItem("grain.themeMode", t),
          theme,
        );
        await page.emulateMedia({ colorScheme: theme });
        await page.reload();
        await expect(card).toBeVisible();
        await page.waitForTimeout(300);
        await card.screenshot({
          path: `${dir}/access-card-task-${name}-${theme}.png`,
        });
      }
      await page.evaluate(() => localStorage.removeItem("grain.themeMode"));
      await page.emulateMedia({ colorScheme: "light" });
      await page.reload();
    }

    // Untick the write, grant the rest.
    await card
      .getByRole("checkbox", {
        name: "Grant acme/lib › write · base: release-2",
      })
      .click();
    await card.getByRole("button", { name: "Grant 4 of 5" }).click();
    await expect.poll(() => decisions.length).toBe(1);
    expect(decisions[0]).toEqual({
      verb: "grant",
      body: { items: [0, 2, 3, 4] },
    });

    // Deny needs a reason.
    await card.getByRole("button", { name: "Deny", exact: true }).click();
    const deny = card.getByRole("button", { name: "Deny request" });
    await expect(deny).toBeDisabled();
    await card.getByRole("textbox", { name: /Why/ }).fill("Not this sprint.");
    await deny.click();
    await expect.poll(() => decisions.length).toBe(2);
    expect(decisions[1]).toEqual({
      verb: "deny",
      body: { reason: "Not this sprint." },
    });

    // The inbox row opens the same card in place of a reply box.
    await page.goto("/inbox");
    const row = page.locator(".inbox-item", { hasText: TITLE });
    await row.getByRole("button", { name: "Reply" }).click();
    const inboxCard = row.getByRole("region", {
      name: "Access request waiting for your decision",
    });
    await expect(inboxCard).toBeVisible();
    await expect(row.getByPlaceholder("Reply...")).toHaveCount(0);
    await expectInside(inboxCard, page, size.width);
    if (dir) {
      for (const theme of ["light", "dark"]) {
        await page.evaluate(
          (t) => localStorage.setItem("grain.themeMode", t),
          theme,
        );
        await page.emulateMedia({ colorScheme: theme });
        await page.waitForTimeout(300);
        await row.screenshot({
          path: `${dir}/access-card-inbox-${name}-${theme}.png`,
        });
      }
      await page.evaluate(() => localStorage.removeItem("grain.themeMode"));
    }
  });
}

// A request parked on grain#ci (task 3950's) on a ci_mode: github repo:
// the item cannot be granted, so the card says why and offers
// api:ci-read@<repo> in its place (AccessRequestItemView.Substitute), and
// Grant sends it rather than failing.
const CI_READ =
  'https://grain.dev/capabilities/api#ci-read={"repo":"acme/widgets"}';
const grainCIAccess = {
  summary: "Grant grain#ci so this run can read its CI",
  reason: "grain ci list answered forbidden.",
  createdAt: "2026-11-03T10:00:00Z",
  items: [
    {
      kind: "grant",
      capability: "https://grain.dev/capabilities/grain#ci",
      error:
        'https://grain.dev/capabilities/grain#ci (formerly grain-ci) cannot be attached by hand: grain grants it to every task on a repository whose CI Mode is "Use Grain CI" (the repo\'s page, Settings -> Repos), and to no task on any other. acme/widgets\'s CI Mode is "Use GitHub CI": `grain ci wait` and `grain pr open` need no grant there, and to read its CI runs and logs (grain ci list/show/log, grain pr status, ci://) grant https://grain.dev/capabilities/api#ci-read with {repo: "acme/widgets"} instead',
      substitute: {
        kind: "grant",
        capability: "https://grain.dev/capabilities/api#ci-read",
        with: { repo: "acme/widgets" },
      },
      substituteGrant: CI_READ,
    },
  ],
  holds: { reads: [], writes: [], grants: [], networkAccess: "" },
};

for (const [name, size] of Object.entries(SIZES)) {
  test(`access request card offers api:ci-read for grain#ci (${name})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(size);
    const task = await awaitingTask(request);
    expect(task, `the demo's "${TITLE}" task`).toBeTruthy();
    const decisions = await fakeAccessRequest(page, task.id, grainCIAccess);

    await page.goto(`/tasks/${task.id}`);
    const card = page.getByRole("region", {
      name: "Access request waiting for your decision",
    });
    await expect(card).toBeVisible();
    await expect(
      card.getByText("instead of https://grain.dev/capabilities/grain#ci"),
    ).toBeVisible();
    await expect(card.getByTestId("access-item-error")).toContainText(
      "in its place; Deny declines it.",
    );
    await expect(
      card.getByRole("checkbox", { name: `Grant ${CI_READ}` }),
    ).toBeChecked();
    await expectInside(card, page, size.width);

    const dir = process.env.SHOTS_DIR;
    if (dir) {
      for (const theme of ["light", "dark"]) {
        await page.evaluate(
          (t) => localStorage.setItem("grain.themeMode", t),
          theme,
        );
        await page.emulateMedia({ colorScheme: theme });
        await page.reload();
        await expect(card).toBeVisible();
        await page.waitForTimeout(300);
        await card.screenshot({
          path: `${dir}/access-card-grain-ci-${name}-${theme}.png`,
        });
      }
      await page.evaluate(() => localStorage.removeItem("grain.themeMode"));
      await page.emulateMedia({ colorScheme: "light" });
      await page.reload();
    }

    await card.getByRole("button", { name: "Grant all" }).click();
    await expect.poll(() => decisions.length).toBe(1);
    expect(decisions[0]).toEqual({ verb: "grant", body: {} });
  });
}
