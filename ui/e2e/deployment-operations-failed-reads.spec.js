import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-observability"] });

// Each of these panes once met a failed first read by spinning for as
// long as it was open (Caches, the CI run list) or by rendering nothing
// at all once the snackbar faded (Top, Config repo, All Teams) -- see
// docs/test-runs/2026-10-15-deployment-operations.md. The real demo
// server answers every other request; only the one read each test is
// about is failed, in the browser, and then let through again.

// failRead answers GETs matching pattern with a 500 until the returned
// function is called, after which they reach the demo server again.
async function failRead(page, pattern, message) {
  let fail = true;
  await page.route(pattern, async (route) => {
    if (!fail || route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: message }),
    });
  });
  return () => {
    fail = false;
  };
}

test("Caches says its read failed instead of spinning, and recovers", async ({
  page,
}) => {
  const allow = await failRead(page, "**/api/package-cache", "stats down");
  await page.goto("/system/caches");
  await expect(
    page.getByText("The caches could not be read (stats down)."),
  ).toBeVisible();
  await expect(page.getByLabel("Loading caches")).toHaveCount(0);

  allow();
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect(page.getByText(/could not be read/)).toHaveCount(0);
  await expect(
    page.getByRole("heading", { name: "Proxy cache" }),
  ).toBeVisible();
});

test("the CI run list says its read failed instead of spinning", async ({
  page,
}) => {
  const allow = await failRead(page, "**/api/ci/runs?*", "runs table locked");
  await page.goto("/ci");
  await expect(
    page.getByText("CI runs could not be listed (runs table locked)."),
  ).toBeVisible();
  await expect(page.getByLabel("Loading CI runs")).toHaveCount(0);

  // The pane polls on its own; the next poll that succeeds clears it.
  allow();
  await expect(page.getByText(/could not be listed/)).toHaveCount(0, {
    timeout: 15_000,
  });
  await expect(
    page.getByText("active sandboxes", { exact: false }),
  ).toBeVisible();
});

test("Top says its read failed, and Refresh brings the table back", async ({
  page,
}) => {
  const allow = await failRead(page, "**/api/host/top*", "procps missing");
  await page.goto("/system/top");
  await expect(
    page.getByText("The process snapshot could not be read (procps missing)."),
  ).toBeVisible();

  allow();
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect(page.getByText(/grain-daemon/)).toBeVisible();
  await expect(page.getByText(/could not be read/)).toHaveCount(0);
});

test("Config repo says its read failed, and Try again reads it", async ({
  page,
}) => {
  const allow = await failRead(page, "**/api/state-repo", "store locked");
  await page.goto("/settings/config-repo");
  await expect(
    page.getByText(
      "The config repository's status could not be read (store locked).",
    ),
  ).toBeVisible();

  allow();
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(
    page.getByText(/not running inside a daemon that owns a config repository/),
  ).toBeVisible();
});

test("the System settings tabs say their read failed, and Try again reads them", async ({
  page,
}) => {
  const allow = await failRead(page, "**/api/settings", "intercepted failure");
  await page.goto("/settings/general");
  const failed = page.getByText(
    "Deployment settings could not be read (intercepted failure).",
  );
  await expect(failed).toBeVisible();
  // It stays once the snackbar carrying the same error has gone.
  await page.waitForTimeout(7_000);
  await expect(failed).toBeVisible();

  allow();
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByText(/could not be read/)).toHaveCount(0);
  await expect(page.getByLabel("Environment name")).toBeVisible();
});
