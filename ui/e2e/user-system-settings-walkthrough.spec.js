import { test, expect } from "./fixtures.js";

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
      await zone.selectOption("Asia/Tokyo");
      await page.goto("/inbox");
      const row = page.locator("section.inbox-group", {
        hasText: "grain/config",
      });
      await expect(row.locator(".inbox-note")).toContainText(inTokyo);

      await page.goto("/settings/preferences");
      await zone.selectOption("UTC");
      await page.goto("/inbox");
      await expect(row.locator(".inbox-note")).toContainText(inUTC);
    } finally {
      await page.goto("/settings/preferences");
      await zone.selectOption("");
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
});
