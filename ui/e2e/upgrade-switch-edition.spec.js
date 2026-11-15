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

// grain/task-4153: another edition's image is never one of the upgrade
// pane's ordinary repository chips. It is offered only behind "Switch
// edition…", which says what a switch loses, and an upgrade onto it is
// confirmed again before anything is posted.
//
// `grain demo -upgrade-image` wires a real image-mode upgrader. Core
// registers no edition but its own, so GET /api/upgrade is answered by
// the daemon and then given one made-up edition's repository to offer
// (the fields pkg/ui's upgradeStatusResponse carries on a deployment
// whose build knows another edition). Nothing is ever posted: every
// confirmation here is cancelled.

const DOCKERHUB = "docker.io/grainai/grain";
const OTHER = "registry.example/acme/acme-grain";
const LOSSES = [
  "the oss install track's defaults: this host is still on the oss track, so the next ordinary upgrade pulls from docker.io/grainai/grain again unless you reinstall it as acme",
  "every run in flight, lost to the restart (and to the one that switches back)",
];

test.use({ demoArgs: [`-upgrade-image=${DOCKERHUB}`] });

async function offerAnotherEdition(page) {
  await page.route(
    (url) => url.pathname === "/api/upgrade",
    async (route) => {
      if (route.request().method() !== "GET") {
        throw new Error("this spec never posts an upgrade");
      }
      const response = await route.fetch();
      const body = await response.json();
      await route.fulfill({
        response,
        json: {
          ...body,
          edition: "oss",
          otherEditionImageRepos: [{ repository: OTHER, edition: "acme" }],
          editionSwitchLosses: LOSSES,
        },
      });
    },
  );
}

test.describe("switching edition on the upgrade pane", () => {
  for (const [name, viewport] of [
    ["desktop", { width: 1440, height: 900 }],
    ["phone", { width: 390, height: 844 }],
  ]) {
    for (const theme of ["light", "dark"]) {
      test(`stays behind its own button, inside the page, at ${name} width in ${theme}`, async ({
        page,
      }, testInfo) => {
        await page.setViewportSize(viewport);
        await page.emulateMedia({ colorScheme: theme });
        await page.addInitScript(
          (t) => window.localStorage.setItem("grain.themeMode", t),
          theme,
        );
        await offerAnotherEdition(page);
        await page.goto("/settings/upgrade");
        await page.getByLabel("Branch").fill("main");

        // Not among the ordinary chips: only the button names a switch.
        await expect(page.getByText(OTHER)).toHaveCount(0);
        const open = page.getByRole("button", { name: "Switch edition…" });
        await expect(open).toBeVisible();
        await open.click();

        const panel = page.locator(".upgrade-switch-edition");
        await expect(panel).toContainText("runs the oss edition");
        await expect(panel.locator(".upgrade-switch-losses li")).toHaveCount(2);
        const chip = panel.getByText(`${OTHER} (acme edition)`);
        await chip.click();
        await expect(page.locator(".upgrade-will-switch")).toContainText(
          "switches this deployment from the oss edition to acme",
        );
        await expect(page.locator(".upgrade-will-ref")).toHaveText(
          `${OTHER}:main`,
        );

        // The panel sits in the form's column, and nothing in it runs off
        // the page.
        const image = page.getByLabel("Image (optional)");
        const outline =
          "xpath=ancestor::div[contains(@class,'MuiInputBase-root')][1]";
        const imageBox = await image.locator(outline).boundingBox();
        const panelBox = await panel.boundingBox();
        expect(Math.abs(panelBox.x - imageBox.x)).toBeLessThanOrEqual(1);
        expect(Math.abs(panelBox.width - imageBox.width)).toBeLessThanOrEqual(
          1,
        );
        const chipBox = await chip.boundingBox();
        expect(chipBox.x).toBeGreaterThanOrEqual(panelBox.x);
        expect(chipBox.x + chipBox.width).toBeLessThanOrEqual(
          panelBox.x + panelBox.width + 1,
        );
        const overflow = await page.evaluate(
          () => document.documentElement.scrollWidth - window.innerWidth,
        );
        expect(overflow).toBeLessThanOrEqual(0);
        await panel.scrollIntoViewIfNeeded();
        await page.screenshot({
          path: testInfo.outputPath(`switch-edition-${name}-${theme}.png`),
        });

        // The button says what it does now, and asks first.
        await page
          .getByRole("button", { name: "Switch edition and upgrade" })
          .click();
        const dialog = page.getByRole("dialog");
        await expect(dialog).toContainText("Switch edition from oss to acme?");
        await expect(dialog.locator(".upgrade-switch-losses li")).toHaveCount(
          2,
        );
        const dialogBox = await dialog.boundingBox();
        expect(dialogBox.x).toBeGreaterThanOrEqual(0);
        expect(dialogBox.x + dialogBox.width).toBeLessThanOrEqual(
          viewport.width,
        );
        await page.screenshot({
          path: testInfo.outputPath(
            `switch-edition-confirm-${name}-${theme}.png`,
          ),
        });
        await dialog.getByRole("button", { name: "Cancel" }).click();
        await expect(dialog).toHaveCount(0);

        // And backing out leaves an ordinary upgrade.
        await page
          .getByRole("button", { name: "Don't switch edition" })
          .click();
        await expect(page.locator(".upgrade-will-switch")).toHaveCount(0);
        await expect(
          page
            .getByTestId("upgrade-section")
            .getByRole("button", { name: "Upgrade" }),
        ).toBeVisible();
      });
    }
  }
});
