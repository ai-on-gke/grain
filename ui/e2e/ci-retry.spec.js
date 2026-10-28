import { test, expect } from "./fixtures.js";

// grain/task-480: a grain CI run that ended without a pass had Cancel's
// opposite nowhere, so a run that errored on a capability the repository
// was later granted could only be force-merged. A concluded run now
// offers Retry, which starts the same run again and opens it.
//
// -fake-ci is the stub that accepts runs and never executes them: a run
// it starts stays pending until it is cancelled, which is the one way to
// have a concluded run on a demo.
test.use({ demoArgs: ["-fake-ci"] });

test("a cancelled run is retried from its own pane", async ({ page }) => {
  const ok = async (res, what) => {
    expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
    return res.json();
  };
  const runs = async () =>
    (await ok(await page.request.get("/api/ci/runs"), "ci runs")).runs;

  const mode = await ok(
    await page.request.get("/api/repos/acme/widgets/ci-mode"),
    "ci mode",
  );
  await ok(
    await page.request.put("/api/repos/acme/widgets/ci-mode", {
      data: { ciMode: "grain" },
    }),
    "ci mode on",
  );
  try {
    const first = await ok(
      await page.request.post("/api/ci/runs", {
        data: { repo: "acme/widgets", ref: "main", commit: "abc1234" },
      }),
      "start",
    );
    await ok(
      await page.request.delete(`/api/ci/runs/${first.id}`, {
        data: { reason: "the e2e suite stopped it" },
      }),
      "cancel",
    );

    await page.goto(`/ci/${first.id}`);
    const retry = page.getByRole("button", { name: "Retry", exact: true });
    await expect(retry).toBeVisible();
    // Every job of a cancelled run was stopped: there is nothing narrower
    // to retry, and Cancel belongs to a run still going.
    await expect(
      page.getByRole("button", { name: "Retry failed jobs" }),
    ).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Cancel run" })).toHaveCount(
      0,
    );

    // Retry sits in the run's header row, where Cancel run sits on a run
    // still going, and stays on screen at a phone's width.
    for (const viewport of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      const button = await retry.boundingBox();
      const repo = await page
        .getByRole("dialog")
        .getByText("acme/widgets", { exact: true })
        .first()
        .boundingBox();
      expect(button.x).toBeGreaterThanOrEqual(0);
      expect(button.x + button.width).toBeLessThanOrEqual(viewport.width);
      if (viewport.width > 1000) {
        const middle = (b) => b.y + b.height / 2;
        expect(Math.abs(middle(button) - middle(repo))).toBeLessThan(6);
      }
    }

    await retry.click();
    // The pane follows to the run Retry started, which is still going.
    await expect(
      page.getByRole("button", { name: "Cancel run" }),
    ).toBeVisible();
    const after = await runs();
    const second = after.find((r) => r.id !== first.id);
    expect(second).toMatchObject({
      repo: "acme/widgets",
      ref: "main",
      commit: "abc1234",
      event: "push",
    });
    expect(second.conclusion ?? "").toBe("");
    await ok(
      await page.request.delete(`/api/ci/runs/${second.id}`, { data: {} }),
      "cancel the retry",
    );
  } finally {
    await page.request.put("/api/repos/acme/widgets/ci-mode", {
      data: { ciMode: mode?.ciMode ?? "" },
    });
  }
});
