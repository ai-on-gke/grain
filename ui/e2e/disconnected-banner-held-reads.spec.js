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

// docs/test-runs/2026-11-09-shell-liveness.md, finding 1: on a daemon
// that holds connections without answering, only the poll's GET
// /api/config had a timeout. The rest of each tick's reads, and the
// background polls', stayed open -- a new set every tick -- and once
// they held all six of the browser's HTTP/1.1 connections to the host
// the config read queued behind them, so a proxy that answered new
// requests again (its old backend connections still half-open) never
// took the banner down. Here every background read is held unanswered,
// and each must be given up on rather than left to pile up; then new
// requests are answered while the held ones never are, and the banner
// must clear.
test.setTimeout(150_000);

const HELD = [
  /\/api\/tasks$/,
  /\/api\/config$/,
  /\/api\/ci\/runs\?/,
  /\/api\/console-sessions$/,
  /\/api\/persistent-sandboxes$/,
  /\/api\/notifications\?/,
  /\/api\/base-branch-gates$/,
];

test("a silent daemon's held reads are given up on, and the banner clears once new ones are answered", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  let silent = false;
  const inFlight = new Map();
  const most = new Map();
  const key = (req) => {
    if (req.method() !== "GET") return null;
    const re = HELD.find((r) => r.test(req.url()));
    return re ? re.source : null;
  };
  page.on("request", (req) => {
    const k = key(req);
    if (!k || !silent) return;
    inFlight.set(k, (inFlight.get(k) || 0) + 1);
    most.set(k, Math.max(most.get(k) || 0, inFlight.get(k)));
  });
  const done = (req) => {
    const k = key(req);
    if (k && inFlight.get(k)) inFlight.set(k, inFlight.get(k) - 1);
  };
  page.on("requestfailed", done);
  page.on("requestfinished", done);
  await page.route(
    (url) => HELD.some((r) => r.test(url.href)),
    (r) => {
      // Never fulfilled, continued nor aborted: held until the page
      // itself gives up on it, like a half-open connection.
      if (silent && r.request().method() === "GET") return;
      return r.continue();
    },
  );
  await page.goto("/tasks");
  await expect(page.locator(".task-row").first()).toBeVisible();
  const banner = page.getByTestId("disconnected-banner");
  await expect(banner).toHaveCount(0);

  silent = true;
  await expect(banner).toBeVisible({ timeout: 45_000 });
  // Long enough for the backoff and the slower polls (staging every
  // 15 s, notifications every 30 s) to have asked again.
  await page.waitForTimeout(35_000);
  // Every read asked while silent was given up on, but for the one of
  // each still in flight.
  for (const re of HELD) {
    expect(inFlight.get(re.source) || 0, re.source).toBeLessThanOrEqual(1);
  }
  expect(most.get(HELD[0].source) || 0).toBeLessThanOrEqual(1);

  // New requests answered again; the held ones above never are.
  silent = false;
  await expect(banner).toHaveCount(0, { timeout: 60_000 });
  await expect(page.locator(".task-row").first()).toBeVisible();
});
