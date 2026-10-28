import { test, expect } from "./fixtures.js";

test.setTimeout(90_000);

// docs/test-runs/2026-10-07-inbox-read-only-hidden-tab.md: a hidden tab
// with inbox notifications on reads /api/tasks every 30s (61c31fa31),
// and one with them off was meant to read nothing -- but the base-branch
// gates kept their own minute's clock, so it still read
// /api/base-branch-gates once a minute for as long as it stayed open.
//
// Two browser boundaries are driven, as the walkthrough drove them:
// window.Notification is a recorder whose permission is granted, and
// document.visibilityState is whatever window.__setVis last said. The
// clock is Playwright's, so minutes pass without waiting them out.
test("a hidden tab with notifications off reads nothing, and catches up when shown", async ({
  page,
}) => {
  await page.addInitScript(() => {
    class RecordingNotification {
      static permission = "granted";
      static requestPermission() {
        return Promise.resolve("granted");
      }
      close() {}
    }
    window.Notification = RecordingNotification;
    let vis = "visible";
    Object.defineProperty(Document.prototype, "visibilityState", {
      get: () => vis,
      configurable: true,
    });
    Object.defineProperty(Document.prototype, "hidden", {
      get: () => vis === "hidden",
      configurable: true,
    });
    document.hasFocus = () => vis === "visible";
    window.__setVis = (v) => {
      vis = v;
      document.dispatchEvent(new Event("visibilitychange"));
    };
  });
  await page.clock.install();
  await page.goto("/inbox");
  await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();

  const reads = [];
  page.on("request", (req) => {
    const path = new URL(req.url()).pathname;
    if (path.startsWith("/api/")) reads.push(path);
  });

  // Notifications on: only the task list, every ten ticks.
  await page.evaluate(() => window.__setVis("hidden"));
  reads.length = 0;
  await page.clock.runFor(2 * 60_000 + 1_000);
  expect(reads.filter((p) => p === "/api/tasks").length).toBeGreaterThanOrEqual(
    3,
  );
  expect(
    [...new Set(reads)].filter(
      (p) => p !== "/api/tasks" && p !== "/api/base-branch-gates",
    ),
  ).toEqual([]);

  // Notifications off: nothing at all, the gates' clock included.
  await page.evaluate(() => window.__setVis("visible"));
  await page.getByRole("button", { name: "Turn off notifications" }).click();
  await expect(
    page.getByRole("button", { name: "Turn on notifications" }),
  ).toBeVisible();
  await page.evaluate(() => window.__setVis("hidden"));
  await page.clock.runFor(5_000);
  reads.length = 0;
  await page.clock.runFor(3 * 60_000);
  expect(reads).toEqual([]);

  // Shown again, the gates are read at once rather than on their next tick.
  const gates = page.waitForRequest((req) =>
    new URL(req.url()).pathname.endsWith("/api/base-branch-gates"),
  );
  await page.evaluate(() => window.__setVis("visible"));
  await gates;
});
