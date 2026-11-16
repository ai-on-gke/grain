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

// What the 2026-11-14 connections & infrastructure walkthrough
// (docs/test-runs/2026-11-14-connections-infrastructure-settings.md)
// left for task 5767: the Sandboxes probe that posted an empty body (F6),
// a fixed overcommit of 0 stored without a word (G8), a CI cache flush
// on one click (G7) and Rotate/Revoke offered on a revoked code (G2).
// Every test puts back what it changed.

test("Sandboxes hides the probe where the server cannot build one", async ({
  page,
}) => {
  // `grain demo` wires no prober: GET /api/settings says so.
  const settings = page.waitForResponse(
    (r) => r.url().endsWith("/api/settings") && r.request().method() === "GET",
  );
  await page.goto("/next/settings/sandboxes");
  expect((await (await settings).json()).sandboxProbe).toBe(false);
  await expect(page.getByTestId("card-sandbox-sizing")).toBeVisible();
  await expect(page.getByTestId("run-sandbox-probe")).toHaveCount(0);
});

test("Sandboxes probes with an id and argv and shows the exit code and output", async ({
  page,
}) => {
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: { ...body, sandboxProbe: true },
    });
  });
  let release;
  const held = new Promise((r) => (release = r));
  let asked;
  await page.route("**/api/sandbox-probe", async (route) => {
    asked = route.request().postDataJSON();
    await held;
    await route.fulfill({
      json: {
        sandbox: `probe-${asked.id}`,
        pod: `grain-granule-probe-${asked.id}`,
        access: "kubectl",
        stdout: "",
        stderr: "warning: slow image pull\n",
        exitCode: 0,
      },
    });
  });

  await page.goto("/next/settings/sandboxes");
  const btn = page.getByTestId("run-sandbox-probe");
  await btn.click();
  // While the sandbox boots the button cannot be pressed again.
  await expect(btn).toBeDisabled();
  await expect(page.getByTestId("sandbox-probe-running")).toBeVisible();
  release();

  const result = page.getByTestId("sandbox-probe-result");
  await expect(result).toBeVisible();
  expect(asked.argv).toEqual(["true"]);
  expect(typeof asked.id).toBe("string");
  expect(asked.id.length).toBeGreaterThan(0);
  await expect(page.getByTestId("sandbox-probe-summary")).toContainText(
    `Sandbox ready: probe-${asked.id} over kubectl exited 0`,
  );
  await expect(page.getByTestId("sandbox-probe-exit")).toHaveText("0");
  await expect(page.getByTestId("sandbox-probe-stdout")).toHaveText("(empty)");
  await expect(page.getByTestId("sandbox-probe-stderr")).toContainText(
    "slow image pull",
  );
  await expect(btn).toBeEnabled();
  await page.getByTestId("sandbox-probe-dismiss").click();
  await expect(result).toHaveCount(0);
});

test("Sandboxes reports a failing probe command as failed", async ({
  page,
}) => {
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    await route.fulfill({
      response: res,
      json: { ...(await res.json()), sandboxProbe: true },
    });
  });
  await page.route("**/api/sandbox-probe", (route) =>
    route.fulfill({
      json: {
        sandbox: "probe-x",
        pod: "p",
        access: "",
        stdout: "",
        stderr: "boom",
        exitCode: 3,
      },
    }),
  );
  await page.goto("/next/settings/sandboxes");
  await page.getByTestId("run-sandbox-probe").click();
  await expect(page.getByTestId("sandbox-probe-summary")).toContainText(
    "The probe command failed: probe-x exited 3",
  );
  await expect(page.getByTestId("sandbox-probe-result")).toHaveClass(/warn/);
});

test("Sandboxes says a fixed overcommit of 0 leaves the dimension unbounded", async ({
  page,
}) => {
  await page.goto("/next/settings/sandboxes");
  const cpu = page.locator("#sb-cpu-oc");
  await expect(cpu).toBeVisible();
  await expect(page.getByTestId("cpu-overcommit-unbounded")).toHaveCount(0);
  await cpu.fill("0");
  await expect(page.getByTestId("cpu-overcommit-unbounded")).toContainText(
    "0 leaves vCPU bounded by the run counts alone",
  );
  await page.locator("#sb-mem-oc").fill("0");
  await expect(page.getByTestId("mem-overcommit-unbounded")).toBeVisible();
  await cpu.fill("1.5");
  await expect(page.getByTestId("cpu-overcommit-unbounded")).toHaveCount(0);
  // Nothing was saved: the draft is dropped with the page.
});

test("CI: Flush CI job cache asks first", async ({ page }) => {
  let flushed = 0;
  await page.route("**/api/ci/cache", (route) => {
    if (route.request().method() !== "DELETE") return route.fallback();
    flushed++;
    return route.fulfill({ status: 204, body: "" });
  });
  await page.goto("/next/settings/ci");
  await page.getByTestId("flush-ci-cache-btn").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Flush CI job cache?");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toHaveCount(0);
  expect(flushed).toBe(0);

  await page.getByTestId("flush-ci-cache-btn").click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Flush cache" })
    .click();
  await expect.poll(() => flushed).toBe(1);
});

test("Hosts: a revoked enrollment code offers no Rotate or Revoke", async ({
  page,
  request,
}) => {
  const name = `e2e-revoked-${Date.now().toString(36)}`;
  const made = await request.post("/api/hostds/enrollment-codes", {
    data: { name },
  });
  expect(made.ok()).toBeTruthy();
  const revoked = await request.post(
    `/api/hostds/enrollment-codes/${name}/revoke`,
    { data: {} },
  );
  expect(revoked.ok()).toBeTruthy();
  const live = `${name}-live`;
  expect(
    (
      await request.post("/api/hostds/enrollment-codes", {
        data: { name: live },
      })
    ).ok(),
  ).toBeTruthy();
  try {
    await page.goto("/next/settings/hosts");
    const dead = page.getByTestId(`enrollment-code-row-${name}`);
    await expect(dead).toBeVisible();
    await expect(dead.getByTestId("enrollment-code-revoked")).toBeVisible();
    await expect(dead.getByRole("button", { name: "Rotate" })).toHaveCount(0);
    await expect(dead.getByRole("button", { name: "Revoke" })).toHaveCount(0);
    const ok = page.getByTestId(`enrollment-code-row-${live}`);
    await expect(ok.getByRole("button", { name: "Rotate" })).toBeVisible();
    await expect(ok.getByRole("button", { name: "Revoke" })).toBeVisible();
  } finally {
    await request.post(`/api/hostds/enrollment-codes/${live}/revoke`, {
      data: {},
    });
  }
});

test("Sandboxes: the probe result fits its card at every width @phone @dark", async ({
  page,
}) => {
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    await route.fulfill({
      response: res,
      json: { ...(await res.json()), sandboxProbe: true },
    });
  });
  await page.route("**/api/sandbox-probe", (route) =>
    route.fulfill({
      json: {
        sandbox: "probe-ui-mi3x9k4q-ab12",
        pod: "p",
        access: "kubectl",
        stdout: "a-long-unbroken-line-of-output-".repeat(6),
        stderr: "",
        exitCode: 0,
      },
    }),
  );
  await page.goto("/next/settings/sandboxes");
  await page.getByTestId("run-sandbox-probe").click();
  const card = await page.getByTestId("card-sandbox-sizing").boundingBox();
  const banner = page.getByTestId("sandbox-probe-result");
  await expect(banner).toBeVisible();
  for (const id of [
    "sandbox-probe-result",
    "sandbox-probe-summary",
    "sandbox-probe-stdout",
    "sandbox-probe-dismiss",
  ]) {
    const b = await page.getByTestId(id).boundingBox();
    expect(b.x, id).toBeGreaterThanOrEqual(card.x - 0.5);
    expect(b.x + b.width, id).toBeLessThanOrEqual(card.x + card.width + 0.5);
  }
  // No sideways scroll on the page itself.
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  await banner.screenshot({
    path: test.info().outputPath(`probe-result.png`),
  });
});
