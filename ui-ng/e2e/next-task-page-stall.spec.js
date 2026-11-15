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

// The task page "froze for about a minute" (task 5801). Two causes were
// suspected and both are pinned here:
//
// 1. A long (~6 KB) markdown description edited over and over while the
//    page is open: every live update must re-render without a main-thread
//    long task, and the page must still answer a click.
// 2. The trace that was actually captured: the main thread was idle and a
//    click to another page started a navigation whose lazy chunk sat
//    pending behind a stalled origin, with nothing on screen changing.
//    The fix preloads every page's chunk shortly after start-up
//    (shell/route-preload.ts) and shows a progress bar while a navigation
//    is still loading (shell/nav-progress.component.ts).

async function api(base, code, method, p, body) {
  const res = await fetch(`${base}${p}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${method} ${p}: ${res.status} ${await res.text()}`);
  }
  const txt = await res.text();
  return txt ? JSON.parse(txt) : null;
}

// About 6 KB of markdown in the shape of the reported description:
// headings, numbered lists and plenty of inline code.
function longDescription(marker) {
  const parts = [`# Stall repro ${marker}`, ""];
  for (let s = 1; parts.join("\n").length < 6000; s++) {
    parts.push(`## Section ${s}`, "");
    for (let i = 1; i <= 6; i++) {
      parts.push(
        `${i}. Step ${s}.${i}: run \`grain ci wait --resume my-branch\` and check \`/api/tasks/${s}${i}?comments_limit=100\` returns \`200\`.`,
      );
    }
    parts.push("", "- a `bullet` with **bold** and _em_ text", "");
  }
  return parts.join("\n");
}

const LONG_TASKS = () => {
  window.__longTasks = [];
  try {
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) window.__longTasks.push(e.duration);
    }).observe({ type: "longtask", buffered: true });
  } catch {
    // longtask is Chromium-only; the suite runs Chromium.
  }
};

const MAIN_NAV = 'nav[aria-label="Primary destinations"]';

async function fileTask(demoServer, accessCode, title, description) {
  const r = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title,
    description,
  });
  return r.id;
}

async function closeQuietly(demoServer, accessCode, id) {
  if (!id) return;
  await fetch(`${demoServer}/api/tasks/${id}/close`, {
    method: "POST",
    headers: { Authorization: `Bearer ${accessCode}` },
  }).catch(() => {});
}

test("repeated edits to a 6 KB description over the live channel cause no long task", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  await page.addInitScript(LONG_TASKS);
  let id;
  try {
    id = await fileTask(
      demoServer,
      accessCode,
      `Stall repro edits ${Date.now()}`,
      longDescription("v0"),
    );
    await page.goto(`/next/task/${id}`);
    const details = page.getByTestId("task-details");
    await expect(details).toContainText("Stall repro v0");
    // Start counting after the first render: boot work isn't the subject.
    await page.evaluate(() => (window.__longTasks.length = 0));

    for (let n = 1; n <= 6; n++) {
      await api(demoServer, accessCode, "PATCH", `/api/tasks/${id}`, {
        description: longDescription(`v${n}`),
      });
      await expect(details).toContainText(`Stall repro v${n}`, {
        timeout: 15_000,
      });
    }

    // The page still answers a click at once.
    const started = Date.now();
    await page.getByTestId("task-tab-runs").click({ timeout: 2_000 });
    await expect(page.getByTestId("task-tab-runs")).toHaveAttribute(
      "aria-current",
      "page",
      { timeout: 2_000 },
    );
    expect(Date.now() - started).toBeLessThan(2_000);

    const longTasks = await page.evaluate(() => window.__longTasks);
    expect(
      Math.max(0, ...longTasks),
      `long tasks: ${JSON.stringify(longTasks)}`,
    ).toBeLessThan(200);
  } finally {
    await closeQuietly(demoServer, accessCode, id);
  }
});

test("pages are preloaded, so a click navigates while the origin stalls with slow 503s", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  let id;
  const held = [];
  try {
    id = await fileTask(
      demoServer,
      accessCode,
      `Stall repro preload ${Date.now()}`,
      longDescription("p"),
    );
    await page.goto(`/next/task/${id}`);
    await expect(page.getByTestId("task-details")).toContainText(
      "Stall repro p",
    );

    // Wait for the preloader to fetch the other pages' chunks: the count
    // of loaded chunk scripts grows well past what the task page itself
    // needed and then stops growing.
    const chunkCount = () =>
      page.evaluate(
        () =>
          performance
            .getEntriesByType("resource")
            .filter((e) => /\/next\/chunk-[^/]*\.js$/.test(e.name)).length,
      );
    const atRender = await chunkCount();
    let last = -1;
    await expect
      .poll(
        async () => {
          const n = await chunkCount();
          const steady = n === last && n > atRender + 5;
          last = n;
          return steady;
        },
        { timeout: 20_000, intervals: [1_000] },
      )
      .toBe(true);

    // Now the origin stalls the way it did behind identity verification:
    // every API read is held 12s and then refused, and any script fetch
    // never answers at all.
    await page.route("**/api/**", async (route) => {
      await new Promise((r) => setTimeout(r, 12_000));
      await route
        .fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({ error: "verifier_unavailable" }),
        })
        .catch(() => {});
    });
    await page.route("**/next/chunk-*.js", (route) => {
      held.push(route);
    });

    await page.locator(`${MAIN_NAV} a[href$="/tasks"]`).first().click();
    await expect(page).toHaveURL(/\/next\/tasks(\?|$)/, { timeout: 3_000 });
    await expect(page.getByTestId("nav-progress")).toHaveCount(0);
    expect(held.map((r) => r.request().url())).toEqual([]);

    // And back: the task page's chunk was loaded already, too.
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`/next/task/${id}`), {
      timeout: 3_000,
    });
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
    for (const r of held) await r.abort().catch(() => {});
    await closeQuietly(demoServer, accessCode, id);
  }
});

test("a navigation stuck on its chunk shows progress and leaves the page usable", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  // Readers who ask to save data get no preloading, which is also the
  // state of a click made before the preloader has run.
  await page.addInitScript(() => {
    Object.defineProperty(navigator, "connection", {
      configurable: true,
      get: () => ({ saveData: true }),
    });
  });
  let id;
  const held = [];
  try {
    id = await fileTask(
      demoServer,
      accessCode,
      `Stall repro progress ${Date.now()}`,
      longDescription("s"),
    );
    await page.goto(`/next/task/${id}`);
    await expect(page.getByTestId("task-details")).toContainText(
      "Stall repro s",
    );

    await page.route("**/next/chunk-*.js", (route) => {
      held.push(route);
    });

    const started = Date.now();
    await page.locator(`${MAIN_NAV} a[href$="/agents"]`).first().click();
    const bar = page.getByTestId("nav-progress");
    await expect(bar).toBeVisible({ timeout: 1_000 });
    expect(Date.now() - started).toBeLessThan(1_000);
    await expect(bar).toHaveAttribute("role", "progressbar");
    expect(held.length).toBeGreaterThan(0);

    // The bar takes no input: the page under it still scrolls and its
    // tabs still answer, and a click there supersedes the stuck navigation.
    expect(await bar.evaluate((el) => getComputedStyle(el).pointerEvents)).toBe(
      "none",
    );
    await page.mouse.move(700, 500);
    await page.mouse.wheel(0, 400);
    await page.getByTestId("task-tab-runs").click({ timeout: 2_000 });
    await expect(page.getByTestId("task-tab-runs")).toHaveAttribute(
      "aria-current",
      "page",
      { timeout: 2_000 },
    );
    await expect(bar).toHaveCount(0);

    // Once the network answers, the next click lands.
    await page.unroute("**/next/chunk-*.js");
    for (const r of held.splice(0)) await r.continue().catch(() => {});
    await page.locator(`${MAIN_NAV} a[href$="/agents"]`).first().click();
    await expect(page).toHaveURL(/\/next\/agents/, { timeout: 10_000 });
    await expect(bar).toHaveCount(0);
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
    for (const r of held) await r.abort().catch(() => {});
    await closeQuietly(demoServer, accessCode, id);
  }
});
