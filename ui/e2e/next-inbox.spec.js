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

import AxeBuilder from "@axe-core/playwright";
import { test, expect } from "./fixtures.js";

// The Angular Inbox at /next/inbox (ui-ng/, design/design/angular-ui.design.md)
// against a real `grain demo` and the real built frontend: signing in
// lands on it, seeded and newly filed items show up (the new ones live,
// over /api/events), an approval and a reply each take their row away
// once the server stops listing it, the two UIs link to each other, a
// viewer is offered nothing it cannot do, and axe finds nothing in
// either theme or at a phone width.
//
// Everything this file answers it files itself, so the demo store's own
// fixtures are left for the specs that need them.

async function apiPost(baseURL, code, path, data) {
  const res = await fetch(`${baseURL}${path}`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!res.ok)
    throw new Error(`POST ${path}: ${res.status} ${await res.text()}`);
  const text = await res.text();
  return text ? JSON.parse(text) : null;
}

async function closeQuietly(baseURL, code, id) {
  if (!id) return;
  await fetch(`${baseURL}/api/tasks/${id}/close`, {
    method: "POST",
    headers: { Authorization: `Bearer ${code}` },
  }).catch(() => {});
}

const row = (page, text) =>
  page.getByTestId("queue-row").filter({ hasText: text });

test("signs in at /next/inbox, sees seeded and live items, approves one and answers another", async ({
  browser,
  demoServer,
  accessCode,
}) => {
  const context = await browser.newContext({
    baseURL: demoServer,
    viewport: { width: 1280, height: 800 },
    storageState: { cookies: [], origins: [] },
  });
  const page = await context.newPage();
  let proposalId = "";
  let questionId = "";
  try {
    // Signed out, /next/inbox is the daemon's own access-code page.
    const first = await page.goto("/next/inbox");
    expect(first.status()).toBe(401);
    await expect(
      page.getByRole("heading", { name: "Enter access code" }),
    ).toBeVisible();
    await page.getByLabel("Your email").fill("alice@example.com");
    await page.getByLabel("Access code").fill(accessCode);
    await page.getByRole("button", { name: "Continue" }).click();

    await expect(page).toHaveURL(/\/next\/inbox$/);
    await expect(page.getByTestId("next-inbox-headline")).toContainText(
      "waiting on you",
    );
    // What `grain demo` seeded is waiting there. Not one named row: other
    // specs on this worker answer seeded items (inbox.spec.js spends the
    // parked question), so this asks only that the seeded queue is shown.
    await expect(page.getByTestId("queue-row").first()).toBeVisible();

    // Queue and inspector side by side, nothing overflowing sideways.
    const queue = await page.getByTestId("next-inbox-queue").boundingBox();
    const inspector = await page
      .getByTestId("next-inbox-inspector")
      .boundingBox();
    expect(inspector.x).toBeGreaterThanOrEqual(queue.x + queue.width - 2);
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      ),
    ).toBeLessThanOrEqual(0);

    // Filed while the page is open: they arrive without a reload.
    const proposalTitle = `Next inbox proposal ${Date.now()}`;
    proposalId = (
      await apiPost(demoServer, accessCode, "/api/tasks", {
        title: proposalTitle,
        approved: false,
      })
    ).id;
    const questionTitle = `Next inbox question ${Date.now()}`;
    questionId = (
      await apiPost(demoServer, accessCode, "/api/tasks", {
        title: questionTitle,
        approved: true,
      })
    ).id;
    await apiPost(demoServer, accessCode, `/api/tasks/${questionId}/messages`, {
      summary: "Keep the classic route alongside /next/?",
      body: "Both UIs share one session cookie.",
    });

    await expect(row(page, proposalTitle)).toBeVisible();
    await row(page, proposalTitle).locator(".row-main").click();
    await expect(page.getByTestId("inspector-open-task")).toHaveAttribute(
      "href",
      `/tasks/${proposalId}`,
    );
    await page.getByTestId("action-approve").click();
    await expect(row(page, proposalTitle)).toHaveCount(0);

    await expect(row(page, questionTitle)).toBeVisible();
    await row(page, questionTitle).locator(".row-main").click();
    await expect(page.getByTestId("resolution-question")).toContainText(
      "Both UIs share one session cookie.",
    );
    await page
      .getByTestId("reply-input")
      .fill("Keep both while pages move over.");
    await page.getByTestId("send-reply").click();
    await expect(row(page, questionTitle)).toHaveCount(0);
  } finally {
    await closeQuietly(demoServer, accessCode, proposalId);
    await closeQuietly(demoServer, accessCode, questionId);
    await context.close();
  }
});

test("links the classic Inbox and the new one both ways, and honours the default", async ({
  page,
}) => {
  await page.goto("/inbox");
  const tryNext = page.getByRole("link", { name: "Try the new Inbox" });
  await expect(tryNext).toHaveAttribute("href", "/next/inbox");
  await tryNext.click();
  await expect(page).toHaveURL(/\/next\/inbox$/);

  const prefer = page.getByTestId("prefer-next-toggle");
  await expect(prefer).toHaveAttribute("aria-pressed", "false");
  await prefer.click();
  await expect(prefer).toHaveAttribute("aria-pressed", "true");
  await page.goto("/inbox");
  await expect(page).toHaveURL(/\/next\/inbox$/);

  await page.getByTestId("classic-inbox-link").click();
  await expect(page).toHaveURL(/\/inbox/);
  await expect(
    page.getByRole("link", { name: "Try the new Inbox" }),
  ).toBeVisible();
  await page.evaluate(() => localStorage.removeItem("grain.ui.preferNext"));
});

test("offers a viewer nothing it cannot do, and axe passes in light and dark", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const title = `Viewer proposal ${Date.now()}`;
  const { id } = await apiPost(demoServer, accessCode, "/api/tasks", {
    title,
    approved: false,
  });
  try {
    await page.route(
      (url) => url.pathname === "/api/config",
      async (route) => {
        const response = await route.fetch();
        const json = {
          ...(await response.json()),
          access: {
            role: "grain/viewer",
            roles: ["grain/viewer"],
            full: false,
            scopes: ["tasks:read"],
          },
        };
        await route.fulfill({ response, json });
      },
    );
    await page.addInitScript(() =>
      localStorage.setItem("grain.themeMode", "light"),
    );
    await page.goto("/next/inbox");
    await row(page, title).locator(".row-main").click();
    await expect(page.getByTestId("action-approve")).toBeDisabled();
    await expect(page.getByTestId("rbac-denied")).toContainText("grain/viewer");

    const light = await new AxeBuilder({ page }).analyze();
    expect(light.violations).toEqual([]);

    await page.getByTestId("theme-toggle").click();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    const dark = await new AxeBuilder({ page }).analyze();
    expect(dark.violations).toEqual([]);
  } finally {
    await closeQuietly(demoServer, accessCode, id);
  }
});

test("at a phone width the inspector is a full-screen sheet, and axe passes", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/next/inbox");
  await expect(page.getByTestId("next-inbox-headline")).toBeVisible();
  await expect(page.locator(".nav-rail")).toBeHidden();
  await expect(page.locator(".compact-nav")).toBeVisible();
  await expect(page.getByTestId("next-inbox-inspector")).toBeHidden();
  expect(
    await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    ),
  ).toBeLessThanOrEqual(0);

  await page.getByTestId("queue-row").first().locator(".row-main").click();
  const sheet = page.getByTestId("next-inbox-inspector");
  await expect(sheet).toBeVisible();
  const box = await sheet.boundingBox();
  expect(box.x).toBe(0);
  expect(box.width).toBe(390);

  const phone = await new AxeBuilder({ page }).analyze();
  expect(phone.violations).toEqual([]);

  await page.getByRole("button", { name: "Close inspector" }).click();
  await expect(sheet).toBeHidden();
});
