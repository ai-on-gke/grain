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

// Settings → Access's access-code card: the mode the demo daemon really
// reports, and -- with GET /api/access/access-code answered as it is for
// a verified viewer, which the demo's access-code browser is not -- the
// recommendation to switch to recovery, laid out inside the card with the
// command and its copy button side by side.

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

const within = (inner, outer) => {
  expect(inner.x).toBeGreaterThanOrEqual(outer.x - 1);
  expect(inner.x + inner.width).toBeLessThanOrEqual(outer.x + outer.width + 1);
  expect(inner.y).toBeGreaterThanOrEqual(outer.y - 1);
  expect(inner.y + inner.height).toBeLessThanOrEqual(
    outer.y + outer.height + 1,
  );
};

test("the access-code card shows the demo daemon's own mode", async ({
  page,
}) => {
  await page.goto("/settings/access");
  await expect(page.getByTestId("access-code-mode")).toHaveText("On");
  // Signed in with the code itself, so no recommendation yet -- but the
  // command that changes the mode is always there.
  await expect(page.getByTestId("access-code-recommend-recovery")).toHaveCount(
    0,
  );
  await expect(page.getByTestId("access-code-mode-command")).toContainText(
    /^sudo -u \S+ grain access-code .*mode recovery$/,
  );
});

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`the access-code card recommends recovery and fits a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const command =
      "sudo -u grain grain access-code -data-dir /var/lib/grain/data mode recovery";
    await page.route("**/api/access/access-code", (route) =>
      route.fulfill({
        json: {
          mode: "on",
          viewerVerified: true,
          verifiedAdminCanSignIn: true,
          recoveryInForce: false,
          account: "grain",
          command,
          onCommand: command.replace(/recovery$/, "on"),
        },
      }),
    );
    await page.goto("/settings/access");

    const card = page.locator("#access-code-mode");
    await expect(card).toBeVisible();
    const recommend = page.getByTestId("access-code-recommend-recovery");
    await expect(recommend).toContainText("recovery");
    const cmd = page.getByTestId("access-code-mode-command");
    await expect(cmd).toHaveText(command);

    const cardBox = await card.boundingBox();
    const cmdBox = await cmd.boundingBox();
    const copyBox = await page
      .locator("#access-code-mode")
      .getByRole("button", { name: "Copy command" })
      .boundingBox();
    within(await recommend.boundingBox(), cardBox);
    within(cmdBox, cardBox);
    within(copyBox, cardBox);
    // The copy button sits to the right of the command, level with its
    // first line, at both widths.
    expect(copyBox.x).toBeGreaterThanOrEqual(cmdBox.x + cmdBox.width - 1);
    expect(Math.abs(copyBox.y - cmdBox.y)).toBeLessThanOrEqual(8);
    // The mode chip leads the card, above the recommendation.
    const chipBox = await page.getByTestId("access-code-mode").boundingBox();
    within(chipBox, cardBox);
    expect(chipBox.y).toBeLessThan((await recommend.boundingBox()).y);

    // Prominent: between how this browser is signed in and the allowlist.
    const browserBox = await page.locator("#this-browser").boundingBox();
    const allowBox = await page.locator("#allowed-principals").boundingBox();
    expect(cardBox.y).toBeGreaterThan(browserBox.y);
    expect(cardBox.y).toBeLessThan(allowBox.y);

    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);
  });
}
