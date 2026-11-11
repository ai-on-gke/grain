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

// grain/task-2641: every session reads every repository the deployment
// works in. The session pane says so under its Grants list, where its
// own reads are, and lists those repositories on a line of their own
// beneath it. Both have to sit inside the pane at desktop and
// phone widths rather than running off its edge.
for (const { name, width, height } of [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`session pane states its default reads within the pane (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height });
    // The demo's director has no repository it reads by default, so the
    // listing is given some: long names, enough to wrap on a phone.
    await page.route("**/api/console-sessions", async (route) => {
      if (route.request().method() !== "GET") {
        return route.fallback();
      }
      const response = await route.fetch();
      const body = await response.json();
      for (const s of body.sessions || []) {
        if (s.id === "director") {
          s.readsAll = true;
          s.autoReads = [
            "acme/widgets",
            "acme/a-repository-with-a-rather-long-name",
            "git.example.com/platform/infrastructure-configuration",
          ];
        }
      }
      await route.fulfill({ response, json: body });
    });
    await page.goto("/session/director");
    await page
      .getByRole("button", { name: "Edit Director capabilities" })
      .click();

    // The repositories it reads are grants in its Grants list
    // (unified-grants slice 5); the caption saying it reads the rest
    // sits under that list, aligned with it.
    const grants = page.getByTestId("session-director-grants");
    await grants.scrollIntoViewIfNeeded();
    const caption = page.getByText(
      /every session reads every repository this deployment works in/,
    );
    await expect(caption).toBeVisible();

    const grantsBox = await grants.boundingBox();
    const captionBox = await caption.boundingBox();
    expect(captionBox.x).toBeGreaterThanOrEqual(0);
    expect(captionBox.x + captionBox.width).toBeLessThanOrEqual(width);
    expect(captionBox.y).toBeGreaterThanOrEqual(
      grantsBox.y + grantsBox.height - 1,
    );
    expect(Math.abs(captionBox.x - grantsBox.x)).toBeLessThanOrEqual(1);

    const autoReads = page.getByTestId("session-auto-reads");
    await expect(autoReads).toContainText(
      "Also reads, by default: acme/widgets, acme/a-repository-with-a-rather-long-name",
    );
    const autoBox = await autoReads.boundingBox();
    expect(autoBox.x).toBeGreaterThanOrEqual(0);
    expect(autoBox.x + autoBox.width).toBeLessThanOrEqual(width);
    expect(autoBox.y).toBeGreaterThan(captionBox.y);
    expect(Math.abs(autoBox.x - grantsBox.x)).toBeLessThanOrEqual(1);
    // Not drawn as one of the session's own, removable reads.
    await expect(
      page.getByRole("button", { name: /acme\/widgets/ }),
    ).toHaveCount(0);
    await page.screenshot({
      path: test.info().outputPath(`session-default-reads-${name}.png`),
      fullPage: false,
    });
  });
}
