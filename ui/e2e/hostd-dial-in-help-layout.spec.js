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

// Settings → Models on a daemon that takes a grain hostd dialing in with
// none connected (grain demo -host-executor=hostd-remote-down): what to
// run, for an installed grain-hostd.service and for a hand-run hostd.
// The demo's executor is answered here, so the spec runs on any demo.

async function stub(page) {
  await page.route("**/api/settings/cli-run", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const data = await (await route.fetch()).json();
    data.hostExecutor = {
      kind: "hostd",
      hostAvailable: true,
      remote: true,
      reason: "host CLIs run through a grain hostd that dials in",
      hostd: {
        reachable: false,
        error:
          "grain hostd is not reachable: no grain hostd is connected to this daemon",
        remote: { connected: false, sessions: 0 },
      },
    };
    await route.fulfill({ json: data });
  });
}

for (const [width, theme] of [
  [1280, "light"],
  [390, "light"],
  [1280, "dark"],
  [390, "dark"],
]) {
  test(`the hostd dial-in commands fit Settings → Models at ${width}px (${theme})`, async ({
    page,
  }) => {
    await page.emulateMedia({ colorScheme: theme });
    await page.setViewportSize({ width, height: 900 });
    await stub(page);
    await page.goto("/settings/models");
    const alert = page.getByTestId("cli-run-hostd-status");
    await expect(alert).toContainText("none is connected", { timeout: 30000 });
    const a = await alert.boundingBox();
    // Each command wraps inside the alert rather than running off it.
    for (const id of ["cli-run-hostd-dropin", "cli-run-hostd-command"]) {
      const block = alert.getByTestId(id);
      await expect(block).toBeVisible();
      const b = await block.boundingBox();
      expect(b.x).toBeGreaterThanOrEqual(a.x - 1);
      expect(b.x + b.width).toBeLessThanOrEqual(a.x + a.width + 1);
      expect(
        await block.evaluate((el) => el.scrollWidth <= el.clientWidth + 1),
      ).toBe(true);
    }
    // The hand-run command is the last thing in the alert, after the
    // drop-in.
    const d = await alert.getByTestId("cli-run-hostd-dropin").boundingBox();
    const c = await alert.getByTestId("cli-run-hostd-command").boundingBox();
    expect(c.y).toBeGreaterThan(d.y + d.height);
    const scrollW = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(scrollW).toBeLessThanOrEqual(width + 1);
    await alert.screenshot({
      path: `screenshots/after/hostd-dial-in-${width}-${theme}.png`,
    });
  });
}
