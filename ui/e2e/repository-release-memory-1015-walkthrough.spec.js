import { test, expect } from "./fixtures.js";

// Faults the 2026-10-15 repository, release and memory walkthrough found
// (docs/test-runs/2026-10-15-repository-release-memory-ui.md), driven the
// way the walkthrough met them: the real `grain demo` server, its seeded
// store, and nothing stubbed.
test.describe.configure({ mode: "serial" });

// Open and Save document are disabled while their request is in flight,
// and a browser drops focus from a button it disables: Enter on Open left
// a keyboard user on <body>, at the top of the page.
test("opening and saving a memory document keeps keyboard focus", async ({
  page,
}) => {
  const name = "release-checklist (this deployment)";
  await page.goto("/settings/memory");
  const main = page.getByRole("main");
  const open = main.getByRole("button", { name: `Open ${name}` });
  await open.focus();
  await page.keyboard.press("Enter");

  const close = main.getByRole("button", { name: `Close ${name}` });
  await expect(close).toBeFocused();

  const doc = main.getByLabel(`Document: ${name}`);
  const original = await doc.inputValue();
  try {
    await doc.fill(`${original}\n\nCorrected by the 10-15 walkthrough.`);
    const save = main.getByRole("button", { name: `Save document ${name}` });
    await save.focus();
    await page.keyboard.press("Enter");
    await expect(main.getByText(/edited here/).first()).toBeVisible();
    await expect(save).toBeFocused();

    // The next open reads the correction back from the store.
    await page.reload();
    await main.getByRole("button", { name: `Open ${name}` }).click();
    await expect(doc).toHaveValue(/Corrected by the 10-15 walkthrough\./);
  } finally {
    await doc.fill(original);
    await main.getByRole("button", { name: `Save document ${name}` }).click();
    await expect(doc).toHaveValue(original);
  }
});

// The repo page's tab is its address's hash, but changing only the hash
// is a same-document navigation, and the page read it once on mount.
test("changing only the hash switches the repo page's tab", async ({
  page,
}) => {
  await page.goto("/repos/acme/widgets#agents");
  await expect(page.getByRole("tab", { name: "agents" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await page.evaluate(() => {
    window.location.hash = "#ci";
  });
  await expect(page.getByRole("tab", { name: "ci" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(page.locator("#repo-tabpanel-ci")).toBeVisible();
  await expect(page.locator("#repo-tabpanel-agents")).toBeHidden();
});

// A release name has no length cap, and a long one widened the release
// picker and its branch names past a phone's screen.
test.describe("at phone width", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("a long release name does not push the releases pane sideways", async ({
    page,
    request,
  }) => {
    const releases = "/api/repos/acme/widgets/releases";
    const long = `walk-${"x".repeat(64)}`;
    const res = await request.post(releases, { data: { name: long } });
    expect(res.status()).toBe(201);
    try {
      await page.goto(`/repos/acme/widgets/releases/${long}`);
      await expect(page.getByText(`${long}.latest`).first()).toBeVisible();
      const main = page.getByRole("main");
      const widths = await main.evaluate((m) => [m.scrollWidth, m.clientWidth]);
      expect(widths[0]).toBeLessThanOrEqual(widths[1]);
    } finally {
      await request.delete(`${releases}/${long}`);
    }
  });
});
