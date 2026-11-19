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

// Regressions from the 2026-11-19 responsive navigation and live-session
// walkthrough (docs/test-runs/2026-11-19-responsive-navigation-live-sessions.md):
//
// - Settings -> Preferences segmented theme control ("Light / Dark / System")
//   shrunk inside .s-toggle-row and clipped "Dark" and "System" past the
//   viewport at tablet (820px) and phone (390px, 320px) widths.
// - Clicking a partially clipped "More" trigger in a scrollable .nav-rail let
//   the browser focus-scroll fire the rail scroll listener and slam the menu
//   shut on the same click.
// - Pressing Escape while GrantsPicker's builder was open closed the enclosing
//   NewTaskSheet or Session settings panel instead of closing only the builder
//   and returning focus to "+ Add API scope or method grant".
// - Toggling "Tasks approved" in /next/sessions posted POST /api/tasks/{id}/approve
//   and dropped tasksApproved instead of saving via PUT /api/console-sessions/{id}.
// - /next/sessions omitted the network-not-isolated warning when
//   effectiveNetworkAccess differed from networkAccess.
// - On a scrollable session chat, a failed send did not scroll Retry/Edit into
//   view, and blurring the focused composer shrank it by 18px on mousedown so
//   real clicks on Edit/Retry missed the button.
// - /next/sessions did not render inline request_access / local-script /
//   config-change approval cards for the session's backing task.

import { expect, test } from "./fixtures";

test("Preferences theme segmented buttons stay unclipped across desktop, tablet and phone @phone @dark", async ({
  page,
}) => {
  await page.goto("settings/preferences");
  await expect(
    page.getByRole("heading", { name: "Preferences", exact: true }),
  ).toBeVisible();

  for (const vp of [
    { width: 1440, height: 900 },
    { width: 820, height: 1180 },
    { width: 390, height: 844 },
    { width: 320, height: 568 },
  ]) {
    await page.setViewportSize(vp);
    const seg = page.locator(".segmented").first();
    await expect(seg).toBeVisible();
    const segBox = await seg.boundingBox();
    expect(segBox, `segmented box at ${vp.width}x${vp.height}`).toBeTruthy();
    expect(segBox!.x).toBeGreaterThanOrEqual(0);
    expect(segBox!.x + segBox!.width).toBeLessThanOrEqual(vp.width + 1);

    for (const label of ["Light", "Dark", "System"]) {
      const btn = seg.getByRole("button", { name: label });
      await expect(btn).toBeVisible();
      const box = await btn.boundingBox();
      expect(box, `${label} box at ${vp.width}x${vp.height}`).toBeTruthy();
      expect(box!.x).toBeGreaterThanOrEqual(segBox!.x - 1);
      expect(box!.x + box!.width).toBeLessThanOrEqual(
        Math.min(segBox!.x + segBox!.width, vp.width) + 1,
      );
    }

    const overflowX = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(
      overflowX,
      `overflowX at ${vp.width}x${vp.height}`,
    ).toBeLessThanOrEqual(1);
  }
});

test("clicking a partially clipped rail More button keeps the menu open on first click", async ({
  page,
}) => {
  // 1024x768 makes the 12-destination rail scroll vertically so rail-more starts
  // below/at the bottom fold and focus-scrolls .nav-rail when clicked.
  await page.setViewportSize({ width: 1024, height: 768 });
  await page.goto("inbox");
  const railMore = page.getByTestId("rail-more");
  await expect(railMore).toBeVisible();

  await railMore.click();
  const menu = page.getByTestId("rail-more-menu");
  await expect(menu).toBeVisible();
  await expect(page.getByTestId("rail-customize-nav")).toBeVisible();
  const box = await menu.boundingBox();
  expect(box).toBeTruthy();
  expect(box!.y).toBeGreaterThanOrEqual(0);
  expect(box!.y + box!.height).toBeLessThanOrEqual(768 + 1);
});

test("Escape closes GrantsPicker inner dropdown then builder without closing NewTaskSheet or Session settings", async ({
  page,
}) => {
  // 1. Inside NewTaskSheet
  await page.goto("inbox");
  await page.getByTestId("rail-new-task").click();
  const sheet = page.locator('[role="dialog"]').first();
  await expect(sheet).toBeVisible();
  const moreToggle = sheet
    .locator("button", { hasText: /More|Advanced|Grants|Options/i })
    .first();
  if (await moreToggle.isVisible()) {
    await moreToggle.click();
  }
  const ntAddOpen = sheet.getByTestId("nt-grants-add-open");
  await ntAddOpen.click();
  const ntBuilder = sheet.getByTestId("nt-grants-builder");
  await expect(ntBuilder).toBeVisible();

  // Open inner pill-select inside builder: 1st Escape closes only the listbox.
  await ntBuilder.locator(".pill-select-btn").first().click();
  await expect(ntBuilder.locator('[role="listbox"]')).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(ntBuilder.locator('[role="listbox"]')).toHaveCount(0);
  await expect(ntBuilder).toBeVisible();
  await expect(sheet).toBeVisible();

  // 2nd Escape closes only the builder, keeps NewTaskSheet open, restores focus.
  await page.keyboard.press("Escape");
  await expect(ntBuilder).toHaveCount(0);
  await expect(sheet).toBeVisible();
  await expect(ntAddOpen).toBeFocused();
  await page.keyboard.press("Escape");

  // 2. Inside Session settings panel
  await page.goto("sessions/director");
  await page.getByTestId("session-settings-toggle").click();
  const settingsPanel = page.getByTestId("session-settings-panel");
  await expect(settingsPanel).toBeVisible();
  const sessAddOpen = page.getByTestId("session-grants-add-open");
  await sessAddOpen.click();
  const sessBuilder = page.getByTestId("session-grants-builder");
  await expect(sessBuilder).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(sessBuilder).toHaveCount(0);
  await expect(settingsPanel).toBeVisible();
  await expect(sessAddOpen).toBeFocused();
});

test("session tasksApproved persists across reload, network-not-isolated warning shows, and inline approval gates render @phone @dark", async ({
  page,
}) => {
  await page.goto("sessions/director");
  await page.getByTestId("session-settings-toggle").click();
  const ta = page.getByTestId("session-tasks-approved");
  await expect(ta).toBeVisible();
  const initialChecked = await ta.isChecked();

  try {
    await ta.click();
    await expect(ta).toHaveJSProperty("checked", !initialChecked);
    await page.reload();
    await page.getByTestId("session-settings-toggle").click();
    await expect(page.getByTestId("session-tasks-approved")).toHaveJSProperty(
      "checked",
      !initialChecked,
    );
  } finally {
    // Restore Director's tasksApproved flag on the worker's demo server.
    await page.request.put(
      new URL("/api/console-sessions/director", page.url()).toString(),
      { data: { tasksApproved: initialChecked } },
    );
  }

  // Stub effectiveNetworkAccess mismatch and pending approval gates on Director.
  await page.route("**/api/console-sessions", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const response = await route.fetch();
    const json = await response.json();
    for (const s of json.sessions ?? []) {
      if (s.id === "director") {
        s.networkAccess = "none";
        s.effectiveNetworkAccess = "open";
      }
    }
    await route.fulfill({ response, json });
  });
  await page.route("**/api/tasks/*", async (route) => {
    const req = route.request();
    if (req.method() !== "GET" || !/\/api\/tasks\/\d+(\?|$)/.test(req.url())) {
      return route.fallback();
    }
    const headers = { ...req.headers() };
    delete headers["if-none-match"];
    const response = await route.fetch({ headers });
    if (response.status() !== 200) return route.fulfill({ response });
    const json = await response.json();
    json.pendingAccess = {
      summary: "Grant read access to acme/infra",
      reason: "Need deployment manifests",
      items: [
        { kind: "read", repo: "acme/infra", grant: "repo#read@acme/infra" },
      ],
    };
    json.localScriptApprovals = [
      {
        id: "lsa-e2e-1",
        taskId: String(json.id),
        script: "status-check.sh",
        args: ["--brief"],
        reason: "Inspect cluster status",
        status: "pending",
      },
    ];
    await route.fulfill({ response, json });
  });

  await page.goto("sessions/director");
  await page.getByTestId("session-settings-toggle").click();
  await expect(page.getByTestId("session-network-not-isolated")).toBeVisible();
  await expect(page.getByTestId("session-network-not-isolated")).toContainText(
    "cannot withhold",
  );
  const gates = page.getByTestId("session-inline-gates");
  await expect(gates).toBeVisible();
  await expect(gates).toContainText("Grant read access to acme/infra");
  await expect(gates).toContainText("status-check.sh");
});

test("failed send in a scrollable chat reveals Edit and accepts a first click without composer blur jump", async ({
  page,
}) => {
  test.info().annotations.push({
    type: "allows-page-error",
    description: "the 500 failed send is logged by the browser",
  });
  // Use a shorter desktop viewport so Director's conversation is guaranteed to overflow chat-log.
  await page.setViewportSize({ width: 1440, height: 680 });
  await page.goto("sessions/director");
  const composer = page.getByTestId("composer");
  await expect(composer).toBeVisible();

  await page.route("**/api/tasks/*/comments", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    await route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "simulated send failure" }),
    });
  });

  await composer.fill("Keep this message on Edit");
  await composer.press("Enter");
  const editBtn = page.getByTestId("pending-edit");
  await expect(editBtn).toBeVisible();
  await expect(composer).toBeFocused();

  // Wait a tick for the newlyFailed auto-scroll effect to settle, then verify
  // Edit is genuinely unobstructed above the composer before clicking it.
  await expect
    .poll(async () =>
      page.evaluate(() => {
        const b = document.querySelector('[data-testid="pending-edit"]');
        if (!b) return "";
        const r = b.getBoundingClientRect();
        const hit = document.elementFromPoint(
          r.x + r.width / 2,
          r.y + r.height / 2,
        );
        return hit?.getAttribute("data-testid") ?? "";
      }),
    )
    .toBe("pending-edit");

  // Single real pointer click while composer is focused must hit Edit without
  // the composer shrinking and shifting the button downward between mousedown and mouseup.
  await editBtn.click();
  await expect(composer).toHaveValue("Keep this message on Edit");
  await expect(composer).toBeFocused();
  await expect(page.getByTestId("pending-0")).toHaveCount(0);
});
