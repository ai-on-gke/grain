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

// The upgrade pane's warning that the release this deployment runs was
// retracted (task 2656; <running>-retracted in the registry, from
// release-from-trunk.sh rollback). GET /api/upgrade is stubbed, as in
// upgrade-restart-report.spec.js: the demo has no upgrader and no
// registry behind it, and what is under test here is how the pane lays
// the warning and its two ways off the release out.

const STATUS = {
  enabled: true,
  mode: "image",
  channel: "latest",
  phase: "",
  running: { image: "ghcr.io/example/grain/grain:v3.5.0" },
  retractedRelease: { running: "v3.5.0", latest: "v3.4.0", newer: "v3.6.0" },
};

for (const width of [1280, 390]) {
  test(`warns that the running release was retracted at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.route("**/api/upgrade", (route) =>
      route.fulfill({ json: STATUS }),
    );
    await page.goto("/settings/upgrade");

    const alert = page.locator(".upgrade-retracted-release");
    await expect(alert).toContainText("Release v3.5.0 was retracted.");
    await expect(alert).toContainText(
      "only safe if the database schema has not changed",
    );
    const forward = alert.getByRole("button", { name: "Upgrade to v3.6.0" });
    const back = alert.getByRole("button", {
      name: "Move back to latest (v3.4.0)",
    });

    // The alert stays inside the viewport, its text does not run off the
    // side, and both buttons sit inside it, the fix-forward first.
    const box = await alert.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    const overflow = await alert.evaluate(
      (el) => el.scrollWidth - el.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
    const fwd = await forward.boundingBox();
    const bck = await back.boundingBox();
    for (const b of [fwd, bck]) {
      expect(b.x).toBeGreaterThanOrEqual(box.x);
      expect(b.x + b.width).toBeLessThanOrEqual(box.x + box.width);
      expect(b.y + b.height).toBeLessThanOrEqual(box.y + box.height);
    }
    expect(fwd.y <= bck.y && (fwd.y < bck.y || fwd.x < bck.x)).toBe(true);

    await forward.click();
    await expect(page.getByLabel(/Branch/)).toHaveValue("v3.6.0");
  });
}
