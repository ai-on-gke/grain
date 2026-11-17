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

// Granting access on one task in /next/inbox must not lock the rest of
// the Inbox: while the first grant is still applying (held open here at
// the network), its own row says "Applying…" and its Grant is off, but
// the next task's Grant is live and posts at once. Both then land and
// both rows leave. Two real access requests are parked on the demo store
// through its loopback e2e hook (pkg/demo/demo_access_request.go).

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

async function parkedAccessTask(baseURL, code, title) {
  const { id } = await apiPost(baseURL, code, "/api/tasks", {
    title,
    approved: true,
    noRepo: true,
    capabilities: [],
  });
  await apiPost(baseURL, code, `/api/e2e/tasks/${id}/pending-access`, {
    summary: "Open network access",
    reason: "to fetch a dependency",
    items: [{ kind: "network", access: "open" }],
  });
  return id;
}

const row = (page, text) =>
  page.getByTestId("queue-row").filter({ hasText: text });

test("grants access on the next task while the first grant is still applying", async ({
  page,
  demoServer,
  accessCode,
}, testInfo) => {
  const stamp = Date.now();
  const firstTitle = `Concurrent grant first ${stamp}`;
  const secondTitle = `Concurrent grant second ${stamp}`;
  const ids = [];
  try {
    ids.push(await parkedAccessTask(demoServer, accessCode, firstTitle));
    ids.push(await parkedAccessTask(demoServer, accessCode, secondTitle));
    const [first, second] = ids;

    // Hold the first grant open at the network until the second is sent.
    let releaseFirst = () => {};
    const firstHeld = new Promise((resolve) => (releaseFirst = resolve));
    await page.route(
      (url) => url.pathname === `/api/tasks/${first}/access-request/grant`,
      async (route) => {
        await firstHeld;
        await route.continue();
      },
    );
    const grantPosts = [];
    page.on("request", (req) => {
      const m = /\/api\/tasks\/([^/]+)\/access-request\/grant$/.exec(
        new URL(req.url()).pathname,
      );
      if (m && req.method() === "POST") grantPosts.push(m[1]);
    });

    await page.goto("inbox");
    await row(page, firstTitle).locator(".row-main").click();
    await expect(page.getByTestId("grant-access")).toBeEnabled();
    await page.getByTestId("grant-access").click();
    await expect(
      row(page, firstTitle).getByTestId("row-applying"),
    ).toBeVisible();
    await expect(page.getByTestId("grant-access")).toBeDisabled();
    expect(grantPosts).toEqual([first]);

    // The next task is answerable while the first is still in flight.
    await row(page, secondTitle).locator(".row-main").click();
    await expect(page.getByTestId("inspector-title")).toContainText(
      secondTitle,
    );
    await expect(page.getByTestId("grant-access")).toBeEnabled();
    await expect(
      row(page, firstTitle).getByTestId("row-applying"),
    ).toBeVisible();
    // The indicator sits on the row's status line, inside the row.
    const rowBox = await row(page, firstTitle).boundingBox();
    const applying = await row(page, firstTitle)
      .getByTestId("row-applying")
      .boundingBox();
    const pill = await row(page, firstTitle)
      .locator(".status-pill")
      .boundingBox();
    expect(applying.x).toBeGreaterThanOrEqual(rowBox.x);
    expect(applying.x + applying.width).toBeLessThanOrEqual(
      rowBox.x + rowBox.width + 1,
    );
    expect(
      Math.abs(applying.y + applying.height / 2 - (pill.y + pill.height / 2)),
    ).toBeLessThanOrEqual(3);
    await page.screenshot({
      path: testInfo.outputPath("concurrent-grants-applying.png"),
    });
    await page.getByTestId("grant-access").click();
    await expect.poll(() => grantPosts).toEqual([first, second]);

    releaseFirst();
    await expect(row(page, firstTitle)).toHaveCount(0);
    await expect(row(page, secondTitle)).toHaveCount(0);
    await expect(page.getByTestId("action-error")).toHaveCount(0);
  } finally {
    for (const id of ids) await closeQuietly(demoServer, accessCode, id);
  }
});
