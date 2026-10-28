import { test, expect } from "./fixtures.js";
import { openAlerts } from "./alerts.js";

// docs/test-plans/2026-10-05.md §9 steps 1 and 2, in a browser against a
// real `grain demo` (docs/test-runs/2026-10-05-model-source-pause-chat-standing.md).
//
// -agent-paused begins a real orchestrator.Pause on demo's one
// connection (gemini-pro, the default team's), the way a run that met a
// provider's usage limit on its credential begins one. Nothing is mocked: the
// templates, schedules and tasks are filed through grain's own API and
// read back in the task pane, Team settings and the banner.
test.use({ demoArgs: ["-agent-paused"] });

async function post(page, path, data, method = "POST") {
  const res = await page.request.fetch(path, { method, data });
  expect(res.ok(), `${method} ${path}: ${res.status()}`).toBe(true);
  return res.status() === 204 ? null : res.json();
}

// modelRow is the task pane's "Model" line (detail/Declared.jsx).
function modelRow(page) {
  return page.locator(".declared-row").filter({
    has: page.locator("text=/^Model$/"),
  });
}

test.describe("Model source, paused-framework UI (§9)", () => {
  test("the pane says which declaration chose a task's model", async ({
    page,
  }) => {
    const stamp = Date.now();
    const tmpl = await post(page, "/api/templates", {
      name: `walk-model-${stamp}`,
      title: `Walk model ${stamp}`,
      description: "A template that chooses a model.",
      repo: "acme/widgets",
      model: "gemini-2.5-flash",
    });
    const sched = await post(page, "/api/schedules", {
      title: `Walk schedule ${stamp}`,
      description: "A schedule that chooses a model.",
      repo: "acme/widgets",
      model: "gemini-2.5-pro",
      recurrence: { kind: "everyNHours", everyNHours: 24 },
    });
    // A schedule firing the template, overriding its model.
    const schedOver = await post(page, "/api/schedules", {
      repo: "acme/widgets",
      templateId: tmpl.id,
      model: "gemini-2.5-pro",
      recurrence: { kind: "everyNHours", everyNHours: 24 },
    });

    const fromTemplate = await post(page, "/api/tasks", {
      templateId: tmpl.id,
    });
    const overridden = await post(page, "/api/tasks", {
      templateId: tmpl.id,
      model: "gemini-2.5-pro",
    });
    const fromSchedule = (
      await post(page, `/api/schedules/${sched.id}/run`, {})
    ).taskId;
    const fromScheduleOverTemplate = (
      await post(page, `/api/schedules/${schedOver.id}/run`, {})
    ).taskId;

    const cases = [
      [fromTemplate.id, "gemini-2.5-flash (from the template)"],
      [fromSchedule, "gemini-2.5-pro (from the schedule)"],
      [fromScheduleOverTemplate, "gemini-2.5-pro (from the schedule)"],
      // The person filing chose the model, so the template is not credited.
      [overridden.id, "gemini-2.5-pro"],
    ];
    for (const [id, want] of cases) {
      await page.goto(`/tasks/${id}`);
      await expect(modelRow(page), `task ${id}`).toContainText(want);
      if (!want.includes("(from")) {
        await expect(modelRow(page)).not.toContainText("from the");
      }
    }

    // An edit that leaves the model alone keeps the credit; one that
    // changes it makes the model the task's own.
    await post(
      page,
      `/api/tasks/${fromTemplate.id}`,
      { title: `Retitled ${stamp}` },
      "PATCH",
    );
    await page.goto(`/tasks/${fromTemplate.id}`);
    await expect(modelRow(page)).toContainText(
      "gemini-2.5-flash (from the template)",
    );
    await post(
      page,
      `/api/tasks/${fromSchedule}`,
      { model: "gemini-2.5-flash" },
      "PATCH",
    );
    await page.goto(`/tasks/${fromSchedule}`);
    await expect(modelRow(page)).toContainText("gemini-2.5-flash");
    await expect(modelRow(page)).not.toContainText("from the");
  });

  test("a paused connection shows on Connections, Models, the pane and Run now, and clears when lifted", async ({
    page,
  }) => {
    const config = await (await page.request.get("/api/config")).json();
    expect(config.agentPause?.paused).toBe(true);
    // One pause, on the connection whose credential met the limit, named
    // with its team -- and liftable by this (admin) viewer.
    expect(config.agentPause.pauses).toEqual([
      expect.objectContaining({
        connection: "gemini-pro",
        team: "default",
        framework: "antigravity",
        canLift: true,
      }),
    ]);
    const reason = config.agentPause.reason;
    expect(reason).toContain("Gemini API quota exhausted");

    // Connections: the paused connection is chipped Paused, with
    // the time it lifts and the provider's reason.
    await page.goto("/settings/models");
    const card = page.locator(".connection-paused").first();
    await expect(card).toBeVisible();
    await expect(card).toContainText(
      /paused until .+ \(\d+h \d+m( left)?\)|paused until /,
    );
    await expect(card).toContainText(reason);
    await expect(
      page.getByText("Paused", { exact: true }).first(),
    ).toBeVisible();
    await expect(page.getByText("Active", { exact: true })).toHaveCount(0);

    // Models: "Served by" chips the connection "(Paused)", and its
    // tooltip carries the same until-and-why line.
    await page.getByTestId("settings-nav-models").click();
    const chip = page.locator(".MuiChip-root", { hasText: "(Paused)" }).first();
    await expect(chip).toBeVisible();
    await expect(chip).toHaveAttribute(
      "title",
      /paused until .*Gemini API quota exhausted/,
    );

    // A queued task of that team, which has no other connection to rotate
    // onto: the pane's waiting line, and the Run now hint.
    const task = await post(page, "/api/tasks", {
      title: `Paused walk ${Date.now()}`,
      description: "Queued under a pause.",
      repo: "acme/widgets",
    });
    await post(page, `/api/tasks/${task.id}/approve`, {});
    await page.goto(`/tasks/${task.id}`);
    await expect(page.locator(".detail-framework-paused")).toContainText(
      /waiting: connection .*gemini.* is paused until .*Gemini API quota exhausted/i,
    );
    const hint = page.locator(".hint", {
      hasText: "A run asked for now waits",
    });
    await expect(hint).toBeVisible();
    await page.getByRole("button", { name: "Run now" }).click();
    await expect(
      page.locator(".hint", { hasText: "Asked for a run" }),
    ).toBeVisible();
    // The request is recorded, the task does not start.
    const after = await (
      await page.request.get(`/api/tasks/${task.id}`)
    ).json();
    expect(after.state).toBe("queued");
    expect(after.runNowAt).toBeTruthy();

    // Lift it from the banner: everything clears. The task pane is a
    // modal, so the banner's button is behind it (aria-hidden) until the
    // pane is closed -- the report's observation O1. Run now is disabled
    // now, which drops focus to the page until the pane's focus trap takes
    // it back (it checks every 50ms); an Escape pressed before then lands
    // outside the pane and closes nothing. The action used to remount the
    // whole pane, which took focus with it, until grain/task-2179 kept the
    // pane up across it so it would not flicker.
    await expect
      .poll(() =>
        page.evaluate(() =>
          Boolean(document.activeElement?.closest(".MuiDialog-root")),
        ),
      )
      .toBe(true);
    await page.keyboard.press("Escape");
    // Lifted from the pause's row in the alerts panel
    // (grain/task-4008).
    const panel = await openAlerts(page);
    await panel.getByRole("button", { name: "Resume now" }).click();
    await expect(page.getByRole("button", { name: "Resume now" })).toHaveCount(
      0,
    );
    await page.goto(`/tasks/${task.id}`);
    await expect(
      page.getByRole("dialog", { name: new RegExp(`Task ${task.id}:`) }),
    ).toBeVisible();
    await expect(page.getByRole("button", { name: "Run now" })).toBeVisible();
    await expect(page.locator(".detail-framework-paused")).toHaveCount(0);
    await expect(hint).toHaveCount(0);
    const cleared = await (await page.request.get("/api/config")).json();
    expect(cleared.agentPause?.paused ?? false).toBe(false);
    await page.goto("/settings/models");
    await expect(
      page.getByText("Active", { exact: true }).first(),
    ).toBeVisible();
    await expect(page.locator(".connection-paused")).toHaveCount(0);
    await page.getByTestId("settings-nav-models").click();
    await expect(
      page.locator(".MuiChip-root", { hasText: "(Paused)" }),
    ).toHaveCount(0);
    await expect(
      page.locator(".MuiChip-root", { hasText: "(Enabled)" }).first(),
    ).toBeVisible();
  });
});
