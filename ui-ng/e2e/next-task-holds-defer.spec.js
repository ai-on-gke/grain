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

// The two /next/task/:id gaps the 2026-11-13 task-intake walkthrough
// found (docs/test-runs/2026-11-13-task-intake-inbox-lifecycle.md, "Gaps
// on /next/"), over a real `grain demo -human-review -sensitive-hold`:
//
// - "Charge in cents" is held by its repository's payments trigger
//   (pathTriggerHold: required) and the page showed no hold and no link
//   to the review task the trigger filed;
// - Defer could only put a task aside indefinitely, never until a date.

test.use({ demoArgs: ["-human-review", "-sensitive-hold"] });

async function apiJson(baseURL, code, method, p, body) {
  const res = await fetch(`${baseURL}${p}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${method} ${p}: ${res.status} ${await res.text()}`);
  }
  const txt = await res.text();
  return txt ? JSON.parse(txt) : null;
}

async function heldTask(demoServer, accessCode) {
  const tasks = await apiJson(demoServer, accessCode, "GET", "/api/tasks");
  const list = Array.isArray(tasks) ? tasks : (tasks.tasks ?? []);
  const held = list.find((t) => t.title === "Charge in cents");
  expect(held, "the -sensitive-hold demo seeds Charge in cents").toBeTruthy();
  return apiJson(demoServer, accessCode, "GET", `/api/tasks/${held.id}`);
}

test("shows the path-trigger hold and links the review task it filed @phone @dark", async ({
  page,
  demoServer,
  accessCode,
  axe,
}) => {
  const t = await heldTask(demoServer, accessCode);
  expect(t.pathTriggerHold).toBe("required");
  const [, reviewId] = t.pathTriggerTasks[0].split("=");

  await page.goto(`task/${t.id}`);
  const hold = page.getByRole("region", {
    name: "Held by this repository's gating triggers",
  });
  await expect(hold).toBeVisible();
  await expect(hold).toContainText("Held by this repository's gating triggers");
  await expect(hold).toContainText("human: required");
  await expect(hold).toContainText(".grain/triggers/payments.yml");
  await expect(hold.getByTestId("approval-rule")).toContainText(/Waiting for /);

  // The card sits inside the page at every width: nothing runs off the
  // right edge on a phone.
  const box = await hold.boundingBox();
  const vw = page.viewportSize().width;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(vw + 0.5);

  expect(await axe()).toEqual([]);

  const link = hold.getByRole("link", { name: `Review task #${reviewId}` });
  await link.click();
  await expect(page).toHaveURL(new RegExp(`/next/task/${reviewId}(\\?|$)`));
  const review = await apiJson(
    demoServer,
    accessCode,
    "GET",
    `/api/tasks/${reviewId}`,
  );
  await expect(page.locator("#task-title")).toContainText(review.title);
});

test("defers a proposal until a picked day and shows when it wakes @phone", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const created = await apiJson(demoServer, accessCode, "POST", "/api/tasks", {
    title: `Defer until walk ${Date.now()}`,
    description: "Put aside until a date",
    noRepo: true,
  });
  try {
    await page.goto(`task/${created.id}`);
    await expect(
      page.getByRole("button", { name: "Defer", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Defer until…" }).click();
    const date = page.getByLabel("Wake on");
    await expect(date).toBeFocused();
    const submit = page.getByRole("button", {
      name: "Defer until",
      exact: true,
    });
    await expect(submit).toBeDisabled();

    // A week after the earliest day the picker offers (tomorrow).
    const min = await date.getAttribute("min");
    const [y, m, d] = min.split("-").map(Number);
    const pick = new Date(Date.UTC(y, m - 1, d + 7)).toISOString().slice(0, 10);
    await date.fill(pick);
    await expect(page.getByTestId("defer-until-hint")).toContainText("Wakes");
    await expect(submit).toBeEnabled();
    // The picker and its button share one centre line.
    const db = await date.boundingBox();
    const sb = await submit.boundingBox();
    expect(
      Math.abs(db.y + db.height / 2 - (sb.y + sb.height / 2)),
    ).toBeLessThan(2);

    // Wait for the server's answer, not just the request leaving: the
    // task is read back straight after, and must already be deferred.
    const [resp] = await Promise.all([
      page.waitForResponse(
        (r) =>
          r.url().endsWith(`/api/tasks/${created.id}/defer`) &&
          r.request().method() === "POST",
      ),
      submit.click(),
    ]);
    expect(resp.ok()).toBe(true);
    const until = JSON.parse(resp.request().postData() || "{}").deferredUntil;
    expect(until).toBeTruthy();
    expect(Date.parse(until)).toBeGreaterThan(Date.now());

    const after = await apiJson(
      demoServer,
      accessCode,
      "GET",
      `/api/tasks/${created.id}`,
    );
    expect(after.state).toBe("deferred");
    expect(Date.parse(after.deferredUntil)).toBe(Date.parse(until));

    // The page the person is on (or comes back to) says when it wakes.
    await page.goto(`task/${created.id}`);
    const wake = page.getByTestId("task-deferred-until");
    await expect(wake).toContainText("Picks itself back up on");
    await expect(wake).toContainText(pick.slice(0, 4));
    await expect(
      page.getByRole("button", { name: "Bring back" }),
    ).toBeVisible();
  } finally {
    await fetch(`${demoServer}/api/tasks/${created.id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});
