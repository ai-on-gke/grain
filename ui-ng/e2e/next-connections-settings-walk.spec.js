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

// Regression coverage from the 2026-11-16 connections & infrastructure
// settings walkthrough (docs/test-runs/2026-11-16-connections-
// infrastructure-settings.md): the Repos page's no-git-connection
// notice, the Deployment credentials description, and the capability
// policy editor's pattern check and in-card errors. Nothing here is left
// changed in the worker's demo.

test("Repos says there is no git connection, and links to Settings → Git", async ({
  page,
}) => {
  // The demo always has its github.com connection; take it out of the
  // config this page reads, rather than out of the shared demo.
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.deployment = {
      ...(body.deployment ?? {}),
      gitConnections: [],
      hasGitCredential: false,
      gitCredential: "",
    };
    await route.fulfill({ response: res, json: body });
  });
  await page.goto("/next/repos");
  const notice = page.getByTestId("no-git-connection");
  await expect(notice).toBeVisible();
  await expect(notice).toContainText("token, SSH key");
  await notice.getByRole("link", { name: "Settings → Git" }).click();
  await expect(page).toHaveURL(/\/next\/settings\/git$/);
});

test("Repos shows no git-connection notice when the deployment has one", async ({
  page,
}) => {
  await page.goto("/next/repos");
  await expect(page.getByTestId("repos-count")).toBeVisible();
  await expect(page.getByTestId("repos-allowlist-note")).toBeVisible();
  await expect(page.getByTestId("no-git-connection")).toHaveCount(0);
});

test("Deployment credentials keeps the space after the gcp server link", async ({
  page,
}) => {
  await page.goto("/next/settings/capabilities");
  const desc = page.getByTestId("card-credentials").locator(".s-card-desc");
  await expect(desc).toContainText("gcp capability server's gcp-key and");
});

test("the capability policy editor refuses a bare name and shows a refused save in its card", async ({
  page,
  request,
}) => {
  const before = await (await request.get("/api/capability-policy")).json();
  await page.goto("/next/settings/capabilities");
  const pattern = page.getByTestId("input-policy-capability");
  await expect(pattern).toHaveAttribute("placeholder", /^https:\/\//);

  // A bare retired name is what the server refuses: say so, and keep
  // Add rule from queuing it.
  await pattern.fill("gh-cli");
  await expect(pattern).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByTestId("policy-capability-hint")).toContainText(
    "starting with https://",
  );
  await expect(page.getByTestId("add-policy-rule-btn")).toBeDisabled();

  // A reference the server still refuses (a malformed glob) is reported
  // in the policy card, not only in the page banner far above it.
  await pattern.fill("https://grain.dev/capabilities/gcp#[");
  await expect(pattern).not.toHaveAttribute("aria-invalid", "true");
  await page.getByTestId("add-policy-rule-btn").click();
  await page.getByTestId("preview-cap-policy-btn").click();
  const err = page.getByTestId("cap-policy-error");
  await expect(err).toBeVisible();
  await expect(err).toContainText("capability policy rule 0");
  await expect(err).toBeInViewport();
  await page.getByTestId("save-cap-policy-btn").click();
  await expect(err).toContainText("capability policy rule 0");
  await err.getByRole("button", { name: "Dismiss" }).click();
  await expect(err).toHaveCount(0);

  // Nothing was stored.
  const after = await (await request.get("/api/capability-policy")).json();
  expect(after).toEqual(before);
});

test("the policy hint and error fit a phone @phone", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/next/settings/capabilities");
  const card = page.getByTestId("card-capability-policy");
  await page
    .getByTestId("input-policy-capability")
    .fill("https://grain.dev/capabilities/gcp#[");
  await page.getByTestId("add-policy-rule-btn").click();
  await page.getByTestId("preview-cap-policy-btn").click();
  await expect(page.getByTestId("cap-policy-error")).toBeVisible();
  await page.getByTestId("input-policy-capability").fill("gh-cli");
  const c = await card.boundingBox();
  for (const id of ["cap-policy-error", "policy-capability-hint"]) {
    const b = await page.getByTestId(id).boundingBox();
    expect(b.x).toBeGreaterThanOrEqual(c.x);
    expect(b.x + b.width).toBeLessThanOrEqual(c.x + c.width + 0.5);
  }
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  await page
    .getByTestId("cap-policy-error")
    .getByRole("button", { name: "Dismiss" })
    .click();
  await page.getByTestId("input-policy-capability").fill("");
});
