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

// The ceilings on a task's own sandbox shape (sandboxShapeLimits): Settings
// → Sandboxes edits the deployment maximum and the per-role ceilings through
// PUT /api/settings, and the New task form shows the signed-in caller's
// ceiling beside its vCPU, memory and disk fields, says the server's
// refusal there, and explains the fields away on a backend with no shape.

import { test, expect } from "./fixtures.js";

async function putSettings(demoServer, accessCode, body) {
  const res = await fetch(`${demoServer}/api/settings`, {
    method: "PUT",
    headers: {
      Authorization: `Bearer ${accessCode}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
  expect(res.ok, await res.text()).toBe(true);
}

async function getSettings(demoServer, accessCode) {
  const res = await fetch(`${demoServer}/api/settings`, {
    headers: { Authorization: `Bearer ${accessCode}` },
  });
  expect(res.ok).toBe(true);
  return res.json();
}

async function within(child, parent) {
  const c = await child.boundingBox();
  const p = await parent.boundingBox();
  expect(c).not.toBeNull();
  expect(p).not.toBeNull();
  expect(c.x).toBeGreaterThanOrEqual(p.x - 1);
  expect(c.x + c.width).toBeLessThanOrEqual(p.x + p.width + 1);
}

test("Settings → Sandboxes round-trips the per-task maximum and role ceilings", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  const before = (await getSettings(demoServer, accessCode))
    .sandboxShapeLimits ?? { max: {} };
  try {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/next/settings/sandboxes");
    const card = page.getByTestId("card-sandbox-shape-limits");
    await expect(card).toBeVisible();
    await expect(page.getByTestId("shape-max-effective")).toHaveText(
      "32 vCPU · 131072 MiB · 1024 GiB disk",
    );
    await expect(page.getByTestId("shape-roles-empty")).toBeVisible();

    await page.getByTestId("input-shape-max-cpus").fill("16");
    await page.getByTestId("input-shape-max-diskGb").fill("200");
    await page.getByTestId("input-shape-role-new").fill("grain/contributor");
    await page.getByTestId("add-shape-role").click();
    const role = page.getByTestId("shape-role-grain/contributor");
    await expect(role).toBeVisible();
    // A role's blank dimension says what it falls back to.
    await expect(
      page.getByTestId("input-shape-role-grain/contributor-cpus"),
    ).toHaveAttribute("placeholder", "Max (16)");
    await page.getByTestId("input-shape-role-grain/contributor-cpus").fill("4");
    await page
      .getByTestId("input-shape-role-grain/contributor-memoryMb")
      .fill("16384");

    // The three role inputs share a row and stay inside the card.
    const cpus = page.getByTestId("input-shape-role-grain/contributor-cpus");
    const disk = page.getByTestId("input-shape-role-grain/contributor-diskGb");
    const cb = await cpus.boundingBox();
    const db = await disk.boundingBox();
    expect(Math.abs(cb.y - db.y)).toBeLessThanOrEqual(1);
    await within(disk, card);

    await page.getByTestId("settings-save-btn").click();
    await expect(page.getByTestId("settings-save-bar")).toBeHidden({
      timeout: 15_000,
    });
    const saved = await getSettings(demoServer, accessCode);
    expect(saved.sandboxShapeLimits).toEqual({
      max: { cpus: 16, diskGb: 200 },
      roles: { "grain/contributor": { cpus: 4, memoryMb: 16384 } },
    });
    expect(saved.sandboxShapeMax).toEqual({
      cpus: 16,
      memoryMb: 131072,
      diskGb: 200,
    });

    await page.reload();
    await expect(page.getByTestId("input-shape-max-cpus")).toHaveValue("16");
    await expect(page.getByTestId("shape-max-effective")).toHaveText(
      "16 vCPU · 131072 MiB · 200 GiB disk",
    );
    await expect(
      page.getByTestId("input-shape-role-grain/contributor-memoryMb"),
    ).toHaveValue("16384");

    // On a phone the card's fields stack inside it, nothing sideways.
    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(250);
    await within(
      page.getByTestId("input-shape-role-grain/contributor-diskGb"),
      page.getByTestId("card-sandbox-shape-limits"),
    );
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      ),
    ).toBeLessThanOrEqual(0);

    // Removing the role and clearing the maximum saves grain's defaults.
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.getByTestId("remove-shape-role-grain/contributor").click();
    await page.getByTestId("input-shape-max-cpus").fill("");
    await page.getByTestId("input-shape-max-diskGb").fill("");
    await page.getByTestId("settings-save-btn").click();
    await expect(page.getByTestId("settings-save-bar")).toBeHidden({
      timeout: 15_000,
    });
    const cleared = await getSettings(demoServer, accessCode);
    expect(cleared.sandboxShapeLimits?.roles ?? {}).toEqual({});
    expect(cleared.sandboxShapeMax).toEqual({
      cpus: 32,
      memoryMb: 131072,
      diskGb: 1024,
    });
  } finally {
    await putSettings(demoServer, accessCode, { sandboxShapeLimits: before });
  }
});

test("New task form shows the caller's sandbox ceiling and says the server's refusal beside the fields", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  const before = (await getSettings(demoServer, accessCode))
    .sandboxShapeLimits ?? { max: {} };
  try {
    await putSettings(demoServer, accessCode, {
      sandboxShapeLimits: { max: { cpus: 4 } },
    });
    for (const viewport of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/next/tasks");
      await page.locator("body").press("c");
      const sheet = page.getByTestId("new-task-sheet");
      await expect(sheet).toBeVisible();
      await page.getByTestId("nt-title").fill(`Shape ceiling ${Date.now()}`);
      await page.getByTestId("more-options").click();
      const ceiling = page.getByTestId("nt-shape-ceiling");
      await ceiling.scrollIntoViewIfNeeded();
      await expect(ceiling).toHaveText(
        "Up to 4 vCPU · 131072 MiB · 1024 GiB disk: this deployment's maximum.",
      );
      const cpus = page.getByTestId("nt-sandbox-cpus");
      await expect(cpus).toHaveAttribute("max", "4");
      // The ceiling sits under the three fields, aligned with the first.
      const cb = await cpus.boundingBox();
      const hb = await ceiling.boundingBox();
      expect(hb.y).toBeGreaterThan(cb.y + cb.height);
      expect(Math.abs(hb.x - cb.x)).toBeLessThanOrEqual(2);
      await within(page.getByTestId("nt-sandbox-disk"), sheet);

      await cpus.fill("8");
      await expect(page.getByTestId("nt-shape-above")).toContainText(
        "CPUs 8 (up to 4)",
      );
      await expect(cpus).toHaveAttribute("aria-invalid", "true");

      const refused = page.waitForResponse(
        (res) =>
          res.url().endsWith("/api/tasks") && res.request().method() === "POST",
      );
      await page.getByTestId("new-task-submit").click();
      expect((await refused).status()).toBe(400);
      const err = page.getByTestId("nt-shape-error");
      await expect(err).toBeVisible();
      await expect(err).toContainText(
        "sandbox shape is above this deployment's maximum",
      );
      // Said beside the fields, not in the banner at the top.
      await expect(page.getByTestId("new-task-error")).toHaveCount(0);
      await expect(cpus).toBeFocused();

      // Bringing the shape back under the ceiling clears the refusal.
      await cpus.fill("2");
      await expect(err).toHaveCount(0);
      await expect(page.getByTestId("nt-shape-above")).toHaveCount(0);
      await page.getByTestId("new-task-close").click();
      await expect(sheet).toHaveCount(0);
    }
  } finally {
    await putSettings(demoServer, accessCode, { sandboxShapeLimits: before });
  }
});

test("New task form explains the shape away on a backend whose sandboxes have none", async ({
  page,
}) => {
  test.setTimeout(60_000);
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: { ...body, sandboxShapeIgnored: true },
    });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/next/tasks");
  await page.locator("body").press("c");
  await expect(page.getByTestId("new-task-sheet")).toBeVisible();
  await page.getByTestId("more-options").click();
  const note = page.getByTestId("nt-shape-ignored");
  await note.scrollIntoViewIfNeeded();
  await expect(note).toContainText("have no shape of their own");
  await expect(page.getByTestId("nt-sandbox-cpus")).toHaveCount(0);
  await expect(page.getByTestId("nt-shape-ceiling")).toHaveCount(0);
});
