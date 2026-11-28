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

// Regressions from the responsive navigation and live-session walkthrough
// (docs/test-runs/2026-11-18-responsive-navigation-live-sessions.md):
//
// - B1: ticking "Tasks approved" in a session's settings saves it on the
//   session (PUT /api/console-sessions/{id}) and survives a reload, rather
//   than approving the session's own task and reverting;
// - B2: a session asking for no network on a sandbox backend that cannot
//   withhold one says so, as the classic pane does;
// - B3: "+ Add API scope or method grant" hands focus into the builder,
//   and Escape (or Done) closes only the builder -- not the settings panel
//   around it -- with focus back on the button;
// - B4: Start a fresh chat hands focus to its choices, Escape hands it
//   back, and starting clean lands it in the composer;
// - B5: a short screen with the list beside the chat (960x540, 1024x600)
//   keeps the conversation and Send on screen.
//
// The only interception is B2's: GET /api/console-sessions answered with
// the demo's own list plus effectiveNetworkAccess "open", as a host-
// sandboxes deployment (which cannot isolate a run's network) answers.

import { expect, test } from "./fixtures";

async function newSession(page, title) {
  await page.goto("sessions");
  await page.getByTestId("new-session").click();
  await page.getByTestId("new-session-name").fill(title);
  await page.getByTestId("new-session-create").click();
  await expect(
    page.getByTestId("chat-pane").getByRole("heading", { name: title }),
  ).toBeVisible();
  return page.url().split("/sessions/")[1];
}

async function openSettings(page) {
  await page.getByTestId("session-settings-toggle").click();
  await expect(page.getByTestId("session-settings-panel")).toBeVisible();
}

async function sessionRow(page, id) {
  const res = await page.request.get("/api/console-sessions");
  const body = await res.json();
  return (body.sessions ?? []).find((s) => s.id === id);
}

test("Tasks approved is saved on the session and survives a reload", async ({
  page,
}) => {
  const id = await newSession(page, `Approved ${Date.now()}`);
  await openSettings(page);
  const box = page.getByTestId("session-tasks-approved");
  await expect(box).not.toBeChecked();
  const posts = [];
  page.on("request", (req) => {
    if (req.method() === "POST" && req.url().includes("/approve")) {
      posts.push(req.url());
    }
  });
  const saved = page.waitForResponse(
    (res) =>
      res.request().method() === "PUT" &&
      res.url().includes(`/api/console-sessions/${id}`),
  );
  await box.click();
  expect((await saved).ok()).toBe(true);
  expect(posts).toEqual([]);
  expect((await sessionRow(page, id))?.tasksApproved).toBe(true);

  await page.reload();
  await openSettings(page);
  await expect(page.getByTestId("session-tasks-approved")).toBeChecked();

  const cleared = page.waitForResponse(
    (res) =>
      res.request().method() === "PUT" &&
      res.url().includes(`/api/console-sessions/${id}`),
  );
  await page.getByTestId("session-tasks-approved").click();
  await cleared;
  await page.reload();
  await openSettings(page);
  await expect(page.getByTestId("session-tasks-approved")).not.toBeChecked();
});

test("a session whose 'none' network cannot be honoured says so @dark", async ({
  page,
  axe,
}) => {
  const id = await newSession(page, `No network ${Date.now()}`);
  await openSettings(page);
  await expect(page.getByTestId("session-network-not-isolated")).toHaveCount(0);
  await page.route("**/api/console-sessions", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    for (const s of body.sessions ?? []) {
      if (s.id === id) {
        s.networkAccess = "none";
        s.effectiveNetworkAccess = "open";
      }
    }
    return route.fulfill({ response: res, json: body });
  });
  await page.reload();
  await openSettings(page);
  const warn = page.getByTestId("session-network-not-isolated");
  await expect(warn).toBeVisible();
  await expect(warn).toContainText("cannot withhold a run's network");
  expect(await axe()).toEqual([]);
  await page.unroute("**/api/console-sessions");
});

for (const size of [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`the grant builder and fresh-chat choices keep focus (${size.name})`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: size.width, height: size.height });
    await newSession(page, `Focus ${size.name} ${Date.now()}`);
    await openSettings(page);

    // B3: the builder takes focus, and Escape closes only the builder.
    const addOpen = page.getByTestId("session-grants-add-open");
    await addOpen.click();
    const builder = page.getByTestId("session-grants-builder");
    await expect(builder).toBeVisible();
    await expect(page.getByTestId("session-grants-mode-api")).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(builder).toHaveCount(0);
    await expect(page.getByTestId("session-settings-panel")).toBeVisible();
    await expect(addOpen).toBeFocused();
    // Done does the same.
    await addOpen.click();
    await builder.getByRole("button", { name: "Done" }).click();
    await expect(builder).toHaveCount(0);
    await expect(addOpen).toBeFocused();
    // With the builder closed, Escape closes the panel as before.
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("session-settings-panel")).toHaveCount(0);

    // B4: Start a fresh chat.
    const freshOpen = page.getByTestId("fresh-chat-open");
    await freshOpen.click();
    await expect(page.getByTestId("fresh-chat-handover")).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("fresh-chat-handover")).toHaveCount(0);
    await expect(freshOpen).toBeFocused();
    await freshOpen.click();
    await page.getByTestId("fresh-chat-cancel").click();
    await expect(freshOpen).toBeFocused();
    await freshOpen.click();
    await page.getByTestId("fresh-chat-clean").click();
    await expect(page.getByTestId("fresh-chat-clean")).toHaveCount(0);
    await expect(page.getByTestId("composer")).toBeFocused();
  });
}

// B5: on a short screen where the session list stays beside the chat, the
// chat head left the conversation 40px tall and, at 960x540, put Send
// below the fold with nothing to scroll.
for (const size of [
  { width: 960, height: 540 },
  { width: 1024, height: 600 },
]) {
  test(`a short ${size.width}x${size.height} screen keeps the conversation and Send on screen`, async ({
    page,
  }) => {
    await page.setViewportSize(size);
    await page.goto("sessions/director");
    const send = page.getByTestId("send");
    await expect(send).toBeVisible();
    const geo = await page.evaluate(() => {
      const log = document
        .querySelector('[data-testid="chat-log"]')
        .getBoundingClientRect();
      const btn = document.querySelector('[data-testid="send"]');
      const r = btn.getBoundingClientRect();
      const hit = document.elementFromPoint(
        r.left + r.width / 2,
        r.top + r.height / 2,
      );
      return {
        logHeight: log.height,
        sendBottom: r.bottom,
        sendOnTop: btn.contains(hit),
        overflow: document.documentElement.scrollWidth - window.innerWidth,
      };
    });
    expect(geo.logHeight).toBeGreaterThanOrEqual(200);
    expect(geo.sendBottom).toBeLessThanOrEqual(size.height);
    expect(geo.sendOnTop).toBe(true);
    expect(geo.overflow).toBeLessThanOrEqual(0);
  });
}
