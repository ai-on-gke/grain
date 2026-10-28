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

// The upgrade pane's account of the runs a restart carries across
// (task 1389): before an upgrade, which of the runs in flight it would
// lose, by framework; after one, what the last restart adopted and lost.
// GET /api/upgrade is stubbed, as in upgrade.spec.js: the demo has no
// upgrader and no restart behind it, and what is under test here is how
// the pane lays the report out.

const STATUS = {
  enabled: true,
  mode: "image",
  channel: "main",
  branch: "main",
  phase: "ok",
  detail: "",
  running: { image: "ghcr.io/bwsalmon/grain/grain:main" },
  inFlightRuns: 9,
  atRisk: {
    lost: 8,
    lostByFramework: [
      { framework: "claude", count: 7 },
      { framework: "setup", count: 1 },
    ],
    survive: 1,
  },
  lastRestart: {
    at: "2026-10-26T07:15:00Z",
    adopted: [
      {
        run: "1115-1",
        task: "1115",
        framework: "antigravity",
        reason: "its spool went 14s unread",
      },
    ],
    lost: [
      {
        run: "1120-1",
        task: "1120",
        framework: "claude",
        reason:
          "its CLI ran inside the daemon's own container and stopped with it " +
          "(not in a run host or grain hostd, so nothing was left to adopt)",
      },
      {
        run: "1121-1",
        task: "1121",
        setup: true,
        reason:
          "it was still setting up (no agent CLI started yet) when the daemon stopped",
      },
    ],
    lostByFramework: [
      { framework: "claude", count: 1 },
      { framework: "setup", count: 1 },
    ],
  },
};

for (const width of [1280, 390]) {
  test(`reports the last restart and the runs an upgrade would lose at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.route("**/api/upgrade", (route) =>
      route.fulfill({ json: STATUS }),
    );
    await page.goto("/system/upgrade");

    const last = page.locator(".upgrade-last-restart");
    await expect(last.locator(".upgrade-last-restart-summary")).toHaveText(
      "1 adopted, 2 lost (claude 1, setup 1).",
    );
    const risk = page.locator(".upgrade-in-flight");
    await expect(risk).toContainText("8 would be lost (claude 7, setup 1)");
    await expect(risk).toContainText(
      "1 is in a run host or grain hostd and would be adopted",
    );

    await last.locator("summary").click();
    const lostRow = last.locator("li", { hasText: "1120-1" });
    await expect(lostRow).toContainText("lost: run 1120-1 on claude");
    await expect(lostRow.getByRole("link", { name: "1120-1" })).toHaveAttribute(
      "href",
      "/tasks/1120",
    );

    // Both alerts sit inside the pane, stacked, the report above the
    // warning, and neither runs off the side at phone width.
    const pane = await page.locator(".upgrade-running").boundingBox();
    const lastBox = await last.boundingBox();
    const riskBox = await risk.boundingBox();
    expect(lastBox.y).toBeGreaterThanOrEqual(pane.y + pane.height - 1);
    expect(riskBox.y).toBeGreaterThanOrEqual(lastBox.y + lastBox.height - 1);
    for (const box of [lastBox, riskBox]) {
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
    }
    const overflow = await last.evaluate(
      (el) => el.scrollWidth - el.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);

    if (process.env.UPGRADE_SHOTS) {
      for (const scheme of ["light", "dark"]) {
        await page.emulateMedia({ colorScheme: scheme });
        await page.screenshot({
          path: `${process.env.UPGRADE_SHOTS}/upgrade-${width}-${scheme}.png`,
          fullPage: false,
        });
        await page.screenshot({
          path: `${process.env.UPGRADE_SHOTS}/crop-${width}-${scheme}.png`,
          clip: {
            x: lastBox.x,
            y: lastBox.y,
            width: lastBox.width,
            height: riskBox.y + riskBox.height - lastBox.y,
          },
        });
      }
    }
  });
}
