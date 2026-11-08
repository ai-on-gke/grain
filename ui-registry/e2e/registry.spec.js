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

  // 5. Admin (/admin)
  await page.getByRole("button", { name: "Admin" }).click();
  await expect(
    page.getByRole("heading", { name: "Registry Administration" }),
  ).toBeVisible();
  await expect(
    page.getByText("registry-us-central1-a-01").first(),
  ).toBeVisible();

  // 6. Status (/status)
  await page.getByRole("button", { name: "Status" }).click();
  await expect(
    page.getByRole("heading", { name: "Registry Service Status" }),
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
