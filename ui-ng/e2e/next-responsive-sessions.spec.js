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

// Regressions from the 2026-11-14 responsive navigation and live-session
// walkthrough (docs/test-runs/2026-11-14-responsive-navigation-live-sessions.md),
// over a real `grain demo`:
//
//  1. Agents auto-selected its first agent at every width above a phone,
//     but below 1080px the inspector is a fixed sheet, so a tablet opened
//     Agents with a sheet over the list and the top bar's right half.
//  2. Sessions still offered and printed a team ("Team: default").
//  3. A failed sessions read showed the error and "No sessions yet."
//     together.

import { test, expect } from "./fixtures.js";

// hitsItself says whether the element's centre point lands on the element
// (or something inside it) rather than on whatever is drawn over it.
async function hitsItself(locator) {
  return locator.evaluate((el) => {
    const r = el.getBoundingClientRect();
    const hit = document.elementFromPoint(
      r.left + r.width / 2,
      r.top + r.height / 2,
    );
    return !!hit && el.contains(hit);
  });
}

for (const [width, height] of [
  [820, 1180],
  [1024, 768],
]) {
  test(`Agents opens without a sheet over the page at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height });
    await page.goto("/next/inbox");
    await page.getByTestId("rail-agents").click();
    await expect(page).toHaveURL(/\/next\/agents$/);
    await expect(page.locator(".agent-card").first()).toBeVisible();
    // Nothing is selected, so no sheet is open and the top bar is
    // clickable end to end.
    await expect(page.locator(".inspector-pane.is-sheet-open")).toHaveCount(0);
    for (const id of ["director-trigger", "help-trigger", "avatar-btn"]) {
      expect(
        await hitsItself(page.getByTestId(id)),
        `${id} is not covered`,
      ).toBe(true);
    }
    // Choosing an agent still opens the sheet, which closes again.
    await page.locator(".agent-card").first().click();
    await expect(page.locator(".inspector-pane.is-sheet-open")).toBeVisible();
    await expect(page).toHaveURL(/[?&]task=/);
  });
}

test("Agents still selects its first agent beside the list on a desktop", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/next/agents");
  await expect(page.getByTestId("agents-pane")).toBeVisible();
  await expect(page.locator(".agent-card.is-selected")).toHaveCount(1);
});

test(
  "Sessions shows and asks for no team",
  { tag: ["@phone"] },
  async ({ page }) => {
    await page.goto("/next/sessions");
    const region = page.getByRole("region", { name: "Sessions" });
    await expect(page.getByTestId("session-director")).toBeVisible();
    await expect(region).not.toContainText(/\bteam\b/i);

    await region.getByRole("button", { name: "New session" }).click();
    const dialog = page.getByTestId("new-session-dialog");
    await expect(dialog).toBeVisible();
    await expect(page.getByTestId("new-session-name")).toBeFocused();
    await expect(dialog).not.toContainText(/\bteam\b/i);
    await expect(page.getByTestId("new-session-team")).toHaveCount(0);
    await page.getByTestId("new-session-name").fill("No team here");
    await page.getByTestId("new-session-create").click();
    await expect(page).toHaveURL(/\/next\/sessions\/[^/?]+$/);
    await expect(page.locator("#chat-title")).toHaveText("No team here");
    await expect(page.getByTestId("session-team-pill")).toHaveCount(0);

    await page.getByTestId("session-settings-toggle").click();
    const panel = page.getByTestId("session-settings-panel");
    await expect(panel).toBeVisible();
    await expect(panel).not.toContainText(/\bteam\b/i);
    await expect(page.getByTestId("session-team-input")).toHaveCount(0);
  },
);

test(
  "a failed sessions read says so instead of showing an empty list",
  { tag: ["@phone"] },
  async ({ page }) => {
    test.info().annotations.push({ type: "allows-page-error" });
    // Stub: only the sessions list read fails; the server, the rest of the
    // API and the page are real.
    await page.route("**/api/console-sessions", (route) =>
      route.request().method() === "GET"
        ? route.fulfill({
            status: 500,
            contentType: "application/json",
            body: JSON.stringify({ error: "synthetic sessions read failure" }),
          })
        : route.continue(),
    );
    await page.goto("/next/sessions");
    await expect(page.getByTestId("load-error")).toContainText(
      "synthetic sessions read failure",
    );
    await expect(page.getByText("No sessions yet.")).toHaveCount(0);

    // The page retries on its own: once the read answers, the banner goes
    // and the Director is listed, with no reload.
    await page.unroute("**/api/console-sessions");
    await expect(page.getByTestId("load-error")).toHaveCount(0, {
      timeout: 15_000,
    });
    await expect(page.getByTestId("session-director")).toBeVisible();
  },
);

// Customize navigation in one tab applies to the browser's other open
// tabs at once (a storage event), not only after they reload.
test("Customize navigation updates another open tab", async ({ page }) => {
  const other = await page.context().newPage();
  await page.goto("/next/inbox");
  await other.goto("/next/inbox");
  await expect(other.getByTestId("rail-ci")).toBeVisible();

  await page.getByTestId("rail-more").click();
  await page.getByTestId("rail-customize-nav").click();
  const dialog = page.getByRole("dialog", { name: "Customize navigation" });
  await expect(dialog).toBeVisible();
  const ci = dialog.getByRole("switch", { name: "Show CI on the rail" });
  await ci.click();
  await expect(ci).toHaveAttribute("aria-checked", "false");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("rail-ci")).toHaveCount(0);

  await expect(other.getByTestId("rail-ci")).toHaveCount(0);
  await expect(other.getByTestId("rail-hidden-count")).toHaveText("1");
  await other.close();
});
