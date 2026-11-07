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
