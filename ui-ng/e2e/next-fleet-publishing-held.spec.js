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

// /next/fleet says when a base branch has stopped publishing, and which
// release triage task holds it (docs/test-runs/2026-11-15-release-train-
// publishing-held.md, F2). /next/system redirects here, and until this
// the page never said that publishing from main was held -- which is also
// what holds every `skip-when: publishing-held` schedule, the release
// train among them.
//
// A demo publishes nothing and runs no release, so the finding is added to
// the demo's own GET /api/sandboxes answer: everything else on the page is
// the real store's. Set GRAIN_UI_SHOTS=<dir> to write the page at each
// size and theme.

import AxeBuilder from "@axe-core/playwright";
import fs from "node:fs";
import path from "node:path";
import { test, expect } from "./fixtures.js";

const SHOTS = process.env.GRAIN_UI_SHOTS || "";

const SIZES = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];
const THEMES = ["light", "dark"];

function findings(repo, triageTaskID) {
  return [
    {
      repo,
      branch: "main",
      failures: 3,
      failingSince: "2026-11-15T06:40:00Z",
      failingForMS: 2 * 60 * 60 * 1000,
      conclusion: "failure",
      commit: "4b1f0c2",
      runID: "gha:acme:grain:9001",
      runInFlight: false,
      triageTaskID,
      gated: true,
      detail:
        `Nothing has published from ${repo} main since 2026-11-15 06:40 UTC, 2h ago: ` +
        "3 releases have been started since and every one of them published nothing, " +
        "the last of them ending failure. Nothing is held up by that -- the merge queue " +
        "is gated on this branch's own CI run and never on its release " +
        "(docs/merge-queue-behind.md) -- so this repository is merging changes and " +
        "shipping none of them, and will go on doing so until somebody looks at the " +
        `publisher. Further releases on this branch are held pending resolution of triage task ${triageTaskID}.`,
    },
    {
      repo,
      branch: "release/v3.5.1",
      failures: 3,
      failingSince: "2026-11-14T09:00:00Z",
      failingForMS: 24 * 60 * 60 * 1000,
      conclusion: "failure",
      runID: "gha:acme:grain:8990",
      runInFlight: false,
      gated: false,
      detail:
        `Nothing has published from ${repo} release/v3.5.1 since 2026-11-14 09:00 UTC, 1d ago: ` +
        "3 releases have been started since and every one of them published nothing, " +
        "the last of them ending failure. Run gha:acme:grain:8990 is the latest attempt " +
        "to read, and the branch's next merge starts another -- no retry is owed to " +
        "the commit that failed.",
    },
  ];
}

for (const size of SIZES) {
  for (const theme of THEMES) {
    test(`Fleet warns that publishing is held, naming its triage task — ${size.name}, ${theme}`, async ({
      page,
    }) => {
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.addInitScript(
        (t) => localStorage.setItem("grain.themeMode", t),
        theme,
      );
      const tasks = await (await page.request.get("/api/tasks")).json();
      const list = Array.isArray(tasks) ? tasks : (tasks.tasks ?? []);
      const triage = String(list[0]?.id ?? "1");
      const repo = "acme/grain";

      await page.route(/\/api\/sandboxes(\?.*)?$/, async (route) => {
        const res = await route.fetch();
        const body = await res.json();
        body.baseBranchPublishing = findings(repo, triage);
        await route.fulfill({ response: res, json: body });
      });

      await page.goto("/next/fleet");
      const banners = page.getByTestId("fleet-publishing");
      await expect(banners).toHaveCount(2);

      const held = banners.nth(0);
      await expect(held).toHaveAttribute("role", "status");
      await expect(held).toContainText(`Publishing held · ${repo}@main`);
      await expect(held).toContainText(
        `held pending resolution of triage task ${triage}.`,
      );
      const link = held.getByRole("link", {
        name: `Open triage task #${triage}`,
      });
      await expect(link).toHaveAttribute("href", `/next/task/${triage}`);

      const notHeld = banners.nth(1);
      await expect(notHeld).toContainText(
        `Publishing nothing · ${repo}@release/v3.5.1`,
      );
      await expect(notHeld.getByRole("link")).toHaveCount(0);

      // Above the host strip, inside the page, nothing scrolling sideways.
      const strip = page.getByTestId("fleet-host-strip");
      await expect(strip).toBeVisible();
      const heldBox = await held.boundingBox();
      const notHeldBox = await notHeld.boundingBox();
      const stripBox = await strip.boundingBox();
      expect(heldBox.y + heldBox.height).toBeLessThanOrEqual(notHeldBox.y);
      expect(notHeldBox.y + notHeldBox.height).toBeLessThanOrEqual(stripBox.y);
      expect(heldBox.x).toBeGreaterThanOrEqual(stripBox.x - 1);
      expect(heldBox.x + heldBox.width).toBeLessThanOrEqual(
        stripBox.x + stripBox.width + 1,
      );
      // The warning shape sits on the first line of the text, not centred
      // on a paragraph that wraps to several on a phone.
      const icon = await held.locator("g-status-icon").boundingBox();
      const firstLine = await held.evaluate((el) => {
        const range = document.createRange();
        range.selectNodeContents(el.querySelector("span"));
        const r = range.getClientRects()[0];
        return { y: r.y, height: r.height };
      });
      expect(
        Math.abs(
          icon.y + icon.height / 2 - (firstLine.y + firstLine.height / 2),
        ),
      ).toBeLessThanOrEqual(3);

      const sideways = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(sideways).toBe(0);

      // The warning keeps its contrast in both themes, link included.
      const scan = await new AxeBuilder({ page })
        .include('[data-testid="fleet-publishing"]')
        .analyze();
      expect(
        scan.violations.filter((v) =>
          ["serious", "critical"].includes(v.impact ?? ""),
        ),
      ).toEqual([]);

      if (SHOTS) {
        fs.mkdirSync(SHOTS, { recursive: true });
        await page.waitForLoadState("networkidle").catch(() => {});
        await page.screenshot({
          path: path.join(SHOTS, `fleet-publishing-${size.name}-${theme}.png`),
        });
      }

      await link.click();
      await expect(page).toHaveURL(new RegExp(`/next/task/${triage}$`));
    });
  }
}
