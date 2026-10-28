import { test, expect } from "./fixtures.js";

// Faults the 2026-10-12 repository, release and memory walkthrough found
// (docs/test-runs/2026-10-12-repository-release-memory-ui.md), driven the
// way the walkthrough met them: the real `grain demo` server, its seeded
// store, and nothing stubbed.
test.describe.configure({ mode: "serial" });

// The repo page drew no main landmark, unlike the Repos list, the
// releases pane and a comparison beside it. It also named the team it
// was edited in two ways ("admin", "default"); since the single-tenant
// reset (stage 3e-ii) it names none, and edits the deployment's own
// defaults for the repo.
test("a repo page is the main landmark and edits the deployment's defaults for the repo", async ({
  page,
}) => {
  await page.goto("/repos");
  await page
    .locator(".repo-list-row")
    .filter({ hasText: "acme/widgets" })
    .getByRole("link", { name: "acme/widgets" })
    .click();
  await expect(page).toHaveURL(/\/repos\/acme\/widgets$/);

  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { level: 2 })).toHaveText(
    "acme/widgets",
  );
  await expect(page.getByRole("combobox", { name: "Team" })).toHaveCount(0);
  await main.getByRole("tab", { name: "Agents" }).click();
  await expect(main.getByText(/Team context/)).toHaveCount(0);
  await expect(
    main.getByLabel("Prompt extension for acme/widgets", { exact: true }),
  ).toBeVisible();
  await expect(
    main.getByRole("combobox", { name: "Default capabilities", exact: true }),
  ).toBeVisible();
});

// A repo's prompt extension saves as it is left. Clicking anywhere else
// leaves it; the browser's Back (Alt+Left, a mouse's back button, a
// phone's back gesture) unmounted it with focus still inside, its onBlur
// never ran, and the edit was gone without a word.
test("Back with an edit still in a repo's prompt extension saves it", async ({
  page,
}) => {
  const promptRoute = "/api/repos/acme/widgets/prompt-extension";
  const text = `Saved by leaving with Back ${Date.now()}`;
  try {
    await page.goto("/repos");
    await page
      .locator(".repo-list-row")
      .filter({ hasText: "acme/widgets" })
      .getByRole("link", { name: "acme/widgets" })
      .click();
    await page.getByRole("tab", { name: "Agents" }).click();
    const box = page.getByLabel("Prompt extension for acme/widgets", {
      exact: true,
    });
    await box.fill(text);
    await expect(box).toBeFocused();

    await page.goBack();
    await expect(page).toHaveURL(/\/repos$/);
    await expect
      .poll(
        async () =>
          (await (await page.request.get(promptRoute)).json())?.promptExtension,
      )
      .toBe(text);

    await page.goForward();
    await expect(box).toHaveValue(text);
  } finally {
    await page.request.put(promptRoute, { data: { promptExtension: "" } });
  }
});
