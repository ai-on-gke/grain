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
import { chooseOption } from "./searchSelect.js";

// Regressions from the 2026-10-23 user and system settings walkthrough
// (docs/test-runs/2026-10-23-user-system-settings.md), driven through the
// real `grain demo` server and built frontend. Each test puts back
// whatever it changed on the worker's shared store.

test.describe("user and system settings walkthrough regressions", () => {
  // Settings → Preferences' time zone was stored and shown in its own
  // picker, but nothing else read it: every time stayed on the
  // deployment's clock. The config-repo sync failure is the one time the
  // demo can be made to print deterministically, so /api/config gains a
  // failure on its way past -- the demo has no state repository to fail.
  test("the browser's chosen time zone reaches the times a page prints", async ({
    page,
  }) => {
    const failure = {
      repo: "grain/config",
      since: "2026-10-23T09:00:00Z",
      error: "push rejected (stubbed)",
    };
    await page.route(/\/api\/config(\?.*)?$/, async (route) => {
      const res = await route.fetch();
      const json = await res.json();
      await route.fulfill({
        response: res,
        json: { ...json, stateRepoSyncFailure: failure, timeZone: "" },
      });
    });
    const inTokyo = await page.evaluate(
      (since) =>
        new Date(since).toLocaleString(undefined, { timeZone: "Asia/Tokyo" }),
      failure.since,
    );
    const inUTC = await page.evaluate(
      (since) => new Date(since).toLocaleString(undefined, { timeZone: "UTC" }),
      failure.since,
    );
    expect(inTokyo).not.toBe(inUTC);

    await page.goto("/settings/preferences");
    const zone = page.getByRole("combobox", { name: "Time zone" });
    try {
      // Open and verify SearchSelect layout geometry and mid-word filtering.
      await zone.click();
      await zone.fill("tokyo");
      const listbox = page.getByRole("listbox");
      await expect(listbox).toBeVisible();
      const fieldBox = await zone
        .locator(
          "xpath=ancestor::div[contains(@class,'MuiOutlinedInput-root')]",
        )
        .boundingBox();
      const listBox = await listbox.boundingBox();
      expect(listBox.y).toBeGreaterThanOrEqual(
        fieldBox.y + fieldBox.height - 2,
      );
      expect(listBox.width).toBeGreaterThanOrEqual(fieldBox.width - 2);
      await chooseOption(zone, "Asia/Tokyo");
      await page.goto("/inbox");
      const row = page.locator("section.inbox-group", {
        hasText: "grain/config",
      });
      await expect(row.locator(".inbox-note")).toContainText(inTokyo);

      await page.goto("/settings/preferences");
      await chooseOption(zone, "UTC");
      await page.goto("/inbox");
      await expect(row.locator(".inbox-note")).toContainText(inUTC);
    } finally {
      await page.goto("/settings/preferences");
      await chooseOption(zone, "");
    }
  });

  // The deployment-wide ignoreGitHubCI was removed from the server, so the
  // box saved nothing and read back unticked after a reload.
  test("Tasks offers no deployment-wide GitHub-CI box that cannot save", async ({
    page,
  }) => {
    await page.goto("/settings/tasks");
    await expect(page.getByTestId("ci-gate-per-repo")).toBeVisible();
    await expect(page.locator("input[name=ignoreGitHubCI]")).toHaveCount(0);
  });

  // A refused save named the API key ("maxAutoSubmitDepth") rather than
  // the box's label, and out-of-range values went to the server.
  test("a refused save names the box by its label", async ({ page }) => {
    // Another spec on the same worker (settings-panel-walkthrough) may
    // have saved a non-default depth; start at the default (2) so clearing
    // the box below is a no-op save.
    await page.request.put("/api/settings", {
      data: { maxAutoSubmitDepth: 2 },
    });
    await page.goto("/settings/tasks");
    const depth = page.locator("input[name=maxAutoSubmitDepth]");
    if ((await depth.inputValue()) !== "2") {
      await depth.fill("2");
      await depth.press("Tab");
      await expect(page.getByTestId("settings-status")).toHaveText(
        "All changes saved",
      );
    }
    const before = await depth.inputValue();
    const puts = [];
    page.on("request", (r) => {
      if (r.url().endsWith("/api/settings") && r.method() === "PUT") {
        puts.push(r.postData());
      }
    });
    await depth.fill("-1");
    await depth.press("Tab");
    await expect(page.getByTestId("settings-section")).toContainText(
      "Couldn't save Max unreviewed follow-up depth",
    );
    await expect(
      page.getByText(
        "Max unreviewed follow-up depth must be at least 1; -1 was not saved.",
      ),
    ).toBeVisible();
    await expect(depth).toHaveValue(before);
    expect(puts).toEqual([]);

    // Clearing the box resets to the default (2) rather than sending 0.
    await depth.fill("");
    await depth.press("Tab");
    await expect(depth).toHaveValue("2");
    expect(puts).toEqual([]);

    await page.goto("/settings/engine");
    const workers = page.locator("input[name=maxWorkers]");
    const workersBefore = await workers.inputValue();
    await workers.fill("-1");
    await workers.press("Tab");
    await expect(page.getByTestId("settings-section")).toContainText(
      "Couldn't save Max worker agents",
    );
    await expect(
      page.getByText("Max worker agents must be at least 1; -1 was not saved."),
    ).toBeVisible();
    await expect(workers).toHaveValue(workersBefore);
    expect(puts).toEqual([]);
  });

  // Remove on a git or provider connection deleted its credential on one
  // click; it now asks, and declining keeps the connection.
  test("removing a connection asks first, and declining keeps it", async ({
    page,
  }) => {
    const prompts = [];
    page.on("dialog", (dialog) => {
      prompts.push(dialog.message());
      dialog.dismiss();
    });

    await page.goto("/settings/git");
    const section = page.getByTestId("settings-section");
    const row = section.locator("tr", { hasText: "github.com" }).first();
    await expect(row).toBeVisible();
    await row.getByRole("button", { name: "Remove" }).click();
    await expect.poll(() => prompts.length).toBe(1);
    expect(prompts[0]).toContain("Remove the github.com connection");
    await page.reload();
    await expect(
      section.locator("tr", { hasText: "github.com" }).first(),
    ).toBeVisible();

    await page.goto("/settings/models");
    await section.getByRole("button", { name: "Remove" }).first().click();
    await expect.poll(() => prompts.length).toBe(2);
    expect(prompts[1]).toMatch(/^Remove the connection /);
    await page.reload();
    await expect(
      section.getByRole("button", { name: "Remove" }).first(),
    ).toBeVisible();
  });

  // Settings → Access formatted PrincipalTable's Added column in UTC
  // regardless of Settings → Preferences' time zone (2026-11-08 settings
  // walkthrough).
  test("Settings → Access prints principal Added times in this browser's chosen time zone", async ({
    page,
  }) => {
    const res = await page.request.get("/api/access/principals");
    expect(res.ok()).toBeTruthy();
    const { entries } = await res.json();
    const first = entries.find((e) => e.addedAt);
    expect(first).toBeTruthy();

    const inTokyo = await page.evaluate(
      (iso) =>
        new Date(iso).toLocaleString(undefined, { timeZone: "Asia/Tokyo" }),
      first.addedAt,
    );
    const inLA = await page.evaluate(
      (iso) =>
        new Date(iso).toLocaleString(undefined, {
          timeZone: "America/Los_Angeles",
        }),
      first.addedAt,
    );
    expect(inTokyo).not.toBe(inLA);

    await page.goto("/settings/preferences");
    const zone = page.getByRole("combobox", { name: "Time zone" });
    try {
      await chooseOption(zone, "Asia/Tokyo");
      await page.goto("/settings/access");
      const row = page.getByTestId(`principal-row-${first.entry}`);
      await expect(row).toContainText(inTokyo);

      await page.goto("/settings/preferences");
      await chooseOption(zone, "America/Los_Angeles");
      await page.goto("/settings/access");
      await expect(row).toContainText(inLA);
    } finally {
      await page.goto("/settings/preferences");
      await chooseOption(zone, "");
    }
  });

  // At a tablet width the nav is the Section select; cancelling "Discard
  // unsaved changes?" kept the page on Agents but left "General" in the
  // box (2026-11-08 settings walkthrough, task 3615).
  test("the tablet Section select keeps the current section when a dirty leave is cancelled", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 800, height: 1000 });
    await page.goto("/settings/agents");
    const promptBox = page.getByLabel("Standing instructions for every run");
    await expect(promptBox).toBeVisible();
    await promptBox.fill("unsaved walkthrough edit");
    await expect(page.getByTestId("settings-status")).toContainText(
      "Unsaved changes in Standing instructions",
    );

    page.once("dialog", (dialog) => dialog.dismiss());
    const picker = page.getByRole("combobox", { name: "Section" });
    await expect(picker).toHaveValue("Agents");
    await chooseOption(picker, "General");

    await expect(page).toHaveURL(/\/settings\/agents$/);
    await expect(
      page.getByRole("heading", { name: "Agents", exact: true }),
    ).toBeVisible();
    await expect(picker).toHaveValue("Agents");
    await page.getByTestId("promptExtension-discard").click();
  });

  // The retired Settings → Legacy capabilities tab was still what Tasks'
  // empty Default legacy capabilities menu pointed at; that always-empty
  // menu is gone, and default grants are GrantsPicker's alone.
  test("Tasks draws no Default legacy capabilities menu", async ({ page }) => {
    await page.goto("/settings/tasks");
    await expect(page.getByTestId("default-grants")).toBeVisible();
    await expect(
      page.getByRole("combobox", { name: "Default legacy capabilities" }),
    ).toHaveCount(0);
    await expect(page.getByText(/Settings → Legacy capabilities/i)).toHaveCount(
      0,
    );
  });

  // Agents → Commit identity and CLI → Sign in reloaded the whole app and
  // skipped the unsaved-changes prompt.
  test("Agents' Git link and CLI's Agents link open in place and guard unsaved edits", async ({
    page,
  }) => {
    await page.goto("/settings/agents");
    await page.evaluate(() => {
      window.__settingsSpaAlive = true;
    });
    const promptBox = page.getByLabel("Standing instructions for every run");
    await promptBox.fill("unsaved standing instruction");
    await expect(page.getByTestId("settings-status")).toContainText(
      "Unsaved changes in Standing instructions",
    );
    const gitLink = page
      .getByTestId("commit-identity-pointer")
      .getByRole("link", { name: /Settings → Git/ });

    page.once("dialog", (dialog) => dialog.dismiss());
    await gitLink.click();
    await expect(page).toHaveURL(/\/settings\/agents$/);
    await expect(promptBox).toHaveValue("unsaved standing instruction");

    page.once("dialog", (dialog) => dialog.accept());
    await gitLink.click();
    await expect(page).toHaveURL(/\/settings\/git$/);
    await expect(
      page.getByRole("heading", { name: "Git", exact: true }),
    ).toBeVisible();
    expect(await page.evaluate(() => window.__settingsSpaAlive)).toBe(true);

    await page.getByTestId("settings-nav-cli").click();
    await expect(page).toHaveURL(/\/settings\/cli$/);
    await page.getByTestId("cli-external-agent-api-link").click();
    await expect(page).toHaveURL(/\/settings\/agents/);
    await expect(
      page.getByRole("heading", { name: "Agents", exact: true }),
    ).toBeVisible();
    expect(await page.evaluate(() => window.__settingsSpaAlive)).toBe(true);
  });

  // Clicking Continue in Settings → Models → New connection unmounts the
  // Continue button and drops document.activeElement to <body>, where MUI's
  // Dialog did not hear Escape (docs/test-runs/2026-11-08-user-system-settings.md).
  test("pressing Escape right after Continue on New connection closes and cleans up the draft", async ({
    page,
    request,
  }) => {
    const before = await (await request.get("/api/settings")).json();
    const beforeIds = (before.agentBackends || []).map((b) => b.id);

    const prompts = [];
    page.on("dialog", (dialog) => {
      prompts.push(dialog.message());
      dialog.accept();
    });

    await page.goto("/settings/models");
    await page.getByRole("button", { name: "New connection" }).click();
    const overlay = page.getByRole("dialog", { name: "New connection" });
    await expect(overlay).toBeVisible();

    await overlay.getByLabel("Agent type").click();
    await page.getByRole("option", { name: "Claude" }).click();
    await overlay.getByRole("button", { name: "Continue" }).click();

    const editedOverlay = page.getByRole("dialog", { name: "Claude" });
    await expect(
      editedOverlay.getByRole("heading", { name: "Authentication" }),
    ).toBeVisible();

    await page.keyboard.press("Escape");
    await expect.poll(() => prompts.length).toBe(1);
    expect(prompts[0]).toContain("Leave and discard");
    expect(prompts[0]).not.toMatch(/^Remove the connection /);
    await expect(editedOverlay).toHaveCount(0);

    await expect
      .poll(async () => {
        const after = await (await request.get("/api/settings")).json();
        return (after.agentBackends || []).map((b) => b.id);
      })
      .toEqual(beforeIds);
  });

  // 2026-11-09 walkthrough: SettingsPage.pick() rewrote window.location via
  // replaceState before App's URL sync effect ran, making App see samePage
  // and overwrite the previous section instead of pushing history.
  test("cross-section filter navigation pushes browser history on desktop and phone", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/");
    await page.getByTestId("top-bar-settings").click();
    await expect(page).toHaveURL(/\/settings\/models$/);

    await page.keyboard.press("/");
    const filter = page.getByRole("textbox", { name: "Filter settings" });
    await expect(filter).toBeFocused();
    await filter.fill("house style");
    await page.getByTestId("settings-result").first().click();
    await expect(page).toHaveURL(/\/settings\/agents#promptExtension$/);

    await page.goBack();
    await expect(page).toHaveURL(/\/settings\/models$/);

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/settings");
    await expect(page.getByTestId("settings-nav")).toBeVisible();
    await page.getByRole("textbox", { name: "Filter settings" }).fill("turns");
    await page.getByTestId("settings-result").first().click();
    await expect(page).toHaveURL(/\/settings\/agents#maxAgentTurns$/);

    await page.goBack();
    await expect(page).toHaveURL(/\/settings$/);
    await expect(page.getByTestId("settings-nav")).toBeVisible();
  });

  // 2026-11-09 walkthrough: filtering for a conditionally hidden field
  // (autoUpgradeTime while autoUpgradeOn is "off") left its parent card
  // (#updates) below the fold with no focus and no highlight.
  test("filtering for a conditionally hidden setting scrolls and highlights its parent card", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 500 });
    await page.goto("/settings/models");
    await page.keyboard.press("/");
    await page
      .getByRole("textbox", { name: "Filter settings" })
      .fill("Automatic upgrade time of day");
    await page.getByTestId("settings-result").first().click();
    await expect(page).toHaveURL(/\/settings\/general#autoUpgradeTime$/);

    const updates = page.locator("#updates");
    await expect(updates).toHaveClass(/setting-highlight/);
    await expect
      .poll(async () => {
        const box = await updates.boundingBox();
        return Boolean(box && box.y < 500 && box.y + box.height > 0);
      })
      .toBe(true);
  });

  // 2026-11-09 walkthrough: while viewing a section's item subpage
  // (/settings/capabilities/<server>), clicking Capabilities in the section
  // nav or picking a same-section filter match did not clear item.
  test("clicking the active section nav or a same-section filter match exits an open capability server subpage", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/settings/capabilities");
    const serverLink = page
      .getByText(/capabilities\.demo\.grain\.example\/cloud/i)
      .first();
    await expect(serverLink).toBeVisible();
    await serverLink.click();
    await expect(page).toHaveURL(
      /\/settings\/capabilities\/https%3A%2F%2Fcapabilities\.demo\.grain\.example%2Fcloud$/,
    );
    await expect(page.getByTestId("capability-servers-back")).toBeVisible();

    await page.getByTestId("settings-nav-capabilities").click();
    await expect(page).toHaveURL(/\/settings\/capabilities$/);
    await expect(page.getByTestId("capability-servers-back")).toHaveCount(0);
    await expect(
      page.getByRole("list", { name: "Capability servers" }),
    ).toBeVisible();

    await serverLink.click();
    await expect(page).toHaveURL(
      /\/settings\/capabilities\/https%3A%2F%2Fcapabilities\.demo\.grain\.example%2Fcloud$/,
    );
    await expect(page.getByTestId("capability-servers-back")).toBeVisible();
    await page.keyboard.press("/");
    await page
      .getByRole("textbox", { name: "Filter settings" })
      .fill("GitHub host");
    await page.getByTestId("settings-result").first().click();
    await expect(page).toHaveURL(/\/settings\/capabilities#githubHost$/);
    await expect(page.getByTestId("capability-servers-back")).toHaveCount(0);
    await expect(page.locator("input[name=githubHost]")).toBeFocused();
  });
});
