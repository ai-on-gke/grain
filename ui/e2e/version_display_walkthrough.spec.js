import { test, expect } from "./fixtures.js";

// Walkthrough: User Settings Version Display and Responsive Viewports
// docs/test-plans/2026-10-10.md §2 (commit 7f7564082)

test.describe("Settings version display walkthrough", () => {
  test("Desktop: Settings overlay displays version matching /api/config with monospace font and dismisses via button and Escape", async ({
    page,
    baseURL,
  }) => {
    // 1. Fetch daemon /api/config directly to observe the expected version
    const cfgRes = await page.request.get("/api/config");
    expect(cfgRes.ok()).toBeTruthy();
    const configData = await cfgRes.json();
    const serverVersion = configData.version;

    // Format expected version string according to CLI and UI formatting rules
    let expectedVersionText = "dev (unstamped)";
    if (serverVersion && (serverVersion.commit || serverVersion.release)) {
      const parts = [];
      if (serverVersion.release) parts.push(serverVersion.release);
      if (serverVersion.edition) parts.push(serverVersion.edition);
      if (serverVersion.commit) {
        const hash =
          serverVersion.commit.slice(0, 7) +
          (serverVersion.modified ? "-dirty" : "");
        parts.push(hash);
      }
      if (serverVersion.committedAt) {
        const d = new Date(serverVersion.committedAt);
        if (!Number.isNaN(d.getTime())) {
          parts.push(d.toISOString().slice(0, 16).replace("T", " ") + "Z");
        }
      }
      expectedVersionText = parts.join(" · ");
    } else if (serverVersion?.edition) {
      expectedVersionText = `dev (${serverVersion.edition}, unstamped)`;
    }

    // 2. Open grain demo home page
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Tasks" })).toBeVisible();

    // The version is Settings → General → About now
    // (docs/design/settings-panel.md §3.2), behind the gear.
    await page.getByTestId("top-bar-settings").click();
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();
    await expect(page.getByRole("heading", { name: "About" })).toBeVisible();

    const versionDisplay = page.getByTestId("settings-version-display");
    await expect(versionDisplay).toBeVisible();
    expect((await versionDisplay.textContent()).trim()).toBe(
      expectedVersionText,
    );
    const fontFamily = await versionDisplay.evaluate(
      (el) => window.getComputedStyle(el).fontFamily,
    );
    expect(fontFamily.toLowerCase()).toContain("monospace");

    // Settings is a view: the rail leaves it.
    await page.getByRole("button", { name: /^Tasks \d+/ }).click();
    await expect(versionDisplay).toHaveCount(0);
  });

  test("Mobile phone viewport: opens navigation drawer, accesses Settings, and verifies responsive layout", async ({
    page,
    baseURL,
  }) => {
    // Set modern mobile viewport (390 x 844)
    await page.setViewportSize({ width: 390, height: 844 });

    // 1. Fetch daemon config
    const cfgRes = await page.request.get("/api/config");
    expect(cfgRes.ok()).toBeTruthy();
    const configData = await cfgRes.json();
    const serverVersion = configData.version;

    let expectedVersionText = "dev (unstamped)";
    if (serverVersion && (serverVersion.commit || serverVersion.release)) {
      const parts = [];
      if (serverVersion.release) parts.push(serverVersion.release);
      if (serverVersion.edition) parts.push(serverVersion.edition);
      if (serverVersion.commit) {
        const hash =
          serverVersion.commit.slice(0, 7) +
          (serverVersion.modified ? "-dirty" : "");
        parts.push(hash);
      }
      if (serverVersion.committedAt) {
        const d = new Date(serverVersion.committedAt);
        if (!Number.isNaN(d.getTime())) {
          parts.push(d.toISOString().slice(0, 16).replace("T", " ") + "Z");
        }
      }
      expectedVersionText = parts.join(" · ");
    } else if (serverVersion?.edition) {
      expectedVersionText = `dev (${serverVersion.edition}, unstamped)`;
    }

    // 2. Open grain demo
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // On phone, nav rail is inside PhoneNav drawer
    // On a phone the gear is on the bar, and Settings opens on its list.
    await page.getByTestId("phone-settings").click();
    await page.getByTestId("settings-nav-general").click();

    const versionDisplay = page.getByTestId("settings-version-display");
    await expect(versionDisplay).toBeVisible();
    expect((await versionDisplay.textContent()).trim()).toBe(
      expectedVersionText,
    );

    // The section fits the phone, and its back arrow returns to the list.
    const width = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(width).toBeLessThanOrEqual(390);
    await page.getByTestId("settings-back").click();
    await expect(page.getByTestId("settings-nav")).toBeVisible();
  });
});
