// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { test, expect } from "./fixtures.js";

// Processes under /next/, from the 2026-11-14 recurring and composed-work
// walkthrough (docs/test-runs/2026-11-14-recurring-composed-ui.md):
//
// - a refused save (an every-5-minutes schedule under the ten-minute
//   floor) put its error in the page banner behind the dialog's scrim, so
//   Save looked like it did nothing;
// - "Next:" showed raw RFC 3339 with nanoseconds, kept showing a time for
//   a paused schedule and the far-future placeholder of a suppressed one,
//   and the rows said nothing about the next run at all;
// - item links (/suites/<id>, /processes/suites/runs/<id>, the classic
//   /schedules/<id> and /templates/<id>) fell through to the Inbox.

async function deleteSchedulesTitled(page, title) {
  const res = await page.request.get("/api/schedules");
  for (const s of await res.json()) {
    if (s.title === title) await page.request.delete(`/api/schedules/${s.id}`);
  }
}

test("a refused schedule save shows its error inside the dialog", async ({
  page,
}, testInfo) => {
  testInfo.annotations.push({ type: "allows-page-error" });
  await page.goto("/next/processes/schedules");
  await page.getByTestId("proc-new-schedule-btn").click();
  const dialog = page.getByTestId("schedule-dialog");
  await page.getByTestId("sched-title-input").fill("e2e floor check");
  await page.getByTestId("sched-desc-input").fill("prompt");
  await page.getByTestId("sched-repo-input").fill("acme/widgets");
  await page.getByTestId("sched-kind-everyNMinutes").click();
  await dialog.locator("input[type=number]").first().fill("5");
  await page.getByTestId("sched-save-btn").click();

  const err = dialog.getByTestId("dialog-error");
  await expect(err).toContainText("at least 10");
  // It is on top: nothing (the scrim, say) covers its middle.
  const box = await err.boundingBox();
  const hit = await page.evaluate(
    ([x, y]) =>
      !!document.elementFromPoint(x, y)?.closest("[data-testid=dialog-error]"),
    [box.x + box.width / 2, box.y + box.height / 2],
  );
  expect(hit).toBe(true);

  // Fixing the value saves and closes; a later dialog opens clean.
  await dialog.locator("input[type=number]").first().fill("15");
  await page.getByTestId("sched-save-btn").click();
  await expect(dialog).toBeHidden();
  await page.getByTestId("proc-new-schedule-btn").click();
  await expect(dialog.getByTestId("dialog-error")).toHaveCount(0);
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await deleteSchedulesTitled(page, "e2e floor check");
});

test("next-run labels: formatted, paused, suppressed", async ({ page }) => {
  const created = await page.request.post("/api/schedules", {
    data: {
      title: "e2e off-target watch",
      description: "watch",
      repo: "acme/widgets",
      recurrence: {
        kind: "git_ref",
        gitRef: { repo: "offlist/upstream", ref: "refs/heads/main" },
      },
    },
  });
  expect(created.ok()).toBe(true);
  const watch = await created.json();
  try {
    await page.goto(`/next/processes/schedules/${watch.id}`);
    const row = page.getByTestId(`sched-row-${watch.id}`);
    await expect(row).toContainText("Suppressed");
    await expect(page.getByTestId(`sched-next-${watch.id}`)).toHaveText(
      "· next: suppressed",
    );
    await expect(page.getByTestId("sched-detail-title")).toContainText(
      "e2e off-target watch",
    );
    await expect(page.getByTestId("sched-detail-next")).toHaveText(
      "Next: suppressed",
    );

    // A suppressed schedule can still be paused, and then says so.
    await page.getByTestId("sched-toggle-enabled-btn").click();
    await expect(row).toContainText("Paused");
    await expect(page.getByTestId("sched-detail-next")).toHaveText(
      "Next: paused",
    );
    await page.reload();
    await expect(page.getByTestId("sched-detail-next")).toHaveText(
      "Next: paused",
    );

    // An enabled time-based schedule shows a readable time, not RFC 3339.
    await page.getByTestId("sched-row-sched-2").click();
    const next = page.getByTestId("sched-detail-next");
    await expect(next).toContainText("2026");
    await expect(next).not.toContainText("T06:00");
  } finally {
    await page.request.delete(`/api/schedules/${watch.id}`);
  }
});

test("item deep links land on the item", async ({ page }) => {
  await page.goto("/next/suites/suite-1");
  await expect(page).toHaveURL(/\/next\/processes\/suites\/suite-1$/);
  await expect(page.getByTestId("suite-detail-title")).toContainText(
    "Release Qualification Suite",
  );

  await page.goto("/next/schedules/sched-1");
  await expect(page).toHaveURL(/\/next\/processes\/schedules\/sched-1$/);
  await expect(page.getByTestId("sched-detail-title")).toContainText(
    "Release Qualification",
  );

  await page.goto("/next/templates/template-2");
  await expect(page.getByTestId("tmpl-detail-title")).toContainText(
    "security-audit",
  );

  // A suite run, started through the UI, opens by its own URL.
  await page.goto("/next/processes/suites/suite-1");
  await page.getByTestId("suite-open-run-dialog-btn").click();
  await page.getByTestId("run-suite-submit").click();
  await expect(page.getByTestId("suite-run-inspector")).toBeVisible();
  const runs = await (await page.request.get("/api/suite-runs")).json();
  const list = Array.isArray(runs) ? runs : runs.runs;
  const id = Math.max(...list.map((r) => r.id));
  await page.goto("/next/inbox");
  await page.goto(`/next/processes/suites/runs/${id}`);
  await expect(page.getByTestId("suite-run-inspector")).toContainText(
    `Run #${id}`,
  );
  await page.goto(`/next/suites/runs/${id}`);
  await expect(page).toHaveURL(
    new RegExp(`/next/processes/suites/runs/${id}$`),
  );
  await expect(page.getByTestId("suite-run-inspector")).toContainText(
    `Run #${id}`,
  );
  // Leave the worker's store as found: cancel the run's pass tasks, which
  // asks first.
  await page.getByTestId("suite-run-cancel-btn").click();
  const confirm = page.getByTestId("confirm-dialog");
  await expect(confirm).toContainText(`suite run #${id}`);
  await confirm.getByRole("button", { name: "Stop suite tasks" }).click();
  await expect(confirm).toBeHidden();
});

// A template with a required input field and no default had its
// composition preview refused by the server ("field is required"), so its
// pane silently showed none.
test("a template with a required field still previews its composition", async ({
  page,
}) => {
  const created = await page.request.post("/api/templates", {
    data: {
      name: "e2e-required-field",
      title: "Fix {{area}}",
      description: "Please fix {{area}}.",
      params: ["area"],
      fields: [{ name: "area", required: true }],
    },
  });
  expect(created.ok()).toBe(true);
  const tmpl = await created.json();
  const refused = [];
  page.on("response", (r) => {
    if (r.url().includes("/composition") && r.status() >= 400) {
      refused.push(`${r.status()} ${r.url()}`);
    }
  });
  try {
    await page.goto(`/next/processes/templates/${tmpl.id}`);
    await expect(page.getByTestId("tmpl-detail-title")).toContainText(
      "e2e-required-field",
    );
    const inspector = page.getByTestId("proc-inspector");
    await expect(inspector).toContainText("Composition preview");
    await expect(inspector).toContainText("Fix {{area}}");
    expect(refused).toEqual([]);
  } finally {
    await page.request.delete(`/api/templates/${tmpl.id}`);
  }
});
