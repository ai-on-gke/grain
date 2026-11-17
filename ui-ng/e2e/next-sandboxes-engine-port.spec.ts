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

import type { APIRequestContext, Page } from "@playwright/test";
import { test, expect } from "./fixtures.js";

// What the 2026-11-14 connections & infrastructure walkthrough
// (docs/test-runs/2026-11-14-connections-infrastructure-settings.md, G8)
// asked of Settings → Sandboxes beyond what next-hub-sandboxes-parity.spec.js
// covers: the disk overcommit box, the auto controller's live factors and
// the warning on a deployment whose sandboxes have no shape. Every test
// that saves puts back what it changed.

type Json = Record<string, unknown>;

const COMMITMENT = {
  level: "info",
  workers: 4,
  mergers: 1,
  ciJobs: 2,
  runs: 7,
  hostCpus: 16,
  hostMemoryMb: 65536,
  cpusPerSandbox: 4,
  memoryMbPerSandbox: 8192,
  cpusCommitted: 28,
  memoryMbCommitted: 57344,
  cpuRatio: 1.75,
  memoryShare: 0.875,
  budget: {
    mode: "auto",
    cpus: 32,
    cpusBounded: true,
    cpuOvercommit: 2,
    memoryMb: 57344,
    memoryBounded: true,
    memoryOvercommit: 1,
    reserveMemoryMb: 8192,
    reserveIsDefault: true,
    overcommit: {
      cpuOvercommit: 2.5,
      memoryOvercommit: 1.25,
      last: { cpuSome: 12.5, memorySome: 0.5 },
    },
  },
};

// withSettings serves GET /api/settings with `patch` laid over what the
// demo answers, for state `grain demo` cannot be put in.
async function withSettings(
  page: Page,
  patch: (body: Json) => Json,
): Promise<void> {
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = (await res.json()) as Json;
    await route.fulfill({ response: res, json: { ...body, ...patch(body) } });
  });
}

const autoWithCommitment = (body: Json): Json => ({
  sandboxCommitment: COMMITMENT,
  sandboxOvercommit: {
    ...(body["sandboxOvercommit"] as Json),
    mode: "auto",
  },
});

async function getSettings(request: APIRequestContext): Promise<Json> {
  const res = await request.get("/api/settings");
  expect(res.ok()).toBeTruthy();
  return (await res.json()) as Json;
}

async function save(page: Page): Promise<Json> {
  const btn = page.getByTestId("settings-save-btn");
  await expect(btn).toBeEnabled();
  const posted = page.waitForRequest(
    (r) => r.url().endsWith("/api/settings") && r.method() === "POST",
  );
  const done = page.waitForResponse(
    (r) => r.url().endsWith("/api/settings") && r.request().method() === "POST",
  );
  await btn.click();
  const body = (await posted).postDataJSON() as Json;
  expect((await done).status()).toBe(200);
  return body;
}

test("Sandboxes: disk overcommit shows its default, keeps a typed 0 and resets when blank", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  const def = String(before["sandboxDiskOvercommitDefault"]);
  await page.goto("/next/settings/sandboxes");
  const disk = page.getByLabel("Disk overcommit (x)");
  await expect(disk).toHaveValue(String(before["sandboxDiskOvercommit"]));
  await expect(disk).toHaveAttribute("placeholder", def);
  await expect(
    page
      .getByTestId("sandbox-disk-floor")
      .getByText(`grain's default (${def})`),
  ).toBeVisible();

  await disk.fill("0");
  try {
    expect(await save(page)).toMatchObject({ sandboxDiskOvercommit: 0 });
    await page.reload();
    // 0 is a value of its own ("the floor alone"), not grain's default.
    await expect(disk).toHaveValue("0");
    await disk.fill("");
    await disk.blur();
    await expect(disk).toHaveValue(def);
    expect(await save(page)).toMatchObject({
      sandboxDiskOvercommit: Number(def),
    });
  } finally {
    await request.post("/api/settings", {
      data: { sandboxDiskOvercommit: before["sandboxDiskOvercommit"] },
    });
  }
});

test("Sandboxes shows the auto controller's live factors in auto mode only", async ({
  page,
}) => {
  await withSettings(page, autoWithCommitment);
  await page.goto("/next/settings/sandboxes");
  await expect(page.getByTestId("auto-overcommit-effective")).toHaveText(
    "Auto overcommit is active — live effective factors: CPU 2.50×, memory 1.25× (PSI: CPU 12.5%, memory 0.5%).",
  );
  await page.getByTestId("select-overcommit-mode").click();
  await page.getByTestId("select-overcommit-mode-opt-fixed").click();
  await expect(page.getByTestId("auto-overcommit-effective")).toHaveCount(0);
});

test("Sandboxes says nothing about live factors the server does not report", async ({
  page,
}) => {
  // `grain demo` reads no host, so it sends no sandboxCommitment.
  await withSettings(page, (body) => ({
    sandboxOvercommit: {
      ...(body["sandboxOvercommit"] as Json),
      mode: "auto",
    },
  }));
  await page.goto("/next/settings/sandboxes");
  await expect(page.getByTestId("overcommit-auto-bounds")).toBeVisible();
  await expect(page.getByTestId("auto-overcommit-effective")).toHaveCount(0);
});

test("Sandboxes warns that a shapeless deployment's sizes are not in effect", async ({
  page,
}) => {
  await page.goto("/next/settings/sandboxes");
  await expect(page.getByTestId("card-sandbox-sizing")).toBeVisible();
  await expect(page.getByTestId("sandbox-shape-ignored")).toHaveCount(0);

  await withSettings(page, () => ({ sandboxShapeIgnored: true }));
  await page.reload();
  await expect(page.getByTestId("sandbox-shape-ignored")).toContainText(
    "Not in effect on this deployment",
  );
});

test("Sandboxes budget card fits and passes axe at every size @phone @dark", async ({
  page,
  axe,
}) => {
  await withSettings(page, autoWithCommitment);
  await page.goto("/next/settings/sandboxes");
  await expect(page.getByTestId("auto-overcommit-effective")).toBeVisible();
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  const card = (await page
    .getByTestId("card-sandbox-overcommit")
    .boundingBox())!;
  for (const id of [
    "auto-overcommit-effective",
    "input-disk-overcommit",
    "sandbox-commitment-note",
  ]) {
    const b = (await page
      .getByTestId("card-sandbox-overcommit")
      .getByTestId(id)
      .boundingBox())!;
    expect(b.x, id).toBeGreaterThanOrEqual(card.x);
    expect(b.x + b.width, id).toBeLessThanOrEqual(card.x + card.width + 0.5);
  }
  // The disk overcommit box shares the disk floor's grid row with the
  // floor share rather than overlapping it.
  const disk = (await page.getByTestId("input-disk-overcommit").boundingBox())!;
  const share = (await page
    .getByTestId("input-disk-floor-share")
    .boundingBox())!;
  const apart =
    disk.x + disk.width <= share.x + 0.5 ||
    share.x + share.width <= disk.x + 0.5 ||
    disk.y + disk.height <= share.y + 0.5 ||
    share.y + share.height <= disk.y + 0.5;
  expect(apart).toBe(true);
  expect(await axe()).toEqual([]);
});

test("Sandboxes: clearing a box that already holds the default shows the default again", async ({
  page,
  request,
}) => {
  const s = await getSettings(request);
  // grain demo stores grain's defaults in these, so clearing one drafts
  // nothing: the box itself has to be put back.
  expect(s["sandboxDiskOvercommit"]).toBe(s["sandboxDiskOvercommitDefault"]);
  expect(s["sandboxDiskFloorShare"]).toBe(s["sandboxDiskFloorShareDefault"]);
  await page.goto("/next/settings/sandboxes");
  for (const [id, key] of [
    ["input-disk-overcommit", "sandboxDiskOvercommitDefault"],
    ["input-disk-floor-share", "sandboxDiskFloorShareDefault"],
  ] as const) {
    const box = page.getByTestId(id);
    await box.fill("");
    await box.blur();
    await expect(box).toHaveValue(String(s[key]));
  }
  await expect(page.getByTestId("settings-save-btn")).toHaveCount(0);
});
