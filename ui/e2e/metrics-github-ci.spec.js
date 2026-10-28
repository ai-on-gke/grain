import { test, expect } from "./fixtures.js";

// The Metrics pane's GitHub CI section (metrics.GitHubCI), over the demo's
// seeded GitHub results (pkg/demo/demo_githubci.go): it follows the
// sections before it in the pane's order (the demo runs no grain CI, so
// CI queueing is not drawn here; ReportBoard.sections.test.jsx holds the
// two side by side), says which source it is, and fits a phone's width --
// its tables scroll inside their own box rather than widening the page.

for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`draws GitHub CI in its place, labelled with its source (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/metrics");

    const heading = page.getByRole("heading", { name: /^GitHub CI/ });
    await expect(heading).toBeVisible();
    const source = page.getByTestId("github-ci-source");
    await expect(source).toHaveText("source: github");

    // In the pane's own order, after the sections before it.
    const backlog = page.getByRole("heading", { name: "Backlog", exact: true });
    const [q, h, chip] = await Promise.all([
      backlog.boundingBox(),
      heading.boundingBox(),
      source.boundingBox(),
    ]);
    expect(h.y).toBeGreaterThan(q.y);
    // The source chip sits on the heading's line, centred on it.
    const mid = (b) => b.y + b.height / 2;
    expect(Math.abs(mid(chip) - mid(h))).toBeLessThanOrEqual(2);

    // The demo's flaky test job is named, and the jobs table is there.
    await expect(page.getByTestId("github-ci-flaky")).toContainText(
      "CI / test (acme/widgets)",
    );
    const jobs = page.getByTestId("github-ci-jobs");
    await expect(jobs).toBeVisible();

    // Nothing in the section is wider than the page: the table's own
    // box scrolls on a phone, and the page itself does not.
    const scroller = await jobs.evaluate((t) => {
      const box = t.parentElement.getBoundingClientRect();
      return { right: box.right, width: box.width };
    });
    expect(scroller.right).toBeLessThanOrEqual(viewport.width + 1);
    const pageWidth = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(pageWidth).toBeLessThanOrEqual(viewport.width + 1);
  });
}
