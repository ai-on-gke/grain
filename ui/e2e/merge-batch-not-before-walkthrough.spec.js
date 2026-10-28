import { test, expect } from "./fixtures.js";

// The browser half of the 2026-10-02 test plan's §4 walkthrough
// (docs/test-runs/2026-10-02-merge-batch-not-before.md): a task filed for
// later over POST /api/tasks, read back in the task pane. Its half on a
// team's merge batch size went with Team settings -> Capacity in the
// single-tenant reset (stage 3e-ii), and the team member it filed as
// with team roles (3e-iii-c2). Against the real demo server; nothing of
// grain's own is stubbed.
//
// Its tasks would be seen by other files sharing a worker's demo, so it
// asks for a demo of its own: a distinct worker-scoped demoArgs is a
// distinct worker. The arguments are demo's defaults.
test.use({ demoArgs: ["-default-target-repo", "acme/widgets"] });

test.describe("merge batch size and not_before walkthrough", () => {
  test("a task filed with deferredUntil shows its wake time and where it wakes to", async ({
    page,
  }) => {
    const wake = new Date(Date.now() + 24 * 3600 * 1000);
    wake.setUTCHours(9, 0, 0, 0);

    const filed = async (title, extra) => {
      const r = await page.request.post("/api/tasks", {
        data: {
          title,
          description: "filed for later",
          repo: "acme/widgets",
          ...extra,
        },
      });
      expect(r.status(), await r.text()).toBe(201);
      return r.json();
    };
    const approved = await filed("approved, for tomorrow", {
      approved: true,
      deferredUntil: wake.toISOString(),
    });
    expect(approved.state).toBe("deferred");
    expect(approved.deferredUntil).toBe(
      wake.toISOString().replace(".000Z", "Z"),
    );
    const waiting = await filed("unapproved, for tomorrow", {
      deferredUntil: wake.toISOString(),
    });
    expect(waiting.state).toBe("deferred");
    const past = await filed("already past", {
      approved: true,
      deferredUntil: "2020-01-01T00:00:00Z",
    });
    expect(past.state).toBe("queued");
    expect(past.deferredUntil).toBeUndefined();

    await page.goto(`/tasks/${approved.id}`);
    await expect(page.getByText(/Picks itself back up on/)).toContainText(
      "onto the queue",
    );
    await page.goto(`/tasks/${waiting.id}`);
    await expect(page.getByText(/Picks itself back up on/)).toContainText(
      "back into the proposals for someone to approve",
    );
  });
});
