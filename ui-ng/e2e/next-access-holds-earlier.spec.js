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

// A request_access card for a request that repeats items people answered
// before (2026-11-19 intake walkthrough, task 7304). The server sends what
// the task holds (pendingAccess.holds) and the earlier answers on the same
// items (pendingAccess.earlier, latest first), and classic's card shows
// both; /next/ showed neither. Now the task page's card and the Inbox's
// say who answered, how, and why, and list what the task already has --
// on a phone too, inside the card.

async function api(baseURL, code, method, path, data) {
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!res.ok)
    throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  const text = await res.text();
  return text ? JSON.parse(text) : null;
}

const NETWORK = { kind: "network", access: "open" };
const ADMIN = {
  kind: "grant",
  capability: "https://grain.dev/capabilities/api#tasks:admin",
};
const DENIAL = "tasks:admin is **too broad**; file a follow-up instead";

// shoot saves a look at the page for the review (not an assertion):
// SHOTS=<dir> names where.
async function shoot(page, name) {
  const dir = process.env.SHOTS;
  if (!dir) return;
  await page.screenshot({
    path: `${dir}/${name}-${test.info().project.name}.png`,
    fullPage: true,
  });
}

// inside fails unless inner's box lies within outer's.
async function expectInside(inner, outer) {
  const a = await inner.boundingBox();
  const b = await outer.boundingBox();
  expect(a).not.toBeNull();
  expect(b).not.toBeNull();
  expect(a.x).toBeGreaterThanOrEqual(b.x - 0.5);
  expect(a.x + a.width).toBeLessThanOrEqual(b.x + b.width + 0.5);
}

test("a repeated access request shows the earlier answers and what the task holds @phone @dark", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const title = `Repeated access request ${Date.now()}`;
  const { id } = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title,
    approved: true,
  });
  const ask = (summary, items) =>
    api(demoServer, accessCode, "POST", `/api/e2e/tasks/${id}/pending-access`, {
      summary,
      items,
    });
  try {
    // First request: open network is granted, tasks:admin declined.
    await ask("Open network and tasks:admin", [NETWORK, ADMIN]);
    await api(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${id}/access-request/grant`,
      { items: [0] },
    );
    // Second: tasks:admin again, denied with a reason.
    await ask("tasks:admin to file follow-ups", [ADMIN]);
    await api(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${id}/access-request/deny`,
      { reason: DENIAL },
    );
    // Third: the same again, which the card is for.
    await ask("tasks:admin, once more", [ADMIN]);

    await page.goto(`task/${id}`);
    const card = page.locator("section.gate-card", {
      has: page.getByTestId("access-grant-btn"),
    });
    await expect(card).toBeVisible();

    // Latest first: the denial, then the partial grant.
    const earlier = card.getByTestId("access-earlier");
    await expect(earlier).toHaveCount(2);
    await expect(earlier.nth(0)).toContainText("Denied before");
    await expect(earlier.nth(0)).toContainText("declined");
    await expect(
      earlier.nth(0).getByTestId("access-earlier-reason").locator("strong"),
    ).toHaveText("too broad");
    await expect(earlier.nth(1)).toContainText("Partly granted before");
    await expect(earlier.nth(1)).toContainText("granted network open");
    // It sits above the items it is about.
    const items = card.getByTestId("access-items-list");
    expect((await earlier.nth(0).boundingBox()).y).toBeLessThan(
      (await items.boundingBox()).y,
    );

    // What the task holds now: the open network the first answer gave.
    const holds = card.getByTestId("access-holds");
    await expect(holds).toContainText("Already holds");
    await expect(holds.locator(".access-hold")).toContainText(["network open"]);
    expect((await holds.boundingBox()).y).toBeGreaterThan(
      (await items.boundingBox()).y,
    );

    // Nothing spills out of the card, at any width.
    await expectInside(earlier.nth(0), card);
    await expectInside(holds, card);
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      ),
    ).toBeLessThanOrEqual(0);
    await shoot(page, "access-card-task");

    // The Inbox's card for the same request says the same.
    await page.goto("inbox");
    await page
      .getByTestId("queue-row")
      .filter({ hasText: title })
      .locator(".row-main")
      .click();
    const inboxGrant = page.getByTestId("grant-access");
    await expect(inboxGrant).toBeVisible();
    // It sits with the task's evidence, below the dock, so the dock's
    // actions keep their room: nothing covers Grant.
    const evidence = page.locator(".inspector-body .evidence-card").first();
    const inboxEarlier = evidence.getByTestId("access-earlier");
    await expect(inboxEarlier).toHaveCount(2);
    await expect(inboxEarlier.nth(0)).toContainText("Denied before");
    await expect(evidence.getByTestId("access-holds")).toContainText(
      "network open",
    );
    await expectInside(inboxEarlier.nth(0), evidence);
    expect(
      await inboxGrant.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const hit = document.elementFromPoint(
          r.x + r.width / 2,
          r.y + r.height / 2,
        );
        return el === hit || el.contains(hit);
      }),
    ).toBe(true);
    await shoot(page, "access-card-inbox");
  } finally {
    await fetch(`${demoServer}/api/tasks/${id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});
