import { expect, test } from "./fixtures.js";

// Settings → General → Updates → Automatic upgrades: a Channel select
// and a Time of day input. Side by side on a wide screen, stacked on a
// phone, inside the card either way, and with the time disabled until a
// channel is chosen.
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

    const channel = page.getByRole("combobox", { name: "Channel" });
    const time = page.getByLabel("Time of day");
    await expect(channel).toBeVisible();
    await expect(channel).toHaveText(/Off \(manual only\)/);
    await expect(time).toBeVisible();
    await expect(time).toBeDisabled();
    await expect(page.getByText(/24-hour, /)).toBeVisible();

    const card = page.locator("#updates");
    const cardBox = await card.boundingBox();
    expect(cardBox).not.toBeNull();
    const channelBox = await channel.boundingBox();
    const timeBox = await time.boundingBox();
    for (const box of [channelBox, timeBox]) {
      expect(box.x).toBeGreaterThanOrEqual(cardBox.x - 1);
      expect(box.x + box.width).toBeLessThanOrEqual(
        cardBox.x + cardBox.width + 1,
      );
    }
    if (name === "desktop") {
      // One row: the time sits to the right of the channel, level with it.
      expect(timeBox.x).toBeGreaterThan(channelBox.x + channelBox.width);
      expect(Math.abs(timeBox.y - channelBox.y)).toBeLessThan(12);
    } else {
      expect(timeBox.y).toBeGreaterThan(channelBox.y + channelBox.height);
    }
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);
  });
}
