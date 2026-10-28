import { expect, test } from "./fixtures.js";

// The repo page's Compare branches form, over `grain demo -github-sim`
// (docs/test-runs/2026-10-23-repository-release-memory-ui.md). The only
// stand-in is GitHub itself: the simulated repo has main and
// release/1.0, read through the real GitHub client.
//
// Before the walkthrough the two boxes suggested only the branches grain
// had been asked to create here -- none of the repository's own, and
// some still pending creation -- and a branch GitHub did not have came
// back as "an upstream service returned an error".
test.use({ demoArgs: ["-github-sim"] });
test.setTimeout(90_000);

test("compare offers the repository's own branches and says plainly when one is missing", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/repos/acme/widgets/settings/branches");
  const base = page.getByRole("textbox", { name: "Base branch" });
  const head = page.getByRole("textbox", { name: "Head branch" });
  await expect(base).toHaveValue("main");

  await head.click();
  await expect(
    page.getByRole("menuitem", { name: "release/1.0" }),
  ).toBeVisible();
  await expect(page.getByRole("menuitem", { name: "main" })).toBeVisible();

  // The two boxes share the row, as the text boxes they replaced did,
  // with Compare after them on the same line.
  const b = await base.boundingBox();
  const h = await head.boundingBox();
  const c = await page
    .getByRole("button", { name: "Compare", exact: true })
    .boundingBox();
  expect(Math.abs(b.y - h.y)).toBeLessThan(2);
  expect(h.x).toBeGreaterThan(b.x + b.width - 1);
  expect(c.x).toBeGreaterThan(h.x + h.width - 1);
  expect(Math.abs(b.width - h.width)).toBeLessThan(2);
  // And between them they fill it: the picker's own root is a plain
  // block, which left each box at its intrinsic width.
  const form = await page
    .locator("form", { has: page.getByRole("button", { name: "Compare" }) })
    .boundingBox();
  expect(c.x + c.width).toBeGreaterThan(form.x + form.width - 2);

  await head.fill("no-such-branch");
  await expect(
    page.getByText('No branch "no-such-branch" on acme/widgets'),
  ).toBeVisible();
  await page.getByRole("button", { name: "Compare", exact: true }).click();
  await expect(page).toHaveURL(
    /\/repos\/acme\/widgets\/compare\/main\.\.\.no-such-branch$/,
  );
  await expect(
    page
      .getByText(
        "GitHub has nothing to compare for main...no-such-branch in acme/widgets",
      )
      .first(),
  ).toBeVisible();
  await expect(page.getByText(/upstream service returned/)).toHaveCount(0);

  await page.goBack();
  await page.getByRole("textbox", { name: "Head branch" }).fill("");
  await page.getByRole("textbox", { name: "Head branch" }).click();
  await page.getByRole("menuitem", { name: "release/1.0" }).click();
  await page.getByRole("button", { name: "Compare", exact: true }).click();
  await expect(page).toHaveURL(
    /\/repos\/acme\/widgets\/compare\/main\.\.\.release\/1\.0$/,
  );
  await expect(
    page.getByText("There are no changes between these branches."),
  ).toBeVisible();
});
