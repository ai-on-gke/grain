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

import { test, expect } from "./fixtures";

// The notice an unmigrated host-mode install gets (pkg/daemon's
// installnotice.go, GET /api/config's installNotice), on /next/ as on the
// classic UI (ui/e2e/install-notice.spec.js): a standing banner below the
// top bar on every page, the same notice in full on Fleet and on Settings
// -> Upgrade, where Start upgrade is off because the server refuses an
// in-place upgrade of such an install (docs/test-runs/2026-11-15-note-
// events-and-host-notice.md, F4). `grain demo -host-mode-install` reports
// the notice without being one; nothing of grain's own API is mocked.

// -upgrade-image is what a host-mode install has (docs/test-runs/2026-11-15-
// note-events-and-host-notice/host_mode.sh), and without it the Upgrade
// pane offers no Start upgrade at all; nothing here pulls from it.
test.use({
  demoArgs: ["-host-mode-install", "-upgrade-image", "localhost:5000/x/grain"],
});

const COMMAND = "sudo ./scripts/setup.sh";

test.describe("a host-mode install on /next/", () => {
  test("shows the banner on every page, below the top bar @phone @dark", async ({
    page,
    axe,
  }) => {
    await page.goto("inbox");
    const banner = page.getByTestId("install-notice-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toHaveAttribute("role", "status");
    await expect(banner).toContainText("Unmigrated host-mode install");
    await expect(banner).toContainText("This install is still host mode");
    await expect(banner.locator("code")).toHaveText(COMMAND);
    await expect(page.getByTestId("install-notice-banner-clis")).toHaveText(
      "claude",
    );
    await expect(
      banner.getByRole("link", { name: "Migration docs" }),
    ).toHaveAttribute("href", /cli-run-location\.design\.md/);

    // At the top of the stage, below the top bar: across it to the
    // viewport's right edge, above the page, and nothing of it pushed
    // past that edge.
    const main = page.getByRole("main");
    await expect(main.getByTestId("install-notice-banner")).toBeVisible();
    const viewport = page.viewportSize()!;
    const box = (await banner.boundingBox())!;
    const stage = (await main.boundingBox())!;
    expect(Math.round(box.y)).toBe(Math.round(stage.y));
    expect(Math.round(box.x + box.width)).toBe(viewport.width);
    const heading = (await page
      .getByRole("heading", { level: 1 })
      .first()
      .boundingBox())!;
    expect(heading.y).toBeGreaterThanOrEqual(box.y + box.height - 1);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);

    // It stays as the person moves on.
    await page.goto("tasks");
    await expect(page.getByTestId("install-notice-banner")).toBeVisible();

    expect(await axe()).toEqual([]);
  });

  test("Fleet says it in full @phone @dark", async ({ page, axe }) => {
    // /next/system is classic's System -> Health, and redirects here.
    await page.goto("system");
    await expect(page).toHaveURL(/\/next\/fleet/);
    const notice = page.getByTestId("fleet-install-notice");
    await expect(notice).toBeVisible();
    await expect(
      notice.getByRole("heading", { name: "Unmigrated host-mode install" }),
    ).toBeVisible();
    await expect(notice.getByTestId("install-notice-command")).toHaveText(
      COMMAND,
    );
    await expect(notice.getByTestId("install-notice-clis")).toContainText(
      "claude: no host binary, and no container location on a bare-machine daemon",
    );
    await expect(
      notice.getByRole("link", { name: "Migration docs" }),
    ).toHaveAttribute("href", /cli-run-location\.design\.md/);

    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
    expect(await axe()).toEqual([]);
  });

  test("Settings → Upgrade says it and will not start an upgrade @phone @dark", async ({
    page,
    axe,
  }) => {
    await page.goto("settings/upgrade");
    const notice = page.getByTestId("upgrade-install-notice");
    await expect(notice).toBeVisible();
    await expect(notice.getByTestId("install-notice-command")).toHaveText(
      COMMAND,
    );
    await expect(notice).toContainText("This install is still host mode");

    const start = page.getByRole("button", { name: "Start upgrade" });
    await expect(start).toBeVisible();
    await expect(start).toBeDisabled();
    const reason = page.getByTestId("upgrade-blocked-reason");
    await expect(reason).toContainText(`run ${COMMAND} instead`);
    // The reason is the button's description, for a screen reader too.
    await expect(start).toHaveAccessibleDescription(
      /run sudo \.\/scripts\/setup\.sh instead/,
    );

    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
    expect(await axe()).toEqual([]);
  });
});
