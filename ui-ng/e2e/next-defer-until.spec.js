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

// Defer with a wake date from the /next/ Tasks selection bar, which the
// 2026-11-14 task intake walkthrough
// (docs/test-runs/2026-11-14-task-intake-inbox-lifecycle.md, steps 3.2
// and 4.3) found always posting deferredUntil "": Defer until… posts the
// start of the picked day for every selected task. The task detail's own
// Defer until… is covered by next-task-holds-defer.spec.js.

// The display zone is pinned to UTC so the start of a picked day is a
// known instant whatever zone the runner is in.
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("grain.timeZone", "UTC"));
});

// A day a month out, as a date input wants it, and the instant it starts.
function wakeDay() {
  const d = new Date(Date.now() + 30 * 24 * 3600 * 1000);
  const day = d.toISOString().slice(0, 10);
  return { day, at: `${day}T00:00:00Z` };
}

function api(demoServer, accessCode) {
  return async (method, path, body) => {
    const res = await fetch(`${demoServer}${path}`, {
      method,
      headers: {
        Authorization: `Bearer ${accessCode}`,
        "Content-Type": "application/json",
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    expect(res.ok, `${method} ${path}: ${res.status}`).toBe(true);
    const text = await res.text();
    return text ? JSON.parse(text) : null;
  };
}

async function closeAll(demoServer, accessCode, ids) {
  for (const id of ids) {
    await fetch(`${demoServer}/api/tasks/${id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
}

test("the Tasks selection bar defers several tasks until a picked day", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const call = api(demoServer, accessCode);
  const ids = [];
  for (const n of [1, 2]) {
    const t = await call("POST", "/api/tasks", {
      title: `Batch defer until ${n} ${Date.now()}`,
      approved: true,
    });
    ids.push(t.id);
  }
  try {
    await page.goto("/next/tasks");
    for (const id of ids) {
      await page.getByLabel(`Select #${id}`, { exact: true }).check();
    }
    const bar = page.getByTestId("batch-bar");
    await expect(bar).toContainText("2 selected");
    await bar.getByTestId("batch-defer-until-open").click();
    const yes = bar.getByTestId("batch-defer-until-yes");
    await expect(yes).toBeDisabled();
    const { day, at } = wakeDay();
    await bar.getByTestId("batch-defer-date").fill(day);
    await yes.click();
    for (const id of ids) {
      await expect(page.getByTestId(`row-${id}`)).toHaveCount(0);
      const read = await call("GET", `/api/tasks/${id}`);
      expect(read.state).toBe("deferred");
      expect(Date.parse(read.deferredUntil)).toBe(Date.parse(at));
    }
    await expect(bar).toHaveCount(0);
  } finally {
    await closeAll(demoServer, accessCode, ids);
  }
});

test("the selection bar's Defer until… fits a phone width without scrolling sideways", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const call = api(demoServer, accessCode);
  const t = await call("POST", "/api/tasks", {
    title: `Batch defer until phone ${Date.now()}`,
    approved: true,
  });
  try {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/next/tasks");
    await page.getByLabel(`Select #${t.id}`, { exact: true }).check();
    const bar = page.getByTestId("batch-bar");
    await bar.getByTestId("batch-defer-until-open").click();
    for (const id of [
      "batch-defer-until",
      "batch-defer-date",
      "batch-defer-until-yes",
    ]) {
      const box = await bar.getByTestId(id).boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(390);
    }
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    );
    expect(overflow).toBe(false);
  } finally {
    await closeAll(demoServer, accessCode, [t.id]);
  }
});
