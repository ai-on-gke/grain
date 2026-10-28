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
import { alertsBell, alertRow, openAlerts } from "./alerts.js";

// A fresh install, paused: no agent model and no git credential
// (-unconfigured leaves the default team with setup steps left) and an agent usage-limit
// pause the demo really began (-agent-paused, pkg/demo/demo_scenarios.go).
// The flags are worker-scoped, so this test has a file and a demo of its
// own. It used to sit in responsive-navigation-walkthrough.spec.js,
// overlaying all four banner flags onto GET /api/config.
test.use({ demoArgs: ["-unconfigured", "-agent-paused"] });

// Three viewports, each a cold page load plus the alerts panel, on a
// worker that starts alongside the suite's heaviest specs: on WebKit in
// CI a pass took 25.6s and a run that was merely slower timed out at
// Playwright's default 30s, failing in the `finally` cleanup rather than
// in anything this test checks (2026-10-23, candidate 10befff/66b743a).
test.setTimeout(90_000);

const TABLET = { width: 820, height: 1180 }; // iPad Air portrait
const PHONE_PORTRAIT = { width: 390, height: 844 }; // iPhone 14
const PHONE_LANDSCAPE = { width: 844, height: 390 }; // iPhone 14 rotated

async function assertNoHorizontalOverflow(page) {
  const overflows = await page.evaluate(() => {
    const docWidth = document.documentElement.clientWidth;
    const scrollWidth = document.documentElement.scrollWidth;
    return {
      docWidth,
      scrollWidth,
      hasOverflow: scrollWidth > docWidth + 1, // allow 1px rounding tolerance
    };
  });
  expect(
    overflows.hasOverflow,
    `Document scrollWidth (${overflows.scrollWidth}px) exceeds clientWidth (${overflows.docWidth}px)`,
  ).toBe(false);
}

// grain/task-1360: the banner strip took whatever its banners needed, so
// three or four at once left a phone held sideways about 9px for the
// page. They are rows of the alerts panel now (grain/task-4008), so the
// page keeps the whole window whatever is wrong, and the warnings are
// one tap away on the bar that stays on screen -- in PhoneNav's bar on a
// phone, not behind the drawer.
test("Standing alerts leave a phone's page the window and stay reachable", async ({
  page,
}) => {
  // Three viewports, each a full page load and a trip through the panel:
  // well inside 30s on an idle host, and not on a contended webkit one,
  // where the default timeout ran out mid-loop and took the finally's
  // pause-lifting delete with it.
  test.setTimeout(90_000);
  // The setup alert speaks for the deployment (config.deployment). On
  // this demo it has no model connection and no GitHub credential:
  // -unconfigured leaves it none. /api/config carries no teams list.
  const config = await (await page.request.get("/api/config")).json();
  expect(config.modelsConfigured).toBe(false);
  expect(config.agentBackends || []).toEqual([]);
  expect(config.teams).toBeUndefined();
  expect(config.tenancy).toBeUndefined();
  expect(config.deployment?.id).toBe("default");
  expect(config.deployment?.gitCredential || "").toBe("");
  expect(config.deployment?.agentBackendId || "").toBe("");
  expect(config.agentPause?.paused).toBe(true);

  // The pipeline pause is real too, with a reason long enough to wrap,
  // and is lifted at the end.
  const reason =
    "rotating the signing key " + "a-long-unbroken-identifier-".repeat(4);
  const paused = await page.request.post("/api/pipeline-pause", {
    data: { reason },
  });
  expect(paused.ok()).toBe(true);

  try {
    for (const viewport of [PHONE_PORTRAIT, PHONE_LANDSCAPE, TABLET]) {
      await page.setViewportSize(viewport);
      await page.goto("/");
      // No banner over the page: the panes take the window below the
      // bar, and the list in them scrolls to its first row.
      await expect(page.getByTestId("banner-strip")).toHaveCount(0);
      const bell = alertsBell(page);
      await expect(bell).toBeVisible();
      await expect(bell).toHaveAttribute("data-severity", "warning");
      const body = await page.locator(".app-body").boundingBox();
      expect(body.height).toBeGreaterThanOrEqual(viewport.height * 0.8);
      const firstRow = page.locator(".task-row").first();
      await firstRow.scrollIntoViewIfNeeded();
      await expect(firstRow).toBeInViewport();
      await assertNoHorizontalOverflow(page);

      // Every alert is in the panel -- the usage-limit pause, the
      // deployment's pause and its setup steps -- and the last
      // one's own action is reachable inside it, the long reason wrapped
      // rather than pushing the panel off the side of the screen.
      const panel = await openAlerts(page);
      await expect(alertRow(page, /usage limit reached/i)).toHaveCount(
        config.agentPause.pauses?.length || 1,
      );
      await expect(alertRow(page, reason.slice(0, 40))).toBeVisible();
      await expect(alertRow(page, /setup steps? left/)).toBeVisible();
      const box = await panel.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 1);
      // And off the edge rather than against it: the panel keeps the
      // same gutter on the right as the window's other side, where a
      // phone's narrow width used to push it flush against the right
      // edge with its border cut off (2026-10-23 walkthrough).
      expect(viewport.width - (box.x + box.width)).toBeGreaterThanOrEqual(15);
      const last = panel.getByTestId("alert-row").last();
      await last.getByRole("button").first().scrollIntoViewIfNeeded();
      await expect(last.getByRole("button").first()).toBeInViewport();
      await page.keyboard.press("Escape");
      await expect(panel).toHaveCount(0);
    }
  } finally {
    await page.request.delete("/api/pipeline-pause");
  }
});
