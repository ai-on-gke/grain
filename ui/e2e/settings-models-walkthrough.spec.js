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

// Regressions from the 2026-10-29 Settings walkthrough
// (docs/test-runs/2026-10-29-user-system-settings.md), on Settings →
// Models, driven through the real `grain demo` server and built frontend.
//
// -agent-paused begins a real usage-limit pause on demo's one connection
// (model-source-pause-walkthrough.spec.js), which the last test lifts:
// this file's worker has a demo of its own, as demoArgs differ.
test.use({ demoArgs: ["-agent-paused"] });

test.describe("Settings → Models walkthrough regressions", () => {
  // A new connection left by a reload, rather than by ← Models, stayed
  // behind as a nameless, disabled row named by its id.
  test("a new connection left unfinished by a reload is deleted", async ({
    page,
  }) => {
    page.on("dialog", (dialog) => dialog.accept());
    await page.goto("/settings/models");
    const section = page.getByTestId("settings-section");
    const remove = section.getByRole("button", { name: "Remove" });
    await expect(remove).toHaveCount(1);

    await section.getByRole("button", { name: "New connection" }).click();
    await page
      .locator(".overlay-pane")
      .getByLabel(/Agent type/i)
      .click();
    await page.getByRole("option", { name: "Antigravity" }).click();
    const stored = page.waitForResponse(
      (r) =>
        r.request().method() === "PUT" &&
        r.url().includes("/agent-backends/backend-"),
    );
    await page.getByRole("button", { name: "Continue" }).click();
    await stored;

    // The DELETE is sent as the page goes away (keepalive), so it is
    // the server that is asked, not the page that is gone.
    await page.reload();
    await expect
      .poll(
        async () =>
          (await (await page.request.get("/api/settings")).json()).agentBackends
            .length,
      )
      .toBe(1);
    await expect(remove).toHaveCount(1);
    await page.reload();
    await expect(remove).toHaveCount(1);
  });

  // With nothing listed by the provider, "Advertise all models" replaced
  // the pinned list with none.
  test("Advertise all models waits for the provider to list some", async ({
    page,
  }) => {
    await page.goto("/settings/models");
    const section = page.getByTestId("settings-section");
    await section.getByRole("button", { name: "Edit" }).first().click();
    const pane = page.locator(".overlay-pane");
    await expect(
      pane.getByText("cannot ask this provider for its models", {
        exact: false,
      }),
    ).toBeVisible();
    await expect(
      pane.getByRole("button", { name: "Advertise all models" }),
    ).toBeDisabled();
    await expect(pane.getByTestId("advertise-all-unavailable")).toBeVisible();
  });

  // A whole connection's pause could be lifted only from the alerts
  // panel; Models offered Resume for a paused model alone.
  test("a paused connection resumes from its row", async ({ page }) => {
    await page.goto("/settings/models");
    const section = page.getByTestId("settings-section");
    await expect(page.locator(".connection-paused")).toHaveCount(1);
    // Beside the pause's sentence, or -- when the sentence takes the row
    // -- under it, flush with its left edge rather than indented by the
    // spacing a first item never gets.
    const resume = section.getByRole("button", { name: "Resume" });
    const said = await page.locator(".connection-paused").boundingBox();
    const button = await resume.boundingBox();
    if (button.y >= said.y + said.height - 1) {
      expect(Math.abs(button.x - said.x)).toBeLessThanOrEqual(1);
    } else {
      expect(button.x).toBeGreaterThanOrEqual(said.x + said.width);
    }
    await resume.click();
    await expect(page.locator(".connection-paused")).toHaveCount(0);
    await page.reload();
    await expect(section.getByText("Active", { exact: true })).toBeVisible();
    await expect(page.locator(".connection-paused")).toHaveCount(0);
    const config = await (await page.request.get("/api/config")).json();
    expect(config.agentPause?.paused ?? false).toBe(false);
  });
});
