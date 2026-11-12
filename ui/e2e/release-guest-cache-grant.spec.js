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

// The Releases tab's warning that grain cannot fill the sandbox guest
// cache names the grant to make by its canonical reference
// (https://grain.dev/capabilities/release#package-publish, task 4482),
// which is long and unbroken: it has to wrap inside the warning rather
// than run off a phone's screen. The demo seeds no repository that
// publishes the guest, so the release settings are answered with the
// server's own and that state laid over them.
const repo = "acme/widgets";
const grant = "https://grain.dev/capabilities/release#package-publish";

for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the guest cache warning wraps the publish grant it names (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.route(`**/api/repos/${repo}/release`, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const res = await route.fetch();
      const body = await res.json();
      await route.fulfill({
        response: res,
        json: {
          ...body,
          enabled: false,
          publishesSandboxGuest: true,
          sandboxGuestCacheJob: ".grain/workflows/image.yml:guest-cache",
          sandboxGuestCacheAutomatic: false,
          postMergeDispatchJobs: [],
        },
      });
    });
    await page.goto(`/repos/${repo}/settings/releases`);

    const alert = page.locator(".MuiAlert-root", {
      hasText: /builds one inline/,
    });
    const code = alert.locator("code", { hasText: grant });
    await expect(code).toBeVisible();
    await code.scrollIntoViewIfNeeded();
    const a = await alert.boundingBox();
    const c = await code.boundingBox();
    expect(c.x).toBeGreaterThanOrEqual(a.x);
    expect(c.x + c.width).toBeLessThanOrEqual(a.x + a.width + 0.5);
    expect(a.x + a.width).toBeLessThanOrEqual(viewport.width + 0.5);
  });
}
