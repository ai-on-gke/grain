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

// Settings → Hub (/settings/registry): three cards covering live
// connected-entry status & advertised scopes (#registry-status), catalog
// metadata & registration credential (#registry-config), and scope override
// rules (#registry-scopes). Verifies alignment, right-slot placement on
// desktop, clean vertical stacking on phone, and zero sideways scroll.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`the hub settings section fits a ${name} screen`, async ({ page }) => {
    const pageErrors = [];
    page.on("pageerror", (err) => pageErrors.push(String(err)));

    await page.setViewportSize(viewport);
    await page.goto("/settings/registry");

    const noSidewaysScroll = async () => {
      const overflow = await page.evaluate(
        () =>
          document.documentElement.scrollWidth >
          document.documentElement.clientWidth + 1,
      );
      expect(overflow, "the page scrolls sideways").toBe(false);
    };

    const statusCard = page.locator("#registry-status");
    await expect(statusCard).toBeVisible();
    const statusCardBox = await statusCard.boundingBox();
    expect(statusCardBox).not.toBeNull();

    const insideBox = (outer) => (inner) => {
      expect(inner).not.toBeNull();
      expect(inner.x).toBeGreaterThanOrEqual(outer.x - 1);
      expect(inner.x + inner.width).toBeLessThanOrEqual(
        outer.x + outer.width + 1,
      );
    };
    const insideStatus = insideBox(statusCardBox);

    const chip = page.getByTestId("registry-state-chip");
    const endpoint = page.getByTestId("registry-endpoint-label");
    const heartbeatBtn = page.getByTestId("registry-heartbeat-now");
    await expect(chip).toHaveText("Connected");
    await expect(endpoint).toContainText("https://registry.grain.example.com");
    await expect(endpoint).toContainText("acme-widgets");
    await expect(heartbeatBtn).toBeVisible();

    const chipBox = await chip.boundingBox();
    const endpointBox = await endpoint.boundingBox();
    const btnBox = await heartbeatBtn.boundingBox();
    insideStatus(chipBox);
    insideStatus(endpointBox);
    insideStatus(btnBox);

    if (name === "desktop") {
      // Status badge centred on the endpoint label, and Heartbeat now in the right-hand slot.
      expect(
        Math.abs(
          chipBox.y +
            chipBox.height / 2 -
            (endpointBox.y + endpointBox.height / 2),
        ),
      ).toBeLessThan(4);
      expect(
        Math.abs(
          chipBox.y + chipBox.height / 2 - (btnBox.y + btnBox.height / 2),
        ),
      ).toBeLessThan(6);
      expect(btnBox.x).toBeGreaterThan(endpointBox.x + endpointBox.width);
      expect(
        statusCardBox.x + statusCardBox.width - (btnBox.x + btnBox.width),
      ).toBeLessThan(40);
    } else {
      // Phone: Heartbeat now stacks below the status row without clipping.
      expect(btnBox.y).toBeGreaterThanOrEqual(chipBox.y + chipBox.height - 1);
    }

    const scopesList = page.getByTestId("registry-advertised-scopes");
    await expect(scopesList).toBeVisible();
    await expect(scopesList).toContainText("acme/widgets");
    await expect(scopesList).toContainText("primary");
    insideStatus(await scopesList.boundingBox());

    // Card 2: Catalog & authentication (#registry-config)
    const configCard = page.locator("#registry-config");
    await configCard.scrollIntoViewIfNeeded();
    const configBox = await configCard.boundingBox();
    expect(configBox).not.toBeNull();
    const insideConfig = insideBox(configBox);

    const authSelect = page.getByRole("combobox", {
      name: "Credential source",
    });
    const hostSelect = page.getByRole("combobox", { name: "Host kind" });
    await expect(authSelect).toBeVisible();
    await expect(hostSelect).toBeVisible();
    const authBox = await authSelect.boundingBox();
    const hostBox = await hostSelect.boundingBox();
    insideConfig(authBox);
    insideConfig(hostBox);
    if (name === "desktop") {
      expect(hostBox.x).toBeGreaterThan(authBox.x + authBox.width);
      expect(Math.abs(hostBox.y - authBox.y)).toBeLessThan(12);
    } else {
      expect(hostBox.y).toBeGreaterThan(authBox.y + authBox.height);
    }

    const secretInput = configCard.getByLabel("Registration token (grc_…)", {
      exact: true,
    });
    const uploadBtn = configCard.getByRole("button", {
      name: "Upload a file for Registration token (grc_…)",
    });
    await expect(secretInput).toBeVisible();
    await expect(uploadBtn).toBeVisible();
    insideConfig(await secretInput.boundingBox());
    insideConfig(await uploadBtn.boundingBox());

    // Card 3: Scope overrides (#registry-scopes)
    const scopesCard = page.locator("#registry-scopes");
    await scopesCard.scrollIntoViewIfNeeded();
    const scopesBox = await scopesCard.boundingBox();
    expect(scopesBox).not.toBeNull();
    const insideScopes = insideBox(scopesBox);

    const repoInput = scopesCard.getByLabel("Repository (owner/name)");
    const prefixInput = scopesCard.getByLabel("Path prefix (optional)");
    const addBtn = scopesCard.getByRole("button", { name: "Add rule" });
    const repoBox = await repoInput.boundingBox();
    const prefixBox = await prefixInput.boundingBox();
    const addBox = await addBtn.boundingBox();
    insideScopes(repoBox);
    insideScopes(prefixBox);
    insideScopes(addBox);
    if (name === "desktop") {
      expect(prefixBox.x).toBeGreaterThan(repoBox.x + repoBox.width);
      expect(addBox.x).toBeGreaterThan(prefixBox.x + prefixBox.width);
    } else {
      expect(prefixBox.y).toBeGreaterThan(repoBox.y + repoBox.height);
      expect(addBox.y).toBeGreaterThan(prefixBox.y + prefixBox.height);
    }

    await noSidewaysScroll();
    expect(pageErrors).toEqual([]);
  });
}

test("saving a hub setting in the browser sends the hub key", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);
  await page.goto("/settings/registry");

  const configCard = page.locator("#registry-config");
  await configCard.scrollIntoViewIfNeeded();
  const purposeInput = configCard.getByLabel("Purpose");
  await expect(purposeInput).toBeVisible();
  const originalPurpose = await purposeInput.inputValue();

  const putPromise = page.waitForRequest(
    (req) => req.url().endsWith("/api/settings") && req.method() === "PUT",
  );
  try {
    await purposeInput.fill(`${originalPurpose} (e2e)`);
    await purposeInput.blur();
    const putReq = await putPromise;
    const payload = putReq.postDataJSON();
    expect(payload).toHaveProperty("hub");
    expect(payload).not.toHaveProperty("registry");
    expect(payload.hub.purpose).toBe(`${originalPurpose} (e2e)`);
    await expect(page.getByTestId("settings-status")).toHaveText(
      "All changes saved",
    );
  } finally {
    const cur = await purposeInput.inputValue().catch(() => originalPurpose);
    if (cur !== originalPurpose) {
      const restoreDone = page.waitForResponse(
        (res) =>
          res.url().endsWith("/api/settings") &&
          res.request().method() === "PUT",
      );
      await purposeInput.fill(originalPurpose);
      await purposeInput.blur();
      await restoreDone;
    }
  }
});
