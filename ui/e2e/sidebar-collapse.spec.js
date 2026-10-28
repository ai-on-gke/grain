import { expect, test } from "./fixtures.js";

// The desktop rail folded down to its icons (Sidebar.jsx's `collapsed`,
// sidebarCollapse.js): the toggle folds it, the icons still navigate,
// the page beside it takes the room it gave up, and the choice outlives
// a reload. A phone never folds it (PhoneNav.jsx's drawer).

test.setTimeout(90_000);

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

async function frameWidth(page) {
  const box = await page.locator(".page-frame").boundingBox();
  expect(box).not.toBeNull();
  return box.width;
}

async function noHorizontalScroll(page) {
  const { scroll, client } = await page.evaluate(() => ({
    scroll: document.scrollingElement.scrollWidth,
    client: document.scrollingElement.clientWidth,
  }));
  expect(scroll).toBeLessThanOrEqual(client);
}

test("folds the rail to icons, navigates from them and widens the page", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize(DESKTOP);
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();

  const aside = page.locator("aside");
  const openRail = await aside.boundingBox();
  const openFrame = await frameWidth(page);
  expect(openRail.width).toBeGreaterThan(200);

  const collapse = aside.getByRole("button", { name: "Collapse sidebar" });
  await expect(collapse).toHaveAttribute("aria-expanded", "true");
  await collapse.click();

  const expand = aside.getByRole("button", { name: "Expand sidebar" });
  await expect(expand).toHaveAttribute("aria-expanded", "false");
  await expect(aside).toHaveAttribute("data-collapsed", "true");
  await expect
    .poll(async () => (await aside.boundingBox()).width)
    .toBeLessThanOrEqual(64);
  const railWidth = (await aside.boundingBox()).width;
  expect(railWidth).toBeGreaterThanOrEqual(56);

  // The page beside the rail takes all of what the rail gave up, with
  // nothing overlapping and nothing scrolling sideways.
  await expect
    .poll(() => frameWidth(page))
    .toBeGreaterThanOrEqual(openFrame + openRail.width - railWidth - 1);
  const frame = await page.locator(".page-frame").boundingBox();
  expect(frame.x).toBeGreaterThanOrEqual(railWidth - 1);
  expect(frame.x + frame.width).toBeLessThanOrEqual(DESKTOP.width + 1);
  await noHorizontalScroll(page);

  // No label is drawn: every one is hidden whole, not clipped. The
  // wordmark and the build stamp go too.
  await expect(aside.getByRole("heading", { name: "grain" })).toHaveClass(
    /visually-hidden/,
  );
  await expect(page.getByTestId("build-stamp")).toHaveCount(0);
  const drawnLabels = await aside
    .locator(".MuiListItemText-root")
    .evaluateAll(
      (els) => els.filter((el) => el.getBoundingClientRect().width > 1).length,
    );
  expect(drawnLabels).toBe(0);

  // Every icon sits inside the rail, a badge on its corner included.
  const outside = await aside
    .locator(".MuiListItemButton-root, .rail-count")
    .evaluateAll(
      (els, width) =>
        els.filter((el) => {
          const r = el.getBoundingClientRect();
          return r.width > 0 && (r.left < 0 || r.right > width + 0.5);
        }).length,
      railWidth,
    );
  expect(outside).toBe(0);

  // Navigate by icon: the hover says where it goes, and the entry is lit
  // as the open page once it is there.
  const repos = aside.getByRole("button", { name: /^Repos/ });
  await repos.hover();
  // (The chevron, still focused from the click, keeps its own open.)
  await expect(
    page.getByRole("tooltip").filter({ hasText: /^Repos$/ }),
  ).toBeVisible();
  await repos.click();
  await expect(repos).toHaveAttribute("aria-current", "page");
  await expect(page).toHaveURL(/\/repos/);

  const tasks = aside.getByRole("button", { name: /^Tasks/ }).last();
  await tasks.click();
  await expect(tasks).toHaveAttribute("aria-current", "page");
  await noHorizontalScroll(page);

  // Remembered across a reload, and unfolded with the "[" key.
  await page.reload();
  await expect(page.locator("aside")).toHaveAttribute("data-collapsed", "true");
  await page.locator("body").press("[");
  await expect(page.locator("aside")).toHaveAttribute(
    "data-collapsed",
    "false",
  );
  await expect
    .poll(async () => (await page.locator("aside").boundingBox()).width)
    .toBeGreaterThan(200);
});

test("never folds the phone drawer, whatever this browser chose", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);
  await page.goto("/");
  await page.evaluate(() =>
    localStorage.setItem("grain.sidebar.collapsed", "true"),
  );
  await page.setViewportSize(PHONE);
  await page.reload();
  await page.getByRole("button", { name: "Open navigation" }).click();
  const aside = page.locator("aside");
  await expect(aside).toBeVisible();
  await expect(aside).toHaveAttribute("data-collapsed", "false");
  await expect(
    aside.getByRole("button", { name: /collapse sidebar|expand sidebar/i }),
  ).toHaveCount(0);
});
