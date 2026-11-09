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

import { test, expect } from "@playwright/test";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { startRegistryDemo } from "./screenshot.mjs";

let tmpDir;
let demo;

test.beforeAll(async () => {
  tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "grain-registry-e2e-"));
  demo = await startRegistryDemo(tmpDir);
});

test.afterAll(async () => {
  demo?.proc.kill();
  if (tmpDir) {
    await fs.rm(tmpDir, { recursive: true, force: true });
  }
});

test("serves embedded ui-registry under strict CSP and walks through all views without errors", async ({
  page,
}) => {
  const pageErrors = [];
  const cspErrors = [];

  page.on("pageerror", (err) => pageErrors.push(err.message));
  page.on("console", (msg) => {
    if (
      msg.type() === "error" &&
      msg.text().includes("Content Security Policy")
    ) {
      cspErrors.push(msg.text());
    }
  });

  // 1. My entries (/)
  const resp = await page.goto(demo.url);
  expect(resp.status()).toBe(200);
  const cspHeader = resp.headers()["content-security-policy"] || "";
  expect(cspHeader).toContain("default-src 'none'");
  expect(cspHeader).toContain("style-src 'self'");
  expect(cspHeader).not.toContain("'unsafe-inline'");

  await expect(page.getByRole("heading", { name: "My Entries" })).toBeVisible();
  await expect(page.getByText("alice@example.com").first()).toBeVisible();

  // 2. Directory (/directory) + interactive route tester
  await page.getByRole("button", { name: "Directory" }).click();
  await expect(
    page.getByRole("heading", { name: "Organization Directory" }),
  ).toBeVisible();
  await expect(page.getByTestId("dir-card-platform-vm")).toBeVisible();

  await page.getByRole("button", { name: "Resolve route" }).click();
  await expect(
    page.getByText(/Primary match: alice-workstation/i),
  ).toBeVisible();

  // 3. Create / Register (/new)
  await page.getByRole("button", { name: "Create / Register" }).click();
  await expect(
    page.getByRole("heading", { name: "Create or Register a Grain Entry" }),
  ).toBeVisible();

  const nameInput = page.getByLabel(/Entry subdomain name/i);
  await nameInput.fill("e2e-workstation");
  await expect(page.getByTestId("name-availability-status")).toHaveText("free");
  await expect(page.getByText(/-registry-entry e2e-workstation/)).toBeVisible();

  await page.getByRole("button", { name: "Register connected entry" }).click();
  await expect(page.getByTestId("created-entry-banner")).toContainText("grc_");

  // 4. Entry detail (/entries/alice-workstation)
  await page.goto(`${demo.url}/entries/alice-workstation`);
  await expect(page.getByTestId("tunnel-status-badge")).toHaveText("connected");
  await expect(page.getByTestId("entry-last-seen")).toBeVisible();
  await expect(page.getByTestId("scope-conflict-banner")).toContainText(
    "platform-vm",
  );

  await page.getByRole("button", { name: "Rotate token" }).click();
  await expect(page.getByTestId("rotated-token-banner")).toContainText("grc_");

  // 4b. Hosted VM creation with custom boot disk size (/new?kind=instance)
  await page.goto(`${demo.url}/new?kind=instance`);
  const createDiskInput = page.getByLabel(/Boot disk size \(GB\)/i);
  await expect(createDiskInput).toHaveValue("100");
  await page.getByRole("button", { name: "Choose custom name" }).click();
  await page.getByLabel(/Entry subdomain name/i).fill("e2e-hosted-vm");
  await expect(page.getByTestId("name-availability-status")).toHaveText("free");
  await createDiskInput.fill("250");
  await page.getByRole("button", { name: "Create hosted VM" }).click();
  await expect(page.getByTestId("created-entry-banner")).toContainText(
    "e2e-hosted-vm",
  );
  await page.goto(`${demo.url}/entries/e2e-hosted-vm`);
  await expect(page.getByText("250 GB")).toBeVisible();
  await expect(page.getByLabel(/Boot Disk Size \(GB\)/i)).toHaveValue("250");

  // 5. Apps acting for you (/grants)
  await page.getByRole("button", { name: "Apps acting for you" }).click();
  await expect(
    page.getByRole("heading", { name: "Apps acting for you" }),
  ).toBeVisible();

  const grantsSection = page.getByTestId("person-grants-section");
  await expect(grantsSection).toContainText("planner");
  await expect(grantsSection).toContainText("dlg_a1b2c3d4e5f60001");
  await expect(grantsSection).toContainText("triage-bot");
  await expect(grantsSection).not.toContainText("dlg_a1b2c3d4e5f60000");

  // Toggle include_revoked
  await grantsSection.getByLabel("Include revoked grants").check();
  await expect(grantsSection).toContainText("dlg_a1b2c3d4e5f60000");

  // Revoke triage-bot grant and re-grant consent
  const triageRow = grantsSection.locator("tr", {
    hasText: "dlg_a1b2c3d4e5f60002",
  });
  await triageRow.getByRole("button", { name: "Revoke" }).click();
  await expect(
    page.getByText(/Revoked consent grant dlg_a1b2c3d4e5f60002/i),
  ).toBeVisible();

  const consentCard = page.getByTestId("grant-consent-card");
  await consentCard.getByLabel("Delegating app name").fill("triage-bot");
  await consentCard.getByLabel("Entries").fill("platform-vm");
  await consentCard.getByLabel("Access level").selectOption("read");
  await consentCard.getByLabel("Grant TTL value").fill("14");
  await consentCard.getByLabel("Grant TTL unit").selectOption("days");
  await consentCard.getByRole("button", { name: "Grant consent" }).click();
  await expect(
    page.getByText(/Granted read consent to triage-bot/i),
  ).toBeVisible();

  // 6. Admin (/admin) + Delegating Apps controls
  await page.getByRole("button", { name: "Admin" }).click();
  await expect(
    page.getByRole("heading", { name: "Hub Administration" }),
  ).toBeVisible();
  await expect(
    page.getByText("registry-us-central1-a-01").first(),
  ).toBeVisible();

  const appsSection = page.getByTestId("admin-delegating-apps-section");
  await expect(appsSection).toContainText(
    "serviceAccount:planner@acme-prod.iam.gserviceaccount.com",
  );
  await appsSection.getByLabel("App name").fill("ci-helper");
  await appsSection
    .getByLabel("Service account email")
    .fill("ci-helper@acme-prod.iam.gserviceaccount.com");
  await appsSection.getByLabel("Display name").fill("CI Helper");
  await appsSection
    .getByRole("button", { name: "Register delegating app" })
    .click();
  await expect(appsSection).toContainText(
    "serviceAccount:ci-helper@acme-prod.iam.gserviceaccount.com",
  );

  // Enable and re-disable legacy-chat
  const legacyRow = appsSection.locator("tr", { hasText: "legacy-chat" });
  await legacyRow.getByRole("button", { name: "Enable" }).click();
  await expect(
    page.getByText(/Enabled delegating app legacy-chat/i),
  ).toBeVisible();
  await legacyRow.getByRole("button", { name: "Disable" }).click();
  await expect(
    page.getByText(/Disabled delegating app legacy-chat/i),
  ).toBeVisible();

  // 7. Status (/status)
  await page.getByRole("button", { name: "Status" }).click();
  await expect(
    page.getByRole("heading", { name: "Hub Service Status" }),
  ).toBeVisible();
  await expect(page.getByText("*.grain.example.com")).toBeVisible();

  expect(pageErrors).toEqual([]);
  expect(cspErrors).toEqual([]);
});

test("stops and starts a hosted instance from My entries and its detail page", async ({
  page,
}) => {
  const pageErrors = [];
  page.on("pageerror", (err) => pageErrors.push(err.message));

  // The demo seeds docs-preview as a stopped instance.
  await page.goto(demo.url);
  const badge = page.getByTestId("lifecycle-badge-docs-preview");
  await expect(badge).toHaveText("stopped");
  await expect(badge).toHaveClass(/reg-badge-stopped/);
  await expect(
    page.getByRole("button", { name: "Start docs-preview" }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Stop platform-vm" }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Stop alice-workstation" }),
  ).toHaveCount(0);

  // The stopped badge sits on the same line as the host-kind badge beside it.
  const row = badge.locator("xpath=..");
  const hostBadge = row.locator(".gs-badge").first();
  const [b, h] = [await badge.boundingBox(), await hostBadge.boundingBox()];
  expect(Math.abs(b.y + b.height / 2 - (h.y + h.height / 2))).toBeLessThan(2);

  // Detail page: banner, Start, then Stop (with confirmation) back to stopped.
  await page.goto(`${demo.url}/entries/docs-preview`);
  await expect(page.getByTestId("stopped-banner")).toContainText(
    "This instance is stopped.",
  );
  await expect(page.getByRole("button", { name: "Stop instance" })).toHaveCount(
    0,
  );
  await page.getByRole("button", { name: "Start instance" }).click();
  await expect(
    page.getByText("Requested start for docs-preview."),
  ).toBeVisible();
  await expect(page.getByTestId("stopped-banner")).toHaveCount(0);
  await expect(page.getByTestId("lifecycle-badge-docs-preview")).toHaveText(
    "running",
  );

  await page.getByRole("button", { name: "Stop instance" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Stop docs-preview?");
  await dialog.getByRole("button", { name: "Stop instance" }).click();
  await expect(
    page.getByText("Requested stop for docs-preview."),
  ).toBeVisible();
  await expect(page.getByTestId("lifecycle-badge-docs-preview")).toHaveText(
    "stopped",
  );
  await expect(page.getByTestId("stopped-banner")).toBeVisible();

  expect(pageErrors).toEqual([]);
});

test("lays out delegation screens without overflow or misalignment at desktop and phone widths in light and dark", async ({
  browser,
}) => {
  const viewports = [
    { name: "desktop", width: 1440, height: 900, isMobile: false },
    { name: "phone", width: 390, height: 844, isMobile: true },
  ];
  const themes = ["light", "dark"];

  for (const vp of viewports) {
    for (const theme of themes) {
      const context = await browser.newContext({
        viewport: { width: vp.width, height: vp.height },
        colorScheme: theme,
        isMobile: vp.isMobile,
        hasTouch: vp.isMobile,
      });
      await context.addInitScript(
        ([k, v]) => localStorage.setItem(k, v),
        ["grain.themeMode", theme],
      );
      const page = await context.newPage();

      // Check /grants ("Apps acting for you")
      await page.goto(`${demo.url}/grants`);
      await expect(
        page.getByRole("heading", { name: "Apps acting for you" }),
      ).toBeVisible();

      const appliedTheme = await page.evaluate(() =>
        document.documentElement.getAttribute("data-theme"),
      );
      expect(appliedTheme).toBe(theme);

      const grantsOverflow = await page.evaluate(
        () => document.documentElement.scrollWidth > window.innerWidth,
      );
      expect(grantsOverflow).toBe(false);

      const consentBox = await page
        .getByTestId("grant-consent-card")
        .boundingBox();
      const grantsBox = await page
        .getByTestId("person-grants-section")
        .boundingBox();
      expect(consentBox.x).toBeGreaterThanOrEqual(0);
      expect(consentBox.x + consentBox.width).toBeLessThanOrEqual(
        vp.width + 0.5,
      );
      expect(grantsBox.x).toBeGreaterThanOrEqual(0);
      expect(grantsBox.x + grantsBox.width).toBeLessThanOrEqual(vp.width + 0.5);

      if (vp.name === "desktop") {
        const headingBox = await page
          .getByRole("heading", { name: "Your Consent Grants" })
          .boundingBox();
        const toggleBox = await page
          .getByTestId("person-grants-section")
          .locator(".reg-checkbox-row")
          .boundingBox();
        expect(toggleBox.x).toBeGreaterThan(headingBox.x + headingBox.width);
        const hMid = headingBox.y + headingBox.height / 2;
        const tMid = toggleBox.y + toggleBox.height / 2;
        expect(Math.abs(hMid - tMid)).toBeLessThanOrEqual(6);

        const plannerRow = page
          .getByTestId("person-grants-section")
          .locator("tr", { hasText: "dlg_a1b2c3d4e5f60001" });
        const updateBox = await plannerRow
          .getByRole("button", { name: "Update" })
          .boundingBox();
        const revokeBox = await plannerRow
          .getByRole("button", { name: "Revoke" })
          .boundingBox();
        expect(revokeBox.x).toBeGreaterThanOrEqual(
          updateBox.x + updateBox.width,
        );
        expect(
          Math.abs(
            updateBox.y +
              updateBox.height / 2 -
              (revokeBox.y + revokeBox.height / 2),
          ),
        ).toBeLessThanOrEqual(4);
      }

      // Check /admin#delegating-apps
      await page.goto(`${demo.url}/admin`);
      const appsSection = page.getByTestId("admin-delegating-apps-section");
      await expect(appsSection).toBeVisible();
      await page.evaluate(() => {
        const el = document.getElementById("delegating-apps");
        if (el) el.scrollIntoView({ block: "start" });
      });

      const adminOverflow = await page.evaluate(
        () => document.documentElement.scrollWidth > window.innerWidth,
      );
      expect(adminOverflow).toBe(false);

      const topbarBox = await page.locator(".reg-topbar").boundingBox();
      const appsBox = await appsSection.boundingBox();
      expect(appsBox.x).toBeGreaterThanOrEqual(0);
      expect(appsBox.x + appsBox.width).toBeLessThanOrEqual(vp.width + 0.5);
      expect(appsBox.y).toBeGreaterThanOrEqual(
        Math.max(0, topbarBox.y + topbarBox.height - 1),
      );

      if (vp.name === "desktop") {
        const appsTitleBox = await appsSection
          .getByRole("heading", { name: "Delegating Apps" })
          .boundingBox();
        const policyBadgeBox = await appsSection
          .locator(".reg-section-head .gs-badge")
          .boundingBox();
        expect(policyBadgeBox.x).toBeGreaterThan(
          appsTitleBox.x + appsTitleBox.width,
        );
        expect(
          Math.abs(
            appsTitleBox.y +
              appsTitleBox.height / 2 -
              (policyBadgeBox.y + policyBadgeBox.height / 2),
          ),
        ).toBeLessThanOrEqual(6);
      }

      await context.close();
    }
  }
});
