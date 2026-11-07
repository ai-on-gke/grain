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

// Settings → Upgrade → Database Backend is read-only: which store the
// deployment runs on, and the offline commands that move it. There is no
// form or button that moves the store from a running daemon
// (pkg/ui/database.go). The command block stays inside the section on a
// phone -- it scrolls inside itself rather than pushing the page
// sideways.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`the database backend section is read-only and fits a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/admin/upgrade");

    const section = page.locator(".database-backend-section");
    await expect(section).toBeVisible();
    await expect(section.locator(".database-backend-current")).toHaveText(
      /This deployment runs on SQLite/,
    );
    const commands = section.locator(".database-backend-commands");
    await expect(commands).toHaveText(
      /^sudo systemctl stop grain-daemon\.service\s+sudo \.\/scripts\/setup\.sh --db-driver postgres\s+sudo grain db check/,
    );
    await expect(section.getByRole("button")).toHaveCount(0);
    await expect(section.getByRole("textbox")).toHaveCount(0);

    await commands.scrollIntoViewIfNeeded();
    const sectionBox = await section.boundingBox();
    for (const part of [
      section.locator(".database-backend-offline"),
      commands,
    ]) {
      const box = await part.boundingBox();
      expect(box).not.toBeNull();
      expect(box.x).toBeGreaterThanOrEqual(sectionBox.x - 1);
      expect(box.x + box.width).toBeLessThanOrEqual(
        sectionBox.x + sectionBox.width + 1,
      );
    }
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);
  });
}
