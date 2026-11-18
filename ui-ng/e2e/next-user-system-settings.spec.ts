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

// Regression coverage for the 2026-11-15 Settings walkthrough (task 5890):
// legacy Settings/Admin routes and #card scrolling, '/' filter shortcut &
// Escape restore, dirty-leave confirmation on rail/section/Memory edits,
// Preferences timezone + Customize navigation cross-tab rail sync, Access
// principal confirmation & deep-link sign-out, workstation CLI commands, and
// Audit stacked phone layout (@phone @dark).

import { test, expect } from "./fixtures.js";

test.describe("Settings layout, lock/save guards, Preferences, Access, CLI & Audit", () => {
  test("resolves legacy URLs, scrolls #card fragments, focuses filter on '/', and guards unsaved edits", async ({
    page,
    axe,
  }) => {
    test.setTimeout(120_000);
    await page.setViewportSize({ width: 1280, height: 600 });

    // Legacy admin & system redirects land on their /next/settings counterparts.
    await page.goto("/next/admin");
    await expect(
      page.getByRole("heading", { level: 1, name: "General" }),
    ).toBeVisible();
    await expect(
      page.getByRole("switch", { name: "Lock settings in UI" }),
    ).toBeDisabled();

    await page.goto("/next/admin/workflow");
    await expect(
      page.getByRole("heading", { level: 1, name: "Tasks" }),
    ).toBeVisible();

    await page.goto("/next/system/executors");
    await expect(
      page.getByRole("heading", { level: 1, name: "Hosts" }),
    ).toBeVisible();

    // #usage scrolls the Token budget card into the stage viewport.
    await page.goto("/next/settings/models#usage");
    const usageCard = page.getByTestId("card-token-budget");
    await expect(usageCard).toBeVisible();
    await expect
      .poll(async () => {
        const box = await usageCard.boundingBox();
        return box ? box.y : 9999;
      })
      .toBeLessThan(260);

    // '/' focuses Filter settings, narrowing groups, and Escape restores all 20 sections.
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.getByRole("heading", { level: 1, name: "Models" }).click();
    await page.keyboard.press("/");
    const filterBox = page.getByRole("searchbox", { name: "Filter settings" });
    await expect(filterBox).toBeFocused();
    await filterBox.fill("house style");
    await expect(page.locator(".settings-nav-item")).toHaveText(["Agents"]);
    await filterBox.press("Escape");
    await expect(page.locator(".settings-nav-item")).toHaveCount(20);

    // Unsaved Memory edit prompts before switching sections or leaving via the rail.
    await page.getByTestId("settings-nav-memory").click();
    await expect(
      page.getByRole("heading", { level: 1, name: "Memory" }),
    ).toBeVisible();
    const memInput = page.getByLabel(/Deployment memory markdown/i);
    await expect(memInput).not.toHaveValue("");
    const origMemory = await memInput.inputValue();
    await memInput.fill(`${origMemory}\nTemporary unsaved walkthrough line`);
    await expect(page.getByTestId("settings-dirty-dot")).toBeVisible();

    // Cancel keeps the user on Memory with their unsaved text intact.
    await page.getByTestId("settings-nav-general").click();
    const dialog = page.getByTestId("confirm-dialog");
    await expect(dialog).toContainText("Discard unsaved changes?");
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(
      page.getByRole("heading", { level: 1, name: "Memory" }),
    ).toBeVisible();
    await expect(memInput).toHaveValue(/Temporary unsaved walkthrough line/);

    // Accept discards and allows leaving Settings via the rail.
    await page.locator('nav.nav-rail a[href*="/inbox"]').first().click();
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: "Discard" }).click();
    await expect(page).toHaveURL(/\/next\/inbox/);

    await page.goto("/next/settings/cli");
    await expect(page.getByTestId("cli-install-snippet")).toContainText(
      "/cli/install.sh | sh",
    );
    await expect(page.getByTestId("cli-login-snippet")).toContainText(
      "--issue-token --scope-set grain/external-dev --expires 30d",
    );
    expect(await axe()).toEqual([]);
  });

  test("syncs Preferences rail customization across tabs and confirms Access principal removal", async ({
    context,
    page,
    demoServer,
    accessCode,
    axe,
  }) => {
    test.setTimeout(120_000);
    await page.setViewportSize({ width: 1440, height: 900 });
    const tab2 = await context.newPage();
    try {
      await page.goto("/next/settings/preferences");
      await tab2.goto("/next/inbox");
      await expect(
        page.getByRole("heading", { level: 1, name: "Preferences" }),
      ).toBeVisible();

      // Customize button sits inside the Preferences card aligned on its row.
      const cardBox = await page.getByTestId("card-preferences").boundingBox();
      const custBtn = page.getByTestId("pref-customize-nav-btn");
      const btnBox = await custBtn.boundingBox();
      expect(cardBox).not.toBeNull();
      expect(btnBox).not.toBeNull();
      expect(btnBox!.x + btnBox!.width).toBeLessThanOrEqual(
        cardBox!.x + cardBox!.width + 1,
      );

      await custBtn.click();
      await expect(page.getByTestId("customize-nav-dialog")).toBeVisible();
      await page.getByTestId("nav-toggle-telemetry").click();
      await page.getByTestId("nav-save").click();

      await expect(
        tab2.locator('nav.nav-rail a[href="/next/telemetry"]'),
      ).toHaveCount(0);

      // Reset navigation back to default for subsequent tests.
      await custBtn.click();
      await page.getByTestId("nav-reset").click();
      await page.getByTestId("nav-save").click();
      await expect(
        tab2.locator('nav.nav-rail a[href="/next/telemetry"]'),
      ).toHaveCount(1);

      // Access: adding a principal with the default role succeeds, and Remove asks first.
      await page.goto("/next/settings/access");
      await expect(
        page.getByRole("heading", { level: 1, name: "Access" }),
      ).toBeVisible();
      const testEntry = "user:walk5890-e2e@example.com";
      await page.getByTestId("input-principal-entry").fill(testEntry);
      await page.getByTestId("add-principal-btn").click();
      await expect(
        page.getByTestId(`principal-row-${testEntry}`),
      ).toBeVisible();

      await page.getByTestId(`remove-principal-${testEntry}`).click();
      await expect(page.getByTestId("confirm-dialog")).toContainText(testEntry);
      await page.getByRole("button", { name: "Cancel" }).click();
      await expect(
        page.getByTestId(`principal-row-${testEntry}`),
      ).toBeVisible();

      await page.getByTestId(`remove-principal-${testEntry}`).click();
      await page.getByTestId("confirm-accept").click();
      await expect(page.getByTestId(`principal-row-${testEntry}`)).toHaveCount(
        0,
      );
      expect(await axe()).toEqual([]);
    } finally {
      await tab2.close();
      await fetch(
        `${demoServer}/api/access/principals/${encodeURIComponent("user:walk5890-e2e@example.com")}`,
        {
          method: "DELETE",
          headers: { Authorization: `Bearer ${accessCode}` },
        },
      ).catch(() => undefined);
    }
  });

  test("stacks Audit rows cleanly at phone width with no horizontal overflow or axe violations @phone @dark", async ({
    page,
    axe,
  }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/next/settings/audit");
    await expect(
      page.getByRole("heading", { level: 1, name: "Audit" }),
    ).toBeVisible();
    await expect(page.getByTestId("settings-save-status")).toHaveCount(0);

    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
    expect(await axe()).toEqual([]);
  });

  test("scopes Settings -> Models quota Resume to the named pause and confirms multi-target lifts @phone @dark", async ({
    page,
    axe,
  }) => {
    const deleteQueries: string[] = [];
    let pauses = [
      {
        connection: "gemini-pro",
        connectionName: "Gemini 2.5 Pro",
        model: "gemini-2.5-pro",
        team: "default",
        until: new Date(Date.now() + 3_600_000).toISOString(),
        secondsRemaining: 3600,
        reason: "HTTP 429 quota exceeded",
        canLift: true,
      },
      {
        team: "default",
        framework: "codex",
        until: new Date(Date.now() + 7_200_000).toISOString(),
        secondsRemaining: 7200,
        reason: "Paused by operator for known-bad CLI release",
        canLift: true,
      },
    ];

    const currentAgentPause = () => ({
      paused: pauses.length > 0,
      until: pauses[pauses.length - 1]?.until,
      reason: pauses[pauses.length - 1]?.reason,
      pauses,
    });

    await page.route("**/api/config", async (route) => {
      try {
        const res = await route.fetch();
        const body = await res.json();
        await route.fulfill({
          response: res,
          json: {
            ...body,
            agentPause: currentAgentPause(),
          },
        });
      } catch {
        // Ignore in-flight poll disposed during page teardown.
      }
    });

    await page.route(/\/api\/pause(\?.*)?$/, async (route) => {
      try {
        if (route.request().method() === "DELETE") {
          const url = new URL(route.request().url());
          deleteQueries.push(url.searchParams.toString());
          const conn = url.searchParams.get("connection") ?? "";
          const model = url.searchParams.get("model") ?? "";
          pauses = pauses.filter((p) => {
            if (!conn) return false;
            if (p.connection !== conn) return true;
            if (
              model &&
              (p.model ?? "").toLowerCase() !== model.toLowerCase()
            ) {
              return true;
            }
            return false;
          });
          await route.fulfill({
            json: { enabled: true, pause: currentAgentPause(), lifted: true },
          });
          return;
        }
        await route.fulfill({
          json: { enabled: true, pause: currentAgentPause() },
        });
      } catch {
        // Ignore teardown disposal.
      }
    });

    try {
      await page.goto("/next/settings/models");
      const banner = page.getByTestId("models-quota-banner");
      await expect(banner).toBeVisible();
      await expect(banner).toContainText("Gemini 2.5 Pro · gemini-2.5-pro");
      await expect(banner).not.toContainText("known-bad CLI release");

      const resumeBtn = banner.getByRole("button", { name: "Resume" });
      await expect(resumeBtn).toBeVisible();
      const bannerBox = (await banner.boundingBox())!;
      const btnBox = (await resumeBtn.boundingBox())!;
      expect(btnBox.x).toBeGreaterThan(bannerBox.x + bannerBox.width / 2);
      expect(btnBox.x + btnBox.width).toBeLessThanOrEqual(
        bannerBox.x + bannerBox.width + 1,
      );
      expect(btnBox.y).toBeGreaterThanOrEqual(bannerBox.y - 1);
      expect(btnBox.y + btnBox.height).toBeLessThanOrEqual(
        bannerBox.y + bannerBox.height + 1,
      );
      expect(await axe()).toEqual([]);

      await resumeBtn.click();
      await expect(banner).toHaveCount(0);
      expect(deleteQueries).toEqual([
        "connection=gemini-pro&model=gemini-2.5-pro",
      ]);
      // The operator-set framework pause was preserved.
      expect(pauses).toHaveLength(1);
      expect(pauses[0].framework).toBe("codex");

      // Multi-target quota pause asks via g-confirm-dialog before lifting each scoped pause.
      deleteQueries.length = 0;
      pauses = [
        {
          connection: "gemini-pro",
          connectionName: "Gemini 2.5 Pro",
          model: "gemini-2.5-pro",
          team: "default",
          until: new Date(Date.now() + 3_600_000).toISOString(),
          secondsRemaining: 3600,
          reason: "HTTP 429 quota exceeded",
          canLift: true,
        },
        {
          connection: "claude-team",
          connectionName: "Claude Team",
          team: "default",
          until: new Date(Date.now() + 3_600_000).toISOString(),
          secondsRemaining: 3600,
          reason: "Rate limit exceeded",
          canLift: true,
        },
        {
          team: "default",
          framework: "codex",
          until: new Date(Date.now() + 7_200_000).toISOString(),
          secondsRemaining: 7200,
          reason: "Paused by operator for known-bad CLI release",
          canLift: true,
        },
      ];

      await page.reload();
      await expect(banner).toBeVisible();
      await banner.getByRole("button", { name: "Resume" }).click();

      const dialog = page.getByRole("dialog");
      await expect(dialog).toBeVisible();
      await expect(dialog).toContainText("Claude Team");
      await expect(dialog).toContainText("Gemini 2.5 Pro · gemini-2.5-pro");
      expect(deleteQueries).toEqual([]);

      await dialog.getByRole("button", { name: "Resume" }).click();
      await expect(banner).toHaveCount(0);
      expect(deleteQueries).toEqual([
        "connection=claude-team",
        "connection=gemini-pro&model=gemini-2.5-pro",
      ]);
      expect(pauses).toHaveLength(1);
      expect(pauses[0].framework).toBe("codex");
    } finally {
      await page.unrouteAll({ behavior: "ignoreErrors" });
    }
  });
});
