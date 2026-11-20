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

// A request_access card whose items are not all grantable (2026-11-18
// intake walkthrough). Of three items -- a repository off the target list
// (refused), open network (grantable) and grain#ci, which an acme/widgets
// task holds already -- Grant sends one. The task page labelled that
// "Grant all" (and "Grant selected (0)" once it was unticked), and the
// Inbox kept the held item ticked and counted it. Both now share
// classic's rule and words: only the grantable item is ticked, and the
// button reads "Grant 1 of 3", then "Grant 0 of 3" and disabled.

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

const ITEMS = [
  { kind: "read", repo: "acme/billing" },
  { kind: "network", access: "open" },
  { kind: "grant", capability: "https://grain.dev/capabilities/grain#ci" },
];

test("a card with refused and held items names how many of them Grant gives", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const title = `Mixed access request ${Date.now()}`;
  const { id } = await apiPost(demoServer, accessCode, "/api/tasks", {
    title,
    approved: true,
  });
  try {
    await apiPost(
      demoServer,
      accessCode,
      `/api/e2e/tasks/${id}/pending-access`,
      { summary: "Grant billing reads and open network", items: ITEMS },
    );

    // The task page's card.
    await page.goto(`task/${id}`);
    const grant = page.getByTestId("access-grant-btn");
    await expect(grant).toHaveText("Grant 1 of 3");
    const card = grant.locator(
      "xpath=ancestor::*[.//input[@type='checkbox']][1]",
    );
    const boxes = card.locator("input[type=checkbox]");
    await expect(boxes).toHaveCount(3);
    await expect(boxes.nth(0)).toBeDisabled();
    await expect(boxes.nth(1)).toBeChecked();
    await expect(boxes.nth(2)).not.toBeChecked();
    await expect(boxes.nth(2)).toBeDisabled();
    await boxes.nth(1).uncheck();
    await expect(grant).toHaveText("Grant 0 of 3");
    await expect(grant).toBeDisabled();

    // The Inbox's card for the same request agrees.
    await page.goto("inbox");
    await page
      .getByTestId("queue-row")
      .filter({ hasText: title })
      .locator(".row-main")
      .click();
    const inboxGrant = page.getByTestId("grant-access");
    await expect(inboxGrant).toHaveText("Grant 1 of 3");
    const items = page.locator(".access-items input[type=checkbox]");
    await expect(items).toHaveCount(3);
    await expect(items.nth(0)).toBeDisabled();
    await expect(items.nth(1)).toBeChecked();
    await expect(items.nth(2)).not.toBeChecked();
    await expect(items.nth(2)).toBeDisabled();

    // Granting sends only the open-network item, and the task leaves
    // the access group.
    const sent = page.waitForRequest(
      (r) =>
        r.method() === "POST" &&
        r.url().endsWith(`/api/tasks/${id}/access-request/grant`),
    );
    await inboxGrant.click();
    expect((await sent).postDataJSON()).toEqual({ items: [1] });
    await expect(
      page.getByTestId("queue-row").filter({ hasText: title }),
    ).toHaveCount(0, { timeout: 10_000 });
    const detail = await (
      await fetch(`${demoServer}/api/tasks/${id}`, {
        headers: { Authorization: `Bearer ${accessCode}` },
      })
    ).json();
    expect(detail.pendingAccess ?? null).toBeNull();
    expect(detail.networkAccess).toBe("open");
  } finally {
    await fetch(`${demoServer}/api/tasks/${id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});
