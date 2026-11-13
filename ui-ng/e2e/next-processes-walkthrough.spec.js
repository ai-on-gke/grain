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

// What the 2026-11-13 Processes walkthrough under /next/ found
// (docs/test-runs/2026-11-13-recurring-composed-ui-task-5387.md):
//
// - /next/suites/<id> and the classic UI's own suite-run URL,
//   /processes/suites/runs/<id>, fell through to the inbox;
// - a schedule save the server refused (every 5 minutes, under its
//   ten-minute floor) left the dialog open with no word of why, the
//   error banner sitting behind the dialog's scrim;
// - a suppressed schedule showed a far-future "Next:" time it will never
//   run at.
test.use({ demoArgs: ["-schedules"] });

test("Processes deep links land on the suite, schedule and suite run they name", async ({
  page,
}) => {
  await page.goto("/next/suites/suite-1");
  await expect(page).toHaveURL(/\/next\/processes\/suites\/suite-1$/);
  await expect(page.getByTestId("suite-row-suite-1")).toHaveAttribute(
    "aria-current",
    "true",
  );

  await page.goto("/next/schedules/sched-2");
  await expect(page).toHaveURL(/\/next\/processes\/schedules\/sched-2$/);
  await expect(page.getByTestId("sched-row-sched-2")).toHaveAttribute(
    "aria-current",
    "true",
  );

  // Start a run through the UI, then open it by its URL.
  await page.goto("/next/processes/suites/suite-1");
  await page.getByTestId("proc-run-suite-btn").click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel(/Base branch/).fill("main");
  const started = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/suite-runs") && r.request().method() === "POST",
  );
  await page.getByTestId("run-suite-submit").click();
  const run = await (await started).json();
  expect(run.id).toBeGreaterThan(0);

  await page.goto(`/next/processes/suites/runs/${run.id}`);
  await expect(page).toHaveURL(
    new RegExp(`/next/processes/suites/runs/${run.id}$`),
  );
  await expect(page.getByTestId("proc-list-pane")).toBeVisible();
  await expect(page.locator(".inbox-split")).toContainText(`Run #${run.id}`);
});

test("a refused schedule save says why inside the dialog", async ({ page }) => {
  await page.goto("/next/processes/schedules");
  await page.getByTestId("proc-new-schedule-btn").click();
  const dialog = page.getByRole("dialog");
  await page.getByTestId("sched-title-input").fill("e2e: too often");
  await page.getByTestId("sched-desc-input").fill("Throwaway.");
  await page.getByTestId("sched-repo-input").fill("acme/widgets");
  await page.getByTestId("sched-kind-everyNMinutes").click();
  const minutes = dialog.getByLabel("Every N minutes");
  // The box's own floor is the server's: ten minutes.
  await expect(minutes).toHaveAttribute("min", "10");
  await minutes.fill("5");
  await page.getByTestId("sched-save-btn").click();

  const error = dialog.getByTestId("proc-dialog-error");
  await expect(error).toContainText("at least 10");
  await expect(error).toBeInViewport();
  // The page's banner does not repeat it behind the scrim.
  await expect(page.getByTestId("proc-action-error")).toHaveCount(0);

  // Closing and reopening the dialog starts it clean.
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await page.getByTestId("proc-new-schedule-btn").click();
  await expect(page.getByTestId("proc-dialog-error")).toHaveCount(0);
});

test("a suppressed schedule says so rather than naming a next run", async ({
  page,
}) => {
  const res = await page.request.post("/api/schedules", {
    data: {
      title: "e2e: off-target ref watch",
      description: "Watches a repository off the target list.",
      repo: "acme/widgets",
      base: "main",
      recurrence: {
        kind: "git_ref",
        gitRef: { repo: "offlist/upstream", ref: "refs/heads/main" },
      },
    },
  });
  expect(res.ok()).toBeTruthy();
  const sched = await res.json();
  try {
    await page.goto(`/next/processes/schedules/${sched.id}`);
    const row = page.getByTestId(`sched-row-${sched.id}`);
    await expect(row).toContainText("Suppressed");
    await expect(row).toHaveAttribute("aria-current", "true");
    await expect(page.getByTestId("sched-next-run")).toHaveText(
      "Next: suppressed",
    );
  } finally {
    await page.request.delete(`/api/schedules/${sched.id}`);
  }
});
