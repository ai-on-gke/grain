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

// The shell-level gaps the 2026-11-13 deployment-operations walkthrough
// found under /next/ (docs/test-runs/2026-11-13-deployment-operations-
// task-5390.md): nothing said the deployment had become unreachable --
// for a dropped connection or for one that accepted the request and never
// answered -- and a pipeline pause showed nowhere but Fleet.

// The shell's config poll runs every 5s and gives a read 10s before it
// counts as unanswered, so a hang is noticed within 15s of starting.
const NOTICE_MS = 20_000;

async function expectBannerAboveShell(page) {
  const banner = page.getByTestId("disconnected-banner");
  const box = await banner.boundingBox();
  expect(box).not.toBeNull();
  expect(Math.round(box.y)).toBe(0);
  // It pushes the shell down rather than covering it: the rail (desktop)
  // and the top bar both start where the banner ends.
  const top = await page.locator("header.top-bar").boundingBox();
  expect(top.y).toBeGreaterThanOrEqual(box.y + box.height - 1);
  const rail = page.getByRole("navigation", { name: "Primary destinations" });
  if (await rail.isVisible()) {
    const r = await rail.boundingBox();
    expect(r.y).toBeGreaterThanOrEqual(box.y + box.height - 1);
    expect(r.x).toBe(0);
  }
}

test("a dropped connection raises the disconnected banner, and an answer clears it @phone", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await page.goto("fleet");
  const banner = page.getByTestId("disconnected-banner");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expect(banner).toBeHidden();

  await page.route("**/api/**", (r) => r.abort());
  try {
    await expect(banner).toBeVisible({ timeout: NOTICE_MS });
    await expect(banner).toHaveAttribute("role", "status");
    await expect(banner).toContainText(
      "This deployment cannot be reached: the connection to it failed.",
    );
    await expect(banner).toContainText("retrying automatically");
    await expectBannerAboveShell(page);
  } finally {
    await page.unroute("**/api/**");
  }
  // No reload: the next poll's answer takes it down.
  await expect(banner).toBeHidden({ timeout: NOTICE_MS });
  const top = await page.locator("header.top-bar").boundingBox();
  expect(Math.round(top.y)).toBe(0);
});

test("a deployment that never answers raises the banner, mirrored into an open dialog", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await page.goto("fleet");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  const banner = page.getByTestId("disconnected-banner");

  // Open New task first: a dialog makes the shell inert, so the banner's
  // own copy is mirrored inside it.
  await page.getByTestId("rail-new-task").click();
  const dialog = page.getByTestId("new-task-sheet");
  await expect(dialog).toBeVisible();

  // Held open, never answered.
  const held = [];
  await page.route("**/api/**", (r) => {
    held.push(r);
  });
  try {
    await expect(banner).toContainText(
      "it accepted the connection but sent no answer within 10 seconds",
      { timeout: NOTICE_MS },
    );
    const mirror = dialog.getByTestId("disconnected-banner-mirror");
    await expect(mirror).toBeVisible();
    await expect(mirror).toHaveAttribute("role", "status");
    await expect(mirror).toContainText("sent no answer within 10 seconds");
  } finally {
    await page.unroute("**/api/**");
    for (const r of held) await r.continue().catch(() => undefined);
  }
  await expect(banner).toBeHidden({ timeout: NOTICE_MS });
  await expect(dialog.getByTestId("disconnected-banner-mirror")).toHaveCount(0);
});

test("a pipeline pause shows in the alerts bell with a link to its fix, and leaves when lifted", async ({
  page,
  demo,
}) => {
  test.setTimeout(90_000);
  const api = (method, path, body) =>
    fetch(`${demo.url}${path}`, {
      method,
      headers: {
        Authorization: `Bearer ${demo.code}`,
        ...(body ? { "Content-Type": "application/json" } : {}),
      },
      body: body ? JSON.stringify(body) : undefined,
    });

  await page.goto("inbox");
  const bell = page.getByTestId("alerts-bell");
  await expect(bell).toBeVisible();
  await bell.click();
  const panel = page.getByRole("dialog", { name: "Alerts" });
  await expect(panel).toBeVisible();
  await expect(panel.locator('[data-alert-id="pipeline-pause"]')).toHaveCount(
    0,
  );
  await page.keyboard.press("Escape");
  await expect(panel).toBeHidden();
  await expect(bell).toBeFocused();

  const paused = await api("POST", "/api/pipeline-pause", {
    paused: true,
    reason: "e2e alerts check",
  });
  expect(paused.ok).toBe(true);
  try {
    await expect(bell).toHaveAttribute("aria-label", /Alerts: [1-9]/, {
      timeout: 15_000,
    });
    await bell.click();
    const row = panel.locator('[data-alert-id="pipeline-pause"]');
    await expect(row).toBeVisible();
    await expect(row).toContainText(
      /Deployment paused|Paused — only tasks you Run now will start/,
    );
    await expect(row).toContainText("e2e alerts check");
    await expect(row).toContainText("Warning");
    const fix = row.getByRole("link", { name: /Fleet → Controls/ });
    await fix.click();
    await expect(page).toHaveURL(/\/next\/fleet\?tab=controls$/);
    await expect(panel).toBeHidden();
  } finally {
    await api("DELETE", "/api/pipeline-pause");
  }

  // Gone on a following poll, with no reload.
  await bell.click();
  await expect(panel.locator('[data-alert-id="pipeline-pause"]')).toHaveCount(
    0,
    { timeout: 15_000 },
  );
  await panel.getByTestId("alerts-see-all").click();
  await expect(page).toHaveURL(/\/next\/fleet$/);
});
