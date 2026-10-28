import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-observability"] });

// Two faults the 2026-10-13 deployment operations walkthrough found
// (docs/test-runs/2026-10-13-deployment-operations.md), each driven here
// through the real demo server and its built frontend.

// System > Agents & Task Branches offered Author name and Author email
// boxes for a deployment-wide commit identity that no longer exists
// (f386abe49): PUT /api/settings dropped them and answered 200, so the
// save looked fine and a reload showed the box empty. The identity is a
// team's credential's own account, shown on Team settings > Git identity,
// and falls back to grain's own -- which that pane names.
test("sends the commit identity to the team's Git identity tab", async ({
  page,
}) => {
  await page.goto("/settings/agents");
  await expect(page.getByLabel("Branch name template")).toBeVisible();
  await expect(page.getByTestId("commit-identity-pointer")).toContainText(
    "grain agent <grain-agent@localhost>",
  );
  await expect(page.locator('input[name="agentGitName"]')).toHaveCount(0);
  await expect(page.locator('input[name="agentGitEmail"]')).toHaveCount(0);

  // The pane types in no identity: it says each run commits as its
  // credential's own account, and names grain's own as the fallback.
  await page.goto("/settings/git");
  await expect(page.getByTestId("commit-author-derived")).toContainText(
    "grain agent <grain-agent@localhost>",
  );
  await expect(page.getByLabel("Commit author name")).toHaveCount(0);
  await expect(page.getByLabel("Commit author email")).toHaveCount(0);
});

// System > Sandboxes: a first read of /api/sandboxes that failed left
// "Loading sandbox health" spinning for as long as the pane was open.
// Stubbed, because the demo's sampler does not fail; everything else is
// the real server and UI.
test("says a failed sandbox-health read failed instead of spinning", async ({
  page,
}) => {
  let fail = true;
  await page.route("**/api/sandboxes", async (route) => {
    if (!fail) {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "sampler crashed" }),
    });
  });

  await page.goto("/system/sandboxes");
  await expect(
    page.getByText(/Sandbox health could not be read \(sampler crashed\)/),
  ).toBeVisible();
  await expect(page.getByLabel("Loading sandbox health")).toHaveCount(0);

  fail = false;
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect(page.getByText(/could not be read/)).toHaveCount(0);
  await expect(page.getByText(/Load average \(1\/5\/15 min\)/)).toBeVisible();
});

// The same walkthrough found three more System tabs that a failed first
// read left blank (Logs, Upgrade) or showing an empty deployment (All
// Users) once the snackbar had gone. Each read is stubbed to fail; the
// pane has to say so on its own.
for (const { path, route, says } of [
  {
    path: "/system/logs",
    route: "**/api/logs",
    says: /The logs could not be listed \(stubbed failure\)/,
  },
  {
    path: "/settings/upgrade",
    route: "**/api/upgrade",
    says: /upgrade status could not be read \(stubbed failure\)/,
  },
]) {
  test(`${path} says a failed first read failed`, async ({ page }) => {
    await page.route(route, (r) =>
      r.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: "stubbed failure" }),
      }),
    );
    await page.goto(path);
    await expect(page.getByText(says)).toBeVisible();
    await expect(page.getByText("No user accounts found.")).toHaveCount(0);
  });
}
