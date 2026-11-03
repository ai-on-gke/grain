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

// Settings → General → Updates → Automatic upgrades: an "Upgrade
// automatically" select (Off, on each release, or once a day). Once a
// day adds a Channel select and a Time of day input, side by side on a
// wide screen and stacked on a phone; on each release adds an "Upgrade
// to" select. Inside the card either way, with the recommended mode's
// label shown whole even on a phone.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`the automatic upgrade settings fit a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/general#updates");

    const card = page.locator("#updates");
    const cardBox = await card.boundingBox();
    expect(cardBox).not.toBeNull();
    const insideCard = (box) => {
      expect(box.x).toBeGreaterThanOrEqual(cardBox.x - 1);
      expect(box.x + box.width).toBeLessThanOrEqual(
        cardBox.x + cardBox.width + 1,
      );
    };
    const noSidewaysScroll = async () => {
      const overflow = await page.evaluate(
        () =>
          document.documentElement.scrollWidth >
          document.documentElement.clientWidth + 1,
      );
      expect(overflow, "the page scrolls sideways").toBe(false);
    };

    const mode = page.getByRole("combobox", { name: "Upgrade automatically" });
    await expect(mode).toBeVisible();
    await expect(mode).toHaveText(/Off \(manual only\)/);
    await expect(page.getByText(/is recommended/)).toBeVisible();
    await expect(page.getByLabel("Time of day")).toHaveCount(0);

    try {
      // On each release: the recommended mode, its label whole.
      await mode.click();
      await page.getByRole("option", { name: /On each release/ }).click();
      await expect(mode).toHaveText("On each release (recommended)");
      const truncated = await mode.evaluate(
        (el) => el.scrollWidth > el.clientWidth + 1,
      );
      expect(truncated, "the mode's label is cut short").toBe(false);
      const target = page.getByRole("combobox", { name: "Upgrade to" });
      await expect(target).toHaveText("latest");
      await expect(
        page.getByText(/Release candidates are never followed/),
      ).toBeVisible();
      const modeBox = await mode.boundingBox();
      const targetBox = await target.boundingBox();
      insideCard(modeBox);
      insideCard(targetBox);
      expect(targetBox.y).toBeGreaterThan(modeBox.y + modeBox.height);
      await noSidewaysScroll();

      // Once a day: the channel and time of day.
      await mode.click();
      await page.getByRole("option", { name: "Once a day" }).click();
      const channel = page.getByRole("combobox", { name: "Channel" });
      const time = page.getByLabel("Time of day");
      await expect(channel).toHaveText("latest");
      await expect(time).toBeEnabled();
      await expect(page.getByText(/24-hour, /)).toBeVisible();
      const channelBox = await channel.boundingBox();
      const timeBox = await time.boundingBox();
      insideCard(channelBox);
      insideCard(timeBox);
      if (name === "desktop") {
        // One row: the time sits to the right of the channel, level with it.
        expect(timeBox.x).toBeGreaterThan(channelBox.x + channelBox.width);
        expect(Math.abs(timeBox.y - channelBox.y)).toBeLessThan(12);
      } else {
        expect(timeBox.y).toBeGreaterThan(channelBox.y + channelBox.height);
      }
      await noSidewaysScroll();
    } finally {
      // The demo server is shared by the worker's other tests.
      await mode.click();
      await page.getByRole("option", { name: /Off/ }).click();
      await expect(mode).toHaveText(/Off \(manual only\)/);
    }
  });
}
