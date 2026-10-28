import { expect, test } from "./fixtures.js";

// The System-rename walkthrough of 2026-10-12
// (docs/test-runs/2026-10-12-system-rename-off-team-boards.md): the
// Admin section is "System" now, /admin stays its URL and /system opens
// the same pane. Three things the sweep found still saying otherwise,
// run against the real `grain demo` server.

// A demo of its own: the member below joins a team, which specs sharing a
// demo do not expect.
test.use({ demoArgs: ["-default-target-repo=acme/rename"] });

test.describe("as a system admin", () => {
  // A bare /system once opened on Logs -- from when /system was the old
  // Debug pane. System is status and operations only now
  // (docs/design/settings-panel.md §2.1), so it opens on Health, and a
  // /system link naming a settings tab goes where /admin/<tab> does.
  test("a bare /system opens on Health, and /admin on Settings → General", async ({
    page,
  }) => {
    await page.goto("/admin");
    await expect(
      page.getByRole("heading", { name: "General", level: 1 }),
    ).toBeVisible();
    await expect(page).toHaveURL(/\/settings\/general$/);

    await page.goto("/system");
    await expect(
      page
        .getByRole("tablist", { name: "System tabs" })
        .getByRole("tab", { name: "Health" }),
    ).toHaveAttribute("aria-selected", "true");
    await expect(page).toHaveURL(/\/system$/);

    await page.goto("/system/engine");
    await expect(
      page.getByRole("heading", { name: "Engine", level: 1 }),
    ).toBeVisible();
    await expect(page).toHaveURL(/\/settings\/engine$/);
  });

  // Whoever holds the access code is the owner, so Access lists no
  // system administrators to be one of.
  test("Settings, Access names no system administrators", async ({ page }) => {
    await page.goto("/settings/access");
    await expect(
      page.getByRole("heading", { name: "This browser" }),
    ).toBeVisible();
    await expect(page.getByText(/system administrator/i)).toHaveCount(0);
  });

  // The icon picker offered the section's figure twice, as "Admin (Core
  // case)" and "System (Core case)".
  test("the pinned-item icon picker offers the System figure once, by its new name", async ({
    page,
  }) => {
    await page.goto("/");
    await page.getByRole("button", { name: "Customize sidebar" }).click();
    await page.getByRole("combobox", { name: "Icon (optional)" }).click();
    const listbox = page.getByRole("listbox");
    await expect(
      listbox.getByRole("option", { name: "System (Core case)" }),
    ).toHaveCount(1);
    await expect(listbox.getByRole("option", { name: /admin/i })).toHaveCount(
      0,
    );
  });
});

// Settings and System show every control (docs/design/settings-panel.md
// assumes #4880's removal of the admin gates), and a browser names nobody
// (§6): a fresh browser gets the same destinations from the same links.
test.describe("from a fresh browser", () => {
  test("every /admin and /system link lands where it points", async ({
    browser,
    baseURL,
  }) => {
    const context = await browser.newContext({ baseURL });
    const page = await context.newPage();
    for (const [path, now] of [
      ["/admin", "/settings/general"],
      ["/system", "/system"],
      ["/system/engine", "/settings/engine"],
      ["/admin/sandboxes", "/system/sandboxes"],
      ["/system/caches", "/system/caches"],
    ]) {
      await page.goto(path);
      await expect
        .poll(() => new URL(page.url()).pathname, { message: path })
        .toBe(now);
    }
    await context.close();
  });
});
