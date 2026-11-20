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

// Regression coverage from the 2026-11-17 connections & infrastructure
// settings walkthrough (docs/test-runs/2026-11-17-connections-
// infrastructure-settings.md): a capability policy saved in Settings →
// Capabilities is reflected in the New task picker, whose note links
// back to #capability-policy (F2), and a fractional cache size is
// refused in plain words (F1). Nothing here is left changed in the
// worker's demo.

test("a policy saved in Settings shows on New task's held grants and links back to it", async ({
  page,
  request,
}) => {
  const before = await (await request.get("/api/capability-policy")).json();
  try {
    await page.goto("/next/settings/capabilities#capability-policy");
    await page
      .getByTestId("input-policy-capability")
      .fill("https://grain.dev/capabilities/github#*");
    await page.getByTestId("select-policy-effect").click();
    await page.getByTestId("select-policy-effect-opt-deny").click();
    await page.getByTestId("add-policy-rule-btn").click();
    const saved = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/capability-policy") &&
        r.request().method() === "PUT",
    );
    await page.getByTestId("save-cap-policy-btn").click();
    expect((await saved).status()).toBe(200);
    await page.reload();
    await expect(page.getByTestId("card-capability-policy")).toContainText(
      "https://grain.dev/capabilities/github#*",
    );

    // The demo's default grants hold github#read-issues={}: the New task
    // picker says the policy denies it for this repository.
    await page.goto("/next/inbox");
    await page.getByRole("button", { name: /^New/ }).first().click();
    await page.getByText(/Capabilities & repo context/).click();
    const picker = page.getByTestId("nt-grants");
    const held = picker.locator('[data-testid^="held-grant-"]', {
      hasText: "read-issues",
    });
    await expect(held).toContainText("denied by policy");
    const notes = picker.getByTestId("nt-grants-policy-notes");
    await expect(notes).toContainText(
      "the capability policy denies https://grain.dev/capabilities/github#read-issues",
    );
    // repo#read being CI-only is never blamed on the policy.
    await expect(notes).not.toContainText("repo#read");
    const link = notes.getByTestId("nt-grants-policy-link").first();
    await expect(link).toHaveAttribute(
      "href",
      "/next/settings/capabilities#capability-policy",
    );
    await link.click();
    await expect(page).toHaveURL(
      /\/next\/settings\/capabilities#capability-policy$/,
    );
    await expect(page.getByTestId("card-capability-policy")).toBeInViewport();
  } finally {
    await request.put("/api/capability-policy", { data: before });
  }
});

test("Caches refuses a fractional size by name, not with Go's decode error", async ({
  page,
}) => {
  await page.goto("/next/settings/caches");
  const pkg = page.locator("#c-pkg-gb");
  await pkg.fill("1.5");
  await pkg.press("Tab");
  const done = page.waitForResponse(
    (r) => r.url().endsWith("/api/settings") && r.request().method() === "POST",
  );
  await page.getByTestId("settings-save-btn").click();
  const res = await done;
  expect(res.status()).toBe(400);
  const msg = "packageCacheSizeGb must be a whole number, not 1.5";
  expect((await res.json()).error).toBe(msg);
  // The banner names the field by its label, not its JSON key.
  await expect(
    page.getByText(
      "Proxy cache max size (GiB): must be a whole number, not 1.5",
    ),
  ).toBeVisible();
  await expect(page.getByText(/Go struct field/)).toHaveCount(0);
  await pkg.fill("");
  await pkg.press("Tab");
});

test("the policy note and its link stay inside the New task sheet on a phone @phone @dark", async ({
  page,
  request,
}) => {
  const before = await (await request.get("/api/capability-policy")).json();
  await request.put("/api/capability-policy", {
    data: {
      policy: {
        rules: [
          { capability: "https://grain.dev/capabilities/github#*", deny: true },
        ],
      },
    },
  });
  try {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/next/inbox");
    await page.getByRole("button", { name: /^New/ }).first().click();
    await page.getByText(/Capabilities & repo context/).click();
    const picker = page.getByTestId("nt-grants");
    const notes = picker.getByTestId("nt-grants-policy-notes");
    await notes.scrollIntoViewIfNeeded();
    await expect(notes).toBeVisible();
    const p = await picker.boundingBox();
    for (const el of [
      notes,
      notes.getByTestId("nt-grants-policy-link").first(),
    ]) {
      const b = await el.boundingBox();
      expect(b.x).toBeGreaterThanOrEqual(p.x - 0.5);
      expect(b.x + b.width).toBeLessThanOrEqual(p.x + p.width + 0.5);
    }
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  } finally {
    await request.put("/api/capability-policy", { data: before });
  }
});
