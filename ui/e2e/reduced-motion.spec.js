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

// MUI draws its own transitions -- the drawer's slide, a Dialog's fade,
// the backdrop's, the Paper shadow -- and on its own it ignores
// prefers-reduced-motion. src/theme.js's reduced-motion variants zero
// them, and AppThemeProvider picks those while the media query matches
// (grain/task-1853). jsdom has no computed transitions, so this is the
// one place that can see what the browser actually runs.
//
// Both halves are asserted: a reader who asked for less motion gets none
// on the phone drawer or a task's overlay, and a reader who did not still
// gets the slide and the fade exactly as before.
//
// The preference is set with page.emulateMedia rather than
// `test.use({ reducedMotion })`: under the Playwright this suite pins
// (1.62) that option never reaches the page -- matchMedia still answers
// false -- with or without this suite's fixtures.
const PHONE = { width: 390, height: 844 };

// durations reads every comma-separated duration in an element's
// computed transition-duration ("0.225s", "0s, 0s") as seconds.
async function durations(locator) {
  const raw = await locator.evaluate(
    (el) => getComputedStyle(el).transitionDuration,
  );
  return raw.split(",").map((d) => {
    const value = parseFloat(d);
    return d.trim().endsWith("ms") ? value / 1000 : value;
  });
}

async function openDrawer(page) {
  await page.goto("/");
  await page.getByRole("button", { name: "Open navigation" }).click();
  const paper = page.locator(".MuiDrawer-paper");
  await expect(paper).toBeVisible();
  return paper;
}

async function openTaskOverlay(page) {
  await page.goto("/tasks/2");
  const paper = page.locator(".MuiDialog-paper").first();
  await expect(paper).toBeVisible();
  return paper;
}

test.describe("under prefers-reduced-motion: reduce", () => {
  test.use({ viewport: PHONE });
  test.beforeEach(async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
  });

  test("the phone drawer opens without a slide", async ({ page }) => {
    const paper = await openDrawer(page);
    for (const d of await durations(paper)) expect(d).toBe(0);
  });

  test("a task's overlay opens without a fade", async ({ page }) => {
    const paper = await openTaskOverlay(page);
    for (const d of await durations(paper)) expect(d).toBe(0);
    const container = page.locator(".MuiDialog-container").first();
    for (const d of await durations(container)) expect(d).toBe(0);
  });

  // The preference is read live: turning it off mid-session brings the
  // transitions back without a reload, as the OS setting would.
  test("follows the preference when it changes", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.getByRole("button", { name: "Open navigation" }),
    ).toBeVisible();
    await page.emulateMedia({ reducedMotion: "no-preference" });
    const paper = await openDrawer(page);
    expect(Math.max(...(await durations(paper)))).toBeGreaterThan(0);
  });
});

test.describe("without a reduced-motion preference", () => {
  test.use({ viewport: PHONE });
  test.beforeEach(async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "no-preference" });
  });

  test("the phone drawer still slides", async ({ page }) => {
    const paper = await openDrawer(page);
    expect(Math.max(...(await durations(paper)))).toBeGreaterThan(0);
  });

  test("a task's overlay still fades", async ({ page }) => {
    await openTaskOverlay(page);
    const container = page.locator(".MuiDialog-container").first();
    expect(Math.max(...(await durations(container)))).toBeGreaterThan(0);
  });
});
