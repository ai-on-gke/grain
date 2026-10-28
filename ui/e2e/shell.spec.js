import { test, expect } from "./fixtures.js";
import { alertsBell, alertRow, openAlerts } from "./alerts.js";

// The shell fits the window: the rail and the page share the height of
// the screen rather than one of them covering another or the last of
// them hanging off the bottom (grain/task-24).
//
// This is a layout question, so it can only be asked here -- jsdom has no
// layout engine and no stylesheet, and the component tests next door can
// say which elements render and nothing about where they end up.
//
// A tablet, because that is where it was reported: on an iPad added to
// the home screen there is no browser chrome to scroll away and no room
// to spare, so a UI a line taller than the window simply loses the line.
// The rules under test are not tablet-specific -- .app-shell and
// .app-body in src/style.css apply everywhere -- so a desktop-sized
// window would be a weaker version of the same check.
const TABLET = { width: 820, height: 1180 }; // an iPad Air, portrait

// standingAlert pauses the pipeline for real (POST /api/pipeline-pause),
// so a standing warning is up for the assertions below -- a row in the
// alerts panel now, where it used to be a banner over the page
// (grain/task-4008). The afterEach in the describe block lifts it again,
// so the specs that run after this one on the same worker's demo find the
// pipeline running.
async function standingAlert(page) {
  const paused = await page.request.post("/api/pipeline-pause", {
    data: { reason: "Maintenance in progress" },
  });
  expect(paused.ok()).toBe(true);
}

test.describe("the shell, with a standing alert up", () => {
  test.use({ viewport: TABLET });
  test.afterEach(async ({ page }) => {
    await page.request.delete("/api/pipeline-pause");
  });

  test("puts the warning behind the bell rather than over the panes", async ({
    page,
  }) => {
    await standingAlert(page);
    await page.goto("/");

    await expect(alertsBell(page)).toHaveAttribute("data-severity", "warning");
    await expect(page.locator(".task-row").first()).toBeVisible();
    // No banner above the rail and the page: they are the shell's first
    // row, and the page's header rail is the top of the page.
    await expect(page.locator(".app-shell > :first-child")).toHaveClass(
      "app-body",
    );
    await expect(page.getByTestId("top-bar")).toHaveCount(0);
    await expect(page.getByText(/Maintenance in progress/i)).toHaveCount(0);

    // The warning is one click away, with its Resume.
    await openAlerts(page);
    const row = alertRow(page, /Maintenance in progress/);
    await expect(row).toBeVisible();
    await expect(row.getByRole("button", { name: "Resume now" })).toBeVisible();
  });

  test("is exactly the height of the window", async ({ page }) => {
    await standingAlert(page);
    await page.goto("/");
    await expect(alertsBell(page)).toHaveAttribute("data-severity", "warning");

    // Nothing went past the bottom of the screen:
    // the shell is the window's height, the document does not scroll, and
    // the foot of the rail is still on it.
    const shell = await page.locator(".app-shell").boundingBox();
    expect(Math.round(shell.height)).toBe(TABLET.height);

    const overflow = await page.evaluate(
      () => document.documentElement.scrollHeight - window.innerHeight,
    );
    expect(overflow).toBeLessThanOrEqual(0);

    // ...and when the rail holds more than the window has room for, the
    // overflow is taken inside the rail rather than by the document, so
    // Settings and the operator destinations beside it stay on screen.
    //
    // The entire left pane (<aside>) is the scroll container so the rail
    // scrolls as a single pane.
    //
    // And asked at a window short enough that the list must overflow
    // whatever this deployment has put in it, rather than at the tablet
    // above: the rail's height is the count of its first-class
    // destinations, which moves on its own -- it overflowed an iPad here
    // until the config-repository controls came out of it.
    const SHORT = { width: TABLET.width, height: 600 };
    await page.setViewportSize(SHORT);
    const aside = page.locator("aside");
    const scrolls = await aside.evaluate(
      (element) => element.scrollHeight > element.clientHeight,
    );
    expect(scrolls).toBe(true);
    await aside.evaluate((element) => {
      element.scrollTop = element.scrollHeight;
    });
    const last = await page
      .locator("aside")
      .getByText("Repos", { exact: true })
      .boundingBox();
    expect(last.y + last.height).toBeLessThanOrEqual(SHORT.height);
  });

  // The stamp is the last child of the rail and the only text-only one,
  // which makes it the first thing flexbox collapses to nothing when the
  // list above it wants more height than there is -- so this is asked
  // with the rail crowded, at the short window the test above uses, and
  // with the list scrolled to its end. Asked at the tablet, with room to
  // spare and nothing to scroll, it says only that the stamp renders.
  test("keeps the build stamp visible at the end of the scrolled rail", async ({
    page,
  }) => {
    await standingAlert(page);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const SHORT = { width: TABLET.width, height: 600 };
    await page.setViewportSize(SHORT);
    await page.locator("aside").evaluate((element) => {
      element.scrollTop = element.scrollHeight;
    });
    const stamp = page.getByTitle(/^Running commit/);
    await expect(stamp).toBeVisible();
    const box = await stamp.boundingBox();
    expect(box.height).toBeGreaterThan(0);
    expect(box.y + box.height).toBeLessThanOrEqual(SHORT.height);
  });
});
